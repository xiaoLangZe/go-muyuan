package downloader

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/xiaoLangZe/go-muyuan/internal/plan"
	"github.com/xiaoLangZe/go-muyuan/internal/security"
)

// 默认可调参数与边界值。
const (
	defaultConnections    = 8
	defaultMinSegmentSize = 1 << 20 // 1 MiB；分片更小会造成浪费
	maxConnections        = 256
	maxSegments           = 4096
	maxWorkers            = 256
)

// defaultMinSegmentSize 注：自动分片的最小尺寸下限由 internal/plan 包导出。
// ProgressFunc 是运行中的下载用于上报进度快照的回调函数。
// 它不得阻塞；耗时工作应派发到别处执行。该回调由下载器的进度
// ticker 协程调用，而非连接（worker）协程。
type ProgressFunc func(Progress)

// Config 描述一次下载。零值不可用；请通过 [New] 构造，后者会调用
// [Config.Validate]。
type Config struct {
	// URL 是待下载的地址。必须是 http 或 https。除非设置
	// AllowPrivateHost，否则会校验 localhost/loopback/私有/保留地址。
	URL string

	// OutputPath 是最终目标路径。partial 文件和元数据会写在它
	// 旁边（除非设置了 PartDir），带有 ".muyuan" 后缀，完成后会被
	// 重命名到此处。
	OutputPath string

	// Connections 是并发拉取该文件的 HTTP 连接数。每个连接同一时刻
	// 只处理一个分片。0 表示默认值（8）。取值范围为 [1, 256]。
	//
	// Connections 与 Segments 相互独立：8 个连接拉取 32 个分片
	// 意味着每个连接领取一个分片、完成后领取下一个。
	//
	// 当设置了 Workers（非零）时，Connections 和 Segments 由
	// Workers 总和自动分配，单独设置的值会被覆盖。
	Connections int

	// Segments 是文件被切分成的字节范围分片数量。0 表示自动：
	// 大致每个连接一个分片，且不小于 MinSegmentSize。取值范围
	// 为 [0, 4096]。
	//
	// 当设置了 Workers（非零）时，Connections 和 Segments 由
	// Workers 总和自动分配，单独设置的值会被覆盖。
	Segments int

	// Workers 是"线程下载 + 切片下载"的总和上限。设为非零值时，
	// 下载器会根据文件大小与服务器特性自动分配 Connections
	// （并发连接数）和 Segments（切片数），两者之和不超过 Workers。
	// 优先保证 Connections 足够以打开足够的并行流，剩余配额
	// 全部用于 Segments，使每个连接都能立即领取分片、不会有
	// 空闲 worker。
	//
	// 取值范围 [0, 256]。0 表示禁用总和模式，回退到分别设置
	// Connections/Segments 的传统模式（二者均保留可用）。
	Workers int

	// PartDir 覆盖 partial 文件 + 元数据的存放目录。为空表示
	// OutputPath 所在目录。
	PartDir string

	// Headers 是额外的请求头（如 Authorization）。
	Headers http.Header

	// HTTPClient 覆盖默认客户端。为 nil 时，会构造一个具备合理
	// 超时和可选代理的客户端。
	HTTPClient *http.Client

	// Proxy 是可选的代理 URL，仅在 HTTPClient 为 nil 时生效。
	Proxy string

	// OnProgress 非空时，会周期性地以 Progress 快照被调用。
	OnProgress ProgressFunc

	// MinSegmentSize 是自动分片尺寸的下限。0 表示 1 MiB。
	MinSegmentSize int

	// AllowPrivateHost 允许 URL 指向 localhost、loopback、
	// 私有（RFC1918）以及其他保留地址段。默认关闭以作为 SSRF
	// 防护；仅当确实需要从可信 LAN 下载时开启。
	AllowPrivateHost bool
}

