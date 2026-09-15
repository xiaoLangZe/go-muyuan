# go-muyuan

[![Go Reference](https://pkg.go.dev/badge/github.com/xiaoLangZe/go-muyuan.svg)](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan)
[![Go Version](https://img.shields.io/badge/go-1.26-blue.svg)](https://go.dev/)

[English](../README.md) | **中文**

📖 **[文档站](https://xiaoLangZe.github.io/go-muyuan/)** ·
📦 [pkg.go.dev](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan) ·
🐙 [GitHub](https://github.com/xiaoLangZe/go-muyuan)

一个 golang 高速下载库，以**可复用的 Go 库**形式提供。它把文件切成若干字节区间（*切片 / segment*），用一组*连接*并发下载；并且在**运行中**即可通过控制信号重配置、暂停、继续、重下和清缓存。另有任务队列，以有界*并发*批量下载多个文件。

## 术语（重要）

共有三个独立维度。名字很关键，下面是本库采用的叫法，以及**为什么不该用**那些"看起来更顺口"的名字：

| 概念 | 本库的叫法 | 为什么不能用直觉上的名字 |
| --- | --- | --- |
| 同时下载几个文件 | **`Concurrency`**（在 `QueueConfig` 上） | 它不是"线程数"——这些是彼此独立的文件下载 |
| **单个文件内**同时几路传输 | **`Connections`**（在 `Config` 上） | 每一路就是一条 HTTP 连接。叫"线程"会误导：这里并没有用多线程，而且会被读成"一个文件用了多线程" |
| 文件被切成几段 | **`Segments`**（在 `Config` 上） | 它与 `Connections` 不同：可以把文件切成 32 段，但只同时下 8 段 |

**`Connections` 与 `Segments` 是两个独立旋钮。** `Segments` 是*计划*（字节区间被切得多细，同时决定了续传粒度）；`Connections` 是*并行度*（同一时刻有多少段在传输）。实际并行度 = `min(Connections, 未完成段数)`。

一个常见误解：实现里的 worker goroutine 就是一条**连接**，而不是"每一段永久占一个线程"。一条连接认领一段、下完、再认领下一段——这也正是 `Connections > Segments` 没有意义的原因（没有多余的段可认领）。

## 特性

- **多连接区间下载** —— 把文件切成 N 段，由 T 条并发连接抓取。段数与连接数相互独立：32 段 / 8 连接是合法的。
- **Workers 总和模式** —— 只设一个 `Workers`，库自动分配 `Connections`（并发 HTTP 流）与 `Segments`（切片数），两者之和不超过预算。推荐用于最高性能：下载器自行决定切分，让每条连接始终有段可领。传统分别设置模式（`Connections` + `Segments` 各自设置）完整保留。
- **慢分片自动拆分** —— 运行中，manager 采样每个进行中分片的速率；明显慢于中位数的分片会被一分为二，让更快的 worker 领走后半段。不搬运任何字节（单 partial 文件、绝对偏移），是纯元数据操作，始终保持最快 worker 满载。
- **批量下载队列** —— 用有界并发下载多个文件（`Queue`），其中"同时下载几个文件"与"每个文件几条连接"是两个独立维度。任务可在运行中添加，可单个控制也可整体控制。
- **四种配置模式** —— 只配连接、只配切片、两者都配，或 Workers 总和模式。运行时同样可用。
- **运行时热重配** —— 下载过程中改 Workers、连接数、切片数或两者；已下载的字节会被保留（不搬运数据、不重下已完成区域）。
- **暂停 / 继续 / 重下 / 清缓存** —— 完整生命周期控制，既可用类型化信号通道，也可用便捷方法。
- **跨进程续传** —— 进度持久化在输出文件旁，并与服务端的 `Content-Length`、`ETag`、`Last-Modified` 校验，因此中断的下载能从断点继续。
- **优雅降级** —— 不支持 `Accept-Ranges` 的服务端会退化为单流下载，而不是直接失败。
- **重试** —— 瞬时故障按退避重试；而永久性故障（404、403）会立即失败，不再白等退避时间。队列层面同样支持任务级重试。
- **SSRF 防护** —— 仅允许 http/https，并在校验阶段和拨号阶段**双重**拒绝 localhost/环回/私有/保留地址（可防 DNS rebinding）。局域网下载用 `AllowPrivateHost` 关闭。
- **进度上报** —— 周期性快照，含速率（EWMA）与剩余时间估计；单文件可回调或轮询，队列有聚合视图。

## 安装

要求 **Go 1.26 或更高版本**（`go.mod` 里的 `go` 指令）。工具链过旧会报
`go.mod requires go >= 1.26`。

仓库发布到 GitHub 之后：

```bash
go get github.com/xiaoLangZe/go-muyuan
```

**目前**它会因为远端尚不存在而失败，报错为 `remote: Repository not found`。
想从本地目录使用，请改用 `replace` 指令指向源码目录：

```text
// 你项目的 go.mod
require github.com/xiaoLangZe/go-muyuan v0.0.0

replace github.com/xiaoLangZe/go-muyuan => /绝对路径/go-muyuan
```

然后导入（包名是 `downloader`，与模块路径最后一段不一致，所以加个别名更清楚）：

```go
import downloader "github.com/xiaoLangZe/go-muyuan"
```

## 快速开始

```go
package main

import (
	"context"
	"fmt"
	"log"

	downloader "github.com/xiaoLangZe/go-muyuan"
)

func main() {
	d, err := downloader.New(downloader.Config{
		URL:         "https://example.com/big.iso",
		OutputPath:  "./big.iso",
		Connections: 8,  // 该文件的并发连接数
		Segments:    16, // 字节区间切片数（0 = 自动）
		OnProgress: func(p downloader.Progress) {
			fmt.Printf("\r%.1f%%  %.1f MiB/s  (%d conns, %d segs)",
				p.Percent, p.Speed/1024/1024, p.Connections, p.Segments)
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := d.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("\ndone:", d.Progress().OutputPath)
}
```

## 配置项

`Config` 字段（完整参考见 [pkg.go.dev](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan)）：

| 字段 | 含义 | 默认值 |
| --- | --- | --- |
| `URL` | 源地址（http/https；会校验 host） | 必填 |
| `OutputPath` | 最终输出路径 | 必填 |
| `Workers` | （线程 + 切片）总和预算；`0` = 传统分别设置模式 | `0` |
| `Connections` | 下载该文件的并发 HTTP 连接数（`Workers>0` 时自动分配） | `8` |
| `Segments` | 文件切成的字节区间段数；`0` = 自动 | `0`（自动） |
| `PartDir` | 临时文件与元数据所在目录 | `OutputPath` 所在目录 |
| `Headers` | 额外请求头（如 `Authorization`） | 无 |
| `HTTPClient` | 自定义 `*http.Client` | 由 `Proxy` + SSRF 防护构建 |
| `Proxy` | 代理地址（`http`、`https`、`socks5`） | 无 |
| `OnProgress` | 进度回调 | 无 |
| `MinSegmentSize` | 自动切片时的单片下限 | `1 MiB` |
| `AllowPrivateHost` | 允许局域网/回环目标（关闭 SSRF 防护） | `false` |

### 四种配置模式

`Connections` 与 `Segments` 是两个独立旋钮；`Workers` 是自动分配两者的总和预算：

- **Workers 总和模式**（*推荐用于最高性能*）—— 只设 `Workers` 一个数；库分配 `Connections` 与 `Segments`，两者之和不超过 `Workers`，并在运行中检测慢分片、自动拆分，让最快的 worker 始终满载。
- **仅连接** —— `Segments` 留 `0`；段数根据连接数与文件大小自动推导。
- **仅切片** —— 设置 `Segments`；`Connections` 保持默认。
- **两者都配** —— 都显式设置（连接数超过段数时，多出的连接会空转，因为一段同一时刻只由一条连接抓取）。

## 运行时控制

每个控制动作都有两种等价形式：`d.Signals()` 通道上的类型化信号，以及便捷方法。

| 动作 | 方法 | 信号 |
| --- | --- | --- |
| 暂停（保留进度） | `d.Pause()` | `SignalEvent{Type: SigPause}` |
| 继续 | `d.Resume()` | `SignalEvent{Type: SigResume}` |
| 设置 Workers 总和 | `d.SetWorkers(n)` | `SignalEvent{Type: SigSetWorkers, Workers: n}` |
| 设置连接数 | `d.SetConnections(n)` | `SignalEvent{Type: SigSetConnections, Connections: n}` |
| 设置切片数 | `d.SetSegments(n)` | `SignalEvent{Type: SigSetSegments, Segments: n}` |
| 同时设置两者 | `d.SetConnectionsAndSegments(c, s)` | `SignalEvent{Type: SigSetConnectionsAndSegments, ...}` |
| 重下（清空并立即重启） | `d.Restart()` | `SignalEvent{Type: SigRestart}` |
| 清缓存（只删除） | `d.ClearCache()` | `SignalEvent{Type: SigClearCache}` |
| 停止（保留进度并退出） | `d.Stop()` | `SignalEvent{Type: SigStop}` |
| 中止（退出，`Wait` 返回 `ErrAborted`） | `d.Abort()` | `SignalEvent{Type: SigAbort}` |

```go
d.Signals() <- downloader.SignalEvent{Type: downloader.SigSetConnections, Connections: 16}
```

### 「重下」与「清缓存」的区别

- **重下（Restart）** 删除临时文件与元数据，并**立即用当前配置重新开始**下载。一个信号，全新开始。
- **清缓存（ClearCache）** 删除临时文件与元数据，让下载器保持空闲；需要再次调用 `Start` 才会开始新的传输。无论下载是否在运行都可用。

## 批量下载（任务队列）

`Queue` 以有界并发下载多个文件。它的 `Concurrency` 上限（同时下载几个文件）与每个文件自己的连接/切片是**两个独立维度**——"同时 3 个文件、每个 8 连接"意味着最多 24 条并发连接。

```go
q, err := downloader.NewQueue(downloader.QueueConfig{
	Concurrency: 3,        // 同时下载的文件数
	OutputDir:   "./downloads",
	MaxRetries:  2,        // 每个任务放弃前的重试次数
	Template: downloader.Config{
		Connections: 8,    // 每个文件的并发连接数
		Segments:    16,   // 每个文件的字节区间段数（0 = 自动）
	},
	OnTaskStateChange: func(t downloader.TaskInfo) {
		fmt.Printf("%s: %s\n", t.ID, t.State)
	},
})
if err != nil {
	log.Fatal(err)
}

ids, err := q.AddAll([]string{url1, url2, url3}, "") // "" => 使用 OutputDir
if err != nil {
	log.Fatal(err)
}

if err := q.Start(context.Background()); err != nil {
	log.Fatal(err)
}
if err := q.Wait(); err != nil {
	// 聚合所有失败任务；可用 errors.As(*downloader.TaskError) 取出单个。
	log.Fatal(err)
}
fmt.Println(q.Summary())
```

任务可以在**队列运行期间继续添加**，会在有空闲槽位时被调度。

### 队列控制

与单文件下载相同的双形态 API：`q.Signals() <- QueueSignal{...}` 或便捷方法。

| 动作 | 方法 | 信号 |
| --- | --- | --- |
| 全部暂停（保留进度） | `q.Pause()` | `QueueSignal{Type: QSigPause}` |
| 全部继续 | `q.Resume()` | `QueueSignal{Type: QSigResume}` |
| 设置同时下载文件数 | `q.SetConcurrency(n)` | `QueueSignal{Type: QSigSetConcurrency, Concurrency: n}` |
| 设置每文件 Workers 总和 | `q.SetWorkers(n)` | `QueueSignal{Type: QSigSetWorkers, Workers: n}` |
| 设置每文件连接数 | `q.SetConnections(n)` | `QueueSignal{Type: QSigSetConnections, Connections: n}` |
| 设置每文件切片数 | `q.SetSegments(n)` | `QueueSignal{Type: QSigSetSegments, Segments: n}` |
| 同时设置两者 | `q.SetConnectionsAndSegments(c, s)` | `QueueSignal{Type: QSigSetConnectionsAndSegments, ...}` |
| 清空所有缓存 | `q.ClearCache()` | `QueueSignal{Type: QSigClearCache}` |
| 停止（保留进度并退出） | `q.Stop()` | `QueueSignal{Type: QSigStop}` |
| 中止 | `q.Abort()` | `QueueSignal{Type: QSigAbort}` |

每文件旋钮（`SetWorkers`/`SetConnections`/`SetSegments`）会同时作用于**正在运行**的任务和**之后启动**的任务。

### 单任务控制与查询

| 动作 | 方法 |
| --- | --- |
| 暂停单个任务（队列 Resume 不会唤醒它） | `q.PauseTask(id)` |
| 继续单个任务 | `q.ResumeTask(id)` |
| 重下单个任务（删除其输出文件） | `q.RestartTask(id)` |
| 删除单个任务的临时文件与元数据 | `q.ClearTaskCache(id)` |
| 取消单个任务 | `q.CancelTask(id)` |
| 取单个/全部任务快照 | `q.Task(id)` / `q.Tasks()` |
| 取聚合快照 | `q.Summary()` |

### 任务状态

```text
Pending ──► Running ──► Completed
   ▲          │  │
   │          │  └──► Failed        (重试次数耗尽)
   │          │
   │          ├──► Paused   ──► Running   (挂起/继续)
   │          │
   └──────────┴──► Canceled
        (重试退避)
```

- **Paused** 同时涵盖调用方暂停（`PauseTask`）和队列挂起（`Pause`，或调低并发）。队列 `Resume` 只会唤醒后者。
- 队列 `Wait` 在所有任务都无法推进时返回。被调用方暂停的任务会让队列一直等到它被继续或取消。

### 并发与槽位

挂起任务会**释放它占用的并发槽位**，同时把进度保留在磁盘上（通过对底层下载执行"停止再续传"，而不是"原地暂停"）。这在批量场景下很重要：被挂起的任务不会白白占着一条空闲连接。把 `Concurrency` 调到低于当前运行数时，**最近启动**的任务会被挂起，它们会在槽位空出时自动恢复。

## 状态机（单文件下载）

```text
        Start
Idle ─────────────► Running ◄────────────┐
                      │  ▲                │ Resume
             Pause    │  └────────────────┤
                      ▼                   │
                   Paused ────────────────┘
                      │
     Stop ────────────┼────────► Stopped   (可续传：再次 Start)
                      │
              Complete┴────────► Completed
                      │
             error ───┴────────► Failed
```

重配置类信号（`SetWorkers` / `SetConnections` / `SetSegments` / `SetConnectionsAndSegments`）不改变状态；它们会取消当前连接代次、重建切片计划，然后重新拉起连接池。在 Workers 总和模式下，manager 还会运行一个自适应慢分片拆分器，把明显滞后的分片一分为二，让更快的 worker 领走后半段（详见架构文档）。

## 进度

```go
p := d.Progress()
// p.State, p.Total, p.Downloaded, p.Percent,
// p.Speed, p.ETA, p.Connections, p.Segments, p.Workers,
// p.AcceptRanges, p.Err
```

`Progress` 是快照，可从任意 goroutine 安全读取。需要推送式更新时，设置 `Config.OnProgress`；回调由管理器的 ticker 触发（约 2 次/秒），且不得阻塞。

## 安全说明

本包会拒绝 host 为 `localhost`、回环、私有（RFC 1918）、链路本地、多播、未指定及其他保留地址的 URL，并且在**拨号时对解析出的实际地址再次校验**，因此通过校验的域名也无法 rebinding 到内网地址。只有在确实要从受信任的局域网或本机服务下载时，才设置 `AllowPrivateHost: true`。

## 下载测试对比（benchmark）

`go test -bench=. -benchtime=3s -count=3`，4 MiB 负载、本地 `httptest` 服务器（AMD Ryzen 7 5800H，Windows）。Workers 模式自动分配连接/切片，并在运行中拆分慢分片。

| Benchmark | 模式 | ns/op（3 次中位数） | vs 单连接 |
| --- | --- | --- | --- |
| `BenchmarkDownloadSingleConn` | 1 连接 / 1 切片 | ~18.7 ms | 1.0× |
| `BenchmarkDownloadMultiConn` | 8 连接 / 16 切片 | ~11.5 ms | **1.63×** |
| `BenchmarkDownloadWorkers` | `Workers=16`（自动） | ~12.3 ms | 1.52× |
| `BenchmarkDownloadWorkersSlowSegment` | `Workers=16` + 首范围慢速 | ~65.0 ms | — |

结论：多连接与 Workers 模式在本地回环上都比单连接快约 1.5–1.6 倍（广域网上差距更大）；Workers 模式有少量自动分配+拆分开销，但它是唯一能在某分片变慢时自动恢复的模式。

## 示例

- [`examples/basic`](examples/basic/main.go) —— 交互式 CLI，带实时进度条，以及暂停/继续/重配/重下/清缓存的命令。
- [`examples/batch`](examples/batch/main.go) —— 使用任务队列的批量下载器，带多行状态面板与单任务命令。
- [`examples/testsrv`](examples/testsrv/main.go) —— 本地支持 Range 的文件服务器，便于端到端试跑下载器。

```bash
# 终端 1：在本地提供某个目录
go run ./examples/testsrv -dir ./testdata -addr 127.0.0.1:18080

# 终端 2：从它下载（回环地址需要 allow-private）
go run ./examples/basic -allow-private -o ./file.bin -conn 8 -seg 16 \
    http://127.0.0.1:18080/file.bin

# 或者一次批量下载多个文件
go run ./examples/batch -allow-private -o ./downloads -c 3 -conn 8 \
    http://127.0.0.1:18080/a.bin http://127.0.0.1:18080/b.bin
```

## 设计原理

设计、单临时文件 + 字节区间元数据的方案，以及热改切片为何安全，见
[`doc/ARCHITECTURE.zh-CN.md`](doc/ARCHITECTURE.zh-CN.md)。

## 测试

```bash
go test ./...
```

测试覆盖：切片计划构建、重切片的进度守恒、URL/host 校验、元数据往返，
以及基于 `httptest` 的端到端下载，包括暂停/继续、运行时热重配、重下、清缓存、
跨实例续传和中止。

## 许可证

MIT —— 见 [LICENSE](LICENSE)。
