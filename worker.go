package downloader

import (
	"context"
	"sync"

	"github.com/xiaoLangZe/go-muyuan/internal/transfer"
)

// worker 是单个下载协程。它反复领取下一个未完成分片、下载其剩余
// 字节、并标记完成，直到没有分片剩余或 ctx 被取消。
//
// 当服务器不支持范围请求时，worker 改为执行一次整包顺序下载
// （该模式下只运行一个 worker）。
func (m *manager) worker(ctx context.Context, cancel context.CancelFunc) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		if !m.acceptRanges {
			// 顺序、一次性下载。不可续传。
			err := transfer.RetrySegment(ctx, func(c context.Context) error {
				return transfer.DownloadSequential(c, m.client, m.url, m.cfg.Headers, m.w,
					func(n int64) { m.addBytes(0, n) })
			})
			if err != nil {
				return err
			}
			m.finishSegment(0)
			return nil
		}

		idx, ok := m.claimSegment()
		if !ok {
			return nil // 无事可做
		}

		err := transfer.RetrySegment(ctx, func(c context.Context) error {
			// 每次尝试都重算恢复点，使部分失败后的重试从正确偏移
			// 续传，而非重写已持久化的字节。
			start, end, done := m.segmentBounds(idx)
			if done {
				return nil
			}
			return transfer.DownloadSegment(c, m.client, m.url, m.cfg.Headers, start, end, m.w,
				func(n int64) { m.addBytes(idx, n) })
		})
		if err != nil {
			m.releaseSegment(idx)
			if ctx.Err() != nil {
				return nil // 被取消：正常收尾
			}
			// 上报给 manager，由它取消其他 worker。
			cancel()
			return err
		}
		m.finishSegment(idx)
	}
}

// claimSegment 原子地标记并返回下一个未完成、未领取分片的索引。
// 无剩余时 ok 为 false。
func (m *manager) claimSegment() (idx int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.segs {
		c := &m.segs[i]
		if m.claimed[i] || c.Done() {
			continue
		}
		m.claimed[i] = true
		return i, true
	}
	return 0, false
}

// segmentBounds 返回分片 idx 仍需拉取的绝对 [start,end)，以及
// 它是否已完成。
func (m *manager) segmentBounds(idx int) (start, end int64, done bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx < 0 || idx >= len(m.segs) {
		return 0, 0, true
	}
	c := m.segs[idx]
	if c.Done() {
		return c.Start, c.End, true
	}
	return c.Start + c.Downloaded, c.End, false
}

// addBytes 将新写入的 n 字节计入分片 idx 和聚合计数器。
func (m *manager) addBytes(idx int, n int64) {
	if n <= 0 {
		return
	}
	m.mu.Lock()
	if idx >= 0 && idx < len(m.segs) {
		c := &m.segs[idx]
		c.Downloaded += n
		if c.Downloaded > c.Size() {
			c.Downloaded = c.Size()
		}
	}
	m.downloaded += n
	m.mu.Unlock()
}

// finishSegment 标记分片 idx 完成并释放其领取。
func (m *manager) finishSegment(idx int) {
	m.mu.Lock()
	if idx >= 0 && idx < len(m.segs) {
		c := &m.segs[idx]
		c.Downloaded = c.Size()
	}
	if idx >= 0 && idx < len(m.claimed) {
		m.claimed[idx] = false
	}
	m.mu.Unlock()
}

// releaseSegment 清除 idx 的领取，使其他 worker（或后续代际）
// 可以重试它。
func (m *manager) releaseSegment(idx int) {
	m.mu.Lock()
	if idx >= 0 && idx < len(m.claimed) {
		m.claimed[idx] = false
	}
	m.mu.Unlock()
}

// clearClaims 清除所有领取，在一个 worker 代际结束时使用。
func (m *manager) clearClaims() {
	m.mu.Lock()
	for i := range m.claimed {
		m.claimed[i] = false
	}
	m.mu.Unlock()
}

// allDone 报告是否每个分片都已完整下载。
func (m *manager) allDone() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.segs) == 0 {
		return false
	}
	for _, c := range m.segs {
		if !c.Done() {
			return false
		}
	}
	return true
}

// startGeneration 启动一个 worker 代际，并安排其结果到达 m.genDone。
// 若已有代际在运行则为 no-op。
func (m *manager) startGeneration() {
	if m.genRunning || m.exit {
		return
	}
	genCtx, cancel := context.WithCancel(m.parent)
	m.cancelGen = cancel
	m.genRunning = true

	connections := m.cfg.Connections
	if connections < 1 {
		connections = 1
	}
	if !m.acceptRanges {
		connections = 1
	}

	go func() {
		err := m.runWorkers(genCtx, cancel, connections)
		m.genDone <- err
	}()
}

// runWorkers 派生 n 个 worker 并等待它们全部返回。取消（用于
// pause/reconfigure/stop）被上报为 nil 错误，使 manager 能将它
// 与真实失败区分开。
func (m *manager) runWorkers(ctx context.Context, cancel context.CancelFunc, n int) error {
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.worker(ctx, cancel); err != nil {
				once.Do(func() { firstErr = err })
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	if ctx.Err() != nil {
		return nil // 被刻意取消
	}
	return nil
}
