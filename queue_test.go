package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// multiServer 提供多个具名负载，带完整 Range 支持，并跟踪峰值请求
// 并发，以便测试能断言并发上限。
type multiServer struct {
	srv   *httptest.Server
	data  map[string][]byte
	delay time.Duration

	// failing 使每个请求返回 500，直到测试清除它，
	// 从而让测试能确定性地产出一个任务失败（探测请求也会失败，
	// 因此失败不会被悄悄吸收）。
	failing atomic.Bool

	mu       sync.Mutex
	inflight int
	peak     int
	requests int
}

func newMultiServer(t *testing.T, payloads map[string][]byte, delay time.Duration) *multiServer {
	t.Helper()
	ms := &multiServer{data: payloads, delay: delay}
	ms.srv = httptest.NewServer(http.HandlerFunc(ms.handle))
	t.Cleanup(ms.srv.Close)
	return ms
}

func (m *multiServer) handle(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")

	m.mu.Lock()
	m.requests++
	m.inflight++
	if m.inflight > m.peak {
		m.peak = m.inflight
	}
	data, ok := m.data[name]
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.inflight--
		m.mu.Unlock()
	}()

	// 强制失败同时覆盖探测与下载请求，使测试能保证某次任务尝试
	// 失败。403 是永久状态，因此下载器立即失败该尝试，被测试的
	// 是队列自身的重试逻辑（而非每分片瞬时退避）。
	if m.failing.Load() {
		http.Error(w, "forced failure", http.StatusForbidden)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Accept-Ranges", "bytes")
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// setFailing 使后续每个请求失败（或停止失败）。
func (m *multiServer) setFailing(v bool) { m.failing.Store(v) }

// newTestQueue 构建一个指向本地测试服务器的队列。
func newTestQueue(t *testing.T, cfg QueueConfig) *Queue {
	t.Helper()
	cfg.Template.AllowPrivateHost = true
	cfg.Template.MinSegmentSize = 1024
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 2
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 20 * time.Millisecond
	}
	q, err := NewQueue(cfg)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q
}

func waitQueue(t *testing.T, q *Queue, timeout time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- q.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("queue did not drain: %s", q)
		return nil
	}
}

// waitTaskState 等待任务到达目标状态。
func waitTaskState(t *testing.T, q *Queue, id string, want TaskState, timeout time.Duration) TaskInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, ok := q.Task(id)
		if !ok {
			t.Fatalf("no such task %q", id)
		}
		if info.State == want {
			return info
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, _ := q.Task(id)
	t.Fatalf("task %s never reached %v (now %v)", id, want, info.State)
	return info
}

// --- 测试 -----------------------------------------------------------------

