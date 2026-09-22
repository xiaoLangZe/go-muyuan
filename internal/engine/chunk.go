package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// granuleSize is the resolution of the completion bitmap. It bounds how much
	// work a pause can throw away, because a claim that was in flight when the
	// transfer stopped is fetched again from its start.
	granuleSize = 64 << 10

	// blocksPerWorker is how many claims a worker should have waiting when the
	// file is split for the first time. Too few leaves workers idle at the end,
	// too many makes the last claims very large.
	blocksPerWorker = 4

	// blockTargetDuration is how long one claim should take. The block length
	// is steered towards it, so a fast server gets long claims and a slow one
	// gets short claims that keep every worker busy.
	blockTargetDuration = 1500 * time.Millisecond
)

// granuleTracker records which parts of the file are on disk. Completion is
// tracked at granuleSize resolution so that the length of a claim can change
// while the transfer runs, which is what lets the block length adapt and the
// number of workers grow or shrink at any moment.
//
// Two bitmaps are kept: done marks bytes that are definitely on disk, claimed
// additionally covers the pieces that are in flight. A claim that is abandoned
// clears only the claimed bits, so bytes another worker finished inside the same
// range are never lost.
type granuleTracker struct {
	granule int64
	total   int64
	words   int

	mu      sync.Mutex
	done    []uint64
	claimed []uint64
	doneN   int64
	freeN   int
	hint    int
}

// newGranuleTracker builds a tracker for a file of total bytes. The granules
// below have bytes are already on disk.
func newGranuleTracker(total, granule, have int64) *granuleTracker {
	if granule <= 0 {
		granule = granuleSize
	}
	n := int((total + granule - 1) / granule)
	if n < 0 {
		n = 0
	}
	words := (n + 63) / 64
	if words < 1 {
		words = 1
	}
	tr := &granuleTracker{
		granule: granule,
		total:   total,
		words:   words,
		done:    make([]uint64, words),
		claimed: make([]uint64, words),
	}
	// The granules past the end of the file are marked unavailable in both
	// bitmaps, so a claim never hands out a range that does not exist.
	for i := n; i < words*64; i++ {
		bitSet(tr.done, i)
		bitSet(tr.claimed, i)
	}
	if have > total {
		have = total
	}
	if done := int(have / granule); done > 0 {
		for i := 0; i < done; i++ {
			if !bitIsSet(tr.done, i) {
				bitSet(tr.done, i)
				tr.doneN++
			}
			bitSet(tr.claimed, i)
		}
	}
	tr.freeN = n - int(tr.doneN)
	return tr
}

func bitSet(words []uint64, i int)   { words[i/64] |= 1 << uint(i%64) }
func bitClear(words []uint64, i int) { words[i/64] &^= 1 << uint(i%64) }
func bitIsSet(words []uint64, i int) bool {
	return words[i/64]&(1<<uint(i%64)) != 0
}

// granuleCount returns how many granules the byte range covers.
func (tr *granuleTracker) granuleCount(start, length int64) int {
	if length <= 0 {
		return 0
	}
	first := start / tr.granule
	last := (start + length - 1) / tr.granule
	return int(last-first) + 1
}

