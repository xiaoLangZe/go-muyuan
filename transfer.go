package go_muyuan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// rangeState records what the server said about partial requests.
type rangeState int8

const (
	// rangeUnknown means no range request has been answered yet.
	rangeUnknown rangeState = iota
	// rangeSupported means the server answers with 206 and a matching offset.
	rangeSupported
	// rangeUnsupported means the server ignores the Range header, so an
	// interrupted transfer has to start over.
	rangeUnsupported
)

// work runs the transfer until it completes, gives up, or is stopped.
func (h *Handle) work(ctx context.Context) {
	delay := h.cfg.retryDelay
	var lastErr error

	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			break
		}
		lastErr = h.attempt(ctx)
		if lastErr == nil {
			h.finish(StatusCompleted, nil)
			h.emit()
			return
		}
		if ctx.Err() != nil {
			// The handle was paused, deleted or canceled while transferring.
			break
		}
		if attempt >= h.cfg.retries || !isTransient(lastErr) {
			break
		}
		if !sleep(ctx, delay) {
			break
		}
		if delay *= 2; delay > maxRetryDelay {
			delay = maxRetryDelay
		}
	}

	h.mu.Lock()
	stopped := h.status == StatusPaused || h.status == StatusDeleted
	h.mu.Unlock()
	if stopped {
		// Pause and Delete own the state they put the handle in.
		return
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	if lastErr == nil {
		lastErr = errors.New("download stopped before the file was complete")
	}
	h.finish(StatusFailed, lastErr)
}

// attempt performs one request and appends the response body to the partial
// file. A nil return means the finished file is in place.
func (h *Handle) attempt(ctx context.Context) error {
	h.mu.Lock()
	offset := h.downloaded
	validator := h.validator
	askRange := offset > 0 && h.ranges != rangeUnsupported
	h.mu.Unlock()

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, h.url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for key, values := range h.cfg.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if askRange {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		if validator != "" {
			// If the resource changed since the bytes on disk were fetched, the
			// server answers with the whole body instead of a range that no
			// longer lines up with it.
			req.Header.Set("If-Range", validator)
		}
	}

	resp, err := h.cfg.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// The caller stopped the transfer; the retry loop checks the
			// context and breaks out.
			return fmt.Errorf("request %s: %w", h.url, err)
		}
		return transient(fmt.Errorf("request %s: %w", h.url, err))
	}
	defer resp.Body.Close()

	h.observeHeaders(resp)

	var total int64
	switch resp.StatusCode {
	case http.StatusOK:
		if offset > 0 {
			// The body starts at zero again, so the bytes on disk are unusable.
			// That happens when the server does not do ranges at all, and when
			// it does but the resource changed under us.
			if askRange && validator == "" {
				h.setRanges(rangeUnsupported)
			}
			h.discardProgress()
			offset = 0
		} else if strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes") {
			h.setRanges(rangeSupported)
		}
		if resp.ContentLength >= 0 {
			total = resp.ContentLength
		}
		h.setTotal(total)

	case http.StatusPartialContent:
		start, size, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil {
			return err
		}
		if start != offset {
			h.discardProgress()
			return transient(fmt.Errorf("server answered from byte %d, expected %d", start, offset))
		}
		h.setRanges(rangeSupported)
		total = size
		h.setTotal(total)

	case http.StatusRequestedRangeNotSatisfiable:
		_, size, _ := parseContentRange(resp.Header.Get("Content-Range"))
		if size > 0 && offset >= size {
			// Everything is on disk already; only the rename is missing.
			h.setTotal(size)
			h.report(size)
			return h.commit(nil)
		}
		h.discardProgress()
		return transient(fmt.Errorf("server rejected the range request at offset %d", offset))

	default:
		return &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status, URL: h.url}
	}

	h.resolvePaths(resp)
	part, _ := h.paths()

	file, err := openPartial(part, offset)
	if err != nil {
		return err
	}
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()

	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	stopWatch := watchStall(h.cfg.stallTimeout, cancel, &lastRead)
	defer stopWatch()

	buffer := make([]byte, h.cfg.bufferSize)
	written := offset
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				return fmt.Errorf("write %s: %w", part, writeErr)
			}
			written += int64(n)
			lastRead.Store(time.Now().UnixNano())
			h.report(written)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if ctx.Err() == nil && reqCtx.Err() != nil {
				// Only the stall watchdog cancels the request context while the
				// download itself is still wanted.
				return transient(fmt.Errorf("%w: no data for %s", ErrStalled, h.cfg.stallTimeout))
			}
			return transient(fmt.Errorf("read body: %w", readErr))
		}
	}

	if total > 0 && written < total {
		// A body that stops early is worth another attempt: the retry resumes
		// from what was written.
		return transient(fmt.Errorf("body ended after %d of %d bytes", written, total))
	}
	return h.commit(file)
}

