package transfer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// memFile 是 WriterAt 的内存实现：一个按偏移稀疏存储的 map，
// 可注入短写以模拟磁盘写不满的场景。
type memFile struct {
	data  map[int64]byte
	fails bool // 为 true 时 WriteAt 返回部分写入或错误
}

func newMemFile() *memFile { return &memFile{data: make(map[int64]byte)} }

func (f *memFile) WriteAt(p []byte, off int64) (int, error) {
	if f.fails {
		// 只写一半，模拟磁盘空间不足。内容不重要，测试只看
		// 返回值触发的短写检查；data 可能为 nil，跳过落盘。
		if f.data != nil {
			for i := 0; i < len(p)/2; i++ {
				f.data[off+int64(i)] = p[i]
			}
		}
		return len(p) / 2, nil
	}
	for i, b := range p {
		f.data[off+int64(i)] = b
	}
	return len(p), nil
}

func (f *memFile) bytes(n int64) []byte {
	out := make([]byte, n)
	var i int64
	for ; i < n; i++ {
		out[i] = f.data[i]
	}
	return out
}

// failWriter 任何写入都报错。
type failWriter struct{}

func (failWriter) WriteAt(p []byte, off int64) (int, error) {
	return 0, errors.New("disk full")
}

// rangeServer 返回一个支持 Range 的 httptest 服务器。
func rangeServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("ETag", `"abc"`)
			w.Header().Set("Last-Modified", "Wed, 21 Oct 2026 07:28:00 GMT")
			http.ServeContent(w, r, "f.bin", time.Now(), strings.NewReader(string(payload)))
			return
		}
		// 简化处理：仅支持单段 Range "bytes=start-end"。
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || end < start || start >= int64(len(payload)) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(payload)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if end >= int64(len(payload)) {
			end = int64(len(payload)) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestProbeHappyPath：HEAD 与 Range GET 都成功，全部字段就位。
func TestProbeHappyPath(t *testing.T) {
	payload := make([]byte, 3000)
	srv := rangeServer(t, payload)

	res, err := Probe(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.AcceptRanges {
		t.Error("AcceptRanges = false, want true")
	}
	if res.Total != int64(len(payload)) {
		t.Errorf("Total = %d, want %d", res.Total, len(payload))
	}
	if res.ETag != `"abc"` {
		t.Errorf("ETag = %q", res.ETag)
	}
	if res.LastModified == "" {
		t.Error("LastModified empty")
	}
}

// TestProbeNoRangeFallback：服务器忽略 Range，返回 200。
func TestProbeNoRangeFallback(t *testing.T) {
	payload := []byte("hello world, no ranges here")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	res, err := Probe(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.AcceptRanges {
		t.Error("AcceptRanges = true, want false")
	}
	if res.Total != int64(len(payload)) {
		t.Errorf("Total = %d, want %d", res.Total, len(payload))
	}
}

// TestProbeHeadErrorIgnored：HEAD 失败但 GET 探测成功。
func TestProbeHeadErrorIgnored(t *testing.T) {
	payload := make([]byte, 100)
	var headCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headCalls++
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		http.ServeContent(w, r, "f.bin", time.Now(), strings.NewReader(string(payload)))
	}))
	t.Cleanup(srv.Close)

	res, err := Probe(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if headCalls != 1 {
		t.Errorf("HEAD calls = %d, want 1", headCalls)
	}
	if !res.AcceptRanges {
		t.Errorf("AcceptRanges = false, want true")
	}
	if res.Total != int64(len(payload)) {
		t.Errorf("Total = %d, want %d", res.Total, len(payload))
	}
}

// TestProbeGetErrorUsesHead：GET 失败但 HEAD 已有足够信息时视为成功。
func TestProbeGetErrorUsesHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", "42")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	res, err := Probe(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.AcceptRanges || res.Total != 42 {
		t.Errorf("unexpected: %+v", res)
	}
}