func TestQueueBatchDownload(t *testing.T) {
	payloads := map[string][]byte{
		"a.bin": testFile(256 << 10),
		"b.bin": testFile(300 << 10),
		"c.bin": testFile(200 << 10),
	}
	ms := newMultiServer(t, payloads, 0)

	dir := t.TempDir()
	q := newTestQueue(t, QueueConfig{
		Concurrency: 2,
		Template:    Config{Connections: 4, Segments: 8},
		OutputDir:   dir,
	})
	ids, err := q.AddAll([]string{
		ms.srv.URL + "/a.bin",
		ms.srv.URL + "/b.bin",
		ms.srv.URL + "/c.bin",
	}, "")
	if err != nil {
		t.Fatalf("AddAll: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("got %d ids, want 3", len(ids))
	}

	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	s := q.Summary()
	if s.Completed != 3 || s.Failed != 0 {
		t.Fatalf("summary = %+v, want 3 completed", s)
	}
	for name, want := range payloads {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if sha(got) != sha(want) {
			t.Fatalf("%s content mismatch", name)
		}
	}
}

func TestQueueRespectsConcurrency(t *testing.T) {
	payloads := map[string][]byte{}
	for i := 0; i < 8; i++ {
		payloads[fmt.Sprintf("f%d.bin", i)] = testFile(96 << 10)
	}
	// 每请求一个延迟，使多个任务重叠，从而峰值并发可观察。
	ms := newMultiServer(t, payloads, 15*time.Millisecond)

	urls := make([]string, 0, len(payloads))
	for i := 0; i < 8; i++ {
		urls = append(urls, fmt.Sprintf("%s/f%d.bin", ms.srv.URL, i))
	}

	q := newTestQueue(t, QueueConfig{
		Concurrency: 2,
		Template:    Config{Connections: 1, Segments: 2},
		OutputDir:   t.TempDir(),
	})
	if _, err := q.AddAll(urls, ""); err != nil {
		t.Fatalf("AddAll: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 在批量进行中采样运行计数。
	maxRunning := 0
	for {
		info := q.Summary()
		if info.Running > maxRunning {
			maxRunning = info.Running
		}
		if info.Done() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if maxRunning > 2 {
		t.Fatalf("observed %d tasks running at once, want <= 2", maxRunning)
	}
	if s := q.Summary(); s.Completed != 8 {
		t.Fatalf("summary = %+v, want 8 completed", s)
	}
}

func TestQueueSetConcurrencyMidRun(t *testing.T) {
	payloads := map[string][]byte{}
	for i := 0; i < 6; i++ {
		payloads[fmt.Sprintf("g%d.bin", i)] = testFile(128 << 10)
	}
	ms := newMultiServer(t, payloads, 5*time.Millisecond)

	urls := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		urls = append(urls, fmt.Sprintf("%s/g%d.bin", ms.srv.URL, i))
	}

	q := newTestQueue(t, QueueConfig{
		Concurrency: 3,
		Template:    Config{Connections: 1, Segments: 2},
		OutputDir:   t.TempDir(),
	})
	ids, err := q.AddAll(urls, "")
	if err != nil {
		t.Fatalf("AddAll: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	q.SetConcurrency(1)
	time.Sleep(80 * time.Millisecond)
	if got := q.Summary().Running; got > 1 {
		t.Fatalf("after SetConcurrency(1): %d running, want <= 1", got)
	}
	q.SetConcurrency(4)
	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if s := q.Summary(); s.Completed != len(ids) {
		t.Fatalf("summary = %+v, want %d completed", s, len(ids))
	}
}

func TestQueuePauseResumeAll(t *testing.T) {
	payloads := map[string][]byte{}
	for i := 0; i < 4; i++ {
		payloads[fmt.Sprintf("p%d.bin", i)] = testFile(512 << 10)
	}
	ms := newMultiServer(t, payloads, 2*time.Millisecond)

	urls := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		urls = append(urls, fmt.Sprintf("%s/p%d.bin", ms.srv.URL, i))
	}

	dir := t.TempDir()
	q := newTestQueue(t, QueueConfig{
		Concurrency: 2,
		Template:    Config{Connections: 4, Segments: 8},
		OutputDir:   dir,
	})
	if _, err := q.AddAll(urls, ""); err != nil {
		t.Fatalf("AddAll: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 让点东西下载后再暂停。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && q.Summary().Running == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	q.Pause()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && q.Summary().Running != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := q.Summary().Running; got != 0 {
		t.Fatalf("after Pause: %d running, want 0", got)
	}
	if got := q.Summary().Paused; got == 0 {
		t.Fatal("after Pause: no paused tasks")
	}

	q.Resume()
	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	for name, want := range payloads {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if sha(got) != sha(want) {
			t.Fatalf("%s content mismatch after pause/resume", name)
		}
	}
}

func TestQueuePerTaskControl(t *testing.T) {
	payloads := map[string][]byte{
		"x.bin": testFile(512 << 10),
		"y.bin": testFile(512 << 10),
	}
	ms := newMultiServer(t, payloads, 2*time.Millisecond)

	dir := t.TempDir()
	q := newTestQueue(t, QueueConfig{
		Concurrency: 2,
		Template:    Config{Connections: 4, Segments: 8},
		OutputDir:   dir,
	})
	xID, err := q.Add(ms.srv.URL+"/x.bin", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := q.Add(ms.srv.URL+"/y.bin", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 只暂停 x；y 应当完成，且 x 必须保持暂停
	// （队列 Resume 不应复活被调用方暂停的任务）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, _ := q.Task(xID); info.State == TaskRunning {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := q.PauseTask(xID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}
	waitTaskState(t, q, xID, TaskPaused, 5*time.Second)

	q.Resume() // 队列级；不应触碰被调用方暂停的任务
	time.Sleep(150 * time.Millisecond)
	if info, _ := q.Task(xID); info.State != TaskPaused {
		t.Fatalf("queue Resume revived a user-paused task: %v", info.State)
	}

	if err := q.ResumeTask(xID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	for name, want := range payloads {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if sha(got) != sha(want) {
			t.Fatalf("%s content mismatch", name)
		}
	}
}

func TestQueueCancelTask(t *testing.T) {
	ms := newMultiServer(t, map[string][]byte{"big.bin": testFile(4 << 20)}, time.Millisecond)

	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Connections: 2, Segments: 4},
		OutputDir:   t.TempDir(),
	})
	id, err := q.Add(ms.srv.URL+"/big.bin", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if err := q.CancelTask(id); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if err := waitQueue(t, q, 15*time.Second); err != nil {
		t.Fatalf("Wait after cancel: %v", err)
	}
	info, _ := q.Task(id)
	if info.State != TaskCanceled {
		t.Fatalf("state = %v, want canceled", info.State)
	}
	if q.Summary().Canceled != 1 {
		t.Fatalf("summary = %+v, want 1 canceled", q.Summary())
	}
}

func TestQueueRetryOnFailure(t *testing.T) {
	payload := testFile(64 << 10)
	ms := newMultiServer(t, map[string][]byte{"flaky.bin": payload}, 0)
	ms.setFailing(true) // 每个请求都失败，直到测试清除它

	dir := t.TempDir()
	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Connections: 2, Segments: 4},
		OutputDir:   dir,
		MaxRetries:  100, // 一直重试，直到测试放行
		RetryDelay:  20 * time.Millisecond,
	})
	id, err := q.Add(ms.srv.URL+"/flaky.bin", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 等到至少一次尝试失败（Err 在重试挂起期间就被记录）。
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if info, _ := q.Task(id); info.Err != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if info, _ := q.Task(id); info.Err == nil {
		t.Fatal("task never reported a failure while the server was failing")
	}

	ms.setFailing(false) // 让下一次尝试成功
	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	info, _ := q.Task(id)
	if info.State != TaskCompleted {
		t.Fatalf("state = %v (err %v), want completed after retries", info.State, info.Err)
	}
	if info.Attempts < 2 {
		t.Fatalf("attempts = %d, want >= 2", info.Attempts)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "flaky.bin"))
	if sha(got) != sha(payload) {
		t.Fatal("content mismatch after retries")
	}
}

func TestQueueFailedTaskReportsError(t *testing.T) {
	ms := newMultiServer(t, map[string][]byte{"ok.bin": testFile(32 << 10)}, 0)

	q := newTestQueue(t, QueueConfig{
		Concurrency: 2,
		Template:    Config{Connections: 2, Segments: 2},
		OutputDir:   t.TempDir(),
		MaxRetries:  0,
	})
	okID, err := q.Add(ms.srv.URL+"/ok.bin", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	badID, err := q.Add(ms.srv.URL+"/missing.bin", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err = waitQueue(t, q, 20*time.Second)
	if err == nil {
		t.Fatal("Wait = nil, want an aggregate failure error")
	}
	var te *TaskError
	if !errors.As(err, &te) {
		t.Fatalf("error %v is not a *TaskError", err)
	}
	if te.ID != badID {
		t.Fatalf("failing task = %s, want %s", te.ID, badID)
	}
	if s := q.Summary(); s.Completed != 1 || s.Failed != 1 {
		t.Fatalf("summary = %+v, want 1 completed + 1 failed", s)
	}
	if info, ok := q.Task(okID); !ok || info.State != TaskCompleted {
		t.Fatalf("healthy task = %+v (ok=%v), want completed", info, ok)
	}
}

func TestQueueAddWhileRunning(t *testing.T) {
	payloads := map[string][]byte{
		"first.bin":  testFile(256 << 10),
		"second.bin": testFile(256 << 10),
	}
	ms := newMultiServer(t, payloads, 2*time.Millisecond)
	dir := t.TempDir()

	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Connections: 2, Segments: 4},
		OutputDir:   dir,
	})
	if _, err := q.Add(ms.srv.URL+"/first.bin", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 第一个还在运行时入队第二个。
	time.Sleep(20 * time.Millisecond)
	if _, err := q.Add(ms.srv.URL+"/second.bin", ""); err != nil {
		t.Fatalf("Add while running: %v", err)
	}
	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if s := q.Summary(); s.Completed != 2 {
		t.Fatalf("summary = %+v, want 2 completed", s)
	}
	for name, want := range payloads {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if sha(got) != sha(want) {
			t.Fatalf("%s content mismatch", name)
		}
	}
}

func TestQueueAbort(t *testing.T) {
	ms := newMultiServer(t, map[string][]byte{"slow.bin": testFile(8 << 20)}, time.Millisecond)
	q := newTestQueue(t, QueueConfig{
		Concurrency: 2,
		Template:    Config{Connections: 4, Segments: 8},
		OutputDir:   t.TempDir(),
	})
	if _, err := q.Add(ms.srv.URL+"/slow.bin", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	q.Abort()
	if err := waitQueue(t, q, 15*time.Second); !errors.Is(err, ErrAborted) {
		t.Fatalf("Wait = %v, want ErrAborted", err)
	}
}

func TestQueueStopThenRestart(t *testing.T) {
	payloads := map[string][]byte{"s1.bin": testFile(512 << 10), "s2.bin": testFile(512 << 10)}
	ms := newMultiServer(t, payloads, 2*time.Millisecond)
	dir := t.TempDir()

	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Connections: 4, Segments: 8},
		OutputDir:   dir,
	})
	if _, err := q.AddAll([]string{ms.srv.URL + "/s1.bin", ms.srv.URL + "/s2.bin"}, ""); err != nil {
		t.Fatalf("AddAll: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	q.Stop()
	if err := waitQueue(t, q, 15*time.Second); err != nil {
		t.Fatalf("Wait after Stop: %v", err)
	}
	if s := q.Summary(); s.Running != 0 {
		t.Fatalf("still running after Stop: %+v", s)
	}

	// 再次 Start：被挂起的任务恢复并完成。
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("re-Start: %v", err)
	}
	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait after re-Start: %v", err)
	}
	if s := q.Summary(); s.Completed != 2 {
		t.Fatalf("summary = %+v, want 2 completed", s)
	}
	for name, want := range payloads {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if sha(got) != sha(want) {
			t.Fatalf("%s content mismatch after stop/restart", name)
		}
	}
}

func TestQueueClearCacheAndRestartTask(t *testing.T) {
	payload := testFile(256 << 10)
	ms := newMultiServer(t, map[string][]byte{"c.bin": payload}, 0)
	dir := t.TempDir()

	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Connections: 2, Segments: 4},
		OutputDir:   dir,
	})
	id, err := q.Add(ms.srv.URL+"/c.bin", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := waitQueue(t, q, 20*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	out := filepath.Join(dir, "c.bin")
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output missing: %v", err)
	}

	// 对已完成任务 ClearCache 仅移除残留附属文件。
	if err := q.ClearTaskCache(id); err != nil {
		t.Fatalf("ClearTaskCache: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("ClearTaskCache removed the output file: %v", err)
	}

	// RestartTask 重新下载，先删除之前的输出。
	if err := q.RestartTask(id); err != nil {
		t.Fatalf("RestartTask: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("re-Start: %v", err)
	}
	if err := waitQueue(t, q, 20*time.Second); err != nil {
		t.Fatalf("Wait after restart: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if sha(got) != sha(payload) {
		t.Fatal("content mismatch after RestartTask")
	}
	if info, _ := q.Task(id); info.State != TaskCompleted {
		t.Fatalf("state = %v, want completed", info.State)
	}
}

func TestQueueSetConnectionsAndSegmentsMidRun(t *testing.T) {
	payload := testFile(2 << 20)
	ms := newMultiServer(t, map[string][]byte{"t.bin": payload}, 200*time.Microsecond)
	dir := t.TempDir()

	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Connections: 2, Segments: 4},
		OutputDir:   dir,
	})
	if _, err := q.Add(ms.srv.URL+"/t.bin", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	q.SetConnectionsAndSegments(6, 16)

	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "t.bin"))
	if sha(got) != sha(payload) {
		t.Fatal("content mismatch after mid-run SetConnectionsAndSegments")
	}
}

