# go-muyuan（木鸢）

[![Go Reference](https://pkg.go.dev/badge/github.com/xiaoLangZe/go-muyuan/zh-CN.svg)](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan/zh-CN)
[![Go Version](https://img.shields.io/badge/go-1.26-blue.svg)](https://go.dev/)

[English](../README.md) | **中文**

📖 **[文档站](https://xiaoLangZe.github.io/go-muyuan/zh-CN/)** ·
📦 [pkg.go.dev](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan/zh-CN) ·
🐙 [GitHub](https://github.com/xiaoLangZe/go-muyuan)

本项目中文名为 **木鸢**，模块路径里的 `muyuan` 即其拼音。

一个 golang 高速下载库，以**可复用的 Go 库**形式提供。它把文件切成若干字节区间
（*切片 / segment*），用一组*连接*并发下载；并且在**运行中**即可重配置、暂停、
继续、重下和清缓存。另有任务队列，以有界*并发*批量下载多个文件。

这是一个供其他程序 import 的库，不是命令行工具。

## 设计要点

传统多线程下载器把每个分片写到独立的 `.partN` 文件，最后拼接。本库不同：
所有切片共享**同一个预分配的 partial 文件**，每条连接写到该段的**绝对偏移**。
于是完成时只需 `fsync` + `rename`（没有合并步骤），而改切片数是纯元数据操作，
不搬运任何字节。

## 特性

- **多连接区间下载** —— N 段切片由 T 条并发连接抓取，两个旋钮相互独立。
- **Workers 总和模式** —— 只设一个数，库自动分配连接与切片，并在运行中拆分慢分片。推荐用于最高性能。
- **运行时热重配** —— 下载过程中改 Workers、连接、切片，已下载字节自动保留。
- **批量下载队列** —— 有界并发下载多个文件，可单任务控制也可整体控制。
- **跨进程续传** —— 进度持久化，并与 `Content-Length` / `ETag` / `Last-Modified` 校验。
- **优雅降级** —— 不支持 `Accept-Ranges` 的服务端退化为单流下载，不直接失败。
- **重试** —— 瞬时故障退避重试；永久故障（404、403）立即失败。
- **SSRF 防护** —— 仅 http/https，校验与拨号**双重**拒绝内网地址。

## 两个模块

本仓库提供两个**互相独立**的 Go 模块，注释语言不同，按需选择一个即可：

| 模块 | 导入路径 | 说明 |
| --- | --- | --- |
| 英文版 | `github.com/xiaoLangZe/go-muyuan` | 根模块 |
| 简体中文版 | `github.com/xiaoLangZe/go-muyuan/zh-CN` | 独立模块，自带 `internal/`，不依赖英文版 |

两者互不引用：导入英文版不会拉入 `zh-CN`，导入中文版也不会拉入英文版。

## 安装（简体中文版）

要求 **Go 1.26 或更高版本**。仓库发布后：

```bash
go get github.com/xiaoLangZe/go-muyuan/zh-CN
```

远端尚不存在时，先用 `replace` 指向本地目录：

```text
require github.com/xiaoLangZe/go-muyuan/zh-CN v0.0.0

replace github.com/xiaoLangZe/go-muyuan/zh-CN => /绝对路径/go-muyuan/zh-CN
```

包名是 `downloader`，与模块路径最后一段不一致，所以加别名更清楚：

```go
import downloader "github.com/xiaoLangZe/go-muyuan/zh-CN"
```

要改用英文版，把上面所有路径末尾的 `/zh-CN` 去掉即可。

## 快速开始

```go
d, err := downloader.New(downloader.Config{
	URL:         "https://example.com/big.iso",
	OutputPath:  "./big.iso",
	Connections: 8,  // 该文件的并发连接数
	Segments:    16, // 字节区间切片数（0 = 自动）
	OnProgress: func(p downloader.Progress) {
		fmt.Printf("\r%.1f%%  %.1f MiB/s", p.Percent, p.Speed/1024/1024)
	},
})
if err != nil {
	log.Fatal(err)
}

if err := d.Start(context.Background()); err != nil {
	log.Fatal(err)
}
if err := d.Wait(); err != nil {
	log.Fatal(err)
}
```

## 文档

完整用户手册在 **[wiki](../wiki/)**，发布地址
<https://xiaoLangZe.github.io/go-muyuan/>。内容包括术语、四种配置模式、
运行时控制、批量队列、进度上报、安全防护与内部架构。

- [简介](../wiki/guide/introduction.md) —— 核心理念与术语
- [快速上手](../wiki/guide/getting-started.md) —— 安装与第一个下载
- [配置选项](../wiki/guide/configuration.md) —— 全字段与四种模式
- [运行时控制](../wiki/guide/runtime-control.md) —— 信号与方法
- [批量队列](../wiki/guide/queue.md) —— 一次下载多个文件
- [架构设计](../wiki/guide/architecture.md) —— 内部实现原理

## 示例

- [`examples/basic`](../examples/basic/main.go) —— 交互式单文件 CLI，带实时进度条。
- [`examples/batch`](../examples/batch/main.go) —— 使用任务队列的批量下载器。
- [`examples/testsrv`](../examples/testsrv/main.go) —— 本地支持 Range 的文件服务器。

```bash
# 终端 1
go run ./examples/testsrv -dir ./testdata -addr 127.0.0.1:18080

# 终端 2（回环地址需要 allow-private）
go run ./examples/basic -allow-private -o ./file.bin -conn 8 -seg 16 \
    http://127.0.0.1:18080/file.bin
```

## 测试

```bash
go test ./...
```

## 许可证

MIT —— 见 [LICENSE](../LICENSE)。
