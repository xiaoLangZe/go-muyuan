// Package transfer 实现单个字节范围段的 HTTP 传输与重试。
//
// 它是下载器的 IO 叶子层：探测远端资源、按范围拉取字节、对瞬时失败
// 做有界退避重试。不触碰任何共享状态，只上报字节增量。
package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// DefaultUserAgent 是未在请求头显式设置 User-Agent 时使用的默认值。
const DefaultUserAgent = "go-muyuan/0.1.0"

// ProbeResult 是探测远端资源得到的结果。
type ProbeResult struct {
	Total        int64 // Content-Length；未知时为 0
	ETag         string
	LastModified string
	AcceptRanges bool // 当且仅当 Range 请求得到 206 响应时为 true
}

// permanentError 标记重试无法修复的失败，例如非预期的 HTTP 状态。
// RetrySegment 会立即返回它，而非耗尽退避预算。
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// StatusError 为非预期的 HTTP 状态构造错误。客户端错误（4xx）是
// 永久性的——404 或 403 不会因为等待而变成 200——但 408 和 429
// 除外，它们明确要求客户端重试。服务端错误（5xx）保持可重试。
func StatusError(op string, code int) error {
	err := fmt.Errorf("%s: unexpected status %d", op, code)
	if code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests {
		return &permanentError{err}
	}
	return err
}

// IsPermanent 报告 err 是否为 permanentError。
func IsPermanent(err error) bool {
	var pe *permanentError
	return errors.As(err, &pe)
}

// Probe 发起一次小范围请求以获知 Content-Length、ETag、Last-Modified，
// 以及服务器是否支持 Range。先尝试 HEAD（开销低），再用 1 字节
// Range GET 确认真实的范围支持，因为某些服务器在 HEAD 上省略
// Accept-Ranges 或对此撒谎。
func Probe(ctx context.Context, c *http.Client, url string, hdr http.Header) (ProbeResult, error) {
	var res ProbeResult

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return res, err
	}
	copyHeaders(req.Header, hdr)
	setDefaultUserAgent(req.Header)
	resp, err := c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		// HEAD 返回非成功状态（如 405）时不信任其头，交给 GET 探测。
		if resp.StatusCode < 400 {
			// ContentLength 未知时为 -1；与"未知为 0"的语义统一。
			if resp.ContentLength > 0 {
				res.Total = resp.ContentLength
			}
			res.ETag = resp.Header.Get("ETag")
			res.LastModified = resp.Header.Get("Last-Modified")
			if resp.Header.Get("Accept-Ranges") == "bytes" {
				res.AcceptRanges = true
			}
		}
	}

	greq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return res, err
	}
	copyHeaders(greq.Header, hdr)
	setDefaultUserAgent(greq.Header)
	greq.Header.Set("Range", "bytes=0-0")
	gresp, err := c.Do(greq)
	if err != nil {
		if res.Total != 0 || res.AcceptRanges {
			// HEAD 给了我们足够信息；忽略 GET 探测失败。
			return res, nil
		}
		return res, fmt.Errorf("probe: %w", err)
	}
	defer gresp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(gresp.Body, 64))

	switch gresp.StatusCode {
	case http.StatusPartialContent:
		res.AcceptRanges = true
		if cr := gresp.Header.Get("Content-Range"); cr != "" {
			if total, ok := parseContentRangeTotal(cr); ok {
				res.Total = total
			}
		}
		if res.ETag == "" {
			res.ETag = gresp.Header.Get("ETag")
		}
		if res.LastModified == "" {
			res.LastModified = gresp.Header.Get("Last-Modified")
		}
	case http.StatusOK:
		// 服务器忽略了 Range：整包语义，不可续传、不可切分。
		res.AcceptRanges = false
		if gresp.ContentLength > 0 {
			res.Total = gresp.ContentLength
		}
	}
	return res, nil
}

