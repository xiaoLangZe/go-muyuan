package go_muyuan

import (
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// partSuffix marks the file a transfer is written to until it completes.
const partSuffix = ".part"

// fallbackFileName is used when neither the options, the server, nor the URL
// suggest a name.
const fallbackFileName = "download"

// maxNameLen bounds a generated file name, leaving room for the directory and
// for filesystems that count bytes rather than characters.
const maxNameLen = 200

// partPath returns where the bytes are written until the transfer completes.
func (h *Handle) partPath() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.filePathLocked() + partSuffix
}

// paths returns the partial and final paths from one consistent snapshot.
func (h *Handle) paths() (part, final string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	final = h.filePathLocked()
	return final + partSuffix, final
}

// filePathLocked is the final path; the caller holds the lock.
func (h *Handle) filePathLocked() string {
	return filepath.Join(h.cfg.dir, h.fileName)
}

// resolvePaths fixes the target name once the server has answered. A name given
// through WithFileName always wins; otherwise the server's Content-Disposition
// is preferred over the name taken from the URL.
func (h *Handle) resolvePaths(resp *http.Response) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.resolved {
		return
	}
	var name string
	if resp != nil {
		name = filenameFromHeader(resp.Header.Get("Content-Disposition"))
	}
	if name == "" {
		name = filenameFromURL(h.target)
	}
	name = sanitizeFileName(name)
	if name == "" {
		name = fallbackFileName
	}
	h.fileName = uniqueName(h.cfg.dir, name, h.cfg.overwrite)
	h.resolved = true
}

// filenameFromHeader extracts the name a Content-Disposition header advertises.
// The RFC 2231 and RFC 5987 encoded forms are handled by mime.ParseMediaType,
// including the filename* variant used for non-ASCII names.
func filenameFromHeader(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if _, params, err := mime.ParseMediaType(value); err == nil {
		return strings.TrimSpace(params["filename"])
	}
	// A malformed header can still carry a usable plain filename.
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		if len(part) < len("filename=") || !strings.EqualFold(part[:len("filename=")], "filename=") {
			continue
		}
		if name := strings.Trim(strings.TrimSpace(part[len("filename="):]), `"`); name != "" {
			return name
		}
	}
	return ""
}

// filenameFromURL derives a name from the last path element of the URL. Query
// and fragment are not part of the path, so a signed URL does not leak its
// token into the file name.
func filenameFromURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	path := u.EscapedPath()
	if path == "" || strings.HasSuffix(path, "/") {
		return ""
	}
	name := path[strings.LastIndex(path, "/")+1:]
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	return name
}

// sanitizeFileName turns a candidate into a name that is safe to create inside
// the output directory: a single path element with no traversal, no characters
// Windows rejects and no reserved device name. It returns "" when nothing
// usable is left.
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// Keeping the last element is what makes traversal impossible: neither
	// "../../etc/passwd" nor `..\..\windows\system32\cmd.exe` survives it.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == 0x7f:
			return '_'
		case strings.ContainsRune(`<>:"|?*`, r):
			return '_'
		default:
			return r
		}
	}, name)
	// Windows silently drops trailing dots and spaces, which would make the
	// name on disk differ from the name reported here.
	name = strings.TrimRight(name, ". ")
	if name == "" || name == "." || name == ".." {
		return ""
	}
	if isReservedName(name) {
		name = "_" + name
	}
	return truncateName(name, maxNameLen)
}

// reservedNames are the device names Windows refuses to create.
var reservedNames = func() map[string]bool {
	names := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}
	for i := 1; i <= 9; i++ {
		names[fmt.Sprintf("COM%d", i)] = true
		names[fmt.Sprintf("LPT%d", i)] = true
	}
	return names
}()

func isReservedName(name string) bool {
	stem := name
	if ext := filepath.Ext(stem); ext != "" {
		stem = stem[:len(stem)-len(ext)]
	}
	return reservedNames[strings.ToUpper(stem)]
}

// truncateName shortens a name to limit bytes while keeping its extension.
func truncateName(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > limit/2 {
		ext = ""
	}
	keep := limit - len(ext)
	if keep <= 0 {
		keep = limit
	}
	name = name[:keep]
	// Cutting on a byte boundary can split a multi-byte character.
	for len(name) > 0 && !utf8.ValidString(name) {
		name = name[:len(name)-1]
	}
	return name + ext
}

// uniqueName returns name, or the first free variant of it when the file
// already exists and overwriting is not allowed: report.pdf, report_1.pdf,
// report_2.pdf, ...
func uniqueName(dir, name string, overwrite bool) string {
	if overwrite || name == "" {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := name
	for i := 1; i <= 10000; i++ {
		_, err := os.Stat(filepath.Join(dir, candidate))
		if os.IsNotExist(err) {
			return candidate
		}
		if err != nil && !os.IsNotExist(err) {
			// The directory cannot be inspected; let the write report it.
			return candidate
		}
		candidate = fmt.Sprintf("%s_%d%s", base, i, ext)
	}
	return candidate
}
