package downloader

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// bytesReader 是 http.ServeContent 所需的 io.ReadSeeker 的薄封装。
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// BenchmarkDownloadSingleConn 基线：单连接顺序下载。
func BenchmarkDownloadSingleConn(b *testing.B) {
	data := testFile(4 << 20) // 4 MiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "bench", time.Time{}, bytesReader(data))
	}))
	defer srv.Close()
	for i := 0; i < b.N; i++ {
		runOneDownload(b, srv.URL, 1, 1)
	}
}

// BenchmarkDownloadMultiConn 多连接 + 多切片下载。
func BenchmarkDownloadMultiConn(b *testing.B) {
	data := testFile(4 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "bench", time.Time{}, bytesReader(data))
	}))
	defer srv.Close()
	for i := 0; i < b.N; i++ {
		runOneDownload(b, srv.URL, 8, 16)
	}
}

// BenchmarkDownloadWorkers Workers 总和模式下载。
func BenchmarkDownloadWorkers(b *testing.B) {
	data := testFile(4 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "bench", time.Time{}, bytesReader(data))
	}))
	defer srv.Close()
	for i := 0; i < b.N; i++ {
		d, err := New(Config{
			URL:              srv.URL,
			OutputPath:       tempOut(b, "w"),
			Workers:          16,
			MinSegmentSize:   1024,
			AllowPrivateHost: true,
		})
		if err != nil {
			b.Fatalf("New: %v", err)
		}
		if err := d.Start(context.Background()); err != nil {
			b.Fatalf("Start: %v", err)
		}
		if err := d.Wait(); err != nil {
			b.Fatalf("Wait: %v", err)
		}
	}
}

// BenchmarkDownloadWorkersSlowSegment Workers 模式 + 慢分片拆分。
// 服务器对第 0 字节起的范围故意慢速响应，触发拆分逻辑。
func BenchmarkDownloadWorkersSlowSegment(b *testing.B) {
	data := testFile(4 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		// 对起始范围慢速、其余范围正常，制造慢分片。
		if r.Header.Get("Range") == "bytes=0-0" {
			time.Sleep(50 * time.Millisecond)
		}
		http.ServeContent(w, r, "bench", time.Time{}, bytesReader(data))
	}))
	defer srv.Close()
	for i := 0; i < b.N; i++ {
		d, err := New(Config{
			URL:              srv.URL,
			OutputPath:       tempOut(b, "slow"),
			Workers:          16,
			MinSegmentSize:   1024,
			AllowPrivateHost: true,
		})
		if err != nil {
			b.Fatalf("New: %v", err)
		}
		if err := d.Start(context.Background()); err != nil {
			b.Fatalf("Start: %v", err)
		}
		if err := d.Wait(); err != nil {
			b.Fatalf("Wait: %v", err)
		}
	}
}

func runOneDownload(tb testing.TB, url string, conn, seg int) {
	tb.Helper()
	d, err := New(Config{
		URL:              url,
		OutputPath:       tempOut(tb, fmt.Sprintf("d-%d-%d", conn, seg)),
		Connections:      conn,
		Segments:         seg,
		MinSegmentSize:   1024,
		AllowPrivateHost: true,
	})
	if err != nil {
		tb.Fatalf("New: %v", err)
	}
	if err := d.Start(context.Background()); err != nil {
		tb.Fatalf("Start: %v", err)
	}
	if err := d.Wait(); err != nil {
		tb.Fatalf("Wait: %v", err)
	}
}

func tempOut(tb testing.TB, name string) string {
	tb.Helper()
	dir := tb.TempDir()
	return dir + "/" + name + ".bin"
}
