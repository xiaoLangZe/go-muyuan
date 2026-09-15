# Runtime Control

Every control action is available two ways: as a typed signal on the channel from `d.Signals()`, and as a convenience method. They are equivalent.

| Action | Method | Signal |
| --- | --- | --- |
| Pause (keep progress) | `d.Pause()` | `SignalEvent{Type: SigPause}` |
| Resume | `d.Resume()` | `SignalEvent{Type: SigResume}` |
| Set workers total | `d.SetWorkers(n)` | `SignalEvent{Type: SigSetWorkers, Workers: n}` |
| Set connections | `d.SetConnections(n)` | `SignalEvent{Type: SigSetConnections, Connections: n}` |
| Set segments | `d.SetSegments(n)` | `SignalEvent{Type: SigSetSegments, Segments: n}` |
| Set both | `d.SetConnectionsAndSegments(c, s)` | `SignalEvent{Type: SigSetConnectionsAndSegments, ...}` |
| Restart (wipe + re-begin) | `d.Restart()` | `SignalEvent{Type: SigRestart}` |
| Clear cache (delete only) | `d.ClearCache()` | `SignalEvent{Type: SigClearCache}` |
| Stop (keep progress, exit) | `d.Stop()` | `SignalEvent{Type: SigStop}` |
| Abort (exit, `Wait` → `ErrAborted`) | `d.Abort()` | `SignalEvent{Type: SigAbort}` |

```go
d.Signals() <- downloader.SignalEvent{Type: downloader.SigSetConnections, Connections: 16}
```

## Restart vs. Clear cache

- **Restart** deletes the partial file and metadata and *immediately re-begins* the download with the current configuration. One signal, fresh download.
- **Clear cache** deletes the partial file and metadata and leaves the downloader idle; call `Start` again to begin a fresh transfer. It works whether or not the download is running.

## Pause vs. Stop vs. Abort

All three stop the transfer, but they differ:

| Action | Manager stays alive | State | Can `Start` again | What `Wait` returns |
| --- | --- | --- | --- | --- |
| `Pause` | yes | `Paused` | via `Resume` | still blocked |
| `Stop` | no | `Stopped` | yes, resumes from progress | `nil` |
| `Abort` | no | terminal | — | `ErrAborted` |

## State machine (single download)

```text
        Start
Idle ─────────────► Running ◄────────────┐
                      │  ▲                │ Resume
             Pause    │  └────────────────┤
                      ▼                   │
                   Paused ────────────────┘
                      │
     Stop ────────────┼────────► Stopped   (resumable: Start again)
                      │
              Complete┴────────► Completed
                      │
             error ───┴────────► Failed
```

Reconfiguration signals (`SetWorkers` / `SetConnections` / `SetSegments` / `SetConnectionsAndSegments`) do not change the state; they cancel the current connection generation, rebuild the segment plan, and respawn the pool. In `Workers` total-budget mode the manager also runs an adaptive slow-segment splitter that splits a lagging segment in two so a faster worker can claim the back half (see [Architecture](./architecture)).

## Why live reconfiguration is safe

There is one rule:

> **A connection generation is never mutated while it runs.** To change anything, the manager cancels the current generation, waits for *every* connection to return, and only then rebuilds the plan and spawns a new generation.

No worker ever observes a plan changing under it. Already-downloaded bytes are remapped onto the new plan via the **longest contiguous prefix**, so completed regions are not re-fetched. Details in [Architecture](./architecture).
