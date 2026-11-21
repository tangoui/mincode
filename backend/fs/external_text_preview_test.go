package fs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func previewFile(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "restore.sql")
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return p
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
