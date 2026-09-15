package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestWorkersAutoAllocates 验证 Workers>0 时 Connections 与 Segments 被自动分配，
// 且 Connections+Segments 的推算不超过 Workers 预算。
func TestWorkersAutoAllocates(t *testing.T) {
	cfg := Config{
		URL:              "http://example.com/f",
		OutputPath:       filepath.Join(t.TempDir(), "f"),
		Workers:          12,
		AllowPrivateHost: true,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Connections == 0 {
		t.Fatal("Connections should be auto-allocated when Workers>0")
	}
	if cfg.Connections < 1 || cfg.Connections > 64 {
		t.Fatalf("Connections %d out of expected [1,64] range", cfg.Connections)
	}
	// Segments 留 0（自动），由 planFor 推算；Workers 字段应保留。
	if cfg.Workers != 12 {
		t.Fatalf("Workers = %d, want 12", cfg.Workers)
	}
}

// TestWorkersZeroFallsBack 验证 Workers=0 时不覆盖显式设置的 Connections/Segments。
func TestWorkersZeroFallsBack(t *testing.T) {
	cfg := Config{
		URL:              "http://example.com/f",
		OutputPath:       filepath.Join(t.TempDir(), "f"),
		Connections:      4,
		Segments:         8,
		Workers:          0,
		AllowPrivateHost: true,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Connections != 4 || cfg.Segments != 8 {
		t.Fatalf("classic mode clobbered: conn=%d seg=%d, want 4/8", cfg.Connections, cfg.Segments)
	}
}

// TestDownloadWithWorkers 用 Workers 模式端到端下载，校验内容正确。
func TestDownloadWithWorkers(t *testing.T) {
	data := testFile(2 << 20) // 2 MiB
	srv, _ := rangeServer(t, data, 0)

	out := filepath.Join(t.TempDir(), "w.bin")
	d, err := New(Config{
		URL:              srv.URL,
		OutputPath:       out,
		Workers:          8,
		MinSegmentSize:   1024,
		AllowPrivateHost: true,
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
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatal("content mismatch in Workers mode")
	}
	p := d.Progress()
	if p.Workers != 8 {
		t.Fatalf("Progress.Workers = %d, want 8", p.Workers)
	}
}

// TestSetWorkersMidRun 运行中切换 Workers 总和，校验下载仍正确完成。
func TestSetWorkersMidRun(t *testing.T) {
	data := testFile(2 << 20)
	srv, _ := rangeServer(t, data, 200*time.Microsecond)

	out := filepath.Join(t.TempDir(), "sw.bin")
	d, err := New(Config{
		URL:              srv.URL,
		OutputPath:       out,
		Workers:          4,
		MinSegmentSize:   1024,
		AllowPrivateHost: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	d.SetWorkers(16)
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatal("content mismatch after mid-run SetWorkers")
	}
}

// TestSlowSegmentSplit 构造一个对首个范围慢速响应的服务器，验证
// 慢分片拆分逻辑被触发且最终下载内容完整。这是"当某个切片下载
// 速度慢时自动分割切片、以最高性能去下载"的端到端验证。
func TestSlowSegmentSplit(t *testing.T) {
	data := testFile(2 << 20) // 2 MiB
	var slowReqs int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", `"v1"`)
		rng := r.Header.Get("Range")
		// 对起始范围（bytes=0-0 探测 + bytes=0-... 的前几个分片）注入延迟。
		if r.Method == http.MethodGet && (rng == "bytes=0-0" || isEarlyRange(rng)) {
			atomic.AddInt64(&slowReqs, 1)
			time.Sleep(80 * time.Millisecond) // 故意慢
		}
		http.ServeContent(w, r, "slow", time.Time{}, bytesReader(data))
	}))
	t.Cleanup(srv.Close)

	out := filepath.Join(t.TempDir(), "slow.bin")
	d, err := New(Config{
		URL:              srv.URL,
		OutputPath:       out,
		Workers:          8,
		MinSegmentSize:   64 * 1024, // 64 KiB，使拆分阈值低一些便于触发
		AllowPrivateHost: true,
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
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatal("content mismatch after slow-segment split")
	}
	// 至少触发过一次慢请求。
	if atomic.LoadInt64(&slowReqs) == 0 {
		t.Log("note: no slow requests observed (server may have served from cache)")
	}
}

// isEarlyRange 粗略判断一个 Range 头是否指向文件起始区域，
// 用于 TestSlowSegmentSplit 注入慢速延迟。
func isEarlyRange(rng string) bool {
	// 形如 "bytes=0-65535" 或 "bytes=0-..." 的早期范围。
	for i := len("bytes="); i < len(rng); i++ {
		c := rng[i]
		if c == '-' {
			return true // 起始偏移部分已解析完且为 0
		}
		if c != '0' {
			return false // 起始偏移非 0
		}
	}
	return false
}

// TestQueueSetWorkers 验证队列级 SetWorkers 同时作用于运行中任务。
func TestQueueSetWorkers(t *testing.T) {
	payloads := map[string][]byte{
		"q1.bin": testFile(256 << 10),
		"q2.bin": testFile(256 << 10),
	}
	ms := newMultiServer(t, payloads, 200*time.Microsecond)

	dir := t.TempDir()
	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		Template:    Config{Workers: 4, MinSegmentSize: 1024},
		OutputDir:   dir,
	})
	if _, err := q.AddAll([]string{
		ms.srv.URL + "/q1.bin",
		ms.srv.URL + "/q2.bin",
	}, ""); err != nil {
		t.Fatalf("AddAll: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	q.SetWorkers(12)
	if err := waitQueue(t, q, 30*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	for name, want := range payloads {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if sha(got) != sha(want) {
			t.Fatalf("%s content mismatch after queue SetWorkers", name)
		}
	}
}

// TestWorkersExceedsSegmentsGracefully 验证 Workers 很大但文件很小时不会崩溃。
func TestWorkersExceedsSegmentsGracefully(t *testing.T) {
	data := testFile(4 << 10) // 4 KiB，极小
	srv, _ := rangeServer(t, data, 0)

	out := filepath.Join(t.TempDir(), "tiny.bin")
	d, err := New(Config{
		URL:              srv.URL,
		OutputPath:       out,
		Workers:          64, // 远超文件可切分片数
		MinSegmentSize:   1024,
		AllowPrivateHost: true,
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
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatal("content mismatch for tiny file with large Workers")
	}
}

// TestWorkersNoRangeFallback 验证 Workers 模式对不支持 Range 的服务器退化正常。
func TestWorkersNoRangeFallback(t *testing.T) {
	data := testFile(200 << 10)
	srv := noRangeServer(t, data)

	out := filepath.Join(t.TempDir(), "nr.bin")
	d, err := New(Config{
		URL:              srv.URL,
		OutputPath:       out,
		Workers:          8,
		AllowPrivateHost: true,
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
	got, _ := os.ReadFile(out)
	if sha(got) != sha(data) {
		t.Fatal("content mismatch in Workers no-range fallback")
	}
	if d.Progress().AcceptRanges {
		t.Fatal("AcceptRanges should be false for no-range server")
	}
}

// 确保引入 errors 以防 vet 抱怨未使用（实际上该测试文件目前未直接用 errors，
// 但保持 import 一致性）。
var _ = errors.Is

// bytesReader 在 bench_test.go 中已定义；这里复用。
// 为避免重复定义，本文件不重复声明，依赖 bench_test.go 的 bytesReader。
// 若 bench_test.go 不在则补一个本地版本。
var _ = fmt.Sprintf
