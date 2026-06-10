# go-bkp — High-Performance Parallel Backup Tool

[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A fast, pure-Go backup utility designed for large numbers of files and directories. It produces standard `.tar.zst` archives while making efficient use of multiple CPU cores through explicit control over queue parallelism and per-job concurrency.

This is the project root for `go-bkp`.

## Features

- **Queue-based parallelism**: Process multiple files/folders concurrently using `-q`
- **Per-job core control**: Fine-grained CPU allocation per backup job with `-c`
- **Single output file**: Each directory becomes one `<name>.<timestamp>.tar.zst`
- **Single files**: Simple timestamped copies (preserves original `cp -p` behavior)
- **Pure Go + high-performance zstd**: No external tools or `exec` calls
- **Parallel I/O + compression**: Concurrent directory walking, file reading, and zstd encoding
- **Standard output**: Fully compatible `.tar.zst` files

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

If no paths are provided, it falls back to a small default set for development convenience.

## Architecture

```
Targets (files + folders)
        ↓
   Work Queue (size = -q)
        ↓ (parallel workers)
   Per-Job Worker
        ├── Parallel directory walking
        ├── Parallel file reading (bounded by -c)
        ├── Parallel zstd compression (klauspost)
        └── Streaming tar writer → <name>.<timestamp>.tar.zst
```

Each backup job gets its own slice of CPU cores, allowing safe concurrent backups without oversubscribing the machine.

## Output Format

### Directories

```
MyProject.20260601123456.tar.zst
```

Extract with:

```bash
unzstd < MyProject.20260601123456.tar.zst | tar -xf -
# or
tar --zstd -xf MyProject.20260601123456.tar.zst
```

The archive contains the folder contents directly (no extra wrapper directory on extraction).

### Single Files

```
report.csv.20260601123456
```

A plain timestamped copy of the original file.

## Building

```bash
# Local development
go build -o go-bkp .

# Linux amd64 static binary (recommended for servers)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o go-bkp-linux-amd64 .
```

## Performance Characteristics

- Excellent on workloads with many small-to-medium files (common in data exports and documentation trees).
- `tar + zstd` streaming + parallel file reading provides strong throughput.
- Memory usage is kept reasonable through bounded in-flight work and early detection of incompressible files.
- Fine-grained control via `-q` and `-c` allows tuning for different hardware and workloads.

## Project Structure

```
.
├── main.go          # CLI, queue logic, tar + zstd pipeline
├── go.mod
├── go.sum
├── backup.sh        # Original shell implementation (reference)
├── go-bkp           # Built binary
└── README.md
```

## License

MIT License

---

Built as a practical exercise in high-performance Go I/O, structured concurrency, and useful systems tooling. Feedback welcome.