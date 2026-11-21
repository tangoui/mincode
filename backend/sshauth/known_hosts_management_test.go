package sshauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestListAndRemoveKnownHostPreservesOtherLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir reads USERPROFILE on Windows; without it the test
	// would touch the real user profile.
	t.Setenv("USERPROFILE", home)
	path, err := KnownHostsPath()
	if err != nil {
		t.Fatal(err)
	}
	// This test rewrites the file; refuse to run against a real profile.
	if rel, err := filepath.Rel(home, path); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("known_hosts path %q is outside the temporary home %q", path, home)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	_, pub1, _ := testSigner(t)
	_, pub2, _ := testSigner(t)
	line1 := knownhosts.Line([]string{knownhosts.Normalize(net.JoinHostPort("one.example", "22"))}, pub1)
	line2 := knownhosts.Line([]string{knownhosts.Normalize(net.JoinHostPort("two.example", "22"))}, pub2)
	if err := os.WriteFile(path, []byte("# keep\n"+line1+"\n"+line2+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, entries, err := ListKnownHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries", len(entries))
	}
	if err := RemoveKnownHost(entries[0].ID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# keep\n"+line2+"\n" {
		t.Fatalf("unexpected content: %q", raw)
	}
}

func testSigner(t *testing.T) (ssh.Signer, ssh.PublicKey, []byte) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer, signer.PublicKey(), nil
}
