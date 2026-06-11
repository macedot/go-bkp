# go-bkp — High-Performance Parallel Backup Tool

[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A fast, pure-Go backup utility designed for large numbers of files and directories. It produces standard `.zip` archives (directly usable with `unzip`, 7z, Explorer, etc.) while making efficient use of multiple CPU cores through explicit control over queue parallelism (`-q`) and per-job concurrency (`-c`).

This is the project root for `go-bkp`.

## Features

- **Queue-based parallelism**: Process multiple files/folders concurrently using `-q`
- **Per-job core control**: Fine-grained CPU allocation per backup job with `-c`
- **Single output file**: Each directory becomes one `<name>.<timestamp>.zip`
- **Single files**: Simple timestamped copies (preserves original `cp -p` behavior, mtime + mode)
- **Pure Go, no external tools**: No `exec`, no pigz/zstd/tar dependencies. Uses `klauspost/compress/zip` + `flate.BestSpeed` (fastest practical profile) + Store for already-compressed/small files.
- **Parallel I/O + compression**: Concurrent directory walking, file reading, and pre-compression; sequential write into standard zip (required by format).
- **Standard output**: Fully compatible `.zip` files. `-d` extracts them (with per-entry error continue and basic zip-slip guards).

## Quick Start

```bash
# Build
go build -o go-bkp .

# Backup a single directory
./go-bkp /path/to/my-folder

# Backup multiple targets in parallel (4 jobs, 3 cores each)
./go-bkp -q 4 -c 3 /data1 /data2 /logs /project

# Verbose mode
./go-bkp -v -q 2 ./important-folder
```

## Usage

```bash
./go-bkp [flags] [path...]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-q` | 1 | Number of top-level items to process in parallel |
| `-c` | 0 (auto) | Cores to give each job (0 = total cores ÷ queue workers) |
| `-v` | false | Verbose mode — list every file being archived |
| `--version` | - | Print the version (embedded at build time from release tag or dev-<sha>) and exit |

If no paths are provided, usage is printed and the process exits with code 2 (no silent defaults).

## Architecture

```
Targets (files + folders)
        ↓
   Work Queue (size = -q)
        ↓ (parallel workers)
   Per-Job Worker
        ├── Parallel directory walking (unbounded goroutines per subdir — see limitations)
        ├── Parallel file reading + pre-compress (bounded in-flight + -c cores)
        ├── Heuristic Store vs Deflate (BestSpeed) for speed
        └── Sequential zip writer → <name>.<timestamp>.zip  (zip format requires central directory at end)
```

Each backup job gets its own slice of CPU cores (`-c`), allowing safe concurrent backups without oversubscribing the machine. All output is a **standard .zip** (no custom format).

## Output Format

### Directories

```
MyProject.20260601123456.zip
```

Extract with any standard tool:

```bash
unzip MyProject.20260601123456.zip
# or 7z, Windows Explorer, macOS Archive Utility, etc.
```

The archive contains the folder contents directly under the root of the zip (the zip filename carries the identity; no extra wrapper directory on extraction).

### Single Files

```
report.csv.20260601123456
```

A plain timestamped copy of the original file (mtime and mode are preserved via `os.Chtimes` + `OpenFile` with the source mode). No .zip is produced for plain files.

## Security & Trust

- **`-d` (extract) on untrusted archives**: The tool refuses absolute paths and `..` traversals (using `filepath.IsAbs` + `Clean` + prefix checks, plus a post-join escape guard when an explicit dest root is used). However, **you are still responsible** for the content and the resource usage of archives you ask it to extract. Zip bombs (high compression ratio) or huge entries can consume significant memory/CPU because the current design reads + pre-compresses files for parallelism.
- **Backup mode** only reads files you point it at and writes sibling archives/copies next to the sources (or in the same directory for single files). It follows symlinks (contents are included; this matches common "I want everything" backup expectations but is not a `--no-dereference` tar clone).
- **No setuid, no chown, no special files**: Zip members do not carry Unix owners/groups or device nodes in this implementation. Extracted permissions are best-effort (the mode bits from the zip entry are applied).
- **Error handling**: Per-target and per-entry errors are logged to STDERR and processing continues for the rest of the queue / archive (partial progress is intentional).
- **Recommendations**: Run with least privilege. Review archives before `-d` extraction into sensitive directories. For very large or untrusted data sets, monitor memory.

## Building

```bash
# Local development
go build -o go-bkp .

# Linux amd64 static binary (recommended for servers)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o go-bkp-linux-amd64 .
```

## Performance Characteristics

- Excellent on workloads with many small-to-medium files (common in data exports and documentation trees).
- Parallel pre-read + pre-compress (BestSpeed or Store) + sequential zip write gives strong wall-clock throughput on multi-core machines.
- Memory usage is bounded by `inFlight` (default ~min(8, workers)) compressed buffers at a time; very large individual files will still allocate their full content + compressed form.
- Fine-grained control via `-q` and `-c` allows tuning for different hardware and workloads.
- Already-compressed files (pdf, jpg, zip, mp3, office docs, etc.) and files < 4 KiB are stored uncompressed for maximum speed.

## Project Structure

```
.
├── main.go          # CLI, queue logic, zip + flate pipeline, extract with slip guards
├── backup_test.go   # Unit + roundtrip + security (zip-slip) tests
├── go.mod / go.sum
├── backup.sh        # Original shell implementation (reference / regression aid)
├── .github/workflows/release.yml  # Multi-arch build, validation, smoke, Homebrew+Scoop
├── LICENSE
└── README.md
```

Binaries (`go-bkp*`) and temporary files are gitignored.

## License

MIT License

---

Built as a practical exercise in high-performance Go I/O, structured concurrency, and useful systems tooling. Feedback welcome.