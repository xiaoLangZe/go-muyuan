// Command basic 演示 go-muyuan 下载器包：多连接下载、实时进度显示，
// 以及一个交互式命令提示，用于发送控制信号
// （暂停、恢复、重配、重启、清空缓存）。
//
// 用法：
//
//	go run ./examples/basic -o ./file.zip -conn 8 -seg 16 https://example.com/file.zip
//
// 下载运行期间，键入命令并回车：
//
//	p            暂停
//	r            恢复
//	conn <n>     设置连接数（该文件的并发连接数）
//	seg <n>      设置分片数（0 = 自动）
//	b <c> <s>    同时设置连接数和分片数
//	restart      清空进度并从头开始
//	clear        清空缓存（删除 partial + 元数据）
//	q            退出（中止）
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	downloader "github.com/xiaoLangZe/go-muyuan/zh-CN"
)

func main() {
	var (
		out         = flag.String("o", "", "output file path (required)")
		workers     = flag.Int("workers", 0, "total budget of (connections+segments); 0 = use -conn/-seg separately")
		connections = flag.Int("conn", 8, "concurrent connections fetching this file (ignored when -workers>0)")
		segments    = flag.Int("seg", 0, "byte-range segments to split the file into (0 = auto; ignored when -workers>0)")
		allow       = flag.Bool("allow-private", false, "allow private/loopback hosts (LAN downloads)")
	)
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: basic [-o out] [-workers n | -conn n -seg n] <url>")
		os.Exit(2)
	}
	url := flag.Arg(0)
	if *out == "" {
		*out = "downloaded.bin"
	}

	// ctx 在 Ctrl-C 时被取消，从而中止下载。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := downloader.New(downloader.Config{
		URL:              url,
		OutputPath:       *out,
		Workers:          *workers,
		Connections:      *connections,
		Segments:         *segments,
		AllowPrivateHost: *allow,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	if err := d.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "start error:", err)
		os.Exit(1)
	}

	// 进度显示。
	go renderLoop(ctx, d)

	// 交互式命令提示。
	go commandLoop(ctx, d)

	if err := d.Wait(); err != nil {
		fmt.Println()
		fmt.Fprintln(os.Stderr, "download failed:", err)
		os.Exit(1)
	}
	fmt.Println()
	fmt.Println("done:", d.Progress().OutputPath)
}

// renderLoop 逐行重绘进度条，直到 ctx 被取消。
func renderLoop(ctx context.Context, d *downloader.Downloader) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastLen int
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p := d.Progress()
			if p.State.IsTerminal() {
				return
			}
			line := formatProgress(p)
			// 用空格填充，使较短的行也能清掉上一行。
			pad := ""
			if n := lastLen - len(line); n > 0 {
				pad = strings.Repeat(" ", n)
			}
			lastLen = len(line)
			fmt.Printf("\r%s%s", line, pad)
		}
	}
}

func formatProgress(p downloader.Progress) string {
	bar := progressBar(p.Percent, 30)
	mode := fmt.Sprintf("conn=%d seg=%d", p.Connections, p.Segments)
	if !p.AcceptRanges {
		mode = "single-stream (no range support)"
	}
	return fmt.Sprintf("[%s] %5.1f%%  %s/%s  %s/s  eta %-6s  %-8s %s",
		bar, p.Percent, human(p.Downloaded), human(p.Total),
		human(int64(p.Speed)), shortDur(p.ETA), mode, p.State)
}

func progressBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	return strings.Repeat("=", filled) + strings.Repeat("-", width-filled)
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

func shortDur(d time.Duration) string {
	if d <= 0 {
		return "--"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// commandLoop 从 stdin 读取命令并推送控制信号。
func commandLoop(ctx context.Context, d *downloader.Downloader) {
	fmt.Println("commands: p | r | workers <n> | conn <n> | seg <n> | b <conn> <seg> | restart | clear | q")
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
		switch fields[0] {
		case "p":
			d.Pause()
		case "r":
			d.Resume()
		case "workers":
			if n, err := atoiArg(fields, 1); err == nil {
				d.SetWorkers(n)
			}
		case "conn":
			if n, err := atoiArg(fields, 1); err == nil {
				d.SetConnections(n)
			}
		case "seg":
			if n, err := atoiArg(fields, 1); err == nil {
				d.SetSegments(n)
			}
		case "b":
			if len(fields) >= 3 {
				c, e1 := strconv.Atoi(fields[1])
				s, e2 := strconv.Atoi(fields[2])
				if e1 == nil && e2 == nil {
					d.SetConnectionsAndSegments(c, s)
				}
			}
		case "restart":
			d.Restart()
		case "clear":
			if err := d.ClearCache(); err != nil {
				fmt.Fprintln(os.Stderr, "clear:", err)
			}
		case "q":
			d.Abort()
			return
		default:
			fmt.Fprintln(os.Stderr, "unknown command:", fields[0])
		}
	}
}

func atoiArg(fields []string, i int) (int, error) {
	if len(fields) <= i {
		return 0, fmt.Errorf("missing argument")
	}
	return strconv.Atoi(fields[i])
}
