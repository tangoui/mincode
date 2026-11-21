package mcpauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// NewPKCE returns a fresh RFC 7636 code verifier (32 random bytes encoded as
// 43 base64url characters) and its S256 code challenge.
func NewPKCE() (verifier, challenge string) {
	verifier = randomToken()
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomToken returns 256 bits from crypto/rand as unpadded base64url.
// crypto/rand.Read never fails on supported platforms (it aborts the process
// instead), so there is no error to report.
func randomToken() string {
	var buf [32]byte
	_, _ = rand.Read(buf[:])
	return base64.RawURLEncoding.EncodeToString(buf[:])
}
