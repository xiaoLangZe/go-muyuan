package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/xiaoLangZe/go-muyuan/zh-CN/internal/meta"
	"github.com/xiaoLangZe/go-muyuan/zh-CN/internal/plan"
	"github.com/xiaoLangZe/go-muyuan/zh-CN/internal/transfer"
)

// manager 的周期性工作时序。
const (
	progressInterval = 500 * time.Millisecond
	metaSaveInterval = 3 * time.Second
	// splitInterval 是慢分片检测与拆分的节拍。
	splitInterval = 2 * time.Second
)

// manager 拥有一次下载的生命周期：探测服务器、构建分片 plan、
// 运行 worker 代际、并响应控制信号。恰好一个 manager 协程（run）
// 修改生命周期字段；worker 通过互斥锁保护的 plan 和计数器通信。
//
// "manager 协程拥有控制权"与"worker 拥有字节"的分离，使运行时
// 重配安全：重配会取消当前 worker 代际、等待每个 worker 返回、
// 之后再重建 plan。没有 worker 会观察到 plan 在它之下被改写。
type manager struct {
	cfg    *Config
	client *http.Client
	url    string
	out    string
	parent context.Context

	sigCh      chan SignalEvent
	onProgress ProgressFunc

	mu           sync.Mutex // 保护以下字段
	state        State
	finErr       error
	segs         []plan.Segment
	claimed      []bool
	total        int64
	acceptRanges bool
	etag         string
	lastModified string
	downloaded   int64
	segmentCount int

	// 慢分片拆分簿记（仅 manager 协程）
	segLastBytes map[int]int64 // 分片 idx -> 上次采样时的累计字节
	segLastTime  map[int]time.Time
	lastSplit    time.Time

	// 代际簿记（仅 manager 协程）
	genRunning bool
	cancelGen  context.CancelFunc
	genDone    chan error
	afterGen   func() // 当前代际排空后要执行的动作
	exit       bool
	lastMeta   time.Time
	abortErr   error

	partial *os.File
	meter   speedMeter

	done     chan struct{} // run 返回时关闭
	finished bool          // 在关闭 done 前于 mu 下置位
}

// newManager 为单次下载运行构造一个 manager。
func newManager(cfg *Config, client *http.Client, parent context.Context, sigCh chan SignalEvent) *manager {
	m := &manager{
		cfg:          cfg,
		client:       client,
		url:          cfg.URL,
		out:          cfg.OutputPath,
		parent:       parent,
		sigCh:        sigCh,
		onProgress:   cfg.OnProgress,
		state:        StateIdle,
		genDone:      make(chan error, 1),
		done:         make(chan struct{}),
		segLastBytes: make(map[int]int64),
		segLastTime:  make(map[int]time.Time),
	}
	return m
}

// init 探测服务器、对账已有进度、打开（并预分配）partial 文件，
// 并构建初始分片 plan。它同步运行于 Start 内，使调用方立即得到
// 探测/校验错误。
func (m *manager) init(ctx context.Context) error {
	pr, err := transfer.Probe(ctx, m.client, m.url, m.cfg.Headers)
	if err != nil {
		return err
	}
	m.total = pr.Total
	m.acceptRanges = pr.AcceptRanges
	m.etag = pr.ETag
	m.lastModified = pr.LastModified

	// 未知长度无法安全切分或校验，故按单顺序、不可续传下载处理。
	if m.total <= 0 {
		m.acceptRanges = false
	}

	existing, ok, err := meta.Load(m.out)
	if err != nil {
		return err
	}
	if !ok {
		existing = nil
	}

	// 不支持 range 的服务器无法续传；丢弃陈旧进度。
	if !m.acceptRanges && existing != nil {
		_ = meta.DeleteAndPartial(m.out)
		existing = nil
	}

	// 远端资源变更时拒绝续传。
	if existing != nil && !m.resumeCompatible(existing) {
		_ = meta.DeleteAndPartial(m.out)
		existing = nil
	}

	f, err := os.OpenFile(meta.PartialPath(m.out), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open partial: %w", err)
	}
	if m.total > 0 {
		if err := f.Truncate(m.total); err != nil {
			f.Close()
			return fmt.Errorf("preallocate: %w", err)
		}
	}
	m.partial = f

	m.segs, m.segmentCount = m.planFor(existing)

	m.claimed = make([]bool, len(m.segs))
	m.downloaded = plan.SumDownloaded(m.segs)
	m.meter.reset()

	m.mu.Lock()
	m.state = StateRunning
	m.mu.Unlock()
	return nil
}

