# Scheduling and Budget

**English** · [中文](Scheduling-and-Budget-zh)

A manager owns one pool of HTTP connections and divides it between the files that
are currently downloading. Two settings shape that division:

```go
m := muyuan.New(
	muyuan.WithThreads(16), // connections in flight across all tasks
	muyuan.WithMaxFiles(3), // files downloading at the same time
)
```

## The two rules

The scheduler recomputes every allocation from scratch on every change, so the
rules live in one place. There are two, applied in order:

1. **Spread first.** Every file that can run gets one connection, up to
   `WithMaxFiles` files and never more than the total budget.
2. **Then deepen.** Whatever is left is dealt out across those files, round by
   round. The round robin caps how many connections any single file can take, so
   one file never consumes the whole budget.

| Budget | Files | Result |
| --- | --- | --- |
| 2 | 3 | files 1 and 2 get 1 each; file 3 stays queued |
| 6 | 2 | each file gets 3 |
| 4 | 4 | each file gets 1 |
| 6 | 4 | two files get 2, two get 1 |
| 8 | 1 | that file gets all 8 |

The point of spreading first is that a large download cannot starve a small one.
If you add a 10 GB file and a 2 MB file to a 4-connection manager, both start
immediately and both make progress; the small one finishes quickly and its
connections move to the large one.

## A task with one connection streams the whole file

This is worth knowing because it changes what the server sees:

**With one connection, the transfer does not send range requests at all.** It
opens one connection and reads the body from start to finish. Splitting a file
over a single connection costs more than it saves, and it is the only option when
the server does not serve ranges.

So a file that was allocated exactly one connection makes one request and no
`Range` header. Only a budget of two or more, against a server that serves
ranges, produces parallel piece requests. See
[Parallel Downloads](Parallel-Downloads).

## Queueing

A task that has not been given a connection reports `StatusQueued`. Add four
files to a manager with `WithMaxFiles(2)` and the last two wait as `queued` until
a slot frees up.

`queued` is not terminal: `Wait` on such a task blocks until it actually runs and
finishes. If you want to know whether a task has started without blocking, look at
`Status()`.

## Changing the budget at runtime

```go
m.SetThreads(32)  // more connections for everyone
m.SetMaxFiles(8)  // let more files start
```

- **Raising `Threads`** hands the extra connections to the running files on the
  next scheduling pass.
- **Lowering `Threads`** takes connections away. A file that ends up with none is
  paused — it keeps the bytes it already has and continues when the budget allows
  it again. Its status becomes `queued`, because it is waiting rather than stopped
  by you.
- **Raising `MaxFiles`** starts queued tasks.
- **Lowering `MaxFiles`** does not interrupt files that are already running; it
  only stops new ones from starting. They finish, and the slots go to the queue.

## Pausing frees connections

`Pause` on a task releases its connections immediately, and the next scheduling
pass hands them to the other files. `Resume` puts the task back in line.

This is the intended way to prioritise: pause the bulk download, let the urgent
one take the bandwidth, then resume.

```go
big.Pause()      // its connections go to the others at once
urgent.Resume()  // and it takes them back as they free up
```

## `Workers()` reports the allocation, not open connections

`task.Workers()` and `Progress().Workers` report the number the scheduler
assigned. The engine honours it up to the number of pieces the file can be split
into.

A 1 MiB file with a 16-connection budget reports 16 but only opens 4, because the
minimum piece length of 256 KiB leaves it four pieces. Nothing is wasted — the
surplus workers exit immediately — but the number is a budget rather than a
measurement. See [FAQ](FAQ) for more on this.

## Related

- [Parallel Downloads](Parallel-Downloads) — what happens inside one file's
  allocation
- [Task Control](Task-Control) — pausing, resuming, deleting
- [API Reference](API-Reference) — the full option list
