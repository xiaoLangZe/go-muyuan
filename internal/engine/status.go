// Package engine performs one file transfer over HTTP. It is the shared core of
// the public downloader and manager packages, which alias its types and wrap
// its Transfer in their own handles.
//
// A transfer can fetch a file over a single connection or, when the worker
// budget allows it and the server supports range requests, over several
// concurrent connections. Both modes share the same lifecycle: start, pause,
// resume, restart and delete, with the same partial-file and rename rules.
package engine

import (
	"fmt"
	"time"
)

// Status is the lifecycle state of a transfer.
type Status int32

const (
	// StatusPending is a transfer that was created but not started.
	StatusPending Status = iota
	// StatusDownloading is a transfer in progress.
	StatusDownloading
	// StatusPaused is a transfer stopped by Pause. The bytes received so far
	// are kept on disk until Resume, Restart or Delete.
	StatusPaused
	// StatusCompleted is a transfer whose file is complete and renamed.
	StatusCompleted
	// StatusFailed is a transfer that stopped on an error. Start retries it
	// from the bytes already received; Restart starts over.
	StatusFailed
	// StatusDeleted is a transfer whose file and partial file were removed.
	StatusDeleted
	// StatusQueued is a transfer waiting for a caller-provided budget to give it
	// a worker. Only a scheduler reports it; a transfer driven directly is
	// never queued.
	StatusQueued
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
	case StatusQueued:
		return "queued"
	default:
		return fmt.Sprintf("status(%d)", int32(s))
	}
}

// RangeMode records what the server said about partial requests.
type RangeMode int8

const (
	// RangeUnknown means no range request has been answered yet.
	RangeUnknown RangeMode = iota
	// RangeSupported means the server answers with 206 and a matching offset.
	RangeSupported
	// RangeUnsupported means the server ignores the Range header, so an
	// interrupted transfer has to start over.
	RangeUnsupported
)

func (m RangeMode) String() string {
	switch m {
	case RangeSupported:
		return "supported"
	case RangeUnsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// Progress is a snapshot of a transfer's statistics.
type Progress struct {
	// Total is the full size in bytes, or zero when the server did not
	// advertise one.
	Total int64
	// Downloaded is the number of bytes written so far. With more than one
	// worker it counts only the bytes of completed blocks, so it trails the
	// work in flight by at most one worker's portion.
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
	// Workers is the number of connections the transfer is allowed to use.
	Workers int
	// BlockSize is the length in bytes of one piece a worker claims, or zero
	// when the transfer is not fetching in parallel.
	BlockSize int64
}

// Info is a consistent snapshot of a transfer.
type Info struct {
	ID         string
	URL        string
	FilePath   string
	PartPath   string
	Status     Status
	RangeMode  RangeMode
	Err        error
	Total      int64
	Downloaded int64
	Speed      int64
	Workers    int
	BlockSize  int64
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
