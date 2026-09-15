package downloader

import (
	"context"
	"fmt"
	"sync"

	"github.com/xiaoLangZe/go-muyuan/internal/meta"
	"github.com/xiaoLangZe/go-muyuan/internal/plan"
)

// Downloader 驱动单文件下载。用 [New] 构造，再调用
// [Downloader.Start]。运行期间可通过 [Downloader.Signals] 通道或
// 等价的便利方法对下载执行暂停、恢复、重配（连接数和/或分片数）、
// 重启或清空。
//
// Downloader 可被多个协程并发安全地使用。
//
// Segment 和 Meta 通过 type alias 重新导出，供依赖 downloader.Segment /
// downloader.Meta 的现有调用方继续使用而无需改 import。
type Downloader struct {
	cfg   Config
	sigCh chan SignalEvent

	mu   sync.Mutex
	m    *manager
	done chan struct{}
}

// New 校验 cfg 并返回一个就绪待 [Downloader.Start] 的 Downloader。
// 除 URL 的 SSRF 主机检查外不执行网络 I/O，但会创建输出与
// part 目录。
func New(cfg Config) (*Downloader, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Downloader{
		cfg:   cfg,
		sigCh: make(chan SignalEvent, 16),
	}, nil
}

// Start 异步开始（或恢复）下载，并在服务器探测完成、plan 构建好之后
// 返回。它返回：
//
//   - ErrAlreadyRunning：下载当前正在运行或暂停；
//   - ErrInvalidConfig 包装的错误：URL 不可达/被拒绝；
//   - 其他探测错误。
//
// 所提供的 ctx 约束整个下载：取消它会中止传输。用
// [Downloader.Wait] 阻塞至完成。
func (d *Downloader) Start(ctx context.Context) error {
	d.mu.Lock()
	prev := d.m
	prevDone := d.done
	d.mu.Unlock()

	if prev != nil {
		if !prev.isFinished() {
			return ErrAlreadyRunning
		}
		// 等待上一个 manager 协程完全返回后再读取 d.cfg，
		// 因为它可能已修改过 cfg（SetConnections/SetSegments）。
		// 从已关闭的 done 通道接收即可建立该顺序。
		if prevDone != nil {
			<-prevDone
		}
	}

	client, err := d.cfg.effectiveClient()
	if err != nil {
		return err
	}

	m := newManager(&d.cfg, client, ctx, d.sigCh)
	if err := m.init(ctx); err != nil {
		return err
	}

	d.mu.Lock()
	d.m = m
	d.done = m.done
	d.mu.Unlock()

	go m.run()
	return nil
}

// Signals 返回用于接收控制信号的只发送通道。
// 发送 [SignalEvent] 等价于调用对应的便利方法，例如发送
// {Type: SigSetConnections, Connections: 8} 等价于
// [Downloader.SetConnections](8)。
//
// 该通道带缓冲，存活于 Downloader 的整个生命周期；在 Start 之前
// 发送也是安全的（信号在运行后处理）。
func (d *Downloader) Signals() chan<- SignalEvent { return d.sigCh }

// send 将信号入队而不阻塞调用方。
func (d *Downloader) send(sig SignalEvent) {
	select {
	case d.sigCh <- sig:
	default:
		// 缓冲已满：下载器未在消费信号。改由 helper 协程阻塞发送，
		// 确保不丢信号。
		go func() { d.sigCh <- sig }()
	}
}

// Pause 停止连接池，保留磁盘进度。暂停的下载可用
// [Downloader.Resume] 恢复。
func (d *Downloader) Pause() { d.send(SignalEvent{Type: SigPause}) }

// Resume 在 [Downloader.Pause] 之后重启连接池。
func (d *Downloader) Resume() { d.send(SignalEvent{Type: SigResume}) }

// SetConnections 修改用于该文件的并发 HTTP 连接数。
func (d *Downloader) SetConnections(n int) {
	d.send(SignalEvent{Type: SigSetConnections, Connections: n})
}

