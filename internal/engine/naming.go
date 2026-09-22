package engine

import (
	"net/http"
	"path/filepath"

	"github.com/xiaoLangZe/go-muyuan/internal/filename"
)

// PartSuffix marks the file a transfer is written to until it completes.
const PartSuffix = ".part"

// filePathLocked is the final path; the caller holds mu.
func (t *Transfer) filePathLocked() string {
	return filepath.Join(t.cfg.Dir, t.fileName)
}

// paths returns the partial and final paths from one consistent snapshot.
func (t *Transfer) paths() (part, final string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	final = t.filePathLocked()
	return final + PartSuffix, final
}

// resolvePaths fixes the target name once the server has answered. A name given
// through the file-name option always wins; otherwise the server's
// Content-Disposition is preferred over the name taken from the URL.
func (t *Transfer) resolvePaths(resp *http.Response) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resolved {
		return
	}
	var name string
	if resp != nil {
		name = filename.FromHeader(resp.Header.Get("Content-Disposition"))
	}
	if name == "" {
		name = filename.FromURL(t.target)
	}
	name = filename.Sanitize(name)
	if name == "" {
		name = filename.Fallback
	}
	t.fileName = filename.Unique(t.cfg.Dir, name, t.cfg.Overwrite)
	t.resolved = true
}