// claim takes the next free run of granules, up to maxLen bytes, and marks it
// in flight. The caller must complete or release the same range.
func (tr *granuleTracker) claim(maxLen int64) (start, length int64, ok bool) {
	if maxLen <= 0 {
		maxLen = tr.granule
	}
	want := int((maxLen + tr.granule - 1) / tr.granule)
	if want < 1 {
		want = 1
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()

	if tr.freeN <= 0 {
		return 0, 0, false
	}
	n := int((tr.total + tr.granule - 1) / tr.granule)
	limit := tr.hint
	if limit > n {
		limit = n
	}
	// Two passes: from the hint to the end, then from the start back to the
	// hint, so the whole bitmap is covered.
	for pass := 0; pass < 2; pass++ {
		begin, stop := 0, limit
		if pass == 0 {
			begin, stop = limit, n
		}
		for i := begin; i < stop; i++ {
			if bitIsSet(tr.claimed, i) {
				continue
			}
			j := i
			for j < n && j-i < want && !bitIsSet(tr.claimed, j) {
				j++
			}
			if j == i {
				continue
			}
			for k := i; k < j; k++ {
				bitSet(tr.claimed, k)
			}
			tr.freeN -= j - i
			tr.hint = j
			if tr.hint >= n {
				tr.hint = 0
			}
			start = int64(i) * tr.granule
			length = int64(j-i) * tr.granule
			if start+length > tr.total {
				length = tr.total - start
			}
			return start, length, true
		}
	}
	return 0, 0, false
}

// complete marks a claimed range as on disk.
func (tr *granuleTracker) complete(start, length int64) {
	n := tr.granuleCount(start, length)
	if n == 0 {
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	first := int(start / tr.granule)
	for i := first; i < first+n; i++ {
		if !bitIsSet(tr.done, i) {
			bitSet(tr.done, i)
			tr.doneN++
		}
		bitSet(tr.claimed, i)
	}
}

// release gives a claimed range back without completing it, so another worker
// claims it again. Granules another worker already completed stay complete and
// stay claimed.
func (tr *granuleTracker) release(start, length int64) {
	n := tr.granuleCount(start, length)
	if n == 0 {
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	first := int(start / tr.granule)
	for i := first; i < first+n; i++ {
		if !bitIsSet(tr.done, i) && bitIsSet(tr.claimed, i) {
			bitClear(tr.claimed, i)
			tr.freeN++
		}
	}
	if first < tr.hint {
		tr.hint = first
	}
}

// completedBytes is the number of bytes that are definitely on disk.
func (tr *granuleTracker) completedBytes() int64 {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	got := tr.doneN * tr.granule
	if got > tr.total {
		return tr.total
	}
	return got
}

// remaining is the number of bytes still to fetch.
func (tr *granuleTracker) remaining() int64 {
	left := tr.total - tr.completedBytes()
	if left < 0 {
		return 0
	}
	return left
}

// free reports whether any granule is still unclaimed.
func (tr *granuleTracker) free() bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.freeN > 0
}

// pending is the number of granules claimed but not yet completed. It is used
// by the tests to check that a pause does not leave anything marked in flight.
func (tr *granuleTracker) pending() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := int((tr.total + tr.granule - 1) / tr.granule)
	count := 0
	for i := 0; i < n; i++ {
		if bitIsSet(tr.claimed, i) && !bitIsSet(tr.done, i) {
			count++
		}
	}
	return count
}

// doneGranules counts the granules marked complete, so a test can cross-check
// the bitmap against the bytes on disk.
func (tr *granuleTracker) doneGranules() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	total := 0
	for _, w := range tr.done {
		total += bits.OnesCount64(w)
	}
	// The unavailable tail occupies whole words, so the count has to exclude
	// everything past the last real granule.
	n := int((tr.total + tr.granule - 1) / tr.granule)
	words := n / 64
	total -= bits.OnesCount64(tr.done[words] &^ ((uint64(1) << uint(n%64)) - 1))
	return total
}

// chunkShaper chooses the length of one claim and steers it towards a target
// duration, so a fast server is asked for long runs and a slow one for short
// runs that keep every worker busy.
type chunkShaper struct {
	min    int64
	max    int64
	target time.Duration

	current int64
	pinned  bool
}

func newChunkShaper(cfg Config, total int64, workers int) *chunkShaper {
	s := &chunkShaper{
		min:    cfg.ChunkMin,
		max:    cfg.ChunkMax,
		target: blockTargetDuration,
	}
	if cfg.BlockSize > 0 {
		s.current = cfg.BlockSize
		s.pinned = true
		return s
	}
	s.current = s.initial(total, workers)
	return s
}