// resumeCompatible 报告记录的进度能否对当前探测到的远端资源续传。
func (m *manager) resumeCompatible(mm *meta.Meta) bool {
	if m.total > 0 && mm.TotalSize > 0 && mm.TotalSize != m.total {
		return false
	}
	if mm.ETag != "" && m.etag != "" && mm.ETag != m.etag {
		return false
	}
	if mm.LastModified != "" && m.lastModified != "" && mm.LastModified != m.lastModified {
		return false
	}
	return true
}

// planFor 生成分片 plan：当记录的进度与配置的切片数兼容时复用，
// 否则重新切分。
func (m *manager) planFor(existing *meta.Meta) ([]plan.Segment, int) {
	// 非 range（或未知长度）的下载是单个顺序分片：
	// 切分无意义，且这类传输无论如何不可续传。
	if !m.acceptRanges {
		if m.total > 0 {
			return plan.BuildPlan(m.total, 1), 1
		}
		return plan.BuildPlan(0, 1), 1
	}

	configured := m.cfg.Segments
	if configured == 0 {
		configured = plan.AutoSegmentCount(m.total, m.cfg.Connections, m.cfg.MinSegmentSize)
	}

	if existing != nil && len(existing.Segments) > 0 {
		// 若调用方显式要求不同的切片数，则将记录的进度
		// 重新切分到新边界上。
		if m.cfg.Segments > 0 && len(existing.Segments) != configured {
			return plan.Reinterleave(existing.Segments, m.total, configured), configured
		}
		return existing.Segments, len(existing.Segments)
	}
	return plan.BuildPlan(m.total, configured), configured
}

// run 是 manager 的主循环。当下载到达终态（completed、failed、
// stopped、cleared）或 parent context 被取消时退出。
func (m *manager) run() {
	defer close(m.done)
	defer func() {
		if m.partial != nil {
			_ = m.partial.Sync()
			_ = m.partial.Close()
			m.partial = nil
		}
	}()

	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()

	m.startGeneration()

	for !m.exit {
		select {
		case <-m.parent.Done():
			if m.abortErr == nil {
				m.abortErr = m.parent.Err()
			}
			m.finish(StateFailed, m.abortErr)
			m.exit = true
		case sig := <-m.sigCh:
			m.handleSignal(sig)
		case err := <-m.genDone:
			m.onGenerationEnd(err)
		case <-ticker.C:
			m.tick()
		}
	}

	m.teardown()
	m.finishIfNeeded()
}

// finishIfNeeded 在某条退出路径未设置状态时标记下载完成，
// 使 Wait 在 run 返回后绝不阻塞。
func (m *manager) finishIfNeeded() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished {
		return
	}
	m.finished = true
	if m.state == StateRunning || m.state == StateIdle || m.state == StatePaused {
		m.state = StateStopped
	}
}

// teardown 取消任何进行中的代际、等待其排空、保存最终进度并上报。
func (m *manager) teardown() {
	if m.genRunning {
		if m.cancelGen != nil {
			m.cancelGen()
		}
		<-m.genDone
		m.genRunning = false
		m.cancelGen = nil
	}
	m.clearClaims()
	m.saveMeta(true)
	m.report()
}

// finish 设置终态和最终错误。
func (m *manager) finish(st State, err error) {
	m.mu.Lock()
	m.state = st
	m.finErr = err
	m.finished = true
	m.mu.Unlock()
}

