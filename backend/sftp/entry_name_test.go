package sftp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsSafeLocalEntryName(t *testing.T) {
	windows := filepath.Separator == '\\'
	tests := []struct {
		name string
		want bool
	}{
		{"file.txt", true},
		{".hidden", true},
		{"中文 名称.log", true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{"../evil", false},
		{"/etc/passwd", false},
		{"bad\x00name", false},
		// 以下名称在 Unix 上是合法文件名，在 Windows 上会逃逸或指向设备/备用数据流。
		{`..\..\evil.exe`, !windows},
		{`a\b`, !windows},
		{`C:x`, !windows},
		{`C:\Windows\evil.dll`, !windows},
		{`file.txt:stream`, !windows},
		{"NUL", !windows},
		{"COM1", !windows},
	}
	for _, test := range tests {
		if got := isSafeLocalEntryName(test.name); got != test.want {
			t.Errorf("isSafeLocalEntryName(%q) = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestResolveDownloadTargetRejectsUnsafeEntryName(t *testing.T) {
	names := []string{"..", "a/b"}
	if filepath.Separator == '\\' {
		names = append(names, `..\..\evil.exe`, `C:x`)
	}
	for _, name := range names {
		if got, err := resolveDownloadTarget(nil, name); err == nil {
			t.Errorf("resolveDownloadTarget(nil, %q) = %q, want error", name, got)
		}
	}
}

type entryNameDirClient struct {
	remoteClient
	entries []os.FileInfo
}

func (c *entryNameDirClient) ReadDir(string) ([]os.FileInfo, error) { return c.entries, nil }

type entryNameInfo struct{ name string }

func (i entryNameInfo) Name() string       { return i.name }
func (i entryNameInfo) Size() int64        { return 0 }
func (i entryNameInfo) Mode() os.FileMode  { return 0o644 }
func (i entryNameInfo) ModTime() time.Time { return time.Time{} }
func (i entryNameInfo) IsDir() bool        { return false }
func (i entryNameInfo) Sys() any           { return nil }

func TestDownloadRemoteDirectoryRejectsUnsafeEntryName(t *testing.T) {
	unsafe := ".."
	if filepath.Separator == '\\' {
		unsafe = `..\evil.exe`
	}
	localDir := t.TempDir()
	client := &entryNameDirClient{entries: []os.FileInfo{entryNameInfo{name: unsafe}}}
	if err := downloadRemoteDirectory(client, "/remote", localDir); err == nil {
		t.Fatalf("downloadRemoteDirectory accepted entry %q", unsafe)
	}
}
