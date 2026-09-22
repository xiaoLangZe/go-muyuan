package engine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// newID returns an identifier that is unique across processes.
func newID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("dl-%d", time.Now().UnixNano())
	}
	return "dl-" + hex.EncodeToString(buf[:])
}
