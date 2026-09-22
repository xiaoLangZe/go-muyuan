// Package muyuan downloads files over a shared connection budget.
//
// A Manager owns one budget of connections and a limit on how many files may be
// downloaded at the same time. Files are added as tasks; the manager decides
// when each one runs and how many connections it gets. The allocation is
// breadth first: every file that fits gets one connection before any file gets a
// second, and whatever is left is then dealt out round by round, so a single
// file never takes the whole budget.
//
//	m := muyuan.New(muyuan.WithThreads(16), muyuan.WithMaxFiles(3),
//		muyuan.WithDefaultDir("downloads"))
//	defer m.Close()
//
//	a, err := m.AddTask("downloads", "https://example.com/a.iso")
//	if err != nil {
//		return err
//	}
//	b, err := m.AddTask("downloads", "https://example.com/b.iso",
//		muyuan.WithFileName("renamed.iso"),
//		muyuan.WithProxy("socks5://127.0.0.1:1080"))
//	if err != nil {
//		return err
//	}
//
//	for ev := range a.Events() {   // live progress, no polling
//		fmt.Printf("%s %.1f%%\n", ev.Status, ev.Progress.Percent)
//	}
//
//	a.Pause()   // its connections go to the other files at once
//	a.Resume()  // and it takes them back as they free up
//	a.Restart() // from zero
//	a.Delete()  // stops it and removes its files
//
//	if err := b.Wait(); err != nil {
//		return err
//	}
package muyuan

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xiaoLangZe/go-muyuan/internal/engine"
)

// Defaults for a manager that is created without an explicit budget.
const (
	DefaultThreads  = 4
	DefaultMaxFiles = 2
)

// ErrClosed reports an operation on a manager that was closed.
var ErrClosed = errors.New("manager is closed")

// Option configures a manager. The values it sets are the defaults every task
// starts from; a task overrides them with the options it is given.
type Option func(*options)

type options struct {
	threads    int
	maxFiles   int
	rootDir    string
	defaultDir string
	defaults   []engine.Option
}

// WithThreads sets how many connections the manager may have in flight across
// all of its tasks. It is the sum that is divided between the running files.
func WithThreads(threads int) Option {
	return func(o *options) { o.threads = threads }
}

// WithMaxFiles sets how many files may be downloaded at the same time. Further
// tasks wait as StatusQueued until a running one finishes or the limit is
// raised.
//
// Raising it starts queued tasks; lowering it below the number of running files
// pauses the ones that no longer fit, keeping the bytes they already have.
func WithMaxFiles(maxFiles int) Option {
	return func(o *options) { o.maxFiles = maxFiles }
}

// defaultOption adds an option that every task of the manager starts from.
func defaultOption(opt engine.Option) Option {
	return func(o *options) { o.defaults = append(o.defaults, opt) }
}

// WithRootDir sets the directory that relative download paths are resolved
// against. Without it, relative paths are resolved against the directory of
// the executable, so a program started from any working directory writes next
// to itself. A relative root is itself resolved against the executable's
// directory; an absolute destination passed to AddTask ignores the root
// entirely.
func WithRootDir(dir string) Option {
	return func(o *options) { o.rootDir = dir }
}

// WithDefaultDir sets the directory tasks are written to when AddTask is not
// given one. A relative value is resolved against the root, an absolute one is
// used as it is.
func WithDefaultDir(dir string) Option {
	return func(o *options) { o.defaultDir = dir }
}

// WithDefaultProxy routes every task's connections through the proxy at rawURL
// unless the task overrides it with WithProxy. See WithProxy for what a proxy
// means for the host guard.
func WithDefaultProxy(rawURL string) Option { return defaultOption(engine.WithProxy(rawURL)) }

// WithDefaultHeader adds a request header to every task.
func WithDefaultHeader(key, value string) Option {
	return defaultOption(engine.WithHeader(key, value))
}

// WithDefaultHeaders adds every entry of the map as a request header to every
// task.
func WithDefaultHeaders(headers map[string]string) Option {
	return defaultOption(engine.WithHeaders(headers))
}

// WithDefaultRetries sets how many further attempts follow a failed transfer.
func WithDefaultRetries(retries int) Option { return defaultOption(engine.WithRetries(retries)) }

// WithDefaultRetryDelay sets the wait before the first retry.
func WithDefaultRetryDelay(delay time.Duration) Option {
	return defaultOption(engine.WithRetryDelay(delay))
}