// onGenerationEnd 处理 worker 代际自行结束或因取消而结束的情况。
// 待处理的 afterGen 动作（pause/reconfigure/restart/stop）先执行；
// 否则评估下载是否完成。
func (m *manager) onGenerationEnd(err error) {
	m.genRunning = false
	m.cancelGen = nil
	m.clearClaims()
	m.saveMeta(true)

	// 真实 worker 错误导致下载失败。刻意的取消被 runWorkers 上报
	// 为 nil，因此此处任何非 nil 错误都是真实的。
	if err != nil {
		m.mu.Lock()
		m.finErr = err
		m.state = StateFailed
		m.finished = true
		m.exit = true
		m.mu.Unlock()
		return
	}

	if m.afterGen != nil {
		fn := m.afterGen
		m.afterGen = nil
		fn()
		return
	}

	if m.allDone() {
		m.complete()
		return
	}

	// worker 在还有分片未完成时退出、且无待处理动作：这不应发生，
	// 但宁可大声失败也不要挂起。
	m.mu.Lock()
	m.finErr = errors.New("downloader: workers exited with unfinished segments")
	m.state = StateFailed
	m.finished = true
	m.exit = true
	m.mu.Unlock()
}

// complete 将完成的 partial 文件改名为最终文件，并标记下载完成。
func (m *manager) complete() {
	if m.partial != nil {
		_ = m.partial.Sync()
		_ = m.partial.Close()
		m.partial = nil
	}
	if err := os.Rename(meta.PartialPath(m.out), m.out); err != nil {
		m.finish(StateFailed, fmt.Errorf("finalize: %w", err))
		m.exit = true
		return
	}
	_ = os.Remove(meta.MetaPath(m.out))
	m.mu.Lock()
	m.state = StateCompleted
	m.downloaded = m.total
	m.finished = true
	m.exit = true
	m.mu.Unlock()
	m.report()
}

// tick 是周期性的进度/元数据节拍，同时触发慢分片检测。
func (m *manager) tick() {
	m.report()
	if time.Since(m.lastMeta) >= metaSaveInterval {
		m.saveMeta(false)
	}
	if m.currentState() == StateRunning && time.Since(m.lastSplit) >= splitInterval {
		m.maybeSplitSlowSegments()
	}
}

// report 计算一份新快照并调用进度回调。
func (m *manager) report() {
	p := m.Snapshot()
	if m.onProgress != nil {
		m.onProgress(p)
	}
}

// Snapshot 返回当前进度。可从任意协程安全调用。
func (m *manager) Snapshot() Progress {
	m.mu.Lock()
	st := m.state
	downloaded := m.downloaded
	total := m.total
	accept := m.acceptRanges
	segments := len(m.segs)
	ferr := m.finErr
	connections := m.cfg.Connections
	workers := m.cfg.Workers
	m.mu.Unlock()

	if segments == 0 {
		segments = m.segmentCount
	}

	now := time.Now()
	var speed float64
	if st == StateRunning {
		speed = m.meter.sample(now, downloaded)
	}

	var pct float64
	if total > 0 {
		pct = float64(downloaded) / float64(total) * 100
		if pct > 100 {
			pct = 100
		}
	}
	var eta time.Duration
	if speed > 0 && total > downloaded {
		eta = time.Duration(float64(total-downloaded)/speed) * time.Second
	}

	return Progress{
		URL:          m.url,
		OutputPath:   m.out,
		Total:        total,
		Downloaded:   downloaded,
		Percent:      pct,
		Speed:        speed,
		ETA:          eta,
		Connections:  connections,
		Segments:     segments,
		Workers:      workers,
		AcceptRanges: accept,
		State:        st,
		Err:          ferr,
	}
}

