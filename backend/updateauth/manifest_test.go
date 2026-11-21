package updateauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestSignAndVerifyManifest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: "1.2.3", Platforms: map[string]Platform{
		"windows-x86_64": {URL: "https://example.com/a.exe", SHA256: strings.Repeat("a", 64), FileSize: 42},
	}}
	if err := Sign(&manifest, base64.StdEncoding.EncodeToString(privateKey)); err != nil {
		t.Fatal(err)
	}
	if err := Verify(manifest, base64.StdEncoding.EncodeToString(publicKey)); err != nil {
		t.Fatal(err)
	}
	manifest.Platforms["windows-x86_64"] = Platform{URL: "https://evil.example/a.exe", SHA256: strings.Repeat("a", 64), FileSize: 42}
	if err := Verify(manifest, base64.StdEncoding.EncodeToString(publicKey)); err == nil {
		t.Fatal("tampered manifest accepted")
	}
}