// parseContentRangeTotal 解析 Content-Range 值的 "<total>" 后缀，
// 例如 "bytes 0-0/12345"。未知形式 "bytes 0-0/*" 返回 ok=false。
func parseContentRangeTotal(cr string) (int64, bool) {
	i := -1
	for k := 0; k < len(cr); k++ {
		if cr[k] == '/' {
			i = k
			break
		}
	}
	if i < 0 || i+1 >= len(cr) {
		return 0, false
	}
	tail := cr[i+1:]
	if tail == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(tail, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// copyHeaders 将 src 中的非空值复制到 dst，跳过 Range 和 Host，
// 它们由调用方显式设置。
func copyHeaders(dst, src http.Header) {
	if src == nil {
		return
	}
	for k, vs := range src {
		if k == "Range" || k == "Host" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// setDefaultUserAgent 在调用方未显式设置 User-Agent 时填入默认值。
func setDefaultUserAgent(h http.Header) {
	if h.Get("User-Agent") == "" {
		h.Set("User-Agent", DefaultUserAgent)
	}
}

// WriterAt 是范围下载器所需的 *os.File 子集，以便测试可替换为
// 内存实现。
type WriterAt interface {
	WriteAt(p []byte, off int64) (n int, err error)
}

// DownloadSegment 从 url 流式拉取字节范围 [start,end) 并写到 f 对应的
// 全局偏移。每次成功写入后调用 onBytes 传入写入字节数，让调用方
// 维护聚合进度和每分片进度。
//
// 该函数不触碰共享状态：它上报增量、由调用方决定如何记录。context
// 在流传输中途取消会返回 context 错误；已写入的字节仍然是持久化的
// （WriteAt 是同步的），因此后续尝试可从 start+written 恢复。
func DownloadSegment(
	ctx context.Context,
	c *http.Client,
	url string,
	hdr http.Header,
	start, end int64,
	f WriterAt,
	onBytes func(n int64),
) error {
	if end <= start {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	copyHeaders(req.Header, hdr)
	setDefaultUserAgent(req.Header)
	// HTTP Range 的 end 是含的。
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))

	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("range request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return StatusError("range request", resp.StatusCode)
	}

	buf := make([]byte, 1<<15) // 32 KiB
	offset := start
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			written, werr := f.WriteAt(buf[:n], offset)
			if werr != nil {
				return fmt.Errorf("writeat: %w", werr)
			}
			if written != n {
				return fmt.Errorf("short write: %d != %d", written, n)
			}
			offset += int64(written)
			if onBytes != nil {
				onBytes(int64(written))
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read body: %w", rerr)
		}
	}
}

// DownloadSequential 处理不支持 Range 的服务器：它发起一次整包 GET，
// 并从偏移 0 起顺序写入流。进度通过 onBytes 上报。不可续传。
func DownloadSequential(
	ctx context.Context,
	c *http.Client,
	url string,
	hdr http.Header,
	f WriterAt,
	onBytes func(n int64),
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	copyHeaders(req.Header, hdr)
	setDefaultUserAgent(req.Header)
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StatusError("get", resp.StatusCode)
	}

	buf := make([]byte, 1<<15)
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			written, werr := f.WriteAt(buf[:n], offset)
			if werr != nil {
				return fmt.Errorf("writeat: %w", werr)
			}
			if written != n {
				return fmt.Errorf("short write: %d != %d", written, n)
			}
			offset += int64(written)
			if onBytes != nil {
				onBytes(int64(written))
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read body: %w", rerr)
		}
	}
}

// retryBackoffBase 是 RetrySegment 首次重试前的等待时间，之后每次
// 翻倍。它是包变量以便测试缩短等待；产品代码不应修改它。
var retryBackoffBase = 500 * time.Millisecond

// RetrySegment 以有界指数退避重试 seg 以应对瞬时失败。context 取消
// 和永久错误（见 StatusError）绝不重试，因为 manager 用取消来驱动
// Pause/Stop/Reconfigure，而永久状态不会因等待而改变。
func RetrySegment(
	ctx context.Context,
	seg func(context.Context) error,
) error {
	const maxAttempts = 4
	backoff := retryBackoffBase
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := seg(ctx)
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if IsPermanent(err) {
			return err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return lastErr
}
