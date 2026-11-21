package associations

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBoundedFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.torrent")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	p, info, e := ValidateFile(file)
	if e != nil || p == "" || info.Size() != 1 {
		t.Fatal(p, info, e)
	}
	link := filepath.Join(dir, "link.torrent")
	if e = os.Symlink(file, link); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{link, dir, "relative.torrent", file + "\n", filepath.Join(dir, "gone.torrent")} {
		if _, _, e := ValidateFile(path); e == nil {
			t.Fatal(path)
		}
	}
	bad := filepath.Join(dir, "x.exe")
	_ = os.WriteFile(bad, []byte("x"), 0600)
	if _, _, e := ValidateFile(bad); e == nil {
		t.Fatal("executable accepted")
	}
}
