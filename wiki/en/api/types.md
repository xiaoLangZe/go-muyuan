# Types & Errors

## Sentinel errors

```go
var (
	ErrInvalidConfig  = errors.New("downloader: invalid config")
	ErrAlreadyRunning = errors.New("downloader: already running")
	ErrAborted        = errors.New("downloader: aborted")
)
```

| Error | When it is returned |
| --- | --- |
| `ErrInvalidConfig` | `New` / `NewQueue` finds a missing required field or an out-of-range value |
| `ErrAlreadyRunning` | `Start` while the download is already running or paused |
| `ErrAborted` | the caller aborted before completion; returned from `Wait` |

## Config

Per-file download configuration. The zero value is unusable; construct via `New`. Field table in [Configuration](../guide/configuration).

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
}

func (c *Config) Validate() error
```

## Progress

Immutable snapshot. Field table in [Progress](../guide/progress).

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

Lifecycle state of a single download.

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

State machine in [Runtime Control](../guide/runtime-control#state-machine-single-download).

## Signal types

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

// Queue side
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

## Queue types

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

## Type aliases

For backward compatibility, these aliases point at internal types:

```go
type Segment = plan.Segment // byte range + downloaded count
type Meta = meta.Meta       // on-disk sidecar metadata
```

## Internal packages

These live under `internal/` and are not part of the importable API, but they are load-bearing for the implementation.

### `internal/plan` — segment math

| Symbol | Purpose |
| --- | --- |
| `type Segment struct{ Position int; Start, End, Downloaded int64 }` | Byte range + progress |
| `Segment.Size()` / `Remaining()` / `Done()` / `NextOffset()` | Range arithmetic |
| `BuildPlan(total int64, n int) []Segment` | Split total into n contiguous, non-overlapping ranges |
| `AutoSegmentCount(total int64, connections, minSegmentBytes int) int` | Decide the count for `Segments=0` (auto) |
| `Reinterleave(old []Segment, total int64, n int) []Segment` | Re-split preserving progress (longest contiguous prefix) |
| `SumDownloaded(plan []Segment) int64` | Sum per-segment progress |
| `DefaultMinSegmentSize = 1 << 20` | 1 MiB |

### `internal/meta` — sidecar files

Manages `P.muyuan.partial` and `P.muyuan.meta.json`.

| Symbol | Purpose |
| --- | --- |
| `PartialSuffix` / `MetaSuffix` | Suffix constants |
| `type Meta struct{...}` | URL, ETag, LastModified, TotalSize, AcceptRanges, Connections, Segments |
| `MetaPath(outputPath)` / `PartialPath(outputPath)` | Path derivation |
| `Load(outputPath)` | Read + decode; returns `ok=false, nil` if absent |
| `(*Meta).Save(outputPath)` | Atomic save (temp file + rename, `0600`) |
| `DeleteAndPartial(outputPath)` | Idempotent delete of both sidecars |

### `internal/security` — SSRF

| Symbol | Purpose |
| --- | --- |
| `ValidateURLQuick(raw string, allowPrivate bool) error` | DNS-free fast check (for enqueue) |
| `ValidateURL(raw string, allowPrivate bool) error` | Authoritative, DNS-resolving check |
| `GuardedDial(allowPrivate bool, dialer *net.Dialer) func(...)` | Dial-time re-check of the resolved address (defeats DNS rebinding) |
| `IsPublicIP(ip net.IP) bool` | Rejects loopback/private/link-local/multicast and more |

### `internal/transfer` — IO leaf layer

| Symbol | Purpose |
| --- | --- |
| `DefaultUserAgent = "go-muyuan/0.1.0"` | Default User-Agent |
| `type ProbeResult struct{...}` | Probe outcome |
| `type WriterAt interface{ WriteAt(p []byte, off int64) (int, error) }` | `*os.File` subset, for testability |
| `Probe(ctx, c, url, hdr)` | HEAD + 1-byte Range GET; learns size/ETag/Last-Modified/Accept-Ranges |
| `DownloadSegment(ctx, c, url, hdr, start, end, f, onBytes)` | Stream `[start,end)` to an absolute offset |
| `DownloadSequential(ctx, c, url, hdr, f, onBytes)` | Whole-body single-stream fallback |
| `RetrySegment(ctx, seg)` | Bounded exponential backoff (4 attempts, 500ms→1s→2s→4s) |
| `StatusError(op, code)` | 4xx (except 408/429) → permanent; 5xx retryable |
| `IsPermanent(err) bool` | Reports whether an error is permanent |
