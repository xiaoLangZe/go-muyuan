# Parallel Downloads

**English** · [中文](Parallel-Downloads-zh)

When a task has more than one connection and the server serves range requests,
the file is fetched in parallel. This page explains how the file is divided and
how the size of a piece is chosen.

## Two levels: granule and piece

The engine does **not** pre-slice the file into fixed chunks. Pre-slicing would
fix both the connection count and the piece boundaries at the start, and those are
exactly what has to stay flexible for
[dynamic allocation](Scheduling-and-Budget).

Instead there are two levels:

- **Granule** — 64 KiB, the resolution of the completion bitmap. A 10 GB file
  needs a 20 KB bitmap.
- **Piece (block)** — what one worker claims in one go. It is a whole number of
  granules, chosen at claim time, so it can change at any moment without
  disturbing the bitmap.

A worker loops:

1. claim the next free run of granules,
2. send one ranged request for exactly that byte range,
3. write the body at its offset,
4. mark the granules complete.

A claim that is abandoned — a pause, a failed request — simply clears its bits.
The range is fetched again later, and bytes another worker already finished inside
the same range are never lost.

## Why progress counts completed pieces

`Progress().Downloaded` counts only bytes that are durably on disk: the granules
that have been written and marked complete. Work still in flight is not counted.

The consequence is that progress trails the transfer by at most one piece per
connection. The benefit is that pausing needs no compensation arithmetic at all,
and resuming does not depend on the server answering `If-Range`: the bitmap is the
single source of truth for what is on disk.

## How the piece length is chosen

- **The first piece** is `file size / (connections × 4)`, clamped to
  `[256 KiB, 16 MiB]`. Four pieces per connection means a worker rarely runs out
  of work while another is still busy.
- **While the transfer runs**, the measured duration of each piece steers the
  length towards a target of about **1.5 s per piece**. A step moves by at most a
  factor of two and the result stays inside the configured bounds, so one unusual
  response cannot collapse or explode the piece length.
- **Near the end**, the remaining bytes are split between the workers so they
  finish together instead of leaving one worker with the whole tail.

To pin the length and disable the adaptation:

```go
muyuan.WithChunkSize(512 << 10)      // one piece is always 512 KiB
muyuan.WithChunkBounds(64<<10, 8<<20) // or just move the bounds
```

## When a file is not fetched in parallel

Parallel fetching is skipped, and the file comes over one connection, when:

- the task has been allocated **one connection** (see
  [Scheduling](Scheduling-and-Budget)),
- the server **answers a range request with 200 instead of 206**, meaning it
  ignores `Range`,
- the server **does not report the file size**, so there is nothing to divide,
- the file is **smaller than two minimum pieces** (512 KiB by default). Splitting
  a file that small costs more than it saves.

The first case is by far the most common: with the default budget of 4 and three
files, each file gets one connection.

You can see which happened through `task.RangeMode()`:

| Value | Meaning |
| --- | --- |
| `RangeUnknown` | No range request has been answered yet |
| `RangeSupported` | The server serves ranges; parallel fetching is possible |
| `RangeUnsupported` | The server ignores `Range`; an interrupted transfer has to start over |

Note that `RangeSupported` with a single connection is normal: it means the server
*could* serve ranges, not that this transfer is using them.

## The partial file is created at full size

In the parallel path the `.part` file is created at the size of the whole file up
front and filled in place with positional writes. Two consequences:

- **While paused, the `.part` file's size is the size of the whole file**, not the
  amount downloaded. Use `Progress().Downloaded`, not the file size.
- On a file system without sparse-file support, the full size is really allocated
  when the transfer starts.

In the single-connection path the `.part` size is the amount received, as you
would expect. `Downloaded` means the same thing in both.

## Related

- [Scheduling and Budget](Scheduling-and-Budget) — how a file gets its connections
- [Task Control](Task-Control) — what pausing does to pieces in flight
- [Architecture](Architecture) — why the engine keeps two transfer paths
