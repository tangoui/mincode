package http

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIDocumentReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.json")
	// 超过通用 5MB 读写上限的文档也能往返。
	content := `{"openapi":"3.0.3","x":"` + strings.Repeat("a", 6*1024*1024) + `"}`
	args, _ := json.Marshal(apiDocumentPathArgs{Path: path, Content: content})
	if _, err := apiDocumentWriteHandler("", string(args)); err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(apiDocumentPathArgs{Path: path})
	result, err := apiDocumentReadHandler("", string(args))
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := json.Unmarshal([]byte(result), &got); err != nil || got != content {
		t.Fatalf("round trip mismatch: len=%d err=%v", len(got), err)
	}

	for _, bad := range []apiDocumentPathArgs{{Path: ""}, {Path: dir}, {Path: filepath.Join(dir, "missing.json")}} {
		args, _ = json.Marshal(bad)
		if _, err := apiDocumentReadHandler("", string(args)); err == nil {
			t.Fatalf("read %q should fail", bad.Path)
		}
	}
	binary := filepath.Join(dir, "bin.json")
	if err := os.WriteFile(binary, []byte{0xff, 0xfe, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(apiDocumentPathArgs{Path: binary})
	if _, err := apiDocumentReadHandler("", string(args)); err == nil || err.Error() != "invalid_api_document" {
		t.Fatalf("binary read err = %v", err)
	}
	args, _ = json.Marshal(apiDocumentPathArgs{Path: dir, Content: "{}"})
	if _, err := apiDocumentWriteHandler("", string(args)); err == nil {
		t.Fatal("write to directory should fail")
	}
}
