package go_muyuan

import (
	"fmt"
	"time"
)

// Status is the lifecycle state of a download.
type Status int32

const (
	// StatusPending is a download that was created but not started.
	StatusPending Status = iota
	// StatusDownloading is a transfer in progress.
	StatusDownloading
	// StatusPaused is a transfer stopped by Pause. The bytes received so far
	// are kept on disk until Resume, Restart or Delete.
	StatusPaused
	// StatusCompleted is a download whose file is complete and renamed.
	StatusCompleted
	// StatusFailed is a download that stopped on an error. Start retries it
	// from the bytes already received; Restart starts over.
	StatusFailed
	// StatusDeleted is a download whose file and partial file were removed.
	StatusDeleted
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusDownloading:
		return "downloading"
	case StatusPaused:
		return "paused"
	case StatusCompleted:
		return "completed"
	case StatusFailed:
		return "failed"
	case StatusDeleted:
		return "deleted"
	default:
		return fmt.Sprintf("status(%d)", int32(s))
	}
}

// Progress is a snapshot of a transfer's statistics.
type Progress struct {
	// Total is the full size in bytes, or zero when the server did not
	// advertise one.
	Total int64
	// Downloaded is the number of bytes written so far.
	Downloaded int64
	// Speed is the transfer rate in bytes per second, measured over the last
	// sampling window.
	Speed int64
	// ETA estimates the remaining time, or zero when it cannot be derived.
	ETA time.Duration
	// Percent is Downloaded of Total, or zero when Total is unknown.
	Percent float64
	// Elapsed is the time spent transferring, excluding pauses.
	Elapsed time.Duration
}

// Info is a consistent snapshot of a Handle.
type Info struct {
	ID         string
	URL        string
	FilePath   string
	PartPath   string
	Status     Status
	Err        error
	Total      int64
	Downloaded int64
	Speed      int64
	Elapsed    time.Duration
}

// Percent returns the share of the file that is on disk, or zero when the
// server did not advertise a size.
func (i Info) Percent() float64 {
	if i.Total <= 0 {
		return 0
	}
	return float64(i.Downloaded) / float64(i.Total) * 100
}
