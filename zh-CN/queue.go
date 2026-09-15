package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xiaoLangZe/go-muyuan/zh-CN/internal/meta"
	"github.com/xiaoLangZe/go-muyuan/zh-CN/internal/security"
)

// 队列默认值与边界。
const (
	defaultConcurrency = 3
	maxConcurrency     = 128
	schedTick          = 200 * time.Millisecond
)

// QueueSignalType 标识发送给运行中队列的控制信号。
type QueueSignalType int

const (
	// QSigPause 挂起所有运行中任务（进度保留）并停止启动新任务。
	QSigPause QueueSignalType = iota + 1
	// QSigResume 撤销 QSigPause，恢复被队列挂起的任务。调用方
	// 单独暂停的任务保持暂停。
	QSigResume
	// QSigSetConcurrency 修改同时下载的文件数。
	QSigSetConcurrency
	// QSigSetConnections 修改运行中和未来任务的每文件 worker 数。
	QSigSetConnections
	// QSigSetSegments 修改运行中和未来任务的每文件切片数。
	QSigSetSegments
	// QSigSetConnectionsAndSegments 同时修改两个每文件旋钮。
	QSigSetConnectionsAndSegments
	// QSigSetWorkers 设定每文件"线程下载 + 切片下载"总和，由
	// 下载器自动分配 Connections 与 Segments。
	QSigSetWorkers
	// QSigClearCache 删除所有任务的 partial 文件和元数据。
	QSigClearCache
	// QSigStop 挂起一切、保留进度，并结束队列运行。
	QSigStop
	// QSigAbort 取消所有任务并结束运行；Wait 返回 ErrAborted。
	QSigAbort
)

// QueueSignal 是 [Queue] 的控制消息，是 [SignalEvent] 的批量版。
type QueueSignal struct {
	Type        QueueSignalType
	Concurrency int
	Connections int
	Segments    int
	Workers     int
}

func (s QueueSignal) String() string {
	switch s.Type {
	case QSigPause:
		return "q-pause"
	case QSigResume:
		return "q-resume"
	case QSigSetConcurrency:
		return fmt.Sprintf("q-set-concurrency(%d)", s.Concurrency)
	case QSigSetConnections:
		return fmt.Sprintf("q-set-connections(%d)", s.Connections)
	case QSigSetSegments:
		return fmt.Sprintf("q-set-segments(%d)", s.Segments)
	case QSigSetConnectionsAndSegments:
		return fmt.Sprintf("q-set-connections-and-segments(%d,%d)", s.Connections, s.Segments)
	case QSigSetWorkers:
		return fmt.Sprintf("q-set-workers(%d)", s.Workers)
	case QSigClearCache:
		return "q-clear-cache"
	case QSigStop:
		return "q-stop"
	case QSigAbort:
		return "q-abort"
	default:
		return fmt.Sprintf("q-signal(%d)", s.Type)
	}
}

// QueueConfig 配置一个批量下载队列。
type QueueConfig struct {
	// Concurrency 是同时下载的文件数。0 表示默认值（3）。它独立于
	// 每个文件自己的 Connections/Segments，因此"3 个文件、每文件
	// 8 连接"意味着最多 24 个并发连接。
	Concurrency int

	// Template 提供每个任务继承的每文件默认值。其 URL 和
	// OutputPath 被忽略；Connections、Segments、Headers、
	// HTTPClient、Proxy、PartDir、MinSegmentBytes、AllowPrivateHost
	// 被继承。其 OnProgress 被忽略——请用 OnTaskProgress。
	Template Config

	// OutputDir 是未指定路径的任务的保存目录。为空表示当前目录。
	OutputDir string

	// MaxRetries 是失败任务被标记 failed 前的重试次数。0 表示不重试。
	MaxRetries int

	// RetryDelay 是重试前等待时长。0 表示 2s。
	RetryDelay time.Duration

	// OnTaskProgress 非空时，随着任务下载进度被调用（每个运行中
	// 任务约每秒两次）。
	OnTaskProgress func(TaskInfo)

	// OnTaskStateChange 非空时，在任务状态变更时被调用。
	OnTaskStateChange func(TaskInfo)

	// OnQueueDone 非空时，在队列排空后被调用一次。
	OnQueueDone func(Summary)
}

