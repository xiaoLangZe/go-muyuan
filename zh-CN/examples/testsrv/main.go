// Command testsrv 通过 HTTP 从一个目录提供文件，支持 Range。
// 它的存在是为了让下载器能针对一个本地、支持 range 的服务器
// 端到端试用（Go 标准库的 FileServer/ServeContent 处理
// Range 和 Accept-Ranges）。
//
// 用法：
//
//	go run ./examples/testsrv -dir ./testdata -addr 127.0.0.1:18080
//
// 然后在另一个终端：
//
//	go run ./examples/basic -allow-private -o ./download.bin \
//	    http://127.0.0.1:18080/somefile.bin
//
// 服务器只会读取解析后位于 -dir 之内的文件：请求路径先被清理，
// 再在打开任何文件前重新校验包含关系。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	dir := flag.String("dir", ".", "directory to serve")
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	flag.Parse()

	root, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("resolve dir: %v", err)
	}
	// 解析符号链接，使包含关系检查比较的是规范路径。
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		log.Fatalf("dir %q is not a readable directory", *dir)
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		full, ok := resolveInside(root, r.URL.Path)
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		f, err := os.Open(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, st.Name(), st.ModTime(), f)
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	fmt.Printf("serving %s on http://%s/\n", root, *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// resolveInside 将 URL 路径映射为保证位于 root 之内的文件系统路径，
// 拒绝任何逃逸（包括编码的 ".."）。
func resolveInside(root, urlPath string) (string, bool) {
	// 以 / 开头做 Clean，使 "../../etc/passwd" 坍缩为 "/etc/passwd"，
	// 再 join 到 root 下并验证结果仍在其中。
	clean := filepath.Clean("/" + urlPath)
	full := filepath.Join(root, filepath.FromSlash(clean))

	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}
