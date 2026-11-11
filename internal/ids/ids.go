// Package ids creates the project's one ID format: a random 128-bit value,
// stored as 32 lowercase hex characters. Jellyfin-compatible apps expect a
// GUID, so GUID renders the same value with dashes; nothing is re-encoded.
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a fresh random ID.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Valid reports whether s has the canonical form.
func Valid(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// GUID renders an ID as 8-4-4-4-12 for protocols that want that shape.
func GUID(id string) string {
	if !Valid(id) {
		return id
	}
	return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}