// Queue 以有界并发下载多个文件。用 [Queue.Add]、[Queue.AddTask]
// 或 [Queue.AddAll] 添加任务，再调用 [Queue.Start]。
//
// 队列运行期间也可添加任务。每个任务由一个 [Downloader] 支撑，
// 因此每文件的 pause/resume/restart/clear 均可工作。
//
// Concurrency（同时文件数）与每个文件的 Connections/Segments 是
// 独立维度。用队列挂起任务会释放其并发槽、同时保留磁盘进度，
// 使大批量任务不至于占用空闲 worker。
//
// Queue 可被多个协程并发安全地使用。
type Queue struct {
	cfg QueueConfig

	// Callbacks 从 cfg 复制出来，以便不加锁读取。
	onTaskProgress    func(TaskInfo)
	onTaskStateChange func(TaskInfo)
	onQueueDone       func(Summary)

	sigCh  chan QueueSignal
	wakeCh chan struct{}

	mu       sync.Mutex
	tasks    []*task
	index    map[string]*task
	nextID   int
	running  int
	paused   bool // 队列级 pause
	stopped  bool // Stop 已请求
	aborted  bool // Abort 已请求或 ctx 已取消
	started  bool
	finished bool
	finErr   error
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewQueue 校验 cfg 并返回一个就绪接收任务的空队列。
func NewQueue(cfg QueueConfig) (*Queue, error) {
	if cfg.Concurrency == 0 {
		cfg.Concurrency = defaultConcurrency
	}
	if cfg.Concurrency < 1 || cfg.Concurrency > maxConcurrency {
		return nil, fmt.Errorf("%w: Concurrency %d out of range [1,%d]",
			ErrInvalidConfig, cfg.Concurrency, maxConcurrency)
	}
	if cfg.MaxRetries < 0 {
		return nil, fmt.Errorf("%w: MaxRetries must not be negative", ErrInvalidConfig)
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 2 * time.Second
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = "."
	}
	if abs, err := filepath.Abs(cfg.OutputDir); err == nil {
		cfg.OutputDir = abs
	}
	return &Queue{
		cfg:               cfg,
		onTaskProgress:    cfg.OnTaskProgress,
		onTaskStateChange: cfg.OnTaskStateChange,
		onQueueDone:       cfg.OnQueueDone,
		sigCh:             make(chan QueueSignal, 16),
		index:             make(map[string]*task),
	}, nil
}

// Add 入队一个下载。outputPath 可为空，此时从 URL 派生文件名放入
// 队列的 OutputDir，并与其他任务去重。返回任务 ID。
func (q *Queue) Add(rawURL, outputPath string) (string, error) {
	return q.AddTask(TaskSpec{URL: rawURL, OutputPath: outputPath})
}

// AddAll 入队每个 URL，保存到 dir（dir 为空时为队列的 OutputDir）。
// 遇到第一个非法 URL 时返回错误以及此前已添加的 ID。
func (q *Queue) AddAll(urls []string, dir string) ([]string, error) {
	ids := make([]string, 0, len(urls))
	for _, u := range urls {
		id, err := q.AddTask(TaskSpec{URL: u, Dir: dir})
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// AddTask 入队一个由 spec 描述的下载。队列运行期间也可添加任务；
// 它们会在槽空出时被调度。
func (q *Queue) AddTask(spec TaskSpec) (string, error) {
	if spec.URL == "" {
		return "", fmt.Errorf("%w: empty URL", ErrInvalidConfig)
	}

	q.mu.Lock()
	allowPrivate := q.cfg.Template.AllowPrivateHost
	q.mu.Unlock()
	// 不做 DNS 的廉价校验以立即反馈。权威检查（DNS 解析 + 拨号时
	// 防护）在任务启动时运行，使入队数千条 URL 仍保持快速。
	if err := security.ValidateURLQuick(spec.URL, allowPrivate); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	t := &task{
		spec:     spec,
		resolved: q.resolveOutputLocked(spec),
		state:    TaskPending,
	}
	switch {
	case spec.MaxRetries > 0:
		t.maxRetry = spec.MaxRetries
	case spec.MaxRetries < 0:
		t.maxRetry = 0 // 显式对本任务禁用重试
	default:
		t.maxRetry = q.cfg.MaxRetries
	}
	q.nextID++
	t.id = fmt.Sprintf("task-%d", q.nextID)
	q.tasks = append(q.tasks, t)
	q.index[t.id] = t
	q.wakeLocked()
	return t.id, nil
}

// resolveOutputLocked 决定任务写入位置：显式 OutputPath 优先，
// 否则从 URL 派生文件名、放入任务的 Dir（或队列的 OutputDir），
// 并与已有任务去重。
func (q *Queue) resolveOutputLocked(spec TaskSpec) string {
	if spec.OutputPath != "" {
		if abs, err := filepath.Abs(spec.OutputPath); err == nil {
			return abs
		}
		return spec.OutputPath
	}

	dir := spec.Dir
	if dir == "" {
		dir = q.cfg.OutputDir
	} else if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	name := deriveFileName(spec.URL)
	used := make(map[string]bool, len(q.tasks))
	for _, t := range q.tasks {
		used[t.resolved] = true
	}
	candidate := filepath.Join(dir, name)
	if !used[candidate] {
		return candidate
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		c := filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		if !used[c] {
			return c
		}
	}
}

// deriveFileName 从 URL 提取安全的基础文件名。
func deriveFileName(rawURL string) string {
	name := ""
	if u, err := url.Parse(rawURL); err == nil {
		name = path.Base(u.Path)
	}
	if s := sanitizeFileName(name); s != "" {
		return s
	}
	return "download"
}

// sanitizeFileName 剥离目录组件、控制字符以及在常见文件系统上
// 不安全或保留的字符。对不含可用名称的输入返回 ""。
func sanitizeFileName(name string) string {
	// 规范化分隔符后只保留最后一段，使构造的名字无法引入路径。
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20:
			// 丢弃控制字符
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}

// Start 开始调度已入队任务。队列已在运行时返回 ErrAlreadyRunning，
// 且要求至少有一个任务。
func (q *Queue) Start(ctx context.Context) error {
	q.mu.Lock()
	if q.started && !q.finished {
		q.mu.Unlock()
		return ErrAlreadyRunning
	}
	if len(q.tasks) == 0 {
		q.mu.Unlock()
		return fmt.Errorf("%w: no tasks queued", ErrInvalidConfig)
	}

	q.ctx, q.cancel = context.WithCancel(ctx)
	q.started = true
	q.finished = false
	q.stopped = false
	q.aborted = false
	q.paused = false
	q.finErr = nil
	q.done = make(chan struct{})
	q.wakeCh = make(chan struct{}, 1)

	// 队列挂起的任务重新变为可运行；调用方暂停的不动。
	for _, t := range q.tasks {
		if t.state == TaskPaused && !t.userPaused {
			t.state = TaskPending
		}
	}
	q.mu.Unlock()

	go q.run()
	return nil
}

// Signals 返回接收队列控制信号的通道。
func (q *Queue) Signals() chan<- QueueSignal { return q.sigCh }

// send 将信号入队而不阻塞调用方。
func (q *Queue) send(sig QueueSignal) {
	select {
	case q.sigCh <- sig:
	default:
		go func() { q.sigCh <- sig }()
	}
}

// Wait 阻塞至队列排空或被中止，返回聚合所有任务失败的错误
// （全部成功时为 nil）。单个失败可用 errors.As 到
// *[TaskError] 检查。
//
// 被调用方暂停的任务会阻止队列排空，因此 Wait 会一直阻塞
// 直到它被恢复或取消。
func (q *Queue) Wait() error {
	q.mu.Lock()
	done := q.done
	started := q.started
	q.mu.Unlock()
	if !started || done == nil {
		return nil
	}
	<-done
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.finErr
}

// Close 中止队列，等待其停止，并返回聚合错误。
func (q *Queue) Close() error {
	q.Abort()
	return q.Wait()
}

// Pause 挂起所有运行中任务（保留进度），并停止启动新任务。
// Resume 可继续。
func (q *Queue) Pause() { q.send(QueueSignal{Type: QSigPause}) }

// Resume 撤销 Pause。被调用方单独暂停的任务保持暂停。
func (q *Queue) Resume() { q.send(QueueSignal{Type: QSigResume}) }

// Stop 挂起一切、保留进度，并结束当前运行。可再次调用 Start 继续。
func (q *Queue) Stop() { q.send(QueueSignal{Type: QSigStop}) }

// Abort 取消所有任务；随后 Wait 返回 ErrAborted。
func (q *Queue) Abort() { q.send(QueueSignal{Type: QSigAbort}) }

// SetConcurrency 修改同时下载的文件数。降到低于当前运行任务数时
// 挂起最近启动的任务，它们会在槽空出时恢复。
func (q *Queue) SetConcurrency(n int) {
	q.send(QueueSignal{Type: QSigSetConcurrency, Concurrency: n})
}

// SetConnections 修改每文件 worker 数，对运行中和未来任务生效。
func (q *Queue) SetConnections(n int) { q.send(QueueSignal{Type: QSigSetConnections, Connections: n}) }

// SetSegments 修改每文件切片数，对运行中和未来任务生效。0 选自动切片。
func (q *Queue) SetSegments(n int) { q.send(QueueSignal{Type: QSigSetSegments, Segments: n}) }

// SetConnectionsAndSegments 同时修改两个每文件旋钮。
func (q *Queue) SetConnectionsAndSegments(connections, segments int) {
	q.send(QueueSignal{Type: QSigSetConnectionsAndSegments, Connections: connections, Segments: segments})
}

// SetWorkers 设定每文件"线程下载 + 切片下载"的总和上限，由下载器
// 自动分配 Connections 与 Segments。传 0 回退到分别设置模式。
func (q *Queue) SetWorkers(n int) {
	q.send(QueueSignal{Type: QSigSetWorkers, Workers: n})
}

// ClearCache 删除每个任务的 partial 文件和元数据。非终态任务
// 被重置以从头下载；终态任务保持其状态、仅清理残留附属文件。
func (q *Queue) ClearCache() error {
	q.send(QueueSignal{Type: QSigClearCache})
	return nil
}

// Tasks 返回所有任务的快照，按添加顺序。
func (q *Queue) Tasks() []TaskInfo {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]TaskInfo, 0, len(q.tasks))
	for _, t := range q.tasks {
		out = append(out, t.info())
	}
	return out
}

// Task 返回单个任务的快照。
func (q *Queue) Task(id string) (TaskInfo, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.index[id]
	if !ok {
		return TaskInfo{}, false
	}
	return t.info(), true
}

// Summary 返回队列的聚合快照。
func (q *Queue) Summary() Summary {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.summaryLocked()
}

// PauseTask 挂起单个任务，保留其进度。队列 Resume 不会恢复它；
// 请调用 ResumeTask。
func (q *Queue) PauseTask(id string) error { return q.controlTask(id, taskCtl{userPause: true}) }

// ResumeTask 使一个被挂起或暂停的任务重新可运行。
func (q *Queue) ResumeTask(id string) error { return q.controlTask(id, taskCtl{userResume: true}) }

// RestartTask 丢弃任务的输出文件和进度，并从头重新运行，同时
// 重置其重试预算。
func (q *Queue) RestartTask(id string) error {
	return q.controlTask(id, taskCtl{restart: true, deleteOutput: true})
}

// ClearTaskCache 删除一个任务的 partial 文件和元数据。非终态任务
// 被重置为从头下载；终态任务保持其状态。
func (q *Queue) ClearTaskCache(id string) error {
	return q.controlTask(id, taskCtl{restart: true})
}

// CancelTask 从队列移除任务：运行中任务被中止，且不会再运行。
func (q *Queue) CancelTask(id string) error { return q.controlTask(id, taskCtl{cancel: true}) }

// taskCtl 是请求的每任务控制动作。
type taskCtl struct {
	userPause    bool
	userResume   bool
	restart      bool
	deleteOutput bool
	cancel       bool
}

// controlTask 应用每任务控制动作并唤醒调度器。文件删除在锁释放后
// 进行。
func (q *Queue) controlTask(id string, ctl taskCtl) error {
	q.mu.Lock()
	t, ok := q.index[id]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("downloader: no such task %q", id)
	}

	var stopNow bool
	var abortNow bool
	var wipeNow []string

	switch {
	case ctl.cancel:
		if t.state != TaskCanceled {
			t.cancelReq = true
			t.userPaused = false
			t.suspendReq = false
			t.retryAt = time.Time{}
			if t.state == TaskRunning {
				stopNow, abortNow = true, true
			} else {
				t.state = TaskCanceled
				t.err = ErrAborted
			}
		}

	case ctl.userPause:
		t.userPaused = true
		switch t.state {
		case TaskRunning:
			t.suspendReq = true
			stopNow = true
		case TaskPending:
			// 在启动前按住它；没有 downloader 要停。
			t.state = TaskPaused
		}

	case ctl.userResume:
		t.userPaused = false
		if t.state == TaskPaused {
			t.state = TaskPending
		}

	case ctl.restart:
		if ctl.deleteOutput && t.resolved != "" {
			wipeNow = append(wipeNow, t.resolved)
		}
		if t.state == TaskRunning {
			// 让 runner 在其停止后观察到该请求。
			t.restartReq = true
			t.restartDeleteOutput = ctl.deleteOutput
			t.suspendReq = true
			stopNow = true
		} else {
			t.attempts = 0
			t.err = nil
			t.retryAt = time.Time{}
			if !t.state.IsTerminal() {
				t.state = TaskPending
			}
			if t.resolved != "" {
				wipeNow = append(wipeNow, t.resolved)
			}
		}
	}

	dl := t.dl
	q.mu.Unlock()

	// 删除运行中任务的输出必须等其 downloader 释放文件后；
	// 由 runner 在其停止时执行该清除。
	for _, p := range wipeNow {
		if stopNow {
			continue
		}
		_ = meta.DeleteAndPartial(p)
	}
	switch {
	case dl != nil && abortNow:
		dl.Abort()
	case dl != nil && stopNow:
		dl.Stop()
	}
	q.wake()
	return nil
}

