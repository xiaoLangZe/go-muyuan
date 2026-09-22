package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go-muyuan/internal/engine"
)

// doneSignal is a completion channel that several goroutines may close, in
// whatever order they discover the task is over.
type doneSignal struct {
	once sync.Once
	ch   chan struct{}
}

func newDoneSignal() *doneSignal {
	return &doneSignal{ch: make(chan struct{})}
}

func (d *doneSignal) close() { d.once.Do(func() { close(d.ch) }) }

// Task is one download inside a manager. It is created by Manager.AddTask and
// is safe for use by several goroutines.
//
// Its shape follows the single-file handle the package replaced: the same
// pause, resume, restart and
// delete verbs, the same statuses and the same progress type. What the manager
// adds is a connection budget: a task is queued until the manager has a
// connection to give it, and the number of connections it gets is decided by
// the manager rather than by the task.
type Task struct {
	mgr *Manager
	tr  *engine.Transfer
	id  string

	mu         sync.Mutex
	userPaused bool
	started    bool
	terminal   bool
	closed     bool
	watching   bool
	err        error
	sig        *doneSignal

	// The event subscription, created by the first call to Events.
	evOnce   sync.Once
	evCh     chan Event
	evSignal chan struct{}
	evStop   chan struct{}
}

func newTask(m *Manager, tr *engine.Transfer) *Task {
	return &Task{mgr: m, tr: tr, id: tr.ID(), sig: newDoneSignal()}
}

// ID returns the identifier of the download.
func (t *Task) ID() string { return t.id }

// URL returns the target URL.
func (t *Task) URL() string { return t.tr.URL() }

// Dir returns the directory the file is written to.
func (t *Task) Dir() string { return t.tr.Dir() }

// FilePath returns the path of the finished file.
func (t *Task) FilePath() string { return t.tr.FilePath() }

// PartPath returns the path of the file written while the download is in
// flight.
func (t *Task) PartPath() string { return t.tr.PartPath() }

// RangeMode reports what the download has learned about the server's support
// for partial requests.
func (t *Task) RangeMode() RangeMode { return t.tr.RangeMode() }

// Workers returns how many connections the manager has given this task.
func (t *Task) Workers() int { return t.tr.Workers() }

// Status returns the current state. A task the manager has not given a
// connection to yet reports StatusQueued, and so does one whose connections were
// taken away and handed to other files; both are waiting for their turn rather
// than stopped by the caller.
func (t *Task) Status() Status {
	t.mu.Lock()
	userPaused, started, terminal, closed := t.userPaused, t.started, t.terminal, t.closed
	t.mu.Unlock()

	if terminal || closed {
		if !started {
			return StatusQueued
		}
		return t.tr.Status()
	}
	if userPaused {
		return StatusPaused
	}
	if !started {
		return StatusQueued
	}
	status := t.tr.Status()
	if status == StatusPaused {
		// Stopped by the manager to free its connections, not by the caller.
		return StatusQueued
	}
	return status
}

// Err returns the error that ended the download, or nil while it is queued,
// running or paused.
func (t *Task) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminal || t.closed {
		return t.err
	}
	return t.tr.Err()
}

// Progress returns a snapshot of the transfer statistics.
func (t *Task) Progress() Progress { return t.tr.Progress() }

// Info returns a consistent snapshot of the task.
func (t *Task) Info() Info { return t.tr.Info() }

// Pause stops the download and keeps the bytes received so far. The connections
// it was using are handed to the other files immediately; Resume puts it back in
// line for them.
func (t *Task) Pause() error {
	t.mu.Lock()
	if t.terminal || t.closed {
		status := t.tr.Status()
		t.mu.Unlock()
		return fmt.Errorf("%w (status %s)", ErrNotDownloading, status)
	}
	t.userPaused = true
	started := t.started
	t.mu.Unlock()

	if started {
		// The engine may already have finished, or not have started the run
		// yet; either way the manager's next pass leaves it alone.
		if err := t.tr.Pause(); err != nil && !errors.Is(err, ErrNotDownloading) {
			return err
		}
	}
	t.pokeEvents()
	t.mgr.poke()
	return nil
}

// Resume puts a paused task back in line for connections. It returns once the
// task is queued again, which is not the same as running: whether it runs now
// depends on the budget.
func (t *Task) Resume() error {
	t.mu.Lock()
	if t.terminal || t.closed {
		status := t.tr.Status()
		t.mu.Unlock()
		return fmt.Errorf("%w (status %s)", ErrNotPaused, status)
	}
	t.userPaused = false
	t.mu.Unlock()

	t.pokeEvents()
	t.mgr.poke()
	return nil
}

// Restart throws away the bytes received so far and downloads the file again
// from the beginning. A task that has not been given a connection yet is simply
// reset and left in the queue.
func (t *Task) Restart() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return ErrClosed
	}
	t.userPaused = false
	t.terminal = false
	t.err = nil
	started := t.started
	old := t.sig
	t.sig = newDoneSignal()
	sig := t.sig
	t.mu.Unlock()

	// Wake anyone waiting on the previous run; they re-read the state and wait
	// for this one instead.
	old.close()

	if !started || t.tr.Workers() < 1 {
		// No connections to run with: reset and let the scheduler start it.
		if err := t.tr.Reset(); err != nil {
			return err
		}
		t.pokeEvents()
		t.mgr.poke()
		return nil
	}
	if err := t.tr.Restart(); err != nil {
		return err
	}
	t.watch(sig)
	t.pokeEvents()
	t.mgr.poke()
	return nil
}

