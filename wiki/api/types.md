# 类型与错误

## 哨兵错误

```go
var (
	ErrInvalidConfig = errors.New("downloader: invalid config")
	ErrAlreadyRunning = errors.New("downloader: already running")
	ErrAborted       = errors.New("downloader: aborted")
	ErrChecksumMismatch = errors.New("downloader: checksum mismatch")
)
```

| 错误 | 何时返回 |
| --- | --- |
| `ErrInvalidConfig` | `New` / `NewQueue` 发现缺少必填字段或取值越界 |
| `ErrAlreadyRunning` | `Start` 时下载已在运行或暂停 |
| `ErrAborted` | 调用方在完成前中止，从 `Wait` 返回 |
| `ErrChecksumMismatch` | 设置了 `VerifySHA256` 且完成后的内容摘要不匹配，从 `Wait` 返回 |

## Config

单文件下载配置。零值不可用，通过 `New` 构造。字段表见[配置选项](../guide/configuration)。

```go
type Config struct {
	URL              string
	OutputPath       string
	Workers          int
	Connections      int
	Segments         int
	PartDir          string
	Headers          http.Header
	HTTPClient       *http.Client
	Proxy            string
	OnProgress       ProgressFunc
	MinSegmentSize   int
	AllowPrivateHost bool
	MaxBytesPerSec   int64
	VerifySHA256     string
}

func (c *Config) Validate() error
```

## Progress

不可变快照。字段表见[进度报告](../guide/progress)。

```go
type Progress struct {
	URL          string
	OutputPath   string
	Total        int64
	Downloaded   int64
	Percent      float64
	Speed        float64
	ETA          time.Duration
	Connections  int
	Segments     int
	Workers      int
	AcceptRanges bool
	State        State
	Err          error
}

type ProgressFunc func(Progress)
```

## State

单文件下载的生命周期状态。

```go
type State int

const (
	StateIdle State = iota
	StateRunning
	StatePaused
	StateCompleted
	StateFailed
	StateStopped
)

func (s State) String() string
func (s State) IsTerminal() bool
```

