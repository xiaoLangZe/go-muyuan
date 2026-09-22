# Robustness

**English** · [中文](Robustness-zh)

What the library does on its own when the network misbehaves, and what it leaves
on disk while doing it.

## Retries

A failed attempt is retried, 3 times by default, with the wait doubling from 1
second up to a ceiling of 30 seconds.

```go
muyuan.WithDefaultRetries(5)              // manager-wide
muyuan.WithDefaultRetryDelay(2*time.Second)
muyuan.WithRetries(0)                     // or per task: give up immediately
```

**What counts as transient** (retried):

- any 5xx response,
- `408 Request Timeout`, `425 Too Early`, `429 Too Many Requests`,
- a connection that drops,
- a body that ends before the advertised length,
- a stalled transfer (see below).

**What does not** (fails immediately):

- 4xx other than the three above — a 404 will not become a 200,
- an unusable URL or a blocked host,
- a write error on disk.

In the parallel path **each piece retries on its own**, so one bad request costs
one piece, not the whole transfer.

`HTTPStatusError` exposes the decision the library made:

```go
var status *muyuan.HTTPStatusError
if errors.As(err, &status) {
	fmt.Println(status.StatusCode, status.Retryable())
}
```

## Stall detection

A connection that goes quiet without closing would hold a download open forever.
If no byte arrives for the stall timeout — 60 seconds by default — the transfer is
treated as a transient failure and retried.

```go
muyuan.WithDefaultStallTimeout(2 * time.Minute)
muyuan.WithDefaultStallTimeout(0) // disable the check
```

The timeout is per connection, so in the parallel path a stalled piece does not
disturb the others.

## Partial files

Bytes are written to `FilePath()+".part"`. Only when everything has arrived is the
file flushed (`fsync`), closed, and renamed to the final name. Nothing else ever
writes the final path, so a file under its real name is always complete.

In the parallel path the `.part` file is created at the full size of the file up
front and filled in place. See [Parallel Downloads](Parallel-Downloads).

## File names

The name comes from, in order of precedence:

1. `WithFileName`, if given;
2. the server's `Content-Disposition` header;
3. the last path element of the URL.

Whatever the source, the name is sanitised before use: reduced to its last path
element, characters Windows rejects replaced, trailing dots and spaces removed,
and a prefix added if it collides with a reserved device name. `../../etc/passwd`
becomes `passwd`.

A name longer than 200 bytes is truncated while keeping its extension, on a rune
boundary so a multi-byte character is not split.

## Existing files

By default an existing file is **not** overwritten. A counter is appended instead:
`report.pdf`, `report_1.pdf`, `report_2.pdf`. The first free name wins.

```go
muyuan.WithOverwrite(true) // replace instead
```

## Cancellation

The manager owns one context, created by `New` and cancelled by `Close`. There is
no per-task context in the API; a task is stopped with `Pause`, `Delete` or by
closing the manager.

```go
m := muyuan.New(...)
defer m.Close() // cancels the manager's context, and with it every transfer
```

After `Close`:

- running transfers are cancelled and end as `failed`,
- tasks that were queued or paused report `ErrClosed`,
- **partial files stay on disk**, so nothing downloaded is lost,
- `AddTask` reports `ErrClosed`.

```go
if errors.Is(err, muyuan.ErrClosed) {
	// the manager is gone
}
```

## What is *not* handled automatically

- **A failed task stays failed.** The manager does not resurrect it; call `Resume`
  (continues from disk) or `Restart` (from zero) yourself.
- **A new process starts over.** The completion bitmap lives in memory, so a
  crash loses the record of which pieces arrived. See
  [Limitations and Roadmap](Limitations-and-Roadmap).
- **Content is not verified.** There is no checksum step; if you need one, hash
  the file after `Wait` returns.

## Related

- [Task Control](Task-Control) — the manual verbs
- [FAQ](FAQ) — the errors people hit in practice
- [Limitations and Roadmap](Limitations-and-Roadmap) — what is missing
