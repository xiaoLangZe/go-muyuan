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
//
// 长等待被切成小片并在每片检查取消信号：即使速率极低，Abort/
// Pause/Stop 也能在约一个切片（50ms）内打断等待，不会把调用方
// 卡在分钟甚至小时级的单次睡眠里。
type throttleWriter struct {
	inner transfer.WriterAt
	done  <-chan struct{}

	mu     sync.Mutex
	rate   float64 // 字节/秒；0 表示直通
	tokens float64 // 令牌余额（字节）
	burst  float64 // 桶容量（字节）
	last   time.Time
}

// reserveSlice 是分段等待时每片的最大时长，同时约束了取消的最坏
// 响应延迟。
const reserveSlice = 50 * time.Millisecond

// newThrottleWriter 构造限速写入器。rate<=0 时返回 nil，调用方
// 直接使用底层 WriterAt 以避免任何开销。done 是"停止限速"信号：
// 收到后仍在等待的写入会立即放行，剩余限速交给外层 ctx 处理。
func newThrottleWriter(inner transfer.WriterAt, rate int64, done <-chan struct{}) *throttleWriter {
	if rate <= 0 {
		return nil
	}
	now := time.Now()
	return &throttleWriter{
		inner:  inner,
		done:   done,
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

// reserve 阻塞直到累积到 n 字节的令牌。每片顶部按真实时间记账，
// 因此等待期间生成的令牌计算准确、不限速为两倍于设定值；每片
// 之间检查取消信号，取消到来时立刻放行本次写入（代际即将结束，
// 是否写出由外层 ctx 决定）。
func (t *throttleWriter) reserve(n int) {
	if t == nil || n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	for {
		now := time.Now()
		t.tokens += now.Sub(t.last).Seconds() * t.rate
		if t.tokens > t.burst {
			t.tokens = t.burst
		}
		t.last = now
		if t.tokens >= float64(n) {
			t.tokens -= float64(n)
			return
		}
		// 令牌不足：睡一小片后重新记账。切片同时把取消的
		// 最坏响应延迟约束在约一个切片时长。
		select {
		case <-t.done:
			t.tokens = t.burst // 不再记账，放行本次写入
			return
		case <-time.After(reserveSlice):
		}
	}
}