状态机见[运行时控制](../guide/runtime-control#状态机单文件下载)。

## 信号类型

```go
type SignalType int

const (
	SigPause SignalType = iota
	SigResume
	SigSetConnections
	SigSetSegments
	SigSetConnectionsAndSegments
	SigSetWorkers
	SigRestart
	SigClearCache
	SigStop
	SigAbort
)

type SignalEvent struct {
	Type        SignalType
	Connections int
	Segments    int
	Workers     int
}

func (e SignalEvent) String() string

// 队列侧
type QueueSignalType int

const (
	QSigPause QueueSignalType = iota
	QSigResume
	QSigSetConcurrency
	QSigSetConnections
	QSigSetSegments
	QSigSetConnectionsAndSegments
	QSigSetWorkers
	QSigClearCache
	QSigStop
	QSigAbort
)

type QueueSignal struct {
	Type        QueueSignalType
	Concurrency int
	Connections int
	Segments    int
	Workers     int
}

func (s QueueSignal) String() string
```

## 队列类型

```go
type QueueConfig struct {
	Concurrency       int
	Template          Config
	OutputDir         string
	MaxRetries        int
	RetryDelay        time.Duration
	OnTaskProgress    func(TaskInfo)
	OnTaskStateChange func(TaskInfo)
	OnQueueDone       func(Summary)
}

type TaskState int

const (
	TaskPending TaskState = iota
	TaskRunning
	TaskPaused
	TaskCompleted
	TaskFailed
	TaskCanceled
)

func (s TaskState) String() string
func (s TaskState) IsTerminal() bool

type TaskSpec struct {
	URL         string
	OutputPath  string
	Dir         string
	Connections int
	Segments    int
	Headers     http.Header
	MaxRetries  int
}

type TaskInfo struct {
	ID         string
	URL        string
	OutputPath string
	State      TaskState
	Attempts   int
	Err        error
	Progress   Progress
}

type Summary struct {
	Total           int
	Pending         int
	Running         int
	Paused          int
	Completed       int
	Failed          int
	Canceled        int
	TotalBytes      int64
	DownloadedBytes int64
	Concurrency     int
	Started         bool
	Finished        bool
}

func (s Summary) Done() bool

type TaskError struct {
	ID  string
	URL string
	Err error
}

func (e *TaskError) Error() string
func (e *TaskError) Unwrap() error
```

## 类型别名

为兼容历史版本，以下别名指向内部类型：

```go
type Segment = plan.Segment // 字节区间 + 已下载计数
type Meta = meta.Meta       // 磁盘 sidecar 元数据
```

## 内部包

以下包位于 `internal/`，不属于可导入的公开 API，但是实现的承重部分。

### `internal/plan` —— 切片数学

| 符号 | 说明 |
| --- | --- |
| `type Segment struct{ Position int; Start, End, Downloaded int64 }` | 字节区间 + 进度 |
| `Segment.Size()` / `Remaining()` / `Done()` / `NextOffset()` | 区间计算 |
| `BuildPlan(total int64, n int) []Segment` | 把 total 切成 n 个连续不重叠区间 |
| `AutoSegmentCount(total int64, connections, minSegmentBytes int) int` | `Segments=0` 时决定段数 |
| `Reinterleave(old []Segment, total int64, n int) []Segment` | 重切分并保留进度（最长连续前缀） |
| `SumDownloaded(plan []Segment) int64` | 汇总进度 |
| `DefaultMinSegmentSize = 1 << 20` | 1 MiB |

### `internal/meta` —— sidecar 文件

管理 `P.muyuan.partial` 与 `P.muyuan.meta.json`。

| 符号 | 说明 |
| --- | --- |
| `PartialSuffix` / `MetaSuffix` | 后缀常量 |
| `type Meta struct{...}` | URL、ETag、LastModified、TotalSize、AcceptRanges、Connections、Segments |
| `MetaPath(outputPath)` / `PartialPath(outputPath)` | 路径推导 |
| `Load(outputPath)` | 读取解码；不存在返回 `ok=false, nil` |
| `(*Meta).Save(outputPath)` | 原子保存（临时文件 + rename，`0600`） |
| `DeleteAndPartial(outputPath)` | 幂等删除两个 sidecar |

### `internal/security` —— SSRF

| 符号 | 说明 |
| --- | --- |
| `ValidateURLQuick(raw string, allowPrivate bool) error` | 免 DNS 快速检查（入队用） |
| `ValidateURL(raw string, allowPrivate bool) error` | 解析 DNS 的权威检查 |
| `GuardedDial(allowPrivate bool, dialer *net.Dialer) func(...)` | 拨号时再校验解析地址（防 DNS rebinding） |
| `IsPublicIP(ip net.IP) bool` | 拒绝环回/私有/链路本地/多播等 |

### `internal/transfer` —— IO 叶层

| 符号 | 说明 |
| --- | --- |
| `DefaultUserAgent = "go-muyuan/0.1.0"` | 默认 UA |
| `type ProbeResult struct{...}` | 探测结果 |
| `type WriterAt interface{ WriteAt(p []byte, off int64) (int, error) }` | `*os.File` 子集，便于测试 |
| `Probe(ctx, c, url, hdr)` | HEAD + 1 字节 Range GET，探测大小/ETag/Last-Modified/Accept-Ranges |
| `DownloadSegment(ctx, c, url, hdr, start, end, f, onBytes)` | 把 `[start,end)` 流式写到绝对偏移 |
| `DownloadSequential(ctx, c, url, hdr, f, onBytes)` | 整 body 单流降级 |
| `RetrySegment(ctx, seg)` | 有界指数退避（4 次，500ms→1s→2s→4s） |
| `StatusError(op, code)` | 4xx（除 408/429）→ 永久错误；5xx 可重试 |
| `IsPermanent(err) bool` | 判断是否为永久错误 |