// run 是调度器循环。
func (q *Queue) run() {
	defer close(q.done)
	ticker := time.NewTicker(schedTick)
	defer ticker.Stop()

	for {
		select {
		case <-q.ctx.Done():
			q.abortAll()
		case sig := <-q.sigCh:
			q.handleSignal(sig)
		case <-q.wakeCh:
		case <-ticker.C:
		}

		q.apply(q.plan())

		q.mu.Lock()
		switch {
		case q.aborted && q.running == 0:
			q.finErr = ErrAborted
			q.finished = true
		case q.stopped && q.running == 0:
			q.finErr = nil
			q.finished = true
		case q.allTerminalLocked():
			q.finErr = q.aggregateErrLocked()
			q.finished = true
		}
		finished := q.finished
		q.mu.Unlock()

		if finished {
			q.notifyDone()
			return
		}
	}
}

// abortAll 将每个任务标记为取消并停止运行中的。尚未启动的任务
// 立即取消；运行中的在其停止后由 runner 记录取消。
func (q *Queue) abortAll() {
	q.mu.Lock()
	q.aborted = true
	for _, t := range q.tasks {
		if t.state.IsTerminal() {
			continue
		}
		t.cancelReq = true
		t.retryAt = time.Time{}
		if t.state != TaskRunning {
			t.state = TaskCanceled
			t.err = ErrAborted
			continue
		}
	}
	dls := q.runningDownloadersLocked()
	q.mu.Unlock()
	for _, dl := range dls {
		dl.Abort()
	}
}

