# Limitations and Roadmap

**English** · [中文](Limitations-and-Roadmap-zh)

What the library does not do, stated plainly. Nothing here is hidden in a
footnote; if a limitation would surprise you, it belongs on this page.

## Missing capabilities

### Resuming across process restarts

**The biggest gap.** Pausing and resuming works within one process, but the
completion bitmap lives in memory and nothing on disk records which pieces
arrived. If the process crashes, is killed, or simply exits, a new process starts
the file over — even though a `.part` file full of correct bytes is sitting there.

For a large file this is expensive: losing a download at 90% means re-fetching
90%.

What it would take: a sidecar file next to the `.part` recording the bitmap and
the validator, written as pieces complete, plus a check that the resource has not
changed since. Not implemented.

### Bandwidth limiting

There is no rate limit. A download takes whatever the network and the connection
budget allow. For a desktop application or a shared connection this is often the
first thing you want.

What it would take: a token bucket shared by the manager, applied at the read
boundary in both transfer paths.

### Content verification

There is no checksum step. The library checks that the number of bytes matches
what the server advertised, and nothing more. If you need to know the content is
correct, hash the file after `Wait` returns.

What it would take: an expected digest option, verified on completion before the
rename.

### A User-Agent option

There is no dedicated option; set it as a header:

```go
muyuan.WithHeader("User-Agent", "my-service/1.0")
```

The default is Go's `Go-http-client/1.1`, which some servers rate-limit or refuse.
A first-class option is a small addition; it has not been made.

### Proxy from the environment

`HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are not consulted. The proxy is
configured explicitly, per manager or per task. This is deliberate — implicit
behaviour is harder to reason about — but it diverges from what most Go HTTP
clients do.

### Two separate unsafe-host switches

`WithAllowUnsafeHosts` turns the host guard off entirely. It cannot allow a
loopback proxy while still refusing a private-network target, because both checks
share one switch. See [Proxy and Security](Proxy-and-Security).

## Behaviour that surprises people

These are documented but worth repeating here.

| Behaviour | Why |
| --- | --- |
| `Workers` reports the allocation, not open connections | The scheduler assigns a number; the engine opens as many as the file has pieces for |
| A 1-connection task sends no `Range` request at all | One connection uses the single-connection path, which streams the body |
| In the parallel path the `.part` file is the full size of the file | It is created up front and filled in place |
| `Downloaded` trails the transfer | It counts only completed pieces, up to one piece per connection behind |
| A short download produces no progress events | Progress is throttled to one per 200 ms |
| `Wait` blocks on a queued or paused task | Neither is terminal |
| A 404 is not retried | Only 5xx, 408, 425 and 429 are transient |
| `Resume` on a failed task continues from disk | That is the intended retry path |

## Engineering gaps

### The tests are not in the repository

The module has 87 tests — 56 for the transfer engine, 24 for the manager, 6 for
the host guard, 1 for file naming — including concurrency stress tests and a race
detector run in CI. **They are not committed.** The repository's `.gitignore`
excludes `*_test.go`, so a clone contains no tests and the CI job's test step
reports "no test files" and passes vacuously.

This is a known, deliberate-but-questionable state: it means the test suite is not
verifiable by anyone else, and a visitor has no evidence of quality.

### No benchmarks

The piece sizing, the bitmap and the scheduler have unit and integration tests but
no measurements. There is no data on how the engine behaves at high connection
counts or on very large files.

### API stability

The module is at `v0.1.0`. Under semantic versioning, `v0.x` may introduce
breaking changes between minor releases. If you depend on it, pin the version.

## Roadmap

In the order I would do them:

1. **Track the test files**, so CI checks something and the suite is reviewable.
2. **Resume across process restarts** — the largest functional gap.
3. **Rate limiting** — the most requested kind of feature for a download library.
4. **A User-Agent option** and a couple of other small conveniences.
5. **Benchmarks** for the parallel path.

None of these are promised. This page records what is missing and why, not a
schedule.

## Related

- [Robustness](Robustness) — what *is* handled automatically
- [FAQ](FAQ) — the practical consequences of these limits
- [Architecture](Architecture) — why the design is what it is
