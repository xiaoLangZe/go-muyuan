package manager

import (
	"net/http"
	"time"

	"go-muyuan/internal/engine"
)

// TaskOption configures one task. Every task starts from the manager's
// defaults and then applies the options given to AddTask, so an option passed
// here overrides the manager's value for this task alone.
type TaskOption = engine.Option

// WithFileName overrides the file name, which is otherwise taken from the
// server's Content-Disposition header, or from the URL when the server names
// nothing.
func WithFileName(name string) TaskOption { return engine.WithFileName(name) }

// WithOverwrite allows the task to replace an existing file. Without it, a free
// name is chosen by appending a counter: report.pdf, report_1.pdf, ...
func WithOverwrite(overwrite bool) TaskOption { return engine.WithOverwrite(overwrite) }

// WithHeader adds a request header to this task, for example an authorization
// token. It can be used several times; a repeated key is sent as a repeated
// header.
func WithHeader(key, value string) TaskOption { return engine.WithHeader(key, value) }

// WithHeaders adds every entry of the map as a request header to this task.
func WithHeaders(headers map[string]string) TaskOption { return engine.WithHeaders(headers) }

// WithRetries sets how many further attempts follow a failed transfer.
func WithRetries(retries int) TaskOption { return engine.WithRetries(retries) }

// WithRetryDelay sets the wait before the first retry; the wait doubles on
// every further attempt, up to 30 seconds.
func WithRetryDelay(delay time.Duration) TaskOption { return engine.WithRetryDelay(delay) }

// WithResponseTimeout bounds the wait for response headers. It does not limit
// how long the body may take.
func WithResponseTimeout(timeout time.Duration) TaskOption {
	return engine.WithResponseTimeout(timeout)
}

// WithStallTimeout gives up on a transfer that receives no data for this long,
// which is retried like any other transient failure. Zero disables the check.
func WithStallTimeout(timeout time.Duration) TaskOption {
	return engine.WithStallTimeout(timeout)
}

// WithBufferSize sets the size of the copy buffer used by the single-connection
// path.
func WithBufferSize(size int) TaskOption { return engine.WithBufferSize(size) }

// WithChunkSize pins the length in bytes of one piece a connection fetches,
// which disables the automatic choice for this task.
func WithChunkSize(size int64) TaskOption { return engine.WithBlockSize(size) }

// WithChunkBounds bounds the length of a piece when it is chosen
// automatically.
func WithChunkBounds(min, max int64) TaskOption { return engine.WithBlockBounds(min, max) }

// WithHTTPClient uses a caller supplied HTTP client for this task. The client
// owns its transport, so the timeouts and the proxy of this package do not
// apply to it, and only the pre-request host check runs. Do not set
// Client.Timeout on it: that field bounds the whole transfer rather than the
// wait for response headers, so it aborts long downloads.
func WithHTTPClient(client *http.Client) TaskOption { return engine.WithHTTPClient(client) }

// WithAllowUnsafeHosts permits this task to reach the local machine or a
// private network. The guard it disables is what keeps an untrusted URL from
// reaching internal services, so enable it only when the URL is known to be
// trusted.
func WithAllowUnsafeHosts(allow bool) TaskOption { return engine.WithAllowUnsafeHosts(allow) }

// WithProxy routes this task's connections through the proxy at rawURL, which
// must have an http, https or socks5 scheme; credentials go in the URL's
// userinfo (http://user:pass@host:port, socks5://user:pass@host:port).
//
// Through a proxy the connection is dialed to the proxy, so the dial-time
// address check sees the proxy's address, not the target's: the target's host
// is still checked before the request is built, but the address the proxy
// resolves it to is not visible and cannot be checked. A proxy inside the local
// network needs WithAllowUnsafeHosts to be dialable at all.
func WithProxy(rawURL string) TaskOption { return engine.WithProxy(rawURL) }

// WithProgress registers a callback that this task reports its statistics to.
// It runs on the transfer goroutine at most every 250ms plus once when the file
// completes, so it must not block or call methods that wait for the transfer to
// stop, such as Pause, Wait or Delete.
//
// Events is the other way to follow a task, and the one to prefer when the
// consumer may be slower than the transfer: a callback holds up the transfer
// goroutine, while an unread event channel does not.
func WithProgress(callback func(Progress)) TaskOption { return engine.WithProgress(callback) }
