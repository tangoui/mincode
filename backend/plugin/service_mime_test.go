package plugin

import (
	"regexp"
	"testing"
)

func TestServiceAccepts(t *testing.T) {
	cases := []struct {
		accepts []string
		name    string
		want    bool
	}{
		{[]string{"image/*"}, "A.PNG", true},
		{[]string{".pdf"}, "x.pdf.png", false},
		{[]string{"application/pdf"}, "x.PDF", true},
		{[]string{".pdf"}, "Report.PDF", true},
		{[]string{".tar.gz"}, "backup.tar.gz", true},
		{[]string{"image/*"}, "notes.txt", false},
		{[]string{"application/*"}, "unknown.zzz", false},
		{[]string{"application/octet-stream"}, "unknown.zzz", false},
		{[]string{"text/*", ".pdf"}, "a.md", true},
		{nil, "a.pdf", false},
		{[]string{"audio/*"}, "song.MP3", true},
		{[]string{"video/*"}, "clip.mov", true},
		{[]string{"font/*"}, "ui.woff2", true},
		{[]string{"application/epub+zip"}, "book.epub", true},
		{[]string{"text/*"}, "main.go", true},
	}
	for _, tc := range cases {
		if got := serviceAccepts(tc.accepts, tc.name); got != tc.want {
			t.Errorf("serviceAccepts(%v, %q) = %v, want %v", tc.accepts, tc.name, got, tc.want)
		}
	}
}

func TestServiceMimeForName(t *testing.T) {
	for name, want := range map[string]string{
		"a.png": "image/png", "B.JPG": "image/jpeg", "c.pdf": "application/pdf", "d.ofd": "application/ofd",
		"e.docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "f.json": "application/json",
		"g.csv": "text/csv", "h.zip": "application/zip", "noext": "application/octet-stream", "i.zzz": "application/octet-stream",
		"j.mp4": "video/mp4", "k.wav": "audio/wav", "l.flac": "audio/flac", "m.ttf": "font/ttf", "n.otf": "font/otf",
		"o.epub": "application/epub+zip", "p.mobi": "application/x-mobipocket-ebook", "q.odg": "application/vnd.oasis.opendocument.graphics",
		"r.xlsm": "application/vnd.ms-excel.sheet.macroenabled.12", "s.js": "text/javascript", "t.sql": "application/sql",
		"u.toml": "application/toml", "v.wasm": "application/wasm",
	} {
		if got := serviceMimeForName(name); got != want {
			t.Errorf("serviceMimeForName(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestServiceMimeTableShape: accepts are lower-cased before matching, so every
// table value must already be a lower-case MIME type an accepts entry can name.
func TestServiceMimeTableShape(t *testing.T) {
	extension := regexp.MustCompile(`^\.[a-z0-9]+$`)
	mime := regexp.MustCompile(`^[a-z][a-z0-9.+-]*/[a-z0-9][a-z0-9.+-]*$`)
	if len(serviceMimeTypes) < 100 {
		t.Fatalf("table has %d entries", len(serviceMimeTypes))
	}
	for key, value := range serviceMimeTypes {
		if !extension.MatchString(key) || !mime.MatchString(value) {
			t.Errorf("%q: %q", key, value)
		}
	}
}