// observeHeaders keeps the validator that makes a later range request safe: the
// server only answers it when the resource still matches.
func (h *Handle) observeHeaders(resp *http.Response) {
	validator := strings.TrimSpace(resp.Header.Get("ETag"))
	if validator == "" {
		validator = strings.TrimSpace(resp.Header.Get("Last-Modified"))
	}
	if validator == "" {
		return
	}
	h.mu.Lock()
	h.validator = validator
	h.mu.Unlock()
}

// commit flushes the partial file and moves it to its final name. A nil file
// means the bytes are already on disk from an earlier attempt and only the
// rename is missing.
func (h *Handle) commit(file *os.File) error {
	part, final := h.paths()
	if file != nil {
		if err := file.Sync(); err != nil {
			return fmt.Errorf("sync %s: %w", part, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close %s: %w", part, err)
		}
	} else if err := syncFile(part); err != nil {
		return err
	}
	if err := os.Rename(part, final); err != nil {
		return fmt.Errorf("move %s to %s: %w", part, final, err)
	}
	return nil
}

// openPartial creates the partial file and positions it at offset, dropping
// anything past it.
func openPartial(path string, offset int64) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := file.Truncate(offset); err != nil {
		file.Close()
		return nil, fmt.Errorf("truncate %s: %w", path, err)
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		file.Close()
		return nil, fmt.Errorf("seek %s: %w", path, err)
	}
	return file, nil
}

// syncFile flushes a file that is no longer open.
func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return nil
}

// watchStall cancels the request when no byte has arrived for timeout. A
// connection that goes quiet without closing would otherwise hold the download
// open forever. The returned function stops the watchdog.
func watchStall(timeout time.Duration, cancel context.CancelFunc, lastRead *atomic.Int64) func() {
	if timeout <= 0 {
		return func() {}
	}
	interval := timeout / 4
	if interval < 20*time.Millisecond {
		interval = 20 * time.Millisecond
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				if now.Sub(time.Unix(0, lastRead.Load())) >= timeout {
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// parseContentRange reads the size and start offset out of a Content-Range
// header: "bytes 200-999/1000" and "bytes */1000" are both valid. A total of
// zero means the server did not advertise one.
func parseContentRange(value string) (start, total int64, err error) {
	value = strings.TrimSpace(value)
	rest, ok := strings.CutPrefix(value, "bytes ")
	if !ok {
		return 0, 0, fmt.Errorf("unexpected Content-Range %q", value)
	}
	span, size, ok := strings.Cut(rest, "/")
	if !ok {
		return 0, 0, fmt.Errorf("unexpected Content-Range %q", value)
	}
	if size != "*" {
		if total, err = strconv.ParseInt(size, 10, 64); err != nil {
			return 0, 0, fmt.Errorf("unexpected Content-Range %q", value)
		}
	}
	if span == "*" {
		return 0, total, nil
	}
	first, _, ok := strings.Cut(span, "-")
	if !ok {
		return 0, 0, fmt.Errorf("unexpected Content-Range %q", value)
	}
	if start, err = strconv.ParseInt(first, 10, 64); err != nil {
		return 0, 0, fmt.Errorf("unexpected Content-Range %q", value)
	}
	return start, total, nil
}
