# Introduction

`go-muyuan` is an high-speed downloader shipped as a **reusable Go library** — not a CLI tool. It splits a file into byte-range *segments* and downloads them over a pool of *connections*, and it can be reconfigured, paused, resumed, restarted, and cache-cleared **while running** via control signals. A task queue downloads many files with bounded *concurrency*.

The project's Chinese name is **木鸢** (mùyuān, "wooden kite"); the `muyuan` in the module path is its pinyin. This manual refers to the project as `go-muyuan` throughout.

## Core idea

A naive multi-threaded downloader writes each segment to its own `.partN` file and concatenates at the end. Changing the segment count then means reading and rewriting every part.

`go-muyuan` does something different: all segments share **one preallocated partial file**, and each connection writes its bytes at the segment's **absolute offset**. Re-splitting is therefore a metadata-only operation that moves no data.

- Completion is a `fsync` + `rename` — there is no merge step.
- Changing the segment count preserves downloaded bytes automatically.

## Terminology

Three independent dimensions. The names matter, so here is the vocabulary this library uses — and what is *not* a good name for each:

| Concept | This library | Why not the obvious name |
| --- | --- | --- |
| Files downloaded simultaneously | **`Concurrency`** (on `QueueConfig`) | not a "thread count" — these are independent file downloads |
| Simultaneous transfers **within one file** | **`Connections`** (on `Config`) | each one is one HTTP connection, so "threads" is misleading: nothing is threaded |
| Byte-range pieces the file is cut into | **`Segments`** (on `Config`) | distinct from `Connections`: you can cut a file into 32 segments and fetch only 8 at a time |

**`Connections` and `Segments` are separate knobs.** `Segments` is the *plan* (how finely the byte range is divided, which also sets resume granularity); `Connections` is the *parallelism* (how many segments are in flight at once). Effective parallelism is `min(Connections, unfinished segments)`.

A common point of confusion: a "worker goroutine" in the implementation is one **connection**, not one thread per segment forever. A connection claims a segment, finishes it, and claims the next — which is why `Connections > Segments` is pointless (there is nothing left to claim).

## Features

- **Multi-connection range downloads** — splits a file into N segments fetched over T concurrent connections.
- **Workers total-budget mode** — set `Workers` once and the library auto-allocates `Connections` and `Segments`, splitting slow segments while running. Recommended for highest performance.
- **Adaptive slow-segment splitting** — a segment clearly slower than the median is split in two so a faster worker can claim the back half. Metadata-only.
- **Batch download queue** — download many files with bounded concurrency, where "files at once" and "connections per file" are separate knobs.
- **Four configuration modes** — connections only, segments only, both, or the Workers total-budget mode. All available at runtime.
- **Live reconfiguration** — change workers, connections, or segments on a running download; downloaded bytes are preserved.
- **Pause / Resume / Restart / Clear cache** — full lifecycle control, via a typed signal channel or convenience methods.
- **Resumable across processes** — progress is persisted and validated against `Content-Length`, `ETag`, and `Last-Modified`.
- **Graceful fallback** — servers without `Accept-Ranges` are downloaded single-stream instead of failing.
- **Retries** — transient failures are retried with backoff; permanent ones (404, 403) fail fast. Queues retry whole tasks too.
- **SSRF guard** — http/https only, with private addresses rejected at both validation and dial time.
- **Progress reporting** — periodic snapshots with speed (EWMA) and ETA.

## Next steps

- [Quick Start](./getting-started) — install and run your first download
- [Configuration](./configuration) — all `Config` fields and the four modes
- [Runtime Control](./runtime-control) — signals and methods

## About this manual

This manual is a VitePress project under `wiki/`, Chinese by default with an English translation. Once a change is pushed to `main`, `.github/workflows/deploy-docs.yml` builds and publishes it to <https://xiaoLangZe.github.io/go-muyuan/>. To preview locally, run `cd wiki && npm install && npm run dev`. When adding documentation, update both the Chinese page under `guide/` (or `api/`) and its English counterpart.
