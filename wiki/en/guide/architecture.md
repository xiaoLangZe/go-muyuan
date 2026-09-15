# Architecture

This document explains how `go-muyuan` (木鸢) performs a download, why the on-disk layout is what it is, and how live reconfiguration stays safe.

Terminology (see [Introduction](./introduction) for the full table): a **connection** is one HTTP transfer, a **segment** is a byte-range piece of the file, **workers** is the total budget of (connections + segments) that the library auto-allocates, and **concurrency** is how many *files* a queue downloads at once.

## Overview

A `Downloader` owns a `manager`. The manager is created per `Start` call and runs in a single goroutine that:

1. **Probes** the server (a `HEAD`, then a 1-byte `Range` GET) to learn the total size, `ETag`, `Last-Modified`, and whether `Accept-Ranges` is honored.
2. **Reconciles** any on-disk progress with the probed resource (resume or discard).
3. **Builds a segment plan** — the file split into contiguous byte ranges.
4. **Runs a connection pool**: T goroutines that repeatedly claim the next unfinished segment and download its remaining bytes.
5. **Reacts to control signals** (pause/resume/reconfigure/restart/clear/stop/abort).

## Connections vs. segments

Two independent knobs:

- **Segments (`Segments`)** — how many byte ranges the file is divided into. This is the plan's length, and it also sets the resume granularity.
- **Connections (`Connections`)** — how many connections run concurrently. Each connection holds at most one segment at a time.

A connection claims a segment, downloads `[Start+Downloaded, End)`, marks it done, and claims the next. With more segments than connections, connections **reuse** themselves across segments; effective parallelism is `min(Connections, unfinished segments)`.

This is why `Connections > Segments` is meaningless — there are only `Segments` pieces to fetch, so extra connections idle immediately.

## On-disk layout

For an output path `P`:

| Path | Purpose |
| --- | --- |
| `P` | the final file (created only on success) |
| `P.muyuan.partial` | the in-progress file, preallocated to full size |
| `P.muyuan.meta.json` | JSON progress record |

The metadata records the URL, size, `ETag`, `Last-Modified`, the connection count (informational), and the per-segment `{position, start, end, downloaded}` array. The segment *count* is the array's length — there is deliberately no separate count field to drift out of sync.

### Why a single partial file (not per-segment part files)

A naive design writes each segment to its own `P.partN` file and concatenates at the end. That makes re-splitting expensive: changing the segment count would require reading and rewriting every part.

Instead, all segments share **one** partial file and each connection writes its bytes at the segment's **absolute offset** via `WriteAt`. A byte's location in the file does not depend on which segment owns it. Consequences:

- **No merge step.** Completion is a `fsync` + `rename`.
- **Re-splitting is metadata-only.** Changing segment boundaries recomputes the per-segment `downloaded` counters; no bytes move.

Metadata writes are atomic (marshal → temp file → `rename`) so a crash mid-write cannot corrupt the record.

## Resume

On `Start`, if `P.muyuan.meta.json` exists it is validated against the freshly probed resource:

- size must match (when both are known),
- `ETag` must match (when both are present),
- `Last-Modified` must match (when both are present).

Any mismatch discards the progress and starts clean, preventing a half-old file from being stitched together with new bytes. When compatible, completed segments are skipped and partial segments resume from `Start+Downloaded`.

## Live reconfiguration

Reconfiguration is the subtle part. The rule that makes it safe:

> **A connection generation is never mutated while it runs.** To change anything, the manager cancels the current generation, waits for *every* connection to return, and only then rebuilds the plan and spawns a new generation.

`runExclusive` implements this: if connections are running, it queues the change and cancels them; the manager applies the queued change when the generation reports done. Pause, reconfigure, restart, stop, and clear-cache all funnel through it.

### Re-splitting preserves progress via *prefix* coverage

When the segment count changes, `reinterleave` recomputes each new segment's `Downloaded` from the old plan. The crucial detail is that it uses the **longest contiguous prefix** already present on disk — not the total intersection.

Why this matters: when old and new boundaries do not align, a downloaded region can sit *beyond a hole* inside a new segment. Example with a 200-byte file:

```text
old segments:  [0,100) done            [100,200) 50 bytes done
old ranges:    [0,100)                 [100,150)
new segment:   [0,200) spanning both
intersection = 150   ✗  resumes at 150, skipping bytes [50,100)
prefix       = 100   ✓  resumes at 100, re-fetching [100,200)
```

Using the prefix guarantees the connection resumes at an offset whose preceding bytes are all valid. Bytes stranded beyond a hole are simply re-downloaded, which is safe; the aggregate `Downloaded` counter is re-derived from the new plan so reporting stays honest.

### Workers total-budget allocation

