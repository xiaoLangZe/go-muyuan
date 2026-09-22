# go-muyuan

一个 HTTP 下载库。用一个下载器管理多个文件，它们共享一份连接预算；单个文件可以多线程分片下载。每个任务都有句柄，可以**暂停**、**继续**、**重新下载**、**取消/删除**，还可以订阅**事件流**实时拿进度。

## 项目结构

```
go-muyuan/
├── go.mod
├── README.md
├── .gitignore
├── internal/                外部项目无法导入
│   ├── engine/              传输引擎：单连接与分片并发两种模式共用一套生命周期
│   ├── filename/            文件名推导与净化
│   └── hostguard/           内网 / 保留地址拦截
└── pkg/
    └── manager/             唯一入口：下载器、任务、事件流
```

## 引入

模块路径目前是裸名 `go-muyuan`，无法直接 `go get`，需要在调用方的 `go.mod` 里用 `replace` 指过来：

```
require go-muyuan v0.0.0

replace go-muyuan => ../go-muyuan
```

```go
import "go-muyuan/pkg/manager"
```

## 快速开始

```go
m := manager.New(
    manager.WithThreads(16),  // 所有任务共享的连接预算
    manager.WithMaxFiles(3),  // 同时最多下载 3 个文件
    manager.WithDefaultDir("downloads"),
)
defer m.Close()

a, err := m.AddTask("downloads", "https://example.com/a.iso")
if err != nil {
    return err
}
b, err := m.AddTask("downloads", "https://example.com/b.iso",
    manager.WithFileName("renamed.iso"),          // 任务级配置，覆盖管理器默认值
    manager.WithProxy("socks5://127.0.0.1:1080"), // 这条任务走代理
    manager.WithHeader("Authorization", "Bearer ..."))
if err != nil {
    return err
}

// 实时进度：订阅事件流
for ev := range a.Events() {
    fmt.Printf("%s %.1f%% %d 条连接\n", ev.Status, ev.Progress.Percent, ev.Progress.Workers)
}

// 或者随时轮询
p := b.Progress()

a.Pause()   // 它占用的连接立刻分给其他文件
a.Resume()  // 有空闲连接时再拿回来
a.Restart()
a.Delete()  // 取消任务和删除文件是同一个操作
a.Wait()
```

`AddTask(destDir, url, opts...)` 的第一个参数是下载目录，留空则用 `WithDefaultDir` 设的。

## 连接怎么分

调度器每次都从头重算分配，规则只有两条，按顺序执行：

1. **先摊开**：每一个能跑的文件先各拿 1 条连接，最多摊到 `WithMaxFiles` 个文件、且不超过总预算。
2. **再加深**：剩下的连接在这些文件之间轮转均分。轮转天然给每个文件设了上限，不会有单个文件吃掉全部预算。

预算 2、3 个文件 → 前两个各 1 条，第三个排队；预算 6、2 个文件 → 每个 3 条。暂停某个文件会立刻释放它的连接，其他文件在下一轮调度接手。

注意 **1 条连接 = 单连接模式**：不预分配文件、不发 Range 请求。这是有意的，一条连接时分片只有开销没有收益。连接数 ≥ 2 且服务端支持 Range 时才会分片。

## 实时进度

两种方式，可同时使用：

**事件流** —— 每 200ms 最多一条进度事件，每次状态变更都发一条，任务结束发完终态事件后关闭 channel：

```go
for ev := range task.Events() {
    switch ev.Type {
    case manager.EventProgress: // 字节数、速度、百分比、连接数、切片大小
    case manager.EventStatus:   // queued / downloading / paused / completed / failed / deleted
    case manager.EventError:    // 以失败告终时的错误
    }
}
```

消费者不读不会拖住下载 —— 传输在独立协程上，只有事件发射协程会等。不再消费时请 `Delete` 任务或 `Close` 管理器来释放它。

**轮询** —— `Progress()` / `Info()` / `Status()` 随时可读，值就是 `Progress` 的字段。事件是 `Progress` 的另一种投递方式，两者数据一致。

进度语义：`Downloaded` 只统计**已经落盘完成**的部分，在途未写完的不计入，所以分片模式下会比单连接模式略微滞后一点（粒度 = 64 KiB）。

## 代理

```go
manager.WithDefaultProxy("http://user:pass@proxy:8080")   // 全部任务
manager.WithProxy("socks5://user:pass@proxy:1080")        // 单条任务
```

支持 `http://`、`https://`、`socks5://` 三种 scheme，账号密码写在 URL 的 userinfo 里。SOCKS5 走 Go 标准库原生支持，模块保持零第三方依赖。分片模式下每一条连接都经代理，不需要额外配置。其他 scheme 在 `AddTask` 时直接报 `ErrInvalidProxy`。

**代理与安全防护的关系**，这一条必须说清楚：

- **请求前的目标校验照常生效**：目标 URL 的 host 仍会被解析并校验，环回、私网、链路本地、保留段一律拦掉 —— 配了代理也不例外。
- **建连时校验的是代理地址**：连接实际拨向代理，所以内网代理需要用 `WithAllowUnsafeHosts(true)` 放行。这个开关是总开关，它会同时关掉目标校验。
- **目标解析出的 IP 在代理模式下无法二次校验**：解析域名的是代理，客户端看不到目标 IP。这是协议本身决定的，第一层校验仍在。

