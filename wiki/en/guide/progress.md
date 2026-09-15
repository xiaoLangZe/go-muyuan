# Progress

`Progress` is an immutable snapshot and is safe to read from any goroutine.

```go
p := d.Progress()
// p.State, p.Total, p.Downloaded, p.Percent,
// p.Speed, p.ETA, p.Connections, p.Segments, p.Workers,
// p.AcceptRanges, p.Err
```

## Progress fields

| Field | Meaning |
| --- | --- |
| `URL` / `OutputPath` | Source and destination |
| `Total` / `Downloaded` | Total and downloaded bytes |
| `Percent` | Completion percentage (`0`–`100`) |
| `Speed` | Bytes per second (EWMA-smoothed) |
| `ETA` | Estimated time remaining |
| `Connections` / `Segments` / `Workers` | Currently effective knobs |
| `AcceptRanges` | Whether the server honors range requests |
| `State` | Current lifecycle state |
| `Err` | Terminal error, if any |

## Two ways to get it

### Polling

```go
for {
	p := d.Progress()
	fmt.Printf("\r%.1f%%  %.1f MiB/s", p.Percent, p.Speed/1024/1024)
	if p.State.IsTerminal() {
		break
	}
	time.Sleep(200 * time.Millisecond)
}
```

### Callback (push)

```go
d, _ := downloader.New(downloader.Config{
	URL:        url,
	OutputPath: path,
	OnProgress: func(p downloader.Progress) {
		fmt.Printf("\r%.1f%%  ETA %s", p.Percent, p.ETA)
	},
})
```

The callback fires from the manager's ticker (~2 Hz) and **must not block** — it runs on the manager goroutine, so blocking slows the download. For expensive work, hand the snapshot to your own channel.

## Queue progress

The queue offers three levels of visibility:

| Method | Granularity |
| --- | --- |
| `QueueConfig.OnTaskProgress` | Per-running-task progress callback |
| `QueueConfig.OnTaskStateChange` | Called on task state changes |
| `q.Tasks()` / `q.Task(id)` | Snapshots of all / one task |
| `q.Summary()` | Aggregate |

`Summary` carries `Total/Pending/Running/Paused/Completed/Failed/Canceled` counts, `TotalBytes`/`DownloadedBytes`, and `Concurrency`. `Summary.Done()` reports whether the queue has drained.
