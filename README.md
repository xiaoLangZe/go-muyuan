# go-muyuan

An HTTP download library for Go. One downloader manages many files over a shared
connection budget; a single file can be fetched over several connections at once.
Every file is a task with a handle that can be **paused**, **resumed**,
**restarted** and **cancelled/deleted**, and progress can be watched live through
an event stream or by polling.

[中文文档](README.zh-CN.md)

---

## Table of contents

- [Features](#features)
- [Project layout](#project-layout)
- [Install](#install)
- [Quick start](#quick-start)
- [Concepts](#concepts)
- [How connections are allocated](#how-connections-are-allocated)
- [Pieces and block sizing](#pieces-and-block-sizing)
- [Proxy](#proxy)
- [Live progress](#live-progress)
- [API reference](#api-reference)
  - [Manager options](#manager-options)
  - [Task options](#task-options)
  - [Manager methods](#manager-methods)
  - [Task methods](#task-methods)
  - [Events](#events)
  - [Types](#types)
  - [Errors](#errors)
- [Behaviour details](#behaviour-details)
- [Security model](#security-model)
- [Recipes](#recipes)
- [Known limits and trade-offs](#known-limits-and-trade-offs)
- [Tests](#tests)

---

## Features

- **Several connections per file.** The file is split into pieces that are
  fetched in parallel, so one slow connection does not cap the transfer.
- **A shared connection budget across files.** The manager divides one pool of
  connections between the running files, breadth first (see
  [below](#how-connections-are-allocated)).
- **Automatic block sizing.** The length of one piece is steered towards a target
  duration, so a fast server gets long requests and a slow one short requests.
- **Live progress without polling.** An event stream carries throttled progress,
  every state change, and the error a task ended with.
- **Proxy support.** `http`, `https` and `socks5`, with or without credentials,
  applied per manager or per task. No third-party dependencies.
- **Resumable.** Pausing and resuming continues from what is already on disk. A
  completion bitmap means it does not depend on the server answering `If-Range`.
- **No half-finished files.** Bytes go to a `.part` file that is flushed, synced
  and renamed only when the transfer is complete.
- **Transient failures are retried**, with backoff, and a stalled connection is
  detected and retried.
- **Internal addresses are refused** before the request is built and again when
  the connection is dialled.
- **Zero third-party dependencies.** Standard library only.

## Project layout

```
go-muyuan/
├── go.mod
├── README.md
├── README.zh-CN.md
├── .gitignore
├── *.go                     the public package: import "github.com/xiaoLangZe/go-muyuan"
└── internal/                not importable from outside the module
    ├── engine/              the transfer engine: one lifecycle for both the
    │                        single-connection and the parallel path
    ├── filename/            deriving and sanitising file names
    └── hostguard/           refusing loopback, private and reserved addresses
```

Everything a caller needs is in the root package. The `internal`
packages are implementation details and cannot be imported by other projects.

## Install

```
go get github.com/xiaoLangZe/go-muyuan
```

Then:

```go
import "github.com/xiaoLangZe/go-muyuan"
```

Before the repository is published, point at a local checkout from the
consuming module's `go.mod` with a `replace` directive:

```
require github.com/xiaoLangZe/go-muyuan v0.0.0

replace github.com/xiaoLangZe/go-muyuan => ../go-muyuan
```

## Quick start

```go
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/xiaoLangZe/go-muyuan"
)

func main() {
	m := muyuan.New(
		muyuan.WithThreads(16),           // connections shared by all tasks
		muyuan.WithMaxFiles(3),           // files downloading at the same time
		muyuan.WithDefaultDir("downloads"),
	)
	defer m.Close()

	a, err := m.AddTask("downloads", "https://example.com/a.iso")
	if err != nil {
		log.Fatal(err)
	}

	b, err := m.AddTask("downloads", "https://example.com/b.iso",
		muyuan.WithFileName("renamed.iso"),           // task-level override
		muyuan.WithProxy("socks5://127.0.0.1:1080"),  // this task goes through a proxy
		muyuan.WithHeader("Authorization", "Bearer ..."))
	if err != nil {
		log.Fatal(err)
	}

	// Watch one task live.
	go func() {
		for ev := range a.Events() {
			fmt.Printf("[%s] %s %.1f%% over %d connections\n",
				ev.TaskID[:6], ev.Status, ev.Progress.Percent, ev.Progress.Workers)
		}
	}()

	// Or poll the other one whenever you like.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		p := b.Progress()
		fmt.Printf("b: %.1f%% (%d/%d bytes, %d B/s)\n", p.Percent, p.Downloaded, p.Total, p.Speed)
		if b.Status() == muyuan.StatusCompleted {
			break
		}
	}

	a.Pause()   // its connections go to the other files at once
	a.Resume()  // and it takes them back as they free up
	a.Restart() // from zero
	a.Delete()  // stops it and removes its files

	if err := a.Wait(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("saved to", a.FilePath())
}
```

`AddTask(destDir, rawURL string, opts ...TaskOption)` takes the destination
directory first. Its resolution has three branches: an absolute `destDir` is
used as-is; a relative `destDir` is joined onto the root directory; an empty
`destDir` falls back to `WithDefaultDir`, which follows the same rules (and
finally the root itself if that is also empty). The root defaults to the
directory of the running executable, so with no options set, downloads land next
to the exe.

## Concepts

**Manager** — owns the connection budget, the file limit, and the scheduler. It
is created by `New` and is safe for concurrent use.

**Task** — one file. Created by `AddTask`, which returns immediately; the task is
queued and starts when the budget has room. The task is the handle: everything
you do to a download goes through it.

**Connection budget** — the total number of HTTP connections the manager may have
in flight across all tasks. Set with `WithThreads`, adjustable at runtime with
`SetThreads`.

**Piece (block)** — the byte range one connection fetches in one request. Its
length is chosen automatically and adjusted while the transfer runs.

**Granule** — the completion-tracking resolution, 64 KiB. A bitmap of granules
records what is on disk. Progress counts completed granules, which is why pausing
never needs to compensate for a partially written piece.

## How connections are allocated

The scheduler recomputes every allocation from scratch on every change, so the
rules live in one place. There are two, applied in order:

1. **Spread first.** Every file that can run gets one connection, up to
   `WithMaxFiles` files and never more than the total budget.
2. **Then deepen.** Whatever is left is dealt out across those files, round by
   round. The round robin caps how many connections any single file can take, so
   one file never consumes the whole budget.

| Budget | Files | Result |
| --- | --- | --- |
| 2 | 3 | files 1 and 2 get 1 each; file 3 stays queued |
| 6 | 2 | each file gets 3 |
| 4 | 4 | each file gets 1 |
| 6 | 4 | two files get 2, two get 1 |

Consequences worth knowing:

- **A task with 1 connection streams the whole file** without range requests.
  Splitting a file over a single connection costs more than it saves. Only a
  budget of 2 or more, against a server that serves ranges, will split.
- **Pausing a file frees its connections immediately.** The next scheduling pass
  hands them to the other files. `Resume` puts the task back in line.
- **`WithMaxFiles` limits starts.** Raising it starts queued tasks. Lowering it
  below the number of running files pauses the ones that no longer fit; they keep
  the bytes already on disk and continue when the limit allows it again.
- **A task waiting for its turn reports `StatusQueued`**, whether it has never
  started or had its connections taken away. It is waiting, not stopped by you.

## Pieces and block sizing

The engine does not pre-slice the file into fixed chunks. Pre-slicing would fix
the connection count and the piece boundaries at the start, which is exactly what
has to stay flexible for [dynamic allocation](#how-connections-are-allocated).

Instead there are two levels:

- **Granule** — 64 KiB, the resolution of the completion bitmap. A 10 GB file
  needs a 20 KB bitmap.
- **Block** — what one worker claims in one go. It is a whole number of granules,
  chosen at claim time, so it can change at any moment without disturbing the
  bitmap.

A worker loops: claim the next free run of granules → send one ranged request →
write at the offset → mark the granules complete. A claim that is abandoned (a
pause, a failed request) simply clears its bits, so the range is fetched again
later.

**How the block length is chosen:**

- The first block is `file size / (connections × 4)`, clamped to
  `[256 KiB, 16 MiB]`.
- While the transfer runs, the measured duration of each piece steers the length
  towards a target of about **1.5 s per piece**. A step moves by at most a factor
  of two, and the result stays inside the configured bounds, so one unusual
  response cannot collapse or explode the block.
- Near the end, the remaining bytes are split between the workers so they finish
  together instead of leaving one worker with the whole tail.

Use `WithChunkSize` to pin the length (which disables the adaptation), and
`WithChunkBounds` to change the bounds.

## Proxy

```go
muyuan.WithDefaultProxy("http://user:pass@proxy.example:8080") // every task
muyuan.WithProxy("socks5://user:pass@proxy.example:1080")      // one task
```

Supported schemes are `http`, `https` and `socks5`; credentials go in the URL's
userinfo. SOCKS5 is handled by the standard library's HTTP transport, so the
module still has no third-party dependencies. In the parallel path every
connection goes through the proxy; nothing extra is needed. Any other scheme is
rejected by `AddTask` with `ErrInvalidProxy`.

**What a proxy means for the host guard** — this part matters:

- **The pre-request target check still runs.** The target URL's host is resolved
  and checked against the blocked ranges, loopback, private, link-local and
  reserved included. A proxy does not turn that off.
- **The dial-time check sees the proxy's address.** The connection is dialled to
  the proxy, so a proxy inside the local network needs
  `WithAllowUnsafeHosts(true)` (`WithDefaultAllowUnsafeHosts` at manager level).
  That switch is all-or-nothing: it turns the target check off as well.
- **The target's resolved address cannot be checked when a proxy is used.** The
  proxy resolves the name, so the client never sees the target's IP. This is a
  property of the protocol, not a missing feature. The pre-request check is what
  remains.
- **`WithHTTPClient` wins.** A caller-supplied client owns its transport, so
  `WithProxy` has no effect on it.

## Live progress

Two ways, usable at the same time and reading the same values.

### Event stream

```go
for ev := range task.Events() {
	switch ev.Type {
	case muyuan.EventProgress:
		// ev.Progress: bytes, speed, ETA, percent, connections, block size
	case muyuan.EventStatus:
		// ev.Status: queued / downloading / paused / completed / failed / deleted
	case muyuan.EventError:
		// ev.Err: why the task ended in failure
	}
}
```

- The channel is created by the first call to `Events()` and **closed once the
  task ends**, after the event reporting the ending has been delivered, so a
  `range` loop runs from start to finish.
- **Progress events are throttled to at most one per 200 ms.** Every state change
  is delivered, unthrottled.
- **A consumer that stops reading does not hold up the download.** The transfer
  runs on its own goroutine; only the emitter waits on the channel, and it is
  released when the task is deleted or the manager is closed. A caller dropping a
  subscription should `Delete` the task or `Close` the manager rather than leave
  it running.
- **A task that finishes faster than the throttle produces no progress events** —
  only the status events, whose `Progress` is still complete. On a fast local
  server this is the normal case.

### Polling

`Progress()`, `Info()` and `Status()` can be called at any time; the values are
consistent with what an event would carry.

```go
p := task.Progress()
fmt.Println(p.Downloaded, p.Total, p.Percent, p.Speed, p.ETA, p.Workers)
```

`Info()` is a superset: identity, paths, status, range mode, error, and the same
statistics.

## API reference

### Manager options

Passed to `muyuan.New`. They set the defaults every task starts from.

| Option | Default | Meaning |
| --- | --- | --- |
| `WithThreads(n int)` | 4 | Connections in flight across all tasks. |
| `WithMaxFiles(n int)` | 2 | Files downloading at the same time. |
| `WithRootDir(dir string)` | exe directory | Root that relative download paths resolve against. A relative value is joined onto the exe directory; an absolute value is used as-is. |
| `WithDefaultDir(dir string)` | root directory | Default task directory, resolved against the root with the same rules as `destDir`. |
| `WithDefaultProxy(rawURL string)` | none | Proxy for every task. |
| `WithDefaultHeader(key, value string)` | none | Request header for every task. Repeatable. |
| `WithDefaultHeaders(map[string]string)` | none | Several request headers at once. |
| `WithDefaultRetries(n int)` | 3 | Further attempts after a failed transfer. |
| `WithDefaultRetryDelay(d time.Duration)` | 1s | Wait before the first retry; doubles per attempt up to 30 s. |
| `WithDefaultResponseTimeout(d time.Duration)` | 30s | Bound on waiting for response headers. |
| `WithDefaultStallTimeout(d time.Duration)` | 60s | Give up when no byte arrives for this long. Zero disables. |
| `WithDefaultBufferSize(n int)` | 64 KiB | Copy buffer of the single-connection path. |
| `WithDefaultChunkSize(n int64)` | auto | Pin the piece length, disabling adaptation. |
| `WithDefaultChunkBounds(min, max int64)` | 256 KiB / 16 MiB | Bounds for an automatic piece length. |
| `WithDefaultHTTPClient(c *http.Client)` | built-in | Client used by every task. |
| `WithDefaultAllowUnsafeHosts(allow bool)` | false | Permit local and private network targets. |
| `WithDefaultProgress(f func(Progress))` | none | Progress callback for every task. |

### Task options

Passed to `AddTask`. They override the manager's defaults **for that task only**.

| Option | Default | Meaning |
| --- | --- | --- |
| `WithFileName(name string)` | from the server or the URL | File name to write. |
| `WithOverwrite(bool)` | false | Replace an existing file instead of picking a free name. |
| `WithHeader(key, value string)` | none | Request header for this task. Repeatable. |
| `WithHeaders(map[string]string)` | none | Several request headers at once. |
| `WithRetries(n int)` | manager default | Further attempts after a failed transfer. |
| `WithRetryDelay(d time.Duration)` | manager default | Wait before the first retry. |
| `WithResponseTimeout(d time.Duration)` | manager default | Bound on waiting for response headers. |
| `WithStallTimeout(d time.Duration)` | manager default | Give up when no byte arrives for this long. |
| `WithBufferSize(n int)` | manager default | Copy buffer of the single-connection path. |
| `WithChunkSize(n int64)` | manager default | Pin the piece length. |
| `WithChunkBounds(min, max int64)` | manager default | Bounds for an automatic piece length. |
| `WithHTTPClient(c *http.Client)` | manager default | Client used by this task. |
| `WithAllowUnsafeHosts(bool)` | manager default | Permit local and private network targets. |
| `WithProxy(rawURL string)` | manager default | Proxy for this task. |
| `WithProgress(f func(Progress))` | manager default | Progress callback for this task. |

There is deliberately **no task-level connection count.** The scheduler assigns
connections and rewrites the number on every pass, so a per-task value would be
overwritten. Use the manager's `WithThreads` to size the budget.

The two sets are named differently on purpose: `WithDefault*` applies to every
task, the bare names apply to one. Go does not allow two functions with the same
name in one package, so the prefix is what distinguishes them.

### Manager methods

| Method | Meaning |
| --- | --- |
| `AddTask(destDir, rawURL string, opts ...TaskOption) (*Task, error)` | Queue a download. Validates the URL and the proxy immediately. |
| `Threads() int` / `SetThreads(n int)` | Read or change the connection budget. |
| `MaxFiles() int` / `SetMaxFiles(n int)` | Read or change the file limit. |
| `Tasks() []*Task` | Snapshot of every task, in the order they were added. |
| `Task(id string) *Task` | Look one up by identifier, or nil. |
| `Wait()` | Block until every task is terminal. |
| `Close()` | Stop the scheduler and every task. Running transfers are cancelled; partial files stay on disk. Idempotent. |

### Task methods

| Method | Meaning |
| --- | --- |
| `Pause() error` | Stop the transfer, keep the bytes, hand the connections over. Pausing a paused task returns nil. |
| `Resume() error` | Put the task back in line for connections. |
| `Restart() error` | Discard the bytes and fetch from zero. Works from any state, including completed or deleted. |
| `Delete() error` | Stop the transfer and remove the finished and partial files. **This is also how a task is cancelled** — the two differ only in whether files are kept. |
| `Events() <-chan Event` | Subscribe to the event stream. Closes when the task ends. |
| `Wait() error` | Block until terminal; a queued or paused task is not terminal. |
| `WaitContext(ctx) error` | `Wait` with a deadline. |
| `Done() <-chan struct{}` | Closed when the task is terminal. A restarted task gets a new channel. |
| `Status() Status` | Current state. |
| `Progress() Progress` | Statistics snapshot. |
| `Info() Info` | Full snapshot, including identity and paths. |
| `Err() error` | Why the task ended, or nil while queued, running or paused. |
| `ID() string` | Identifier, unique across processes. |
| `URL() string` / `Dir() string` | Target URL and destination directory. |
| `FilePath() string` / `PartPath() string` | Final path and the in-flight path. |
| `Workers() int` | Connections currently assigned. |
| `RangeMode() RangeMode` | Whether the server serves ranges. |

### Events

```go
type EventType int

const (
	EventProgress EventType = iota // bytes arrived or the rate changed
	EventStatus                    // the task changed state
	EventError                     // the task ended in failure
)

type Event struct {
	TaskID   string
	Type     EventType
	Status   Status
	Progress Progress
	Err      error     // EventError only
	At       time.Time
}
```

### Types

`Status` — `StatusPending`, `StatusQueued`, `StatusDownloading`, `StatusPaused`,
`StatusCompleted`, `StatusFailed`, `StatusDeleted`. `String()` gives the
lower-case name.

`RangeMode` — `RangeUnknown`, `RangeSupported`, `RangeUnsupported`. Reported by
`RangeMode()`; `RangeSupported` is what makes parallel fetching possible.

`Progress`:

```go
type Progress struct {
	Total      int64         // full size, or 0 when the server did not say
	Downloaded int64         // bytes durably on disk
	Speed      int64         // bytes per second, over the last sampling window
	ETA        time.Duration // estimated remaining time, or 0
	Percent    float64       // Downloaded of Total, or 0
	Elapsed    time.Duration // time spent transferring, excluding pauses
	Workers    int           // connections assigned
	BlockSize  int64         // current piece length, or 0 when not splitting
}
```

`Info` — `ID`, `URL`, `FilePath`, `PartPath`, `Status`, `RangeMode`, `Err`,
`Total`, `Downloaded`, `Speed`, `Workers`, `BlockSize`, `Elapsed`, plus a
`Percent()` method.

### Errors

All are sentinels, usable with `errors.Is`. Failures returned by a task wrap one
of them where applicable.

| Error | Meaning |
| --- | --- |
| `ErrInvalidURL` | The target is not an absolute `http`/`https` URL, or carries credentials. |
| `ErrInvalidProxy` | The proxy is not an `http`/`https`/`socks5` URL with a host. |
| `ErrBlockedHost` | A host — target or proxy — resolves to a loopback, private, link-local, multicast or otherwise reserved address. |
| `ErrNotPaused` | `Resume` on a task that is not paused. |
| `ErrNotDownloading` | `Pause` on a task that is no longer running. |
| `ErrCompleted` | An operation on a task that already finished. Use `Restart`. |
| `ErrDeleted` | An operation on a deleted task. |
| `ErrStalled` | No data arrived for the stall timeout. |
| `ErrClosed` | An operation on a closed manager. |
| `HTTPStatusError` | A response with an unusable status. Has `StatusCode`, `Status`, `URL`, and a `Retryable()` method. |

```go
if errors.Is(err, muyuan.ErrBlockedHost) {
	// the URL points somewhere the downloader refuses to go
}
var status *muyuan.HTTPStatusError
if errors.As(err, &status) && status.StatusCode == 404 {
	// the file is not there
}
```

## Behaviour details

**Half-finished files are never mistaken for finished ones.** Bytes go to
`FilePath()+".part"`, which is flushed and `fsync`ed, then renamed to the final
name. Nothing else ever writes the final path.

**In the parallel path the `.part` file is created at its full size up front** and
filled in place with positional writes. So while paused, its **file size** is the
size of the whole file, not the number of bytes received. Use
`Progress().Downloaded` for how much arrived. In the single-connection path the
`.part` size is the amount received. `Downloaded` means the same thing in both:
bytes durably on disk, with work still in flight not counted.

**Resuming does not depend on the server answering `If-Range`.** The completion
bitmap knows which granules are on disk. A pause clears the bits of the pieces
that were in flight, and they are fetched again on resume — no compensation
arithmetic, no dependence on a validator. When the server does not serve ranges
at all, `Resume` starts the file over rather than assembling something corrupt.

**Transient failures are retried.** 5xx, dropped connections and a body that ends
early are retried, 3 times by default, with the wait doubling to a 30 s ceiling.
408, 425 and 429 count as transient; other 4xx do not. In the parallel path each
piece retries on its own.

**A stalled connection is detected.** If no byte arrives for the stall timeout
(60 s by default), the transfer is treated as a transient failure and retried.
This is what stops a connection that goes quiet without closing from holding a
download open forever.

**Existing files are not overwritten.** When the destination name is taken, a
counter is appended: `report.pdf`, `report_1.pdf`, `report_2.pdf`. Pass
`WithOverwrite(true)` to replace instead.

**The file name is sanitised.** A name from the server's
`Content-Disposition` or from the URL is reduced to its last path element, has
characters Windows rejects replaced, loses trailing dots and spaces, and gets a
prefix if it is a reserved device name. `../../etc/passwd` becomes `passwd`. A
name longer than 200 bytes is truncated keeping its extension.

**Restarting is always possible.** `Restart` works from any state, including a
completed or deleted task: it discards the bytes, resets the counters and starts
a fresh transfer.

**The context given to a manager bounds the whole manager.** `Close` cancels it,
which stops running transfers; a task that was queued or paused reports
`ErrClosed`.

## Security model

A URL handed to a downloader may come from a config file, a user, or a remote
API, so it may point at the local machine or at services reachable only from
inside the network. Two layers stop that:

1. **Before the request is built**, the target's host is resolved and every
   address it resolves to is checked. A hostname that resolves to a blocked
   address is refused, because which address gets dialled is not under the
   client's control.
2. **When the connection is dialled**, the address is checked again. This closes
   the window in which a name resolves to a public address once and an internal
   one the next time (DNS rebinding). In the parallel path every connection goes
   through the same guarded dialer, so there is no way around it.

The blocked set covers loopback, private, link-local (unicast and multicast),
unspecified, multicast, and the special-purpose ranges: `0.0.0.0/8`,
`100.64.0.0/10`, `192.0.0.0/24`, `192.0.2.0/24`, `192.88.99.0/24`,
`198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `240.0.0.0/4`,
`64:ff9b::/96`, `100::/64`, `2001:db8::/32`, `2002::/16`. IPv4-mapped IPv6
addresses are judged as the IPv4 address they carry.

Credentials in a URL are refused (`https://user:pass@host/`) because they end up
in logs and error messages; use `WithHeader` for an `Authorization` header
instead.

`WithAllowUnsafeHosts(true)` turns all of this off, for targets known to be
trusted — a test server on loopback, a proxy inside the network. It is
all-or-nothing.

## Recipes

**Download one file and wait for it.**

```go
m := muyuan.New(muyuan.WithThreads(8), muyuan.WithMaxFiles(1))
defer m.Close()

t, err := m.AddTask("downloads", url)
if err != nil {
	return err
}
if err := t.Wait(); err != nil {
	return err
}
fmt.Println(t.FilePath())
```

**Show overall progress across every task.**

```go
for _, t := range m.Tasks() {
	p := t.Progress()
	fmt.Printf("%-30s %6.1f%%  %s\n", filepath.Base(t.FilePath()), p.Percent, t.Status())
}
```

**Wait for all of them, with a deadline.**

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()
for _, t := range m.Tasks() {
	if err := t.WaitContext(ctx); err != nil {
		return err
	}
}
```

**Retry a failed task without losing the bytes.**

```go
if t.Status() == muyuan.StatusFailed {
	t.Resume() // resumes from what is on disk
}
```

**Re-download from scratch.**

```go
t.Restart()
```

**Free a connection for a more urgent file.**

```go
big.Pause()      // its connections are handed to the others at once
urgent.Resume()  // and it takes them back as they free up
```

**Give a task its own proxy and credentials.**

```go
t, err := m.AddTask(dir, url,
	muyuan.WithProxy("http://user:pass@proxy.example:3128"),
	muyuan.WithHeader("Authorization", "Bearer "+token))
```

**Pin the piece length, for a server that dislikes big requests.**

```go
m := muyuan.New(muyuan.WithThreads(8), muyuan.WithDefaultChunkSize(512<<10))
```

## Known limits and trade-offs

- **A proxy disables the target's dial-time address check.** The proxy resolves
  the name, so the client cannot see the target's IP. The pre-request check
  remains. See [Proxy](#proxy).
- **`WithAllowUnsafeHosts` is all-or-nothing.** It cannot permit a loopback proxy
  while still refusing a private-network target; that would need two separate
  switches.
- **A task-level connection count does not exist.** The scheduler owns the
  allocation and rewrites it every pass. Size the budget instead.
- **Pausing discards the pieces in flight.** Up to one piece per connection is
  fetched again on resume. That is the price of the bitmap being the single
  source of truth, and it is bounded by 64 KiB per connection when the block
  length is small, and by the block length when it is not.
- **Progress counts completed pieces**, so it lags the work in flight by up to one
  piece per connection.
- **`Workers` reports the allocation, not the connections actually open.** It is
  the number the scheduler assigned, and the engine honours it up to the number of
  pieces the file can be split into. A 1 MiB file with a 16-connection budget
  reports 16 but only opens 4, because the minimum piece length leaves it four
  pieces. Nothing is wasted — the surplus workers exit immediately — but the
  number is a budget rather than a count.
- **An event subscription costs one goroutine per task.** It is started by the
  first `Events()` call, so a task nobody subscribes to costs nothing. A caller
  that subscribes and stops reading should `Delete` or `Close` to release it.
- **Cross-process resume is not implemented.** A `.part` file left by a crash can
  be resumed within the same run, but no sidecar file records the bitmap, so a new
  process starts the file over.
- **No `LICENSE` file yet.** Add one before publishing.

## Tests

```
go test ./...
```

81 tests, all passing:

- **`internal/engine` (56)** — the single-connection path in full: name
  derivation and sanitising, counter suffixes, overwriting, pause and resume
  including the branch where the server does not serve ranges, restart, delete,
  retries and the refusal to retry a 4xx, the stall timeout, context
  cancellation, and two concurrency stress tests that hammer every control
  operation at once. The parallel path: concurrency equals the configured
  connection count, the piece requests **tile the file exactly once with no
  overlap**, pinned and automatic block lengths, raising and lowering the
  connection count mid-transfer, promotion of a single-connection transfer to
  parallel, small files not being split, and the fallback when the server ignores
  ranges. Proxies: an HTTP forward proxy, a minimal SOCKS5 server implemented in
  the test, username/password authentication, parallel fetching through a proxy,
  invalid schemes, a blocked proxy, and an internal target still being refused
  with a proxy configured. Plus unit tests for the bitmap and the block shaper.
- **the root package (18)** — the global connection budget never being exceeded, the
  simultaneous file limit, **breadth-first allocation** (a running file must not
  deepen while another waits), spare connections deepening the running files,
  pausing handing connections over, a queued task starting when the budget frees
  up, changing the budget at runtime, task control operations, per-task file
  names, every task being waitable after close, and internal addresses being
  refused. Events: progress and status delivery, throttling, the error event,
  closing after the terminal event, a slow consumer, and no leaked goroutines
  from a subscription that is never read.
- **`internal/filename` (1)** — file name sanitising.
- **`internal/hostguard` (6)** — every blocked address class (loopback, private,
  link-local, multicast, unspecified, reserved ranges, IPv4-mapped IPv6) and the
  pass-through of routable ones, host input trimming, the resolver path, and the
  dial-time re-check refusing an address without connecting.
