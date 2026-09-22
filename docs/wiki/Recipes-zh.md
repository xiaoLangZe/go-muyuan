# 常见用法

[English](Recipes) · **中文**

真实需求的可抄片段。

## 下一个文件并等它完成

```go
m := muyuan.New(muyuan.WithThreads(8), muyuan.WithMaxFiles(1))
defer m.Close()

task, err := m.AddTask("downloads", url)
if err != nil {
	return err
}
if err := task.Wait(); err != nil {
	return err
}
fmt.Println(task.FilePath())
```

## 下多个文件并等全部完成

```go
m := muyuan.New(muyuan.WithThreads(16), muyuan.WithMaxFiles(4),
	muyuan.WithDefaultDir("downloads"))
defer m.Close()

for _, url := range urls {
	if _, err := m.AddTask("", url); err != nil {
		return err
	}
}
m.Wait()

for _, task := range m.Tasks() {
	if err := task.Err(); err != nil {
		log.Printf("%s 失败: %v", task.URL(), err)
		continue
	}
	fmt.Println("已保存", task.FilePath())
}
```

## 带超时等待

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
defer cancel()

for _, task := range m.Tasks() {
	if err := task.WaitContext(ctx); err != nil {
		return err // 超时则是 context.DeadlineExceeded
	}
}
```

## 显示实时进度条

```go
for ev := range task.Events() {
	if ev.Type != muyuan.EventProgress {
		continue
	}
	p := ev.Progress
	filled := int(p.Percent / 2)
	fmt.Printf("\r[%s%s] %5.1f%%  %s/s  %d 条连接",
		strings.Repeat("=", filled), strings.Repeat(" ", 50-filled),
		p.Percent, human(p.Speed), p.Workers)
}
fmt.Println()
```

## 打印每个任务一行的总览

```go
for _, task := range m.Tasks() {
	p := task.Progress()
	fmt.Printf("%-28s %6.1f%%  %9s / %-9s  %s\n",
		filepath.Base(task.FilePath()), p.Percent,
		human(p.Downloaded), human(p.Total), task.Status())
}
```

## 失败后不丢进度地重试

```go
if task.Status() == muyuan.StatusFailed {
	// 服务端支持 Range 时从磁盘上已有的字节继续，不支持时从头下载
	if err := task.Resume(); err != nil {
		return err
	}
}
```

## 把带宽让给紧急的下载

```go
big.Pause()     // 它的连接立刻转给其他文件
urgent.Resume() // 有空闲时再拿回来
```

## 用满全部预算下一个文件

```go
// 没有别的任务竞争：这个文件独享所有连接
m := muyuan.New(muyuan.WithThreads(16), muyuan.WithMaxFiles(1))
```

## 一个一个慢慢过队列

```go
// 一次一个文件，各 3 条连接，按加入顺序
m := muyuan.New(muyuan.WithThreads(3), muyuan.WithMaxFiles(1))
for _, url := range urls {
	task, err := m.AddTask("downloads", url)
	if err != nil {
		return err
	}
	if err := task.Wait(); err != nil {
		return err
	}
}
```

## 用 token 鉴权

```go
task, err := m.AddTask(dir, url,
	muyuan.WithHeader("Authorization", "Bearer "+token))
```

## 自定义 User-Agent

```go
// 没有专门的选项，用请求头达到同样效果
task, err := m.AddTask(dir, url,
	muyuan.WithHeader("User-Agent", "my-service/1.0"))
```

## 走代理

```go
// 全部任务：
m := muyuan.New(muyuan.WithDefaultProxy("http://user:pass@proxy:8080"))

// 只有这条：
task, err := m.AddTask(dir, url, muyuan.WithProxy("socks5://user:pass@proxy:1080"))
```

## 按失败类型分支处理

```go
err := task.Wait()

switch {
case err == nil:
	// 完成

case errors.Is(err, muyuan.ErrBlockedHost):
	// URL 指向了下载器拒绝去的地方

case errors.Is(err, muyuan.ErrStalled):
	// 连接静默了；Resume 会从磁盘重试

case errors.Is(err, muyuan.ErrClosed):
	// 管理器被关闭了

default:
	var status *muyuan.HTTPStatusError
	if errors.As(err, &status) {
		fmt.Println("HTTP", status.StatusCode, "可重试:", status.Retryable())
	}
}
```

## 在测试里使用

```go
// httptest 服务器监听环回地址，默认被主机校验拒绝
m := muyuan.New(
	muyuan.WithThreads(4),
	muyuan.WithMaxFiles(2),
	muyuan.WithDefaultDir(t.TempDir()),
	muyuan.WithDefaultAllowUnsafeHosts(true),
)
defer m.Close()

task, err := m.AddTask(t.TempDir(), srv.URL+"/file.bin")
```

## 不限速，但别让下载永远挂着

```go
m := muyuan.New(
	muyuan.WithThreads(8),
	muyuan.WithDefaultStallTimeout(30*time.Second), // 30 秒没字节就重试
	muyuan.WithDefaultRetries(5),
	muyuan.WithDefaultRetryDelay(2*time.Second),
)
```

没有带宽限速；见[限制与路线](Limitations-and-Roadmap-zh)。

## 相关

- [API 参考](API-Reference-zh) —— 完整的接口面
- [快速开始](Getting-Started-zh) —— 这些片段的基础
