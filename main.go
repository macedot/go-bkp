package main

import (
	"archive/zip"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	kzip "github.com/klauspost/compress/zip"
	"github.com/klauspost/compress/flate"
)

// go-bkp is a clean, modern Go port of backup.sh.
// - Directories → single <name>.<timestamp>.zip using parallel compression (klauspost zip + flate)
// - Single files  → timestamped copy (original behavior)
//
// Supports list of files/folders processed via parallel queue (-q workers, -c cores per job).
// -d for decompress mode (extracts list of .zip files, logs errors per item and continues).
// Pure Go only. No external tools. Standard .zip output (directly usable with unzip/7z etc.).

func main() {
	verbose := flag.Bool("v", false, "verbose output (list files)")

	queueWorkers := flag.Int("q", 1, "number of top-level items (files/folders) to process in parallel from the queue")
	coresPerJob := flag.Int("c", 0, "cores to allocate per job for reading + compression (0 = auto: total/queue)")
	decompress := flag.Bool("d", false, "decompress mode: extract the provided .zip archive files")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] <file|directory> [file|directory] ...\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nIf no targets are provided, this usage message is printed.\n")
		fmt.Fprintf(os.Stderr, "\nIn -d mode, arguments are treated as .zip files to extract (errors per file are logged, processing continues).\n")
	}

	flag.Parse()

	if len(flag.Args()) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	totalCores := runtime.NumCPU()
	if *coresPerJob <= 0 {
		*coresPerJob = totalCores / *queueWorkers
		if *coresPerJob < 1 {
			*coresPerJob = 1
		}
	}

	fmt.Printf("Queue: %d parallel jobs | %d cores per job (total: %d)\n\n",
		*queueWorkers, *coresPerJob, totalCores)

	targets := flag.Args()

	ts := time.Now().Format("20060102150405")

	// Create a cancellable context (useful for future signal handling / graceful shutdown).
	// Note: We intentionally do *not* cancel on per-job failures so that other
	// backups can continue (partial progress is allowed).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create work queue
	jobs := make(chan string, len(targets))
	for _, t := range targets {
		jobs <- t
	}
	close(jobs)

	var wg sync.WaitGroup
	var mu sync.Mutex
	lastError := 0

	for i := 0; i < *queueWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}

				var err error
				if *decompress {
					err = extractZip(target, *verbose)
				} else {
					err = backup(ctx, target, ts, *verbose, *coresPerJob)
				}

				if err != nil {
					mu.Lock()
					lastError = 1
					wrapped := wrapTargetError(target, err)
					fmt.Fprintf(os.Stderr, "Error: %v\n", wrapped)
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()
	os.Exit(lastError)
}

func backup(ctx context.Context, input, ts string, verbose bool, coresPerJob int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	input = strings.TrimRight(input, "/")
	if input == "" {
		input = "."
	}

	info, err := os.Stat(input)
	if os.IsNotExist(err) {
		return fmt.Errorf("not found: %s", input)
	}
	if err != nil {
		return err
	}

	base := filepath.Base(input)
	if base == "." || base == ".." || base == "" {
		return fmt.Errorf("cannot backup '.' or '..': %s", input)
	}
	dir := filepath.Dir(input)

	// Single file → plain timestamped copy
	if !info.IsDir() {
		outPath := base + "." + ts
		if dir != "." && dir != "" {
			outPath = filepath.Join(dir, base+"."+ts)
		}
		fmt.Printf("Backing up: %s (%s) -> %s (copy)\n", input, humanSize(info.Size()), outPath)
		if err := copyFileWithTimestamp(input, outPath, info); err != nil {
			return err
		}
		fmt.Printf("Done: %s\n\n", outPath)
		return nil
	}

	// Directory → single .zip file using parallel compression
	outPath := base + "." + ts + ".zip"
	if dir != "." && dir != "" {
		outPath = filepath.Join(dir, base+"."+ts+".zip")
	}

	orig := calculateSize(input)
	fmt.Printf("Backing up: %s (%s) -> %s\n", input, humanSize(orig), outPath)

	outFile, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer func() {
		outFile.Close()
		if err != nil {
			os.Remove(outPath)
		}
	}()

	zw := kzip.NewWriter(outFile)

	// Register fast deflate compressor
	zw.RegisterCompressor(kzip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestSpeed)
	})

	// Parallel read + compress into zip
	if addErr := addToZip(zw, input, base, verbose, coresPerJob); addErr != nil {
		zw.Close()
		return addErr
	}

	if zerr := zw.Close(); zerr != nil {
		return zerr
	}

	var compSize int64
	if fi, statErr := os.Stat(outPath); statErr == nil {
		compSize = fi.Size()
	}
	fmt.Printf("Done: %s (%s)\n\n", outPath, humanSize(compSize))
	return nil
}

// job and result for parallel zip compression (pre-compress files in workers for parallelism).
type job struct {
	path string
	rel  string
}

type result struct {
	hdr  *kzip.FileHeader
	data []byte
	err  error
}

