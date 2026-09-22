# 进度与事件

[English](Progress-and-Events) · **中文**

传输跑在自己的协程上，所以你做什么都不会阻塞它。有两种方式跟随它，可以同时使用。

## 事件流

```go
for ev := range task.Events() {
	switch ev.Type {
	case muyuan.EventProgress:
		fmt.Printf("%.1f%% %d 条连接\n", ev.Progress.Percent, ev.Progress.Workers)
	case muyuan.EventStatus:
		fmt.Println("状态:", ev.Status)
	case muyuan.EventError:
		fmt.Fprintln(os.Stderr, "失败:", ev.Err)
	}
}
```

```go
type Event struct {
	TaskID   string
	Type     EventType // EventProgress、EventStatus、EventError
	Status   Status
	Progress Progress
	Err      error     // 仅 EventError 时非 nil
	At       time.Time
}
```

几条重要的规则：

- channel 在**首次**调用 `Events()` 时创建，在任务结束、**且报告结束的那条事件已投递之后关闭**。所以 `range` 循环能从开始一路跑到结束并自行终止。
- **进度事件最多每 200ms 一条。** 状态变更每条都发，不节流。
- **消费者不读不会拖住下载。** 传输在自己的协程上，只有事件发射协程会等 channel。资源语义见下。
- **比节流还快就结束的任务不产生进度事件** —— 只有状态事件，但里面带的 `Progress` 仍是完整的。在本地快速服务器上这是常态。见[常见问题](FAQ-zh)。

### 资源语义

订阅会为每个任务启动一个协程，所以没人订阅的任务零开销。事件发射协程在任务被删除或管理器被关闭时释放。**订阅了却不再读**的调用方应该 `Delete` 任务或 `Close` 管理器，而不是放着不管。

## 轮询

`Progress()`、`Info()`、`Status()` 随时可调。值与事件里带的完全一致，所以两种风格可以随意混用。

```go
ticker := time.NewTicker(time.Second)
defer ticker.Stop()
for range ticker.C {
	p := task.Progress()
	fmt.Printf("%.1f%%  %d/%d 字节  %d B/s  eta %s\n",
		p.Percent, p.Downloaded, p.Total, p.Speed, p.ETA.Truncate(time.Second))
	if task.Status() == muyuan.StatusCompleted {
		break
	}
}
```

`Info()` 是超集：

```go
type Info struct {
	ID, URL, FilePath, PartPath string
	Status                      Status
	RangeMode                   RangeMode
	Err                         error
	Total, Downloaded, Speed    int64
	Workers                     int
	BlockSize                   int64
	Elapsed                     time.Duration
}
```

## 这些数字的含义

```go
type Progress struct {
	Total      int64         // 完整大小；服务端未告知时为 0
	Downloaded int64         // 已可靠落盘的字节数
	Speed      int64         // 字节/秒，按最近一个采样窗口计算
	ETA        time.Duration // 预计剩余时间，无法推算时为 0
	Percent    float64       // Downloaded / Total，未知时为 0
	Elapsed    time.Duration // 净传输耗时，不含暂停
	Workers    int           // 分配到的连接数
	BlockSize  int64         // 当前切片长度，未分片时为 0
}
```

- **`Downloaded` 只统计已完成片**，所以会比在途工作滞后，上界是每条连接一片。在并行路径里这是唯一事实来源，也是暂停时不需要任何补偿计算的原因。见[并行下载](Parallel-Downloads-zh)。
- **`Speed` 是采样值，不是瞬时值。** 引擎最多每 250ms 取一次快照，事件流最多每 200ms 转发一次，所以非常短的传输第一个样本可能低于真实速率。
- **`Elapsed` 不含暂停时间**，所以它是传输耗时而非墙钟时间。
- **`Workers` 是分配量**，不是实际开着的连接数。见[常见问题](FAQ-zh)。

## 同时监控多个任务

```go
for _, task := range m.Tasks() {
	p := task.Progress()
	fmt.Printf("%-30s %6.1f%%  %s\n",
		filepath.Base(task.FilePath()), p.Percent, task.Status())
}
```

`m.Tasks()` 是按加入顺序的快照。`m.Task(id)` 按标识查一个。

## 回调

`WithProgress` 和 `WithDefaultProgress` 注册回调而不是 channel。回调跑在**传输协程**上，所以**它不能阻塞，也不能调用会等待传输停止的方法** —— 在回调里对同一个任务调用 `Pause`、`Wait` 或 `Delete` 会死锁。

有条件的话优先用事件流：没人读的 channel 不会拖住传输，而慢回调会。

## 相关

- [API 参考](API-Reference-zh) —— 精确的签名
- [任务控制](Task-Control-zh) —— 事件可能报告的那些状态
- [常见问题](FAQ-zh) —— 小文件为什么没有进度事件
