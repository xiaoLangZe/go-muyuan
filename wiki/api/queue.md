# Queue

`Queue` 是有界并发的多文件下载器，并发安全。

## 构造与入队

| 方法 | 说明 |
| --- | --- |
| `NewQueue(cfg QueueConfig) (*Queue, error)` | 校验配置，返回空队列。配置无效返回 `ErrInvalidConfig`。 |
| `Add(rawURL, outputPath string) (string, error)` | 入队一个下载；路径留空则从 URL 推导到 `OutputDir` 并去重。返回任务 ID。 |
| `AddAll(urls []string, dir string) ([]string, error)` | 逐个人队到目录（`""` = `OutputDir`）；返回 ID 列表与首个 URL 错误。 |
| `AddTask(spec TaskSpec) (string, error)` | 用完整 `TaskSpec` 入队；可在运行中调用。 |

## 运行与等待

| 方法 | 说明 |
| --- | --- |
| `Start(ctx context.Context) error` | 开始调度；已在运行返回 `ErrAlreadyRunning`；要求至少一个任务。 |
| `Wait() error` | 阻塞到排空或中止；返回 `nil` 或 `errors.Join` 的 `*TaskError`。被调用方暂停的任务会阻止排空。 |
| `Close() error` | 中止并等待，返回聚合错误。 |
| `Signals() chan<- QueueSignal` | 队列控制信号通道。 |
| `String() string` | 调试渲染。 |

## 整体控制

| 方法 | 说明 |
| --- | --- |
| `Pause()` | 挂起所有运行中任务（保留进度），停止启动新任务。 |
| `Resume()` | 撤销 `Pause`；被调用方暂停的任务保持暂停。 |
| `Stop()` | 挂起全部，保留进度，结束当前运行；再次 `Start` 继续。 |
| `Abort()` | 取消所有任务；`Wait` 随后返回 `ErrAborted`。 |
| `SetConcurrency(n int)` | 改变同时下载的文件数；调低会挂起最近启动的任务。 |
| `SetWorkers(n int)` | 设置每文件（连接 + 切片）预算；`0` = 传统模式。 |
| `SetConnections(n int)` | 改变每文件连接数，作用于运行中与后续任务。 |
| `SetSegments(n int)` | 改变每文件切片数（`0` = 自动），作用于运行中与后续任务。 |
| `SetConnectionsAndSegments(connections, segments int)` | 一次改两个每文件旋钮。 |
| `ClearCache() error` | 删除每个任务的临时文件与元数据；非终态任务重置为重新下载。 |

## 单任务控制

| 方法 | 说明 |
| --- | --- |
| `PauseTask(id string) error` | 挂起一个任务（粘性：队列 `Resume` 不会唤醒它）。 |
| `ResumeTask(id string) error` | 让被挂起/暂停的任务重新可运行。 |
| `RestartTask(id string) error` | 丢弃输出与进度，从头重跑，重置重试预算。 |
| `ClearTaskCache(id string) error` | 删除一个任务的临时文件与元数据；非终态重置为重新下载。 |
| `CancelTask(id string) error` | 从队列移除任务；运行中的会被中止，且永不重跑。 |

## 查询

| 方法 | 说明 |
| --- | --- |
| `Tasks() []TaskInfo` | 按入队顺序返回全部任务快照。 |
| `Task(id string) (TaskInfo, bool)` | 返回单个任务快照。 |
| `Summary() Summary` | 聚合快照。 |

## 示例

```go
q, err := downloader.NewQueue(downloader.QueueConfig{
	Concurrency: 3,
	OutputDir:   "./downloads",
	MaxRetries:  2,
	Template:    downloader.Config{Connections: 8, Segments: 16},
})
if err != nil {
	log.Fatal(err)
}
defer q.Close()

ids, err := q.AddAll(urls, "")
if err != nil {
	log.Fatal(err)
}

if err := q.Start(context.Background()); err != nil {
	log.Fatal(err)
}

// 运行中挂起单个任务
if len(ids) > 0 {
	_ = q.PauseTask(ids[0])
}

if err := q.Wait(); err != nil {
	// 取出单个任务错误
	var te *downloader.TaskError
	if errors.As(err, &te) {
		log.Printf("任务 %s（%s）失败：%v", te.ID, te.URL, te.Err)
	}
}

s := q.Summary()
log.Printf("完成 %d / 失败 %d / 共 %d", s.Completed, s.Failed, s.Total)
```

## 相关

- [Downloader](./downloader)
- [类型与错误](./types) —— `QueueConfig`、`TaskSpec`、`TaskInfo`、`Summary`
- [批量队列指南](../guide/queue)
