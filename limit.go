package downloader

import (
	"sync"
	"time"

	"github.com/xiaoLangZe/go-muyuan/internal/transfer"
)

// throttleWriter 包装 WriterAt，将全部调用方的合计写入速率限制在
// rate 字节/秒。每个下载器只有一个实例：该文件的所有连接共享
// 同一个令牌桶，因此速率限制是"整个文件"级别而非"每连接"级别。
//
// 令牌以时间为单位持续补充，桶容量为 1 秒的配额（允许瞬时突发
// 到 1 秒总量）。等待发生在 WriteAt 内：写入不会丢失字节，
// 只会延迟。
type throttleWriter struct {
	inner transfer.WriterAt

	mu     sync.Mutex
	rate   float64 // 字节/秒；0 表示直通
	tokens float64 // 令牌余额（字节）
	burst  float64 // 桶容量（字节）
	last   time.Time
}

// newThrottleWriter 构造限速写入器。rate<=0 时返回 nil，调用方
// 直接使用底层 WriterAt 以避免任何开销。
func newThrottleWriter(inner transfer.WriterAt, rate int64) *throttleWriter {
	if rate <= 0 {
		return nil
	}
	now := time.Now()
	return &throttleWriter{
		inner:  inner,
		rate:   float64(rate),
		tokens: float64(rate),
		burst:  float64(rate),
		last:   now,
	}
}

func (t *throttleWriter) WriteAt(p []byte, off int64) (int, error) {
	t.reserve(len(p))
	return t.inner.WriteAt(p, off)
}

// reserve 阻塞直到累积到 n 字节的令牌。等待在锁内进行：
// 锁竞争只发生在多个连接同时等待配额时，而此场景下各连接的
// 等待顺序无关紧要；简单正确优先于极致公平。
func (t *throttleWriter) reserve(n int) {
	if t == nil || n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.tokens += now.Sub(t.last).Seconds() * t.rate
	if t.tokens > t.burst {
		t.tokens = t.burst
	}
	if t.tokens >= float64(n) {
		t.tokens -= float64(n)
		t.last = now
		return
	}
	need := float64(n) - t.tokens
	wait := time.Duration(need / t.rate * float64(time.Second))
	time.Sleep(wait)
	t.tokens = 0
	t.last = time.Now()
}
