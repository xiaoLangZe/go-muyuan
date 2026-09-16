# go-muyuan (木鸢)

[![Go Reference](https://pkg.go.dev/badge/github.com/xiaoLangZe/go-muyuan.svg)](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan)
[![Go Version](https://img.shields.io/badge/go-1.26-blue.svg)](https://go.dev/)

**English** | [中文](zh-CN/README.md)

📖 **[Documentation](https://xiaoLangZe.github.io/go-muyuan/)** ·
📦 [pkg.go.dev](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan) ·
🐙 [GitHub](https://github.com/xiaoLangZe/go-muyuan)

An high-speed downloader as a **reusable Go library**. It splits a file into
byte-range *segments* and downloads them over a pool of *connections*, and it can
be reconfigured, paused, resumed, restarted, and cache-cleared **while running**.
A task queue downloads many files with bounded *concurrency*.

The project's Chinese name is **木鸢** (mùyuān, "wooden kite"); the `muyuan` in the
module path is its pinyin.

This is a package meant to be imported by other programs, not a CLI tool.

## Why it is different

Rather than writing each segment to its own `.partN` file and concatenating at
the end, every segment shares **one preallocated partial file** and writes at its
**absolute offset**. Completion is a `fsync` + `rename` — no merge step — and
changing the segment count is a metadata-only operation that moves no bytes.

## Features

- **Multi-connection range downloads** — N segments over T concurrent connections; the two are independent knobs.
- **Workers total-budget mode** — set one number and the library auto-allocates connections and segments, splitting slow segments while running. Recommended for highest performance.
- **Live reconfiguration** — change workers, connections, or segments on a running download; downloaded bytes are preserved.
- **Batch download queue** — many files with bounded concurrency, controllable individually or as a group.
- **Resumable across processes** — progress persisted and validated against `Content-Length` / `ETag` / `Last-Modified`.
- **Graceful fallback** — servers without `Accept-Ranges` degrade to a single stream instead of failing.
- **Retries** — transient failures back off; permanent ones (404, 403) fail fast.
- **SSRF guard** — http/https only, with private addresses rejected at validation *and* dial time.

## Install

Requires **Go 1.26 or newer**. The current release is **v0.2.0**.

```bash
go get github.com/xiaoLangZe/go-muyuan
```

Pin the release explicitly with `@v0.2.0` if you want a reproducible version.

The package name is `downloader`, which does not match the module path, so an
alias keeps it obvious:

```go
import downloader "github.com/xiaoLangZe/go-muyuan"
```

To hack on the library itself, clone
<https://github.com/xiaoLangZe/go-muyuan.git> and point a `replace` directive at
your checkout:

```text
require github.com/xiaoLangZe/go-muyuan v0.0.0

replace github.com/xiaoLangZe/go-muyuan => /absolute/path/to/go-muyuan
```

## Quick start

```go
d, err := downloader.New(downloader.Config{
	URL:         "https://example.com/big.iso",
	OutputPath:  "./big.iso",
	Connections: 8,  // simultaneous connections for this file
	Segments:    16, // byte-range pieces (0 = auto)
	OnProgress: func(p downloader.Progress) {
		fmt.Printf("\r%.1f%%  %.1f MiB/s", p.Percent, p.Speed/1024/1024)
	},
})
if err != nil {
	log.Fatal(err)
}

if err := d.Start(context.Background()); err != nil {
	log.Fatal(err)
}
if err := d.Wait(); err != nil {
	log.Fatal(err)
}
```

## Documentation

The full user manual lives in **[the wiki](wiki/)** and is published at
<https://xiaoLangZe.github.io/go-muyuan/>. It covers terminology, all
configuration modes, runtime control, the batch queue, progress reporting,
security, and the internal architecture.

- [Introduction](wiki/guide/introduction.md) — core concept and terminology
- [Quick Start](wiki/guide/getting-started.md) — install and first download
- [Configuration](wiki/guide/configuration.md) — all fields and the four modes
- [Runtime Control](wiki/guide/runtime-control.md) — signals and methods
- [Batch Queue](wiki/guide/queue.md) — many files at once
- [Architecture](wiki/guide/architecture.md) — how it works inside

## Examples

- [`examples/basic`](examples/basic/main.go) — interactive single-file CLI with a live progress bar.
- [`examples/batch`](examples/batch/main.go) — batch downloader using the task queue.
- [`examples/testsrv`](examples/testsrv/main.go) — a local Range-capable file server for end-to-end testing.

```bash
# Terminal 1: create a test file, then serve it locally
mkdir -p testdata && dd if=/dev/zero of=testdata/file.bin bs=1M count=50
go run ./examples/testsrv -dir ./testdata -addr 127.0.0.1:18080

# Terminal 2 (allow-private is required for loopback)
go run ./examples/basic -allow-private -o ./file.bin -conn 8 -seg 16 \
    http://127.0.0.1:18080/file.bin
```

## Testing

```bash
go test ./...
```

## License

Apache License 2.0 — see [LICENSE](LICENSE).
