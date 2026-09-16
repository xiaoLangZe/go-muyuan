package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// demoServe 在 httptest 服务器上提供一个支持 Range 的固定载荷，
// 并返回服务器与载荷。示例必须离线、快速且输出确定。
func demoServe(payload string) (*httptest.Server, string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "demo.bin", time.Time{}, strings.NewReader(payload))
	}))
	return srv, payload
}

// ExampleNew 演示一次最小单文件下载的完整生命周期。
func ExampleNew() {
	srv, payload := demoServe("hello, go-muyuan")
	defer srv.Close()
	_ = payload // 输出断言里通过读取文件校验同一载荷

	dir, err := os.MkdirTemp("", "muyuan-example-")
	if err != nil {
		fmt.Println("mkdirtemp:", err)
		return
	}
	defer os.RemoveAll(dir)

	d, err := New(Config{
		URL:              srv.URL,
		OutputPath:       filepath.Join(dir, "demo.bin"),
		Connections:      4,
		Segments:         2,
		MinSegmentSize:   1,
		AllowPrivateHost: true, // 示例服务器监听回环地址
	})
	if err != nil {
		fmt.Println("new:", err)
		return
	}
	if err := d.Start(context.Background()); err != nil {
		fmt.Println("start:", err)
		return
	}
	if err := d.Wait(); err != nil {
		fmt.Println("wait:", err)
		return
	}
	got, _ := os.ReadFile(d.Progress().OutputPath)
	fmt.Printf("state=%s bytes=%q\n", d.Progress().State, got)
	// Output:
	// state=completed bytes="hello, go-muyuan"
}

// ExampleNew_maxBytesPerSec 演示限速下载：即使源很快，
// 完成时间也受速率上限约束。
func ExampleNew_maxBytesPerSec() {
	srv, payload := demoServe("rate limited payload")
	defer srv.Close()
	_ = payload // 输出断言里通过读取文件校验同一载荷

	dir, err := os.MkdirTemp("", "muyuan-example-")
	if err != nil {
		fmt.Println("mkdirtemp:", err)
		return
	}
	defer os.RemoveAll(dir)

	d, err := New(Config{
		URL:              srv.URL,
		OutputPath:       filepath.Join(dir, "demo.bin"),
		Connections:      2,
		Segments:         2,
		MinSegmentSize:   1,
		MaxBytesPerSec:   1 << 20, // 载荷远小于 1 MiB/s：仅验证配置生效
		AllowPrivateHost: true,
	})
	if err != nil {
		fmt.Println("new:", err)
		return
	}
	if err := d.Start(context.Background()); err != nil {
		fmt.Println("start:", err)
		return
	}
	if err := d.Wait(); err != nil {
		fmt.Println("wait:", err)
		return
	}
	got, _ := os.ReadFile(d.Progress().OutputPath)
	fmt.Printf("state=%s bytes=%q\n", d.Progress().State, got)
	// Output:
	// state=completed bytes="rate limited payload"
}

// ExampleNew_verifySha256 演示下载完成后按期望摘要校验内容。
func ExampleNew_verifySha256() {
	srv, payload := demoServe("integrity checked")
	defer srv.Close()

	sum := sha256.Sum256([]byte(payload))
	dir, err := os.MkdirTemp("", "muyuan-example-")
	if err != nil {
		fmt.Println("mkdirtemp:", err)
		return
	}
	defer os.RemoveAll(dir)

	d, err := New(Config{
		URL:              srv.URL,
		OutputPath:       filepath.Join(dir, "demo.bin"),
		Connections:      2,
		Segments:         2,
		MinSegmentSize:   1,
		VerifySHA256:     hex.EncodeToString(sum[:]),
		AllowPrivateHost: true,
	})
	if err != nil {
		fmt.Println("new:", err)
		return
	}
	if err := d.Start(context.Background()); err != nil {
		fmt.Println("start:", err)
		return
	}
	if err := d.Wait(); err != nil {
		fmt.Println("wait:", err)
		return
	}
	_, statErr := os.Stat(d.Progress().OutputPath)
	fmt.Printf("state=%s output=%t\n", d.Progress().State, statErr == nil)
	// Output:
	// state=completed output=true
}

// ExampleNew_verifySha256Mismatch 演示摘要不匹配时产物被删除，
// Wait 返回 ErrChecksumMismatch。
func ExampleNew_verifySha256Mismatch() {
	srv, _ := demoServe("tampered in transit?")
	defer srv.Close()

	dir, err := os.MkdirTemp("", "muyuan-example-")
	if err != nil {
		fmt.Println("mkdirtemp:", err)
		return
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "demo.bin")

	d, err := New(Config{
		URL:              srv.URL,
		OutputPath:       out,
		Connections:      2,
		Segments:         2,
		MinSegmentSize:   1,
		VerifySHA256:     "0000000000000000000000000000000000000000000000000000000000000000",
		AllowPrivateHost: true,
	})
	if err != nil {
		fmt.Println("new:", err)
		return
	}
	if err := d.Start(context.Background()); err != nil {
		fmt.Println("start:", err)
		return
	}
	err = d.Wait()
	_, statErr := os.Stat(out)
	fmt.Printf("mismatch=%t output-removed=%t\n",
		isChecksumMismatch(err), os.IsNotExist(statErr))
	// Output:
	// mismatch=true output-removed=true
}

func isChecksumMismatch(err error) bool {
	return err != nil && strings.Contains(err.Error(), "checksum mismatch")
}