// SetSegments 修改文件被切分成的字节范围分片数。传 0 选择自动
// 分片数。已下载字节在重新切分中被保留。
func (d *Downloader) SetSegments(n int) {
	d.send(SignalEvent{Type: SigSetSegments, Segments: n})
}

// SetConnectionsAndSegments 在一次重配中同时修改两个旋钮。
func (d *Downloader) SetConnectionsAndSegments(connections, segments int) {
	d.send(SignalEvent{Type: SigSetConnectionsAndSegments, Connections: connections, Segments: segments})
}

// SetWorkers 设定"线程下载 + 切片下载"的总和上限，由下载器自动
// 分配 Connections 与 Segments。传 0 回退到分别设置模式
// （此时保留当前 Connections/Segments 不变）。这是推荐的高性能
// 下载模式：下载器会根据文件大小与服务器特性自动分配，并在
// 运行中检测慢分片、自动拆分以追求最高性能。
func (d *Downloader) SetWorkers(n int) {
	d.send(SignalEvent{Type: SigSetWorkers, Workers: n})
}

// Restart 删除所有进度，并立即以当前配置重新开始下载。
func (d *Downloader) Restart() { d.send(SignalEvent{Type: SigRestart}) }

// ClearCache 删除 partial 文件和元数据，使下载器保持空闲。
// 与 [Downloader.Restart] 不同，它不会重新开始下载；请再次调用
// [Downloader.Start] 以进行全新传输。无论下载是否正在运行，
// ClearCache 都有效。
func (d *Downloader) ClearCache() error {
	d.mu.Lock()
	m := d.m
	d.mu.Unlock()
	if m != nil && !m.isFinished() {
		d.send(SignalEvent{Type: SigClearCache})
		return nil
	}
	// 未运行：直接删除。
	return meta.DeleteAndPartial(d.cfg.OutputPath)
}

// Stop 停止下载，保留进度以备后续 [Downloader.Start]。
func (d *Downloader) Stop() { d.send(SignalEvent{Type: SigStop}) }

// Abort 立即取消下载；随后 [Downloader.Wait] 返回 ErrAborted。
// 磁盘上的进度保留。
func (d *Downloader) Abort() { d.send(SignalEvent{Type: SigAbort}) }

// Progress 返回当前下载状态的快照。任何时刻都可安全调用，
// 包括 Start 之前（返回一个空闲快照）。
func (d *Downloader) Progress() Progress {
	d.mu.Lock()
	m := d.m
	d.mu.Unlock()
	if m == nil {
		return Progress{
			URL:          d.cfg.URL,
			OutputPath:   d.cfg.OutputPath,
			Connections:  d.cfg.Connections,
			Segments:     d.cfg.Segments,
			State:        StateIdle,
			AcceptRanges: true,
		}
	}
	return m.Snapshot()
}

// Wait 阻塞至当前下载到达终态，并返回终态错误（成功为 nil，
// Abort 后为 ErrAborted）。
func (d *Downloader) Wait() error {
	d.mu.Lock()
	m := d.m
	done := d.done
	d.mu.Unlock()
	if m == nil || done == nil {
		return nil
	}
	<-done
	return m.finalErr()
}

// Close 中止任何进行中的下载、等待其停止、并释放资源。
// Close 之后不应再复用该 Downloader。
func (d *Downloader) Close() error {
	d.Abort()
	d.mu.Lock()
	done := d.done
	m := d.m
	d.mu.Unlock()
	if done != nil {
		<-done
	}
	if m != nil {
		return m.finalErr()
	}
	return nil
}

// String 渲染配置用于调试。
func (d *Downloader) String() string {
	return fmt.Sprintf("downloader{url=%q out=%q connections=%d segments=%d}",
		d.cfg.URL, d.cfg.OutputPath, d.cfg.Connections, d.cfg.Segments)
}

// Segment 是 internal/plan.Segment 的类型别名，保留作
// downloader.Segment 的向后兼容引用。它描述文件的一段连续字节
// 范围及其已写入字节数。
type Segment = plan.Segment

// Meta 是 internal/meta.Meta 的类型别名，保留作 downloader.Meta 的
// 向后兼容引用。它描述下载进度的磁盘附属元数据。
type Meta = meta.Meta
