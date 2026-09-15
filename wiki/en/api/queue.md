# Queue

`Queue` is a bounded-concurrency multi-file downloader; concurrent-safe.

## Construct and enqueue

| Method | Description |
| --- | --- |
| `NewQueue(cfg QueueConfig) (*Queue, error)` | Validate config, return an empty ready queue. Returns `ErrInvalidConfig` when invalid. |
| `Add(rawURL, outputPath string) (string, error)` | Enqueue one download; an empty path is derived from the URL into `OutputDir` and de-duplicated. Returns the task ID. |
| `AddAll(urls []string, dir string) ([]string, error)` | Enqueue each URL into a dir (`""` = `OutputDir`); returns IDs and the first URL error. |
| `AddTask(spec TaskSpec) (string, error)` | Enqueue from a full `TaskSpec`; callable while running. |

## Run and wait

| Method | Description |
| --- | --- |
| `Start(ctx context.Context) error` | Begin scheduling; `ErrAlreadyRunning` if running; requires ≥1 task. |
| `Wait() error` | Block until drained/aborted; returns `nil` or `errors.Join` of `*TaskError`. Caller-paused tasks block drain. |
| `Close() error` | Abort + wait, return the aggregate error. |
| `Signals() chan<- QueueSignal` | Queue control signal channel. |
| `String() string` | Debug render. |

## Whole-queue control

| Method | Description |
| --- | --- |
| `Pause()` | Suspend all running tasks (progress kept), stop launching new ones. |
| `Resume()` | Undo `Pause`; caller-paused tasks stay paused. |
| `Stop()` | Suspend all, keep progress, end the current run; `Start` again to continue. |
| `Abort()` | Cancel all tasks; `Wait` then returns `ErrAborted`. |
| `SetConcurrency(n int)` | Change files-at-once; lowering suspends most-recently-started tasks. |
| `SetWorkers(n int)` | Set the per-file (connections + segments) budget; `0` = classic mode. |
| `SetConnections(n int)` | Change per-file connections for running + future tasks. |
| `SetSegments(n int)` | Change per-file segments (`0` = auto) for running + future tasks. |
| `SetConnectionsAndSegments(connections, segments int)` | Change both per-file knobs. |
| `ClearCache() error` | Delete every task's partial + metadata; non-terminal tasks reset to re-download. |

## Per-task control

| Method | Description |
| --- | --- |
| `PauseTask(id string) error` | Suspend one task (sticky: a queue `Resume` will not revive it). |
| `ResumeTask(id string) error` | Make a suspended/paused task runnable again. |
| `RestartTask(id string) error` | Discard output + progress, re-run from scratch, reset the retry budget. |
| `ClearTaskCache(id string) error` | Delete one task's partial + metadata; non-terminal reset to re-download. |
| `CancelTask(id string) error` | Remove a task from the queue; a running one is aborted and never runs again. |

## Inspection

| Method | Description |
| --- | --- |
| `Tasks() []TaskInfo` | Snapshots of all tasks in add order. |
| `Task(id string) (TaskInfo, bool)` | Snapshot of one task. |
| `Summary() Summary` | Aggregate snapshot. |

## Example

```go
q, err := downloader.NewQueue(downloader.QueueConfig{
	Concurrency: 3,
	OutputDir:   "./downloads",
	MaxRetries:  2,
	Template:    downloader.Config{Connections: 8, Segments: 16},
})
if err != nil {
	log.Fatal(err)
}
defer q.Close()

ids, err := q.AddAll(urls, "")
if err != nil {
	log.Fatal(err)
}

if err := q.Start(context.Background()); err != nil {
	log.Fatal(err)
}

// Pause a single task while running
if len(ids) > 0 {
	_ = q.PauseTask(ids[0])
}

if err := q.Wait(); err != nil {
	// Pull out one task's error
	var te *downloader.TaskError
	if errors.As(err, &te) {
		log.Printf("task %s (%s) failed: %v", te.ID, te.URL, te.Err)
	}
}

s := q.Summary()
log.Printf("completed %d / failed %d / total %d", s.Completed, s.Failed, s.Total)
```

## See also

- [Downloader](./downloader)
- [Types & Errors](./types) — `QueueConfig`, `TaskSpec`, `TaskInfo`, `Summary`
- [Batch Queue guide](../guide/queue)
