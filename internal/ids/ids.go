// Package ids generates sortable, URL-safe identifiers.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"strings"
	"time"
)

var enc = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// New returns prefix + a 26-character id whose first 48 bits are the Unix
// millisecond timestamp, so ids sort by creation time (like a ULID), followed
// by 80 random bits.
func New(prefix string) string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint64(b[0:8], ms<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	return prefix + enc.EncodeToString(b[:])
}

// Token returns a random secret with 256 bits of entropy.
func Token() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	return strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]), "=")
}

// Valid reports whether s looks like an id produced by New with the prefix.
func Valid(prefix, s string) bool {
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	body := s[len(prefix):]
	if len(body) != 26 {
		return false
	}
	for _, c := range body {
		if !strings.ContainsRune("0123456789abcdefghjkmnpqrstvwxyz", c) {
			return false
		}
	}
	return true
}
