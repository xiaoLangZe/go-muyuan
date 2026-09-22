# Progress and Events

**English** · [中文](Progress-and-Events-zh)

A transfer runs on its own goroutine, so nothing you do blocks it. There are two
ways to follow it, and they can be used at the same time.

## The event stream

```go
for ev := range task.Events() {
	switch ev.Type {
	case muyuan.EventProgress:
		fmt.Printf("%.1f%% %d connections\n", ev.Progress.Percent, ev.Progress.Workers)
	case muyuan.EventStatus:
		fmt.Println("state:", ev.Status)
	case muyuan.EventError:
		fmt.Fprintln(os.Stderr, "failed:", ev.Err)
	}
}
```

```go
type Event struct {
	TaskID   string
	Type     EventType // EventProgress, EventStatus, EventError
	Status   Status
	Progress Progress
	Err      error     // set on EventError only
	At       time.Time
}
```

Rules that matter:

- The channel is created by the **first** call to `Events()` and is **closed once
  the task ends**, after the event reporting the ending has been delivered. A
  `range` loop therefore runs from start to finish and terminates on its own.
- **Progress events are throttled to at most one per 200 ms.** Every state change
  is delivered, unthrottled.
- **A consumer that stops reading does not hold up the download.** The transfer is
  on its own goroutine; only the emitter waits on the channel. See the resource
  note below.
- **A task that finishes faster than the throttle produces no progress events** —
  only the status events, whose `Progress` is still complete. On a fast local
  server this is the normal case. See [FAQ](FAQ).

### Resource note

Subscribing starts one goroutine per task, so a task nobody subscribes to costs
nothing. The emitter is released when the task is deleted or the manager is
closed. A caller that subscribes and then stops reading should `Delete` the task
or `Close` the manager rather than leave it running.

## Polling

`Progress()`, `Info()` and `Status()` can be called at any time. The values are
the same ones an event would carry, so the two styles mix freely.

```go
ticker := time.NewTicker(time.Second)
defer ticker.Stop()
for range ticker.C {
	p := task.Progress()
	fmt.Printf("%.1f%%  %d/%d bytes  %d B/s  eta %s\n",
		p.Percent, p.Downloaded, p.Total, p.Speed, p.ETA.Truncate(time.Second))
	if task.Status() == muyuan.StatusCompleted {
		break
	}
}
```

`Info()` is a superset:

```go
type Info struct {
	ID, URL, FilePath, PartPath string
	Status                      Status
	RangeMode                   RangeMode
	Err                         error
	Total, Downloaded, Speed    int64
	Workers                     int
	BlockSize                   int64
	Elapsed                     time.Duration
}
```

## What the numbers mean

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

- **`Downloaded` counts only completed pieces**, so it trails the work in flight
  by at most one piece per connection. In the parallel path that is a single
  source of truth, and it is why pausing needs no compensation arithmetic. See
  [Parallel Downloads](Parallel-Downloads).
- **`Speed` is sampled, not instantaneous.** The engine takes a snapshot at most
  every 250 ms, and the event stream forwards it at most every 200 ms, so a very
  short transfer may report a first sample that is lower than the real rate.
- **`Elapsed` excludes paused time**, so it is transfer time rather than wall
  time.
- **`Workers` is the allocation**, not the number of connections actually open.
  See [FAQ](FAQ).

## Monitoring several tasks at once

```go
for _, task := range m.Tasks() {
	p := task.Progress()
	fmt.Printf("%-30s %6.1f%%  %s\n",
		filepath.Base(task.FilePath()), p.Percent, task.Status())
}
```

`m.Tasks()` is a snapshot in the order the tasks were added. `m.Task(id)` looks one
up by its identifier.

## Callbacks

`WithProgress` and `WithDefaultProgress` register a callback instead of a channel.
It runs on the transfer goroutine, so **it must not block and must not call
methods that wait for the transfer to stop** — calling `Pause`, `Wait` or `Delete`
on the same task from inside it will deadlock.

Given the choice, prefer the event stream: an unread channel cannot hold up a
transfer, while a slow callback does.

## Related

- [API Reference](API-Reference) — the exact signatures
- [Task Control](Task-Control) — the states an event can report
- [FAQ](FAQ) — why no progress events arrived for a small file
