package go_muyuan

import (
	"errors"
	"fmt"
	"net/http"
)

// Sentinel errors reported by this package. Failures returned by a Handle wrap
// one of them where applicable, so errors.Is works:
//
//	if errors.Is(err, go_muyuan.ErrBlockedHost) {
//		// the URL points somewhere the downloader refuses to go
//	}
var (
	// ErrInvalidURL reports a target that is not an absolute http or https URL.
	ErrInvalidURL = errors.New("invalid URL")

	// ErrBlockedHost reports a host that resolves to a loopback, private,
	// link-local, multicast or otherwise reserved address. See
	// WithAllowUnsafeHosts.
	ErrBlockedHost = errors.New("host is not allowed")

	// ErrNotPaused reports Resume on a download that is not paused.
	ErrNotPaused = errors.New("download is not paused")

	// ErrNotDownloading reports Pause on a download that is no longer running.
	ErrNotDownloading = errors.New("download is not running")

	// ErrCompleted reports Start on a download that already finished. Use
	// Restart to fetch the file again.
	ErrCompleted = errors.New("download already completed")

	// ErrDeleted reports an operation on a download that was deleted.
	ErrDeleted = errors.New("download was deleted")

	// ErrStalled reports a transfer that received no data for the timeout set
	// by WithStallTimeout. The attempt is retried unless retries are exhausted.
	ErrStalled = errors.New("transfer stalled, no data received")
)

// HTTPStatusError reports a response with a status code the downloader cannot
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