func TestQueueDerivedNamesAreUniqueAndSafe(t *testing.T) {
	dir := t.TempDir()
	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Connections: 1, Segments: 1},
		OutputDir:   dir,
	})
	// 同名两次必须不冲突。
	id1, err := q.Add("http://example.com/dir/same.bin", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	id2, err := q.Add("http://example.com/other/same.bin", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	i1, _ := q.Task(id1)
	i2, _ := q.Task(id2)
	if i1.OutputPath == i2.OutputPath {
		t.Fatalf("colliding output paths: %q", i1.OutputPath)
	}
	if filepath.Dir(i1.OutputPath) != dir || filepath.Dir(i2.OutputPath) != dir {
		t.Fatalf("paths escaped OutputDir: %q %q", i1.OutputPath, i2.OutputPath)
	}
}

func TestQueueRejectsBadURLAtAdd(t *testing.T) {
	// AllowPrivateHost 保持默认 false，使内网目标在入队时就被拒绝，
	// 无需任何 DNS 查询。
	q, err := NewQueue(QueueConfig{Concurrency: 1, OutputDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	cases := []string{
		"http://127.0.0.1/x",
		"http://localhost/x",
		"http://10.1.2.3/x",
		"http://[::1]/x",
		"ftp://example.com/x",
		"file:///etc/passwd",
		"",
	}
	for _, u := range cases {
		if _, err := q.Add(u, ""); err == nil {
			t.Errorf("Add(%q) = nil, want error", u)
		}
	}
	// 公网主机名被接受（DNS 解析延迟到启动时）。
	if _, err := q.Add("http://example.com/ok.bin", ""); err != nil {
		t.Errorf("Add(public host) = %v, want nil", err)
	}
	// AllowPrivateHost 开启时，LAN 目标被接受。
	lan, err := NewQueue(QueueConfig{
		Concurrency: 1,
		OutputDir:   t.TempDir(),
		Template:    Config{AllowPrivateHost: true},
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	if _, err := lan.Add("http://127.0.0.1:8080/x", ""); err != nil {
		t.Errorf("Add(loopback, allowPrivate) = %v, want nil", err)
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"file.bin":            "file.bin",
		"../../etc/passwd":    "passwd",
		`..\..\windows\x`:     "x",
		"a/b/c.tar.gz":        "c.tar.gz",
		"":                    "",
		"..":                  "",
		"weird:name?.bin":     "weird_name_.bin",
		"with\x01control.bin": "withcontrol.bin",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQueueAlreadyRunning(t *testing.T) {
	ms := newMultiServer(t, map[string][]byte{"e.bin": testFile(512 << 10)}, time.Millisecond)
	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Connections: 2, Segments: 4},
		OutputDir:   t.TempDir(),
	})
	if _, err := q.Add(ms.srv.URL+"/e.bin", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := q.Start(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start = %v, want ErrAlreadyRunning", err)
	}
	q.Abort()
	_ = waitQueue(t, q, 15*time.Second)
}

func TestQueueEmptyStartFails(t *testing.T) {
	q := newTestQueue(t, QueueConfig{})
	if err := q.Start(context.Background()); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Start on empty queue = %v, want ErrInvalidConfig", err)
	}
}

func TestQueueCallbacks(t *testing.T) {
	ms := newMultiServer(t, map[string][]byte{"cb.bin": testFile(128 << 10)}, 0)

	var states int64
	var progress int64
	var done int64
	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Connections: 2, Segments: 4},
		OutputDir:   t.TempDir(),
		OnTaskStateChange: func(TaskInfo) {
			atomic.AddInt64(&states, 1)
		},
		OnTaskProgress: func(TaskInfo) {
			atomic.AddInt64(&progress, 1)
		},
		OnQueueDone: func(Summary) {
			atomic.AddInt64(&done, 1)
		},
	})
	if _, err := q.Add(ms.srv.URL+"/cb.bin", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := waitQueue(t, q, 20*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if atomic.LoadInt64(&states) == 0 {
		t.Error("OnTaskStateChange never fired")
	}
	if atomic.LoadInt64(&done) != 1 {
		t.Errorf("OnQueueDone fired %d times, want 1", atomic.LoadInt64(&done))
	}
}

func TestQueueConcurrentAccess(t *testing.T) {
	payloads := map[string][]byte{}
	for i := 0; i < 6; i++ {
		payloads[fmt.Sprintf("cc%d.bin", i)] = testFile(128 << 10)
	}
	ms := newMultiServer(t, payloads, 100*time.Microsecond)
	urls := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		urls = append(urls, fmt.Sprintf("%s/cc%d.bin", ms.srv.URL, i))
	}

	q := newTestQueue(t, QueueConfig{
		Concurrency: 3,
		Template:    Config{Connections: 2, Segments: 4},
		OutputDir:   t.TempDir(),
	})
	ids, err := q.AddAll(urls, "")
	if err != nil {
		t.Fatalf("AddAll: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = q.Tasks()
					_ = q.Summary()
					_ = q.String()
					for _, id := range ids {
						_, _ = q.Task(id)
					}
				}
			}
		}()
	}
	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	close(stop)
	wg.Wait()
	if s := q.Summary(); s.Completed != len(ids) {
		t.Fatalf("summary = %+v, want %d completed", s, len(ids))
	}
}

// 确保队列对忽略 Range 的服务器（退化路径）也能工作。
func TestQueueNoRangeFallback(t *testing.T) {
	data := testFile(200 << 10)
	srv := noRangeServer(t, data)

	dir := t.TempDir()
	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Connections: 4, Segments: 8},
		OutputDir:   dir,
	})
	if _, err := q.Add(srv.URL+"/x.bin", filepath.Join(dir, "nr.bin")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := waitQueue(t, q, 20*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "nr.bin"))
	if sha(got) != sha(data) {
		t.Fatal("content mismatch in no-range fallback")
	}
}
