package engine

import (
	"errors"
	"fmt"
	"net/http"

	"go-muyuan/internal/hostguard"
)

// Sentinel errors reported by the transfer engine. Failures returned by a
// transfer wrap one of them where applicable, so errors.Is works.
var (
	// ErrInvalidURL reports a target that is not an absolute http or https URL.
	ErrInvalidURL = errors.New("invalid URL")

	// ErrBlockedHost reports a host that resolves to a loopback, private,
	// link-local, multicast or otherwise reserved address. See the
	// allow-unsafe-hosts option.
	ErrBlockedHost = hostguard.ErrBlocked

	// ErrNotPaused reports Resume on a transfer that is not paused.
	ErrNotPaused = errors.New("download is not paused")

	// ErrNotDownloading reports Pause on a transfer that is no longer running.
	ErrNotDownloading = errors.New("download is not running")

	// ErrCompleted reports Start on a transfer that already finished. Use
	// Restart to fetch the file again.
	ErrCompleted = errors.New("download already completed")

	// ErrDeleted reports an operation on a transfer that was deleted.
	ErrDeleted = errors.New("download was deleted")

	// ErrStalled reports a transfer that received no data for the configured
	// stall timeout. The attempt is retried unless retries are exhausted.
	ErrStalled = errors.New("transfer stalled, no data received")

	// ErrInvalidProxy reports a proxy URL that is not an http, https or socks5
	// URL with a host.
	ErrInvalidProxy = errors.New("invalid proxy URL")
)

// The two internal control-flow signals of the transfer loop. They are never
// returned to a caller: work() consumes them and decides what to do next.
var (
	// errFallbackSingle means the server cannot serve part of the file, so the
	// parallel path is not available and the single-connection path has to run
	// instead.
	errFallbackSingle = errors.New("server does not support range requests")

	// errTooSmall means the file is small enough that splitting it would cost
	// more than it saves. The server may well serve ranges; the transfer simply
	// chooses not to ask for them.
	errTooSmall = errors.New("file is too small to split")

	// errPromote means the worker budget grew while a single-connection attempt
	// was running, so the attempt has to be restarted with several workers.
	errPromote = errors.New("worker budget changed")
)

// HTTPStatusError reports a response with a status code the transfer cannot
// use, such as 404 or 500.
type HTTPStatusError struct {
	StatusCode int
	Status     string
	URL        string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("unexpected HTTP status %s from %s", e.Status, e.URL)
}

// Retryable reports whether a further attempt could succeed.
func (e *HTTPStatusError) Retryable() bool {
	switch e.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return e.StatusCode >= 500
}

// transientError marks a failure that another attempt can recover from, such as
// a dropped connection or a short body.
type transientError struct {
	err error
}

func (e *transientError) Error() string { return e.err.Error() }

func (e *transientError) Unwrap() error { return e.err }

func transient(err error) error { return &transientError{err: err} }

// isTransient reports whether err is worth retrying.
func isTransient(err error) bool {
	var marked *transientError
	if errors.As(err, &marked) {
		return true
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return status.Retryable()
	}
	return false
}