// qAction 是在锁外应用的调度决策。
type qAction struct {
	suspend *task
	start   *task
}

// plan 计算本轮动作：挂起哪些运行中任务、启动哪些可运行任务，
// 遵守并发上限。
func (q *Queue) plan() []qAction {
	q.mu.Lock()
	defer q.mu.Unlock()

	var acts []qAction

	// 整队列 pause/stop/abort：挂起所有运行中任务，不启动任何任务。
	if q.aborted || q.stopped || q.paused {
		for _, t := range q.tasks {
			if t.state == TaskRunning && !t.suspendReq {
				t.suspendReq = true
				acts = append(acts, qAction{suspend: t})
			}
		}
		return acts
	}

	// 超过上限：挂起最近启动的任务。
	var running []*task
	for _, t := range q.tasks {
		if t.state == TaskRunning {
			running = append(running, t)
		}
	}
	if len(running) > q.cfg.Concurrency {
		for _, t := range running[q.cfg.Concurrency:] {
			if !t.suspendReq {
				t.suspendReq = true
				acts = append(acts, qAction{suspend: t})
			}
		}
	}

	// 空闲槽要扣除即将被挂起的任务。
	willSuspend := 0
	for _, a := range acts {
		if a.suspend != nil {
			willSuspend++
		}
	}
	slots := q.cfg.Concurrency - (len(running) - willSuspend)
	now := time.Now()
	for _, t := range q.tasks {
		if slots <= 0 {
			break
		}
		if !q.runnableLocked(t, now) {
			continue
		}
		slots--
		acts = append(acts, qAction{start: t})
	}
	return acts
}

