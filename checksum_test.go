package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestChecksumVerifySuccess 校验和匹配时下载成功。
func TestChecksumVerifySuccess(t *testing.T) {
	payload := testFile(128 << 10)
	srv, _ := rangeServer(t, payload, 0)

	out := t.TempDir() + "/f.bin"
	d := newTestDownloader(t, srv.URL, out, 2, 4)
	d.cfg.VerifySHA256 = sha256Hex(payload)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got := readFileBytes(t, out)
	if sha(got) != sha(payload) {
		t.Fatal("content mismatch")
	}
}

// TestChecksumVerifyMismatch 校验和不匹配时产物被删除、Wait 返回
// ErrChecksumMismatch。
func TestChecksumVerifyMismatch(t *testing.T) {
	payload := testFile(64 << 10)
	srv, _ := rangeServer(t, payload, 0)

	out := t.TempDir() + "/f.bin"
	d := newTestDownloader(t, srv.URL, out, 2, 4)
	// 故意取错误摘要：用全零而不是真实内容。
	d.cfg.VerifySHA256 = sha256Hex(make([]byte, 32))
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err := d.Wait()
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Wait = %v, want ErrChecksumMismatch", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("output not removed after checksum mismatch")
	}
}

// TestChecksumQueueIntegration 队列模板中的校验和在任务失败时可见。
func TestChecksumQueueIntegration(t *testing.T) {
	payload := testFile(64 << 10)
	ms := newMultiServer(t, map[string][]byte{"q.bin": payload}, 0)
	q := newTestQueue(t, QueueConfig{
		Concurrency: 1,
		OutputDir:   t.TempDir(),
		Template:    Config{VerifySHA256: sha256Hex(make([]byte, 32))},
	})
	id, err := q.Add(ms.srv.URL+"/q.bin", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := waitQueue(t, q, 20*time.Second); err != nil {
		if !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("Wait = %v, want ErrChecksumMismatch", err)
		}
	}
	info, ok := q.Task(id)
	if !ok || info.State != TaskFailed {
		t.Fatalf("task = %+v, want failed", info)
	}
	if !errors.Is(info.Err, ErrChecksumMismatch) {
		t.Fatalf("task err = %v, want ErrChecksumMismatch", info.Err)
	}
}

// TestValidateSHA256Field 非法校验和格式在 Validate 报错。
func TestValidateSHA256Field(t *testing.T) {
	c := privateCfg(t, "http://127.0.0.1/x", t.TempDir()+"/f.bin")
	for _, bad := range []string{"xyz", "abcd1234", "a1"} {
		c.VerifySHA256 = bad
		if err := c.Validate(); err == nil {
			t.Errorf("VerifySHA256=%q accepted, want error", bad)
		}
	}
	c.VerifySHA256 = strings.Repeat("0", 64)
	if err := c.Validate(); err != nil {
		t.Errorf("all-zero hex rejected: %v", err)
	}
	c.VerifySHA256 = sha256Hex([]byte("x"))
	if err := c.Validate(); err != nil {
		t.Errorf("valid sha256 rejected: %v", err)
	}
}
