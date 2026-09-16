// Command batch 演示木鸢（go-muyuan）任务队列：以有界并发下载多个文件，
// 并提供实时多行状态显示，运行期间接受 stdin 命令控制队列。
//
// 用法：
//
//	go run ./examples/batch -o ./downloads -c 2 -conn 8 -seg 16 url1 url2 url3 ...
//
// 批量运行期间，键入命令并回车：
//
//	p            暂停整个队列
//	r            恢复队列
//	c <n>        设置并发（同时下载文件数）
//	conn <n>     设置每文件连接数
//	seg <n>      设置每文件分片数（0 = 自动）
//	b <c> <s>    同时设置两个每文件旋钮
//	pause <id>   暂停一个任务
//	resume <id>  恢复一个任务
//	restart <id> 重启一个任务（重新下载）
//	cancel <id>  取消一个任务
//	q            退出（中止）
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	downloader "github.com/xiaoLangZe/go-muyuan"
)

func main() {
	var (
		out         = flag.String("o", "./downloads", "output directory")
		concurrency = flag.Int("c", 3, "files to download simultaneously")
		workers     = flag.Int("workers", 0, "per-file (connections+segments) budget; 0 = use -conn/-seg separately")
		connections = flag.Int("conn", 8, "concurrent connections per file (ignored when -workers>0)")
		segments    = flag.Int("seg", 0, "byte-range segments per file (0 = auto; ignored when -workers>0)")
		retries     = flag.Int("retries", 2, "retries per failed task")
		allow       = flag.Bool("allow-private", false, "allow private/loopback hosts")
		proxy       = flag.String("proxy", "", "proxy URL (http/https/socks5/socks5h)")
		headers     = flag.String("headers", "", `extra request headers, comma-separated "K: V" pairs`)
		maxRate     = flag.Int64("max-rate", 0, "per-file total download rate cap in bytes/sec (0 = unlimited)")
		sha256sum   = flag.String("sha256", "", "expected SHA-256 for every file (64 hex chars); empty = skip")
	)
	flag.Parse()

	urls := flag.Args()
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "usage: batch [-o dir] [-c n] [-workers n | -conn n -seg n] [-retries n] [-proxy u] [-headers k:v] [-max-rate n] [-sha256 sum] <url> [url...]")
		os.Exit(2)
	}

	hdr, err := parseHeaders(*headers)
	if err != nil {
		fmt.Fprintln(os.Stderr, "headers error:", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	q, err := downloader.NewQueue(downloader.QueueConfig{
		Concurrency: *concurrency,
		OutputDir:   *out,
		MaxRetries:  *retries,
		Template: downloader.Config{
			Workers:          *workers,
			Connections:      *connections,
			Segments:         *segments,
			AllowPrivateHost: *allow,
			Proxy:            *proxy,
			Headers:          hdr,
			MaxBytesPerSec:   *maxRate,
			VerifySHA256:     *sha256sum,
		},
		OnTaskStateChange: func(ti downloader.TaskInfo) {
			// 在活动区块上方打印状态变更，随后让它重绘。
			lock.Lock()
			fmt.Printf("\r%s\n", fmt.Sprintf("%-9s %s -> %s", ti.ID, fileName(ti), ti.State))
			lock.Unlock()
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "queue config error:", err)
		os.Exit(1)
	}

	ids, err := q.AddAll(urls, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "added %d task(s) before error: %v\n", len(ids), err)
		os.Exit(1)
	}
	if *workers > 0 {
		fmt.Printf("queued %d task(s), concurrency=%d workers=%d (auto-allocates connections/segments)\n",
			len(ids), *concurrency, *workers)
	} else {
		fmt.Printf("queued %d task(s), concurrency=%d connections=%d segments=%s\n",
			len(ids), *concurrency, *connections, segmentLabel(*segments))
	}

	if err := q.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "start error:", err)
		os.Exit(1)
	}

	go renderLoop(ctx, q)
	go commandLoop(ctx, q, ids)

	err = q.Wait()
	fmt.Println()
	if err != nil {
		fmt.Fprintln(os.Stderr, "finished with errors:", err)
		printSummary(q.Summary())
		os.Exit(1)
	}
	printSummary(q.Summary())
	fmt.Println("all downloads complete in", *out)
}

// lock 在 render 循环与回调之间串行化控制台写入。
var lock sync.Mutex

func fileName(ti downloader.TaskInfo) string {
	if ti.OutputPath == "" {
		return ti.URL
	}
	parts := strings.FieldsFunc(ti.OutputPath, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return ti.OutputPath
	}
	return parts[len(parts)-1]
}