// WithDefaultResponseTimeout bounds the wait for response headers.
func WithDefaultResponseTimeout(timeout time.Duration) Option {
	return defaultOption(engine.WithResponseTimeout(timeout))
}

// WithDefaultStallTimeout gives up on a transfer that receives no data for this
// long.
func WithDefaultStallTimeout(timeout time.Duration) Option {
	return defaultOption(engine.WithStallTimeout(timeout))
}

// WithDefaultBufferSize sets the size of the copy buffer used by the
// single-connection path.
func WithDefaultBufferSize(size int) Option {
	return defaultOption(engine.WithBufferSize(size))
}

// WithDefaultChunkSize pins the length of one piece a connection fetches, which
// disables the automatic choice for every task.
func WithDefaultChunkSize(size int64) Option {
	return defaultOption(engine.WithBlockSize(size))
}

// WithDefaultChunkBounds bounds the length of a piece when it is chosen
// automatically.
func WithDefaultChunkBounds(min, max int64) Option {
	return defaultOption(engine.WithBlockBounds(min, max))
}

// WithDefaultHTTPClient uses a caller supplied HTTP client for every task. Its
// transport should allow as many connections per host as the budget needs.
func WithDefaultHTTPClient(client *http.Client) Option {
	return defaultOption(engine.WithHTTPClient(client))
}

// WithDefaultAllowUnsafeHosts permits every task to reach the local machine or
// a private network.
func WithDefaultAllowUnsafeHosts(allow bool) Option {
	return defaultOption(engine.WithAllowUnsafeHosts(allow))
}

// WithDefaultProgress registers a callback that every task reports its
// statistics to. Events is the other way to follow a task, and the one to
// prefer when the consumer may be slower than the transfer.
func WithDefaultProgress(callback func(Progress)) Option {
	return defaultOption(engine.WithProgress(callback))
}

// Manager downloads files over one shared connection budget. It is safe for use
// by several goroutines.
type Manager struct {
	mu       sync.Mutex
	wake     chan struct{}
	base     context.Context
	cancel   context.CancelFunc
	loopDone chan struct{}

	threads    int
	maxFiles   int
	root       string
	defaultDir string
	defaults   []engine.Option
	tasks      []*Task
	closed     bool
}

// New creates a manager and starts its scheduler.
func New(opts ...Option) *Manager {
	o := options{threads: DefaultThreads, maxFiles: DefaultMaxFiles}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.threads < 1 {
		o.threads = 1
	}
	if o.maxFiles < 1 {
		o.maxFiles = 1
	}
	root := o.rootDir
	if root == "" {
		root = exeDir()
	} else if !filepath.IsAbs(root) {
		root = filepath.Join(exeDir(), root)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		wake:       make(chan struct{}, 1),
		base:       ctx,
		cancel:     cancel,
		loopDone:   make(chan struct{}),
		threads:    o.threads,
		maxFiles:   o.maxFiles,
		root:       root,
		defaultDir: o.defaultDir,
		defaults:   o.defaults,
	}
	go m.loop()
	return m
}

// AddTask queues a download. destDir is the directory the file is written to
// and may be empty; rawURL is the target. The options override the manager's
// defaults for this task alone.
//
// The directory is decided in three steps: an absolute destDir is used as it
// is; a relative destDir is resolved against the root; an empty destDir falls
// back to WithDefaultDir, which follows the same rule and reaches the root
// itself when it is empty too.
//
// The task starts when the manager's budget has room for it. The returned task
// is its handle.
func (m *Manager) AddTask(destDir, rawURL string, opts ...TaskOption) (*Task, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	all := make([]engine.Option, 0, len(m.defaults)+1+len(opts))
	all = append(all, m.defaults...)
	all = append(all, engine.WithDir(resolveDir(m.root, m.defaultDir, destDir)))
	all = append(all, opts...)
	base := m.base
	m.mu.Unlock()

	tr, err := engine.NewTransfer(base, rawURL, all...)
	if err != nil {
		return nil, err
	}
	t := newTask(m, tr)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = tr.Delete()
		return nil, ErrClosed
	}
	m.tasks = append(m.tasks, t)
	m.mu.Unlock()

	m.poke()
	return t, nil
}

// Threads returns the size of the connection budget.
func (m *Manager) Threads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.threads
}

