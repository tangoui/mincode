package sshauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func strictTestKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func useStrictHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeKnownHost(t *testing.T, home, address string, key ssh.PublicKey) {
	t.Helper()
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(address)}, key) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStrictHostKeyCallbackRejectsUnknownHostsWithoutWriting(t *testing.T) {
	home := useStrictHome(t)
	key := strictTestKey(t)
	callback := StrictHostKeyCallback("backup.example", 2222)
	remote := address("backup.example:2222")
	if err := callback("backup.example:2222", remote, key); !errors.Is(err, ErrUnknownHostKey) {
		t.Fatalf("missing known_hosts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict callback created files: %v", err)
	}
	other := strictTestKey(t)
	writeKnownHost(t, home, "other.example:22", other)
	before, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
	if err := callback("backup.example:2222", remote, key); !errors.Is(err, ErrUnknownHostKey) {
		t.Fatalf("unknown host: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
	if string(before) != string(after) {
		t.Fatal("strict callback wrote known_hosts")
	}
}

func TestStrictHostKeyCallbackAcceptsKnownAndRejectsChangedKeys(t *testing.T) {
	home := useStrictHome(t)
	key := strictTestKey(t)
	writeKnownHost(t, home, net.JoinHostPort("backup.example", "2222"), key)
	callback := StrictHostKeyCallback("backup.example", 2222)
	remote := address("backup.example:2222")
	if err := callback("backup.example:2222", remote, key); err != nil {
		t.Fatalf("known key: %v", err)
	}
	if err := callback("backup.example:2222", remote, strictTestKey(t)); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("changed key: %v", err)
	}
}

func TestHasKnownHost(t *testing.T) {
	home := useStrictHome(t)
	if known, err := HasKnownHost("backup.example", 22); err != nil || known {
		t.Fatalf("no file: %v %v", known, err)
	}
	writeKnownHost(t, home, "backup.example:2222", strictTestKey(t))
	if known, err := HasKnownHost("backup.example", 2222); err != nil || !known {
		t.Fatalf("recorded host: %v %v", known, err)
	}
	if known, err := HasKnownHost("backup.example", 22); err != nil || known {
		t.Fatalf("other port: %v %v", known, err)
	}
	writeKnownHost(t, home, "plain.example:22", strictTestKey(t))
	if known, err := HasKnownHost("plain.example", 22); err != nil || !known {
		t.Fatalf("default port entry: %v %v", known, err)
	}
	if known, _ := HasKnownHost("backup.example", 2222); known {
		t.Fatal("stale entry after rewrite")
	}
}
