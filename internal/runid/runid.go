// Package runid owns the canonical identifier shared by quality results and evidence.
package runid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
)

var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// New returns a lowercase RFC 4122 variant UUID version 4.
func New() (string, error) {
	return newWithReader(rand.Reader)
}

func newWithReader(reader io.Reader) (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(reader, value); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:]), nil
}

// Valid reports whether value is the exact lowercase canonical UUID wire form.
// Generation remains UUIDv4, while history accepts every version allowed by SPEC.
func Valid(value string) bool {
	return canonicalUUID.MatchString(value)
}
