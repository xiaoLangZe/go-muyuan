# 性能基准

基准测试位于仓库的 `bench_test.go`，在**本地回环的 `httptest` 服务器**上
下载 4 MiB 载荷。需要说明：回环链路上 TCP 拥塞与带宽不是瓶颈，数字反映
的是调度与 HTTP 处理开销；真实广域网下，多连接与 Workers 模式的差距
只会更大。

运行：

```bash
go test -bench=. -benchtime=2s -count=3 -run='^$' .
```

## 参考数据

环境：AMD Ryzen 7 5800H、Windows、Go 1.26（2026-09 测得）。
数值是 3 次运行的中位数。

| 基准 | 模式 | ns/op | 相对单连接 |
| --- | --- | --- | --- |
| `BenchmarkDownloadSingleConn` | 1 连接 / 1 切片 | ~17.8 ms | 1.00× |
| `BenchmarkDownloadMultiConn` | 8 连接 / 16 切片 | ~11.7 ms | **1.52×** |
| `BenchmarkDownloadWorkers` | `Workers=16`（自动分配） | ~12.7 ms | 1.40× |
| `BenchmarkDownloadWorkersSlowSegment` | `Workers=16` + 首段慢速 | ~64.1 ms | — |

## 结论

- **多连接与 Workers 模式都明显快于单连接**：在回环上约快 1.4–1.5 倍。
- **Workers 模式的自动分配有少量开销**（比手工 8/16 略慢），但它
  是唯一在某个分片变慢时**自动恢复**的模式：`SlowSegment` 行显示
  整组仍能完成，而单连接基线会承受全部慢速。
- 单连接 vs 多连接的差距随链路延迟放大——下载大文件到远距离
  CDN 时，多连接收益远高于回环测试。

## 如何复现

```bash
git clone https://github.com/xiaoLangZe/go-muyuan.git
cd go-muyuan
go test -bench=. -benchtime=3s -count=3 -run='^$' .
```

你自己的数字会因机器与 Go 版本而异；报告数字时请注明环境。
