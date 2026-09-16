package downloader

import (
	"fmt"
	"net/http"
	"time"
)

// TaskState 是队列任务的生命周期状态。
type TaskState int

const (
	// TaskPending：已入队，等待空闲并发槽（或重试延迟结束）。
	TaskPending TaskState = iota
	// TaskRunning：其下载正在进行。
	TaskRunning
	// TaskPaused：已挂起但进度保留。可能是调用方暂停，也可能是
	// 队列挂起（队列 pause，或并发被降低）。
	TaskPaused
	// TaskCompleted：文件已完整下载。
	TaskCompleted
	// TaskFailed：下载以错误结束且无重试机会。
	TaskFailed
	// TaskCanceled：被调用方移出队列。
	TaskCanceled
)

// String 返回任务状态的小写英文名，用于日志与状态显示。
func (s TaskState) String() string {
	switch s {
	case TaskPending:
		return "pending"
	case TaskRunning:
		return "running"
	case TaskPaused:
		return "paused"
	case TaskCompleted:
		return "completed"
	case TaskFailed:
		return "failed"
	case TaskCanceled:
		return "canceled"
	default:
		return fmt.Sprintf("task-state(%d)", int(s))
	}
}

// IsTerminal 报告任务在没有显式 Restart 或 Resume 的情况下是否
// 不会再运行。
func (s TaskState) IsTerminal() bool {
	return s == TaskCompleted || s == TaskFailed || s == TaskCanceled
}

// TaskSpec 描述一个待下载文件。零值字段从队列的 Template 配置继承。
type TaskSpec struct {
	// URL 是待下载文件。必填。
	URL string
	// OutputPath 是保存位置。为空时从 URL 派生文件名并放入 Dir
	// （或队列的 OutputDir），与其他任务去重。
	OutputPath string
	// Dir 是派生文件名使用的目录，仅在 OutputPath 为空时生效。
	// 为空表示队列的 OutputDir。
	Dir string
	// Connections > 0 时覆盖 template 的连接数。
	Connections int
	// Segments > 0 时覆盖 template 的分片数。
	Segments int
	// Headers 非 nil 时替换 template 的 headers。
	Headers http.Header
	// MaxRetries 覆盖队列的重试预算：正值设置之，负值对本任务禁用
	// 重试，零值继承 QueueConfig.MaxRetries。
	MaxRetries int
}

// TaskInfo 是任务的不可变快照，由 [Queue.Tasks] 和 [Queue.Task] 返回，
// 也传给队列回调。
type TaskInfo struct {
	// ID 在其所属队列内唯一标识该任务。
	ID string
	// URL 是来源 URL。
	URL string
	// OutputPath 是解析后的目标路径。
	OutputPath string
	// State 是任务的生命周期状态。
	State TaskState
	// Attempts 计数下载已启动的次数（含重试）。
	Attempts int
	// Err 是任务失败时的最近错误。
	Err error
	// Progress 是底层下载的进度。任务从未运行时为零值。
	Progress Progress
}

// Summary 是队列的聚合视图。
type Summary struct {
	// Total 是队列中的任务总数。
	Total int
	// 各状态计数。
	Pending   int
	Running   int
	Paused    int
	Completed int
	Failed    int
	Canceled  int
	// TotalBytes 和 DownloadedBytes 跨任务聚合；TotalBytes 仅
	// 统计大小已知的任务。
	TotalBytes      int64
	DownloadedBytes int64
	// Concurrency 是已配置的同时下载数。
	Concurrency int
	// Started 报告队列是否已启动。
	Started bool
	// Finished 报告队列是否已排空（无剩余可运行任务）。
	Finished bool
}

// Done 报告是否每个任务都已到达终态。
func (s Summary) Done() bool {
	return s.Total > 0 && s.Pending == 0 && s.Running == 0 && s.Paused == 0
}

// TaskError 在 [Queue.Wait] 聚合错误时标识失败的任务。
type TaskError struct {
	ID  string
	URL string
	Err error
}

// Error 以"task ID (URL): 原因"的格式描述失败任务。
func (e *TaskError) Error() string {
	return fmt.Sprintf("task %s (%s): %v", e.ID, e.URL, e.Err)
}

// Unwrap 暴露底层错误，供 errors.Is/As 使用。
func (e *TaskError) Unwrap() error { return e.Err }

// task 是 TaskSpec 的队列内部记录。
type task struct {
	id   string
	spec TaskSpec

	// resolved 是派生/去重后的目标路径。
	resolved string

	dl       *Downloader
	state    TaskState
	attempts int
	err      error
	maxRetry int

	// 控制标志，全部由队列协程在 Queue.mu 下设置
	userPaused bool // 被调用方暂停：绝不自动恢复
	suspendReq bool // 为释放槽或因队列 pause 而请求停止
	cancelReq  bool // 任务正被移除
	restartReq bool // 清空进度并重新开始
	// restartDeleteOutput 在重启时额外删除此前下载的输出文件
	// （由 RestartTask 设置，ClearTaskCache 不设）。
	restartDeleteOutput bool
	// restartWipeCache 在重启时删除 partial 与元数据、重新从头下载
	// （由队列级 ClearCache 设置，区别于 RestartTask 的删输出语义）。
	restartWipeCache bool

	retryAt time.Time // 此时间之前不要启动（重试退避）
}

func (t *task) info() TaskInfo {
	info := TaskInfo{
		ID:         t.id,
		URL:        t.spec.URL,
		OutputPath: t.resolved,
		State:      t.state,
		Attempts:   t.attempts,
		Err:        t.err,
	}
	if t.dl != nil {
		info.Progress = t.dl.Progress()
	} else {
		info.Progress = Progress{
			URL:          t.spec.URL,
			OutputPath:   t.resolved,
			State:        StateIdle,
			AcceptRanges: true,
		}
	}
	return info
}
