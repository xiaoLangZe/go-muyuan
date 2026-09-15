// Package downloader 实现了一个 golang 高速下载库：多连接、可续传、
// 运行时可重配的文件下载器。它是一个可复用库：调用方 import
// "github.com/xiaoLangZe/go-muyuan/zh-CN" 后，通过 [Downloader] 类型驱动一次下载。
//
// 下载会将文件切分为字节范围分片，并通过一个连接池下载它们。分片数
// （切多少块）和连接数（同时拉多少块）相互独立，并可通过类型化信号
// 在下载运行期间重新配置；也可设置 Workers 总和，由下载器自动分配
// 两者，并在运行中检测慢分片、自动拆分以追求最高性能。
//
// 不支持范围请求的服务器不算错误：下载会退化为单流顺序下载，
// 通过 [Progress.AcceptRanges] == false 来体现。
package downloader

import "errors"

// 包返回的哨兵错误。
var (
	// ErrInvalidConfig 在 [New] 发现所提供的 [Config] 缺少必填字段
	// 或取值越界时返回。
	ErrInvalidConfig = errors.New("downloader: invalid config")
	// ErrAlreadyRunning 在 [Downloader.Start] 发现下载已在运行或
	// 暂停时返回。
	ErrAlreadyRunning = errors.New("downloader: already running")
	// ErrAborted 在下载被调用方在完成前中止时，从 [Downloader.Wait]
	// 返回。
	ErrAborted = errors.New("downloader: aborted")
)
