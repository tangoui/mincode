package mcpauth

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"testing"
)

func TestPKCEChallengeIsS256OfVerifier(t *testing.T) {
	verifier, challenge := NewPKCE()
	if !regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`).MatchString(verifier) {
		t.Fatalf("verifier %q is not a valid RFC 7636 verifier", verifier)
	}
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); challenge != want {
		t.Fatalf("challenge = %q, want %q", challenge, want)
	}
	other, _ := NewPKCE()
	if other == verifier {
		t.Fatal("verifiers must be random")
	}
}
