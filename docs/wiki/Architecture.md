# Architecture

**English** · [中文](Architecture-zh)

Why the code is laid out the way it is. Useful if you intend to read or change it;
skippable if you only use the library.

## Package layout

```
github.com/xiaoLangZe/go-muyuan/          package muyuan — the public API
├── muyuan.go      manager: options, AddTask, the scheduler
├── task.go        task: the handle, pause/resume/restart/delete
├── events.go      the event stream
├── options.go     task-level options
├── status.go      Status / Progress / Info / RangeMode
├── errors.go      sentinel errors
└── internal/
    ├── engine/    the transfer engine: one file's lifecycle
    ├── hostguard/ refusing loopback, private and reserved addresses
    └── filename/  deriving and sanitising file names
```

The public package sits at the module root so the import path is the module path:
`import "github.com/xiaoLangZe/go-muyuan"`. The package is named `muyuan`, which is
shorter than `go_muyuan` and valid Go — a package name cannot contain a hyphen.

`internal/` cannot be imported from outside the module, so `engine`, `hostguard`
and `filename` are implementation details. Nothing in them is a public API and
they may change in any release.

## Two owners

- **The engine** owns one file's transfer: the state machine, the connection
  budget for that file, the pieces, the partial file, retries and stalls. It knows
  nothing about other files.
- **The manager** owns the pool of connections and the order in which files run.
  It gives each task a number of connections by calling into the engine; it never
  touches the transfer itself.

That split is why `SetWorkers` exists on the engine: it is the one knob the
scheduler turns. The scheduler recomputes every allocation from scratch on every
change and applies the results, which keeps the two rules of
[scheduling](Scheduling-and-Budget) in one place instead of scattered across the
operations that trigger them.

## Why there are two transfer paths

The engine has a single-connection path and a parallel one. Both fetch the same
file and share the same lifecycle, but they are genuinely different algorithms,
and the simpler one is not a subset of the other:

| | Single connection | Parallel |
| --- | --- | --- |
| Requests | one, `Range` only when resuming | one per piece, each with an explicit range |
| Partial file | grows as bytes arrive | created at full size, written in place |
| Completion recorded by | the file's length | a 64 KiB bitmap |
| Resume needs | an offset | the bitmap |

Single connection is used when the file has one connection, when the server does
not serve ranges, when the size is unknown, or when the file is too small to
split. Preallocating a whole file and tracking a bitmap for a single connection
would be pure overhead, so the two paths are kept apart rather than forced
together.

## Why a bitmap instead of fixed chunks

Pre-slicing the file into N fixed chunks would fix both the piece boundaries and
the connection count at the start. Those are exactly what has to stay flexible:

- the scheduler changes how many connections a file has while it runs,
- the piece length adapts to the measured throughput.

So completion is tracked by a bitmap at 64 KiB resolution, and a piece is
whatever run of free granules a worker claims at that moment. Changing the piece
length and the worker count are then both trivial: the bitmap does not care.

The bitmap is also why pausing needs no compensation arithmetic. A piece in
flight is simply not marked complete, so it is fetched again; there is no partial
state to reconcile.

## Concurrency model

Inside the engine, `ctl` serialises the lifecycle operations — start, pause,
restart, delete — each of which holds it for its whole duration, including waiting
for the previous run to return. The run itself only ever takes `mu`, which is what
keeps that wait from deadlocking. Only one run is active at a time.

Inside the manager, the scheduler is a single goroutine that recomputes the
allocation when woken by any change. Engine calls are made outside the manager's
lock, because some of them wait.

## Type aliases between the two layers

`Status`, `Progress`, `Info`, `RangeMode` and `Option` are aliases of the engine's
types:

```go
type Status = engine.Status
type Option = engine.Option
```

So a value read from a task and one read deep inside the engine are the same type
rather than two definitions kept in sync by hand. Errors are the same story: the
public sentinels *are* the engine's sentinels, which is why `errors.Is` works
across the boundary.

## Design decisions worth knowing

- **The host guard is wired into the dialer**, not just checked before the
  request, so the parallel path cannot bypass it.
- **The event emitter is lazily started** by the first `Events()` call, so a task
  nobody watches costs no goroutine.
- **The partial file is only ever renamed**, never written under its final name,
  so an interrupted download cannot leave something that looks complete.
- **No third-party dependencies** anywhere in the module.

## Related

- [Parallel Downloads](Parallel-Downloads) — the piece and granule design in use
- [Scheduling and Budget](Scheduling-and-Budget) — the two allocation rules
- [Limitations and Roadmap](Limitations-and-Roadmap) — what this design does not
  provide yet
