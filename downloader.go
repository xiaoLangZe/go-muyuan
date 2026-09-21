// Package go_muyuan downloads a file over HTTP and hands back a handle that can
// be paused, resumed, restarted and deleted.
//
// A download runs in its own goroutine, so the caller only needs the handle: it
// carries the state, the statistics and the resulting path.
//
//	h, err := go_muyuan.Download(ctx, "https://example.com/big.iso",
//		go_muyuan.WithDir("downloads"),
//		go_muyuan.WithProgress(func(p go_muyuan.Progress) {
//			fmt.Printf("%.1f%% at %.1f MiB/s\n", p.Percent, float64(p.Speed)/(1<<20))
//		}))
//	if err != nil {
//		return err
//	}
//
//	time.Sleep(5 * time.Second)
//	if err := h.Pause(); err != nil { // stops the transfer, keeps the bytes
//		return err
//	}
//	if err := h.Resume(); err != nil { // continues from the same offset
//		return err
//	}
//	if err := h.Wait(); err != nil {
//		return err
//	}
//	fmt.Println("saved to", h.FilePath())
//
// The file is written to FilePath()+".part" while it is in flight and renamed
// once it is complete, so a partially downloaded file is never mistaken for a
// finished one. Pausing keeps that partial file and resuming continues with an
// HTTP range request; when the server does not support ranges, resuming starts
// the file over instead of corrupting it.
//
// The target URL is checked before the first request, and the resolved address
// is checked again when the connection is dialed, so a URL pointing at the
// local machine or at a private network is refused. WithAllowUnsafeHosts
// disables that guard for targets that are known to be trusted.
package go_muyuan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultRetries      = 3
	defaultRetryDelay   = time.Second
	defaultResponseWait = 30 * time.Second
	defaultStallTimeout = 60 * time.Second
	defaultBufferSize   = 64 << 10

	// progressInterval is how often a transfer hands a snapshot to the
	// callback configured with WithProgress.
	progressInterval = 250 * time.Millisecond

	// maxRetryDelay caps the doubling of the retry delay.
	maxRetryDelay = 30 * time.Second
)

// Option configures a download.
type Option func(*config)

// config is the resolved configuration of one download.
type config struct {
	dir          string
	fileName     string
	overwrite    bool
	headers      http.Header
	client       *http.Client
	retries      int
	retryDelay   time.Duration
	responseWait time.Duration
	stallTimeout time.Duration
	bufferSize   int
	progress     func(Progress)
	allowUnsafe  bool
}

// WithDir sets the directory the file is written to. It is created if missing.
func WithDir(dir string) Option {
	return func(c *config) { c.dir = dir }
}

// WithFileName overrides the file name, which is otherwise taken from the
// server's Content-Disposition header, or from the URL when the server names
// nothing.
func WithFileName(name string) Option {
	return func(c *config) { c.fileName = name }
}

// WithOverwrite allows the download to replace an existing file. Without it, a
// free name is chosen by appending a counter: report.pdf, report_1.pdf, ...
func WithOverwrite(overwrite bool) Option {
	return func(c *config) { c.overwrite = overwrite }
}

// WithHeader adds a request header, for example an authorization token. It can
// be used several times; a repeated key is sent as a repeated header.
func WithHeader(key, value string) Option {
	return func(c *config) { c.headers.Add(key, value) }
}

// WithHeaders adds every entry of the map as a request header.
func WithHeaders(headers map[string]string) Option {
	return func(c *config) {
		for key, value := range headers {
			c.headers.Add(key, value)
		}
	}
}

// WithHTTPClient uses a caller supplied HTTP client. The client owns its
// transport, so the timeouts of WithResponseTimeout and WithStallTimeout do not
// apply to it, and only the pre-request host check runs. Do not set
// Client.Timeout on it: that field bounds the whole transfer rather than the
// wait for response headers, so it aborts long downloads.
func WithHTTPClient(client *http.Client) Option {
	return func(c *config) { c.client = client }
}

// WithRetries sets how many further attempts follow a failed transfer. The
// default is 3.
func WithRetries(retries int) Option {
	return func(c *config) { c.retries = retries }
}

// WithRetryDelay sets the wait before the first retry; the wait doubles on
// every further attempt, up to 30 seconds. The default is 1 second.
func WithRetryDelay(delay time.Duration) Option {
	return func(c *config) { c.retryDelay = delay }
}

// WithResponseTimeout bounds the wait for response headers. The default is 30
// seconds. It does not limit how long the body may take.
func WithResponseTimeout(timeout time.Duration) Option {
	return func(c *config) { c.responseWait = timeout }
}

// WithStallTimeout gives up on a transfer that receives no data for this long,
// which is retried like any other transient failure. The default is 60 seconds;
// zero disables the check.
func WithStallTimeout(timeout time.Duration) Option {
	return func(c *config) { c.stallTimeout = timeout }
}

// WithBufferSize sets the size of the copy buffer. The default is 64 KiB.
func WithBufferSize(size int) Option {
	return func(c *config) { c.bufferSize = size }
}

