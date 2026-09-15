# Quick Start

## Requirements

Requires **Go 1.26 or newer** (the `go` directive in `go.mod`). An older toolchain reports `go.mod requires go >= 1.26`.

## Install

Requires **Go 1.26 or newer** (the `go` directive in `go.mod`). An older toolchain reports `go.mod requires go >= 1.26`.

```bash
go get github.com/xiaoLangZe/go-muyuan
```

The current release is **v0.1.0**; pin it with `@v0.1.0` for a reproducible version.

The Simplified-Chinese variant is a separate module — append `/zh-CN` to the path
(see [Introduction](./introduction)):

```bash
go get github.com/xiaoLangZe/go-muyuan/zh-CN
```

Because it is a nested module, its releases are tagged with a path prefix: the
v0.1.0 release tag is `zh-CN/v0.1.0`.

Then import it (the package name is `downloader`, which does not match the last element of the module path, so an alias keeps it obvious):

```go
import downloader "github.com/xiaoLangZe/go-muyuan"
```

To hack on the library itself, clone <https://github.com/xiaoLangZe/go-muyuan.git>
and point a `replace` directive at your checkout:

```text
// go.mod of your project
require github.com/xiaoLangZe/go-muyuan v0.0.0

replace github.com/xiaoLangZe/go-muyuan => /absolute/path/to/go-muyuan
```

## Your first download

```go
package main

import (
	"context"
	"fmt"
	"log"

	downloader "github.com/xiaoLangZe/go-muyuan"
)

func main() {
	d, err := downloader.New(downloader.Config{
		URL:         "https://example.com/big.iso",
		OutputPath:  "./big.iso",
		Connections: 8,  // simultaneous connections for this file
		Segments:    16, // byte-range pieces (0 = auto)
		OnProgress: func(p downloader.Progress) {
			fmt.Printf("\r%.1f%%  %.1f MiB/s  (%d conns, %d segs)",
				p.Percent, p.Speed/1024/1024, p.Connections, p.Segments)
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
	fmt.Println("\ndone:", d.Progress().OutputPath)
}
```

## Lifecycle

A typical download goes through four steps:

1. **`New(Config)`** — validates config, creates output and part dirs, SSRF-pre-checks the URL (no real I/O).
2. **`Start(ctx)`** — synchronous probe (`HEAD` + 1-byte `Range` GET), reconciles on-disk resume metadata, preallocates the partial file, builds the segment plan; probe/validation errors return immediately. The manager goroutine takes over after that.
3. **`Wait()`** — blocks until terminal, returning `nil` (success), `ErrAborted`, or the failure error.
4. **`Close()`** — abort and drain, releasing resources (optional).

To observe or intervene mid-flight, poll `d.Progress()`, set `Config.OnProgress`, or use `d.Signals()` / the convenience methods (see [Runtime Control](./runtime-control)).

## On-disk files

For an output path `P`:

| Path | Purpose |
| --- | --- |
| `P` | the final file (created only on success) |
| `P.muyuan.partial` | the in-progress file, preallocated to full size |
| `P.muyuan.meta.json` | JSON progress record |

## Run the examples

Serve a Range-capable source locally:

```bash
# Terminal 1: serve a directory locally
go run ./examples/testsrv -dir ./testdata -addr 127.0.0.1:18080

# Terminal 2: download from it (allow-private is required for loopback)
go run ./examples/basic -allow-private -o ./file.bin -conn 8 -seg 16 \
    http://127.0.0.1:18080/file.bin

# Or download a whole batch at once
go run ./examples/batch -allow-private -o ./downloads -c 3 -conn 8 \
    http://127.0.0.1:18080/a.bin http://127.0.0.1:18080/b.bin
```

- `examples/basic` — interactive CLI with a live progress bar and commands to pause/resume/reconfigure/restart/clear.
- `examples/batch` — batch downloader using the task queue, with a multi-line status display and per-task commands.
- `examples/testsrv` — a local Range-capable file server.

## Next steps

- [Configuration](./configuration)
- [Runtime Control](./runtime-control)
- [Batch Queue](./queue)
