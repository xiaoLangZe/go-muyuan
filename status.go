package muyuan

import "github.com/xiaoLangZe/go-muyuan/internal/engine"

// Status is the lifecycle state of a download.
type Status = engine.Status

const (
	// StatusPending is a task whose transfer was prepared but not started.
	StatusPending = engine.StatusPending
	// StatusQueued is a task waiting for the manager to give it a connection.
	// A task whose connections were taken away and handed to other files also
	// reports it: it is waiting for its turn rather than stopped by the caller.
	StatusQueued = engine.StatusQueued
	// StatusDownloading is a transfer in progress.
	StatusDownloading = engine.StatusDownloading
	// StatusPaused is a task stopped by Pause. The bytes received so far are
	// kept on disk until Resume, Restart or Delete.
	StatusPaused = engine.StatusPaused
	// StatusCompleted is a task whose file is complete and renamed.
	StatusCompleted = engine.StatusCompleted
	// StatusFailed is a task that stopped on an error. Restart starts it over.
	StatusFailed = engine.StatusFailed
	// StatusDeleted is a task whose file and partial file were removed.
	StatusDeleted = engine.StatusDeleted
)

// RangeMode records what a task has learned about the server's support for
// partial requests.
type RangeMode = engine.RangeMode

const (
	// RangeUnknown means no range request has been answered yet.
	RangeUnknown = engine.RangeUnknown
	// RangeSupported means the server answers with 206 and a matching offset.
	// It is what makes fetching a file over several connections possible.
	RangeSupported = engine.RangeSupported
	// RangeUnsupported means the server ignores the Range header, so a file is
	// fetched over one connection and an interrupted transfer has to start over
	// from zero.
	RangeUnsupported = engine.RangeUnsupported
)

// Progress is a snapshot of a transfer's statistics.
//
// Downloaded counts only the bytes of completed pieces, so with several
// connections it trails the work in flight by at most one piece per connection.
type Progress = engine.Progress

// Info is a consistent snapshot of a task.
type Info = engine.Info
