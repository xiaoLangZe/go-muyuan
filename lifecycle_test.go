package downloader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaoLangZe/go-muyuan/internal/meta"
)

// mutServer 是支持 Range 的服务器，载荷可随时切换，并统计请求数。
// 用于续传拒绝矩阵：同 URL 的内容变化后旧进度必须被丢弃。
type mutServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	data    []byte
	lastMod string
	hits    int64
	delay   time.Duration
}

func newMutServerDelay(t *testing.T, data []byte, delay time.Duration) *mutServer {
	t.Helper()
	m := &mutServer{data: data, delay: delay, lastMod: "Wed, 21 Oct 2026 07:28:00 GMT"}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&m.hits, 1)
		if m.delay > 0 {
			time.Sleep(m.delay)
		}
		m.mu.Lock()
		d := append([]byte(nil), m.data...)
		lm := m.lastMod
		m.mu.Unlock()
		setLM := func(h http.Header) { h.Set("Last-Modified", lm) }
		if r.Header.Get("Range") == "" {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(d)))
			setLM(w.Header())
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(d)
			return
		}
		var start, end int64
		_, _ = fmtSscanf(r.Header.Get("Range"), &start, &end)
		if end >= int64(len(d)) {
			end = int64(len(d)) - 1
		}
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.Itoa(len(d)))
		setLM(w.Header())
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(d[start : end+1])
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// setPayload 替换载荷并推进 Last-Modified，模拟远端内容更新。
// 分辨率为秒；测试在同一秒内多次调用时需自行控制序。
func (m *mutServer) setPayload(d []byte) {
	m.mu.Lock()
	m.data = append([]byte(nil), d...)
	m.lastMod = time.Now().Add(time.Second).UTC().Format(http.TimeFormat)
	m.mu.Unlock()
}

// waitPartial 等待下载产生部分进度且仍处于运行状态。
func waitPartial(t *testing.T, d *Downloader) {
	t.Helper()
	waitFor(t, func() bool {
		p := d.Progress()
		return p.State == StateRunning && p.Downloaded > 0 && p.Downloaded < p.Total
	}, "download reached partial progress")
}

// TestDownloaderCloseRunning 运行中的下载被 Close 中止。
// Close 内部即 Abort，返回 ErrAborted 是预期语义。
func TestDownloaderCloseRunning(t *testing.T) {
	srv, _ := rangeServer(t, testFile(1<<20), 50*time.Millisecond)
	d := newTestDownloader(t, srv.URL, t.TempDir()+"/f.bin", 2, 4)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := d.Close(); !errors.Is(err, ErrAborted) {
		t.Fatalf("Close = %v, want ErrAborted", err)
	}
	if !d.Progress().State.IsTerminal() {
		t.Errorf("state after Close = %v, want terminal", d.Progress().State)
	}
}

// TestDownloaderReStartAfterFinish 终态后可再次 Start。
func TestDownloaderReStartAfterFinish(t *testing.T) {
	out := t.TempDir() + "/f.bin"
	srv, _ := rangeServer(t, testFile(64<<10), 0)
	d := newTestDownloader(t, srv.URL, out, 2, 4)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// 完成后再次 Start：应允许（上一 manager 已结束）并重新下载。
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("re-Start after finish: %v", err)
	}
	if err := d.Wait(); err != nil {
		t.Fatalf("re-Wait: %v", err)
	}
}

// TestDownloaderParentContextCancel 父 ctx 取消使下载失败并返回
// context.Canceled。
func TestDownloaderParentContextCancel(t *testing.T) {
	srv, _ := rangeServer(t, testFile(4<<20), 30*time.Millisecond)
	d := newTestDownloader(t, srv.URL, t.TempDir()+"/f.bin", 2, 4)
	ctx, cancel := context.WithCancel(context.Background())
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	cancel()
	err := d.Wait()
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v, want context.Canceled", err)
	}
}

