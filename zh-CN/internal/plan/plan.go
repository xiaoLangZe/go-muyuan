// Package plan 描述一次下载的分片切分与进度重算。
//
// 一个 Segment 是文件的一段连续字节范围，并跟踪其中已写入 partial
// 文件的字节数。分片通过 Position 在全局 plan 中索引。Start/End 是
// 下载文件内的绝对字节偏移（Start 含、End 不含）。Downloaded 是从
// Start 起已写入的字节数；当 Downloaded == End-Start 时该分片完成。
//
// 一个分片同一时刻只由一个连接拉取。分片数与并发连接数相互独立：
// 32 个分片由 8 个连接拉取是合法的，此时每个连接领取一个分片、
// 完成后领取下一个。
package plan

// Segment 描述文件的一段连续字节范围，并跟踪其中已写入
// partial 文件的字节数。
//
// 分片通过其 Position 字段在全局 plan 中索引。它们的 Start/End 是
// 下载文件内的绝对字节偏移（Start 含、End 不含）。Downloaded 是
// 从 Start 起已写入的字节数；当 Downloaded == End-Start 时该分片完成。
//
// 一个分片同一时刻只由一个连接拉取。分片数与连接数相互独立：32 个
// 分片由 8 个连接拉取是合法的，此时每个连接领取一个分片、完成后
// 领取下一个。
type Segment struct {
	Position   int   `json:"position"`
	Start      int64 `json:"start"`
	End        int64 `json:"end"`
	Downloaded int64 `json:"downloaded"`
}

// Size 返回该分片的总字节长度。
func (s Segment) Size() int64 { return s.End - s.Start }

// Remaining 返回该分片尚需拉取的字节数。
func (s Segment) Remaining() int64 {
	r := s.Size() - s.Downloaded
	if r < 0 {
		return 0
	}
	return r
}

// Done 报告该分片是否已完整下载。
func (s Segment) Done() bool { return s.Downloaded >= s.Size() }

// NextOffset 返回该分片下一次写入应落到的绝对偏移
// （Start + Downloaded）。
func (s Segment) NextOffset() int64 { return s.Start + s.Downloaded }

// DefaultMinSegmentSize 是自动分片的默认最小尺寸（1 MiB）。
// 更小的分片会造成浪费。
const DefaultMinSegmentSize = 1 << 20

// BuildPlan 将 total 字节切分为 n 个连续、不重叠的分片，
// 完整覆盖 [0, total)。最后一个分片吸收余数，使 plan 永远精确。
func BuildPlan(total int64, n int) []Segment {
	if n < 1 {
		n = 1
	}
	if total <= 0 {
		return []Segment{{Position: 0, Start: 0, End: 0, Downloaded: 0}}
	}
	if n == 1 {
		return []Segment{{Position: 0, Start: 0, End: total, Downloaded: 0}}
	}
	base := total / int64(n)
	rem := total % int64(n)
	segments := make([]Segment, 0, n)
	var off int64
	for i := 0; i < n; i++ {
		size := base
		if i < int(rem) {
			size++
		}
		segments = append(segments, Segment{
			Position: i,
			Start:    off,
			End:      off + size,
		})
		off += size
	}
	return segments
}

// AutoSegmentCount 为 "0 == 自动" 场景选择分片数：大致每个连接一个
// 分片，但每个不小于 MinSegmentBytes，且分片数不超过文件的字节数
// （一个 50 字节的文件不该被切成 8 片）。
func AutoSegmentCount(total int64, connections, minSegmentBytes int) int {
	if total <= 0 || connections < 1 {
		return 1
	}
	if minSegmentBytes < 1 {
		minSegmentBytes = DefaultMinSegmentSize
	}
	n := connections
	if int64(n)*int64(minSegmentBytes) > total {
		n = int(total / int64(minSegmentBytes))
		if n < 1 {
			n = 1
		}
	}
	return n
}

// Reinterleave 在同一字节范围上以不同分片数生成新的分片 plan，
// 并保留已下载的字节。
//
// 由于所有分片共享同一个 partial 后端文件、且字节按其绝对偏移写入，
// 改变分片边界无需移动任何数据：偏移 X 处已下载的字节无论归哪个
// 分片所有都仍然有效。因此我们把每个新分片的 Downloaded 重算为
// 磁盘上已存在的、其 [Start,End) 的最长 *连续前缀* 的长度。
//
// 前缀（而非总交集）很关键：当新旧边界不对齐时，一个已下载区域
// 可能位于新分片内部的一个空洞之后。只记录前缀保证了连接从一个
// "此前字节全部有效" 的偏移恢复；空洞之后被孤立的字节只需重下，
// 这是安全的。
//
// 这是运行时 SetSegments / SetConnectionsAndSegments 背后的关键操作。
func Reinterleave(old []Segment, total int64, n int) []Segment {
	newPlan := BuildPlan(total, n)
	if len(old) == 0 {
		return newPlan
	}
	// 从旧 plan 构造已下载字节范围。每个旧分片贡献
	// [Start, Start+Downloaded)。旧分片按升序铺满文件，
	// 所以这些范围是有序且不相交的。
	ranges := make([]rngT, 0, len(old))
	for _, s := range old {
		if s.Downloaded <= 0 {
			continue
		}
		end := s.Start + s.Downloaded
		if end > s.End {
			end = s.End
		}
		if end > s.Start {
			ranges = append(ranges, rngT{s.Start, end})
		}
	}
	for i := range newPlan {
		s := &newPlan[i]
		s.Downloaded = prefixDownloaded(s.Start, s.End, ranges)
	}
	return newPlan
}

// prefixDownloaded 返回 [start,end) 起始处被 ranges 连续覆盖的字节数。
// 它遍历有序、不相交的 ranges，遇到第一个空洞即停止。
func prefixDownloaded(start, end int64, ranges []rngT) int64 {
	cur := start
	for _, r := range ranges {
		if r.end <= cur {
			continue // 完全在游标之前
		}
		if r.start > cur {
			break // 出现空洞：前缀到此结束
		}
		// r.start <= cur < r.end，因此它延伸（或紧邻）前缀。
		next := r.end
		if next > end {
			next = end
		}
		cur = next
		if cur >= end {
			break
		}
	}
	if cur > end {
		cur = end
	}
	if cur < start {
		return 0
	}
	return cur - start
}

// SumDownloaded 合计每分片进度，用于在续传时初始化聚合计数器。
func SumDownloaded(plan []Segment) int64 {
	var n int64
	for _, c := range plan {
		n += c.Downloaded
	}
	return n
}

// rngT 是 re-slicing 辅助函数使用的字节范围。
type rngT struct{ start, end int64 }