`Config.Workers` is the total budget of (connections + segments). When it is non-zero, `allocateWorkers` derives `Connections` (~`Workers/3`, clamped to `[2, 64]`) and leaves `Segments` at `0` (auto), so the plan builder picks a segment count that keeps every connection busy. This is the recommended high-performance mode: the caller sets one number and the library picks the split. Setting `Workers` to `0` falls back to the classic per-knob mode where `Connections` and `Segments` are set independently and both remain available.

### Adaptive slow-segment splitting

In `Workers` mode (and in the classic mode too) the manager runs a periodic slow-segment detector (`maybeSplitSlowSegments`, every ~2 s):

1. It samples each in-flight segment's bytes-per-second since the last sample.
2. It computes the median rate across all in-flight segments.
3. A segment whose rate is below `median/2` **and** that still has `>= 2 * MinSegmentSize` bytes remaining is split: the back half of its remaining range becomes a new segment whose claim is released, so a faster worker can pick it up immediately.

Splitting is a metadata-only operation — all segments share one partial file and bytes are written at absolute offsets, so carving a new boundary moves no data. The current connection continues the front half from where it stopped; the new segment starts at the split point with `Downloaded = 0`.

This is how "a slow segment is automatically split to keep the fastest workers busy" works without ever copying bytes.

## Fallback: no range support

If the server does not answer a `Range` request with `206 Partial Content` (or the length is unknown), the download cannot be split or resumed. The manager switches to a **single sequential segment** and one connection streams the whole body from offset 0. Any existing progress is discarded, since it cannot be verified.

## The queue (batch downloads)

`Queue` schedules many `Downloader`s. It keeps two dimensions apart:

- **`Concurrency`** — how many files are active at once (queue-wide).
- **`Connections` / `Segments`** — the per-file knobs from the sections above.

A single scheduler goroutine owns all queue state and runs a small loop: handle a signal, recompute the plan, apply it, check for completion. All state mutation happens on that goroutine under one mutex; blocking work (creating a `Downloader`, probing the server, waiting for a download) happens in per-task runner goroutines, never while the lock is held.

### Suspension releases a slot

The subtle requirement is that suspending a task must not leave an idle connection holding a concurrency slot — otherwise a large batch would stall with all slots occupied by tasks nobody asked to run.

That is why the queue suspends by **stopping** a task's downloader (which exits the manager and persists progress) rather than calling `Downloader.Pause` (which keeps the manager alive). The slot is freed, progress is on disk, and resuming creates a fresh `Downloader` that continues from the sidecar.

Two kinds of pause must not be confused:

- **Queue suspend** (`Pause`, or `Concurrency` lowered) is *resumable*: the task becomes eligible again as slots free up.
- **Caller pause** (`PauseTask`) is *sticky*: a queue `Resume` will not revive it. Internally this is one `userPaused` flag consulted when deciding whether a paused task is runnable.

Lowering `Concurrency` below the running count suspends the most recently started tasks, so the ones closest to finishing are the ones kept running.

### Retries and permanent failures

A failed attempt is retried at the queue level (up to `MaxRetries`, after `RetryDelay`) by re-launching the task; a fresh `Downloader` resumes from the sidecar, so retries do not restart the transfer.

Inside a single file, `retrySegment` retries transient failures with bounded backoff. It deliberately does **not** retry a `permanentError`, which `statusError` returns for 4xx responses (except 408 and 429). Without this, a 404 would consume the whole backoff budget — about 3.5 seconds — before failing, which is both slow and pointless. 5xx stays retryable.

### Derived output paths

A task with no explicit `OutputPath` gets a name derived from the URL. The derivation takes only the last path component and strips directory separators, control characters, and filesystem-reserved characters, so a crafted URL cannot escape `OutputDir`. Names are de-duplicated against existing tasks by appending `-1`, `-2`, and so on.

### Task errors

`Queue.Wait` returns the aggregate of all task failures as an `errors.Join` of `*TaskError`, each carrying the task ID and URL. A queue where every task succeeded returns nil; a queue with any failure returns a non-nil error even if other tasks completed, so partial failures cannot pass silently. `Summary` gives the breakdown.

## Error model

- A genuine worker error fails the whole download (`StateFailed`, error from `Wait`); deliberate cancellation is reported as `nil` so the manager can tell the two apart.
- Transient failures inside a segment are retried with bounded exponential backoff (`retrySegment`); cancellation is never retried, and neither is a permanent HTTP status.
- Retries recompute the resume offset from current state, so a retry after a partial failure continues rather than re-writing bytes.

## Concurrency invariants

- `manager.mu` guards the plan, claims, counters, and state.
- The manager goroutine owns lifecycle fields (`exit`, `genRunning`, `cancelGen`, `afterGen`); connections never touch them.
- Connections communicate with the manager over the plan (mutex-guarded) and the per-generation `genDone` channel, which provides the happens-before edge that lets the manager safely close/rename the partial file only after all connections have returned.
- Lock ordering is one-directional (`Queue.mu` → `manager.mu`); progress callbacks are invoked after releasing the inner lock, so no path takes them in the opposite order.
