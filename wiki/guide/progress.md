# 进度报告

`Progress` 是不可变快照，可从任意 goroutine 安全读取。

```go
p := d.Progress()
// p.State, p.Total, p.Downloaded, p.Percent,
// p.Speed, p.ETA, p.Connections, p.Segments, p.Workers,
// p.AcceptRanges, p.Err
```

## Progress 字段

| 字段 | 含义 |
| --- | --- |
| `URL` / `OutputPath` | 源地址与输出路径 |
| `Total` / `Downloaded` | 总字节数与已下载字节数 |
| `Percent` | 完成百分比（`0`–`100`） |
| `Speed` | 速率，字节/秒（EWMA 平滑） |
| `ETA` | 预计剩余时间 |
| `Connections` / `Segments` / `Workers` | 当前生效的三维参数 |
| `AcceptRanges` | 服务端是否支持区间请求 |
| `State` | 当前生命周期状态 |
| `Err` | 终态错误（若有） |

## 两种获取方式

### 轮询

```go
for {
	p := d.Progress()
	fmt.Printf("\r%.1f%%  %.1f MiB/s", p.Percent, p.Speed/1024/1024)
	if p.State.IsTerminal() {
		break
	}
	time.Sleep(200 * time.Millisecond)
}
```

### 回调（推送式）

```go
d, _ := downloader.New(downloader.Config{
	URL:        url,
	OutputPath: path,
	OnProgress: func(p downloader.Progress) {
		fmt.Printf("\r%.1f%%  ETA %s", p.Percent, p.ETA)
	},
})
```

回调由管理器的 ticker 触发（约 2 次/秒），且**不得阻塞**——回调在管理器 goroutine 上执行，阻塞会拖慢下载。需要做耗时处理时，把快照投递到自己的 channel 里。

## 队列进度

队列提供三个层次的视图：

| 方法 | 粒度 |
| --- | --- |
| `QueueConfig.OnTaskProgress` | 每个运行中任务的进度回调 |
| `QueueConfig.OnTaskStateChange` | 任务状态变化时回调 |
| `q.Tasks()` / `q.Task(id)` | 全部 / 单个任务的快照 |
| `q.Summary()` | 聚合统计 |

`Summary` 包含 `Total/Pending/Running/Paused/Completed/Failed/Canceled` 计数、`TotalBytes`/`DownloadedBytes` 字节汇总，以及 `Concurrency`。`Summary.Done()` 表示队列是否已排空。