// runnableLocked 报告任务当前是否可启动。
func (q *Queue) runnableLocked(t *task, now time.Time) bool {
	if t.cancelReq || t.suspendReq {
		return false
	}
	switch t.state {
	case TaskPending:
		return t.retryAt.IsZero() || !now.Before(t.retryAt)
	case TaskPaused:
		// 仅队列挂起的任务自动恢复；调用方暂停的保持不动。
		return !t.userPaused
	default:
		return false
	}
}

// apply 执行 plan 的动作。挂起先于启动，使它们的槽真正空出
// 后再启动新任务。
func (q *Queue) apply(acts []qAction) {
	for _, a := range acts {
		if a.suspend != nil && a.suspend.dl != nil {
			a.suspend.dl.Stop()
		}
	}
	for _, a := range acts {
		if a.start != nil {
			q.launch(a.start)
		}
	}
}

// launch 异步启动（或恢复）一个任务。
func (q *Queue) launch(t *task) {
	q.mu.Lock()
	if t.cancelReq || t.suspendReq || t.state == TaskRunning ||
		q.aborted || q.stopped || q.paused {
		q.mu.Unlock()
		return
	}
	t.state = TaskRunning
	t.attempts++
	t.retryAt = time.Time{}
	q.running++
	ctx := q.ctx
	q.mu.Unlock()

	q.notifyState(t)

	go func() {
		q.taskFinished(t, q.runTask(t, ctx))
	}()
}