// saveMeta 将进度持久化到磁盘。force 为 false 时由 metaSaveInterval
// 节流，使快速下载不至于频繁写盘。下载完成或空闲时为 no-op
// （无进度可记录）。
func (m *manager) saveMeta(force bool) {
	if !force && time.Since(m.lastMeta) < metaSaveInterval {
		return
	}
	m.mu.Lock()
	if len(m.segs) == 0 || m.state == StateCompleted || m.state == StateIdle {
		m.mu.Unlock()
		return
	}
	p := make([]plan.Segment, len(m.segs))
	copy(p, m.segs)
	mm := &meta.Meta{
		URL:          m.url,
		ETag:         m.etag,
		LastModified: m.lastModified,
		TotalSize:    m.total,
		AcceptRanges: m.acceptRanges,
		Connections:  m.cfg.Connections,
		Segments:     p,
	}
	m.mu.Unlock()

	if err := mm.Save(m.out); err == nil {
		m.lastMeta = time.Now()
	}
}

// handleSignal 分发单个控制信号。所有生命周期变更都发生在此
// manager 协程上。
func (m *manager) handleSignal(sig SignalEvent) {
	switch sig.Type {
	case SigPause:
		if m.currentState() != StateRunning {
			return
		}
		m.runExclusive(func() {
			m.setState(StatePaused)
			m.report()
		})

	case SigResume:
		if m.currentState() != StatePaused {
			return
		}
		m.setState(StateRunning)
		m.meter.reset()
		m.startGeneration()

	case SigSetConnections:
		m.setConnections(sig.Connections)
		m.runExclusive(m.applyReconfigure)

	case SigSetSegments:
		m.setSegments(sig.Segments)
		m.runExclusive(m.applyReconfigure)

	case SigSetConnectionsAndSegments:
		m.setConnections(sig.Connections)
		m.setSegments(sig.Segments)
		m.runExclusive(m.applyReconfigure)

	case SigSetWorkers:
		m.setWorkers(sig.Workers)
		m.runExclusive(m.applyReconfigure)

	case SigRestart:
		if sig.Workers > 0 {
			m.setWorkers(sig.Workers)
		}
		if sig.Connections > 0 {
			m.setConnections(sig.Connections)
		}
		if sig.Segments > 0 {
			m.setSegments(sig.Segments)
		}
		m.runExclusive(m.applyRestart)

	case SigClearCache:
		m.runExclusive(m.applyClearCache)

	case SigStop:
		m.runExclusive(func() {
			m.finish(StateStopped, nil)
			m.exit = true
			m.report()
		})

	case SigAbort:
		m.mu.Lock()
		m.abortErr = ErrAborted
		m.finErr = ErrAborted
		m.state = StateFailed
		m.finished = true
		m.exit = true
		m.mu.Unlock()
	}
}

// runExclusive 在无 worker 代际活动时运行 fn。若有 worker 在运行，
// 则把 fn 入队并取消它们；onGenerationEnd 在它们全部返回后调用 fn。
func (m *manager) runExclusive(fn func()) {
	if m.genRunning {
		m.afterGen = fn
		if m.cancelGen != nil {
			m.cancelGen()
		}
		return
	}
	fn()
}

// applyReconfigure 以当前配置重建分片 plan，并在运行时重启
// worker 池。
func (m *manager) applyReconfigure() {
	if m.total > 0 {
		m.mu.Lock()
		old := make([]plan.Segment, len(m.segs))
		copy(old, m.segs)
		m.mu.Unlock()

		segments := m.cfg.Segments
		if segments == 0 {
			segments = plan.AutoSegmentCount(m.total, m.cfg.Connections, m.cfg.MinSegmentSize)
		}
		np := plan.Reinterleave(old, m.total, segments)

		m.mu.Lock()
		m.segs = np
		m.claimed = make([]bool, len(np))
		m.segmentCount = segments
		// 从新 plan 重新推导聚合值：重新切分只保留每分片的连续
		// 前缀，因此空洞之后被孤立的字节不再计入（它们会被重下）。
		m.downloaded = plan.SumDownloaded(np)
		m.mu.Unlock()

		m.saveMeta(true)
	}

	if m.currentState() == StateRunning {
		m.startGeneration()
	}
	m.report()
}

