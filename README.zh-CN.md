# go-muyuan

一个 Go 的 HTTP 下载库。一个下载器管理多个文件，它们共享一份连接预算；单个文件可以同时用多条连接下载。每个文件都是一个任务、都有句柄，可以**暂停**、**继续**、**重新下载**、**取消/删除**，进度既能订阅事件流实时拿到，也能随时轮询。

[English](README.md)

---

## 目录

- [特性](#特性)
- [项目结构](#项目结构)
- [引入](#引入)
- [快速开始](#快速开始)
- [核心概念](#核心概念)
- [连接怎么分](#连接怎么分)
- [分片与切片大小](#分片与切片大小)
- [代理](#代理)
- [实时进度](#实时进度)
- [API 参考](#api-参考)
  - [管理器选项](#管理器选项)
  - [任务选项](#任务选项)
  - [Manager 方法](#manager-方法)
  - [Task 方法](#task-方法)
  - [事件](#事件)
  - [类型](#类型)
  - [错误](#错误)
- [行为细节](#行为细节)
- [安全模型](#安全模型)
- [常见用法](#常见用法)
- [已知限制与取舍](#已知限制与取舍)
- [测试](#测试)

---

## 特性

- **单个文件多连接**：文件被切成若干片并行取，一条慢连接不会拖住整个传输。
- **跨文件共享连接预算**：管理器把一份连接池按广度优先的规则分给正在跑的文件（见[下文](#连接怎么分)）。
- **切片大小自动调整**：每片的长度向「约 1.5 秒一片」的目标靠拢，服务端快就发长请求、慢就发短请求。
- **实时进度不用轮询**：事件流带节流后的进度、每一次状态变更、以及任务结束时的错误。
- **代理支持**：`http`、`https`、`socks5`，可带账号密码，管理器级或任务级配置，零第三方依赖。
- **可续传**：暂停后继续从磁盘上已有的字节往下下。完成度用位图记录，因此**不依赖服务端响应 `If-Range`**。
- **不会把半成品当成成品**：字节先写 `.part`，全部收完 flush + fsync 后才改名。
- **瞬态失败会重试**：带退避；连接停滞会被检测并重试。
- **内网地址被拒绝**：发请求前校验一次，建连时再校验一次。
- **零第三方依赖**：只用标准库。

## 项目结构

```
go-muyuan/
├── go.mod
├── README.md
├── README.zh-CN.md
├── .gitignore
├── *.go                     公开包：import "github.com/xiaoLangZe/go-muyuan"
└── internal/                外部项目无法导入
    ├── engine/              传输引擎：单连接与分片并发两种模式共用一套生命周期
    ├── filename/            文件名推导与净化
    └── hostguard/           内网 / 保留地址拦截
```

调用方需要的一切都在根包里。两个 `internal` 包是实现细节，外部项目无法导入。

## 引入

```
go get github.com/xiaoLangZe/go-muyuan
```

然后：

```go
import "github.com/xiaoLangZe/go-muyuan"
```

仓库还没发布时，在调用方的 `go.mod` 里用 `replace` 指向本地检出：

```
require github.com/xiaoLangZe/go-muyuan v0.0.0

replace github.com/xiaoLangZe/go-muyuan => ../go-muyuan
```

## 快速开始

```go
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/xiaoLangZe/go-muyuan"
)

func main() {
	m := muyuan.New(
		muyuan.WithThreads(16),           // 所有任务共享的连接预算
		muyuan.WithMaxFiles(3),           // 同时下载的文件数
		muyuan.WithDefaultDir("downloads"),
	)
	defer m.Close()

	a, err := m.AddTask("downloads", "https://example.com/a.iso")
	if err != nil {
		log.Fatal(err)
	}

	b, err := m.AddTask("downloads", "https://example.com/b.iso",
		muyuan.WithFileName("renamed.iso"),           // 任务级覆盖
		muyuan.WithProxy("socks5://127.0.0.1:1080"),  // 这条任务走代理
		muyuan.WithHeader("Authorization", "Bearer ..."))
	if err != nil {
		log.Fatal(err)
	}

	// 实时看其中一个
	go func() {
		for ev := range a.Events() {
			fmt.Printf("[%s] %s %.1f%% 用了 %d 条连接\n",
				ev.TaskID[:6], ev.Status, ev.Progress.Percent, ev.Progress.Workers)
		}
	}()

	// 另一个随时轮询
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		p := b.Progress()
		fmt.Printf("b: %.1f%% (%d/%d 字节, %d B/s)\n", p.Percent, p.Downloaded, p.Total, p.Speed)
		if b.Status() == muyuan.StatusCompleted {
			break
		}
	}

	a.Pause()   // 它占用的连接立刻分给其他文件
	a.Resume()  // 有空闲连接时再拿回来
	a.Restart() // 从 0 重下
	a.Delete()  // 停传输并删除文件

	if err := a.Wait(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("saved to", a.FilePath())
}
```

`AddTask(destDir, rawURL string, opts ...TaskOption)` 第一个参数是下载目录，按三分支解析：绝对路径原样使用；相对路径拼接到根目录；留空则回退到 `WithDefaultDir`（同样按此规则解析，若它也为空则用根目录本身）。根目录默认是可执行文件所在目录，因此不设任何选项时，下载文件落在 exe 同级目录。

## 核心概念

**管理器（Manager）** —— 持有连接预算、文件数上限和调度器。由 `New` 创建，可并发使用。

**任务（Task）** —— 一个文件。`AddTask` 立即返回，任务进入队列，预算有空位时开跑。任务就是句柄：对一次下载的所有操作都通过它。

**连接预算** —— 管理器在所有任务上同时允许的在途 HTTP 连接总数。`WithThreads` 设置，`SetThreads` 运行中调整。

**切片（block / piece）** —— 一条连接一次请求取的字节区间。长度自动选择并在传输过程中调整。

**粒度（granule）** —— 完成度记录的分辨率，64 KiB。粒度位图记录磁盘上有什么。进度按已完成粒度计数，所以暂停时不需要为「写了一半的片」做任何补偿计算。

## 连接怎么分

调度器每次变更都从头重算一遍分配，规则只有两条，按顺序执行：

1. **先摊开**：每一个能跑的文件先各拿 1 条连接，最多摊到 `WithMaxFiles` 个文件、且不超过总预算。
2. **再加深**：剩下的连接在这些文件之间轮转均分。轮转天然给每个文件设了上限，不会有单个文件吃掉全部预算。

| 预算 | 文件数 | 结果 |
| --- | --- | --- |
| 2 | 3 | 前两个各 1 条，第三个排队 |
| 6 | 2 | 每个 3 条 |
| 4 | 4 | 每个 1 条 |
| 6 | 4 | 两个文件各 2 条，两个各 1 条 |

几个值得知道的推论：

- **只有 1 条连接的任务会整文件流式下载**，不发 Range 请求。一条连接时分片只有开销没有收益。只有预算 ≥ 2 且服务端支持 Range 时才会分片。
- **暂停一个文件会立刻释放它的连接**，下一轮调度就交给其他文件；`Resume` 让它重新排队。
- **`WithMaxFiles` 限制的是启动**。调大它会让排队的任务开跑；调小到低于正在运行的文件数，会把放不下的那些暂停（保留已下载字节），等限额允许时继续。
- **等待轮次的任务报 `StatusQueued`**，无论它是从没启动过、还是连接被让给了别人。它是在排队等，不是被你停了。

## 分片与切片大小

引擎**不预先**把文件切成固定分片。预切会在开头就把连接数和分片边界定死，而这恰恰是[动态分配](#连接怎么分)必须保持灵活的东西。

所以分成两层：

- **粒度（granule）**：64 KiB，完成度位图的分辨率。10 GB 文件只需要 20 KB 位图。
- **切片（block）**：一个 worker 一次认领的长度，是粒度的整数倍，在认领那一刻决定，因此可以随时变化而不扰动位图。

worker 循环执行：认领下一段连续空闲粒度 → 发一个带 Range 的请求 → 按偏移写入 → 标记这些粒度已完成。被放弃的认领（暂停、请求失败）只需清掉自己的位，那段之后会被重新取。

**切片长度怎么定：**

- 第一片是 `文件大小 / (连接数 × 4)`，夹在 `[256 KiB, 16 MiB]`。
- 运行中用每片的**实测耗时**把长度朝「每片约 1.5 秒」的目标拉。每次调整幅度不超过一倍，结果也不越出配置的上下界，所以一次异常响应不会让切片崩掉或爆掉。
- 收尾时把剩余字节按连接数分给各 worker，让它们一起结束，而不是留下一个 worker 磨最后一大段。

用 `WithChunkSize` 固定长度（同时关闭自动调整），用 `WithChunkBounds` 改上下界。

## 代理

```go
muyuan.WithDefaultProxy("http://user:pass@proxy.example:8080") // 全部任务
muyuan.WithProxy("socks5://user:pass@proxy.example:1080")      // 单条任务
```

支持 `http`、`https`、`socks5` 三种 scheme，账号密码写在 URL 的 userinfo 里。SOCKS5 由标准库的 HTTP transport 直接处理，模块依旧没有第三方依赖。分片模式下每一条连接都经代理，不需要额外配置。其他 scheme 会在 `AddTask` 时被 `ErrInvalidProxy` 拒绝。

**代理对安全防护的影响** —— 这一段很重要：

- **请求前的目标校验照常运行**。目标 URL 的 host 会被解析并对照黑名单检查，环回、私网、链路本地、保留段都包括。配了代理并不会关掉它。
- **建连时校验的是代理地址**。连接实际拨向代理，所以内网代理需要 `WithAllowUnsafeHosts(true)`（管理器级是 `WithDefaultAllowUnsafeHosts`）。这个开关是**总开关**：它会把目标校验也一起关掉。
- **用了代理就无法校验目标解析出的地址**。解析域名的是代理，客户端看不到目标 IP。这是协议本身的属性，不是功能缺失。剩下的是请求前那一层校验。
- **`WithHTTPClient` 优先**。调用方自带的 client 拥有自己的 transport，`WithProxy` 对它不生效。

## 实时进度

两种方式，可同时使用，读到的值一致。

### 事件流

```go
for ev := range task.Events() {
	switch ev.Type {
	case muyuan.EventProgress:
		// ev.Progress：字节数、速度、ETA、百分比、连接数、切片大小
	case muyuan.EventStatus:
		// ev.Status：queued / downloading / paused / completed / failed / deleted
	case muyuan.EventError:
		// ev.Err：以失败告终的原因
	}
}
```

- channel 在**首次调用 `Events()`** 时创建，在任务结束、**且报告结束的那条事件已经投递之后关闭**，所以 `range` 循环能从开始一路跑到结束。
- **进度事件最多每 200ms 一条**；状态变更每条都发，不节流。
- **消费者不读不会拖住下载**。传输在独立协程上跑，只有事件发射协程会等 channel；任务被删除或管理器关闭时它会被释放。不再消费时要 `Delete` 任务或 `Close` 管理器，而不是放着不管。
- **比节流还快就结束的任务不会产生进度事件** —— 只有状态事件，但里面带的 `Progress` 是完整的。在本地快速服务器上这是常态。

### 轮询

`Progress()`、`Info()`、`Status()` 随时可调，值与事件里带的完全一致。

```go
p := task.Progress()
fmt.Println(p.Downloaded, p.Total, p.Percent, p.Speed, p.ETA, p.Workers)
```

`Info()` 是超集：标识、路径、状态、Range 模式、错误，以及同样的统计量。

## API 参考

### 管理器选项

传给 `muyuan.New`，设定所有任务的默认值。

| 选项 | 默认值 | 含义 |
| --- | --- | --- |
| `WithThreads(n int)` | 4 | 全部任务共享的在途连接数。 |
| `WithMaxFiles(n int)` | 2 | 同时下载的文件数。 |
| `WithRootDir(dir string)` | exe 所在目录 | 相对下载路径的根目录。相对值拼接到 exe 目录，绝对值原样使用。 |
| `WithDefaultDir(dir string)` | 根目录 | 默认任务目录，按 `destDir` 同样的规则相对根目录解析。 |
| `WithDefaultProxy(rawURL string)` | 无 | 每个任务的代理。 |
| `WithDefaultHeader(key, value string)` | 无 | 每个任务的请求头，可重复调用。 |
| `WithDefaultHeaders(map[string]string)` | 无 | 一次加多个请求头。 |
| `WithDefaultRetries(n int)` | 3 | 失败后的重试次数。 |
| `WithDefaultRetryDelay(d time.Duration)` | 1s | 首次重试前的等待，逐次翻倍至 30 秒上限。 |
| `WithDefaultResponseTimeout(d time.Duration)` | 30s | 等待响应头的上限。 |
| `WithDefaultStallTimeout(d time.Duration)` | 60s | 多久没有收到字节就判定停滞。0 关闭。 |
| `WithDefaultBufferSize(n int)` | 64 KiB | 单连接路径的拷贝缓冲区。 |
| `WithDefaultChunkSize(n int64)` | 自动 | 固定切片长度，关闭自动调整。 |
| `WithDefaultChunkBounds(min, max int64)` | 256 KiB / 16 MiB | 自动切片长度的上下界。 |
| `WithDefaultHTTPClient(c *http.Client)` | 内建 | 每个任务使用的 client。 |
| `WithDefaultAllowUnsafeHosts(allow bool)` | false | 允许本机与私网目标。 |
| `WithDefaultProgress(f func(Progress))` | 无 | 每个任务的进度回调。 |

### 任务选项

传给 `AddTask`，**只对那一条任务**覆盖管理器的默认值。

| 选项 | 默认值 | 含义 |
| --- | --- | --- |
| `WithFileName(name string)` | 来自服务端或 URL | 写入的文件名。 |
| `WithOverwrite(bool)` | false | 覆盖已有文件，而不是另取一个空闲名字。 |
| `WithHeader(key, value string)` | 无 | 这条任务的请求头，可重复调用。 |
| `WithHeaders(map[string]string)` | 无 | 一次加多个请求头。 |
| `WithRetries(n int)` | 管理器默认值 | 失败后的重试次数。 |
| `WithRetryDelay(d time.Duration)` | 管理器默认值 | 首次重试前的等待。 |
| `WithResponseTimeout(d time.Duration)` | 管理器默认值 | 等待响应头的上限。 |
| `WithStallTimeout(d time.Duration)` | 管理器默认值 | 多久没收到字节判定停滞。 |
| `WithBufferSize(n int)` | 管理器默认值 | 单连接路径的拷贝缓冲区。 |
| `WithChunkSize(n int64)` | 管理器默认值 | 固定切片长度。 |
| `WithChunkBounds(min, max int64)` | 管理器默认值 | 自动切片长度的上下界。 |
| `WithHTTPClient(c *http.Client)` | 管理器默认值 | 这条任务使用的 client。 |
| `WithAllowUnsafeHosts(bool)` | 管理器默认值 | 允许本机与私网目标。 |
| `WithProxy(rawURL string)` | 管理器默认值 | 这条任务的代理。 |
| `WithProgress(f func(Progress))` | 管理器默认值 | 这条任务的进度回调。 |

**故意没有任务级的连接数配置**。调度器负责分配，并且每轮都会重写这个数字，所以任务级的值只会被覆盖。要调总预算用管理器的 `WithThreads`。

两套选项的命名是刻意区分的：`WithDefault*` 作用于每个任务，不带前缀的只作用于一条。Go 不允许同一个包里有两个同名函数，前缀就是它们的区分方式。

### Manager 方法

| 方法 | 含义 |
| --- | --- |
| `AddTask(destDir, rawURL string, opts ...TaskOption) (*Task, error)` | 排队一次下载。立即校验 URL 与代理。 |
| `Threads() int` / `SetThreads(n int)` | 读 / 改连接预算。 |
| `MaxFiles() int` / `SetMaxFiles(n int)` | 读 / 改文件数上限。 |
| `Tasks() []*Task` | 所有任务的快照，按加入顺序。 |
| `Task(id string) *Task` | 按标识查一个，没有则返回 nil。 |
| `Wait()` | 阻塞到所有任务进入终态。 |
| `Close()` | 停掉调度器和所有任务。正在跑的传输被取消，`.part` 文件留在磁盘上。可重复调用。 |

### Task 方法

| 方法 | 含义 |
| --- | --- |
| `Pause() error` | 停传输、保留字节、让出连接。暂停已暂停的任务返回 nil。 |
| `Resume() error` | 让任务重新排队等连接。 |
| `Restart() error` | 丢弃已收字节，从 0 重下。任何状态都可调用，含已完成、已删除。 |
| `Delete() error` | 停传输并删除成品文件与 `.part` 文件。**取消任务就是它** —— 两者只差在删不删文件。 |
| `Events() <-chan Event` | 订阅事件流。任务结束时 channel 关闭。 |
| `Wait() error` | 阻塞到终态；排队中和暂停中都不算终态。 |
| `WaitContext(ctx) error` | 带超时的 `Wait`。 |
| `Done() <-chan struct{}` | 任务进入终态时关闭。重下的任务会换一个新 channel。 |
| `Status() Status` | 当前状态。 |
| `Progress() Progress` | 统计快照。 |
| `Info() Info` | 完整快照，含标识与路径。 |
| `Err() error` | 结束任务的原因；排队中、运行中、暂停时为 nil。 |
| `ID() string` | 标识符，跨进程唯一。 |
| `URL() string` / `Dir() string` | 目标 URL 与下载目录。 |
| `FilePath() string` / `PartPath() string` | 成品路径 / 传输中的临时路径。 |
| `Workers() int` | 当前分配到的连接数。 |
| `RangeMode() RangeMode` | 服务端是否支持 Range。 |

### 事件

```go
type EventType int

const (
	EventProgress EventType = iota // 字节到达或速率变化
	EventStatus                    // 任务状态变更
	EventError                     // 任务以失败告终
)

type Event struct {
	TaskID   string
	Type     EventType
	Status   Status
	Progress Progress
	Err      error     // 仅 EventError 时非 nil
	At       time.Time
}
```

### 类型

`Status` —— `StatusPending`、`StatusQueued`、`StatusDownloading`、`StatusPaused`、`StatusCompleted`、`StatusFailed`、`StatusDeleted`。`String()` 给出小写名字。

`RangeMode` —— `RangeUnknown`、`RangeSupported`、`RangeUnsupported`。由 `RangeMode()` 报告；`RangeSupported` 是并行取片的前提。

`Progress`：

```go
type Progress struct {
	Total      int64         // 完整大小，服务端未告知时为 0
	Downloaded int64         // 已可靠落盘的字节数
	Speed      int64         // 字节/秒，按最近一个采样窗口计算
	ETA        time.Duration // 预计剩余时间，无法推算时为 0
	Percent    float64       // Downloaded / Total，未知时为 0
	Elapsed    time.Duration // 净传输耗时，不含暂停
	Workers    int           // 分配到的连接数
	BlockSize  int64         // 当前切片长度，未分片时为 0
}
```

`Info` —— `ID`、`URL`、`FilePath`、`PartPath`、`Status`、`RangeMode`、`Err`、`Total`、`Downloaded`、`Speed`、`Workers`、`BlockSize`、`Elapsed`，外加一个 `Percent()` 方法。

### 错误

全部是哨兵值，可用 `errors.Is` 判断。任务返回的失败在适用时都会包裹其中之一。

| 错误 | 含义 |
| --- | --- |
| `ErrInvalidURL` | 目标不是绝对的 `http`/`https` URL，或带了账号密码。 |
| `ErrInvalidProxy` | 代理不是带 host 的 `http`/`https`/`socks5` URL。 |
| `ErrBlockedHost` | host（目标或代理）解析到环回、私网、链路本地、组播或其他保留地址。 |
| `ErrNotPaused` | 对未暂停的任务调用 `Resume`。 |
| `ErrNotDownloading` | 对已不在运行的任务调用 `Pause`。 |
| `ErrCompleted` | 对已完成的任务做操作。用 `Restart`。 |
| `ErrDeleted` | 对已删除的任务做操作。 |
| `ErrStalled` | 停滞超时内没有收到数据。 |
| `ErrClosed` | 对已关闭的管理器做操作。 |
| `HTTPStatusError` | 响应状态不可用。含 `StatusCode`、`Status`、`URL` 和 `Retryable()` 方法。 |

```go
if errors.Is(err, muyuan.ErrBlockedHost) {
	// URL 指向了下载器拒绝去的地方
}
var status *muyuan.HTTPStatusError
if errors.As(err, &status) && status.StatusCode == 404 {
	// 文件不存在
}
```

## 行为细节

**半成品永远不会被当成成品。** 字节写到 `FilePath()+".part"`，flush 并 `fsync` 之后才改名成最终文件。最终路径不会有别的写入者。

**分片模式下 `.part` 会预先创建成完整大小**，再用定位写入原地填充。所以暂停时它的**文件大小**是整个文件的大小，不是已下载量 —— 已下载量看 `Progress().Downloaded`。单连接模式下 `.part` 的大小就是已下载量。两种模式里 `Downloaded` 的含义一致：已可靠落盘的字节，在途未写完的不计入。

**续传不依赖服务端响应 `If-Range`。** 完成度位图知道哪些粒度已经在磁盘上。暂停时会清掉在途片的位，恢复时重新取那一段 —— 不需要任何补偿计算，也不依赖 validator。服务端完全不支持 Range 时，`Resume` 从头下载，而不是拼出一个损坏的文件。

**瞬态失败会重试。** 5xx、连接被断开、响应体提前结束都会重试，默认 3 次，等待时间逐次翻倍到 30 秒上限。408、425、429 也算瞬态，其他 4xx 不算。分片模式下每个片各自重试。

**停滞会被检测。** 如果连续停滞超时（默认 60 秒）没有收到任何字节，这次传输按瞬态失败处理并重试。这就是「连接不关闭但也不发数据」不会永久挂住下载的原因。

**已有文件不会被覆盖。** 目标名字被占用时会追加序号：`report.pdf`、`report_1.pdf`、`report_2.pdf`。要覆盖用 `WithOverwrite(true)`。

**文件名会被净化。** 来自服务端 `Content-Disposition` 或 URL 的名字会被裁到只剩最后一段路径，Windows 不接受的字符被替换，末尾的点和空格被去掉，命中设备保留名会加前缀。`../../etc/passwd` 变成 `passwd`。超过 200 字节的名字会被截断并保留扩展名。

**重下随时可用。** `Restart` 在任何状态都能工作，包括已完成和已删除：丢弃字节、清空计数、开一次全新的传输。

**传给管理器的 context 约束整个管理器。** `Close` 会取消它，从而停掉正在跑的传输；当时处于排队或暂停状态的任务报 `ErrClosed`。

## 安全模型

交给下载器的 URL 可能来自配置文件、用户输入或远端 API，因此它可能指向本机或只有内网才能访问的服务。两层机制拦住它：

1. **发出请求之前**，解析目标的 host，并检查它解析出的全部地址。解析到被拦地址的主机名会被拒绝，因为最终连哪个地址不由客户端决定。
2. **建立连接时**，再检查一次地址。这堵住了「同个域名第一次解析到公网地址、第二次解析到内网地址」的时间窗（DNS rebinding）。分片模式下每条连接都走同一个带防护的 dialer，没有绕过的路径。

被拦的集合覆盖环回、私网、链路本地（单播与组播）、未指定、组播，以及这些特殊用途网段：`0.0.0.0/8`、`100.64.0.0/10`、`192.0.0.0/24`、`192.0.2.0/24`、`192.88.99.0/24`、`198.18.0.0/15`、`198.51.100.0/24`、`203.0.113.0/24`、`240.0.0.0/4`、`64:ff9b::/96`、`100::/64`、`2001:db8::/32`、`2002::/16`。IPv4-mapped 的 IPv6 地址按它承载的 IPv4 地址判断。

URL 里带账号密码会被拒绝（`https://user:pass@host/`），因为它们会进入日志和错误信息；需要鉴权请用 `WithHeader` 传 `Authorization`。

`WithAllowUnsafeHosts(true)` 会关掉上述**全部**防护，用于已知可信的目标 —— 本机环回上的测试服务器、内网里的代理。它是总开关。

## 常见用法

**下单个文件并等它完成。**

```go
m := muyuan.New(muyuan.WithThreads(8), muyuan.WithMaxFiles(1))
defer m.Close()

t, err := m.AddTask("downloads", url)
if err != nil {
	return err
}
if err := t.Wait(); err != nil {
	return err
}
fmt.Println(t.FilePath())
```

**查看所有任务的总览。**

```go
for _, t := range m.Tasks() {
	p := t.Progress()
	fmt.Printf("%-30s %6.1f%%  %s\n", filepath.Base(t.FilePath()), p.Percent, t.Status())
}
```

**带超时等全部完成。**

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()
for _, t := range m.Tasks() {
	if err := t.WaitContext(ctx); err != nil {
		return err
	}
}
```

**失败后不丢字节地重试。**

```go
if t.Status() == muyuan.StatusFailed {
	t.Resume() // 从磁盘上已有的部分继续
}
```

**从 0 重下。**

```go
t.Restart()
```

**给更紧急的文件腾出连接。**

```go
big.Pause()      // 它的连接立刻转给其他文件
urgent.Resume()  // 有空闲时再拿回来
```

**给单条任务配代理和鉴权头。**

```go
t, err := m.AddTask(dir, url,
	muyuan.WithProxy("http://user:pass@proxy.example:3128"),
	muyuan.WithHeader("Authorization", "Bearer "+token))
```

**固定切片长度，应对不喜欢大请求的服务端。**

```go
m := muyuan.New(muyuan.WithThreads(8), muyuan.WithDefaultChunkSize(512<<10))
```

## 已知限制与取舍

- **配了代理就无法校验目标的解析地址。** 解析域名的是代理，客户端看不到目标 IP。请求前那层校验仍在。见[代理](#代理)。
- **`WithAllowUnsafeHosts` 是总开关。** 它没法做到「放行环回上的代理，同时仍然拒绝私网目标」—— 那需要两个独立的开关。
- **没有任务级的连接数配置。** 分配归调度器所有，每轮都会重写。要调就用总预算。
- **暂停会丢弃在途的片。** 每条连接最多一片会在恢复时重取。这是「以位图为唯一事实来源」的代价，片长较小时上界是每条连接 64 KiB，片长大时上界就是片长本身。
- **进度按已完成片计数**，所以会比在途的工作滞后，上界是每条连接一片。
- **`Workers` 报告的是分配量，不是实际开着的连接数。** 它是调度器分配的数字，引擎会照办，但最多用到「文件能切出的片数」那么多。1 MiB 的文件配 16 条连接预算时报 16，实际只开 4 条 —— 因为最小切片长度只够切 4 片。没有浪费（多余的 worker 会立刻退出），但这个数字是预算而不是实测值。
- **一个事件订阅占一个协程。** 它由首次 `Events()` 调用启动，所以没人订阅的任务零开销。订阅了却不再读的调用方应该 `Delete` 或 `Close` 来释放它。
- **没有实现跨进程续传。** 崩溃留下的 `.part` 能在同一次运行内续上，但没有附带文件记录位图，所以新进程会从头下。
- **还没有 `LICENSE` 文件。** 发布前记得补。

## 测试

```
go test ./...
```

81 个测试，全部通过：

- **`internal/engine`（56 个）** —— 单连接路径的完整行为：文件名推导与净化、序号避让、覆盖、暂停续传（含服务端不支持 Range 的分支）、重下、删除、重试与 4xx 不重试、停滞超时、context 取消，以及两个并发压测（把全部控制操作同时猛打）。分片路径：并发数等于配置的连接数、分片请求**恰好不重叠地覆盖整个文件**、固定与自动切片长度、传输过程中增减连接数、单连接升级为分片、小文件不分片、服务端忽略 Range 时的回退。代理：HTTP 转发代理、测试内实现的最小 SOCKS5 服务端、账号密码鉴权、经代理分片、非法 scheme、代理地址被拦、以及**配了代理仍然拦内网目标**。另有位图与切片调整器的单元测试。
- **根包（18 个）** —— 全局连接预算不被突破、同时文件数上限、**广度优先分配**（有文件排队时已运行文件不得加深）、空闲连接下沉到已运行文件、暂停让出连接、排队任务在预算释放后启动、预算热调整、任务控制操作、任务级文件名、关闭后所有任务可等待、内网地址拦截。事件流：进度与状态投递、节流、错误事件、终态后关闭、慢消费者、订阅后不读不泄漏协程。
- **`internal/filename`（1 个）** —— 文件名净化。
- **`internal/hostguard`（6 个）** —— 全部被拦地址类别（环回、私网、链路本地、组播、未指定、保留网段、IPv4-mapped IPv6）与可路由地址放行、host 输入裁剪、解析器路径，以及建连时二次校验在不建立连接的情况下拒绝地址。
