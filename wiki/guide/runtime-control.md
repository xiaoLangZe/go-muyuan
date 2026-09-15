# 运行时控制

每个控制动作都有两种等价形式：`d.Signals()` 通道上的类型化信号，以及便捷方法。二者完全等价。

| 动作 | 方法 | 信号 |
| --- | --- | --- |
| 暂停（保留进度） | `d.Pause()` | `SignalEvent{Type: SigPause}` |
| 继续 | `d.Resume()` | `SignalEvent{Type: SigResume}` |
| 设置 Workers 总和 | `d.SetWorkers(n)` | `SignalEvent{Type: SigSetWorkers, Workers: n}` |
| 设置连接数 | `d.SetConnections(n)` | `SignalEvent{Type: SigSetConnections, Connections: n}` |
| 设置切片数 | `d.SetSegments(n)` | `SignalEvent{Type: SigSetSegments, Segments: n}` |
| 同时设置两者 | `d.SetConnectionsAndSegments(c, s)` | `SignalEvent{Type: SigSetConnectionsAndSegments, ...}` |
| 重下（清空并立即重启） | `d.Restart()` | `SignalEvent{Type: SigRestart}` |
| 清缓存（只删除） | `d.ClearCache()` | `SignalEvent{Type: SigClearCache}` |
| 停止（保留进度并退出） | `d.Stop()` | `SignalEvent{Type: SigStop}` |
| 中止（退出，`Wait` 返回 `ErrAborted`） | `d.Abort()` | `SignalEvent{Type: SigAbort}` |

```go
d.Signals() <- downloader.SignalEvent{Type: downloader.SigSetConnections, Connections: 16}
```

## 「重下」与「清缓存」的区别

- **重下（Restart）** 删除临时文件与元数据，并**立即用当前配置重新开始**下载。一个信号，全新开始。
- **清缓存（ClearCache）** 删除临时文件与元数据，让下载器保持空闲；需要再次调用 `Start` 才会开始新的传输。无论下载是否在运行都可用。

## 暂停 / 停止 / 中止的区别

这三个都"停下来"，但语义不同：

| 动作 | manager 是否存活 | 状态 | 能否再 `Start` | `Wait` 返回什么 |
| --- | --- | --- | --- | --- |
| `Pause` | 是 | `Paused` | 用 `Resume` 继续 | 等待中 |
| `Stop` | 否 | `Stopped` | 可以，从断点续传 | 返回 `nil` |
| `Abort` | 否 | 终态 | —— | `ErrAborted` |

## 状态机（单文件下载）

```text
        Start
Idle ─────────────► Running ◄────────────┐
                      │  ▲                │ Resume
             Pause    │  └────────────────┤
                      ▼                   │
                   Paused ────────────────┘
                      │
     Stop ────────────┼────────► Stopped   (可续传：再次 Start)
                      │
              Complete┴────────► Completed
                      │
             error ───┴────────► Failed
```

重配置类信号（`SetWorkers` / `SetConnections` / `SetSegments` / `SetConnectionsAndSegments`）**不改变状态**；它们会取消当前连接代次、重建切片计划，然后重新拉起连接池。在 Workers 总和模式下，manager 还会运行一个自适应慢分片拆分器，把明显滞后的分片一分为二，让更快的 worker 领走后半段（详见[架构设计](./architecture)）。

## 热重配为什么安全

规则只有一条：

> **连接代次在运行期间绝不被修改。** 要改任何东西，manager 先取消当前代次，等**每一条**连接返回，然后才重建计划并生成新一代。

因此重配不会让任何 worker 观察到"计划在自己脚下变了"。已下载的字节通过**最长连续前缀**重新映射到新计划，不重下已完成区域。细节见[架构设计](./architecture)。
