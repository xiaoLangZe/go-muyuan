# Task Control

**English** · [中文](Task-Control-zh)

A task is a handle. Four verbs act on it, plus waiting.

| Verb | Effect |
| --- | --- |
| `Pause()` | Stop the transfer, keep the bytes, hand the connections back |
| `Resume()` | Continue from what is on disk |
| `Restart()` | Discard the bytes and fetch from zero |
| `Delete()` | Stop and remove the files — **this is also "cancel"** |
| `Wait()` / `WaitContext(ctx)` | Block until the task is over |

## Pause

```go
if err := task.Pause(); err != nil {
	return err
}
// The partial file is closed here, so it is safe to inspect or move it.
```

`Pause` returns only once the transfer has stopped and the partial file is closed.
Its connections are released immediately and the next scheduling pass hands them
to the other files.

Pausing a task that is already paused does nothing and reports nil. Pausing a task
that has finished reports `ErrNotDownloading`.

**What happens to the pieces in flight:** they are abandoned. Their granules are
marked as not-complete, so on resume those ranges are fetched again. This is the
price of the completion bitmap being the single source of truth, and it is bounded
by one piece per connection.

## Resume

```go
if err := task.Resume(); err != nil {
	return err
}
```

`Resume` works on a paused task, and also on a **failed** one: it continues from
the bytes already on disk when the server serves ranges, and starts over when it
does not. That makes `Resume` the natural retry after a transient network failure.

Resuming a task that is already running reports nil. Resuming one that has never
been paused — for example a completed or deleted task — reports `ErrNotPaused`.

On a task waiting for a connection, `Resume` simply clears the paused mark and
puts it back in line. It may still wait.

## Restart

```go
if err := task.Restart(); err != nil {
	return err
}
```

`Restart` throws away the bytes received so far, resets the counters, and starts a
fresh transfer. It works from **any** state, including a completed or deleted
task, which makes it the way to re-download something.

A task that has not been given a connection yet is reset and left in the queue
rather than started immediately, so restarting a queued task does not let it jump
the concurrency limit.

## Delete — and "cancel"

```go
if err := task.Delete(); err != nil {
	return err
}
```

`Delete` stops the transfer and removes both the finished file and the partial
file. The task ends in `StatusDeleted`.

**There is deliberately no separate `Cancel`.** Cancelling a task and deleting it
differ only in whether the files are kept, and of the two, deleting is the one
that leaves no surprise on disk. If you want to abandon a download but keep what
arrived, use `Pause` — it is the operation that stops without removing.

Deleting a deleted task is harmless and reports nil.

## Waiting

```go
if err := task.Wait(); err != nil {
	return err
}

// Or with a deadline of your own:
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()
if err := task.WaitContext(ctx); err != nil {
	return err
}
```

`Wait` blocks until the task reaches a terminal state: `completed`, `failed` or
`deleted`. **A queued or paused task is not terminal**, so `Wait` keeps blocking
until it runs and finishes, is deleted, or the manager is closed. This is the
usual reason a program appears to hang — check `Status()`.

`Done()` gives the channel behind `Wait` if you would rather select on it. A task
that is restarted gets a new channel, and an engine-level `Wait` follows a
transfer into the new run.

## Reading state without blocking

```go
task.Status()    // queued / downloading / paused / completed / failed / deleted
task.Progress()  // bytes, speed, ETA, percent, connections, piece length
task.Info()      // the above plus ID, URL, paths, range mode and the error
task.Err()       // why it ended, or nil while queued, running or paused
task.Workers()   // connections assigned
task.RangeMode() // whether the server serves ranges
```

## The lifecycle

```
queued ──▶ downloading ──▶ completed
  ▲            │  ▲            │
  │            │  │            │
  └────────────┘  │            │
   (out of budget)│            │
                 ▼            ▼
              paused ─────▶ deleted
                 │
                 ▼
              failed
```

- `Pause` moves `downloading` to `paused`.
- `Resume` moves `paused` back to `downloading` (or to `queued`, if there is no
  connection for it yet).
- Running out of budget while running moves a task to `queued`, not `paused` — it
  is waiting, not stopped by you.
- `Restart` moves any state back to `downloading` or `queued`.
- `Delete` moves any state to `deleted`.
- A failed task stays `failed` until you `Resume` or `Restart` it. The manager
  does not retry it on its own.

## Related

- [Robustness](Robustness) — retries and stalls, which happen without your
  involvement
- [Progress and Events](Progress-and-Events) — watching the transitions
- [Scheduling and Budget](Scheduling-and-Budget) — why a task is queued
