package evidence

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestHMACSHA256MatchesPublishedVector(t *testing.T) {
	digest := hmacDigest([]byte("key"), []byte("The quick brown fox jumps over the lazy dog"))
	defer clear(digest)
	if got := hex.EncodeToString(digest); got != "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8" {
		t.Fatalf("HMAC = %q", got)
	}
}

func TestSecureEqualHexRejectsMismatchAndEitherInvalidOperand(t *testing.T) {
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	if !secureEqualHex(first, first) {
		t.Fatal("equal digests were rejected")
	}
	for _, values := range [][2]string{{first, second}, {"not-hex", first}, {first, "not-hex"}, {"not-hex", "also-not-hex"}} {
		if secureEqualHex(values[0], values[1]) {
			t.Fatalf("unequal or malformed digests were accepted: %q %q", values[0], values[1])
		}
	}
}