// SetThreads changes the size of the connection budget. Connections are taken
// from, or handed to, the running files on the next scheduling pass.
func (m *Manager) SetThreads(threads int) {
	if threads < 1 {
		threads = 1
	}
	m.mu.Lock()
	m.threads = threads
	m.mu.Unlock()
	m.poke()
}

// MaxFiles returns how many files may run at the same time.
func (m *Manager) MaxFiles() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxFiles
}

// SetMaxFiles changes how many files may run at the same time. Lowering it below
// the number of running files pauses the ones that no longer fit; they keep the
// bytes already on disk and continue when the limit allows it again.
func (m *Manager) SetMaxFiles(maxFiles int) {
	if maxFiles < 1 {
		maxFiles = 1
	}
	m.mu.Lock()
	m.maxFiles = maxFiles
	m.mu.Unlock()
	m.poke()
}

// Tasks returns a snapshot of every task the manager holds, in the order they
// were added.
func (m *Manager) Tasks() []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*Task(nil), m.tasks...)
}

// Task returns the task with the given identifier, or nil.
func (m *Manager) Task(id string) *Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.ID() == id {
			return t
		}
	}
	return nil
}

// Wait blocks until every task has reached a terminal state. A task that is
// paused is not terminal, so it keeps Wait blocked until it is resumed, deleted
// or the manager is closed.
func (m *Manager) Wait() {
	for _, t := range m.Tasks() {
		_ = t.Wait()
	}
}

// Close stops the scheduler and every task it holds. Transfers that are running
// are canceled, and the partial files stay on disk. Close may be called more
// than once.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		<-m.loopDone
		return
	}
	m.closed = true
	tasks := append([]*Task(nil), m.tasks...)
	m.mu.Unlock()

	m.cancel()
	for _, t := range tasks {
		t.markClosed()
	}
	<-m.loopDone
}

// exeDir returns the directory of the running executable. It is the base for
// every relative download path when no root is set, which keeps the output
// independent of the working directory the program happened to be started
// from. os.Executable covers Windows, macOS and Linux; when it fails, the
// working directory is the best that is left.
func exeDir() string {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		return filepath.Dir(exe)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// resolveDir picks the directory a task writes to. An absolute destDir is
// taken as it is, a relative one is placed under root, and an empty one falls
// back to defaultDir, reaching root itself when that is empty too.
func resolveDir(root, defaultDir, destDir string) string {
	if destDir == "" {
		destDir = defaultDir
	}
	if filepath.IsAbs(destDir) {
		return destDir
	}
	return filepath.Join(root, destDir)
}

// poke wakes the scheduler without blocking.
func (m *Manager) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// loop is the scheduler. Every change that could alter the allocation wakes it,
// and it then re-divides the budget from scratch, which keeps the rules in one
// place instead of spreading them over the operations that trigger them.
func (m *Manager) loop() {
	defer close(m.loopDone)
	for {
		select {
		case <-m.base.Done():
			return
		case <-m.wake:
		}
		m.reconcile()
	}
}

// allocation is the number of connections one task should be running.
type allocation struct {
	task    *Task
	workers int
}

// reconcile gives every task the number of connections the rules call for. The
// plan is made under the lock and applied outside it, because applying it can
// wait for a transfer to stop and must not hold up the other operations.
func (m *Manager) reconcile() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	threads, maxFiles := m.threads, m.maxFiles
	tasks := append([]*Task(nil), m.tasks...)
	m.mu.Unlock()

	// Waiting for a connection: not finished, not paused by whoever owns it.
	// The list is taken once, because the shares have to line up with it.
	active := make([]*Task, 0, len(tasks))
	for _, t := range tasks {
		if t.wantsWork() {
			active = append(active, t)
		}
	}

	files := len(active)
	if files > maxFiles {
		files = maxFiles
	}
	if files > threads {
		files = threads
	}

	share := make(map[*Task]int, len(active))
	for i := 0; i < files; i++ {
		// Breadth first: one connection each before anyone gets a second.
		share[active[i]] = 1
	}
	if files > 0 {
		extra := threads - files
		for i := 0; extra > 0; i = (i + 1) % files {
			share[active[i]]++
			extra--
		}
	}

	plan := make([]allocation, 0, len(tasks))
	for _, t := range tasks {
		plan = append(plan, allocation{task: t, workers: share[t]})
	}

	for _, a := range plan {
		a.task.apply(a.workers)
	}
}
