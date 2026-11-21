package sshauth

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPrivateKeyPathsKeepsFilePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-key")
	if err := os.WriteFile(path, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := PrivateKeyPaths(path); !reflect.DeepEqual(got, []string{path}) {
		t.Fatalf("PrivateKeyPaths() = %v, want %v", got, []string{path})
	}
}

func TestPrivateKeyPathsExpandsDirectory(t *testing.T) {
	dir := t.TempDir()
	files := []string{"z-custom-key", "id_rsa", "id_ed25519", "id_ed25519.pub", "known_hosts", "config"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("key"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}

	want := []string{
		filepath.Join(dir, "id_ed25519"),
		filepath.Join(dir, "id_rsa"),
		filepath.Join(dir, "z-custom-key"),
	}
	if got := PrivateKeyPaths(dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrivateKeyPaths() = %v, want %v", got, want)
	}
}

func TestPrivateKeyPathsReturnsDefaultsForEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	want := []string{filepath.Join(dir, "id_ed25519"), filepath.Join(dir, "id_rsa")}
	if got := PrivateKeyPaths(dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrivateKeyPaths() = %v, want %v", got, want)
	}
}
