# 批量队列

`Queue` 以有界并发下载多个文件。它的 `Concurrency` 上限（同时下载几个文件）与每个文件自己的连接/切片是**两个独立维度**——"同时 3 个文件、每个 8 连接"意味着最多 24 条并发连接。

## 基本用法

```go
q, err := downloader.NewQueue(downloader.QueueConfig{
	Concurrency: 3,        // 同时下载的文件数
	OutputDir:   "./downloads",
	MaxRetries:  2,        // 每个任务放弃前的重试次数
	Template: downloader.Config{
		Connections: 8,    // 每个文件的并发连接数
		Segments:    16,   // 每个文件的字节区间段数（0 = 自动）
	},
	OnTaskStateChange: func(t downloader.TaskInfo) {
		fmt.Printf("%s: %s\n", t.ID, t.State)
	},
})
if err != nil {
	log.Fatal(err)
}

ids, err := q.AddAll([]string{url1, url2, url3}, "") // "" => 使用 OutputDir
if err != nil {
	log.Fatal(err)
}

if err := q.Start(context.Background()); err != nil {
	log.Fatal(err)
}
if err := q.Wait(); err != nil {
	// 聚合所有失败任务；可用 errors.As(*downloader.TaskError) 取出单个。
	log.Fatal(err)
}
fmt.Println(q.Summary())
```

任务可以在**队列运行期间继续添加**，会在有空闲槽位时被调度。

## QueueConfig 字段

| 字段 | 含义 | 默认值 |
| --- | --- | --- |
| `Concurrency` | 同时下载的文件数，范围 `[1,128]` | `3` |
| `Template` | 每个文件的默认配置（其 `URL`/`OutputPath`/`OnProgress` 被忽略） | —— |
| `OutputDir` | 未指定输出路径的任务的保存目录 | `.` |
| `MaxRetries` | 每个任务的重试次数 | `0`（不重试） |
| `RetryDelay` | 重试前等待时间 | `2s` |
| `OnTaskProgress` | 每个运行中任务的进度回调 | 无 |
| `OnTaskStateChange` | 任务状态变化回调 | 无 |
| `OnQueueDone` | 队列排空时调用一次 | 无 |

## 三种入队方式

| 方法 | 用途 |
| --- | --- |
| `q.Add(url, outputPath)` | 入队一个下载；路径留空则从 URL 推导并去重 |
| `q.AddAll(urls, dir)` | 批量入队到目录（`""` = `OutputDir`） |
| `q.AddTask(TaskSpec{...})` | 用完整 `TaskSpec` 入队，可在运行中调用 |

## 队列控制

与单文件下载相同的双形态 API：`q.Signals() <- QueueSignal{...}` 或便捷方法。

| 动作 | 方法 | 信号 |
| --- | --- | --- |
| 全部暂停（保留进度） | `q.Pause()` | `QueueSignal{Type: QSigPause}` |
| 全部继续 | `q.Resume()` | `QueueSignal{Type: QSigResume}` |
| 设置同时下载文件数 | `q.SetConcurrency(n)` | `QueueSignal{Type: QSigSetConcurrency, Concurrency: n}` |
| 设置每文件 Workers 总和 | `q.SetWorkers(n)` | `QueueSignal{Type: QSigSetWorkers, Workers: n}` |
| 设置每文件连接数 | `q.SetConnections(n)` | `QueueSignal{Type: QSigSetConnections, Connections: n}` |
| 设置每文件切片数 | `q.SetSegments(n)` | `QueueSignal{Type: QSigSetSegments, Segments: n}` |
| 同时设置两者 | `q.SetConnectionsAndSegments(c, s)` | `QueueSignal{Type: QSigSetConnectionsAndSegments, ...}` |
| 清空所有缓存 | `q.ClearCache()` | `QueueSignal{Type: QSigClearCache}` |
| 停止（保留进度并退出） | `q.Stop()` | `QueueSignal{Type: QSigStop}` |
| 中止 | `q.Abort()` | `QueueSignal{Type: QSigAbort}` |

每文件旋钮（`SetWorkers`/`SetConnections`/`SetSegments`）会同时作用于**正在运行**的任务和**之后启动**的任务。

## 单任务控制与查询

| 动作 | 方法 |
| --- | --- |
| 暂停单个任务（队列 Resume 不会唤醒它） | `q.PauseTask(id)` |
| 继续单个任务 | `q.ResumeTask(id)` |
| 重下单个任务（删除其输出文件） | `q.RestartTask(id)` |
| 删除单个任务的临时文件与元数据 | `q.ClearTaskCache(id)` |
| 取消单个任务 | `q.CancelTask(id)` |
| 取单个/全部任务快照 | `q.Task(id)` / `q.Tasks()` |
| 取聚合快照 | `q.Summary()` |

## 任务状态

```text
Pending ──► Running ──► Completed
   ▲          │  │
   │          │  └──► Failed        (重试次数耗尽)
   │          │
   │          ├──► Paused   ──► Running   (挂起/继续)
   │          │
   └──────────┴──► Canceled
        (重试退避)
```

- **Paused** 同时涵盖调用方暂停（`PauseTask`）和队列挂起（`Pause`，或调低并发）。队列 `Resume` 只会唤醒后者。
- 队列 `Wait` 在所有任务都无法推进时返回。被调用方暂停的任务会让队列一直等到它被继续或取消。

## 并发与槽位

挂起任务会**释放它占用的并发槽位**，同时把进度保留在磁盘上（通过对底层下载执行"停止再续传"，而不是"原地暂停"）。这在批量场景下很重要：被挂起的任务不会白白占着一条空闲连接。

把 `Concurrency` 调到低于当前运行数时，**最近启动**的任务会被挂起，它们会在槽位空出时自动恢复。

## 输出路径推导

未显式指定 `OutputPath` 的任务，其文件名从 URL 推导：只取最后一个路径段，并剥掉目录分隔符、控制字符与文件系统保留字符，因此构造过的 URL 无法逃出 `OutputDir`。重名会追加 `-1`、`-2` 去重。

## 任务错误

`Queue.Wait` 返回所有失败任务的聚合错误（`errors.Join` 的 `*TaskError`，各带任务 ID 与 URL）。全部成功返回 `nil`；只要有一个失败就返回非 nil，因此部分失败不会静默通过。`Summary` 给出分类统计。