// runTask 为一次尝试构建 Downloader 并阻塞至其结束。
func (q *Queue) runTask(t *task, ctx context.Context) error {
	cfg, err := q.taskConfig(t)
	if err != nil {
		return err
	}
	dl, err := New(cfg)
	if err != nil {
		return err
	}

	q.mu.Lock()
	if t.cancelReq || q.aborted {
		q.mu.Unlock()
		return ErrAborted
	}
	if t.suspendReq {
		// 启动前被挂起：保持不下载。
		q.mu.Unlock()
		return nil
	}
	t.dl = dl
	q.mu.Unlock()

	if err := dl.Start(ctx); err != nil {
		return err
	}
	return dl.Wait()
}

// taskConfig 解析任务的生效每文件配置。
func (q *Queue) taskConfig(t *task) (Config, error) {
	q.mu.Lock()
	c := q.cfg.Template
	id := t.id
	q.mu.Unlock()

	c.URL = t.spec.URL
	c.OutputPath = t.resolved
	if t.spec.Connections > 0 {
		c.Connections = t.spec.Connections
	}
	if t.spec.Segments > 0 {
		c.Segments = t.spec.Segments
	}
	if t.spec.Headers != nil {
		c.Headers = t.spec.Headers
	}
	// 每文件进度经队列路由，使调用方获得任务身份。
	c.OnProgress = func(Progress) {
		if q.onTaskProgress != nil {
			q.onTaskProgress(q.snapshot(id))
		}
	}
	return c, nil
}

