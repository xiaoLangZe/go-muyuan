package go_muyuan

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The default host guard refuses loopback targets, which is what every test
// server is; the tests that cover downloaded data opt out of it explicitly.
func unsafeHosts() Option { return WithAllowUnsafeHosts(true) }

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	return payload
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func etagFor(payload []byte) string {
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%q", hex.EncodeToString(sum[:8]))
}

// newFileServer serves the payload at every path, with range support.
func newFileServer(t *testing.T, payload []byte, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etagFor(payload))
		http.ServeContent(w, r, name, time.Unix(0, 0), bytes.NewReader(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// gatedReader serves the payload but stops after gate bytes until the test
// releases it. A test can therefore pause a transfer at a known offset.
// Several readers may be alive at once when a download is restarted, so the
// "reached the gate" signal is a shared Once rather than one per reader.
type gatedReader struct {
	payload   []byte
	pos       int64
	gate      int64
	blocked   chan struct{}
	signalled *sync.Once
	release   chan struct{}
}

func (g *gatedReader) Read(p []byte) (int, error) {
	if g.pos >= g.gate {
		g.signalled.Do(func() { close(g.blocked) })
		<-g.release
	}
	if g.pos >= int64(len(g.payload)) {
		return 0, io.EOF
	}
	n := copy(p, g.payload[g.pos:])
	g.pos += int64(n)
	return n, nil
}

func (g *gatedReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		g.pos = offset
	case io.SeekCurrent:
		g.pos += offset
	case io.SeekEnd:
		g.pos = int64(len(g.payload)) + offset
	default:
		return 0, fmt.Errorf("unsupported whence %d", whence)
	}
	if g.pos < 0 {
		return 0, errors.New("negative seek position")
	}
	return g.pos, nil
}

// gateServer serves the payload, holding back everything past gate bytes on the
// first request. Later requests, which carry a Range header, are served in full
// from the requested offset.
type gateServer struct {
	payload []byte
	gate    int64
	blocked chan struct{}
	release chan struct{}

	blockOnce   sync.Once
	releaseOnce sync.Once

	mu       sync.Mutex
	requests int
	ranges   []string
}

func newGateServer(t *testing.T, payload []byte, gate int64) (*gateServer, *httptest.Server) {
	t.Helper()
	gs := &gateServer{
		payload: payload,
		gate:    gate,
		blocked: make(chan struct{}),
		release: make(chan struct{}),
	}
	srv := httptest.NewServer(gs)
	t.Cleanup(func() {
		gs.unblock()
		srv.Close()
	})
	return gs, srv
}

func (g *gateServer) unblock() { g.releaseOnce.Do(func() { close(g.release) }) }

func (g *gateServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.requests++
	g.ranges = append(g.ranges, r.Header.Get("Range"))
	g.mu.Unlock()

	w.Header().Set("ETag", etagFor(g.payload))
	if r.Header.Get("Range") != "" {
		http.ServeContent(w, r, "payload.bin", time.Unix(0, 0), bytes.NewReader(g.payload))
		return
	}
	http.ServeContent(w, r, "payload.bin", time.Unix(0, 0), &gatedReader{
		payload:   g.payload,
		gate:      g.gate,
		blocked:   g.blocked,
		signalled: &g.blockOnce,
		release:   g.release,
	})
}

func (g *gateServer) seenRanges() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.ranges...)
}

func TestDownloadCompletes(t *testing.T) {
	payload := randomBytes(t, 512<<10)
	srv := newFileServer(t, payload, "payload.bin")
	dir := t.TempDir()

	var (
		mu         sync.Mutex
		reports    int
		lastReport Progress
	)
	h, err := Download(context.Background(), srv.URL+"/payload.bin",
		WithDir(dir), unsafeHosts(),
		WithProgress(func(p Progress) {
			mu.Lock()
			reports++
			lastReport = p
			mu.Unlock()
		}))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := h.Status(); got != StatusCompleted {
		t.Fatalf("status = %s, want completed", got)
	}
	if want := filepath.Join(dir, "payload.bin"); h.FilePath() != want {
		t.Fatalf("file path = %q, want %q", h.FilePath(), want)
	}
	if !bytes.Equal(readFile(t, h.FilePath()), payload) {
		t.Fatal("file content does not match the payload")
	}
	if _, err := os.Stat(h.PartPath()); !os.IsNotExist(err) {
		t.Fatalf("partial file still present: %v", err)
	}
	p := h.Progress()
	if p.Downloaded != int64(len(payload)) || p.Total != int64(len(payload)) {
		t.Fatalf("progress = %d/%d, want %d", p.Downloaded, p.Total, len(payload))
	}
	if p.Percent != 100 {
		t.Fatalf("percent = %v, want 100", p.Percent)
	}
	mu.Lock()
	defer mu.Unlock()
	if reports == 0 {
		t.Fatal("progress callback never ran")
	}
	if lastReport.Percent != 100 {
		t.Fatalf("last report = %v%%, want 100%%", lastReport.Percent)
	}
}

