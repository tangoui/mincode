package plugin

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestCoverData(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"assets/cover.png": {Data: buf.Bytes()}, "bad.svg": {Data: []byte("<svg/>")}, "big.png": {Data: make([]byte, maxCoverBytes+1)}}
	if !strings.HasPrefix(coverData(files, "assets/cover.png"), "data:image/png;base64,") {
		t.Fatal("valid cover rejected")
	}
	for _, name := range []string{"", "missing.png", "../cover.png", "/cover.png", "https://example.com/a.png", `assets\cover.png`, "bad.svg", "big.png"} {
		if coverData(files, name) != "" {
			t.Errorf("accepted invalid cover %q", name)
		}
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.png")); err != nil {
		t.Fatal(err)
	}
	if localCoverData(root, "escape.png") != "" {
		t.Fatal("symlink escaped plugin root")
	}
	if err := os.WriteFile(filepath.Join(root, "cover.png"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if localCoverData(root, "cover.png") == "" {
		t.Fatal("local cover rejected")
	}
	record := &pluginRecord{root: root, manifest: &Manifest{Cover: "cover.png"}}
	if recordMeta(record).Cover == "" {
		t.Fatal("disabled plugin cover missing from metadata")
	}
}

func TestLocalCoverDataCachesPerRevision(t *testing.T) {
	encode := func(size int) []byte {
		var buf bytes.Buffer
		if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, size, size))); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	root := t.TempDir()
	path := filepath.Join(root, "cover.png")
	if err := os.WriteFile(path, encode(2), 0o600); err != nil {
		t.Fatal(err)
	}
	first := localCoverData(root, "cover.png")
	if first == "" {
		t.Fatal("cover rejected")
	}
	coverCache.Lock()
	key := coverCacheKey{directory: root, name: "cover.png"}
	entry := coverCache.entries[key]
	entry.data = "cached"
	coverCache.entries[key] = entry
	coverCache.Unlock()
	if got := localCoverData(root, "cover.png"); got != "cached" {
		t.Fatalf("unchanged cover re-read: %q", got)
	}
	if err := os.WriteFile(path, encode(3), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := localCoverData(root, "cover.png"); got == "cached" || got == first || got == "" {
		t.Fatal("changed cover served from cache")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if localCoverData(root, "cover.png") != "" {
		t.Fatal("removed cover still served")
	}
}
