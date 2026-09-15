package downloader

import (
	"sync"
	"time"
)

// Progress 是下载在某一时刻状态的不可变快照。
type Progress struct {
	// URL 是来源 URL。
	URL string
	// OutputPath 是最终目标路径。
	OutputPath string
	// Total 是总字节数；服务器未上报长度时为 0。
	Total int64
	// Downloaded 是已写入 partial 文件的字节数。
	Downloaded int64
	// Percent 是 Downloaded/Total*100；Total 未知时为 0。
	Percent float64
	// Speed 是当前传输速率，单位字节/秒（EWMA）。
	Speed float64
	// ETA 是预计剩余时间；未知时为 0。
	ETA time.Duration
	// Connections 是已配置的连接数。
	Connections int
	// Segments 是已配置的分片数。
	Segments int
	// Workers 是"线程下载 + 切片下载"的总和上限；0 表示
	// 当前使用分别设置模式（看 Connections/Segments）。
	Workers int
	// AcceptRanges 报告服务器是否支持范围请求。为 false 时
	// 下载以单流方式进行，且不可续传。
	AcceptRanges bool
	// State 是快照时刻的生命周期状态。
	State State
	// Err 在 State 为 StateFailed 时非 nil。
	Err error
}

// speedMeter 从周期性采样计算平滑后的传输速率。
type speedMeter struct {
	mu       sync.Mutex
	last     time.Time
	lastByte int64
	ewma     float64
	primed   bool
}

// sample 在 now 时刻记录累计字节数并返回更新后的平滑速率
// （字节/秒）。第一个采样仅用于初始化计量器。
func (s *speedMeter) sample(now time.Time, bytes int64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.primed {
		s.last = now
		s.lastByte = bytes
		s.primed = true
		return 0
	}
	dt := now.Sub(s.last).Seconds()
	if dt <= 0 {
		return s.ewma
	}
	db := float64(bytes - s.lastByte)
	inst := db / dt
	const alpha = 0.4
	if s.ewma == 0 {
		s.ewma = inst
	} else {
		s.ewma = alpha*inst + (1-alpha)*s.ewma
	}
	s.last = now
	s.lastByte = bytes
	if s.ewma < 0 {
		s.ewma = 0
	}
	return s.ewma
}

// reset 清空计量器，使新下载不继承陈旧的采样。
func (s *speedMeter) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.primed = false
	s.ewma = 0
	s.lastByte = 0
}