// initial splits the file so that every worker has a few claims waiting.
func (s *chunkShaper) initial(total int64, workers int) int64 {
	if workers < 1 {
		workers = 1
	}
	size := total / int64(workers*blocksPerWorker)
	if size < s.min {
		size = s.min
	}
	if size > s.max {
		size = s.max
	}
	return size
}

// observe feeds back how long a claim of size bytes took, and returns the
// length to use next.
//
// The step is driven by the measured duration rather than by an estimated rate:
// a rate estimate carries its history for several samples, which would keep a
// block far too large long after the server slowed down. The price is that one
// unusual response moves the block, so a step is bounded to a factor of two and
// the result is clamped to the configured bounds.
func (s *chunkShaper) observe(size int64, took time.Duration) int64 {
	if s.pinned || size <= 0 {
		return s.current
	}
	if took <= 0 {
		took = time.Millisecond
	}
	// A short final claim took less time only because it was short; scale it to
	// what a full claim would have taken.
	if size != s.current && s.current > 0 {
		took = time.Duration(int64(took) * s.current / size)
	}
	ratio := float64(s.target) / float64(took)
	if ratio > 2 {
		ratio = 2
	}
	if ratio < 0.5 {
		ratio = 0.5
	}
	want := int64(float64(s.current) * ratio)
	if want < s.min {
		want = s.min
	}
	if want > s.max {
		want = s.max
	}
	if want < 1 {
		want = s.min
	}
	s.current = want
	return s.current
}

// tail returns a claim length that lets the remaining workers finish together
// instead of leaving one worker with the whole tail.
func (s *chunkShaper) tail(remaining int64, workers int) int64 {
	if workers < 1 {
		workers = 1
	}
	if remaining <= 0 {
		return s.current
	}
	size := remaining / int64(workers)
	if size < s.min {
		size = s.min
	}
	if size > s.current {
		size = s.current
	}
	return size
}

// probeResult is what a single range request tells the transfer about the
// resource before the workers start.
type probeResult struct {
	total  int64
	ranges bool
}

// probe asks for the first byte of the file. A server that answers with 206
// serves ranges and the total size comes back in the Content-Range header; a
// server that answers with 200 does not serve ranges and the transfer has to
// use a single connection.
func (t *Transfer) probe(ctx context.Context) (probeResult, error) {
	t.mu.Lock()
	validator := t.validator
	t.mu.Unlock()

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, t.url, nil)
	if err != nil {
		return probeResult{}, fmt.Errorf("build request: %w", err)
	}
	t.applyHeaders(req)
	req.Header.Set("Range", "bytes=0-0")
	if validator != "" {
		req.Header.Set("If-Range", validator)
	}

	resp, err := t.cfg.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return probeResult{}, fmt.Errorf("request %s: %w", t.url, err)
		}
		return probeResult{}, transient(fmt.Errorf("request %s: %w", t.url, err))
	}
	defer resp.Body.Close()

	t.observeHeaders(resp)

	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, total, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil {
			return probeResult{}, err
		}
		if start != 0 {
			return probeResult{}, fmt.Errorf("server answered from byte %d for a probe at 0", start)
		}
		return probeResult{total: total, ranges: true}, nil

	case http.StatusOK:
		// The server ignored the range request, so it either does not serve
		// ranges or the resource changed. Either way the file has to come over
		// one connection.
		return probeResult{total: resp.ContentLength, ranges: false}, nil

	case http.StatusRequestedRangeNotSatisfiable:
		_, total, _ := parseContentRange(resp.Header.Get("Content-Range"))
		return probeResult{total: total, ranges: true}, nil

	default:
		return probeResult{}, &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status, URL: t.url}
	}
}

// probeWithRetry runs the probe until it succeeds or the retries run out.
func (t *Transfer) probeWithRetry(ctx context.Context) (probeResult, error) {
	delay := t.cfg.RetryDelay
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return probeResult{}, ctx.Err()
		}
		result, err := t.probe(ctx)
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return probeResult{}, ctx.Err()
		}
		if attempt >= t.cfg.Retries || !isTransient(err) {
			return probeResult{}, err
		}
		if !sleep(ctx, delay) {
			return probeResult{}, ctx.Err()
		}
		if delay *= 2; delay > maxRetryDelay {
			delay = maxRetryDelay
		}
	}
}

