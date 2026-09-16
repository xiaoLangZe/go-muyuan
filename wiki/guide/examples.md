# 示例

本页的示例都是**完整可运行的程序**（含 `package main` 与全部 import），已对照发布版本 v0.2.0 编译验证。直接复制到文件里即可 `go run`。

## 单文件下载

```go
package main

import (
	"context"
	"fmt"
	"log"

	downloader "github.com/xiaoLangZe/go-muyuan"
)

func main() {
	d, err := downloader.New(downloader.Config{
		URL:         "https://example.com/big.iso",
		OutputPath:  "./big.iso",
		Connections: 8,  // 该文件的并发连接数
		Segments:    16, // 字节区间切片数（0 = 自动）
		OnProgress: func(p downloader.Progress) {
			fmt.Printf("\r%.1f%%  %.1f MiB/s", p.Percent, p.Speed/1024/1024)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	if err := d.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n完成:", d.Progress().OutputPath)
}
```

## 批量下载（任务队列）

`Queue` 以有界并发下载多个文件。注意 `Concurrency`（同时几个文件）与每个文件的 `Connections`/`Segments` 是**两个独立维度**。

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	downloader "github.com/xiaoLangZe/go-muyuan"
)

func main() {
	urls := []string{
		"https://example.com/a.iso",
		"https://example.com/b.iso",
		"https://example.com/c.iso",
	}

	q, err := downloader.NewQueue(downloader.QueueConfig{
		Concurrency: 3, // 同时下载的文件数
		OutputDir:   "./downloads",
		MaxRetries:  2,               // 每个任务的重试次数
		RetryDelay:  2 * time.Second, // 重试前等待
		Template: downloader.Config{
			Workers: 16, // 每个文件的（连接 + 切片）总和预算
		},
		OnTaskStateChange: func(t downloader.TaskInfo) {
			fmt.Printf("[%s] %s\n", t.ID, t.State)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer q.Close()

	// 路径留空 => 由 URL 推导文件名并存入 OutputDir
	ids, err := q.AddAll(urls, "")
	if err != nil {
		log.Fatal(err)
	}

	if err := q.Start(context.Background()); err != nil {
		log.Fatal(err)
	}

	// 运行中同样可以加任务、改并发、挂起单个任务
	if _, err := q.Add("https://example.com/late.iso", ""); err != nil {
		log.Printf("追加任务失败: %v", err)
	}
	q.SetConcurrency(5)
	if len(ids) > 0 {
		_ = q.PauseTask(ids[0]) // 粘性暂停：队列 Resume 不会唤醒它
	}

	// Wait 返回所有失败任务的聚合错误
	if err := q.Wait(); err != nil {
		var te *downloader.TaskError
		if errors.As(err, &te) {
			log.Printf("任务失败: %s (%s): %v", te.ID, te.URL, te.Err)
		} else {
			log.Printf("队列错误: %v", err)
		}
	}

	s := q.Summary()
	fmt.Printf("共 %d 个：完成 %d，失败 %d，取消 %d，暂停 %d，已下载 %d 字节\n",
		s.Total, s.Completed, s.Failed, s.Canceled, s.Paused, s.DownloadedBytes)
}
```

### 这段示例体现的几点

| 代码 | 含义 |
| --- | --- |
| `Concurrency: 3` | 同时下载 3 个文件；与每文件 8 连接合起来最多 24 条连接 |
| `Template.Workers: 16` | 每个文件用总和模式自动分配连接与切片 |
| `AddAll(urls, "")` | 路径留空则由 URL 推导文件名，写入 `OutputDir` 并自动去重 |
| `q.Add(...)` 在 `Start` 之后 | 任务可在队列运行期间追加，有空槽即调度 |
| `q.PauseTask(ids[0])` | 单任务暂停是**粘性**的，队列级 `Resume` 不会唤醒它 |
| `errors.As(err, &te)` | `Wait` 返回 `errors.Join` 的 `*TaskError`，逐个取出失败原因 |
| `q.Summary()` | 聚合计数与字节数，用于收尾统计 |

如果不需要错误归因，`q.Wait()` 返回非 nil 时直接 `log.Fatal` 也可以——只要有一个任务失败它就非 nil，部分失败不会被静默吞掉。

## 端到端试跑

用仓库自带的本地文件服务器起一个支持 Range 的源，无需外网：

```bash
# 终端 1：创建测试文件并本地起服务
mkdir -p testdata && dd if=/dev/zero of=testdata/file.bin bs=1M count=50
go run ./examples/testsrv -dir ./testdata -addr 127.0.0.1:18080

# 终端 2：从它下载（回环地址需要 allow-private）
go run ./examples/basic -allow-private -o ./file.bin -conn 8 -seg 16 \
    http://127.0.0.1:18080/file.bin

# 或者一次批量下载多个文件
go run ./examples/batch -allow-private -o ./downloads -c 3 -conn 8 \
    http://127.0.0.1:18080/a.bin http://127.0.0.1:18080/b.bin
```

## 更多用法：限速、校验和与跨进程续传

```bash
# 限速下载（该文件全部连接合计 256 KiB/s）
go run ./examples/basic -allow-private -max-rate 262144 -o ./slow.bin \
    http://127.0.0.1:18080/file.bin

# 带校验和下载（不匹配时产物会被删除并以错误退出）
sha256sum testdata/file.bin   # 先取得期望摘要
go run ./examples/basic -allow-private -sha256 <64位hex> -o ./v.bin \
    http://127.0.0.1:18080/file.bin

# 跨进程续传：第一条命令下载 3 秒后停止，第二条继续到完成
go run ./examples/resume -allow-private -o ./r.bin -stop-after 3s \
    http://127.0.0.1:18080/file.bin
go run ./examples/resume -allow-private -o ./r.bin \
    http://127.0.0.1:18080/file.bin
```

## 仓库中的交互式示例

上面两段是为文档准备的最小程序；仓库里的示例是带实时状态面板的交互式 CLI，运行中可从 stdin 输入命令：

- [`examples/basic`](https://github.com/xiaoLangZe/go-muyuan/tree/main/examples/basic) —— 单文件下载，实时进度条，命令：`p`（暂停）/`r`（继续）/`conn <n>`/`seg <n>`/`workers <n>`/`restart`/`clear`/`q`；另有 `-proxy`、`-headers`、`-max-rate`、`-sha256` 参数。
- [`examples/batch`](https://github.com/xiaoLangZe/go-muyuan/tree/main/examples/batch) —— 任务队列批量下载，多行状态面板，另有 `pause`/`resume`/`restart`/`cancel <id>` 等单任务命令；同批支持 `-proxy`、`-headers`、`-max-rate`、`-sha256`。
- [`examples/resume`](https://github.com/xiaoLangZe/go-muyuan/tree/main/examples/resume) —— 跨进程续传演示：`-stop-after` 中途停止，下次直接续传。
- [`examples/testsrv`](https://github.com/xiaoLangZe/go-muyuan/tree/main/examples/testsrv) —— 本地支持 Range 的文件服务器，用于端到端试跑。
