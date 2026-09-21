package go_muyuan

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"
)

// Handle is one download. It is created by New, Download or DownloadTo and is
// safe for use by several goroutines.
//
// Only one transfer runs at a time. Start, Pause, Restart and Delete hold ctl
// for their whole duration, including the wait for the previous worker to
// return, so a new transfer never begins while the old one is still writing,
// and a worker that is on its way out can never overwrite the state of the
// transfer that replaced it. The worker itself only ever takes mu, which is
// what keeps that wait from deadlocking.
type Handle struct {
	id     string
	url    string
	target *url.URL
	cfg    config
	base   context.Context

	ctl      sync.Mutex
	mu       sync.Mutex
	status   Status
	err      error
	fileName string
	resolved bool

	downloaded  int64
	total       int64
	speed       int64
	active      time.Duration
	ranges      rangeState
	validator   string
	sampleAt    time.Time
	sampleBytes int64

	// The current run. runDone is closed once its worker has returned.
	runCtx   context.Context
	cancel   context.CancelFunc
	runDone  chan struct{}
	runStart time.Time

	done     chan struct{}
	finished bool
}

// ID returns the identifier of the download.
func (h *Handle) ID() string { return h.id }

// URL returns the target URL.
func (h *Handle) URL() string { return h.url }

// Dir returns the directory the file is written to.
func (h *Handle) Dir() string { return h.cfg.dir }

// FilePath returns the path of the finished file. Until the response headers
// are read the name comes from the URL; a Content-Disposition name from the
// server replaces it once it is known.
func (h *Handle) FilePath() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.filePathLocked()
}

// PartPath returns the path of the file written while the download is in
// flight. It is renamed to FilePath when the transfer completes.
func (h *Handle) PartPath() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.filePathLocked() + partSuffix
}

// Status returns the current lifecycle state.
func (h *Handle) Status() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

// Err returns the error that ended the download. It is nil while the download
// runs or is paused.
func (h *Handle) Err() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

// Progress returns a snapshot of the transfer statistics.
func (h *Handle) Progress() Progress {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.progressLocked(time.Now())
}

// Info returns a consistent snapshot of the handle.
func (h *Handle) Info() Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := h.filePathLocked()
	p := h.progressLocked(time.Now())
	return Info{
		ID:         h.id,
		URL:        h.url,
		FilePath:   path,
		PartPath:   path + partSuffix,
		Status:     h.status,
		Err:        h.err,
		Total:      p.Total,
		Downloaded: p.Downloaded,
		Speed:      p.Speed,
		Elapsed:    p.Elapsed,
	}
}

// Start begins the transfer, or continues it after a pause or a failure. It
// reports nil when the download is already running.
//
// A download that failed can be started again: it resumes from the bytes
// already on disk when the server supports ranges, and starts over when it does
// not.
func (h *Handle) Start() error {
	h.ctl.Lock()
	defer h.ctl.Unlock()
	return h.start()
}

// start begins a transfer; the caller holds ctl.
func (h *Handle) start() error {
	h.mu.Lock()
	switch h.status {
	case StatusDownloading:
		h.mu.Unlock()
		return nil
	case StatusCompleted:
		h.mu.Unlock()
		return ErrCompleted
	case StatusDeleted:
		h.mu.Unlock()
		return ErrDeleted
	}
	previous := h.runDone
	h.mu.Unlock()

	// The worker of the previous run must have returned before this one reads
	// the state it maintains: the offset to resume from, the counters and the
	// partial file all belong to it until it is done with them.
	if previous != nil {
		<-previous
	}

	ctx, cancel := context.WithCancel(h.base)
	run := make(chan struct{})
	h.mu.Lock()
	h.runCtx, h.cancel, h.runDone = ctx, cancel, run
	h.status = StatusDownloading
	h.err = nil
	h.runStart = time.Now()
	// A transfer that is started again is a new thing to wait for, so the
	// channel behind Done and Wait is replaced. The previous worker has already
	// returned by now, which is what makes the replacement race free.
	h.done = make(chan struct{})
	h.finished = false
	// Backdating the sample window by one interval makes the first progress
	// report due immediately.
	h.sampleAt = h.runStart.Add(-progressInterval)
	h.sampleBytes = h.downloaded
	h.mu.Unlock()

	go func() {
		defer close(run)
		defer h.stopClock()
		h.work(ctx)
	}()
	return nil
}

// Pause stops the transfer and keeps the bytes received so far, so Resume can
// continue from the same offset. Pausing a paused download does nothing and
// reports nil. It returns once the transfer has stopped and the partial file is
// closed, which makes the file safe to inspect or move.
func (h *Handle) Pause() error {
	h.ctl.Lock()
	defer h.ctl.Unlock()

	h.mu.Lock()
	switch h.status {
	case StatusPaused:
		h.mu.Unlock()
		return nil
	case StatusDownloading:
		// The worker reads this to tell an intentional stop from a cancellation
		// and to leave the state alone.
		h.status = StatusPaused
	default:
		status := h.status
		h.mu.Unlock()
		return fmt.Errorf("%w (status %s)", ErrNotDownloading, status)
	}
	cancel, run := h.cancel, h.runDone
	h.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if run != nil {
		<-run
	}
	return nil
}