// applyHeaders copies the configured request headers onto req.
func (t *Transfer) applyHeaders(req *http.Request) {
	for key, values := range t.cfg.Headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
}

// wakeChan is the channel a worker budget change is signalled on. It exists for
// the lifetime of a run.
func (t *Transfer) wakeChan() chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.wake
}

// runParallel fetches the file over several connections. It returns
// errFallbackSingle when the server cannot serve part of the file, and any
// other error as a failure.
func (t *Transfer) runParallel(ctx context.Context) error {
	probe, err := t.probeWithRetry(ctx)
	if err != nil {
		return err
	}
	if !probe.ranges || probe.total <= 0 {
		return errFallbackSingle
	}
	t.setRanges(RangeSupported)
	t.setTotal(probe.total)
	if probe.total < 2*t.cfg.ChunkMin {
		// Splitting a file this small costs more than it saves.
		return errTooSmall
	}

	t.mu.Lock()
	have := t.downloaded
	t.mu.Unlock()
	workers := t.Workers()
	shaper := newChunkShaper(t.cfg, probe.total, workers)
	tracker := newGranuleTracker(probe.total, granuleSize, have)
	t.mu.Lock()
	t.tracker = tracker
	t.shaper = shaper
	t.blockSize = shaper.current
	t.parallel = true
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.parallel = false
		t.mu.Unlock()
	}()

	part, _ := t.paths()
	if tracker.remaining() == 0 {
		// Everything is already on disk; only the rename is missing.
		if err := syncFile(part); err != nil {
			return err
		}
		return t.finishFile()
	}

	file, err := openChunked(part, probe.total)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	wake := t.wakeChan()
	if wake == nil {
		wake = make(chan struct{}, 1)
	}

	var (
		running atomic.Int64
		wg      sync.WaitGroup
		errMu   sync.Mutex
		runErr  error
	)
	poke := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	fail := func(err error) {
		errMu.Lock()
		if runErr == nil {
			runErr = err
		}
		errMu.Unlock()
		cancelRun()
		poke()
	}
	collect := func() error {
		errMu.Lock()
		defer errMu.Unlock()
		return runErr
	}

	spawn := func() {
		wg.Add(1)
		running.Add(1)
		go func() {
			defer wg.Done()
			defer running.Add(-1)
			defer poke()
			t.workerLoop(runCtx, file, tracker, shaper, fail)
		}()
	}

	for {
		if runCtx.Err() != nil {
			break
		}
		if !tracker.free() && running.Load() == 0 {
			break
		}
		if want := int64(t.Workers()); running.Load() < want && tracker.free() {
			spawn()
			continue
		}
		select {
		case <-runCtx.Done():
		case <-wake:
		}
	}

	wg.Wait()
	if err := collect(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		// Paused, deleted or canceled. The completed bytes stay on disk and the
		// tracker is rebuilt from them when the transfer is resumed, while the
		// pieces that were in flight are fetched again.
		return ctx.Err()
	}
	if left := tracker.remaining(); left > 0 {
		return transient(fmt.Errorf("download ended with %d bytes missing", left))
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", part, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", part, err)
	}
	closed = true
	return t.finishFile()
}

// workerLoop claims pieces of the file and fetches them until there is nothing
// left, the run ends, or the budget no longer has room for this worker.
func (t *Transfer) workerLoop(ctx context.Context, file *os.File, tracker *granuleTracker, shaper *chunkShaper, fail func(error)) {
	for {
		if ctx.Err() != nil {
			return
		}
		workers := t.Workers()
		if workers < 1 {
			// The budget was taken away; this worker steps aside and the run
			// stays alive until the budget comes back or the run is stopped.
			return
		}
		length := shaper.tail(tracker.remaining(), workers)
		start, got, ok := tracker.claim(length)
		if !ok {
			return
		}
		took, err := t.fetchBlock(ctx, file, start, got)
		if err != nil {
			tracker.release(start, got)
			if ctx.Err() != nil {
				return
			}
			fail(err)
			return
		}
		tracker.complete(start, got)
		t.report(tracker.completedBytes())
		next := shaper.observe(got, took)
		t.mu.Lock()
		t.blockSize = next
		t.mu.Unlock()
	}
}

