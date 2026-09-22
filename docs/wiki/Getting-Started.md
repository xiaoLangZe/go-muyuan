# Getting Started

**English** · [中文](Getting-Started-zh)

## Install

```bash
go get github.com/xiaoLangZe/go-muyuan
```

```go
import muyuan "github.com/xiaoLangZe/go-muyuan"
```

The package is named `muyuan`, so the import reads `muyuan.New(...)`.

If you are working against a local checkout rather than the published module,
point at it with a `replace` directive instead:

```
require github.com/xiaoLangZe/go-muyuan v0.0.0

replace github.com/xiaoLangZe/go-muyuan => ../go-muyuan
```

## Download your first file

```go
package main

import (
	"fmt"
	"log"

	muyuan "github.com/xiaoLangZe/go-muyuan"
)

func main() {
	m := muyuan.New(
		muyuan.WithThreads(8),  // connections available to the tasks
		muyuan.WithMaxFiles(1), // one file at a time
	)
	defer m.Close()

	task, err := m.AddTask("downloads", "https://example.com/file.iso")
	if err != nil {
		log.Fatal(err)
	}

	if err := task.Wait(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("saved to", task.FilePath())
}
```

Four things happen here:

1. `New` creates a manager: it owns the connection budget, the file limit, and a
   scheduler. `Close` stops it and every task it holds, so it belongs in a
   `defer`.
2. `AddTask` validates the URL immediately and queues the download. It returns at
   once; the transfer starts when the budget has room for it.
3. `Wait` blocks until the task is over and reports why.
4. `FilePath` is where the finished file ended up.

`AddTask(destDir, rawURL string, opts ...TaskOption)` takes the destination
directory first. An empty string falls back to `WithDefaultDir`.

## Watching it happen

A transfer runs on its own goroutine, so the handle is all you need to follow it.
Two ways, usable at the same time:

```go
// 1. An event stream: progress, state changes, and the ending.
for ev := range task.Events() {
	fmt.Printf("%s %.1f%% %d connections\n",
		ev.Status, ev.Progress.Percent, ev.Progress.Workers)
}

// 2. Polling, whenever it suits you.
p := task.Progress()
fmt.Println(p.Downloaded, p.Total, p.Speed, p.ETA)
```

Both read the same values. See [Progress and Events](Progress-and-Events).

## Where the file goes

The destination directory is resolved in three cases:

| `destDir` | Result |
| --- | --- |
| absolute, e.g. `/srv/files` | used as-is |
| relative, e.g. `downloads` | joined onto the root, which defaults to the directory of the running executable |
| empty | the manager's `WithDefaultDir`, resolved the same way |

The root can be changed with `WithRootDir`. The reason the default is the
executable's directory rather than the working directory: a download that starts
from a service or a scheduled job has no meaningful working directory, and
writing next to the binary is at least predictable.

While a transfer is in flight the bytes live in `FilePath()+".part"`. The partial
file is renamed to the final name only once everything has arrived and been
flushed, so nothing ever sees a half-written file under the real name. See
[Robustness](Robustness).

## Reading the status

| Status | Meaning |
| --- | --- |
| `StatusQueued` | Waiting for the manager to give it a connection |
| `StatusPending` | Prepared but not started |
| `StatusDownloading` | In progress |
| `StatusPaused` | Stopped by you; the bytes are kept |
| `StatusCompleted` | Done, file renamed |
| `StatusFailed` | Stopped on an error; see `Err()` |
| `StatusDeleted` | Stopped and the files removed |

`queued` and `paused` are both "not running", and both are not terminal: `Wait`
keeps blocking on them. The difference is who stopped it — you paused it, versus
the scheduler has not given it a connection yet.

## Where to go next

- [Scheduling and Budget](Scheduling-and-Budget) — why a task waits, and how to
  change how many run at once
- [Task Control](Task-Control) — pausing, resuming, restarting, deleting
- [Recipes](Recipes) — copy-paste answers for common needs
- [FAQ](FAQ) — the questions that come up first