// Validate 归一化默认值并校验必填字段。它会被 [New] 调用；调用方
// 也可直接调用它，以便在启动下载前暴露配置错误。
func (c *Config) Validate() error {
	if c.URL == "" {
		return fmt.Errorf("%w: empty URL", ErrInvalidConfig)
	}
	if c.OutputPath == "" {
		return fmt.Errorf("%w: empty OutputPath", ErrInvalidConfig)
	}
	if !filepath.IsAbs(c.OutputPath) {
		abs, err := filepath.Abs(c.OutputPath)
		if err != nil {
			return fmt.Errorf("%w: cannot resolve OutputPath: %v", ErrInvalidConfig, err)
		}
		c.OutputPath = abs
	}
	if c.PartDir == "" {
		c.PartDir = filepath.Dir(c.OutputPath)
	} else if !filepath.IsAbs(c.PartDir) {
		abs, err := filepath.Abs(c.PartDir)
		if err != nil {
			return fmt.Errorf("%w: cannot resolve PartDir: %v", ErrInvalidConfig, err)
		}
		c.PartDir = abs
	}
	// 尽早创建输出目录，使后续失败的报错更清晰。
	if err := os.MkdirAll(filepath.Dir(c.OutputPath), 0o700); err != nil {
		return fmt.Errorf("%w: cannot create output dir: %v", ErrInvalidConfig, err)
	}
	if err := os.MkdirAll(c.PartDir, 0o700); err != nil {
		return fmt.Errorf("%w: cannot create part dir: %v", ErrInvalidConfig, err)
	}

	if c.Workers < 0 || c.Workers > maxWorkers {
		return fmt.Errorf("%w: Workers %d out of range [0,%d]",
			ErrInvalidConfig, c.Workers, maxWorkers)
	}
	if c.Workers > 0 {
		// 总和模式：由 Workers 自动分配 Connections 与 Segments。
		// 优先保证足够多的并发连接以打开并行流；剩余配额全部
		// 用于切片，使每个连接都有分片可领。
		allocateWorkers(c)
	}
	if c.Connections == 0 {
		c.Connections = defaultConnections
	}
	if c.Connections < 1 || c.Connections > maxConnections {
		return fmt.Errorf("%w: Connections %d out of range [1,%d]",
			ErrInvalidConfig, c.Connections, maxConnections)
	}
	if c.Segments < 0 || c.Segments > maxSegments {
		return fmt.Errorf("%w: Segments %d out of range [0,%d]",
			ErrInvalidConfig, c.Segments, maxSegments)
	}
	if c.MinSegmentSize == 0 {
		c.MinSegmentSize = defaultMinSegmentSize
	}
	if c.MinSegmentSize < plan.DefaultMinSegmentSize/64 { // 16 KiB 下限：更小的 Range 请求没有意义
		c.MinSegmentSize = plan.DefaultMinSegmentSize / 64
	}

	// 预校验 URL/主机，让调用方在 New 时而非 Start 时就知道
	// SSRF/拼写错误。
	if err := security.ValidateURL(c.URL, c.AllowPrivateHost); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if c.Proxy != "" {
		if err := security.ValidateProxyURL(c.Proxy); err != nil {
			return fmt.Errorf("%w: invalid proxy: %v", ErrInvalidConfig, err)
		}
	}
	return nil
}

// allocateWorkers 由 Workers 总和自动分配 Connections 与 Segments。
//
// 策略：把 Workers 的一部分用作并发连接数（Connections），其余
// 全部用作切片数（Segments），使 Connections+Segments <= Workers。
// Connections 取 max(2, Workers/3)，保证至少 2 条并行流；若文件
// 太小无法支撑这么多分片，Segments 会在 plan.AutoSegmentCount
// 里被自然限制。
func allocateWorkers(c *Config) {
	w := c.Workers
	if w < 2 {
		w = 2
	}
	// 三分之一用作并发连接，但不少于 2、不超过 64。
	conns := w / 3
	if conns < 2 {
		conns = 2
	}
	if conns > 64 {
		conns = 64
	}
	if conns > w-1 {
		conns = w - 1 // 至少留 1 个配额给切片
	}
	c.Connections = conns
	c.Segments = 0 // 0 = 自动：planFor 会按连接数推算切片数，受 Workers 总和约束
}

// effectiveClient 返回要使用的 HTTP 客户端；当 Config.HTTPClient 为 nil 时，
// 构造一个带合理超时和可选代理的默认客户端。
//
// 默认 transport 接入了拨号时 SSRF 防护（见 guardedDial），因此即使
// DNS 重绑定也无法到达私有地址，除非设置 AllowPrivateHost。
func (c *Config) effectiveClient() (*http.Client, error) {
	if c.HTTPClient != nil {
		return c.HTTPClient, nil
	}
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	tr := &http.Transport{
		// Range 请求受益于 keep-alive，每个连接不必重新握手。
		DialContext:           security.GuardedDial(c.AllowPrivateHost, dialer),
		MaxIdleConns:          c.Connections*2 + 8,
		MaxIdleConnsPerHost:   c.Connections*2 + 8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if c.Proxy != "" {
		if err := setProxy(tr, c.Proxy); err != nil {
			return nil, err
		}
	}
	return &http.Client{
		Transport: tr,
		// 每请求的取消由 context 驱动；硬性的 client.Timeout
		// 会过早杀死长 Range 下载。
		Timeout: 0,
	}, nil
}