// fetchBlock fetches one piece, retrying transient failures. The returned
// duration is how long the whole piece took, which the shaper steers on.
func (t *Transfer) fetchBlock(ctx context.Context, file *os.File, start, length int64) (time.Duration, error) {
	began := time.Now()
	delay := t.cfg.RetryDelay
	var lastErr error

	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		err := t.fetchBlockOnce(ctx, file, start, length)
		if err == nil {
			return time.Since(began), nil
		}
		lastErr = err
		if ctx.Err() != nil || errors.Is(err, errFallbackSingle) {
			return 0, err
		}
		if attempt >= t.cfg.Retries || !isTransient(err) {
			break
		}
		if !sleep(ctx, delay) {
			return 0, ctx.Err()
		}
		if delay *= 2; delay > maxRetryDelay {
			delay = maxRetryDelay
		}
	}
	return 0, lastErr
}

// fetchBlockOnce performs one ranged request and writes the body at its offset.
func (t *Transfer) fetchBlockOnce(ctx context.Context, file *os.File, start, length int64) error {
	if length <= 0 {
		return nil
	}
	end := start + length - 1

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, t.url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	t.applyHeaders(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	t.mu.Lock()
	validator := t.validator
	t.mu.Unlock()
	if validator != "" {
		req.Header.Set("If-Range", validator)
	}

	resp, err := t.cfg.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return transient(fmt.Errorf("request %s: %w", t.url, err))
	}
	defer resp.Body.Close()

	t.observeHeaders(resp)

	switch resp.StatusCode {
	case http.StatusPartialContent:
		got, total, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil {
			return err
		}
		if got != start {
			return transient(fmt.Errorf("server answered from byte %d, expected %d", got, start))
		}
		t.setTotal(total)

	case http.StatusOK:
		// The server ignored the range header, so the file cannot be split.
		return errFallbackSingle

	case http.StatusRequestedRangeNotSatisfiable:
		_, total, _ := parseContentRange(resp.Header.Get("Content-Range"))
		if total > 0 && start >= total {
			return nil
		}
		return transient(fmt.Errorf("server rejected the range request at offset %d", start))

	default:
		return &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status, URL: t.url}
	}

	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	stopWatch := watchStall(t.cfg.StallTimeout, cancel, &lastRead)
	defer stopWatch()

	buffer := make([]byte, t.cfg.BufferSize)
	var written int64
	for written < length {
		want := int64(len(buffer))
		if left := length - written; left < want {
			want = left
		}
		n, readErr := resp.Body.Read(buffer[:want])
		if n > 0 {
			if _, err := file.WriteAt(buffer[:n], start+written); err != nil {
				return fmt.Errorf("write at %d: %w", start+written, err)
			}
			written += int64(n)
			lastRead.Store(time.Now().UnixNano())
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if ctx.Err() == nil && reqCtx.Err() != nil {
				return transient(fmt.Errorf("%w: no data for %s", ErrStalled, t.cfg.StallTimeout))
			}
			return transient(fmt.Errorf("read body: %w", readErr))
		}
	}
	if written != length {
		return transient(fmt.Errorf("piece ended after %d of %d bytes", written, length))
	}
	return nil
}

// finishFile renames the completed partial file into place.
func (t *Transfer) finishFile() error {
	part, final := t.paths()
	if err := os.Rename(part, final); err != nil {
		return fmt.Errorf("move %s to %s: %w", part, final, err)
	}
	return nil
}
