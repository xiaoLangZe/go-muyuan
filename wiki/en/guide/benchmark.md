# Benchmarks

Benchmarks live in the repo's `bench_test.go` and download a 4 MiB payload from
a **local loopback `httptest` server**. On loopback, TCP congestion and bandwidth
are not the bottleneck, so the numbers reflect scheduling and HTTP-handling
overhead; on a real WAN the gap between modes only widens.

Run them with:

```bash
go test -bench=. -benchtime=2s -count=3 -run='^$' .
```

## Reference numbers

Environment: AMD Ryzen 7 5800H, Windows, Go 1.26 (measured 2026-09).
Values are medians of 3 runs.

| Benchmark | Mode | ns/op | vs single-conn |
| --- | --- | --- | --- |
| `BenchmarkDownloadSingleConn` | 1 conn / 1 segment | ~17.8 ms | 1.00× |
| `BenchmarkDownloadMultiConn` | 8 conns / 16 segments | ~11.7 ms | **1.52×** |
| `BenchmarkDownloadWorkers` | `Workers=16` (auto) | ~12.7 ms | 1.40× |
| `BenchmarkDownloadWorkersSlowSegment` | `Workers=16` + slow first range | ~64.1 ms | — |

## Takeaways

- **Both multi-connection and Workers modes clearly beat single-connection**:
  about 1.4–1.5× faster even on loopback.
- **Workers mode pays a small price for auto-allocation** (slightly slower than
  the hand-tuned 8/16 case), but it is the only mode that **recovers
  automatically** when one segment is slow — the `SlowSegment` row shows the
  group still completes, while a single-connection baseline would inherit the
  full slowdown.
- The single- vs multi-connection gap grows with link latency — for large files
  from a distant CDN the benefit is far larger than the loopback numbers show.

## Reproducing

```bash
git clone https://github.com/xiaoLangZe/go-muyuan.git
cd go-muyuan
go test -bench=. -benchtime=3s -count=3 -run='^$' .
```

Your numbers will differ by machine and Go version; always state the environment
when reporting.