// Delete stops the download and removes the finished file and the partial file.
// It is also how a task is cancelled: there is no separate cancel, because the
// two differ only in whether the files are kept. Deleting a task that is already
// deleted reports nil.
func (t *Task) Delete() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return ErrClosed
	}
	t.terminal = true
	t.userPaused = false
	t.err = nil
	sig := t.sig
	t.mu.Unlock()

	// The engine's Delete works from any state, including a finished one, and
	// reports nil when there is nothing left to remove.
	err := t.tr.Delete()

	t.mu.Lock()
	t.err = err
	t.mu.Unlock()

	sig.close()
	// The emitter is deliberately not released here: it still has the terminal
	// event to deliver, and it closes the channel itself once that is done.
	t.pokeEvents()
	t.mgr.poke()
	return err
}

// Wait blocks until the task reaches a terminal state and returns the error that
// ended it. A queued or paused task is not terminal, so Wait stays blocked until
// it runs and finishes, is deleted, or the manager is closed.
func (t *Task) Wait() error {
	for {
		t.mu.Lock()
		sig := t.sig
		over := t.terminal || t.closed
		err := t.err
		t.mu.Unlock()

		if over {
			if err != nil {
				return err
			}
			return t.tr.Err()
		}
		<-sig.ch
	}
}

// WaitContext is Wait with a deadline of the caller's choosing. It reports the
// context error when the deadline passes first.
func (t *Task) WaitContext(ctx context.Context) error {
	for {
		t.mu.Lock()
		sig := t.sig
		over := t.terminal || t.closed
		err := t.err
		t.mu.Unlock()

		if over {
			if err != nil {
				return err
			}
			return t.tr.Err()
		}
		select {
		case <-sig.ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Done returns a channel that is closed once the task reaches a terminal state.
// A task that is restarted gets a new channel.
func (t *Task) Done() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sig.ch
}

// wantsWork reports whether the scheduler should count this task among the ones
// competing for connections.
func (t *Task) wantsWork() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.terminal && !t.closed && !t.userPaused
}

// apply brings the task in line with the number of connections the scheduler
// decided on. It runs on the scheduler goroutine and may wait for a transfer to
// stop, which is why it is never called while the manager's lock is held.
func (t *Task) apply(workers int) {
	// Anything this does can change the state a subscriber is watching.
	defer t.pokeEvents()

	t.mu.Lock()
	if t.terminal || t.closed || t.userPaused {
		t.mu.Unlock()
		return
	}
	started := t.started
	if workers < 1 {
		t.mu.Unlock()
		if started && t.tr.Status() == StatusDownloading {
			// Out of budget: keep the bytes, hand the connections over.
			_ = t.tr.Pause()
		}
		return
	}
	sig := t.sig
	t.mu.Unlock()

	if !started {
		t.mu.Lock()
		t.started = true
		t.mu.Unlock()
		if err := t.tr.Start(); err != nil {
			t.noteFinished()
			return
		}
		t.tr.SetWorkers(workers)
		t.watch(sig)
		return
	}

	switch t.tr.Status() {
	case StatusPending:
		if err := t.tr.Start(); err != nil {
			t.noteFinished()
			return
		}
		t.watch(sig)
	case StatusPaused:
		if err := t.tr.Resume(); err != nil {
			return
		}
	case StatusDownloading:
	case StatusCompleted, StatusFailed, StatusDeleted:
		return
	}
	t.tr.SetWorkers(workers)
}

// watch keeps an eye on the current run so the task is marked finished when it
// ends, whatever ends it. The engine's Wait follows a transfer into a new run,
// so a pause and resume in between does not end the watch early.
func (t *Task) watch(sig *doneSignal) {
	t.mu.Lock()
	if t.watching {
		t.mu.Unlock()
		return
	}
	t.watching = true
	t.mu.Unlock()

	go func() {
		err := t.tr.Wait()

		t.mu.Lock()
		t.watching = false
		t.terminal = true
		if !t.closed {
			t.err = err
		}
		current := t.sig
		t.mu.Unlock()

		current.close()
		t.pokeEvents()
		t.mgr.poke()
	}()
}

// noteFinished marks a task finished when starting it turned out to be
// impossible because it is already in a terminal state.
func (t *Task) noteFinished() {
	t.mu.Lock()
	t.terminal = true
	if !t.closed {
		t.err = t.tr.Err()
	}
	sig := t.sig
	t.mu.Unlock()
	sig.close()
	t.mgr.poke()
}

// markClosed ends a task that the manager is shutting down. It does not touch
// the files, so a task that was part way through keeps what it downloaded.
func (t *Task) markClosed() {
	t.mu.Lock()
	t.closed = true
	if !t.terminal && t.err == nil {
		t.err = ErrClosed
	}
	sig := t.sig
	t.mu.Unlock()
	sig.close()
	// The manager is going away and a consumer may never read again, so the
	// emitter is released rather than left waiting for a reader.
	t.pokeEvents()
	t.stopEvents()
}
