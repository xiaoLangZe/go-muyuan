package downloader

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// verifySHA256 流式计算 path 的 SHA-256 并与期望的 64 位十六进制
// 值比较。返回是否匹配与实际计算值；读取失败时返回错误。
func verifySHA256(path, want string) (matched bool, got string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, "", err
	}
	got = hex.EncodeToString(h.Sum(nil))
	wantHex, err := hex.DecodeString(want)
	if err != nil {
		return false, got, fmt.Errorf("decode expected checksum: %w", err)
	}
	return hex.EncodeToString(wantHex) == got, got, nil
}