// taskFinished 记录一次尝试的结果并决定后续动作
// （完成、重试、挂起、重启、取消或失败）。
func (q *Queue) taskFinished(t *task, err error) {
	dl := t.dl
	completed := dl != nil && dl.Progress().State == StateCompleted

	q.mu.Lock()
	q.running--

	var wipeOutput bool
	switch {
	case t.cancelReq:
		t.state = TaskCanceled
		t.err = ErrAborted

	case t.restartReq:
		wipeOutput = t.restartDeleteOutput
		t.restartReq = false
		t.restartDeleteOutput = false
		t.suspendReq = false
		t.userPaused = false
		t.attempts = 0
		t.err = nil
		t.retryAt = time.Time{}
		t.state = TaskPending

	case t.suspendReq:
		t.suspendReq = false
		t.state = TaskPaused

	case completed:
		t.state = TaskCompleted
		t.err = nil

	case err != nil:
		t.err = err
		if t.attempts <= t.maxRetry {
			t.state = TaskPending
			t.retryAt = time.Now().Add(q.cfg.RetryDelay)
		} else {
			t.state = TaskFailed
		}

	default:
		// 无错误、未完成、且无停止请求。
		t.err = errors.New("downloader: task ended without completing")
		t.state = TaskFailed
	}
	if t.state == TaskPending || t.state == TaskCompleted {
		q.wakeLocked()
	}
	q.mu.Unlock()

	if wipeOutput && t.resolved != "" {
		_ = os.Remove(t.resolved)
		_ = meta.DeleteAndPartial(t.resolved)
	}

	q.notifyState(t)
	q.wake()
}

// handleSignal 分发一个队列控制信号。
func (q *Queue) handleSignal(sig QueueSignal) {
	switch sig.Type {
	case QSigPause:
		q.mu.Lock()
		q.paused = true
		q.mu.Unlock()

	case QSigResume:
		q.mu.Lock()
		q.paused = false
		q.stopped = false
		q.mu.Unlock()

	case QSigSetConcurrency:
		q.setConcurrency(sig.Concurrency)

	case QSigSetConnections:
		q.setConnections(sig.Connections)

	case QSigSetSegments:
		q.setSegments(sig.Segments)

	case QSigSetConnectionsAndSegments:
		q.setConnections(sig.Connections)
		q.setSegments(sig.Segments)

	case QSigSetWorkers:
		q.setWorkers(sig.Workers)

	case QSigClearCache:
		q.clearAllCache()

	case QSigStop:
		q.mu.Lock()
		q.stopped = true
		q.mu.Unlock()

	case QSigAbort:
		q.abortAll()
	}
	q.wake()
}

func (q *Queue) setConcurrency(n int) {
	if n <= 0 {
		return
	}
	if n > maxConcurrency {
		n = maxConcurrency
	}
	q.mu.Lock()
	q.cfg.Concurrency = n
	q.mu.Unlock()
}

func (q *Queue) setConnections(n int) {
	if n <= 0 {
		return
	}
	if n > maxConnections {
		n = maxConnections
	}
	q.mu.Lock()
	q.cfg.Template.Connections = n
	dls := q.runningDownloadersLocked()
	q.mu.Unlock()
	for _, dl := range dls {
		dl.SetConnections(n)
	}
}

func (q *Queue) setSegments(n int) {
	if n < 0 {
		return
	}
	if n > maxSegments {
		n = maxSegments
	}
	q.mu.Lock()
	q.cfg.Template.Segments = n
	dls := q.runningDownloadersLocked()
	q.mu.Unlock()
	for _, dl := range dls {
		dl.SetSegments(n)
	}
}

func (q *Queue) setWorkers(n int) {
	if n < 0 {
		return
	}
	if n > maxWorkers {
		n = maxWorkers
	}
	q.mu.Lock()
	q.cfg.Template.Workers = n
	if n > 0 {
		allocateWorkers(&q.cfg.Template)
	}
	dls := q.runningDownloadersLocked()
	q.mu.Unlock()
	for _, dl := range dls {
		dl.SetWorkers(n)
	}
}

