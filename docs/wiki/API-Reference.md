# API Reference

**English** · [中文](API-Reference-zh)

Everything exported by `github.com/xiaoLangZe/go-muyuan`. The package name is
`muyuan`.

## Manager options

Passed to `New`. They set the defaults every task starts from.

| Option | Default | Meaning |
| --- | --- | --- |
| `WithThreads(n int)` | 4 | Connections in flight across all tasks |
| `WithMaxFiles(n int)` | 2 | Files downloading at the same time |
| `WithDefaultDir(dir string)` | `""` | Directory tasks are written to |
| `WithRootDir(dir string)` | executable's directory | Root that relative paths resolve against |
| `WithDefaultProxy(rawURL string)` | none | Proxy for every task |
| `WithDefaultHeader(key, value string)` | none | Request header for every task; repeatable |
| `WithDefaultHeaders(map[string]string)` | none | Several request headers at once |
| `WithDefaultRetries(n int)` | 3 | Further attempts after a failed transfer |
| `WithDefaultRetryDelay(d time.Duration)` | 1s | Wait before the first retry; doubles up to 30s |
| `WithDefaultResponseTimeout(d time.Duration)` | 30s | Bound on waiting for response headers |
| `WithDefaultStallTimeout(d time.Duration)` | 60s | Give up when no byte arrives for this long; 0 disables |
| `WithDefaultBufferSize(n int)` | 64 KiB | Copy buffer of the single-connection path |
| `WithDefaultChunkSize(n int64)` | auto | Pin the piece length, disabling adaptation |
| `WithDefaultChunkBounds(min, max int64)` | 256 KiB / 16 MiB | Bounds for an automatic piece length |
| `WithDefaultHTTPClient(c *http.Client)` | built-in | Client used by every task |
| `WithDefaultAllowUnsafeHosts(allow bool)` | false | Permit local and private network targets |
| `WithDefaultProgress(f func(Progress))` | none | Progress callback for every task |

## Task options

Passed to `AddTask`. They override the manager's defaults **for that task only**.

| Option | Default | Meaning |
| --- | --- | --- |
| `WithFileName(name string)` | from the server or URL | File name to write |
| `WithOverwrite(bool)` | false | Replace an existing file instead of picking a free name |
| `WithHeader(key, value string)` | manager default | Request header; repeatable |
| `WithHeaders(map[string]string)` | manager default | Several request headers at once |
| `WithRetries(n int)` | manager default | Further attempts after a failed transfer |
| `WithRetryDelay(d time.Duration)` | manager default | Wait before the first retry |
| `WithResponseTimeout(d time.Duration)` | manager default | Bound on waiting for response headers |
| `WithStallTimeout(d time.Duration)` | manager default | Give up when no byte arrives for this long |
| `WithBufferSize(n int)` | manager default | Copy buffer of the single-connection path |
| `WithChunkSize(n int64)` | manager default | Pin the piece length |
| `WithChunkBounds(min, max int64)` | manager default | Bounds for an automatic piece length |
| `WithHTTPClient(c *http.Client)` | manager default | Client used by this task |
| `WithAllowUnsafeHosts(bool)` | manager default | Permit local and private network targets |
| `WithProxy(rawURL string)` | manager default | Proxy for this task |
| `WithProgress(f func(Progress))` | manager default | Progress callback for this task |

The two sets are named differently on purpose: `WithDefault*` applies to every
task, the bare names apply to one. Go does not allow two functions with the same
name in one package, so the prefix is what distinguishes them.

**There is deliberately no task-level connection count.** The scheduler owns the
allocation and rewrites it on every pass, so a per-task value would be
overwritten. Size the budget with `WithThreads` instead.

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

- `AddTask` validates the URL and the proxy immediately and queues the download.
  An empty `destDir` falls back to `WithDefaultDir`.
- `Tasks` returns a snapshot in the order tasks were added.
- `Task(id)` returns nil when nothing matches.
- `Wait` blocks until every task is terminal. A paused task keeps it blocked.
- `Close` stops the scheduler and every task. It is idempotent.

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

## Events

```go
type EventType int

const (
	EventProgress EventType = iota // bytes arrived or the rate changed
	EventStatus                    // the task changed state
	EventError                     // the task ended in failure
)

type Event struct {
	TaskID   string
	Type     EventType
	Status   Status
	Progress Progress
	Err      error     // EventError only
	At       time.Time
}
```

## Types

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

`Status` and `RangeMode` both have a `String()` method returning the lower-case
name.

## Errors

All are sentinels, usable with `errors.Is`. Failures returned by a task wrap one
of them where applicable.

| Error | Meaning |
| --- | --- |
| `ErrInvalidURL` | The target is not an absolute `http`/`https` URL, or carries credentials |
| `ErrInvalidProxy` | The proxy is not an `http`/`https`/`socks5` URL with a host |
| `ErrBlockedHost` | A host — target or proxy — resolves to a blocked address |
| `ErrNotPaused` | `Resume` on a task that is not paused |
| `ErrNotDownloading` | `Pause` on a task that is no longer running |
| `ErrCompleted` | An operation on a task that already finished; use `Restart` |
| `ErrDeleted` | An operation on a deleted task |
| `ErrStalled` | No data arrived for the stall timeout |
| `ErrClosed` | An operation on a closed manager |
| `HTTPStatusError` | A response with an unusable status |

```go
type HTTPStatusError struct {
	StatusCode int
	Status     string
	URL        string
}

func (e *HTTPStatusError) Error() string
func (e *HTTPStatusError) Retryable() bool
```

`Retryable` is true for 5xx, 408, 425 and 429.

## Related

- [Recipes](Recipes) — these calls in context
- [Scheduling and Budget](Scheduling-and-Budget) — how `Threads` and `MaxFiles` interact
- [Limitations and Roadmap](Limitations-and-Roadmap) — what is not in the API
