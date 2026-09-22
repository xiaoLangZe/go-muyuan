package engine

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xiaoLangZe/go-muyuan/internal/hostguard"
)

const (
	// DefaultRetries is how many further attempts follow a failed transfer.
	DefaultRetries = 3
	// DefaultRetryDelay is the wait before the first retry.
	DefaultRetryDelay = time.Second
	// DefaultResponseWait bounds the wait for response headers.
	DefaultResponseWait = 30 * time.Second
	// DefaultStallTimeout gives up on a transfer that receives no data for this
	// long. Zero disables the check.
	DefaultStallTimeout = 60 * time.Second
	// DefaultBufferSize is the size of the copy buffer.
	DefaultBufferSize = 64 << 10

	// DefaultWorkers is one connection: the single-stream path.
	DefaultWorkers = 1
	// DefaultChunkMin and DefaultChunkMax bound one claimed piece.
	DefaultChunkMin = 256 << 10
	DefaultChunkMax = 16 << 20

	// progressInterval is how often a transfer hands a snapshot to the progress
	// callback.
	progressInterval = 250 * time.Millisecond

	// maxRetryDelay caps the doubling of the retry delay.
	maxRetryDelay = 30 * time.Second
)

// Config is the resolved configuration of one transfer.
type Config struct {
	// Dir is the directory the file is written to.
	Dir string
	// FileName overrides the name, which is otherwise taken from the server's
	// Content-Disposition header or from the URL.
	FileName string
	// Overwrite allows replacing an existing file instead of picking a free
	// name.
	Overwrite bool
	// Headers are added to every request.
	Headers http.Header
	// Client performs the requests. When nil, one is built with the host guard
	// wired into its dialer.
	Client *http.Client
	// Retries is how many further attempts follow a failed transfer.
	Retries int
	// RetryDelay is the wait before the first retry; it doubles per attempt.
	RetryDelay time.Duration
	// ResponseWait bounds the wait for response headers.
	ResponseWait time.Duration
	// StallTimeout gives up on a transfer that receives no data for this long.
	StallTimeout time.Duration
	// BufferSize is the size of the copy buffer used by the single-connection
	// path.
	BufferSize int
	// Workers is how many connections the transfer may use at once. One means
	// a single connection, which is also the mode used when the server does not
	// serve ranges.
	Workers int
	// BlockSize pins the length of one claimed piece. Zero selects a length
	// from ChunkMin, ChunkMax and the observed throughput.
	BlockSize int64
	// ChunkMin and ChunkMax bound an automatically chosen BlockSize.
	ChunkMin int64
	ChunkMax int64
	// Progress is called with transfer statistics on the transfer goroutine.
	Progress func(Progress)
	// AllowUnsafe permits targets inside the local machine or a private
	// network.
	AllowUnsafe bool
	// Proxy routes every connection through the proxy at this URL. Only http,
	// https and socks5 schemes are accepted; credentials go in the URL's
	// userinfo. An empty string connects directly.
	Proxy string
}

// Option configures a transfer.
type Option func(*Config)

// WithDir sets the directory the file is written to. It is created if missing.
func WithDir(dir string) Option {
	return func(c *Config) { c.Dir = dir }
}

// WithFileName overrides the file name, which is otherwise taken from the
// server's Content-Disposition header, or from the URL when the server names
// nothing.
func WithFileName(name string) Option {
	return func(c *Config) { c.FileName = name }
}

// WithOverwrite allows the transfer to replace an existing file. Without it, a
// free name is chosen by appending a counter: report.pdf, report_1.pdf, ...
func WithOverwrite(overwrite bool) Option {
	return func(c *Config) { c.Overwrite = overwrite }
}

// WithHeader adds a request header, for example an authorization token. It can
// be used several times; a repeated key is sent as a repeated header.
func WithHeader(key, value string) Option {
	return func(c *Config) { c.Headers.Add(key, value) }
}

// WithHeaders adds every entry of the map as a request header.
func WithHeaders(headers map[string]string) Option {
	return func(c *Config) {
		for key, value := range headers {
			c.Headers.Add(key, value)
		}
	}
}

// WithHTTPClient uses a caller supplied HTTP client. The client owns its
// transport, so the response and stall timeouts do not apply to it, and only
// the pre-request host check runs. Do not set Client.Timeout on it: that field
// bounds the whole transfer rather than the wait for response headers, so it
// aborts long downloads.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Config) { c.Client = client }
}

// WithRetries sets how many further attempts follow a failed transfer.
func WithRetries(retries int) Option {
	return func(c *Config) { c.Retries = retries }
}

// WithRetryDelay sets the wait before the first retry; the wait doubles on
// every further attempt, up to 30 seconds.
func WithRetryDelay(delay time.Duration) Option {
	return func(c *Config) { c.RetryDelay = delay }
}

// WithResponseTimeout bounds the wait for response headers. It does not limit
// how long the body may take.
func WithResponseTimeout(timeout time.Duration) Option {
	return func(c *Config) { c.ResponseWait = timeout }
}

// WithStallTimeout gives up on a transfer that receives no data for this long,
// which is retried like any other transient failure. Zero disables the check.
func WithStallTimeout(timeout time.Duration) Option {
	return func(c *Config) { c.StallTimeout = timeout }
}

// WithBufferSize sets the size of the copy buffer used by the
// single-connection path.
func WithBufferSize(size int) Option {
	return func(c *Config) { c.BufferSize = size }
}

