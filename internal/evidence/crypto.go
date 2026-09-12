package evidence

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func deriveHMACKey(key []byte, namespace string) []byte {
	return hmacDigest(key, []byte(namespace))
}

func namespacedHMAC(key []byte, namespace string, body []byte) string {
	digest := hmacDigest(key, append([]byte(namespace), body...))
	defer clear(digest)
	return hex.EncodeToString(digest)
}

func hmacDigest(key, value []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func secureEqualHex(left, right string) bool {
	leftBytes, leftErr := hex.DecodeString(left)
	rightBytes, rightErr := hex.DecodeString(right)
	if leftErr != nil {
		return false
	}
	if rightErr != nil {
		return false
	}
	defer clear(leftBytes)
	defer clear(rightBytes)
	return hmac.Equal(leftBytes, rightBytes)
}
