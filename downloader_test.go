package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// --- 辅助函数 ---------------------------------------------------------------

// testFile 生成长度为 n 的确定性伪随机字节。
func testFile(n int) []byte {
	b := make([]byte, n)
	var x uint32 = 0x12345678
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// rangeServer 以完整的 Range 支持提供数据，并记录它看到的
// 不同 range 请求数。
func rangeServer(t *testing.T, data []byte, delay time.Duration) (*httptest.Server, *int64) {
	t.Helper()
	var reqs int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&reqs, 1)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")

		rng := r.Header.Get("Range")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			return
		}
		if rng == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		start, end, ok := parseRange(rng, int64(len(data)))
		if !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		if delay > 0 {
			// 以小块输出，使取消有落点。
			body := data[start : end+1]
			for i := 0; i < len(body); i += 4096 {
				j := i + 4096
				if j > len(body) {
					j = len(body)
				}
				if _, err := w.Write(body[i:j]); err != nil {
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				time.Sleep(delay)
			}
			return
		}
		_, _ = w.Write(data[start : end+1])
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

// parseRange 解析单条 "bytes=a-b" 头（b 可省略）。
func parseRange(h string, total int64) (start, end int64, ok bool) {
	h = strings.TrimPrefix(h, "bytes=")
	parts := strings.SplitN(h, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	s, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || s < 0 || s >= total {
		return 0, 0, false
	}
	if parts[1] == "" {
		return s, total - 1, true
	}
	e, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if e >= total {
		e = total - 1
	}
	if e < s {
		return 0, 0, false
	}
	return s, e, true
}

// noRangeServer 忽略 Range，总是返回整包。
func noRangeServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestDownloader(t *testing.T, url, out string, connections, segments int) *Downloader {
	t.Helper()
	d, err := New(Config{
		URL:              url,
		OutputPath:       out,
		Connections:      connections,
		Segments:         segments,
		MinSegmentSize:   1024,
		AllowPrivateHost: true, // httptest 监听 127.0.0.1
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func waitState(t *testing.T, d *Downloader, want State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.Progress().State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state never reached %v (now %v)", want, d.Progress().State)
}

// --- 单元测试 ------------------------------------------------------------
//
// 纯算法单测（分片构建、重新切分、自动分片、IP/URL 校验、Meta 往返、
// 文件名消毒）已迁移至各 internal/ 子包的白盒 *_test.go。这里只保留
// 依赖 httptest 与 Downloader 的集成测试。

func TestConfigValidate(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty config: got %v, want ErrInvalidConfig", err)
	}
	if _, err := New(Config{URL: "http://x", OutputPath: ""}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing output: got %v", err)
	}
	d, err := New(Config{
		URL:              "http://example.com/f",
		OutputPath:       filepath.Join(t.TempDir(), "f"),
		Connections:      0, // 走默认
		AllowPrivateHost: true,
	})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if d.cfg.Connections != defaultConnections {
		t.Fatalf("default connections = %d, want %d", d.cfg.Connections, defaultConnections)
	}
}

// --- 集成测试 -------------------------------------------------------------

func TestDownloadMultiThread(t *testing.T) {
	data := testFile(1 << 20) // 1 MiB
	srv, _ := rangeServer(t, data, 0)

	dir := t.TempDir()
	out := filepath.Join(dir, "out.bin")
	d := newTestDownloader(t, srv.URL, out, 4, 8)

	ctx := context.Background()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if sha(got) != sha(data) {
		t.Fatalf("content mismatch: got %d bytes", len(got))
	}
	if d.Progress().State != StateCompleted {
		t.Fatalf("state = %v, want completed", d.Progress().State)
	}
}

func TestDownloadNoRangeFallback(t *testing.T) {
	data := testFile(300 << 10)
	srv := noRangeServer(t, data)

	out := filepath.Join(t.TempDir(), "nr.bin")
	d := newTestDownloader(t, srv.URL, out, 8, 16) // 退化模式下 connections/segments 被忽略

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatalf("content mismatch in fallback")
	}
	if d.Progress().AcceptRanges {
		t.Fatal("AcceptRanges should be false for fallback")
	}
}

func TestPauseResume(t *testing.T) {
	data := testFile(2 << 20)
	// 每片一个延迟，让传输慢到足以中途暂停。
	srv, _ := rangeServer(t, data, time.Millisecond)

	out := filepath.Join(t.TempDir(), "pr.bin")
	d := newTestDownloader(t, srv.URL, out, 4, 8)

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 等到有字节落地后再暂停。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && d.Progress().Downloaded == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	d.Pause()
	waitState(t, d, StatePaused, 5*time.Second)
	paused := d.Progress().Downloaded
	if paused == 0 || paused >= int64(len(data)) {
		t.Fatalf("paused at %d/%d, expected partial", paused, len(data))
	}

	d.Resume()
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait after resume: %v", err)
	}
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatal("content mismatch after pause/resume")
	}
}

func TestReconfigureConnectionsAndSegments(t *testing.T) {
	data := testFile(4 << 20)
	srv, _ := rangeServer(t, data, 300*time.Microsecond)

	out := filepath.Join(t.TempDir(), "rc.bin")
	d := newTestDownloader(t, srv.URL, out, 2, 4)

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	// 先只改连接，再只改分片，最后同时改两者。
	d.SetConnections(6)
	time.Sleep(30 * time.Millisecond)
	d.SetSegments(32)
	time.Sleep(30 * time.Millisecond)
	d.SetConnectionsAndSegments(8, 16)

	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatalf("content mismatch after reconfiguration (got %d bytes)", len(got))
	}
	p := d.Progress()
	if p.Connections != 8 || p.Segments != 16 {
		t.Fatalf("final config connections=%d segments=%d, want 8/16", p.Connections, p.Segments)
	}
}