// WithProgress registers a callback for transfer statistics. It runs on the
// transfer goroutine at most every 250ms plus once when the file completes, so
// it must not block or call methods that wait for the transfer to stop, such as
// Pause, Wait or Delete.
func WithProgress(callback func(Progress)) Option {
	return func(c *Config) { c.Progress = callback }
}

// WithAllowUnsafeHosts permits targets inside the local machine or a private
// network. The guard it disables is what keeps an untrusted URL from reaching
// internal services, so enable it only when the URLs are known to be trusted.
func WithAllowUnsafeHosts(allow bool) Option {
	return func(c *Config) { c.AllowUnsafe = allow }
}

// WithProxy routes every connection through the proxy at rawURL, which must
// have an http, https or socks5 scheme; credentials go in the URL's userinfo
// (http://user:pass@host:port, socks5://user:pass@host:port).
//
// Through a proxy the connection is dialed to the proxy, so the dial-time
// address check sees the proxy's address, not the target's: the target's host
// is still checked before the request is built, but the address the proxy
// resolves it to is not visible and cannot be checked. A proxy inside the
// local network needs WithAllowUnsafeHosts to be dialable at all. The option
// does nothing when WithHTTPClient supplies the client, because that client
// owns its transport.
func WithProxy(rawURL string) Option {
	return func(c *Config) { c.Proxy = rawURL }
}

// WithWorkers sets how many connections the transfer may use at once. One, the
// default, fetches the file over a single connection. Above one the transfer
// asks the server for the file in parallel pieces, and falls back to a single
// connection when the server does not serve ranges.
func WithWorkers(workers int) Option {
	return func(c *Config) { c.Workers = workers }
}

// WithBlockSize pins the length of one claimed piece, which disables the
// automatic choice. It is only used when more than one worker is allowed.
func WithBlockSize(size int64) Option {
	return func(c *Config) { c.BlockSize = size }
}

// WithBlockBounds bounds an automatically chosen piece length.
func WithBlockBounds(min, max int64) Option {
	return func(c *Config) {
		c.ChunkMin, c.ChunkMax = min, max
	}
}

// DefaultConfig returns the configuration a transfer starts from.
func DefaultConfig() Config {
	return Config{
		Dir:          ".",
		Headers:      make(http.Header),
		Retries:      DefaultRetries,
		RetryDelay:   DefaultRetryDelay,
		ResponseWait: DefaultResponseWait,
		StallTimeout: DefaultStallTimeout,
		BufferSize:   DefaultBufferSize,
		Workers:      DefaultWorkers,
		ChunkMin:     DefaultChunkMin,
		ChunkMax:     DefaultChunkMax,
	}
}

// NormalizeConfig fills in the values left at zero, so a caller that builds a
// Config directly still gets a usable one.
func NormalizeConfig(c *Config) {
	if c.Dir == "" {
		c.Dir = "."
	}
	if c.Headers == nil {
		c.Headers = make(http.Header)
	}
	if c.BufferSize <= 0 {
		c.BufferSize = DefaultBufferSize
	}
	if c.Retries < 0 {
		c.Retries = 0
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = DefaultRetryDelay
	}
	if c.ResponseWait <= 0 {
		c.ResponseWait = DefaultResponseWait
	}
	if c.StallTimeout < 0 {
		c.StallTimeout = 0
	}
	if c.Workers < 1 {
		c.Workers = DefaultWorkers
	}
	if c.ChunkMin <= 0 {
		c.ChunkMin = DefaultChunkMin
	}
	if c.ChunkMax <= 0 {
		c.ChunkMax = DefaultChunkMax
	}
	if c.ChunkMax < c.ChunkMin {
		c.ChunkMax = c.ChunkMin
	}
	if c.BlockSize > 0 {
		if c.BlockSize < c.ChunkMin {
			c.BlockSize = c.ChunkMin
		}
		if c.BlockSize > c.ChunkMax {
			c.BlockSize = c.ChunkMax
		}
	}
}

// parseProxy accepts an http, https or socks5 proxy URL with a host. An empty
// string means no proxy.
func parseProxy(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, nil
	}
	proxy, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProxy, err)
	}
	switch proxy.Scheme {
	case "http", "https", "socks5":
	case "":
		return nil, fmt.Errorf("%w: %q has no scheme", ErrInvalidProxy, rawURL)
	default:
		return nil, fmt.Errorf("%w: scheme %q is not supported", ErrInvalidProxy, proxy.Scheme)
	}
	if proxy.Hostname() == "" {
		return nil, fmt.Errorf("%w: %q has no host", ErrInvalidProxy, rawURL)
	}
	return proxy, nil
}

// NewClient builds the client used when the caller does not supply one. It has
// no overall timeout on purpose: a deadline that covers the whole transfer
// would abort exactly the long downloads this engine exists for. Dialing and
// the response headers are bounded instead.
//
// The host guard is wired into the dialer, so every connection is checked
// against the blocked address ranges even in the parallel path. With a proxy
// configured, connections are dialed to the proxy, so what the dial-time check
// sees is the proxy's address; the target's host is checked before the request
// is built either way.
func NewClient(cfg Config) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	dial := dialer.DialContext
	if !cfg.AllowUnsafe {
		dial = (&hostguard.Dialer{Base: dialer}).DialContext
	}
	transport := &http.Transport{
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		DisableCompression:    true,
		ResponseHeaderTimeout: cfg.ResponseWait,
	}
	if proxy, err := parseProxy(cfg.Proxy); err == nil && proxy != nil {
		// socks5 needs no extra machinery: Transport.Proxy accepts a socks5
		// URL and does the handshake itself, userinfo and all.
		transport.Proxy = http.ProxyURL(proxy)
	}
	return &http.Client{Transport: transport}
}