// TestQueueSignalsChannel 通过 q.Signals() 发送控制信号生效。
func TestQueueSignalsChannel(t *testing.T) {
	ms := newMultiServer(t, map[string][]byte{"s.bin": testFile(64 << 10)}, 20*time.Millisecond)
	q := newTestQueue(t, QueueConfig{Concurrency: 2, OutputDir: t.TempDir()})
	if _, err := q.Add(ms.srv.URL+"/s.bin", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	q.Signals() <- QueueSignal{Type: QSigSetConcurrency, Concurrency: 1}
	time.Sleep(100 * time.Millisecond)
	if got := q.Summary().Concurrency; got != 1 {
		t.Errorf("Concurrency = %d, want 1", got)
	}
	q.Signals() <- QueueSignal{Type: QSigAbort}
	_ = q.Wait()
}

// TestQueueCloseRunning 关闭运行中的队列，任务被取消。
// Close 内部即 Abort，返回 ErrAborted 是预期语义。
func TestQueueCloseRunning(t *testing.T) {
	ms := newMultiServer(t, map[string][]byte{"c1.bin": testFile(1 << 20)}, 40*time.Millisecond)
	q := newTestQueue(t, QueueConfig{Concurrency: 1, OutputDir: t.TempDir()})
	if _, err := q.Add(ms.srv.URL+"/c1.bin", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := q.Close(); !errors.Is(err, ErrAborted) {
		t.Fatalf("Close = %v, want ErrAborted", err)
	}
	if got := q.Summary().Canceled; got == 0 {
		t.Errorf("Canceled = %d, want >0", got)
	}
}

// multiRequests 返回多文件服务器累计请求数。
func multiRequests(ms *multiServer) int {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.requests
}

// TestQueueClearCacheAll 队列级 ClearCache：终态任务输出保留、
// 附属文件被清。
func TestQueueClearCacheAll(t *testing.T) {
	payload := testFile(128 << 10)
	ms := newMultiServer(t, map[string][]byte{"cc.bin": payload}, 0)
	dir := t.TempDir()
	q := newTestQueue(t, QueueConfig{Concurrency: 1, OutputDir: dir})
	if _, err := q.Add(ms.srv.URL+"/cc.bin", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := waitQueue(t, q, 20*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	out := filepath.Join(dir, "cc.bin")
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output missing: %v", err)
	}
	if err := q.ClearCache(); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	waitFor(t, func() bool {
		_, e1 := os.Stat(meta.MetaPath(out))
		_, e2 := os.Stat(meta.PartialPath(out))
		return os.IsNotExist(e1) && os.IsNotExist(e2)
	}, "sidecars removed")
	if _, err := os.Stat(out); err != nil {
		t.Fatal("ClearCache removed the finished output")
	}
	if info, ok := q.Task("task-1"); !ok || info.State != TaskCompleted {
		t.Fatalf("task snapshot = %+v, ok=%v", info, ok)
	}
}

// TestQueueClearCacheRunningTask 运行中任务在队列级 ClearCache 后
// 必须从头重新下载（回归：此前该路径不清理缓存导致静默续传）。
func TestQueueClearCacheRunningTask(t *testing.T) {
	payload := testFile(256 << 10)
	ms := newMultiServer(t, map[string][]byte{"cr.bin": payload}, 25*time.Millisecond)
	dir := t.TempDir()
	q := newTestQueue(t, QueueConfig{Concurrency: 1, OutputDir: dir})
	id, err := q.Add(ms.srv.URL+"/cr.bin", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	before := multiRequests(ms)
	if err := q.ClearCache(); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	if info := waitTaskState(t, q, id, TaskCompleted, 20*time.Second); info.State != TaskCompleted {
		t.Fatalf("task not completed: %v", info.State)
	}
	if err := waitQueue(t, q, 20*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if multiRequests(ms) <= before {
		t.Errorf("no request after ClearCache: %d <= %d", multiRequests(ms), before)
	}
	out := filepath.Join(dir, "cr.bin")
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if sha(got) != sha(payload) {
		t.Fatal("content mismatch after queue ClearCache")
	}
	// 完成后附属文件应已被完成的 finalize 清掉（meta 由 complete 删除）。
	if _, err := os.Stat(meta.MetaPath(out)); !os.IsNotExist(err) {
		t.Error("meta sidecar survived completion")
	}
}

// TestResumeRejectsChangedPayload 远端内容变化后必须丢弃旧进度、
// 以新内容覆盖输出。
func TestResumeRejectsChangedPayload(t *testing.T) {
	out := t.TempDir() + "/r.bin"
	old := testFile(512 << 10)
	ms := newMutServerDelay(t, old, 10*time.Millisecond)

	d := newTestDownloader(t, ms.srv.URL, out, 2, 4)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitPartial(t, d)
	d.Stop()
	waitState(t, d, StateStopped, 10*time.Second)
	if _, err := os.Stat(meta.MetaPath(out)); err != nil {
		t.Fatalf("meta not written on stop: %v", err)
	}

	// 同长不同内容 + 新 Last-Modified → 续传校验必须拒绝旧进度。
	replacement := testFile(512 << 10)
	replacement[0] = ^replacement[0]
	ms.setPayload(replacement)

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("re-Start: %v", err)
	}
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if sha(got) != sha(replacement) {
		t.Fatal("output has stale bytes after remote change")
	}
	if sha(got) == sha(old) {
		t.Fatal("output equals old payload; resume not invalidated")
	}
}

// TestResumeSamePayloadContinues 内容未变时续传得以继续（对照）。
func TestResumeSamePayloadContinues(t *testing.T) {
	out := t.TempDir() + "/c.bin"
	payload := testFile(512 << 10)
	ms := newMutServerDelay(t, payload, 10*time.Millisecond)

	d := newTestDownloader(t, ms.srv.URL, out, 2, 4)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitPartial(t, d)
	d.Stop()
	waitState(t, d, StateStopped, 10*time.Second)
	hitsAfterStop := atomic.LoadInt64(&ms.hits)

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("re-Start: %v", err)
	}
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if atomic.LoadInt64(&ms.hits) <= hitsAfterStop {
		t.Fatal("resume made no requests")
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if sha(got) != sha(payload) {
		t.Fatal("content mismatch after resume")
	}
}

// TestConcurrentProgressDuringReconfigure 并发读取快照并热重配。
// 回归：Snapshot 曾在 segments 为空时越过锁边界读 segmentCount；
// CI 的 -race 会检验本例下的数据竞争，实际跑通则验证不 panic。
func TestConcurrentProgressDuringReconfigure(t *testing.T) {
	payload := testFile(1 << 20)
	srv, _ := rangeServer(t, payload, 10*time.Millisecond)
	d := newTestDownloader(t, srv.URL, t.TempDir()+"/f.bin", 2, 4)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = d.Progress()
		}
	}()
	for i := 0; i < 8; i++ {
		d.SetSegments(2 + i%3)
		time.Sleep(25 * time.Millisecond)
	}
	<-done

	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got := readFileBytes(t, d.Progress().OutputPath)
	if sha(got) != sha(payload) {
		t.Fatal("content mismatch after concurrent reconfigure")
	}
}

// waitFor 轮询 cond 直到为真或超时。
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition never met: %s", msg)
}

// fmtSscanf 从 "bytes=start-end" 解析整数对。
func fmtSscanf(s string, a, b *int64) (int, error) {
	parts := strings.SplitN(strings.TrimPrefix(s, "bytes="), "-", 2)
	if len(parts) != 2 {
		return 0, errors.New("bad range")
	}
	v1, err1 := strconv.ParseInt(parts[0], 10, 64)
	v2, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, errors.New("bad integers")
	}
	*a, *b = v1, v2
	return 2, nil
}
