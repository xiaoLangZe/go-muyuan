# 常见问题

[English](FAQ) · **中文**

实战里会撞到的问题，带**原因**而不只是修法。

---

### 用 `httptest` 写测试为什么报 `ErrBlockedHost`？

因为 `httptest` 监听环回地址，而主机校验默认拒绝环回、私网与保留地址。这正是校验在尽职：一个可能来自用户或远端 API 的 URL 不该能碰到你自己的机器。

在测试里关掉它，且只在测试里关：

```go
m := muyuan.New(muyuan.WithDefaultAllowUnsafeHosts(true))
```

这个开关还关掉了什么，见[代理与安全](Proxy-and-Security-zh)。

---

### 只收到状态事件，没有进度事件

下载在 200ms 之内就结束了。进度事件被节流到每 200ms 最多一条，所以一个已经结束的传输不产生任何进度事件；但状态事件里带的 `Progress` 快照依然完整，包括最终的 100%。

在本地网络里这是常态。想看到进度事件，就下足够大的东西、让传输持续超过零点几秒，或者把服务端弄慢。

---

### `Workers()` 显示 16，但服务端只看到 4 条连接

`Workers` 报告的是调度器**分配**的数字，不是实际开着的连接数。引擎会照办，但最多用到「文件能切出的片数」那么多。

1 MiB 的文件、最小切片 256 KiB，只能切 4 片，所以最多 4 条连接真的干活；另外 12 个 worker 立刻退出了。没有浪费，但这个数字是预算而不是实测值。

想知道真正在用的连接数，看 `BlockSize` 和文件大小去推算，或者在服务端数请求。

---

### 暂停时 `.part` 文件是整个文件的大小

那是并行路径：文件被预先创建成完整大小，再用定位写入原地填充，所以文件大小完全反映不出进度。判断进度请用 `Progress().Downloaded`。

单连接路径下 `.part` 大小就是已接收量，符合直觉。两种模式里 `Downloaded` 含义一致：已可靠落盘的字节。

---

### `Wait()` 永远不返回

`Wait` 阻塞到任务进入终态 —— `completed`、`failed` 或 `deleted`。**排队中**的任务不是终态，**暂停中**的也不是：

- 管理器没有连接可给它时，任务处于 `queued`。用 `WithMaxFiles(2)` 加四个文件，其中两个就在等。
- 你调过 `Pause` 之后任务是 `paused`，会一直停在那里，直到 `Resume` 或 `Delete`。

查 `Status()` 看是哪一种。想带时限等待，用 `WaitContext`。

---

### 进度回调死锁了

`WithProgress` 的回调跑在**传输协程**上。在回调里对同一个任务调 `Pause`、`Wait` 或 `Delete`，会等那个协程，而那个协程正在等回调返回：死锁。

优先用事件流 —— 没人读的 channel 不会拖住传输：

```go
for ev := range task.Events() { ... }
```

---

### 404 不重试，但 500 会

刻意如此。404 下次尝试也不会变成 200，重试只是浪费时间。瞬态指的是：5xx、408、425、429、连接被断开、响应体提前结束、或停滞。

```go
var status *muyuan.HTTPStatusError
if errors.As(err, &status) {
	fmt.Println(status.StatusCode, status.Retryable())
}
```

---

### 进程重启后怎么续传？

目前做不到。完成度位图在内存里，所以新进程会从头下，哪怕磁盘上的 `.part` 装着正确的字节。这是已知的最大缺口，见[限制与路线](Limitations-and-Roadmap-zh)。

在**同一个进程内**，暂停继续是正常的，而且 `Resume` 也能让**失败**的任务从磁盘继续。

---

### 吞吐比预期低

查 `Workers()` 和 `RangeMode()`：

- **`Workers() == 1`** —— 文件在用单连接流式下载。这在「预算分给多个文件后只剩 1 条」时发生，而 `WithThreads(4)` 加几个文件就是默认情形。给这个文件更多预算，或减少同时下载的文件数。
- **`RangeMode() == RangeUnsupported`** —— 服务端忽略 `Range`，文件没法切分。客户端无能为力。
- **小于 512 KiB 的文件永不切分** —— 太小，开销划不来。

---

### 怎么取消下载但保留已下载的部分？

用 `Pause`。没有单独的 `Cancel`：取消和删除的区别只在于文件留不留，而 `Delete` 是删的那个。

```go
task.Pause()  // 停下、保留字节、释放连接
task.Delete() // 停下并删除两个文件
```

---

### 我设的请求头没有发出去

看你用的是哪个选项。`WithHeader` 只给**一条任务**设头；`WithDefaultHeader` 给管理器创建的**每个任务**设。把 `WithHeader` 传给 `New` 编译不过，因为两种选项类型不同。

```go
m := muyuan.New(muyuan.WithDefaultHeader("X-Token", token))        // 每个任务
task, _ := m.AddTask(dir, url, muyuan.WithHeader("X-Token", token)) // 单个任务
```

---

### `go test` 报 "Access is denied" / "fork/exec"

在 Windows 上观察到过，原因是杀软或端点防护拦下了 Go 构建缓存里刚生成的测试二进制。二进制本身是好的 —— 直接执行它就能跑通。绕法：

```bash
go test -c -o /tmp/pkg.test.exe ./...
/tmp/pkg.test.exe
```

或者给杀软加排除项：Go 构建缓存目录和 `%TEMP%\go-build*`。

注意本模块自己的测试不在版本库里，见[限制与路线](Limitations-and-Roadmap-zh)。

---

### 为什么包名是 `muyuan` 而不是 `go_muyuan`？

两个原因。Go 的包名不能含连字符；而 `go_muyuan` 带下划线虽然合法但不地道。`muyuan` 是这个模块去掉 `go-` 前缀之后的叫法，也是仓库命名为 `go-something` 时的通行做法。

导入路径是模块根：

```go
import muyuan "github.com/xiaoLangZe/go-muyuan"
```

那个显式别名是可选的，写它是因为路径的最后一段是 `go-muyuan` 而包名是 `muyuan`。

---

### 能不能不配连接预算、就当普通下载器用？

可以 —— 预算设 1、文件数设 1，它的行为就等同于一个顺序下载器：

```go
m := muyuan.New(muyuan.WithThreads(1), muyuan.WithMaxFiles(1))
```

文件会用单连接取回，重试、停滞检测、部分文件这些保证一个不少。

## 相关

- [快速开始](Getting-Started-zh) —— 基础
- [健壮性](Robustness-zh) —— 什么会被自动处理
- [限制与路线](Limitations-and-Roadmap-zh) —— 缺口的完整清单
