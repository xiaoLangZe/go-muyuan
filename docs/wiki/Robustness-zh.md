# 健壮性

[English](Robustness) · **中文**

网络出问题时库自己会做什么，以及做这些事的时候磁盘上留着什么。

## 重试

失败的尝试会被重试，默认 3 次，等待时间从 1 秒逐次翻倍到 30 秒上限。

```go
muyuan.WithDefaultRetries(5)              // 管理器级
muyuan.WithDefaultRetryDelay(2*time.Second)
muyuan.WithRetries(0)                     // 或单条任务：立刻放弃
```

**算瞬态的**（会重试）：

- 任何 5xx 响应；
- `408 Request Timeout`、`425 Too Early`、`429 Too Many Requests`；
- 连接被断开；
- 响应体在到达声明的长度之前结束；
- 传输停滞（见下）。

**不算的**（直接失败）：

- 上述三种之外的 4xx —— 404 不会变成 200；
- URL 不可用或 host 被拦；
- 磁盘写入错误。

分片模式下**每个分片各自重试**，所以一次坏请求只损失一片，不是整个传输。

`HTTPStatusError` 暴露了库做出的判断：

```go
var status *muyuan.HTTPStatusError
if errors.As(err, &status) {
	fmt.Println(status.StatusCode, status.Retryable())
}
```

## 停滞检测

不关闭但也不再发数据的连接会把下载永久挂住。如果连续停滞超时 —— 默认 60 秒 —— 没有收到任何字节，这次传输按瞬态失败处理并重试。

```go
muyuan.WithDefaultStallTimeout(2 * time.Minute)
muyuan.WithDefaultStallTimeout(0) // 关闭检测
```

超时是**按连接**计的，所以分片模式下一个分片停滞不会影响其他分片。

## 部分文件

字节写入 `FilePath()+".part"`。只有全部收完之后，文件才会被 flush（`fsync`）、关闭、改名成最终文件名。最终路径不会有别的写入者，所以真名之下的文件永远是完整的。

分片模式下 `.part` 会被预先创建成完整大小、原地填充。见[并行下载](Parallel-Downloads-zh)。

## 文件名

名字的来源按优先级：

1. `WithFileName`，如果给了；
2. 服务端的 `Content-Disposition` 头；
3. URL 的最后一段路径。

无论来源是什么，使用前都会被净化：裁到只剩最后一段路径、Windows 不接受的字符被替换、末尾的点和空格去掉、命中设备保留名时加前缀。`../../etc/passwd` 会变成 `passwd`。

超过 200 字节的名字会被截断并保留扩展名，按 rune 边界裁，不会切断多字节字符。

## 同名文件

默认**不覆盖**已存在的文件，而是追加序号：`report.pdf`、`report_1.pdf`、`report_2.pdf`，取第一个空闲的名字。

```go
muyuan.WithOverwrite(true) // 改为直接替换
```

## 取消

管理器持有一个 context，由 `New` 创建、由 `Close` 取消。API 里没有任务级的 context；要停一个任务就用 `Pause`、`Delete`，或关掉管理器。

```go
m := muyuan.New(...)
defer m.Close() // 取消管理器的 context，也就取消了它下面所有传输
```

`Close` 之后：

- 正在跑的传输被取消，以 `failed` 收尾；
- 当时排队或暂停的任务报 `ErrClosed`；
- **部分文件留在磁盘上**，已下载的内容不会丢；
- `AddTask` 报 `ErrClosed`。

```go
if errors.Is(err, muyuan.ErrClosed) {
	// 管理器已经关了
}
```

## 什么**不会**被自动处理

- **失败的任务就停在失败状态。** 管理器不会把它复活；你自己调 `Resume`（从磁盘继续）或 `Restart`（从 0 重下）。
- **新进程从头开始。** 完成度位图只在内存里，进程崩溃就丢失了「哪些分片已到」的记录。见[限制与路线](Limitations-and-Roadmap-zh)。
- **不校验内容。** 没有摘要校验环节；需要的话在 `Wait` 返回后自己算哈希。

## 相关

- [任务控制](Task-Control-zh) —— 手动的那几个动词
- [常见问题](FAQ-zh) —— 实战里会撞到的错误
- [限制与路线](Limitations-and-Roadmap-zh) —— 还缺什么
