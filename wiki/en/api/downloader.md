# Downloader

`Downloader` drives a single file download; concurrent-safe.

```go
import downloader "github.com/xiaoLangZe/go-muyuan"
```

## Constructor

### `New(cfg Config) (*Downloader, error)`

Validates config and returns a ready-to-`Start` downloader. Creates output/part dirs and SSRF-pre-checks the URL (no real I/O except dir creation). Returns [`ErrInvalidConfig`](./types#sentinel-errors) when the config is invalid.

## Lifecycle methods

| Method | Description |
| --- | --- |
| `Start(ctx context.Context) error` | Async begin/resume; synchronous probe + plan. Returns `ErrAlreadyRunning` if already running/paused. `ctx` bounds the whole download. |
| `Wait() error` | Blocks until terminal; returns `nil` (success), `ErrAborted`, or the failure error. |
| `Close() error` | Aborts any in-progress download, waits for stop, releases resources. **Do not reuse after.** |
| `Progress() Progress` | Current-state snapshot; safe from any goroutine. Returns an idle snapshot before `Start`. |
| `String() string` | Debug render of the config. |

## Control methods

Each has an equivalent signal form (see [Runtime Control](../guide/runtime-control)).

| Method | Description |
| --- | --- |
| `Pause()` | Stop the connection pool, keep on-disk progress; resumable. |
| `Resume()` | Restart the pool after `Pause`. |
| `SetWorkers(n int)` | Set the (connections + segments) total budget, auto-allocated; `0` = classic mode. |
| `SetConnections(n int)` | Change concurrent HTTP connections for this file (live). |
| `SetSegments(n int)` | Re-split the file into n segments (`0` = auto); downloaded bytes preserved. |
| `SetConnectionsAndSegments(connections, segments int)` | Change both knobs in one reconfigure. |
| `Restart()` | Delete all progress and immediately re-begin with the current config. |
| `ClearCache() error` | Delete partial + metadata, leave idle; call `Start` again for a fresh transfer. Works whether running or not. |
| `Stop()` | Stop the download, keep progress for a later `Start` (→ `Stopped`). |
| `Abort()` | Cancel immediately; `Wait` then returns `ErrAborted`; on-disk progress kept. |
| `Signals() chan<- SignalEvent` | Control signal channel (equivalent to the methods); safe to send before `Start`. |

## Example

```go
d, err := downloader.New(downloader.Config{
	URL:         "https://example.com/big.iso",
	OutputPath:  "./big.iso",
	Connections: 8,
	Segments:    16,
})
if err != nil {
	log.Fatal(err)
}
defer d.Close()

if err := d.Start(context.Background()); err != nil {
	log.Fatal(err)
}

// Live reconfiguration while running
d.SetConnections(16)
d.SetWorkers(24)

if err := d.Wait(); err != nil {
	log.Fatal(err)
}
log.Printf("done: %s", d.Progress().OutputPath)
```

## See also

- [Queue](./queue) — batch downloads
- [Types & Errors](./types) — `Config`, `Progress`, `State`, sentinel errors
- [Runtime Control](../guide/runtime-control) — signal table and state machine