// Resume continues a paused download. It reports nil when the download is
// already running.
func (h *Handle) Resume() error {
	h.mu.Lock()
	status := h.status
	h.mu.Unlock()

	switch status {
	case StatusDownloading:
		return nil
	case StatusPaused:
		return h.Start()
	default:
		return fmt.Errorf("%w (status %s)", ErrNotPaused, status)
	}
}

// Restart throws away the bytes received so far and downloads the file again
// from the beginning. It works from any state, including a finished or a
// deleted download.
func (h *Handle) Restart() error {
	h.ctl.Lock()
	defer h.ctl.Unlock()

	h.mu.Lock()
	if h.status == StatusDownloading {
		h.status = StatusPaused
	}
	cancel, run := h.cancel, h.runDone
	h.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if run != nil {
		<-run
	}

	part, _ := h.paths()
	if err := os.Remove(part); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", part, err)
	}

	h.mu.Lock()
	h.downloaded = 0
	h.total = 0
	h.speed = 0
	h.active = 0
	h.validator = ""
	h.ranges = rangeUnknown
	h.err = nil
	h.status = StatusPending
	h.mu.Unlock()

	return h.start()
}

// Delete stops the download and removes the finished file and the partial file.
// The handle ends up in StatusDeleted, and calling Delete again reports nil.
func (h *Handle) Delete() error {
	h.ctl.Lock()
	defer h.ctl.Unlock()

	h.mu.Lock()
	if h.status == StatusDownloading {
		// The worker must not report a failure for a transfer that is being
		// dropped on purpose.
		h.status = StatusDeleted
	}
	cancel, run := h.cancel, h.runDone
	h.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if run != nil {
		<-run
	}
	h.finish(StatusDeleted, nil)

	part, final := h.paths()
	var errs []error
	for _, path := range []string{final, part} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// Wait blocks until the download reaches a terminal state and returns the error
// that ended it. A paused download is not terminal, so Wait stays blocked until
// it is resumed or deleted. Waiting on a handle created by New and never
// started blocks until Start is called.
func (h *Handle) Wait() error {
	<-h.Done()
	return h.Err()
}

// WaitContext is Wait with a deadline of the caller's choosing. It reports the
// context error when the deadline passes first.
func (h *Handle) WaitContext(ctx context.Context) error {
	select {
	case <-h.Done():
		return h.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done returns a channel that is closed once the download reaches a terminal
// state: completed, failed or deleted. A download that is started again gets a
// new channel.
func (h *Handle) Done() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.done
}

// finish moves the handle into a terminal state and wakes everyone blocked on
// Done. The flag rather than a sync.Once is what lets a later run install a
// fresh channel without racing the run that just ended.
func (h *Handle) finish(status Status, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status = status
	h.err = err
	if !h.finished {
		h.finished = true
		close(h.done)
	}
}

// stopClock folds the running period into the elapsed time. It runs as the
// worker returns.
func (h *Handle) stopClock() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.runStart.IsZero() {
		h.active += time.Since(h.runStart)
		h.runStart = time.Time{}
	}
}

// progressLocked derives the statistics of a download; the caller holds the
// lock.
func (h *Handle) progressLocked(now time.Time) Progress {
	elapsed := h.active
	if !h.runStart.IsZero() {
		elapsed += now.Sub(h.runStart)
	}
	p := Progress{
		Total:      h.total,
		Downloaded: h.downloaded,
		Speed:      h.speed,
		Elapsed:    elapsed,
	}
	if h.total > 0 {
		p.Percent = float64(h.downloaded) / float64(h.total) * 100
		if p.Percent > 100 {
			p.Percent = 100
		}
		if h.speed > 0 && h.total > h.downloaded {
			p.ETA = time.Duration(float64(h.total-h.downloaded)/float64(h.speed)) * time.Second
		}
	}
	return p
}

// report records newly written bytes and, at most once per progress interval,
// hands a snapshot to the callback. The callback runs outside the lock so it
// can read the handle, but on this goroutine, which is why it must not block.
func (h *Handle) report(downloaded int64) {
	h.mu.Lock()
	h.downloaded = downloaded
	now := time.Now()
	elapsed := now.Sub(h.sampleAt)
	if elapsed < progressInterval {
		h.mu.Unlock()
		return
	}
	h.speed = int64(float64(downloaded-h.sampleBytes) / elapsed.Seconds())
	h.sampleAt, h.sampleBytes = now, downloaded
	callback := h.cfg.progress
	var snapshot Progress
	if callback != nil {
		snapshot = h.progressLocked(now)
	}
	h.mu.Unlock()

	if callback != nil {
		callback(snapshot)
	}
}

// emit reports the finished transfer, so a callback always sees the last state
// even when the file arrived faster than one progress interval.
func (h *Handle) emit() {
	h.mu.Lock()
	callback := h.cfg.progress
	var snapshot Progress
	if callback != nil {
		snapshot = h.progressLocked(time.Now())
	}
	h.mu.Unlock()

	if callback != nil {
		callback(snapshot)
	}
}

// discardProgress gives up on the bytes received so far. The partial file is
// truncated by the next attempt, which opens it at offset zero.
func (h *Handle) discardProgress() {
	h.mu.Lock()
	h.downloaded = 0
	h.mu.Unlock()
}

func (h *Handle) setTotal(total int64) {
	h.mu.Lock()
	h.total = total
	h.mu.Unlock()
}

func (h *Handle) setRanges(state rangeState) {
	h.mu.Lock()
	h.ranges = state
	h.mu.Unlock()
}

// sleep waits for d and reports false when ctx ends first.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
