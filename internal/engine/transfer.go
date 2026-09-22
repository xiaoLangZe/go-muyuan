package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xiaoLangZe/go-muyuan/internal/filename"
	"github.com/xiaoLangZe/go-muyuan/internal/hostguard"
)

// errIncomplete reports a transfer that stopped without finishing and without
// an error of its own, for example when the base context was canceled.
var errIncomplete = errors.New("download stopped before the file was complete")

// Transfer is one file transfer. It is created by NewTransfer and is safe for
// use by several goroutines.
//
// Only one run is active at a time. Start, Pause, Restart and Delete hold ctl
// for their whole duration, including the wait for the previous run to return,
// so a new run never begins while the old one is still writing, and a run that
// is on its way out can never overwrite the state of the run that replaced it.
// The run itself only ever takes mu, which is what keeps that wait from
// deadlocking.
type Transfer struct {
	id     string
	url    string
	target *url.URL
	cfg    Config
	base   context.Context

	ctl sync.Mutex
	mu  sync.Mutex

	status   Status
	err      error
	fileName string
	resolved bool

	downloaded  int64
	total       int64
	speed       int64
	active      time.Duration
	ranges      RangeMode
	validator   string
	sampleAt    time.Time
	sampleBytes int64

	// workers is the number of connections the run may use. It is read live by
	// the parallel supervisor, so a caller can change it mid-transfer.
	workers   int
	blockSize int64
	shaper    *chunkShaper

	// The current run. runDone is closed once its worker has returned.
	runCtx     context.Context
	cancel     context.CancelFunc
	runDone    chan struct{}
	runStart   time.Time
	parallel   bool
	promote    bool
	attemptCut context.CancelFunc
	wake       chan struct{}
	tracker    *granuleTracker

	done     chan struct{}
	finished bool
}

// NewTransfer prepares a transfer without starting it. It validates the target,
// applies the host guard unless the configuration allows unsafe hosts, and
// fixes the initial file name.
func NewTransfer(ctx context.Context, rawURL string, opts ...Option) (*Transfer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := DefaultConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	NormalizeConfig(&cfg)

	target, err := ParseTarget(rawURL)
	if err != nil {
		return nil, err
	}
	if _, err := parseProxy(cfg.Proxy); err != nil {
		return nil, err
	}
	if !cfg.AllowUnsafe {
		if err := hostguard.CheckHost(ctx, target.Hostname()); err != nil {
			return nil, err
		}
	}
	if cfg.Client == nil {
		cfg.Client = NewClient(cfg)
	}

	t := &Transfer{
		id:      newID(),
		url:     target.String(),
		target:  target,
		cfg:     cfg,
		base:    ctx,
		status:  StatusPending,
		workers: cfg.Workers,
		done:    make(chan struct{}),
	}

	name := cfg.FileName
	if name == "" {
		name = filename.FromURL(target)
	}
	name = filename.Sanitize(name)
	if name == "" {
		name = filename.Fallback
	}
	t.fileName = filename.Unique(cfg.Dir, name, cfg.Overwrite)
	// A name given by the caller is final; one derived from the URL may still
	// be replaced by the name the server sends in Content-Disposition.
	t.resolved = cfg.FileName != ""

	return t, nil
}

// ParseTarget accepts an absolute http or https URL with a host and no
// credentials.
func ParseTarget(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("%w: empty URL", ErrInvalidURL)
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	switch target.Scheme {
	case "http", "https":
	case "":
		return nil, fmt.Errorf("%w: %q has no scheme", ErrInvalidURL, rawURL)
	default:
		return nil, fmt.Errorf("%w: scheme %q is not supported", ErrInvalidURL, target.Scheme)
	}
	if target.Host == "" {
		return nil, fmt.Errorf("%w: %q has no host", ErrInvalidURL, rawURL)
	}
	if target.User != nil {
		// Credentials in the URL end up in logs and in error messages; a header
		// set with WithHeader keeps them out of both.
		return nil, fmt.Errorf("%w: credentials in the URL are not supported", ErrInvalidURL)
	}
	return target, nil
}

// ID returns the identifier of the transfer.
func (t *Transfer) ID() string { return t.id }

// URL returns the target URL.
func (t *Transfer) URL() string { return t.url }

// Dir returns the directory the file is written to.
func (t *Transfer) Dir() string { return t.cfg.Dir }

// FilePath returns the path of the finished file. Until the response headers
// are read the name comes from the URL; a Content-Disposition name from the
// server replaces it once it is known.
func (t *Transfer) FilePath() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.filePathLocked()
}

// PartPath returns the path of the file written while the transfer is in
// flight. It is renamed to FilePath when the transfer completes.
func (t *Transfer) PartPath() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.filePathLocked() + PartSuffix
}

