# go-muyuan

[English](Home) · **中文**

一个 Go 的 HTTP 下载库。一个下载器管理多个文件，它们共享一份连接预算；单个文件可以同时用多条连接下载。每个文件都是一个任务、都有句柄，可以暂停、继续、重新下载、取消，进度可以实时跟随。

```go
m := muyuan.New(
	muyuan.WithThreads(16),  // 所有任务共享的连接预算
	muyuan.WithMaxFiles(3),  // 同时下载的文件数
	muyuan.WithDefaultDir("downloads"),
)
defer m.Close()

task, err := m.AddTask("downloads", "https://example.com/big.iso")
if err != nil {
	return err
}
for ev := range task.Events() { // 实时进度，不用轮询
	fmt.Printf("%s %.1f%%\n", ev.Status, ev.Progress.Percent)
}
fmt.Println("saved to", task.FilePath())
```

## 特性

- **单个文件多连接**：文件被切成若干片并行取，一条慢连接不会拖住整个传输。
- **跨文件共享连接预算**：管理器按广度优先把一份连接池分给正在跑的文件。见[调度与预算](Scheduling-and-Budget-zh)。
- **切片长度自动调整**：切片长度向「约 1.5 秒一片」的目标靠拢，服务端快就发长请求、慢就发短请求。
- **实时进度不用轮询**：事件流带节流后的进度、每一次状态变更、以及任务结束时的错误。见[进度与事件](Progress-and-Events-zh)。
- **代理支持**：`http`、`https`、`socks5`，可带账号密码。见[代理与安全](Proxy-and-Security-zh)。
- **可续传**：暂停后从磁盘上已有的字节继续，不依赖服务端响应 `If-Range`。
- **不会把半成品当成成品**：字节先写 `.part`，全部收完才改名。
- **瞬态失败会重试**，连接停滞会被检测。
- **内网地址被拒绝**：发请求前校验一次，建连时再校验一次。
- **零第三方依赖**：只用标准库。

## 目录

| 页面 | 内容 |
| --- | --- |
| [快速开始](Getting-Started-zh) | 引入、下第一个文件、看懂结果 |
| [调度与预算](Scheduling-and-Budget-zh) | 连接如何分配给各个文件 |
| [并行下载](Parallel-Downloads-zh) | 切片与粒度、切片长度如何决定 |
| [任务控制](Task-Control-zh) | 暂停、继续、重下、删除，以及字节的去向 |
| [进度与事件](Progress-and-Events-zh) | 事件流与轮询 |
| [代理与安全](Proxy-and-Security-zh) | 代理，以及主机校验管什么、不管什么 |
| [健壮性](Robustness-zh) | 重试、停滞检测、分片文件、命名 |
| [API 参考](API-Reference-zh) | 每个选项、方法、类型、错误 |
| [常见用法](Recipes-zh) | 可直接抄的片段 |
| [架构](Architecture-zh) | 包的划分，以及为什么保留两条传输路径 |
| [限制与路线](Limitations-and-Roadmap-zh) | 还没实现的东西 |
| [常见问题](FAQ-zh) | 实战踩坑，带原因解释 |

## 环境要求

Go 1.21 或更高。除标准库外无任何依赖。

## 许可证

MIT —— 见 [LICENSE](https://github.com/xiaoLangZe/go-muyuan/blob/main/LICENSE)。