// addToZip walks the source tree, reads + compresses files in parallel workers (using coresPerJob),
// then writes them sequentially into the zip archive (zip writing is inherently sequential due to central directory).
//
// This provides parallel compression while producing a standard .zip file that can be opened directly
// by unzip, 7z, Windows Explorer, etc. without any special unpacking step.
func addToZip(zw *kzip.Writer, sourcePath, archiveBase string, verbose bool, coresPerJob int) error {
	numWorkers := coresPerJob
	if numWorkers < 1 {
		numWorkers = 1
	}

	jobs := make(chan job, 512)
	results := make(chan result, 64)

	// Limit concurrent in-memory compressed data for memory safety on large dirs.
	maxInFlight := 8
	if numWorkers < maxInFlight {
		maxInFlight = numWorkers
	}
	inFlight := make(chan struct{}, maxInFlight)

	var readerWG sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for j := range jobs {
				inFlight <- struct{}{}

				raw, err := os.ReadFile(j.path)
				if err != nil {
					<-inFlight
					results <- result{err: err}
					continue
				}

				// Pre-compress in parallel for speed (this is the "parallel zip" part)
				var buf bytes.Buffer
				fw, _ := flate.NewWriter(&buf, flate.BestSpeed)
				fw.Write(raw)
				fw.Close()

				hdr := &kzip.FileHeader{
					Name:   j.rel, // direct contents, no extra folder wrapper (the zip filename carries the identity)
					Method: kzip.Deflate,
				}
				hdr.SetModTime(time.Now())

				// Optimization: store instead of compress for already-compressed files
				if shouldStoreRaw(j.path) {
					hdr.Method = kzip.Store
					results <- result{hdr: hdr, data: raw}
					<-inFlight
					continue
				}

				results <- result{hdr: hdr, data: buf.Bytes()}
				<-inFlight
			}
		}()
	}

	// Dispatcher with parallel directory walking
	go func() {
		var walkWG sync.WaitGroup

		var walkDir func(dir string)
		walkDir = func(dir string) {
			defer walkWG.Done()

			ents, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, e := range ents {
				full := filepath.Join(dir, e.Name())
				rel, _ := filepath.Rel(sourcePath, full)
				if e.IsDir() {
					walkWG.Add(1)
					go walkDir(full)
				} else {
					jobs <- job{path: full, rel: rel}
				}
			}
		}

		walkWG.Add(1)
		go walkDir(sourcePath)
		walkWG.Wait()
		close(jobs)
	}()

	go func() {
		readerWG.Wait()
		close(results)
	}()

	// Write sequentially to zip (required by format)
	count := 0
	for r := range results {
		if r.err != nil {
			return r.err
		}

		w, err := zw.CreateHeader(r.hdr)
		if err != nil {
			return err
		}
		if _, err := w.Write(r.data); err != nil {
			return err
		}

		if verbose {
			fmt.Println(r.hdr.Name)
		}
		count++
	}

	return nil
}

func calculateSize(path string) int64 {
	var total int64
	filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func humanSize(n int64) string {
	if n == 0 {
		return "0B"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}

func copyFileWithTimestamp(src, dst string, info os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	os.Chtimes(dst, info.ModTime(), info.ModTime())
	return nil
}

// extractZip extracts a standard .zip archive to the current directory.
// It logs errors for individual files but continues to the next file in the archive.
func extractZip(archivePath string, verbose bool) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open zip %s: %w", archivePath, err)
	}
	defer r.Close()

	for _, f := range r.File {
		if err := extractZipFile(f, verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Error extracting %s from %s: %v\n", f.Name, archivePath, err)
			// continue to next file
		}
	}
	return nil
}

func extractZipFile(f *zip.File, verbose bool) error {
	// Prevent zip slip
	if strings.Contains(f.Name, "..") {
		return fmt.Errorf("refusing to extract path with .. : %s", f.Name)
	}

	if verbose {
		fmt.Println(f.Name)
	}

	// Create directories if needed
	if f.FileInfo().IsDir() {
		return os.MkdirAll(f.Name, f.FileInfo().Mode())
	}

	if err := os.MkdirAll(filepath.Dir(f.Name), 0755); err != nil {
		return err
	}

	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(f.Name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.FileInfo().Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}

// shouldStoreRaw returns true for file types that are typically already compressed
// (PDFs, images, archives, etc.). For these, we use ZIP Store method instead of Deflate
// for maximum speed and often better or equal size.
func shouldStoreRaw(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf", ".jpg", ".jpeg", ".png", ".gif", ".zip", ".gz", ".bz2", ".xz", ".7z", ".rar",
		".mp3", ".mp4", ".avi", ".mov", ".mkv", ".webm", ".flac", ".ogg", ".docx", ".xlsx", ".pptx":
		return true
	}
	// Very small files: not worth compressing
	if info, err := os.Stat(path); err == nil && info.Size() < 4096 {
		return true
	}
	return false
}

// wrapTargetError wraps an error with the original queue entry name so that
// errors are easy to correlate back to the input list.
func wrapTargetError(target string, err error) error {
	return fmt.Errorf("backup %q: %w", target, err)
}
