package manager

import "go-muyuan/internal/engine"

// Sentinel errors reported by this package. Failures returned by a task wrap one
// of them where applicable, so errors.Is works:
//
//	if errors.Is(err, manager.ErrBlockedHost) {
//		// the URL points somewhere the downloader refuses to go
//	}
var (
	// ErrInvalidURL reports a target that is not an absolute http or https URL.
	ErrInvalidURL = engine.ErrInvalidURL

	// ErrInvalidProxy reports a proxy that is not an http, https or socks5 URL
	// with a host. See WithProxy.
	ErrInvalidProxy = engine.ErrInvalidProxy

	// ErrBlockedHost reports a host that resolves to a loopback, private,
	// link-local, multicast or otherwise reserved address, and a proxy on such
	// an address. See WithAllowUnsafeHosts.
	//
	// A block is reported both before the request is built and when the
	// connection is dialed, and both match this value. With a proxy configured
	// the dial-time check sees the proxy's address, so an internal proxy needs
	// WithAllowUnsafeHosts while the target's host is checked either way.
	ErrBlockedHost = engine.ErrBlockedHost

	// ErrNotPaused reports Resume on a task that is not paused.
	ErrNotPaused = engine.ErrNotPaused

	// ErrNotDownloading reports Pause on a task that is no longer running.
	ErrNotDownloading = engine.ErrNotDownloading

	// ErrCompleted reports an operation on a task that already finished. Use
	// Restart to fetch the file again.
	ErrCompleted = engine.ErrCompleted

	// ErrDeleted reports an operation on a task that was deleted.
	ErrDeleted = engine.ErrDeleted

	// ErrStalled reports a transfer that received no data for the timeout set
	// by WithStallTimeout. The attempt is retried unless retries are exhausted.
	ErrStalled = engine.ErrStalled
)

// HTTPStatusError reports a response with a status code the downloader cannot
// use, such as 404 or 500.
type HTTPStatusError = engine.HTTPStatusError
