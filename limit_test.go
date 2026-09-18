package downloader

import (
	"context"
	"os"
	"testing"
	"time"
)

// memWriter 是根包测试用的内存 WriterAt。
type memWriter struct{ buf map[int64]byte }

func newMemWriter() *memWriter { return &memWriter{buf: make(map[int64]byte)} }

func (w *memWriter) WriteAt(p []byte, off int64) (int, error) {
	for i, b := range p {
		w.buf[off+int64(i)] = b
	}
	return len(p), nil
}

func (w *memWriter) bytesAt(off int64, n int) []byte {
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = w.buf[off+int64(i)]
	}
	return out
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// TestNewThrottleWriterZeroRate 零或负速率返回 nil（直通）。
func TestNewThrottleWriterZeroRate(t *testing.T) {
	f := newMemWriter()
	if got := newThrottleWriter(f, 0, nil); got != nil {
		t.Fatal("rate 0 should yield nil throttle")
	}
	if got := newThrottleWriter(f, -1, nil); got != nil {
		t.Fatal("negative rate should yield nil throttle")
	}
}

// TestThrottleWriterBurst 1 秒突发配额内写入不等待。
func TestThrottleWriterBurst(t *testing.T) {
	f := newMemWriter()
	tw := newThrottleWriter(f, 1<<20, nil) // 1 MiB/s，桶容量 1 MiB
	start := time.Now()
	if _, err := tw.WriteAt(make([]byte, 64<<10), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("burst write took %v, want immediate", elapsed)
	}
}

// TestThrottleWriterWaits 超出配额的写入必须等待。
func TestThrottleWriterWaits(t *testing.T) {
	const rate int64 = 128 << 10 // 128 KiB/s
	f := newMemWriter()
	tw := newThrottleWriter(f, rate, nil)

	// 先耗尽 1 秒突发。
	if _, err := tw.WriteAt(make([]byte, rate), 0); err != nil {
		t.Fatalf("burst WriteAt: %v", err)
	}
	// 再写 128 KiB：按速率需约 1s。
	start := time.Now()
	if _, err := tw.WriteAt(make([]byte, rate), rate); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 700*time.Millisecond {
		t.Fatalf("oversized write took %v, want ≥ ~1s throttled", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("oversized write took %v, throttling too slow", elapsed)
	}
}

// TestThrottleWriterCancelInterruptsWait 极低速率下，关闭 done 通道
// 必须在约一个切片时长内打断等待（回归：此前单次长睡可把 Abort/
// Close 卡在小时级）。
func TestThrottleWriterCancelInterruptsWait(t *testing.T) {
	const rate int64 = 1 // 1 B/s：任意写入都需要极长等待
	f := newMemWriter()
	done := make(chan struct{})
	tw := newThrottleWriter(f, rate, done)

	// 先耗尽 1 字节的突发配额。
	if _, err := tw.WriteAt([]byte{1}, 0); err != nil {
		t.Fatalf("burst WriteAt: %v", err)
	}

	go func() {
		time.Sleep(120 * time.Millisecond)
		close(done)
	}()

	start := time.Now()
	// 10 字节 @ 1 B/s 理论上要等 10 秒；取消应在 1s 内打断。
	if _, err := tw.WriteAt(make([]byte, 10), 1); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancel did not interrupt the wait (took %v)", elapsed)
	}
}

// TestThrottleWriterPassesThrough 字节原样透传到底层（含偏移）。
func TestThrottleWriterPassesThrough(t *testing.T) {
	f := newMemWriter()
	tw := newThrottleWriter(f, 1<<20, nil)
	payload := []byte("rate limited bytes")
	if _, err := tw.WriteAt(payload, 7); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	got := f.bytesAt(7, len(payload))
	if string(got) != string(payload) {
		t.Fatalf("bytes = %q, want %q", got, payload)
	}
}

// TestRateLimitedDownload 端到端：限速下载内容完整且确实被限速。
func TestRateLimitedDownload(t *testing.T) {
	payload := testFile(256 << 10)
	srv, _ := rangeServer(t, payload, 0)

	d := newTestDownloader(t, srv.URL, t.TempDir()+"/f.bin", 4, 8)
	d.cfg.MaxBytesPerSec = 128 << 10 // 128 KiB/s → 理论 2s
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	start := time.Now()
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	elapsed := time.Since(start)
	// 1 秒突发（128 KiB）+ 剩余 128 KiB @ 128 KiB/s ≈ 1s。
	if elapsed < 700*time.Millisecond {
		t.Errorf("rate-limited download took only %v, throttle not applied", elapsed)
	}
	got := readFileBytes(t, d.Progress().OutputPath)
	if sha(got) != sha(payload) {
		t.Fatal("content mismatch after rate-limited download")
	}
}

// TestConfigRejectsNegativeRate 负速率在 Validate 阶段被拒。
func TestConfigRejectsNegativeRate(t *testing.T) {
	c := privateCfg(t, "http://127.0.0.1/x", t.TempDir()+"/f.bin")
	c.MaxBytesPerSec = -1
	if err := c.Validate(); err == nil {
		t.Fatal("negative MaxBytesPerSec accepted")
	}
}
