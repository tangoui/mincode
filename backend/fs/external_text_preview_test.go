package fs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const testChunkBytes = 512 * 1024

type previewChunkTestResult struct {
	Text        string `json:"text"`
	StartOffset int64  `json:"startOffset"`
	NextOffset  int64  `json:"nextOffset"`
	TotalBytes  int64  `json:"totalBytes"`
	EOF         bool   `json:"eof"`
	Version     string `json:"version"`
}

func previewFile(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "restore.sql")
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func previewRead(t *testing.T, path string, offset int64, version string) (previewChunkTestResult, error) {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"path": path, "offset": offset, "expectedVersion": version})
	raw, err := handleReadExternalTextChunk(context.Background(), "", string(args))
	var r previewChunkTestResult
	if err == nil {
		err = json.Unmarshal([]byte(raw), &r)
	}
	return r, err
}

func TestExternalLargeTextInspection(t *testing.T) {
	for name, data := range map[string]string{
		"utf8":      strings.Repeat("x", 8191) + "中文🙂" + strings.Repeat("a", 5*1024*1024),
		"nul":       "--\x00" + strings.Repeat("a", 6*1024*1024),
		"invalid":   "\xff\xfe" + strings.Repeat("a", 6*1024*1024),
		"pdf-magic": "%PDF-1.7" + strings.Repeat("a", 6*1024*1024),
		"small":     "select 1;",
	} {
		t.Run(name, func(t *testing.T) {
			p := previewFile(t, data)
			args, _ := json.Marshal(map[string]string{"path": p})
			raw, err := handleInspectExternalFile("", string(args))
			if err != nil {
				t.Fatal(err)
			}
			var r struct {
				Kind      string `json:"kind"`
				Available bool   `json:"textPreviewAvailable"`
			}
			if err = json.Unmarshal([]byte(raw), &r); err != nil {
				t.Fatal(err)
			}
			if r.Available != (name == "utf8") {
				t.Fatalf("preview availability=%v, kind=%s", r.Available, r.Kind)
			}
			if name == "utf8" && r.Kind != "binary" {
				t.Fatal("large file must stay out of editable text path")
			}
		})
	}
}

func TestExternalTextPreviewBoundedAndLossless(t *testing.T) {
	// Both page boundaries split multibyte UTF-8 unless adjusted.
	text := "\uFEFF" + strings.Repeat("x", testChunkBytes-4) + "中🙂" + strings.Repeat("y", testChunkBytes-8) + "é末尾"
	p := previewFile(t, text)
	var combined strings.Builder
	var offset int64
	version := ""
	var first previewChunkTestResult
	for i := 0; i < 5; i++ {
		r, err := previewRead(t, p, offset, version)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = r
			version = r.Version
			if version == "" {
				t.Fatal("missing version")
			}
		}
		if len(r.Text) > testChunkBytes || !utf8.ValidString(r.Text) || r.StartOffset != offset || r.NextOffset != offset+int64(len(r.Text)) || r.TotalBytes != int64(len(text)) {
			t.Fatalf("invalid chunk: bytes=%d range=%d..%d", len(r.Text), r.StartOffset, r.NextOffset)
		}
		combined.WriteString(r.Text)
		if r.EOF {
			break
		}
		if r.NextOffset <= offset {
			t.Fatal("no progress")
		}
		offset = r.NextOffset
	}
	if combined.String() != text {
		t.Fatal("lost or duplicated text across UTF-8 boundary")
	}
	back, err := previewRead(t, p, 0, version)
	if err != nil || back != first {
		t.Fatalf("back: %v", err)
	}
	if _, err := handleReadExternalTextFile("", `{"path":"`+p+`"}`); err != nil {
		t.Fatal(err)
	}
}

func TestExternalTextPreviewLargeNoNewlines(t *testing.T) {
	// Generated text, never a user's SQL dump.
	p := previewFile(t, strings.Repeat("x", 70*1024*1024))
	r, err := previewRead(t, p, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Text) != testChunkBytes || r.TotalBytes != 70*1024*1024 || r.EOF {
		t.Fatalf("not bounded: length=%d eof=%v", len(r.Text), r.EOF)
	}
	args, _ := json.Marshal(map[string]string{"path": p})
	if _, err = handleReadExternalTextFile("", string(args)); err == nil || !strings.Contains(err.Error(), "file_too_large") {
		t.Fatalf("ordinary full read limit changed: %v", err)
	}
}

func TestExternalTextPreviewRejectsInvalidInput(t *testing.T) {
	for name, text := range map[string]string{"nul": "abc\x00def", "invalid": "a\xff", "incomplete": "select '\xe4\xb8", "magic": "%PDF-1.7"} {
		t.Run(name, func(t *testing.T) {
			if _, err := previewRead(t, previewFile(t, text), 0, ""); err == nil {
				t.Fatal("non-text accepted")
			}
		})
	}
	p := previewFile(t, "中abc")
	for _, offset := range []int64{-1, 1, 100, 9223372036854775807} {
		if _, err := previewRead(t, p, offset, ""); err == nil {
			t.Fatalf("offset %d accepted", offset)
		}
	}
	if _, err := previewRead(t, t.TempDir(), 0, ""); err == nil {
		t.Fatal("directory accepted")
	}
	p = previewFile(t, strings.Repeat("a", testChunkBytes)+"bad\x00tail")
	first, err := previewRead(t, p, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = previewRead(t, p, first.NextOffset, first.Version); err == nil {
		t.Fatal("later binary chunk accepted")
	}
	empty, err := previewRead(t, previewFile(t, ""), 0, "")
	if err != nil || empty.Text != "" || !empty.EOF {
		t.Fatalf("empty: %+v %v", empty, err)
	}
}

func TestExternalTextPreviewFileChangesAndCancellation(t *testing.T) {
	for _, change := range []string{"grow", "truncate", "replace"} {
		t.Run(change, func(t *testing.T) {
			p := previewFile(t, strings.Repeat("a", testChunkBytes+10))
			r, err := previewRead(t, p, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			if change == "replace" {
				replacement := previewFile(t, "changed")
				err = os.Rename(replacement, p)
			} else if change == "truncate" {
				err = os.Truncate(p, 10)
			} else {
				f, e := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
				if e != nil {
					t.Fatal(e)
				}
				_, err = f.WriteString("more")
				f.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = previewRead(t, p, r.NextOffset, r.Version); err == nil || !strings.Contains(err.Error(), "file_changed") {
				t.Fatalf("change not detected: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := handleReadExternalTextChunk(ctx, "", `{}`); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ignored: %v", err)
	}
}
