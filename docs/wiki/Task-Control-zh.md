# 任务控制

[English](Task-Control) · **中文**

任务就是句柄。四个动词作用于它，外加等待。

| 动词 | 效果 |
| --- | --- |
| `Pause()` | 停传输、保留字节、把连接还回去 |
| `Resume()` | 从磁盘上已有的部分继续 |
| `Restart()` | 丢弃已收字节，从 0 重下 |
| `Delete()` | 停止并删除文件 —— **取消任务也就是它** |
| `Wait()` / `WaitContext(ctx)` | 阻塞到任务结束 |

## 暂停

```go
if err := task.Pause(); err != nil {
	return err
}
// 到这里部分文件已经关闭，可以安全地查看或搬移它。
```

`Pause` 只在传输真的停下、且部分文件已关闭之后才返回。它的连接立刻释放，下一轮调度交给其他文件。

暂停一个已经暂停的任务什么也不做，返回 nil。暂停一个已经完成的任务返回 `ErrNotDownloading`。

**在途的分片会怎样**：被放弃。它们对应的粒度被标记为未完成，所以恢复时那些区间会重新取。这是「位图作为唯一事实来源」的代价，上界是每条连接一片。

## 继续

```go
if err := task.Resume(); err != nil {
	return err
}
```

`Resume` 作用于暂停中的任务，也作用于**失败**的任务：服务端支持 Range 时它从磁盘上已有的字节继续，不支持时从头下载。所以 `Resume` 是网络瞬断后的自然重试手段。

对正在跑的任务调 `Resume` 返回 nil。对从未暂停过的任务 —— 比如已完成或已删除的 —— 调它返回 `ErrNotPaused`。

对等待连接的任务调 `Resume` 只是清掉暂停标记、让它重新排队，它可能还要等。

## 重新下载

```go
if err := task.Restart(); err != nil {
	return err
}
```

`Restart` 丢弃已收字节、清空计数、开一次全新的传输。它在**任何**状态下都能工作，包括已完成和已删除，所以它也是「重下」的手段。

还没拿到连接的任务会被复位、留在队列里，而不是立刻启动 —— 这样对排队中的任务调 `Restart` 不会让它插队突破并发上限。

## 删除 —— 以及「取消」

```go
if err := task.Delete(); err != nil {
	return err
}
```

`Delete` 停止传输并删除成品文件和部分文件。任务以 `StatusDeleted` 收尾。

**这里刻意没有单独的 `Cancel`。** 取消任务和删除任务的区别只在于文件留不留，而两者之中，删除是那个不会在磁盘上留下意外的东西。如果你想放弃下载但保留已下部分，用 `Pause` —— 它才是「停下但不删除」的操作。

对已删除的任务再调 `Delete` 无害，返回 nil。

## 等待

```go
if err := task.Wait(); err != nil {
	return err
}

// 或者自带超时：
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()
if err := task.WaitContext(ctx); err != nil {
	return err
}
```

`Wait` 阻塞到任务进入终态：`completed`、`failed` 或 `deleted`。**排队中和暂停中都不是终态**，所以 `Wait` 会一直阻塞，直到它跑完、被删除、或管理器被关闭。程序看起来卡住，十有八九是这个原因 —— 查一下 `Status()`。

`Done()` 给出 `Wait` 背后的 channel，供你想用 `select` 的场合。被重下的任务会拿到新 channel，引擎层的 `Wait` 会跟随传输进入新的一轮。

## 不阻塞地读状态

```go
task.Status()    // queued / downloading / paused / completed / failed / deleted
task.Progress()  // 字节数、速度、ETA、百分比、连接数、切片长度
task.Info()      // 上述之外还有 ID、URL、路径、Range 模式与错误
task.Err()       // 结束原因；排队中、运行中、暂停时为 nil
task.Workers()   // 分配到的连接数
task.RangeMode() // 服务端是否支持 Range
```

## 状态机

```
queued ──▶ downloading ──▶ completed
  ▲            │  ▲            │
  │            │  │            │
  └────────────┘  │            │
   （预算被收回）  │            │
                 ▼            ▼
              paused ─────▶ deleted
                 │
                 ▼
              failed
```

- `Pause` 把 `downloading` 变成 `paused`。
- `Resume` 把 `paused` 变回 `downloading`（如果暂时没有连接给它，则是 `queued`）。
- 运行中预算被收回，任务变成 `queued` 而不是 `paused` —— 它是在等，不是被你停了。
- `Restart` 把任何状态变回 `downloading` 或 `queued`。
- `Delete` 把任何状态变成 `deleted`。
- 失败的任务停在 `failed`，直到你 `Resume` 或 `Restart`。管理器不会自己重试它。

## 相关

- [健壮性](Robustness-zh) —— 不需要你介入就会发生的重试与停滞处理
- [进度与事件](Progress-and-Events-zh) —— 观察状态变迁
- [调度与预算](Scheduling-and-Budget-zh) —— 任务为什么在排队
