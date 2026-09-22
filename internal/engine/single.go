package engine

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

// runSingle fetches the file over one connection, retrying transient failures.
// A nil return means the finished file is in place.
func (t *Transfer) runSingle(ctx context.Context) error {
	delay := t.cfg.RetryDelay
	var lastErr error

	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			break
		}
		lastErr = t.attempt(ctx)
		if lastErr == nil {
			return nil
		}
		if t.promoted() {
			// The budget grew while this request was in flight; the bytes
			// written so far stay on disk and the parallel path takes over.
			return errPromote
		}
		if ctx.Err() != nil {
			// The transfer was paused, deleted or canceled while transferring.
			break
		}
		if attempt >= t.cfg.Retries || !isTransient(lastErr) {
			break
		}
		if !sleep(ctx, delay) {
			break
		}
		if delay *= 2; delay > maxRetryDelay {
			delay = maxRetryDelay
		}
	}

	if lastErr == nil {
		lastErr = ctx.Err()
	}
	if lastErr == nil {
		lastErr = errIncomplete
	}
	return lastErr
}

// attempt performs one request and appends the response body to the partial
// file. A nil return means the finished file is in place.
func (t *Transfer) attempt(ctx context.Context) error {
	t.mu.Lock()
	offset := t.downloaded
	validator := t.validator
	askRange := offset > 0 && t.ranges != RangeUnsupported
	t.mu.Unlock()

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	t.mu.Lock()
	t.attemptCut = cancel
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.attemptCut = nil
		t.mu.Unlock()
	}()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, t.url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for key, values := range t.cfg.Headers {
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

	resp, err := t.cfg.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// The caller stopped the transfer; the retry loop checks the
			// context and breaks out.
			return fmt.Errorf("request %s: %w", t.url, err)
		}
		return transient(fmt.Errorf("request %s: %w", t.url, err))
	}
	defer resp.Body.Close()

	t.observeHeaders(resp)

	var total int64
	switch resp.StatusCode {
	case http.StatusOK:
		if offset > 0 {
			// The body starts at zero again, so the bytes on disk are unusable.
			// That happens when the server does not do ranges at all, and when
			// it does but the resource changed under us.
			if askRange && validator == "" {
				t.setRanges(RangeUnsupported)
			}
			t.discardProgress()
			offset = 0
		} else if strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes") {
			t.setRanges(RangeSupported)
		}
		if resp.ContentLength >= 0 {
			total = resp.ContentLength
		}
		t.setTotal(total)

	case http.StatusPartialContent:
		start, size, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil {
			return err
		}
		if start != offset {
			t.discardProgress()
			return transient(fmt.Errorf("server answered from byte %d, expected %d", start, offset))
		}
		t.setRanges(RangeSupported)
		total = size
		t.setTotal(total)

	case http.StatusRequestedRangeNotSatisfiable:
		_, size, _ := parseContentRange(resp.Header.Get("Content-Range"))
		if size > 0 && offset >= size {
			// Everything is on disk already; only the rename is missing.
			t.setTotal(size)
			t.report(size)
			return t.commit(nil)
		}
		t.discardProgress()
		return transient(fmt.Errorf("server rejected the range request at offset %d", offset))

	default:
		return &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status, URL: t.url}
	}

	t.resolvePaths(resp)
	part, _ := t.paths()

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
	stopWatch := watchStall(t.cfg.StallTimeout, cancel, &lastRead)
	defer stopWatch()

	buffer := make([]byte, t.cfg.BufferSize)
	written := offset
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				return fmt.Errorf("write %s: %w", part, writeErr)
			}
			written += int64(n)
			lastRead.Store(time.Now().UnixNano())
			t.report(written)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if ctx.Err() == nil && reqCtx.Err() != nil {
				// Only the stall watchdog or a promotion cancels the request
				// context while the download itself is still wanted.
				if t.promoted() {
					return errPromote
				}
				return transient(fmt.Errorf("%w: no data for %s", ErrStalled, t.cfg.StallTimeout))
			}
			return transient(fmt.Errorf("read body: %w", readErr))
		}
	}

	if total > 0 && written < total {
		// A body that stops early is worth another attempt: the retry resumes
		// from what was written.
		return transient(fmt.Errorf("body ended after %d of %d bytes", written, total))
	}
	return t.commit(file)
}

// observeHeaders keeps the validator that makes a later range request safe: the
// server only answers it when the resource still matches.
func (t *Transfer) observeHeaders(resp *http.Response) {
	validator := strings.TrimSpace(resp.Header.Get("ETag"))
	if validator == "" {
		validator = strings.TrimSpace(resp.Header.Get("Last-Modified"))
	}
	if validator == "" {
		return
	}
	t.mu.Lock()
	t.validator = validator
	t.mu.Unlock()
}

// commit flushes the partial file and moves it to its final name. A nil file
// means the bytes are already on disk from an earlier attempt and only the
// rename is missing.
func (t *Transfer) commit(file *os.File) error {
	part, final := t.paths()
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
	return openFile(path, offset, true)
}

// openChunked opens the partial file for positional writes and makes sure it is
// at least size bytes long, without dropping what is already there.
func openChunked(path string, size int64) (*os.File, error) {
	return openFile(path, size, false)
}

func openFile(path string, size int64, truncate bool) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	flags := os.O_CREATE | os.O_WRONLY
	if !truncate {
		flags = os.O_CREATE | os.O_RDWR
	}
	file, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if truncate {
		if err := file.Truncate(size); err != nil {
			file.Close()
			return nil, fmt.Errorf("truncate %s: %w", path, err)
		}
		if _, err := file.Seek(size, io.SeekStart); err != nil {
			file.Close()
			return nil, fmt.Errorf("seek %s: %w", path, err)
		}
		return file, nil
	}
	if info, err := file.Stat(); err == nil && info.Size() < size {
		if err := file.Truncate(size); err != nil {
			file.Close()
			return nil, fmt.Errorf("extend %s: %w", path, err)
		}
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
