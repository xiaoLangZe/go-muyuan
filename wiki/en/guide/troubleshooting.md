# Troubleshooting

## `go get` fails with 404 or `Repository not found`

- Check the spelling of the module path: `github.com/xiaoLangZe/go-muyuan`,
  with the original letter casing (Go module paths are case-sensitive).
- Through a proxy (`GOPROXY` pointing at goproxy.cn etc.), the index can lag;
  pinning an explicit version — `go get ...@v0.2.0` — usually works right
  away, while `@latest` waits for the proxy to reindex.

## `go get` resolves to an old pseudo-version

Without tags a module can only be fetched by commit pseudo-versions, and those
break when history is rewritten. This project tags releases since v0.1.0 —
use the real version:

```bash
go get github.com/xiaoLangZe/go-muyuan@v0.2.0
```

To drop stale local entries: `go clean -modcache`.

## Download fails immediately: `host resolves to non-public`

The SSRF guard is doing its job: the target is loopback/LAN/reserved and
`AllowPrivateHost` is off. Either:

- the target really is a trusted LAN/local server → set `AllowPrivateHost: true`;
- otherwise the URL is wrong — do not disable the guard to "fix" the error.

## Progress stays at 0% and then fails

Usually the server does not support `Range` (see
[Architecture](./architecture#fallback-no-range-support)) or returns 403/404.
Check `Progress().AcceptRanges`: `false` means single-stream fallback. The
error from `Wait` carries the concrete cause.

## `Wait` returns a checksum mismatch (`ErrChecksumMismatch`)

`VerifySHA256` was set and the digest didn't match: the output was deleted —
a wrong file never survives. Check the digest source (a release page's
checksums, not another file's) and compare against `sha256sum` of a known-good
file.

## What happens when the disk fills up?

Short writes (fewer bytes written than requested) are detected and fail the
download — no silently truncated output. Free space and re-run; it resumes.

## How do I rate-limit?

`Config.MaxBytesPerSec`: the cap across **all** connections of one file
(with a 1-second burst allowance). It is not live-reconfigurable — restart the
download to change it.

## How often does the progress callback fire?

About every 500 ms. The callback runs on the manager goroutine and
**must not block** — hand the snapshot to your own channel for slow work.

## Every task in my queue is stuck in Paused?

Do not confuse the two kinds of pause: a queue suspension (`Pause`, or
concurrency lowered) recovers via `Resume` or when slots free up; a per-task
pause (`PauseTask`) is **sticky** and needs `ResumeTask` or `CancelTask`.

## Language of the docs

Every page has an English mirror under the `/en/` prefix; when editing docs,
update both.