## 线程与切片

- `WithThreads(n)`：管理器级，全部任务共享的连接预算，运行中可用 `SetThreads` 热调整。
- `WithMaxFiles(n)`：管理器级，同时下载的文件数上限。调小会把超出的运行中任务暂停（保留已下载字节），调大或预算释放后自动继续。
- 切片大小**自动调整**：起始按 `文件大小 / (连接数 × 4)` 估算并夹在 `[256 KiB, 16 MiB]`，运行中按实测耗时向「每片约 1.5 秒」靠拢（每次调整不超过一倍，避免震荡），收尾把剩余量按连接数均分。要固定用 `WithChunkSize`，要改上下界用 `WithChunkBounds`。

## 任务方法

| 方法 | 说明 |
| --- | --- |
| `Pause()` | 停传输并保留已收字节；分片模式下在途分片被放弃、恢复时重下；连接立刻让给其他文件 |
| `Resume()` | 继续；还要等调度器给连接 |
| `Restart()` | 丢弃已收字节重新下载，任何状态（含已完成、已删除）都可调用 |
| `Delete()` | 停传输并删除成品文件与 `.part` 文件；**取消任务就是它**，两者只差在删不删文件 |
| `Events()` | 订阅事件流，任务结束时 channel 关闭 |
| `Wait()` / `WaitContext(ctx)` | 阻塞到终态；排队中和暂停中都不算终态 |
| `Status()` | `pending` / `queued` / `downloading` / `paused` / `completed` / `failed` / `deleted` |
| `Progress()` / `Info()` | 字节数、速度、ETA、百分比、连接数、当前切片大小 |
| `FilePath()` / `PartPath()` / `Dir()` | 成品路径 / 传输中的临时路径 / 目录 |
| `Workers()` | 当前分配到的连接数 |
| `RangeMode()` | 服务端是否支持 Range：`unknown` / `supported` / `unsupported` |
| `Err()` | 结束任务的错误，排队中、运行中或暂停时为 nil |

管理器另有 `Threads()` / `SetThreads()`、`MaxFiles()` / `SetMaxFiles()`、`Tasks()`、`Task(id)`、`Wait()`、`Close()`。

## 行为说明

**半成品不会被误认成成品。** 传输期间写 `.part`，全部收完并 `fsync` 后才改名为最终文件。

**分片模式下 `.part` 会预先创建成完整大小**再并行写入，所以暂停时它的**文件大小**等于整个文件，而不是已下载量；单连接模式下文件大小就是已下载量。`Progress().Downloaded` 始终是已下载量。

**续传不依赖服务端支持 `If-Range`。** 分片模式用位图记录哪些 64 KiB 分片已完成，暂停时在途分片直接不置位、恢复时重下那一段。服务端不支持 Range 时，`Resume` 会从头下载，而不是拼出损坏的文件。

**失败会重试。** 5xx、连接中断、响应体提前结束等瞬态失败重试（默认 3 次，退避翻倍至 30 秒上限），408/425/429 也算瞬态；4xx 不重试。分片模式下每个分片各自重试。连续 `WithStallTimeout`（默认 60 秒）没收到任何字节即判定停滞并重试。

**重名不会覆盖。** 目录已有同名文件时默认追加序号（`report.pdf`、`report_1.pdf`…）。要覆盖用 `WithOverwrite(true)`。

**默认拒绝内网目标**，两层校验：发请求前校验 host，建连时再校验解析出的地址。分片模式下每条连接都走同一个带防护的 dialer，不会绕过。已知可信的目标用 `WithAllowUnsafeHosts(true)` 放行 —— 用 `httptest` 做测试时需要它。

**进度回调不能在回调里调用控制方法。** `WithProgress` 的回调跑在传输协程上，在里面调用同一任务的 `Pause`/`Wait`/`Delete` 会自锁死。要跟随进度优先用 `Events()`。

## 测试

```
go test ./...
```

75 个测试：

- `internal/engine`（56 个）：单连接路径的完整行为（命名与净化、重名加序号、覆盖、暂停续传含不支持 Range 的分支、重下、删除、重试与 4xx 不重试、停滞超时、context 取消、并发控制操作压测）；分片路径（并发数等于配置的连接数、分片区间**互不重叠且恰好覆盖整个文件**、切片固定与自动调整、连接数运行中增减、单连接升级为分片、小文件不分片）；代理（HTTP 转发代理、测试内实现的最小 SOCKS5 服务端、带账号密码、分片经代理、非法 scheme、内网代理被拦、配了代理仍拦内网目标）；位图认领与切片调整的单元测试。
- `pkg/manager`（18 个）：全局连接预算不被突破、最大同时文件数、**广度优先分配**（有文件排队时已运行文件不得加深）、空闲连接下沉、暂停让出连接、排队任务在预算释放后启动、预算热调整、任务控制操作、任务级文件名、关闭时所有任务可等待、内网地址拦截；事件流（进度与状态事件、节流、错误事件、终态后关闭、慢消费者、订阅后不读不泄漏 goroutine）。
- `internal/filename`（1 个）：文件名净化。
