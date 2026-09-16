// Command resume 演示跨进程续传：同一条命令分两次运行。
//
// 第一次：用 -stop-after 在下载中途停止，进度留在磁盘。
//
//	go run ./examples/resume -allow-private -o ./data.bin \
//	    -stop-after 3s http://127.0.0.1:18080/file.bin
//
// 第二次：不带 -stop-after，从断点继续直到完成。
//
//	go run ./examples/resume -allow-private -o ./data.bin \
//	    http://127.0.0.1:18080/file.bin
//
// 两次之间无需任何额外操作：partial 文件与元数据就写在输出旁边。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	downloader "github.com/xiaoLangZe/go-muyuan"
)

func main() {
	var (
		out       = flag.String("o", "data.bin", "output file path")
		conn      = flag.Int("conn", 8, "concurrent connections")
		seg       = flag.Int("seg", 0, "segments (0 = auto)")
		stopAfter = flag.Duration("stop-after", 0, "stop the download after this duration, keeping progress (0 = run to completion)")
		maxRate   = flag.Int64("max-rate", 0, "total download rate cap in bytes/sec (0 = unlimited)")
		allow     = flag.Bool("allow-private", false, "allow private/loopback hosts")
	)
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: resume [-o out] [-conn n] [-seg n] [-stop-after d] [-max-rate n] <url>")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := downloader.New(downloader.Config{
		URL:              flag.Arg(0),
		OutputPath:       *out,
		Connections:      *conn,
		Segments:         *seg,
		AllowPrivateHost: *allow,
		MaxBytesPerSec:   *maxRate,
		OnProgress: func(p downloader.Progress) {
			fmt.Printf("\r%.1f%%  %d/%d bytes  %s", p.Percent, p.Downloaded, p.Total, p.State)
		},
	})
	if err != nil {
		log.Fatal("config:", err)
	}

	if err := d.Start(ctx); err != nil {
		log.Fatal("start:", err)
	}

	if *stopAfter > 0 {
		time.Sleep(*stopAfter)
		d.Stop()
		fmt.Println()
		fmt.Printf("按计划停止，进度已保留（%.1f%%）。", d.Progress().Percent)
		fmt.Println("重新运行本命令（不带 -stop-after）即可续传。")
		_ = d.Wait()
		return
	}

	if err := d.Wait(); err != nil {
		fmt.Println()
		log.Fatal("download:", err)
	}
	fmt.Println()
	fmt.Println("完成:", d.Progress().OutputPath)
}