// Status returns the current lifecycle state.
func (t *Transfer) Status() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

// RangeMode reports what the transfer has learned about the server's support
// for partial requests.
func (t *Transfer) RangeMode() RangeMode {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ranges
}

// Err returns the error that ended the transfer. It is nil while the transfer
// runs or is paused.
func (t *Transfer) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// Workers returns the number of connections the transfer is allowed to use.
func (t *Transfer) Workers() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.workers
}

// SetWorkers changes how many connections the transfer may use at once. The
// change takes effect immediately while the transfer is fetching the file in
// parallel. When a single connection is in flight and the budget grows above
// one, the current request is cut short and the transfer continues in parallel
// from the bytes already on disk; a server that does not serve ranges keeps
// using the single connection regardless.
func (t *Transfer) SetWorkers(n int) {
	if n < 0 {
		n = 0
	}
	t.mu.Lock()
	changed := n != t.workers
	t.workers = n
	cut := t.attemptCut
	promote := changed && n > 1 && !t.parallel && t.ranges != RangeUnsupported &&
		t.status == StatusDownloading
	if promote {
		t.promote = true
	}
	wake := t.wake
	t.mu.Unlock()

	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	if promote && cut != nil {
		cut()
	}
}

// Progress returns a snapshot of the transfer statistics.
func (t *Transfer) Progress() Progress {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.progressLocked(time.Now())
}

// Info returns a consistent snapshot of the transfer.
func (t *Transfer) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	path := t.filePathLocked()
	p := t.progressLocked(time.Now())
	return Info{
		ID:         t.id,
		URL:        t.url,
		FilePath:   path,
		PartPath:   path + PartSuffix,
		Status:     t.status,
		RangeMode:  t.ranges,
		Err:        t.err,
		Total:      p.Total,
		Downloaded: p.Downloaded,
		Speed:      p.Speed,
		Workers:    p.Workers,
		BlockSize:  p.BlockSize,
		Elapsed:    p.Elapsed,
	}
}

// Start begins the transfer, or continues it after a pause or a failure. It
// reports nil when the transfer is already running.
//
// A transfer that failed can be started again: it resumes from the bytes
// already on disk when the server supports ranges, and starts over when it does
// not.
func (t *Transfer) Start() error {
	t.ctl.Lock()
	defer t.ctl.Unlock()
	return t.start()
}

// start begins a run; the caller holds ctl.
func (t *Transfer) start() error {
	t.mu.Lock()
	switch t.status {
	case StatusDownloading:
		t.mu.Unlock()
		return nil
	case StatusCompleted:
		t.mu.Unlock()
		return ErrCompleted
	case StatusDeleted:
		t.mu.Unlock()
		return ErrDeleted
	}
	previous := t.runDone
	t.mu.Unlock()

	// The run of the previous attempt must have returned before this one reads
	// the state it maintains: the offset to resume from, the counters and the
	// partial file all belong to it until it is done with them.
	if previous != nil {
		<-previous
	}

	ctx, cancel := context.WithCancel(t.base)
	run := make(chan struct{})
	wake := make(chan struct{}, 1)
	t.mu.Lock()
	t.runCtx, t.cancel, t.runDone = ctx, cancel, run
	t.wake = wake
	t.promote = false
	t.status = StatusDownloading
	t.err = nil
	t.runStart = time.Now()
	// A transfer that is started again is a new thing to wait for, so the
	// channel behind Done and Wait is replaced. Anyone still waiting on the old
	// channel is woken up rather than left on a channel that will never close;
	// they see that the transfer is running again and wait for this run.
	t.replaceDoneLocked()
	// Backdating the sample window by one interval makes the first progress
	// report due immediately.
	t.sampleAt = t.runStart.Add(-progressInterval)
	t.sampleBytes = t.downloaded
	t.mu.Unlock()

	go func() {
		defer close(run)
		defer t.stopClock()
		t.work(ctx)
	}()
	return nil
}

// Pause stops the transfer and keeps the bytes received so far, so Resume can
// continue from the same offset. Pausing a paused transfer does nothing and
// reports nil. It returns once the transfer has stopped and the partial file is
// closed, which makes the file safe to inspect or move.
func (t *Transfer) Pause() error {
	t.ctl.Lock()
	defer t.ctl.Unlock()

	t.mu.Lock()
	switch t.status {
	case StatusPaused:
		t.mu.Unlock()
		return nil
	case StatusDownloading:
		// The run reads this to tell an intentional stop from a cancellation
		// and to leave the state alone.
		t.status = StatusPaused
	default:
		status := t.status
		t.mu.Unlock()
		return fmt.Errorf("%w (status %s)", ErrNotDownloading, status)
	}
	cancel, run := t.cancel, t.runDone
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if run != nil {
		<-run
	}
	return nil
}

