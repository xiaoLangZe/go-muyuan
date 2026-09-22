# API 参考

[English](API-Reference) · **中文**

`github.com/xiaoLangZe/go-muyuan` 导出的全部内容。包名是 `muyuan`。

## 管理器选项

传给 `New`，设定所有任务的默认值。

| 选项 | 默认值 | 含义 |
| --- | --- | --- |
| `WithThreads(n int)` | 4 | 所有任务同时的在途连接数 |
| `WithMaxFiles(n int)` | 2 | 同时下载的文件数 |
| `WithDefaultDir(dir string)` | `""` | 任务写入的目录 |
| `WithRootDir(dir string)` | 可执行文件所在目录 | 相对路径解析所基于的根 |
| `WithDefaultProxy(rawURL string)` | 无 | 每个任务的代理 |
| `WithDefaultHeader(key, value string)` | 无 | 每个任务的请求头，可重复 |
| `WithDefaultHeaders(map[string]string)` | 无 | 一次加多个请求头 |
| `WithDefaultRetries(n int)` | 3 | 失败后的重试次数 |
| `WithDefaultRetryDelay(d time.Duration)` | 1s | 首次重试前的等待，逐次翻倍至 30s |
| `WithDefaultResponseTimeout(d time.Duration)` | 30s | 等待响应头的上限 |
| `WithDefaultStallTimeout(d time.Duration)` | 60s | 多久没收到字节判定停滞；0 关闭 |
| `WithDefaultBufferSize(n int)` | 64 KiB | 单连接路径的拷贝缓冲区 |
| `WithDefaultChunkSize(n int64)` | 自动 | 固定切片长度，关闭自动调整 |
| `WithDefaultChunkBounds(min, max int64)` | 256 KiB / 16 MiB | 自动切片长度的上下界 |
| `WithDefaultHTTPClient(c *http.Client)` | 内建 | 每个任务使用的 client |
| `WithDefaultAllowUnsafeHosts(allow bool)` | false | 允许本机与私网目标 |
| `WithDefaultProgress(f func(Progress))` | 无 | 每个任务的进度回调 |

## 任务选项

传给 `AddTask`，**只对那一条任务**覆盖管理器的默认值。

| 选项 | 默认值 | 含义 |
| --- | --- | --- |
| `WithFileName(name string)` | 来自服务端或 URL | 写入的文件名 |
| `WithOverwrite(bool)` | false | 覆盖已有文件，而不是另取空闲名 |
| `WithHeader(key, value string)` | 管理器默认值 | 请求头，可重复 |
| `WithHeaders(map[string]string)` | 管理器默认值 | 一次加多个请求头 |
| `WithRetries(n int)` | 管理器默认值 | 失败后的重试次数 |
| `WithRetryDelay(d time.Duration)` | 管理器默认值 | 首次重试前的等待 |
| `WithResponseTimeout(d time.Duration)` | 管理器默认值 | 等待响应头的上限 |
| `WithStallTimeout(d time.Duration)` | 管理器默认值 | 多久没收到字节判定停滞 |
| `WithBufferSize(n int)` | 管理器默认值 | 单连接路径的拷贝缓冲区 |
| `WithChunkSize(n int64)` | 管理器默认值 | 固定切片长度 |
| `WithChunkBounds(min, max int64)` | 管理器默认值 | 自动切片长度的上下界 |
| `WithHTTPClient(c *http.Client)` | 管理器默认值 | 这条任务使用的 client |
| `WithAllowUnsafeHosts(bool)` | 管理器默认值 | 允许本机与私网目标 |
| `WithProxy(rawURL string)` | 管理器默认值 | 这条任务的代理 |
| `WithProgress(f func(Progress))` | 管理器默认值 | 这条任务的进度回调 |

两套选项的命名是刻意区分的：`WithDefault*` 作用于每个任务，不带前缀的只作用于一条。Go 不允许同一个包里有两个同名函数，前缀就是它们的区分方式。

**刻意没有任务级的连接数配置。** 分配归调度器所有，每轮都会重写，所以任务级的值只会被覆盖。要调总预算用 `WithThreads`。