// clearAllCache 清除每个任务的附属文件。运行中任务先被挂起并
// 重置以重新下载；终态任务保持其状态、仅清理残留文件。
func (q *Queue) clearAllCache() {
	q.mu.Lock()
	var wipeNow []string
	var stop []*Downloader
	for _, t := range q.tasks {
		switch {
		case t.state == TaskRunning:
			t.restartReq = true
			t.restartDeleteOutput = false
			t.suspendReq = true
			if t.dl != nil {
				stop = append(stop, t.dl)
			}
		case t.state.IsTerminal():
			if t.resolved != "" {
				wipeNow = append(wipeNow, t.resolved)
			}
		default:
			t.attempts = 0
			if t.resolved != "" {
				wipeNow = append(wipeNow, t.resolved)
			}
		}
	}
	q.mu.Unlock()

	for _, p := range wipeNow {
		_ = meta.DeleteAndPartial(p)
	}
	for _, dl := range stop {
		dl.Stop()
	}
}

// runningDownloadersLocked 返回运行中任务的 downloader。
func (q *Queue) runningDownloadersLocked() []*Downloader {
	out := make([]*Downloader, 0, q.running)
	for _, t := range q.tasks {
		if t.state == TaskRunning && t.dl != nil {
			out = append(out, t.dl)
		}
	}
	return out
}

// allTerminalLocked 报告是否没有任务还能继续。
func (q *Queue) allTerminalLocked() bool {
	for _, t := range q.tasks {
		if !t.state.IsTerminal() {
			return false
		}
	}
	return true
}

// summaryLocked 构建聚合视图。
func (q *Queue) summaryLocked() Summary {
	s := Summary{
		Total:       len(q.tasks),
		Concurrency: q.cfg.Concurrency,
		Started:     q.started,
		Finished:    q.finished,
	}
	for _, t := range q.tasks {
		switch t.state {
		case TaskPending:
			s.Pending++
		case TaskRunning:
			s.Running++
		case TaskPaused:
			s.Paused++
		case TaskCompleted:
			s.Completed++
		case TaskFailed:
			s.Failed++
		case TaskCanceled:
			s.Canceled++
		}
		if t.dl != nil {
			p := t.dl.Progress()
			s.DownloadedBytes += p.Downloaded
			if p.Total > 0 {
				s.TotalBytes += p.Total
			}
		}
	}
	return s
}

// aggregateErrLocked 合并每个失败任务的错误。
func (q *Queue) aggregateErrLocked() error {
	var errs []error
	for _, t := range q.tasks {
		if t.state == TaskFailed && t.err != nil {
			errs = append(errs, &TaskError{ID: t.id, URL: t.spec.URL, Err: t.err})
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// snapshot 按 ID 返回任务快照（未知时为零值）。
func (q *Queue) snapshot(id string) TaskInfo {
	q.mu.Lock()
	defer q.mu.Unlock()
	if t, ok := q.index[id]; ok {
		return t.info()
	}
	return TaskInfo{ID: id}
}

// notifyState 将任务状态变更上报给调用方回调。
func (q *Queue) notifyState(t *task) {
	if q.onTaskStateChange == nil {
		return
	}
	q.onTaskStateChange(q.snapshot(t.id))
}

// notifyDone 将队列完成上报给调用方回调。
func (q *Queue) notifyDone() {
	if q.onQueueDone != nil {
		q.onQueueDone(q.Summary())
	}
}

// wake 不阻塞地轻推调度器。
func (q *Queue) wake() {
	q.mu.Lock()
	q.wakeLocked()
	q.mu.Unlock()
}

// wakeLocked 轻推调度器；调用方持有 q.mu。
func (q *Queue) wakeLocked() {
	if q.wakeCh == nil {
		return
	}
	select {
	case q.wakeCh <- struct{}{}:
	default:
	}
}

// String 渲染队列用于调试。
func (q *Queue) String() string {
	s := q.Summary()
	return fmt.Sprintf("queue{total=%d pending=%d running=%d paused=%d completed=%d failed=%d canceled=%d concurrency=%d}",
		s.Total, s.Pending, s.Running, s.Paused, s.Completed, s.Failed, s.Canceled, s.Concurrency)
}