// Resume continues a paused transfer. It reports nil when the transfer is
// already running.
func (t *Transfer) Resume() error {
	t.mu.Lock()
	status := t.status
	t.mu.Unlock()

	switch status {
	case StatusDownloading:
		return nil
	case StatusPaused:
		return t.Start()
	default:
		return fmt.Errorf("%w (status %s)", ErrNotPaused, status)
	}
}

// Restart throws away the bytes received so far and fetches the file again from
// the beginning. It works from any state, including a finished or a deleted
// transfer.
func (t *Transfer) Restart() error {
	t.ctl.Lock()
	defer t.ctl.Unlock()
	if err := t.reset(); err != nil {
		return err
	}
	return t.start()
}

// Reset throws away the bytes received so far and returns the transfer to
// StatusPending without starting it, so a scheduler decides when it runs again.
func (t *Transfer) Reset() error {
	t.ctl.Lock()
	defer t.ctl.Unlock()
	return t.reset()
}

// reset stops the run, discards the partial file and clears the counters; the
// caller holds ctl.
func (t *Transfer) reset() error {
	t.mu.Lock()
	if t.status == StatusDownloading {
		t.status = StatusPaused
	}
	cancel, run := t.cancel, t.runDone
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if run != nil {
		<-run
	}

	part, _ := t.paths()
	if err := os.Remove(part); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", part, err)
	}

	t.mu.Lock()
	t.downloaded = 0
	t.total = 0
	t.speed = 0
	t.active = 0
	t.validator = ""
	t.ranges = RangeUnknown
	t.tracker = nil
	t.blockSize = 0
	t.err = nil
	t.status = StatusPending
	// A reset is a new thing to wait for, so a caller waiting on the run that
	// was just discarded goes back to waiting for the next one.
	t.replaceDoneLocked()
	t.mu.Unlock()
	return nil
}

// Delete stops the transfer and removes the finished file and the partial file.
// The transfer ends up in StatusDeleted, and calling Delete again reports nil.
func (t *Transfer) Delete() error {
	t.ctl.Lock()
	defer t.ctl.Unlock()

	t.mu.Lock()
	if t.status == StatusDownloading {
		// The run must not report a failure for a transfer that is being
		// dropped on purpose.
		t.status = StatusDeleted
	}
	cancel, run := t.cancel, t.runDone
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if run != nil {
		<-run
	}
	t.finish(StatusDeleted, nil)

	part, final := t.paths()
	var errs []error
	for _, path := range []string{final, part} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// Wait blocks until the transfer reaches a terminal state and returns the error
// that ended it. A paused transfer is not terminal, so Wait stays blocked until
// it is resumed or deleted. Waiting on a transfer that was never started blocks
// until Start is called.
//
// A transfer that is started again while Wait is blocked is followed into the
// new run, so Wait only returns once the transfer has actually stopped for good.
func (t *Transfer) Wait() error {
	for {
		<-t.Done()
		if _, err, ok := t.terminalState(); ok {
			return err
		}
	}
}

// WaitContext is Wait with a deadline of the caller's choosing. It reports the
// context error when the deadline passes first.
func (t *Transfer) WaitContext(ctx context.Context) error {
	for {
		select {
		case <-t.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
		if _, err, ok := t.terminalState(); ok {
			return err
		}
	}
}

// terminalState reports whether the transfer has stopped for good.
func (t *Transfer) terminalState() (Status, error, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.status {
	case StatusCompleted, StatusFailed, StatusDeleted:
		return t.status, t.err, true
	}
	return t.status, nil, false
}

// replaceDoneLocked installs a fresh completion channel and wakes anyone still
// waiting on the old one. The flag rather than a sync.Once is what keeps the
// channel from being closed twice.
func (t *Transfer) replaceDoneLocked() {
	if !t.finished && t.done != nil {
		close(t.done)
	}
	t.done = make(chan struct{})
	t.finished = false
}

// Done returns a channel that is closed once the transfer reaches a terminal
// state: completed, failed or deleted. A transfer that is started again gets a
// new channel.
func (t *Transfer) Done() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

// work runs the transfer until it completes, gives up, or is stopped. It picks
// the parallel path when the budget allows it and the server serves ranges, and
// the single-connection path otherwise.
func (t *Transfer) work(ctx context.Context) {
	for {
		mode, workers := t.mode()
		if workers > 1 && mode != RangeUnsupported {
			err := t.runParallel(ctx)
			switch {
			case err == nil:
				t.finish(StatusCompleted, nil)
				t.emit()
				return
			case errors.Is(err, errFallbackSingle):
				// The server cannot serve part of the file, so the file has to
				// come over one connection. Whatever the parallel path wrote is
				// discarded, because a server that ignored a range request
				// answered with the whole body.
				t.discardProgress()
				t.setRanges(RangeUnsupported)
			case errors.Is(err, errTooSmall):
				// The server serves ranges; the file is just not worth splitting.
				// What the probe learned about range support is kept.
				t.discardProgress()
			case errors.Is(err, errPromote):
				continue
			default:
				if t.stopped() {
					return
				}
				t.finish(StatusFailed, err)
				return
			}
		}

		err := t.runSingle(ctx)
		switch {
		case err == nil:
			t.finish(StatusCompleted, nil)
			t.emit()
			return
		case errors.Is(err, errPromote):
			continue
		case t.stopped():
			return
		default:
			t.finish(StatusFailed, err)
			return
		}
	}
}

// mode reports the range knowledge and the worker budget of this run.
func (t *Transfer) mode() (RangeMode, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ranges, t.workers
}

// stopped reports whether Pause or Delete owns the state the transfer is in, in
// which case the run must not report a failure for it.
func (t *Transfer) stopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status == StatusPaused || t.status == StatusDeleted
}

// promoted reports whether the budget grew while a single-connection attempt
// was running, and clears the flag.
func (t *Transfer) promoted() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.promote {
		return false
	}
	t.promote = false
	return true
}

