package fs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectExternalFileClassifiesTextAndBinary(t *testing.T) {
	dir := t.TempDir()
	textPath := filepath.Join(dir, "config.unknown")
	if err := os.WriteFile(textPath, []byte("hello\n世界\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	textInfo, err := os.Stat(textPath)
	if err != nil {
		t.Fatal(err)
	}
	kind, _, err := inspectExternalFile(textPath, textInfo)
	if err != nil || kind != "text" {
		t.Fatalf("inspectExternalFile(text) = %q, %v", kind, err)
	}

	binaryPath := filepath.Join(dir, "archive.bin")
	if err := os.WriteFile(binaryPath, []byte{0x01, 0x00, 0x02}, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryInfo, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	kind, _, err = inspectExternalFile(binaryPath, binaryInfo)
	if err != nil || kind != "binary" {
		t.Fatalf("inspectExternalFile(binary) = %q, %v", kind, err)
	}
}

func TestInspectExternalFileClassifiesPreviewTypes(t *testing.T) {
	dir := t.TempDir()
	for name, want := range map[string]string{
		"image.png":  "image",
		"manual.pdf": "pdf",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("preview"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		kind, _, err := inspectExternalFile(path, info)
		if err != nil || kind != want {
			t.Fatalf("inspectExternalFile(%s) = %q, %v; want %q", name, kind, err, want)
		}
	}
}

func TestInspectExternalDirectoryDoesNotAllowFileOperations(t *testing.T) {
	root := t.TempDir()
	args, err := json.Marshal(externalFilePathArgs{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	result, err := handleInspectExternalFile("", string(args))
	if err != nil {
		t.Fatal(err)
	}
	var info externalFileInfo
	if err := json.Unmarshal([]byte(result), &info); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "directory" || info.Path != canonicalRoot {
		t.Fatalf("unexpected directory info: %+v", info)
	}
	if _, _, err := externalRegularFile(root); err == nil {
		t.Fatal("a directory must not be accepted for file reads/writes")
	}
	if _, err := handleInspectExternalFile("", `{"path":""}`); err == nil {
		t.Fatal("empty path accepted")
	}
}
