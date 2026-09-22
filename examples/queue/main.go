// Command queue downloads several files over one shared connection budget.
//
//	go run ./examples/queue <url> [url...]
//
// All tasks draw on the same pool of connections. The scheduler spreads them
// breadth first — every file that fits gets one connection before any file gets
// a second — so one large file cannot starve the others. The program prints a
// line per task once a second until everything has finished, and exits non-zero
// if any task failed.
//
// Each task is a handle: Pause, Resume, Restart and Delete are available on it
// at any point, and Progress, Status and Info can be read whenever you like.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaoLangZe/go-muyuan"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: queue <url> [url...]")
		os.Exit(2)
	}

	m := muyuan.New(
		muyuan.WithThreads(6),  // connections shared by every task
		muyuan.WithMaxFiles(2), // files downloading at the same time
		muyuan.WithDefaultDir("downloads"),
	)
	defer m.Close()

	tasks := make([]*muyuan.Task, 0, len(os.Args)-1)
	for _, rawURL := range os.Args[1:] {
		task, err := m.AddTask("", rawURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "跳过 %s: %v\n", rawURL, err)
			continue
		}
		tasks = append(tasks, task)
	}
	if len(tasks) == 0 {
		fmt.Fprintln(os.Stderr, "没有可用任务")
		os.Exit(1)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		allOver := true
		for _, task := range tasks {
			p := task.Progress()
			name := filepath.Base(task.FilePath())
			if len(name) > 32 {
				name = name[:29] + "..."
			}
			fmt.Printf("%-32s %6.2f%%  %9s / %-9s  %4d 连接  %s\n",
				name, p.Percent, size(p.Downloaded), size(p.Total), p.Workers, task.Status())
			if !over(task.Status()) {
				allOver = false
			}
		}
		fmt.Println(strings.Repeat("-", 92))
		if allOver {
			break
		}
	}

	failed := 0
	for _, task := range tasks {
		if err := task.Wait(); err != nil {
			fmt.Fprintf(os.Stderr, "失败 %s: %v\n", task.URL(), err)
			failed++
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
	fmt.Printf("%d 个文件全部完成\n", len(tasks))
}

// over reports whether a status is one a task never leaves.
func over(status muyuan.Status) bool {
	switch status {
	case muyuan.StatusCompleted, muyuan.StatusFailed, muyuan.StatusDeleted:
		return true
	}
	return false
}

// size formats a byte count for a fixed-width column.
func size(n int64) string {
	if n <= 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}