// finish moves the transfer into a terminal state and wakes everyone blocked on
// Done. The flag rather than a sync.Once is what lets a later run install a
// fresh channel without racing the run that just ended.
func (t *Transfer) finish(status Status, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = status
	t.err = err
	if !t.finished {
		t.finished = true
		close(t.done)
	}
}

// stopClock folds the running period into the elapsed time. It runs as the run
// returns.
func (t *Transfer) stopClock() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.runStart.IsZero() {
		t.active += time.Since(t.runStart)
		t.runStart = time.Time{}
	}
}

// progressLocked derives the statistics of a transfer; the caller holds mu.
func (t *Transfer) progressLocked(now time.Time) Progress {
	elapsed := t.active
	if !t.runStart.IsZero() {
		elapsed += now.Sub(t.runStart)
	}
	p := Progress{
		Total:      t.total,
		Downloaded: t.downloaded,
		Speed:      t.speed,
		Elapsed:    elapsed,
		Workers:    t.workers,
		BlockSize:  t.blockSize,
	}
	if t.total > 0 {
		p.Percent = float64(t.downloaded) / float64(t.total) * 100
		if p.Percent > 100 {
			p.Percent = 100
		}
		if t.speed > 0 && t.total > t.downloaded {
			p.ETA = time.Duration(float64(t.total-t.downloaded)/float64(t.speed)) * time.Second
		}
	}
	return p
}

// report records newly written bytes and, at most once per progress interval,
// hands a snapshot to the callback. The callback runs outside the lock so it
// can read the transfer, but on this goroutine, which is why it must not block.
func (t *Transfer) report(downloaded int64) {
	t.mu.Lock()
	if downloaded > t.downloaded {
		t.downloaded = downloaded
	}
	now := time.Now()
	elapsed := now.Sub(t.sampleAt)
	if elapsed < progressInterval {
		t.mu.Unlock()
		return
	}
	t.speed = int64(float64(t.downloaded-t.sampleBytes) / elapsed.Seconds())
	t.sampleAt, t.sampleBytes = now, t.downloaded
	callback := t.cfg.Progress
	var snapshot Progress
	if callback != nil {
		snapshot = t.progressLocked(now)
	}
	t.mu.Unlock()

	if callback != nil {
		callback(snapshot)
	}
}

// emit reports the finished transfer, so a callback always sees the last state
// even when the file arrived faster than one progress interval.
func (t *Transfer) emit() {
	t.mu.Lock()
	callback := t.cfg.Progress
	var snapshot Progress
	if callback != nil {
		snapshot = t.progressLocked(time.Now())
	}
	t.mu.Unlock()

	if callback != nil {
		callback(snapshot)
	}
}

// discardProgress gives up on the bytes received so far. The partial file is
// truncated by the next attempt, which opens it at offset zero.
func (t *Transfer) discardProgress() {
	t.mu.Lock()
	t.downloaded = 0
	t.total = 0
	t.tracker = nil
	t.blockSize = 0
	t.mu.Unlock()
}

// setTotal records the size the server advertised.
func (t *Transfer) setTotal(total int64) {
	t.mu.Lock()
	if total > 0 {
		t.total = total
	}
	t.mu.Unlock()
}

// setRanges records what the server said about partial requests.
func (t *Transfer) setRanges(mode RangeMode) {
	t.mu.Lock()
	t.ranges = mode
	t.mu.Unlock()
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
