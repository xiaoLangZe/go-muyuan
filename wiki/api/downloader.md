# Downloader

`Downloader` 驱动单个文件的下载，并发安全。

```go
import downloader "github.com/xiaoLangZe/go-muyuan"
```

## 构造函数

### `New(cfg Config) (*Downloader, error)`

校验配置，返回一个可以立即 `Start` 的 downloader。会创建输出与临时目录，并对 URL 做 SSRF 预检（除目录创建外不做真实 I/O）。配置无效时返回 [`ErrInvalidConfig`](./types#哨兵错误)。

## 生命周期方法

| 方法 | 说明 |
| --- | --- |
| `Start(ctx context.Context) error` | 异步开始/续传；同步完成探测与计划。已在运行或暂停时返回 `ErrAlreadyRunning`。`ctx` 界定整个下载。 |
| `Wait() error` | 阻塞到终态，返回 `nil`（成功）、`ErrAborted` 或失败原因。 |
| `Close() error` | 中止进行中的下载，等待停止，释放资源。**之后不要复用**。 |
| `Progress() Progress` | 当前状态快照，任意 goroutine 安全。`Start` 之前返回 idle 快照。 |
| `String() string` | 调试用的配置渲染。 |

## 控制方法

每个方法都有一个等价的信号形式（见[运行时控制](../guide/runtime-control)）。

| 方法 | 说明 |
| --- | --- |
| `Pause()` | 停掉连接池，保留磁盘进度，可续传。 |
| `Resume()` | 在 `Pause` 之后重新拉起连接池。 |
| `SetWorkers(n int)` | 设置（连接 + 切片）总和预算，自动分配；`0` = 传统模式。 |
| `SetConnections(n int)` | 改变该文件的并发 HTTP 连接数（运行中生效）。 |
| `SetSegments(n int)` | 重新切成 n 段（`0` = 自动）；已下载字节保留。 |
| `SetConnectionsAndSegments(connections, segments int)` | 一次重配同时改两个旋钮。 |
| `Restart()` | 删除所有进度并立即用当前配置重新开始。 |
| `ClearCache() error` | 删除临时文件与元数据，保持空闲；再次 `Start` 开始新传输。运行中或空闲都可调用。 |
| `Stop()` | 停止下载并保留进度以备之后 `Start`（→ `Stopped`）。 |
| `Abort()` | 立即取消；`Wait` 随后返回 `ErrAborted`；磁盘进度保留。 |
| `Signals() chan<- SignalEvent` | 控制信号通道（与便捷方法等价）；`Start` 之前也可发送。 |

## 示例

```go
d, err := downloader.New(downloader.Config{
	URL:         "https://example.com/big.iso",
	OutputPath:  "./big.iso",
	Connections: 8,
	Segments:    16,
})
if err != nil {
	log.Fatal(err)
}
defer d.Close()

if err := d.Start(context.Background()); err != nil {
	log.Fatal(err)
}

// 运行中热重配
d.SetConnections(16)
d.SetWorkers(24)

if err := d.Wait(); err != nil {
	log.Fatal(err)
}
log.Printf("完成：%s", d.Progress().OutputPath)
```

## 相关

- [Queue](./queue) —— 批量下载
- [类型与错误](./types) —— `Config`、`Progress`、`State`、哨兵错误
- [运行时控制](../guide/runtime-control) —— 信号表与状态机
