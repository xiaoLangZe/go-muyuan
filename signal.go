package downloader

import "fmt"

// SignalType 标识发送给运行中下载器的控制信号。
type SignalType int

const (
	// SigPause 取消当前连接代际但不重新派生；进行中的分片进度
	// 保留在磁盘上，使 Resume 从各分片停止处继续。
	SigPause SignalType = iota + 1
	// SigResume 在 Pause 之后以当前配置重新派生连接池。
	SigResume
	// SigSetConnections 修改运行中下载的连接数。
	SigSetConnections
	// SigSetSegments 将文件重新切分为指定数量的分片。
	SigSetSegments
	// SigSetConnectionsAndSegments 在一次重配中同时修改连接数和
	// 分片数。
	SigSetConnectionsAndSegments
	// SigSetWorkers 设定"线程下载 + 切片下载"总和，由下载器
	// 自动分配 Connections 与 Segments。0 回退到分别设置模式。
	SigSetWorkers
	// SigRestart 删除所有进度（partial 文件 + 元数据），并立即
	// 以当前配置重新开始下载。
	SigRestart
	// SigClearCache 删除所有进度，使下载器保持空闲；
	// 调用方必须再次调用 Start 以开始全新下载。
	SigClearCache
	// SigStop 取消连接，保留进度，并转入 Stopped 状态。
	// 之后可用 Start 恢复下载。
	SigStop
	// SigAbort 取消一切并释放资源；Wait 返回 ErrAborted。
	// 磁盘上的进度保留。
	SigAbort
)

// SignalEvent 是发送给运行中下载器的控制消息，通过
// [Downloader.Signals] 或某个便利方法
// （[Downloader.Pause]、[Downloader.SetConnections] 等）发送。
//
// 字段按 Type 解释：
//
//   - SigSetConnections           -> Connections 为新连接数
//   - SigSetSegments              -> Segments 为新分片数（0 = 自动）
//   - SigSetConnectionsAndSegments -> Connections 和 Segments 同时生效
//   - SigSetWorkers                -> Workers 为新总和（0 = 回退到分别设置）
//   - SigRestart                  -> Connections/Segments/Workers 非零时在
//     重新开始前更新配置（0 表示"保持当前"）
//
// 其余信号类型忽略 payload。
type SignalEvent struct {
	Type        SignalType
	Connections int
	Segments    int
	Workers     int
}

// String 返回信号的人类可读描述，用于日志与调试。
func (s SignalEvent) String() string {
	switch s.Type {
	case SigPause:
		return "pause"
	case SigResume:
		return "resume"
	case SigSetConnections:
		return fmt.Sprintf("set-connections(%d)", s.Connections)
	case SigSetSegments:
		return fmt.Sprintf("set-segments(%d)", s.Segments)
	case SigSetConnectionsAndSegments:
		return fmt.Sprintf("set-connections-and-segments(%d,%d)", s.Connections, s.Segments)
	case SigSetWorkers:
		return fmt.Sprintf("set-workers(%d)", s.Workers)
	case SigRestart:
		return "restart"
	case SigClearCache:
		return "clear-cache"
	case SigStop:
		return "stop"
	case SigAbort:
		return "abort"
	default:
		return fmt.Sprintf("signal(%d)", s.Type)
	}
}

// State 是 [Downloader] 的生命周期状态。
type State int

const (
	// StateIdle 是初始状态：尚未启动。
	StateIdle State = iota
	// StateRunning：连接正在下载。
	StateRunning
	// StatePaused：连接已停止但进度保留；可 Resume。
	StatePaused
	// StateCompleted：文件已完整下载并改名完成。
	StateCompleted
	// StateFailed：下载因错误结束。
	StateFailed
	// StateStopped：下载被调用方停止；进度保留，可用 Start 恢复。
	StateStopped
)

// String 返回状态的小写英文名，用于日志与进度显示。
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateRunning:
		return "running"
	case StatePaused:
		return "paused"
	case StateCompleted:
		return "completed"
	case StateFailed:
		return "failed"
	case StateStopped:
		return "stopped"
	default:
		return fmt.Sprintf("state(%d)", int(s))
	}
}

// IsTerminal 报告状态是否为终态（Completed/Failed），因此除了
// Restart 或 ClearCache 之外无法继续驱动。
func (s State) IsTerminal() bool {
	return s == StateCompleted || s == StateFailed
}