// applyRestart 清空进度，并以当前配置开始全新下载。
func (m *manager) applyRestart() {
	if m.partial != nil {
		_ = m.partial.Truncate(0)
		if m.total > 0 {
			_ = m.partial.Truncate(m.total)
		}
	}
	_ = os.Remove(meta.MetaPath(m.out))

	segments := m.cfg.Segments
	if segments == 0 {
		segments = plan.AutoSegmentCount(m.total, m.cfg.Connections, m.cfg.MinSegmentSize)
	}
	m.mu.Lock()
	m.segs = plan.BuildPlan(m.total, segments)
	m.claimed = make([]bool, len(m.segs))
	m.segmentCount = segments
	m.downloaded = 0
	m.state = StateRunning
	m.finErr = nil
	m.mu.Unlock()
	m.meter.reset()

	m.saveMeta(true)
	m.startGeneration()
}

// applyClearCache 删除所有磁盘进度，使 manager 保持空闲。
func (m *manager) applyClearCache() {
	if m.partial != nil {
		_ = m.partial.Close()
		m.partial = nil
	}
	_ = meta.DeleteAndPartial(m.out)

	m.mu.Lock()
	m.segs = nil
	m.claimed = nil
	m.downloaded = 0
	m.total = 0
	m.state = StateIdle
	m.finErr = nil
	m.finished = true
	m.exit = true
	m.mu.Unlock()
	m.report()
}

// maybeSplitSlowSegments 检测下载明显慢于同代际其他分片的分片，
// 并把它拆成两半，让空闲或更快的 worker 能领取后半段。这是
// "当一个切片下载速度慢的时候自动分割切片，以最高性能去下载"
// 的实现。
//
// 判据：一个未完成、已领取的分片，若在 splitInterval 内的增量
// 低于"全体已领取分片增量中位数"的一半，且它仍剩余较大尺寸
// （>= 2 * MinSegmentSize），就触发拆分。被拆分的分片的前半段
// 保留原领取者继续下载，后半段变为一个新分片，被释放供其他
// worker 领取。
func (m *manager) maybeSplitSlowSegments() {
	m.mu.Lock()
	if len(m.segs) == 0 || !m.acceptRanges {
		m.mu.Unlock()
		return
	}
	now := time.Now()

	// 采样各分片自上次以来的增量。
	type sample struct {
		idx     int
		rate    float64 // bytes/sec
		claimed bool
		rem     int64
	}
	samples := make([]sample, 0, len(m.segs))
	var rates []float64
	for i := range m.segs {
		s := &m.segs[i]
		if s.Done() {
			continue
		}
		cur := s.Downloaded
		prev, had := m.segLastBytes[i]
		pt, hadT := m.segLastTime[i]
		m.segLastBytes[i] = cur
		m.segLastTime[i] = now
		if !had || !hadT {
			continue
		}
		dt := now.Sub(pt).Seconds()
		if dt <= 0 {
			continue
		}
		rate := float64(cur-prev) / dt
		samples = append(samples, sample{idx: i, rate: rate, claimed: m.claimed[i], rem: s.Remaining()})
		rates = append(rates, rate)
	}
	m.mu.Unlock()

	m.lastSplit = now
	if len(rates) < 2 {
		return
	}
	median := medianFloat64(rates)
	if median <= 0 {
		return // 大家都没动静，可能是刚恢复或暂停
	}
	threshold := median / 2

	for _, smp := range samples {
		// 只拆已领取的慢分片，且剩余尺寸足够大。
		if smp.rate > threshold || !smp.claimed || smp.rem < int64(2*m.cfg.MinSegmentSize) {
			continue
		}
		m.splitSegment(smp.idx)
	}
}

