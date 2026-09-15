package meta

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xiaoLangZe/go-muyuan/zh-CN/internal/plan"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	mm := &Meta{
		URL:          "http://example.com/file.bin",
		ETag:         `"abc"`,
		TotalSize:    12345,
		AcceptRanges: true,
		Connections:  2,
		Segments:     plan.BuildPlan(12345, 4),
	}
	if err := mm.Save(out); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := Load(out)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	// Meta.Segments 携带 plan，因此其长度即分片数。
	if got.TotalSize != mm.TotalSize || len(got.Segments) != 4 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// 保存的 meta 文件权限应仅属主可读写。
	// 注意：Windows 不支持 Unix 权限模型，os.Chmod 仅能切只读位，
	// 因此在该平台跳过断言。
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(MetaPath(out)); err == nil {
			if mode := info.Mode().Perm(); mode != 0o600 {
				t.Fatalf("meta file mode = %o, want 0600", mode)
			}
		}
	}
	if err := DeleteAndPartial(out); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := Load(out); ok {
		t.Fatal("meta still present after delete")
	}
}

func TestDeleteIdempotent(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "nope.bin")
	if err := DeleteAndPartial(out); err != nil {
		t.Fatalf("delete on absent files should be nil, got: %v", err)
	}
	// 直接断言：两个附属文件都不存在。
	if _, err := os.Stat(MetaPath(out)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("meta file should not exist")
	}
	if _, err := os.Stat(PartialPath(out)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial file should not exist")
	}
}