// WithProgress registers a callback for transfer statistics. It runs on the
// download goroutine at most every 250ms plus once when the file completes, so
// it must not block or call methods that wait for the transfer to stop, such as
// Pause, Wait or Delete.
func WithProgress(callback func(Progress)) Option {
	return func(c *config) { c.progress = callback }
}

// WithAllowUnsafeHosts permits targets inside the local machine or a private
// network. The guard it disables is what keeps an untrusted URL from reaching
// internal services, so enable it only when the URLs are known to be trusted.
func WithAllowUnsafeHosts(allow bool) Option {
	return func(c *config) { c.allowUnsafe = allow }
}

func defaultConfig() config {
	return config{
		dir:          ".",
		headers:      make(http.Header),
		retries:      defaultRetries,
		retryDelay:   defaultRetryDelay,
		responseWait: defaultResponseWait,
		stallTimeout: defaultStallTimeout,
		bufferSize:   defaultBufferSize,
	}
}

func normalizeConfig(c *config) {
	if c.dir == "" {
		c.dir = "."
	}
	if c.bufferSize <= 0 {
		c.bufferSize = defaultBufferSize
	}
	if c.retries < 0 {
		c.retries = 0
	}
	if c.retryDelay <= 0 {
		c.retryDelay = defaultRetryDelay
	}
}

// New prepares a download without starting it. Call Start to begin the
// transfer, or use Download to do both at once.
//
// The context bounds the whole handle rather than a single request: canceling
// it stops the transfer, marks the download as failed, and leaves the handle
// unable to resume.
func New(ctx context.Context, rawURL string, opts ...Option) (*Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := defaultConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	normalizeConfig(&cfg)

	target, err := parseTarget(rawURL)
	if err != nil {
		return nil, err
	}
	if !cfg.allowUnsafe {
		if err := checkHost(ctx, target.Hostname()); err != nil {
			return nil, err
		}
	}
	if cfg.client == nil {
		cfg.client = newClient(cfg)
	}

	h := &Handle{
		id:     newID(),
		url:    target.String(),
		target: target,
		cfg:    cfg,
		base:   ctx,
		status: StatusPending,
		done:   make(chan struct{}),
	}

	name := cfg.fileName
	if name == "" {
		name = filenameFromURL(target)
	}
	name = sanitizeFileName(name)
	if name == "" {
		name = fallbackFileName
	}
	h.fileName = uniqueName(cfg.dir, name, cfg.overwrite)
	// A name given by the caller is final; one derived from the URL may still
	// be replaced by the name the server sends in Content-Disposition.
	h.resolved = cfg.fileName != ""

	return h, nil
}

// Download starts a transfer and returns the handle that controls it.
func Download(ctx context.Context, rawURL string, opts ...Option) (*Handle, error) {
	h, err := New(ctx, rawURL, opts...)
	if err != nil {
		return nil, err
	}
	if err := h.Start(); err != nil {
		return nil, err
	}
	return h, nil
}

// DownloadTo is Download with the destination path given in advance.
func DownloadTo(ctx context.Context, rawURL, path string, opts ...Option) (*Handle, error) {
	opts = append([]Option{
		WithDir(filepath.Dir(path)),
		WithFileName(filepath.Base(path)),
	}, opts...)
	return Download(ctx, rawURL, opts...)
}

// parseTarget accepts an absolute http or https URL with a host and no
// credentials.
func parseTarget(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("%w: empty URL", ErrInvalidURL)
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	switch target.Scheme {
	case "http", "https":
	case "":
		return nil, fmt.Errorf("%w: %q has no scheme", ErrInvalidURL, rawURL)
	default:
		return nil, fmt.Errorf("%w: scheme %q is not supported", ErrInvalidURL, target.Scheme)
	}
	if target.Host == "" {
		return nil, fmt.Errorf("%w: %q has no host", ErrInvalidURL, rawURL)
	}
	if target.User != nil {
		// Credentials in the URL end up in logs and in error messages; a header
		// set with WithHeader keeps them out of both.
		return nil, fmt.Errorf("%w: credentials in the URL are not supported", ErrInvalidURL)
	}
	return target, nil
}

// newClient builds the client used when the caller does not supply one. It has
// no overall timeout on purpose: a deadline that covers the whole transfer
// would abort exactly the long downloads this package exists for. Dialing and
// the response headers are bounded instead.
func newClient(cfg config) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	dial := dialer.DialContext
	if !cfg.allowUnsafe {
		dial = (&guardedDialer{dialer: dialer}).DialContext
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         dial,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			// Progress, byte counts and range arithmetic all describe the bytes
			// on the wire, so transparent decompression stays off.
			DisableCompression:    true,
			ResponseHeaderTimeout: cfg.responseWait,
		},
	}
}

// newID returns an identifier that is unique across processes.
func newID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("dl-%d", time.Now().UnixNano())
	}
	return "dl-" + hex.EncodeToString(buf[:])
}
