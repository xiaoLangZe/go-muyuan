# go-muyuan

[![Go Reference](https://pkg.go.dev/badge/github.com/xiaoLangZe/go-muyuan.svg)](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan)
[![Go Version](https://img.shields.io/badge/go-1.26-blue.svg)](https://go.dev/)

**English** | [中文](zh-CN/README.md)

📖 **[Documentation](https://xiaoLangZe.github.io/go-muyuan/)** ·
📦 [pkg.go.dev](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan) ·
🐙 [GitHub](https://github.com/xiaoLangZe/go-muyuan)

An high-speed downloader as a **reusable Go library**. It splits a file
into byte-range *segments* and downloads them over a pool of *connections*, and it
can be reconfigured, paused, resumed, restarted, and cache-cleared **while
running** via control signals. A task queue downloads many files with bounded
*concurrency*.

This is a package meant to be imported by other programs, not a CLI tool.

## Terminology

Three independent dimensions. The names matter, so here is the vocabulary this
library uses — and what is *not* a good name for each:

| Concept | This library | Why not the obvious name |
| --- | --- | --- |
| Files downloaded simultaneously | **`Concurrency`** (on `QueueConfig`) | not a "thread count" — these are independent file downloads |
| Simultaneous transfers **within one file** | **`Connections`** (on `Config`) | each one is one HTTP connection, so "threads" is misleading: nothing is threaded, and it reads as if one file were multi-threaded |
| Byte-range pieces the file is cut into | **`Segments`** (on `Config`) | distinct from `Connections`: you can cut a file into 32 segments and fetch only 8 at a time |

**`Connections` and `Segments` are separate knobs.** `Segments` is the *plan*
(how finely the byte range is divided, which also sets resume granularity);
`Connections` is the *parallelism* (how many segments are in flight at once).
Effective parallelism is `min(Connections, unfinished segments)`.

A common point of confusion: a "worker goroutine" in the implementation is one
**connection**, not one thread per segment forever. A connection claims a
segment, finishes it, and claims the next — which is why `Connections > Segments`
is pointless (there is nothing left to claim).

## Features

- **Multi-connection range downloads** — splits a file into N segments fetched over T concurrent connections. Segments and connections are independent: 32 segments / 8 connections is valid.
- **Workers total-budget mode** — set `Workers` once and the library auto-allocates `Connections` (parallel HTTP streams) and `Segments` (byte-range pieces) whose sum stays within the budget. Recommended for highest performance: the downloader decides the split so every connection always has a segment to claim. The classic per-knob mode (`Connections` + `Segments` set separately) is fully retained.
- **Adaptive slow-segment splitting** — while running, the manager samples each in-flight segment's rate; a segment clearly slower than the median is split in two so a faster worker can claim the back half. No bytes move (single partial file, absolute offsets); it is a metadata-only operation that keeps the fastest workers busy.
- **Batch download queue** — download many files with bounded concurrency (`Queue`), where "files at once" and "connections per file" are separate knobs. Tasks can be added while running, and controlled individually or as a group.
- **Three configuration modes** — set connections only, segments only, or both. Plus the Workers total-budget mode. The same modes are available at runtime.
- **Live reconfiguration** — change workers, connections, segments, or both on a running download; already-downloaded bytes are preserved (no data movement, no re-download of completed regions).
- **Pause / Resume / Restart / Clear cache** — full lifecycle control, driven either by a typed signal channel or convenience methods.
- **Resumable across processes** — progress is persisted next to the output and validated against the server's `Content-Length`, `ETag`, and `Last-Modified`, so an interrupted download continues where it left off.
- **Graceful fallback** — servers without `Accept-Ranges` are downloaded single-stream instead of failing.
- **Retries** — transient failures are retried with backoff, while permanent ones (404, 403) fail fast instead of waiting out the backoff budget. Queues retry whole tasks too.
- **SSRF guard** — http/https only, with localhost/loopback/private/reserved addresses rejected at both validation and dial time (defeats DNS rebinding). Opt out with `AllowPrivateHost` for LAN downloads.
- **Progress reporting** — periodic snapshots with speed (EWMA) and ETA, via callbacks or polling, per download and aggregated per queue.

## Install

Requires **Go 1.26 or newer** (the `go` directive in `go.mod`). An older
toolchain reports `go.mod requires go >= 1.26`.

Once the repository is published to GitHub:

```bash
go get github.com/xiaoLangZe/go-muyuan
```

**Until then**, `go get` fails with `remote: Repository not found` because the
remote does not exist yet. To use the package from a local checkout, point a
`replace` directive at the source directory instead — this is exactly how the
package's own tests exercise an external consumer:

```text
// go.mod of your project
require github.com/xiaoLangZe/go-muyuan v0.0.0

replace github.com/xiaoLangZe/go-muyuan => /absolute/path/to/go-muyuan
```

Then import it (the package name is `downloader`, which does not match the last
element of the module path, so an alias keeps it obvious):

```go
import downloader "github.com/xiaoLangZe/go-muyuan"
```

## Quick start

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

## Configuration

`Config` fields (see [pkg.go.dev](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan) for the full reference):

| Field | Meaning | Default |
| --- | --- | --- |
| `URL` | Source URL (http/https; host validated) | required |
| `OutputPath` | Final destination path | required |
| `Workers` | Total budget of (connections + segments); 0 = classic per-knob mode | `0` |
| `Connections` | Concurrent HTTP connections fetching this file (auto-allocated when `Workers>0`) | `8` |
| `Segments` | Byte-range pieces the file is split into; `0` = auto | `0` (auto) |
| `PartDir` | Where the partial file + metadata live | dir of `OutputPath` |
| `Headers` | Extra request headers (e.g. `Authorization`) | none |
| `HTTPClient` | Custom `*http.Client` | built from `Proxy` + SSRF guard |
| `Proxy` | Proxy URL (`http`, `https`, `socks5`) | none |
| `OnProgress` | Progress callback | none |
| `MinSegmentSize` | Lower bound for auto segment sizing | `1 MiB` |
| `AllowPrivateHost` | Permit LAN/loopback targets (disables SSRF guard) | `false` |

### The four configuration modes

`Connections` and `Segments` are independent knobs; `Workers` is a total budget that auto-allocates them:

- **workers total-budget** *(recommended for highest performance)* — set `Workers` to a single number; the library allocates `Connections` and `Segments` whose sum stays within `Workers`, and while running it splits slow segments so the fastest workers stay busy.
- **connections only** — leave `Segments` at `0`; the segment count is derived from the connection count and file size.
- **segments only** — set `Segments`; `Connections` stays at its default.
- **both** — set both explicitly (connections above the segment count just idle, since a segment is fetched by one connection at a time).

## Runtime control

Every control action is available two ways: as a typed signal on the channel
from `d.Signals()`, and as a convenience method. They are equivalent.

| Action | Method | Signal |
| --- | --- | --- |
| Pause (keep progress) | `d.Pause()` | `SignalEvent{Type: SigPause}` |
| Resume | `d.Resume()` | `SignalEvent{Type: SigResume}` |
| Set workers total | `d.SetWorkers(n)` | `SignalEvent{Type: SigSetWorkers, Workers: n}` |
| Set connections | `d.SetConnections(n)` | `SignalEvent{Type: SigSetConnections, Connections: n}` |
| Set segments | `d.SetSegments(n)` | `SignalEvent{Type: SigSetSegments, Segments: n}` |
| Set both | `d.SetConnectionsAndSegments(c, s)` | `SignalEvent{Type: SigSetConnectionsAndSegments, ...}` |
| Restart (wipe + re-begin) | `d.Restart()` | `SignalEvent{Type: SigRestart}` |
| Clear cache (delete only) | `d.ClearCache()` | `SignalEvent{Type: SigClearCache}` |
| Stop (keep progress, exit) | `d.Stop()` | `SignalEvent{Type: SigStop}` |
| Abort (exit, `Wait` → `ErrAborted`) | `d.Abort()` | `SignalEvent{Type: SigAbort}` |

```go
d.Signals() <- downloader.SignalEvent{Type: downloader.SigSetConnections, Connections: 16}
```

### Restart vs. Clear cache

- **Restart** deletes the partial file and metadata and *immediately re-begins* the
  download with the current configuration. One signal, fresh download.
- **Clear cache** deletes the partial file and metadata and leaves the downloader
  idle; call `Start` again to begin a fresh transfer. It works whether or not the
  download is running.

## Batch download (task queue)

`Queue` downloads many files with bounded concurrency. Its `Concurrency` limit
(files at once) is a **separate dimension** from each file's own
connections/segments, so "3 files at once, 8 connections each" means up to 24
concurrent connections.

```go
q, err := downloader.NewQueue(downloader.QueueConfig{
	Concurrency: 3,        // files downloaded simultaneously
	OutputDir:   "./downloads",
	MaxRetries:  2,        // per-task retries before giving up
	Template: downloader.Config{
		Connections: 8,    // simultaneous connections per file
		Segments:    16,   // byte-range pieces per file (0 = auto)
	},
	OnTaskStateChange: func(t downloader.TaskInfo) {
		fmt.Printf("%s: %s\n", t.ID, t.State)
	},
})
if err != nil {
	log.Fatal(err)
}

ids, err := q.AddAll([]string{url1, url2, url3}, "") // "" => OutputDir
if err != nil {
	log.Fatal(err)
}

if err := q.Start(context.Background()); err != nil {
	log.Fatal(err)
}
if err := q.Wait(); err != nil {
	// Aggregates per-task failures; inspect with errors.As(*downloader.TaskError).
	log.Fatal(err)
}
fmt.Println(q.Summary())
```

Tasks may be **added while the queue is running**; they are scheduled as slots
free up.

### Queue control

Same dual API as single downloads: `q.Signals() <- QueueSignal{...}` or the
convenience method.

| Action | Method | Signal |
| --- | --- | --- |
| Pause all (keep progress) | `q.Pause()` | `QueueSignal{Type: QSigPause}` |
| Resume all | `q.Resume()` | `QueueSignal{Type: QSigResume}` |
| Set files at once | `q.SetConcurrency(n)` | `QueueSignal{Type: QSigSetConcurrency, Concurrency: n}` |
| Set per-file workers total | `q.SetWorkers(n)` | `QueueSignal{Type: QSigSetWorkers, Workers: n}` |
| Set per-file connections | `q.SetConnections(n)` | `QueueSignal{Type: QSigSetConnections, Connections: n}` |
| Set per-file segments | `q.SetSegments(n)` | `QueueSignal{Type: QSigSetSegments, Segments: n}` |
| Set both per-file knobs | `q.SetConnectionsAndSegments(c, s)` | `QueueSignal{Type: QSigSetConnectionsAndSegments, ...}` |
| Clear all caches | `q.ClearCache()` | `QueueSignal{Type: QSigClearCache}` |
| Stop (keep progress, exit) | `q.Stop()` | `QueueSignal{Type: QSigStop}` |
| Abort | `q.Abort()` | `QueueSignal{Type: QSigAbort}` |

Per-file knobs (`SetWorkers`/`SetConnections`/`SetSegments`) apply both to running tasks and
to tasks started later.

### Per-task control and inspection

| Action | Method |
| --- | --- |
| Pause one task (stays paused across a queue Resume) | `q.PauseTask(id)` |
| Resume one task | `q.ResumeTask(id)` |
| Re-download one task (deletes its output) | `q.RestartTask(id)` |
| Delete one task's partial + metadata | `q.ClearTaskCache(id)` |
| Cancel one task | `q.CancelTask(id)` |
| Snapshot one task / all tasks | `q.Task(id)` / `q.Tasks()` |
| Aggregate snapshot | `q.Summary()` |

### Task states

```text
Pending ──► Running ──► Completed
   ▲          │  │
   │          │  └──► Failed        (retries exhausted)
   │          │
   │          ├──► Paused   ──► Running   (suspend/resume)
   │          │
   └──────────┴──► Canceled
        (retry backoff)
```

- **Paused** covers both a caller pause (`PauseTask`) and a queue suspension
  (`Pause`, or concurrency lowered). A queue `Resume` revives only the latter.
- A queue `Wait` returns once no task can progress. A task paused by the caller
  keeps the queue alive until it is resumed or canceled.

### Concurrency and slots

Suspending a task **releases its slot** while keeping progress on disk (via a
stop-and-resume of the underlying download, not a live pause). This matters for
large batches: a suspended task does not hold an idle connection. Lowering
`Concurrency` below the number of running tasks suspends the most recently
started ones, which resume automatically as slots free up.

## State machine (single download)

```text
        Start
Idle ─────────────► Running ◄────────────┐
                      │  ▲                │ Resume
             Pause    │  └────────────────┤
                      ▼                   │
                   Paused ────────────────┘
                      │
     Stop ────────────┼────────► Stopped   (resumable: Start again)
                      │
              Complete┴────────► Completed
                      │
             error ───┴────────► Failed
```

Reconfiguration signals (`SetWorkers` / `SetConnections` / `SetSegments` /
`SetConnectionsAndSegments`) do not change the state; they cancel the current
connection generation, rebuild the segment plan, and respawn the pool. In
`Workers` total-budget mode the manager also runs an adaptive slow-segment
splitter that splits a lagging segment in two so a faster worker can claim the
back half (see Architecture).

## Progress

```go
p := d.Progress()
// p.State, p.Total, p.Downloaded, p.Percent,
// p.Speed, p.ETA, p.Connections, p.Segments, p.Workers,
// p.AcceptRanges, p.Err
```

`Progress` is a snapshot and is safe to read from any goroutine. For push-style
updates, set `Config.OnProgress`; the callback fires from the manager's ticker
(~2 Hz) and must not block.

## Security note

The package rejects any URL whose host is `localhost`, loopback, private
(RFC 1918), link-local, multicast, unspecified, or otherwise reserved, and it
re-checks the **resolved address at dial time** so a name that passed validation
cannot rebind to an internal address. Set `AllowPrivateHost: true` only when you
intend to download from a trusted LAN or local server.

## Download benchmarks

`go test -bench=. -benchtime=3s -count=3` on a 4 MiB payload over a local
`httptest` server (AMD Ryzen 7 5800H, Windows). `Workers` mode auto-allocates
connections/segments and additionally splits slow segments at runtime.

| Benchmark | Mode | ns/op (median of 3) | vs single-conn |
| --- | --- | --- | --- |
| `BenchmarkDownloadSingleConn` | 1 conn / 1 segment | ~18.7 ms | 1.0× |
| `BenchmarkDownloadMultiConn` | 8 conn / 16 segment | ~11.5 ms | 1.63× |
| `BenchmarkDownloadWorkers` | `Workers=16` (auto) | ~12.3 ms | 1.52× |
| `BenchmarkDownloadWorkersSlowSegment` | `Workers=16` + slow first range | ~65.0 ms | — |

Takeaways: multi-connection and Workers modes both beat single-connection by
~1.5–1.6× on a local loopback link where TCP/congestion isn't the bottleneck;
on a real WAN the gap widens. The Workers mode pays a small overhead for
auto-allocation and adaptive splitting, but it is the only mode that recovers
automatically when one segment is slow — the `SlowSegment` row shows it still
completes (the single-conn baseline on that workload would inherit the full
slowdown).

## Examples

- [`examples/basic`](examples/basic/main.go) — interactive CLI with a live progress bar and commands to pause/resume/reconfigure/restart/clear.
- [`examples/batch`](examples/batch/main.go) — batch downloader using the task queue, with a multi-line status display and per-task commands.
- [`examples/testsrv`](examples/testsrv/main.go) — a local Range-capable file server for trying the downloader end-to-end.

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

## Architecture

See [`doc/ARCHITECTURE.md`](doc/ARCHITECTURE.md) for the design, the
single-partial-file + byte-range metadata scheme, and why live re-slicing is
safe.

## Testing

```bash
go test ./...
```

Tests cover plan building, re-slicing progress preservation, URL/host
validation, metadata round-trips, and end-to-end `httptest` downloads including
pause/resume, live reconfiguration, restart, clear-cache, cross-instance resume,
and abort.

## License

MIT — see [LICENSE](LICENSE).