func TestNameFromURLDropsQuery(t *testing.T) {
	payload := randomBytes(t, 4096)
	srv := newFileServer(t, payload, "data.bin")
	dir := t.TempDir()

	h, err := Download(context.Background(), srv.URL+"/files/data.bin?token=secret", WithDir(dir), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if want := filepath.Join(dir, "data.bin"); h.FilePath() != want {
		t.Fatalf("file path = %q, want %q", h.FilePath(), want)
	}
}

func TestServerSuppliedName(t *testing.T) {
	payload := randomBytes(t, 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''report%202026.bin`)
		http.ServeContent(w, r, "ignored.bin", time.Unix(0, 0), bytes.NewReader(payload))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()

	h, err := Download(context.Background(), srv.URL+"/opaque/id", WithDir(dir), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if want := filepath.Join(dir, "report 2026.bin"); h.FilePath() != want {
		t.Fatalf("file path = %q, want %q", h.FilePath(), want)
	}
}

func TestExistingFileIsKeptAndRenamed(t *testing.T) {
	payload := randomBytes(t, 4096)
	srv := newFileServer(t, payload, "payload.bin")
	dir := t.TempDir()

	existing := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(dir), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if want := filepath.Join(dir, "payload_1.bin"); h.FilePath() != want {
		t.Fatalf("file path = %q, want %q", h.FilePath(), want)
	}
	if got := string(readFile(t, existing)); got != "old" {
		t.Fatalf("existing file was modified: %q", got)
	}
	if !bytes.Equal(readFile(t, h.FilePath()), payload) {
		t.Fatal("downloaded file does not match the payload")
	}
}

func TestOverwriteReplacesExistingFile(t *testing.T) {
	payload := randomBytes(t, 4096)
	srv := newFileServer(t, payload, "payload.bin")
	dir := t.TempDir()

	existing := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(dir), unsafeHosts(), WithOverwrite(true))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if h.FilePath() != existing {
		t.Fatalf("file path = %q, want %q", h.FilePath(), existing)
	}
	if !bytes.Equal(readFile(t, existing), payload) {
		t.Fatal("file was not replaced")
	}
}

func TestPauseAndResume(t *testing.T) {
	payload := randomBytes(t, 2<<20)
	const gate = 256 << 10
	gs, srv := newGateServer(t, payload, gate)
	dir := t.TempDir()

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(dir), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	select {
	case <-gs.blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("server never reached the gate")
	}
	waitFor(t, "bytes on disk", func() bool { return h.Progress().Downloaded > 0 })

	if err := h.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if got := h.Status(); got != StatusPaused {
		t.Fatalf("status = %s, want paused", got)
	}
	held := h.Progress().Downloaded
	if held <= 0 || held >= int64(len(payload)) {
		t.Fatalf("paused after %d bytes of %d", held, len(payload))
	}
	if err := h.Pause(); err != nil {
		t.Fatalf("second pause: %v", err)
	}
	info, err := os.Stat(h.PartPath())
	if err != nil {
		t.Fatalf("stat partial file: %v", err)
	}
	if info.Size() != held {
		t.Fatalf("partial file is %d bytes, want %d", info.Size(), held)
	}

	gs.unblock()
	if err := h.Resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !bytes.Equal(readFile(t, h.FilePath()), payload) {
		t.Fatal("resumed file does not match the payload")
	}

	ranges := gs.seenRanges()
	if len(ranges) < 2 {
		t.Fatalf("expected a second request, got %d", len(ranges))
	}
	if want := fmt.Sprintf("bytes=%d-", held); ranges[1] != want {
		t.Fatalf("resume asked for %q, want %q", ranges[1], want)
	}
	p := h.Progress()
	if p.Downloaded != int64(len(payload)) || p.Percent != 100 {
		t.Fatalf("progress after resume = %d (%v%%)", p.Downloaded, p.Percent)
	}
}

func TestResumeWithoutRangeSupport(t *testing.T) {
	payload := randomBytes(t, 1<<20)
	const gate = 128 << 10

	blocked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var (
		mu     sync.Mutex
		ranges []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()

		// The server ignores Range and always answers with the whole body.
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		if r.Header.Get("Range") == "" {
			if _, err := w.Write(payload[:gate]); err != nil {
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			once.Do(func() { close(blocked) })
			<-release
			return
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(func() {
		once.Do(func() { close(blocked) })
		close(release)
		srv.Close()
	})
	dir := t.TempDir()

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(dir), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	select {
	case <-blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("server never reached the gate")
	}
	waitFor(t, "bytes on disk", func() bool { return h.Progress().Downloaded > 0 })
	if err := h.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := h.Resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !bytes.Equal(readFile(t, h.FilePath()), payload) {
		t.Fatal("file does not match the payload after restarting from zero")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ranges) < 2 || ranges[1] == "" {
		t.Fatalf("resume did not ask for a range: %v", ranges)
	}
	if h.ranges != rangeUnsupported {
		t.Fatal("the handle did not record that the server ignores ranges")
	}
}

func TestRestartStartsOver(t *testing.T) {
	payload := randomBytes(t, 2<<20)
	const gate = 128 << 10
	gs, srv := newGateServer(t, payload, gate)
	dir := t.TempDir()

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(dir), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	select {
	case <-gs.blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("server never reached the gate")
	}
	waitFor(t, "bytes on disk", func() bool { return h.Progress().Downloaded > 0 })
	gs.unblock()

	if err := h.Restart(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := h.Status(); got != StatusCompleted {
		t.Fatalf("status = %s, want completed", got)
	}
	if !bytes.Equal(readFile(t, h.FilePath()), payload) {
		t.Fatal("restarted file does not match the payload")
	}
	// A restart downloads from zero, so the second request carries no range.
	ranges := gs.seenRanges()
	if len(ranges) < 2 || ranges[1] != "" {
		t.Fatalf("restart kept a range request: %v", ranges)
	}
}

func TestDeleteRemovesFiles(t *testing.T) {
	payload := randomBytes(t, 2<<20)
	const gate = 128 << 10
	gs, srv := newGateServer(t, payload, gate)
	dir := t.TempDir()

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(dir), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	select {
	case <-gs.blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("server never reached the gate")
	}
	waitFor(t, "bytes on disk", func() bool { return h.Progress().Downloaded > 0 })

	part := h.PartPath()
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("expected a partial file: %v", err)
	}
	if err := h.Delete(); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := h.Status(); got != StatusDeleted {
		t.Fatalf("status = %s, want deleted", got)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait after delete: %v", err)
	}
	for _, path := range []string{part, h.FilePath()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", path, err)
		}
	}
	if err := h.Delete(); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if err := h.Resume(); !errors.Is(err, ErrNotPaused) {
		t.Fatalf("resume after delete = %v, want ErrNotPaused", err)
	}
}

func TestRetriesTransientStatus(t *testing.T) {
	payload := randomBytes(t, 64<<10)
	var (
		mu       sync.Mutex
		failures = 2
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fail := failures > 0
		if fail {
			failures--
		}
		mu.Unlock()
		if fail {
			http.Error(w, "try again", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, "payload.bin", time.Unix(0, 0), bytes.NewReader(payload))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(dir), unsafeHosts(),
		WithRetries(3), WithRetryDelay(10*time.Millisecond))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !bytes.Equal(readFile(t, h.FilePath()), payload) {
		t.Fatal("file does not match the payload")
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	h, err := Download(context.Background(), srv.URL+"/missing.bin",
		WithDir(t.TempDir()), unsafeHosts(), WithRetries(3), WithRetryDelay(10*time.Millisecond))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	waitErr := h.Wait()
	var statusErr *HTTPStatusError
	if !errors.As(waitErr, &statusErr) || statusErr.StatusCode != http.StatusNotFound {
		t.Fatalf("wait error = %v, want a 404 HTTPStatusError", waitErr)
	}
	if got := h.Status(); got != StatusFailed {
		t.Fatalf("status = %s, want failed", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("server saw %d requests, want 1", requests)
	}
}

func TestStalledTransferFails(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("start")); err != nil {
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release
	}))
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		srv.Close()
	})

	h, err := Download(context.Background(), srv.URL+"/payload.bin",
		WithDir(t.TempDir()), unsafeHosts(),
		WithStallTimeout(200*time.Millisecond), WithRetries(0))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	waitErr := h.Wait()
	if !errors.Is(waitErr, ErrStalled) {
		t.Fatalf("wait error = %v, want ErrStalled", waitErr)
	}
	if got := h.Status(); got != StatusFailed {
		t.Fatalf("status = %s, want failed", got)
	}
}

func TestContextCancelFailsDownload(t *testing.T) {
	payload := randomBytes(t, 2<<20)
	gs, srv := newGateServer(t, payload, 128<<10)
	ctx, cancel := context.WithCancel(context.Background())

	h, err := Download(ctx, srv.URL+"/payload.bin", WithDir(t.TempDir()), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	select {
	case <-gs.blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("server never reached the gate")
	}
	waitFor(t, "bytes on disk", func() bool { return h.Progress().Downloaded > 0 })

	cancel()
	waitErr := h.Wait()
	if !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("wait error = %v, want context.Canceled", waitErr)
	}
	if got := h.Status(); got != StatusFailed {
		t.Fatalf("status = %s, want failed", got)
	}
}

func TestConcurrentPauseAndResume(t *testing.T) {
	payload := randomBytes(t, 2<<20)
	gs, srv := newGateServer(t, payload, 64<<10)
	dir := t.TempDir()

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(dir), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	select {
	case <-gs.blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("server never reached the gate")
	}
	waitFor(t, "bytes on disk", func() bool { return h.Progress().Downloaded > 0 })

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = h.Pause()
				return
			}
			_ = h.Resume()
		}(i)
	}
	wg.Wait()

	gs.unblock()
	// A resume issued while the hammering ran may already have finished the
	// transfer, so a completed handle is a valid outcome here.
	if err := h.Resume(); err != nil && !errors.Is(err, ErrCompleted) {
		t.Fatalf("resume: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := h.Status(); got != StatusCompleted {
		t.Fatalf("status = %s, want completed", got)
	}
	if !bytes.Equal(readFile(t, h.FilePath()), payload) {
		t.Fatal("file does not match the payload")
	}
}

// TestControlOperationsStress mixes every control operation on one handle. It
// exists to catch the two failure modes that a lock held across a wait invites:
// a deadlock, and a worker of a finished run writing state that belongs to the
// run that replaced it.
func TestControlOperationsStress(t *testing.T) {
	payload := randomBytes(t, 1<<20)
	gs, srv := newGateServer(t, payload, 32<<10)
	dir := t.TempDir()

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(dir), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 12; j++ {
				switch (i + j) % 5 {
				case 0:
					_ = h.Pause()
				case 1:
					_ = h.Resume()
				case 2:
					_ = h.Start()
				case 3:
					_ = h.Progress()
					_ = h.Info()
					_ = h.Status()
				case 4:
					_ = h.Restart()
				}
			}
		}(i)
	}

	hammered := make(chan struct{})
	go func() {
		wg.Wait()
		close(hammered)
	}()
	select {
	case <-hammered:
	case <-time.After(60 * time.Second):
		t.Fatal("control operations deadlocked")
	}

	gs.unblock()
	if err := h.Resume(); err != nil && !errors.Is(err, ErrCompleted) {
		t.Fatalf("resume after the hammering: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := h.Status(); got != StatusCompleted {
		t.Fatalf("status = %s, want completed", got)
	}
	if !bytes.Equal(readFile(t, h.FilePath()), payload) {
		t.Fatal("file does not match the payload")
	}
}

func TestHostGuardRejectsInternalTargets(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:8080/file",
		"http://127.1.2.3/file",
		"http://0.0.0.0/file",
		"http://10.0.0.5/file",
		"http://172.16.3.4/file",
		"http://192.168.1.10/file",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.1.1/file",
		"http://[::1]/file",
		"http://[fe80::1]/file",
		"http://[fd00::1]/file",
		"http://localhost/file",
	} {
		_, err := Download(context.Background(), raw, WithDir(t.TempDir()))
		if !errors.Is(err, ErrBlockedHost) {
			t.Errorf("Download(%q) error = %v, want ErrBlockedHost", raw, err)
		}
	}
}

func TestInvalidTargets(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"not a url",
		"/relative/path",
		"example.com/file.bin",
		"ftp://example.com/file.bin",
		"file:///etc/passwd",
		"https://",
		"https://user:secret@example.com/file.bin",
	} {
		_, err := New(context.Background(), raw)
		if !errors.Is(err, ErrInvalidURL) {
			t.Errorf("New(%q) error = %v, want ErrInvalidURL", raw, err)
		}
	}
}

func TestUnsafeHostsAllowsLoopback(t *testing.T) {
	payload := randomBytes(t, 4096)
	srv := newFileServer(t, payload, "payload.bin")

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(t.TempDir()), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

func TestDownloadTo(t *testing.T) {
	payload := randomBytes(t, 4096)
	srv := newFileServer(t, payload, "payload.bin")
	target := filepath.Join(t.TempDir(), "nested", "renamed.bin")

	h, err := DownloadTo(context.Background(), srv.URL+"/payload.bin", target, unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if h.FilePath() != target {
		t.Fatalf("file path = %q, want %q", h.FilePath(), target)
	}
	if !bytes.Equal(readFile(t, target), payload) {
		t.Fatal("file does not match the payload")
	}
}

func TestStartOnCompletedDownload(t *testing.T) {
	payload := randomBytes(t, 4096)
	srv := newFileServer(t, payload, "payload.bin")

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(t.TempDir()), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := h.Start(); !errors.Is(err, ErrCompleted) {
		t.Fatalf("start on a finished download = %v, want ErrCompleted", err)
	}
	if err := h.Pause(); !errors.Is(err, ErrNotDownloading) {
		t.Fatalf("pause on a finished download = %v, want ErrNotDownloading", err)
	}
	if err := h.Restart(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	// The restart is a new transfer, so Wait has to block for it rather than
	// return on the channel the first, already finished transfer closed.
	if err := h.Wait(); err != nil {
		t.Fatalf("wait after restart: %v", err)
	}
	if got := h.Status(); got != StatusCompleted {
		t.Fatalf("status after restart = %s, want completed", got)
	}
	if !bytes.Equal(readFile(t, h.FilePath()), payload) {
		t.Fatal("file does not match the payload after the restart")
	}
}

func TestWaitContextTimesOut(t *testing.T) {
	payload := randomBytes(t, 2<<20)
	gs, srv := newGateServer(t, payload, 64<<10)

	h, err := Download(context.Background(), srv.URL+"/payload.bin", WithDir(t.TempDir()), unsafeHosts())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	select {
	case <-gs.blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("server never reached the gate")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := h.WaitContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitContext = %v, want context.DeadlineExceeded", err)
	}
	gs.unblock()
	if err := h.Resume(); err != nil && !errors.Is(err, ErrNotPaused) {
		t.Fatalf("resume: %v", err)
	}
	_ = h.Delete()
}

func TestSanitizeFileName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"report.pdf", "report.pdf"},
		{"../../etc/passwd", "passwd"},
		{`..\..\windows\system32\cmd.exe`, "cmd.exe"},
		{"/absolute/path/file.bin", "file.bin"},
		{"CON.txt", "_CON.txt"},
		{"nul", "_nul"},
		{"trailing. ", "trailing"},
		{`a<b>c:d"e|f?g*h.bin`, "a_b_c_d_e_f_g_h.bin"},
		{"", ""},
		{"   ", ""},
		{"..", ""},
		{"\x00bad.bin", "_bad.bin"},
	}
	for _, tc := range cases {
		if got := sanitizeFileName(tc.in); got != tc.want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	long := strings.Repeat("中", 300) + ".bin"
	got := sanitizeFileName(long)
	if len(got) > maxNameLen {
		t.Errorf("sanitizeFileName kept %d bytes, want at most %d", len(got), maxNameLen)
	}
	if !strings.HasSuffix(got, ".bin") {
		t.Errorf("sanitizeFileName lost the extension: %q", got)
	}
}

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		in      string
		start   int64
		total   int64
		wantErr bool
	}{
		{"bytes 200-999/1000", 200, 1000, false},
		{"bytes 0-0/1", 0, 1, false},
		{"bytes */1000", 0, 1000, false},
		{"bytes 5-99/*", 5, 0, false},
		{"items 1-2/3", 0, 0, true},
		{"bytes 1-2", 0, 0, true},
		{"", 0, 0, true},
		{"bytes */", 0, 0, true},
	}
	for _, tc := range cases {
		start, total, err := parseContentRange(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseContentRange(%q) did not fail", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseContentRange(%q) failed: %v", tc.in, err)
			continue
		}
		if start != tc.start || total != tc.total {
			t.Errorf("parseContentRange(%q) = (%d, %d), want (%d, %d)", tc.in, start, total, tc.start, tc.total)
		}
	}
}
