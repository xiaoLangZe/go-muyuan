package muyuan

import "time"

// EventType tells what an Event is reporting.
type EventType int

const (
	// EventProgress reports that bytes arrived or the rate changed. Progress
	// events are throttled, so a task may finish between two of them.
	EventProgress EventType = iota
	// EventStatus reports that the task changed state. Status events are not
	// throttled: every state the task passes through is delivered.
	EventStatus
	// EventError reports that the task ended in failure. It follows the
	// terminal EventStatus and carries the error that ended it.
	EventError
)

func (t EventType) String() string {
	switch t {
	case EventProgress:
		return "progress"
	case EventStatus:
		return "status"
	case EventError:
		return "error"
	default:
		return "event"
	}
}

// Event is one observation of a task: a progress snapshot, a state change, or
// the error a task ended with.
type Event struct {
	// TaskID identifies the task the event belongs to.
	TaskID string
	// Type is what this event reports.
	Type EventType
	// Status is the state of the task when the event was made.
	Status Status
	// Progress is the statistics of the task when the event was made.
	Progress Progress
	// Err is set on EventError only.
	Err error
	// At is when the event was made.
	At time.Time
}

const (
	// eventsTick bounds how long a change can go unnoticed when nothing else
	// wakes the emitter.
	eventsTick = 100 * time.Millisecond

	// eventsProgressMin is the shortest gap between two progress events. It is
	// wider than the engine's own sampling interval so a burst of samples does
	// not turn into a burst of events.
	eventsProgressMin = 200 * time.Millisecond

	// eventsBuffer is how many events may wait for a consumer before the
	// emitter blocks.
	eventsBuffer = 16
)

// Events returns a channel of progress and status events for this task.
//
// The channel is created the first time Events is called and is closed once the
// task ends, after the event that reports the ending has been delivered, so
//
//	for ev := range task.Events() {
//		fmt.Printf("%s %.1f%%\n", ev.Status, ev.Progress.Percent)
//	}
//
// runs from start to finish.
//
// A consumer that stops reading does not hold up the download: the transfer runs
// on its own goroutine and only the emitter waits on the channel. The emitter is
// released when the task is deleted or the manager is closed, so a caller that
// drops a subscription should Delete the task or Close the manager rather than
// leave it running. Progress is reported at most every 200ms and every state
// change is reported, so the channel gets at most a handful of events per
// second.
//
// Progress, Info and Status read the same values at any moment, so a caller that
// would rather poll can ignore this method entirely.
func (t *Task) Events() <-chan Event {
	t.evOnce.Do(func() {
		t.mu.Lock()
		ch := make(chan Event, eventsBuffer)
		signal := make(chan struct{}, 1)
		stop := make(chan struct{})
		t.evCh, t.evSignal, t.evStop = ch, signal, stop
		t.mu.Unlock()
		go t.emitLoop(ch, signal, stop)
	})

	t.mu.Lock()
	defer t.mu.Unlock()
	return t.evCh
}

// pokeEvents nudges the emitter, if anyone is listening.
func (t *Task) pokeEvents() {
	t.mu.Lock()
	signal := t.evSignal
	t.mu.Unlock()
	if signal == nil {
		return
	}
	select {
	case signal <- struct{}{}:
	default:
	}
}

// stopEvents releases the emitter. It is called when the task ends, so a
// subscription never outlives the task that owns it.
func (t *Task) stopEvents() {
	t.mu.Lock()
	stop := t.evStop
	t.mu.Unlock()
	if stop == nil {
		return
	}
	select {
	case <-stop:
	default:
		close(stop)
	}
}

// emitLoop turns the task's state into events until the task ends or the
// subscription is released.
func (t *Task) emitLoop(ch chan Event, signal chan struct{}, stop chan struct{}) {
	defer close(ch)

	ticker := time.NewTicker(eventsTick)
	defer ticker.Stop()

	var (
		started    bool
		lastStatus Status
		lastBytes  int64
		lastSpeed  int64
		lastSent   time.Time
	)

	for {
		info := t.Info()
		status := t.Status()
		now := time.Now()

		event, terminal := t.nextEvent(info, status, started, lastStatus, lastBytes, lastSpeed, lastSent, now)
		if event != nil {
			select {
			case ch <- *event:
				lastSent = now
				started = true
				lastStatus, lastBytes, lastSpeed = status, info.Downloaded, info.Speed
			case <-stop:
				return
			}
		}

		if terminal {
			// The ending has been reported. Deliver the error as well so a
			// consumer that ranges over the channel sees why it stopped.
			if info.Err != nil {
				select {
				case ch <- Event{
					TaskID:   t.ID(),
					Type:     EventError,
					Status:   status,
					Progress: progressOf(info),
					Err:      info.Err,
					At:       time.Now(),
				}:
				case <-stop:
				}
			}
			return
		}

		select {
		case <-stop:
			return
		case <-signal:
		case <-ticker.C:
		}
	}
}

// progressOf lifts the statistics out of a snapshot. The percent and the
// estimate are derived here rather than carried in the snapshot, and they use
// the same rule the engine uses, so a value read from an event and one read
// from Progress agree.
func progressOf(info Info) Progress {
	p := Progress{
		Total:      info.Total,
		Downloaded: info.Downloaded,
		Speed:      info.Speed,
		Elapsed:    info.Elapsed,
		Workers:    info.Workers,
		BlockSize:  info.BlockSize,
	}
	if info.Total > 0 {
		p.Percent = float64(info.Downloaded) / float64(info.Total) * 100
		if p.Percent > 100 {
			p.Percent = 100
		}
		if info.Speed > 0 && info.Total > info.Downloaded {
			p.ETA = time.Duration(float64(info.Total-info.Downloaded)/float64(info.Speed)) * time.Second
		}
	}
	return p
}

// nextEvent decides what, if anything, is worth reporting right now. It reports
// the second return value as true once the task has reached a terminal state and
// that state has been delivered.
func (t *Task) nextEvent(info Info, status Status, started bool, lastStatus Status, lastBytes, lastSpeed int64, lastSent, now time.Time) (*Event, bool) {
	terminal := status == StatusCompleted || status == StatusFailed || status == StatusDeleted

	// A state change is always worth an event, and so is the terminal state
	// even when the state itself did not change.
	if !started || status != lastStatus {
		return &Event{
			TaskID:   t.ID(),
			Type:     EventStatus,
			Status:   status,
			Progress: progressOf(info),
			At:       now,
		}, terminal
	}
	if terminal {
		return nil, true
	}

	// Progress: only when something moved and the throttle allows it.
	moved := info.Downloaded != lastBytes || info.Speed != lastSpeed
	if !moved || now.Sub(lastSent) < eventsProgressMin {
		return nil, false
	}
	return &Event{
		TaskID:   t.ID(),
		Type:     EventProgress,
		Status:   status,
		Progress: progressOf(info),
		At:       now,
	}, false
}
