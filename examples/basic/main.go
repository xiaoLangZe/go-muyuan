// Command basic downloads one file and follows it through the event stream.
//
//	go run ./examples/basic <url>
//
// The file lands in ./downloads. The download uses up to eight connections;
// the server decides whether that is possible, and a server that does not serve
// range requests is fetched over one connection instead.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiaoLangZe/go-muyuan"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "用法: basic <url>")
		os.Exit(2)
	}

	m := muyuan.New(
		muyuan.WithThreads(8),  // connections available to the tasks
		muyuan.WithMaxFiles(1), // one file at a time
		muyuan.WithDefaultDir("downloads"),
	)
	defer m.Close()

	task, err := m.AddTask("downloads", os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "加入任务失败:", err)
		os.Exit(1)
	}

	// Events carry progress, every state change, and the error a task ended
	// with. The channel closes once the task is over.
	for ev := range task.Events() {
		switch ev.Type {
		case muyuan.EventProgress:
			p := ev.Progress
			fmt.Printf("\r%6.2f%%  %s / %s  %s/s  %d 连接  切片 %s",
				p.Percent, human(p.Downloaded), human(p.Total),
				human(p.Speed), p.Workers, human(p.BlockSize))
		case muyuan.EventStatus:
			fmt.Printf("\n状态 → %s\n", ev.Status)
		case muyuan.EventError:
			fmt.Fprintf(os.Stderr, "失败: %v\n", ev.Err)
		}
	}

	if err := task.Wait(); err != nil {
		os.Exit(1)
	}
	if abs, err := filepath.Abs(task.FilePath()); err == nil {
		fmt.Println("已保存到", abs)
	}
}

// human formats a byte count for a terminal one line wide.
func human(n int64) string {
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
