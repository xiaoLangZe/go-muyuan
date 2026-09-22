// Package filename turns an untrusted candidate into a file name that is safe
// to create inside the download directory: a single path element with no
// traversal, no characters Windows rejects and no reserved device name.
//
// The candidates come from places that are not under the caller's control — the
// Content-Disposition header of a remote server, the last element of a URL — so
// every one of them is reduced to its last path element before it is used.
package filename

import (
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Fallback is used when neither the caller, the server, nor the URL suggests a
// name.
const Fallback = "download"

// MaxLen bounds a generated file name, leaving room for the directory and for
// filesystems that count bytes rather than characters.
const MaxLen = 200

// FromHeader extracts the name a Content-Disposition header advertises. The
// RFC 2231 and RFC 5987 encoded forms are handled by mime.ParseMediaType,
// including the filename* variant used for non-ASCII names.
func FromHeader(value string) string {
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

// FromURL derives a name from the last path element of the URL. Query and
// fragment are not part of the path, so a signed URL does not leak its token
// into the file name.
func FromURL(u *url.URL) string {
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

// Sanitize reduces a candidate to a name that is safe to create in a directory.
// It returns "" when nothing usable is left.
func Sanitize(name string) string {
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
	return truncateName(name, MaxLen)
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

// Unique returns name, or the first free variant of it when the file already
// exists and overwriting is not allowed: report.pdf, report_1.pdf,
// report_2.pdf, ...
func Unique(dir, name string, overwrite bool) string {
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