// splitSegment 将分片 idx 拆成两半：前半保留给当前领取者继续，
// 后半成为一个**追加在末尾**的新分片并释放领取权，使其他 worker
// 可以立即认领。已下载字节不移动（单 partial 文件、绝对偏移），
// 因此拆分是元数据操作。
//
// 关键设计：新分片追加在 m.segs 末尾而非插入到 idx+1，使已有
// 索引不发生偏移——正在运行的 worker 持有的 idx 仍然指向正确的
// 分片。若 worker 已下载越过拆分点，多出的字节会被转移到新分片
// 的 Downloaded，避免重下。
func (m *manager) splitSegment(idx int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx < 0 || idx >= len(m.segs) {
		return
	}
	s := &m.segs[idx]
	if s.Done() || !m.claimed[idx] {
		return
	}
	// 拆分点：当前下载点之后的中点。
	remaining := s.Remaining()
	if remaining < int64(2*m.cfg.MinSegmentSize) {
		return // 剩余太小不拆
	}
	splitAt := s.Start + s.Downloaded + (remaining+1)/2
	if splitAt <= s.Start+s.Downloaded || splitAt >= s.End {
		return
	}

	// 计算已越过拆分点的字节数（worker 可能已下载超过 splitAt）。
	excess := s.Downloaded - (splitAt - s.Start)
	if excess < 0 {
		excess = 0
	}
	// 若已越过拆分点，把旧分片 Downloaded 收到拆分点。
	newDl := splitAt - s.Start
	if s.Downloaded > newDl {
		s.Downloaded = newDl
	}
	oldEnd := s.End
	s.End = splitAt

	// 新分片追加在末尾——已有索引不偏移，worker 持有的 idx 仍有效。
	newSeg := plan.Segment{
		Position:   len(m.segs),
		Start:      splitAt,
		End:        oldEnd,
		Downloaded: excess,
	}
	m.segs = append(m.segs, newSeg)
	m.claimed = append(m.claimed, false)
	m.segmentCount = len(m.segs)

	// 清空采样基准，使下一轮从新状态计起。
	delete(m.segLastBytes, idx)
	delete(m.segLastTime, idx)
}

// medianFloat64 返回排序后切片的中位数。
func medianFloat64(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	// 复制后排序，避免修改原切片。
	cp := make([]float64, len(v))
	copy(cp, v)
	// 简单插入排序：切片通常很短（分片数有限）。
	for i := 1; i < len(cp); i++ {
		j := i
		for j > 0 && cp[j-1] > cp[j] {
			cp[j-1], cp[j] = cp[j], cp[j-1]
			j--
		}
	}
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// setConnections 钳制并存储新的 worker 数。它取 m.mu 是因为
// Snapshot 会从其他协程读取 cfg.Connections。
func (m *manager) setConnections(n int) {
	if n <= 0 {
		return
	}
	if n > maxConnections {
		n = maxConnections
	}
	m.mu.Lock()
	m.cfg.Connections = n
	m.mu.Unlock()
}

// setSegments 钳制并存储新的切片数（0 表示自动）。
func (m *manager) setSegments(n int) {
	if n < 0 {
		return
	}
	if n > maxSegments {
		n = maxSegments
	}
	m.mu.Lock()
	m.cfg.Segments = n
	m.mu.Unlock()
}

// setWorkers 钳制并存储新的"线程 + 切片"总和，由 allocateWorkers
// 自动分配 Connections 与 Segments。传 0 回退到分别设置模式
// （不覆盖当前 Connections/Segments，只清零 Workers 字段）。
func (m *manager) setWorkers(n int) {
	if n < 0 {
		return
	}
	if n > maxWorkers {
		n = maxWorkers
	}
	m.mu.Lock()
	m.cfg.Workers = n
	if n > 0 {
		allocateWorkers(m.cfg)
	}
	m.mu.Unlock()
}

func (m *manager) currentState() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *manager) setState(st State) {
	m.mu.Lock()
	m.state = st
	m.mu.Unlock()
}

// finalErr 返回终态错误（若有）。
func (m *manager) finalErr() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.finErr
}

// isFinished 报告 run() 是否已返回。
func (m *manager) isFinished() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.finished
}
