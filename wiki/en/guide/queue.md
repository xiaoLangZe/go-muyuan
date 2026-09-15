# Batch Queue

`Queue` downloads many files with bounded concurrency. Its `Concurrency` limit (files at once) is a **separate dimension** from each file's own connections/segments, so "3 files at once, 8 connections each" means up to 24 concurrent connections.

::: tip Complete example
The code on this page is a fragment. For a complete, runnable batch download program see [Examples](./examples#batch-download-task-queue).
:::

## Basic usage

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

Tasks may be **added while the queue is running**; they are scheduled as slots free up.

## QueueConfig fields

| Field | Meaning | Default |
| --- | --- | --- |
| `Concurrency` | Files downloaded simultaneously, range `[1,128]` | `3` |
| `Template` | Per-file defaults (its `URL`/`OutputPath`/`OnProgress` are ignored) | — |
| `OutputDir` | Save dir for tasks with no explicit path | `.` |
| `MaxRetries` | Per-task retries | `0` (no retry) |
| `RetryDelay` | Wait before retry | `2s` |
| `OnTaskProgress` | Per-task progress callback | none |
| `OnTaskStateChange` | Called on task state changes | none |
| `OnQueueDone` | Called once when the queue drains | none |

## Three ways to enqueue

| Method | Purpose |
| --- | --- |
| `q.Add(url, outputPath)` | Enqueue one download; empty path is derived from the URL into `OutputDir` and de-duplicated |
| `q.AddAll(urls, dir)` | Enqueue each URL into a dir (`""` = `OutputDir`) |
| `q.AddTask(TaskSpec{...})` | Enqueue from a full `TaskSpec`; callable while running |

## Queue control

Same dual API as single downloads: `q.Signals() <- QueueSignal{...}` or the convenience method.

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

Per-file knobs (`SetWorkers`/`SetConnections`/`SetSegments`) apply both to running tasks and to tasks started later.

## Per-task control and inspection

| Action | Method |
| --- | --- |
| Pause one task (stays paused across a queue Resume) | `q.PauseTask(id)` |
| Resume one task | `q.ResumeTask(id)` |
| Re-download one task (deletes its output) | `q.RestartTask(id)` |
| Delete one task's partial + metadata | `q.ClearTaskCache(id)` |
| Cancel one task | `q.CancelTask(id)` |
| Snapshot one task / all tasks | `q.Task(id)` / `q.Tasks()` |
| Aggregate snapshot | `q.Summary()` |

## Task states

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

- **Paused** covers both a caller pause (`PauseTask`) and a queue suspension (`Pause`, or concurrency lowered). A queue `Resume` revives only the latter.
- A queue `Wait` returns once no task can progress. A task paused by the caller keeps the queue alive until it is resumed or canceled.

## Concurrency and slots

Suspending a task **releases its slot** while keeping progress on disk (via a stop-and-resume of the underlying download, not a live pause). This matters for large batches: a suspended task does not hold an idle connection.

Lowering `Concurrency` below the number of running tasks suspends the most recently started ones, which resume automatically as slots free up.

## Derived output paths

A task with no explicit `OutputPath` gets a name derived from the URL. The derivation takes only the last path component and strips directory separators, control characters, and filesystem-reserved characters, so a crafted URL cannot escape `OutputDir`. Names are de-duplicated against existing tasks by appending `-1`, `-2`, and so on.

## Task errors

`Queue.Wait` returns the aggregate of all task failures as an `errors.Join` of `*TaskError`, each carrying the task ID and URL. A queue where every task succeeded returns nil; a queue with any failure returns a non-nil error even if other tasks completed, so partial failures cannot pass silently. `Summary` gives the breakdown.