// TestProbeUnknownTotal：206 但 Content-Range 为 "*"，Total 保持 0。
func TestProbeUnknownTotal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Range", "bytes 0-0/*")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("x"))
	}))
	t.Cleanup(srv.Close)

	res, err := Probe(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.AcceptRanges {
		t.Error("AcceptRanges = false, want true")
	}
	if res.Total != 0 {
		t.Errorf("Total = %d, want 0", res.Total)
	}
}

// TestProbeContextCancelled：已取消的 ctx 立即失败。
func TestProbeContextCancelled(t *testing.T) {
	srv := rangeServer(t, []byte("payload"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Probe(ctx, srv.Client(), srv.URL, nil); err == nil {
		t.Fatal("Probe with cancelled ctx = nil error")
	}
}

// TestProbeCopiesHeadersAndUA：自定义头被复制、默认 UA 被填写。
func TestProbeCopiesHeadersAndUA(t *testing.T) {
	var gotUA, gotX string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotX = r.Header.Get("X-Custom")
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-0/1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("x"))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	t.Cleanup(srv.Close)

	hdr := http.Header{"X-Custom": []string{"yes"}}
	if _, err := Probe(context.Background(), srv.Client(), srv.URL, hdr); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if gotX != "yes" {
		t.Errorf("X-Custom = %q, want yes", gotX)
	}
	if gotUA != DefaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, DefaultUserAgent)
	}
}

// TestParseContentRangeTotal：合法/非法形式。
func TestParseContentRangeTotal(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"bytes 0-0/12345", 12345, true},
		{"bytes 0-9/10", 10, true},
		{"bytes 0-0/*", 0, false},
		{"bytes 0-0", 0, false},
		{"garbage", 0, false},
		{"bytes 0-0/-5", 0, false},
		{"bytes 0-0/", 0, false},
	}
	for _, c := range cases {
		got, ok := parseContentRangeTotal(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseContentRangeTotal(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestStatusErrorMatrix：永久/可重试分类。
func TestStatusErrorMatrix(t *testing.T) {
	permanent := []int{400, 403, 404, 410, 451}
	retryable := []int{408, 429, 500, 502, 503, 200, 300}
	for _, code := range permanent {
		if !IsPermanent(StatusError("op", code)) {
			t.Errorf("StatusError(%d) not permanent", code)
		}
	}
	for _, code := range retryable {
		if IsPermanent(StatusError("op", code)) {
			t.Errorf("StatusError(%d) unexpectedly permanent", code)
		}
	}
	if IsPermanent(fmt.Errorf("wrapped: %w", StatusError("op", 404))) == false {
		t.Error("IsPermanent through wrap = false")
	}
	if IsPermanent(nil) {
		t.Error("IsPermanent(nil) = true")
	}
}

// TestDownloadSegmentContent：把 [start,end) 精确写到目标偏移。
func TestDownloadSegmentContent(t *testing.T) {
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	srv := rangeServer(t, payload)

	f := newMemFile()
	var total int64
	if err := DownloadSegment(context.Background(), srv.Client(), srv.URL, nil, 100, 900, f, func(n int64) { total += n }); err != nil {
		t.Fatalf("DownloadSegment: %v", err)
	}
	if total != 800 {
		t.Errorf("onBytes total = %d, want 800", total)
	}
	got := f.bytes(4096)
	for i := 100; i < 900; i++ {
		if got[i] != payload[i] {
			t.Fatalf("byte at %d = %d, want %d", i, got[i], payload[i])
		}
	}
	for i := 0; i < 100; i++ {
		if _, ok := f.data[int64(i)]; ok {
			t.Fatalf("byte at %d outside range written", i)
		}
	}
}

// TestDownloadSegmentEmptyRange：end<=start 短路且不发请求。
func TestDownloadSegmentEmptyRange(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	t.Cleanup(srv.Close)

	if err := DownloadSegment(context.Background(), srv.Client(), srv.URL, nil, 10, 10, newMemFile(), nil); err != nil {
		t.Fatalf("empty range: %v", err)
	}
	if hits != 0 {
		t.Errorf("server hit %d times, want 0", hits)
	}
}

// TestDownloadSegmentNon206：非 206 响应返回永久错误。
func TestDownloadSegmentNon206(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	err := DownloadSegment(context.Background(), srv.Client(), srv.URL, nil, 0, 10, newMemFile(), nil)
	if !IsPermanent(err) {
		t.Fatalf("err = %v, want permanent", err)
	}
}

// TestDownloadSegmentShortWrite：short write 必须报错而不是静默丢字节。
func TestDownloadSegmentShortWrite(t *testing.T) {
	payload := make([]byte, 2048)
	srv := rangeServer(t, payload)

	err := DownloadSegment(context.Background(), srv.Client(), srv.URL, nil, 0, int64(len(payload)), &memFile{fails: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "short write") {
		t.Fatalf("err = %v, want short write error", err)
	}
}

// TestDownloadSegmentWriterError：写入错误直接透传。
func TestDownloadSegmentWriterError(t *testing.T) {
	payload := make([]byte, 100)
	srv := rangeServer(t, payload)

	err := DownloadSegment(context.Background(), srv.Client(), srv.URL, nil, 0, 100, failWriter{}, nil)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want writer error", err)
	}
}

// TestDownloadSegmentContextCancel：取消时返回 ctx 错误。
func TestDownloadSegmentContextCancel(t *testing.T) {
	payload := make([]byte, 1<<20)
	srv := rangeServer(t, payload)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := DownloadSegment(ctx, srv.Client(), srv.URL, nil, 0, int64(len(payload)), newMemFile(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestDownloadSequentialContent：整包顺序写入。
func TestDownloadSequentialContent(t *testing.T) {
	payload := []byte("sequential stream payload, no ranges at all")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	f := newMemFile()
	var total int64
	if err := DownloadSequential(context.Background(), srv.Client(), srv.URL, nil, f, func(n int64) { total += n }); err != nil {
		t.Fatalf("DownloadSequential: %v", err)
	}
	if total != int64(len(payload)) {
		t.Errorf("onBytes total = %d, want %d", total, len(payload))
	}
	if got := string(f.bytes(int64(len(payload)))); got != string(payload) {
		t.Fatalf("content = %q, want %q", got, payload)
	}
}

// TestDownloadSequentialNon200：非 200 返回永久错误。
func TestDownloadSequentialNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	err := DownloadSequential(context.Background(), srv.Client(), srv.URL, nil, newMemFile(), nil)
	if !IsPermanent(err) {
		t.Fatalf("err = %v, want permanent", err)
	}
}

// TestDownloadSequentialShortWrite：顺序路径同样必须检测短写
// （回归测试：此前该路径缺失检查会静默丢字节）。
func TestDownloadSequentialShortWrite(t *testing.T) {
	payload := make([]byte, 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	err := DownloadSequential(context.Background(), srv.Client(), srv.URL, nil, &memFile{fails: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "short write") {
		t.Fatalf("err = %v, want short write error", err)
	}
}

// TestRetrySegmentSucceedsAfterTransient：两次瞬时失败后第三次成功。
func TestRetrySegmentSucceedsAfterTransient(t *testing.T) {
	old := retryBackoffBase
	retryBackoffBase = 5 * time.Millisecond
	t.Cleanup(func() { retryBackoffBase = old })

	var attempts int
	err := RetrySegment(context.Background(), func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RetrySegment: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

// TestRetrySegmentPermanentNoRetry：永久错误只尝试一次。
func TestRetrySegmentPermanentNoRetry(t *testing.T) {
	old := retryBackoffBase
	retryBackoffBase = 5 * time.Millisecond
	t.Cleanup(func() { retryBackoffBase = old })

	var attempts int
	perm := StatusError("op", 404)
	err := RetrySegment(context.Background(), func(ctx context.Context) error {
		attempts++
		return perm
	})
	if !errors.Is(err, perm) {
		t.Fatalf("err = %v, want %v", err, perm)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

// TestRetrySegmentExhaustion：耗尽 4 次后返回最后一次错误。
func TestRetrySegmentExhaustion(t *testing.T) {
	old := retryBackoffBase
	retryBackoffBase = time.Millisecond
	t.Cleanup(func() { retryBackoffBase = old })

	var attempts int
	last := errors.New("still failing")
	err := RetrySegment(context.Background(), func(ctx context.Context) error {
		attempts++
		return last
	})
	if err == nil || err.Error() != last.Error() {
		t.Fatalf("err = %v, want last error", err)
	}
	if attempts != 4 {
		t.Errorf("attempts = %d, want 4", attempts)
	}
}

// TestRetrySegmentCancelDuringBackoff：退避等待期间取消立即返回。
func TestRetrySegmentCancelDuringBackoff(t *testing.T) {
	old := retryBackoffBase
	retryBackoffBase = time.Hour
	t.Cleanup(func() { retryBackoffBase = old })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := RetrySegment(ctx, func(ctx context.Context) error {
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Error("cancellation did not interrupt backoff promptly")
	}
}

// TestRetrySegmentCancelNoWait：已取消 ctx 不会等待退避。
func TestRetrySegmentCancelNoWait(t *testing.T) {
	old := retryBackoffBase
	retryBackoffBase = time.Hour
	t.Cleanup(func() { retryBackoffBase = old })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := RetrySegment(ctx, func(ctx context.Context) error {
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("pre-cancelled ctx waited")
	}
}

// TestCopyHeadersSkipsRangeAndHost：Range 与 Host 永不被外部头覆盖。
func TestCopyHeadersSkipsRangeAndHost(t *testing.T) {
	src := http.Header{
		"Range":      []string{"bytes=0-999"},
		"Host":       []string{"evil.example"},
		"X-Keep":     []string{"a", "b"},
		"User-Agent": []string{"custom"},
	}
	dst := http.Header{}
	copyHeaders(dst, src)
	if got := dst.Get("Range"); got != "" {
		t.Errorf("Range copied: %q", got)
	}
	if got := dst.Get("Host"); got != "" {
		t.Errorf("Host copied: %q", got)
	}
	if got := dst.Values("X-Keep"); len(got) != 2 {
		t.Errorf("X-Keep = %v, want 2 values", got)
	}
}

// TestCopyHeadersNilSource：nil 源为 no-op。
func TestCopyHeadersNilSource(t *testing.T) {
	dst := http.Header{}
	copyHeaders(dst, nil)
	if len(dst) != 0 {
		t.Errorf("nil src copied: %v", dst)
	}
}

// TestSetDefaultUserAgent：已有 UA 则保留，否则填默认。
func TestSetDefaultUserAgent(t *testing.T) {
	h := http.Header{}
	setDefaultUserAgent(h)
	if h.Get("User-Agent") != DefaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", h.Get("User-Agent"), DefaultUserAgent)
	}
	h.Set("User-Agent", "mine/1.0")
	setDefaultUserAgent(h)
	if h.Get("User-Agent") != "mine/1.0" {
		t.Errorf("User-Agent = %q, want guard to keep custom value", h.Get("User-Agent"))
	}
}

// TestDownloadSegmentOnBytes：逐块正确累计写入字节数。
func TestDownloadSegmentOnBytes(t *testing.T) {
	payload := make([]byte, 3*(1<<15)) // 三个 32 KiB 缓冲块
	for i := range payload {
		payload[i] = 0xAB
	}
	srv := rangeServer(t, payload)

	f := newMemFile()
	var total, calls int64
	if err := DownloadSegment(context.Background(), srv.Client(), srv.URL, nil, 0, int64(len(payload)), f, func(n int64) {
		total += n
		calls++
	}); err != nil {
		t.Fatalf("DownloadSegment: %v", err)
	}
	if total != int64(len(payload)) {
		t.Errorf("onBytes total = %d, want %d", total, len(payload))
	}
	if calls != 3 {
		t.Errorf("onBytes calls = %d, want 3", calls)
	}
}
