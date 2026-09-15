// Package meta 实现下载进度的磁盘附属文件读写。
//
// 附属文件使用 ".muyuan" 命名空间以避免冲突，写在最终输出旁。
// 元数据写入是原子化的（marshal → 临时文件 → rename），使进程在
// 写入中途被杀死也不会损坏记录。
package meta

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiaoLangZe/go-muyuan/zh-CN/internal/plan"
)

// metaSuffix 和 partialSuffix 是写在最终输出旁的附属文件名。
// 它们使用 ".muyuan" 命名空间以避免冲突。
const (
	PartialSuffix = ".muyuan.partial"
	MetaSuffix    = ".muyuan.meta.json"
)

// Meta 是描述下载进度的磁盘元数据。它写在 partial 文件旁，
// 在 Start 时被读取以决定是否可续传。
type Meta struct {
	// URL 是该 partial 文件的来源 URL。
	URL string `json:"url"`
	// ETag / LastModified 是探测时捕获的服务器校验器。
	// 它们的存在让我们能在远端资源变更时拒绝续传。
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	// TotalSize 是 Content-Length。服务器未上报时为 0/未知。
	TotalSize int64 `json:"total_size"`
	// AcceptRanges 报告服务器是否支持范围请求。
	AcceptRanges bool `json:"accept_ranges"`
	// Connections 是记录该进度时的连接数。
	// 仅供参考：续传不依赖它。
	Connections int `json:"connections"`
	// Segments 是每分片进度。仅在 AcceptRanges 为真时有意义。
	// 其长度即分片数。
	Segments []plan.Segment `json:"segments"`
}

// MetaPath 返回给定输出对应的元数据路径。
func MetaPath(outputPath string) string {
	return outputPath + MetaSuffix
}

// PartialPath 返回给定输出对应的 partial 文件路径。
func PartialPath(outputPath string) string {
	return outputPath + PartialSuffix
}

// Load 读取并解码元数据文件。文件不存在时返回 ok=false 且
// error 为 nil（一次全新下载）。任何其他 I/O 或解码错误都会被返回。
func Load(outputPath string) (m *Meta, ok bool, err error) {
	p := MetaPath(outputPath)
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read meta: %w", err)
	}
	var mm Meta
	if err := json.Unmarshal(data, &mm); err != nil {
		return nil, false, fmt.Errorf("decode meta: %w", err)
	}
	return &mm, true, nil
}

// Save 将元数据原子地写入磁盘：先序列化为 JSON、写入同目录临时文件、
// 再 rename 覆盖目标。这样即使进程在写入中途被杀死，磁盘状态也保持一致。
func (m *Meta) Save(outputPath string) error {
	p := MetaPath(outputPath)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".muyuan-meta-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp meta: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 若 rename 成功则为 no-op
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp meta: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 附属文件含下载数据与进度，收紧为仅属主可读写。
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp meta: %w", err)
	}
	return os.Rename(tmpName, p)
}

// DeleteAndPartial 删除两个附属文件。它是幂等的：文件不存在
// 不算错误。被 Restart / ClearCache / 完成时使用。
func DeleteAndPartial(outputPath string) error {
	errs := make([]error, 0, 2)
	if err := os.Remove(MetaPath(outputPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	if err := os.Remove(PartialPath(outputPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	if len(errs) == 1 {
		return errs[0]
	}
	if len(errs) == 2 {
		return fmt.Errorf("delete sidecars: %v; %v", errs[0], errs[1])
	}
	return nil
}