// renderLoop 重绘每任务状态区块，直到 ctx 被取消。
func renderLoop(ctx context.Context, q *downloader.Queue) {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	lines := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lock.Lock()
			if lines > 0 {
				fmt.Printf("\x1b[%dA", lines) // 光标上移覆盖旧区块
			}
			tasks := q.Tasks()
			sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
			for _, ti := range tasks {
				fmt.Printf("\r\x1b[K%-9s %-8s %s\n", ti.ID, ti.State, describe(ti))
			}
			s := q.Summary()
			fmt.Printf("\r\x1b[K%s\n", summaryLine(s))
			lines = len(tasks) + 1
			lock.Unlock()
		}
	}
}

func describe(ti downloader.TaskInfo) string {
	p := ti.Progress
	if p.Total > 0 {
		return fmt.Sprintf("%-28s %5.1f%%  %s/%s  %s/s",
			truncate(fileName(ti), 28), p.Percent, human(p.Downloaded), human(p.Total), human(int64(p.Speed)))
	}
	if p.Downloaded > 0 {
		return fmt.Sprintf("%-28s %s downloaded", truncate(fileName(ti), 28), human(p.Downloaded))
	}
	return truncate(fileName(ti), 28)
}

func summaryLine(s downloader.Summary) string {
	return fmt.Sprintf("total=%d running=%d pending=%d paused=%d done=%d failed=%d canceled=%d  %s/%s",
		s.Total, s.Running, s.Pending, s.Paused, s.Completed, s.Failed, s.Canceled,
		human(s.DownloadedBytes), human(s.TotalBytes))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func printSummary(s downloader.Summary) {
	fmt.Printf("completed=%d failed=%d canceled=%d paused=%d, %s of %s\n",
		s.Completed, s.Failed, s.Canceled, s.Paused,
		human(s.DownloadedBytes), human(s.TotalBytes))
}

func segmentLabel(n int) string {
	if n == 0 {
		return "auto"
	}
	return strconv.Itoa(n)
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), units[exp])
}

// commandLoop 从 stdin 读取命令并应用到队列。
func commandLoop(ctx context.Context, q *downloader.Queue, ids []string) {
	fmt.Println("commands: p | r | c <n> | workers <n> | conn <n> | seg <n> | b <conn> <seg> | pause|resume|restart|cancel <id> | q")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		fields := strings.Fields(strings.ToLower(sc.Text()))
		if len(fields) == 0 {
			continue
		}
		var err error
		switch fields[0] {
		case "p":
			q.Pause()
		case "r":
			q.Resume()
		case "c":
			if n, e := atoiArg(fields, 1); e == nil {
				q.SetConcurrency(n)
			} else {
				err = e
			}
		case "workers":
			if n, e := atoiArg(fields, 1); e == nil {
				q.SetWorkers(n)
			} else {
				err = e
			}
		case "conn":
			if n, e := atoiArg(fields, 1); e == nil {
				q.SetConnections(n)
			} else {
				err = e
			}
		case "seg":
			if n, e := atoiArg(fields, 1); e == nil {
				q.SetSegments(n)
			} else {
				err = e
			}
		case "b":
			if len(fields) >= 3 {
				c, e1 := strconv.Atoi(fields[1])
				s, e2 := strconv.Atoi(fields[2])
				if e1 == nil && e2 == nil {
					q.SetConnectionsAndSegments(c, s)
				} else {
					err = fmt.Errorf("b needs two integers")
				}
			}
		case "pause", "resume", "restart", "cancel":
			if len(fields) < 2 {
				err = fmt.Errorf("%s needs a task id", fields[0])
				break
			}
			id := resolveID(fields[1], ids)
			switch fields[0] {
			case "pause":
				err = q.PauseTask(id)
			case "resume":
				err = q.ResumeTask(id)
			case "restart":
				err = q.RestartTask(id)
			case "cancel":
				err = q.CancelTask(id)
			}
		case "q":
			q.Abort()
			return
		default:
			err = fmt.Errorf("unknown command %q", fields[0])
		}
		if err != nil {
			lock.Lock()
			fmt.Fprintf(os.Stderr, "\rerror: %v\n", err)
			lock.Unlock()
		}
	}
}

// resolveID 接受完整 task id 或纯序号（1 起）。
func resolveID(arg string, ids []string) string {
	if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= len(ids) {
		return ids[n-1]
	}
	return arg
}

func atoiArg(fields []string, i int) (int, error) {
	if len(fields) <= i {
		return 0, fmt.Errorf("missing argument")
	}
	return strconv.Atoi(fields[i])
}

// parseHeaders 解析逗号分隔的 "K: V" 请求头对。
func parseHeaders(raw string) (http.Header, error) {
	h := make(http.Header)
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	for _, pair := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(pair, ":")
		if !ok {
			return nil, fmt.Errorf("header %q not in \"K: V\" form", pair)
		}
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, fmt.Errorf("empty header name in %q", pair)
		}
		h.Add(k, strings.TrimSpace(v))
	}
	return h, nil
}
