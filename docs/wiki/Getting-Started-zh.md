# 快速开始

[English](Getting-Started) · **中文**

## 引入

```bash
go get github.com/xiaoLangZe/go-muyuan
```

```go
import muyuan "github.com/xiaoLangZe/go-muyuan"
```

包名是 `muyuan`，所以调用写成 `muyuan.New(...)`。

如果你是对着本地检出开发而不是用已发布的模块，改用 `replace` 指向它：

```
require github.com/xiaoLangZe/go-muyuan v0.0.0

replace github.com/xiaoLangZe/go-muyuan => ../go-muyuan
```

## 下第一个文件

```go
package main

import (
	"fmt"
	"log"

	muyuan "github.com/xiaoLangZe/go-muyuan"
)

func main() {
	m := muyuan.New(
		muyuan.WithThreads(8),  // 可供任务使用的连接数
		muyuan.WithMaxFiles(1), // 同时下一个文件
	)
	defer m.Close()

	task, err := m.AddTask("downloads", "https://example.com/file.iso")
	if err != nil {
		log.Fatal(err)
	}

	if err := task.Wait(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("saved to", task.FilePath())
}
```

这里发生了四件事：

1. `New` 创建一个管理器：它持有连接预算、文件数上限和一个调度器。`Close` 会停掉它和它持有的全部任务，所以适合放在 `defer` 里。
2. `AddTask` 立即校验 URL 并把下载排队。它马上返回；传输等预算有空位才开始。
3. `Wait` 阻塞到任务结束，并报告结束的原因。
4. `FilePath` 是成品文件最终的位置。

`AddTask(destDir, rawURL string, opts ...TaskOption)` 的第一个参数是下载目录。留空则回退到 `WithDefaultDir`。

## 看着它下载

传输跑在自己的协程上，所以句柄就足够你跟随它。两种方式，可同时用：

```go
// 1. 事件流：进度、状态变更、以及结束
for ev := range task.Events() {
	fmt.Printf("%s %.1f%% %d 条连接\n",
		ev.Status, ev.Progress.Percent, ev.Progress.Workers)
}

// 2. 轮询，随你什么时候读
p := task.Progress()
fmt.Println(p.Downloaded, p.Total, p.Speed, p.ETA)
```

两者读到的值一致。见[进度与事件](Progress-and-Events-zh)。

## 文件落在哪里

下载目录的解析分三种情况：

| `destDir` | 结果 |
| --- | --- |
| 绝对路径，如 `/srv/files` | 原样使用 |
| 相对路径，如 `downloads` | 拼接到根目录上，根目录默认为**可执行文件所在目录** |
| 空 | 用管理器的 `WithDefaultDir`，按同样的规则解析 |

根目录可以用 `WithRootDir` 改。默认取可执行文件目录而不是当前工作目录的原因：从服务或定时任务里启动的下载没有有意义的「当前目录」，写在二进制旁边至少是可预测的。

传输期间字节写在 `FilePath()+".part"`。全部收完并 flush 之后才改名成最终文件名，所以真名之下永远不会出现写了一半的文件。见[健壮性](Robustness-zh)。

## 看懂状态

| 状态 | 含义 |
| --- | --- |
| `StatusQueued` | 等管理器给它一条连接 |
| `StatusPending` | 已准备好但还没启动 |
| `StatusDownloading` | 传输中 |
| `StatusPaused` | 被你停了，字节保留 |
| `StatusCompleted` | 已完成，文件已改名 |
| `StatusFailed` | 因错误停止，看 `Err()` |
| `StatusDeleted` | 已停止且文件已删除 |

`queued` 和 `paused` 都是「没在跑」，也都不是终态：`Wait` 会一直阻塞在这两个状态上。区别在于**是谁停的** —— 你按了暂停，还是调度器还没轮到它。

## 接下来看什么

- [调度与预算](Scheduling-and-Budget-zh) —— 任务为什么在排队，以及怎么改同时下载的文件数
- [任务控制](Task-Control-zh) —— 暂停、继续、重下、删除
- [常见用法](Recipes-zh) —— 常见需求的可抄片段
- [常见问题](FAQ-zh) —— 最先冒出来的那些疑问