## Manager

```go
func New(opts ...Option) *Manager

func (m *Manager) AddTask(destDir, rawURL string, opts ...TaskOption) (*Task, error)
func (m *Manager) Threads() int
func (m *Manager) SetThreads(threads int)
func (m *Manager) MaxFiles() int
func (m *Manager) SetMaxFiles(maxFiles int)
func (m *Manager) Tasks() []*Task
func (m *Manager) Task(id string) *Task
func (m *Manager) Wait()
func (m *Manager) Close()
```

- `AddTask` 立即校验 URL 与代理并把下载排队。`destDir` 为空时回退到 `WithDefaultDir`。
- `Tasks` 返回按加入顺序的快照。
- `Task(id)` 找不到时返回 nil。
- `Wait` 阻塞到所有任务进入终态。暂停中的任务会让它一直阻塞。
- `Close` 停掉调度器和所有任务，可重复调用。

## Task

```go
func (t *Task) Pause() error
func (t *Task) Resume() error
func (t *Task) Restart() error
func (t *Task) Delete() error
func (t *Task) Events() <-chan Event
func (t *Task) Wait() error
func (t *Task) WaitContext(ctx context.Context) error
func (t *Task) Done() <-chan struct{}

func (t *Task) ID() string
func (t *Task) URL() string
func (t *Task) Dir() string
func (t *Task) FilePath() string
func (t *Task) PartPath() string
func (t *Task) Status() Status
func (t *Task) Err() error
func (t *Task) Progress() Progress
func (t *Task) Info() Info
func (t *Task) Workers() int
func (t *Task) RangeMode() RangeMode
```

## 事件

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

## 类型

```go
type Status int32

const (
	StatusPending = iota
	StatusQueued
	StatusDownloading
	StatusPaused
	StatusCompleted
	StatusFailed
	StatusDeleted
)

type RangeMode int8

const (
	RangeUnknown RangeMode = iota
	RangeSupported
	RangeUnsupported
)

type Progress struct {
	Total      int64
	Downloaded int64
	Speed      int64
	ETA        time.Duration
	Percent    float64
	Elapsed    time.Duration
	Workers    int
	BlockSize  int64
}

type Info struct {
	ID, URL, FilePath, PartPath string
	Status                      Status
	RangeMode                   RangeMode
	Err                         error
	Total, Downloaded, Speed    int64
	Workers                     int
	BlockSize                   int64
	Elapsed                     time.Duration
}

func (i Info) Percent() float64
```

`Status` 和 `RangeMode` 都有 `String()` 方法，返回小写名字。

## 错误

全部是哨兵值，可用 `errors.Is` 判断。任务返回的失败在适用时都会包裹其中之一。

| 错误 | 含义 |
| --- | --- |
| `ErrInvalidURL` | 目标不是绝对的 `http`/`https` URL，或带了账号密码 |
| `ErrInvalidProxy` | 代理不是带 host 的 `http`/`https`/`socks5` URL |
| `ErrBlockedHost` | host（目标或代理）解析到被拦地址 |
| `ErrNotPaused` | 对未暂停的任务调 `Resume` |
| `ErrNotDownloading` | 对已不在运行的任务调 `Pause` |
| `ErrCompleted` | 对已完成的任务做操作；用 `Restart` |
| `ErrDeleted` | 对已删除的任务做操作 |
| `ErrStalled` | 停滞超时内没有收到数据 |
| `ErrClosed` | 对已关闭的管理器做操作 |
| `HTTPStatusError` | 响应状态不可用 |

```go
type HTTPStatusError struct {
	StatusCode int
	Status     string
	URL        string
}

func (e *HTTPStatusError) Error() string
func (e *HTTPStatusError) Retryable() bool
```

`Retryable` 在 5xx、408、425、429 时为 true。

## 相关

- [常见用法](Recipes-zh) —— 这些调用的实际上下文
- [调度与预算](Scheduling-and-Budget-zh) —— `Threads` 与 `MaxFiles` 如何互相作用
- [限制与路线](Limitations-and-Roadmap-zh) —— API 里没有什么
