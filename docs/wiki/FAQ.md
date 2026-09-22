# FAQ

**English** · [中文](FAQ-zh)

Questions that come up in practice, with the cause rather than just the fix.

---

### Why does my test against `httptest` fail with `ErrBlockedHost`?

Because `httptest` listens on loopback, and the host guard refuses loopback,
private and reserved addresses by default. That is the guard doing its job: a URL
that may come from a user or a remote API must not be able to reach your machine.

Turn it off for the test, and only for the test:

```go
m := muyuan.New(muyuan.WithDefaultAllowUnsafeHosts(true))
```

See [Proxy and Security](Proxy-and-Security) for what else the switch turns off.

---

### No progress events arrived, only status events

The download finished in under 200 ms. Progress events are throttled to one per
200 ms, so a transfer that is already over produces none; the status events still
carry a complete `Progress` snapshot, including the final 100%.

On a local network this is normal. To see progress events, download something
large enough to take more than a fraction of a second, or slow the server down.

---

### `Workers()` says 16 but the server only sees 4 connections

`Workers` reports what the scheduler **assigned**, not what is open. The engine
honours the assignment up to the number of pieces the file can be split into.

A 1 MiB file with a 256 KiB minimum piece length has four pieces, so at most four
connections do anything; the other twelve workers exit immediately. Nothing is
wasted, but the number is a budget rather than a measurement.

If you want the count that is really in use, check `BlockSize` and the file size,
or count requests on the server.

---

### While paused, the `.part` file is the whole size of the file

That is the parallel path: the file is created at its full size up front and
filled in place with positional writes, so the file size says nothing about
progress. Use `Progress().Downloaded`.

In the single-connection path the `.part` size is the amount received, as you
would expect. `Downloaded` means the same thing in both: bytes durably on disk.

---

### `Wait()` never returns

`Wait` blocks until the task reaches a terminal state — `completed`, `failed` or
`deleted`. A **queued** task is not terminal, and neither is a **paused** one:

- A task is `queued` while the manager has no connection to give it. With
  `WithMaxFiles(2)` and four files, two of them wait.
- A task is `paused` after you called `Pause`, and stays that way until you
  `Resume` or `Delete` it.

Check `Status()` to see which. To wait with a bound, use `WaitContext`.

---

### The progress callback deadlocks

`WithProgress` callbacks run on the transfer goroutine. Calling `Pause`, `Wait` or
`Delete` on the same task from inside the callback waits for that goroutine, which
is waiting for the callback: a deadlock.

Prefer the event stream — an unread channel cannot hold up a transfer:

```go
for ev := range task.Events() { ... }
```

---

### A 404 is not retried, but a 500 is

Deliberate. A 404 will not turn into a 200 on the next attempt, so retrying only
wastes time. Transient means: 5xx, 408, 425, 429, a dropped connection, a body
that ends early, or a stall.

```go
var status *muyuan.HTTPStatusError
if errors.As(err, &status) {
	fmt.Println(status.StatusCode, status.Retryable())
}
```

---

### How do I resume after the process restarts?

You cannot, currently. The completion bitmap is in memory, so a new process starts
the file over even though the `.part` file is on disk with correct bytes. This is
the largest known gap; see
[Limitations and Roadmap](Limitations-and-Roadmap).

Within one process, `Pause` and `Resume` work as expected, and `Resume` also
continues a **failed** task from disk.

---

### Throughput is lower than I expected

Check `Workers()` and `RangeMode()`:

- **`Workers() == 1`** — the file is streaming over a single connection. This
  happens when the budget divided between the running files leaves this one with a
  single connection, which is the default with `WithThreads(4)` and several files.
  Give the file more of the budget, or run fewer files at once.
- **`RangeMode() == RangeUnsupported`** — the server ignores `Range`, so the file
  cannot be split. Nothing can be done client-side.
- **A file below 512 KiB is never split** — too small for the overhead to pay off.

---

### How do I cancel a download but keep what arrived?

`Pause`. There is no separate `Cancel`: cancelling and deleting differ only in
whether the files are kept, and `Delete` is the one that removes them.

```go
task.Pause() // stop, keep the bytes, free the connections
task.Delete() // stop and remove both files
```

---

### My headers are not being sent

Check which option you used. `WithHeader` sets a header for **one task**;
`WithDefaultHeader` sets it for every task the manager creates. Passing
`WithHeader` to `New` does not compile, because the two option types are
different.

```go
m := muyuan.New(muyuan.WithDefaultHeader("X-Token", token)) // every task
task, _ := m.AddTask(dir, url, muyuan.WithHeader("X-Token", token)) // one task
```

---

### `go test` fails with "Access is denied" / "fork/exec"

Observed on Windows when antivirus or endpoint protection intercepts the freshly
built test binary in the Go build cache. The binary itself is fine — running it
directly works. Workarounds:

```bash
go test -c -o /tmp/pkg.test.exe ./...
/tmp/pkg.test.exe
```

or add the Go build cache and `%TEMP%\go-build*` to the antivirus exclusions.

Note that the module's own tests are not in the repository; see
[Limitations and Roadmap](Limitations-and-Roadmap).

---

### Why is the package called `muyuan` and not `go_muyuan`?

Two reasons. A Go package name cannot contain a hyphen, and `go_muyuan` with an
underscore is legal but unidiomatic. `muyuan` is what the module is called once
the `go-` prefix is dropped, which is the usual convention for a repository named
`go-something`.

The import path is the module root, so it reads:

```go
import muyuan "github.com/xiaoLangZe/go-muyuan"
```

The explicit alias is optional; it is there because the last element of the path
is `go-muyuan` while the package is `muyuan`.

---

### Can I use it without a connection budget?

Yes — set the budget to one and the file limit to one, and it behaves like a plain
sequential downloader:

```go
m := muyuan.New(muyuan.WithThreads(1), muyuan.WithMaxFiles(1))
```

The file will be fetched over a single connection, with the same retry, stall and
partial-file guarantees.

## Related

- [Getting Started](Getting-Started) — the basics
- [Robustness](Robustness) — what happens automatically
- [Limitations and Roadmap](Limitations-and-Roadmap) — the full list of gaps
