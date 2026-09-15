# 快速上手

## 环境要求

要求 **Go 1.26 或更高版本**（`go.mod` 里的 `go` 指令）。工具链过旧会报 `go.mod requires go >= 1.26`。

## 安装

仓库发布到 GitHub 之后：

```bash
go get github.com/xiaoLangZe/go-muyuan
```

**目前**它会因为远端尚不存在而失败，报错为 `remote: Repository not found`。想从本地目录使用，请改用 `replace` 指令指向源码目录：

```text
// 你项目的 go.mod
require github.com/xiaoLangZe/go-muyuan v0.0.0

replace github.com/xiaoLangZe/go-muyuan => /绝对路径/go-muyuan
```

然后导入（包名是 `downloader`，与模块路径最后一段不一致，所以加个别名更清楚）：

```go
import downloader "github.com/xiaoLangZe/go-muyuan"
```

## 第一个下载

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
			fmt.Printf("\r%.1f%%  %.1f MiB/s  (%d conns, %d segs)",
				p.Percent, p.Speed/1024/1024, p.Connections, p.Segments)
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := d.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("\ndone:", d.Progress().OutputPath)
}
```

## 生命周期

一次典型下载按顺序走这四步：

1. **`New(Config)`** —— 校验配置、创建输出与临时目录、对 URL 做 SSRF 预检（不做真实 I/O）。
2. **`Start(ctx)`** —— 同步探测服务端（`HEAD` + 1 字节 `Range` GET）、核对磁盘上的续传元数据、预分配 partial 文件、构建切片计划；探测或校验失败会立即返回。之后 manager goroutine 接管。
3. **`Wait()`** —— 阻塞到终态，返回 `nil`（成功）、`ErrAborted` 或失败原因。
4. **`Close()`** —— 中止并等待收尾，释放资源（可选）。

中途想观察进度或干预，用 `d.Progress()` 轮询、`Config.OnProgress` 回调，或 `d.Signals()` / 便捷方法（见[运行时控制](./runtime-control)）。

## 磁盘上的文件

对输出路径 `P`：

| 路径 | 用途 |
| --- | --- |
| `P` | 最终文件（仅在成功时创建） |
| `P.muyuan.partial` | 下载中的文件，已预分配到完整大小 |
| `P.muyuan.meta.json` | JSON 进度记录 |

## 跑示例

仓库自带三个示例。先用本地文件服务器起一个支持 Range 的源：

```bash
# 终端 1：在本地提供某个目录
go run ./examples/testsrv -dir ./testdata -addr 127.0.0.1:18080

# 终端 2：从它下载（回环地址需要 allow-private）
go run ./examples/basic -allow-private -o ./file.bin -conn 8 -seg 16 \
    http://127.0.0.1:18080/file.bin

# 或者一次批量下载多个文件
go run ./examples/batch -allow-private -o ./downloads -c 3 -conn 8 \
    http://127.0.0.1:18080/a.bin http://127.0.0.1:18080/b.bin
```

- `examples/basic` —— 交互式 CLI，带实时进度条，以及暂停/继续/重配/重下/清缓存的命令。
- `examples/batch` —— 使用任务队列的批量下载器，带多行状态面板与单任务命令。
- `examples/testsrv` —— 本地支持 Range 的文件服务器。

## 下一步

- [配置选项](./configuration)
- [运行时控制](./runtime-control)
- [批量队列](./queue)