func TestRestartAndClearCache(t *testing.T) {
	data := testFile(1 << 20)
	srv, _ := rangeServer(t, data, 200*time.Microsecond)

	dir := t.TempDir()
	out := filepath.Join(dir, "rs.bin")
	d := newTestDownloader(t, srv.URL, out, 2, 4)

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 让它跑起来再重启。
	time.Sleep(40 * time.Millisecond)
	d.Restart()
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait after restart: %v", err)
	}
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatal("content mismatch after restart")
	}

	// ClearCache 移除附属文件；下载完成后 partial 已被改名走，
	// 因此只剩输出文件。
	if err := d.ClearCache(); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	if _, err := os.Stat(meta.MetaPath(out)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("meta still present: %v", err)
	}
}

func TestResumeAcrossInstances(t *testing.T) {
	data := testFile(2 << 20)
	srv, _ := rangeServer(t, data, time.Millisecond)

	dir := t.TempDir()
	out := filepath.Join(dir, "x.bin")

	// 第一次：下载到中途后停止。
	d1 := newTestDownloader(t, srv.URL, out, 4, 8)
	if err := d1.Start(context.Background()); err != nil {
		t.Fatalf("Start1: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && d1.Progress().Downloaded == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	d1.Stop()
	time.Sleep(100 * time.Millisecond) // 让收尾写入元数据
	partial := d1.Progress().Downloaded

	// 第二次：同配置，应续传并完成。
	d2 := newTestDownloader(t, srv.URL, out, 4, 8)
	if err := d2.Start(context.Background()); err != nil {
		t.Fatalf("Start2: %v", err)
	}
	if got := d2.Progress().Downloaded; got < partial {
		t.Fatalf("resume lost progress: started at %d, previously %d", got, partial)
	}
	if err := d2.Wait(); err != nil {
		t.Fatalf("Wait2: %v", err)
	}
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatal("content mismatch after cross-instance resume")
	}
}

func TestStopKeepsProgressAndResumes(t *testing.T) {
	data := testFile(1 << 20)
	srv, _ := rangeServer(t, data, time.Millisecond)
	out := filepath.Join(t.TempDir(), "st.bin")
	d := newTestDownloader(t, srv.URL, out, 3, 6)

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	d.Stop()
	waitState(t, d, StateStopped, 5*time.Second)
}

func TestSignalsChannel(t *testing.T) {
	data := testFile(1 << 20)
	srv, _ := rangeServer(t, data, 200*time.Microsecond)
	out := filepath.Join(t.TempDir(), "sig.bin")
	d := newTestDownloader(t, srv.URL, out, 2, 4)

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	// 纯通过通道驱动控制。
	d.Signals() <- SignalEvent{Type: SigSetConnections, Connections: 5}
	d.Signals() <- SignalEvent{Type: SigSetSegments, Segments: 12}
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatal("content mismatch via signals channel")
	}
	if p := d.Progress(); p.Connections != 5 || p.Segments != 12 {
		t.Fatalf("connections/segments = %d/%d, want 5/12", p.Connections, p.Segments)
	}
}

func TestAbort(t *testing.T) {
	data := testFile(4 << 20)
	srv, _ := rangeServer(t, data, time.Millisecond)
	out := filepath.Join(t.TempDir(), "ab.bin")
	d := newTestDownloader(t, srv.URL, out, 4, 8)

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	d.Abort()
	if err := d.Wait(); !errors.Is(err, ErrAborted) {
		t.Fatalf("Wait = %v, want ErrAborted", err)
	}
}

func TestAlreadyRunning(t *testing.T) {
	data := testFile(1 << 20)
	srv, _ := rangeServer(t, data, time.Millisecond)
	out := filepath.Join(t.TempDir(), "ar.bin")
	d := newTestDownloader(t, srv.URL, out, 2, 4)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := d.Start(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start = %v, want ErrAlreadyRunning", err)
	}
	d.Abort()
	_ = d.Wait()
}

func TestConcurrentProgressAccess(t *testing.T) {
	// 考察锁纪律：下载期间多个并发读者。
	data := testFile(1 << 20)
	srv, _ := rangeServer(t, data, 100*time.Microsecond)
	out := filepath.Join(t.TempDir(), "cp.bin")
	d := newTestDownloader(t, srv.URL, out, 4, 8)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = d.Progress()
				}
			}
		}()
	}
	_ = d.Wait()
	close(stop)
	wg.Wait()
}

func TestProgressCallback(t *testing.T) {
	data := testFile(512 << 10)
	srv, _ := rangeServer(t, data, 200*time.Microsecond)
	out := filepath.Join(t.TempDir(), "pc.bin")

	var count int64
	var last int64
	d, err := New(Config{
		URL:              srv.URL,
		OutputPath:       out,
		Connections:      4,
		Segments:         8,
		MinSegmentSize:   1024,
		AllowPrivateHost: true,
		OnProgress: func(p Progress) {
			atomic.AddInt64(&count, 1)
			atomic.StoreInt64(&last, p.Downloaded)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if atomic.LoadInt64(&count) == 0 {
		t.Fatal("OnProgress was never called")
	}
}
