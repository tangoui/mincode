package sftp

import (
	"bytes"
	"encoding/base64"

	"os"
	"strings"
	"testing"
	"time"
)

func TestImagePreviewMIME(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"png", append([]byte("\x89PNG\r\n\x1a\n"), 1), "image/png"},
		{"jpeg", []byte("\xff\xd8\xff\xe0"), "image/jpeg"},
		{"gif87a", []byte("GIF87a"), "image/gif"},
		{"gif89a", []byte("GIF89a"), "image/gif"},
		{"webp", []byte("RIFF1234WEBP"), "image/webp"},
		{"short webp", []byte("RIFFWEBP"), ""},
		{"unsupported", []byte("not an image"), ""},
		{"empty", nil, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := imagePreviewMIME(test.data); got != test.want {
				t.Fatalf("imagePreviewMIME() = %q, want %q", got, test.want)
			}
		})
	}
}

type previewClient struct {
	remoteClient
	info      os.FileInfo
	reader    *bytes.Reader
	opened    bool
	readBytes int
}

func (c *previewClient) Lstat(string) (os.FileInfo, error) { return c.info, nil }
func (c *previewClient) Open(string) (remoteFile, error) {
	c.opened = true
	return &previewFile{client: c}, nil
}

type previewFile struct {
	remoteFile
	client *previewClient
}

func (f *previewFile) Read(p []byte) (int, error) {
	n, err := f.client.reader.Read(p)
	f.client.readBytes += n
	return n, err
}
func (f *previewFile) Close() error { return nil }

type previewInfo struct {
	size int64
	mode os.FileMode
}

func (i previewInfo) Name() string       { return "image" }
func (i previewInfo) Size() int64        { return i.size }
func (i previewInfo) Mode() os.FileMode  { return i.mode }
func (i previewInfo) ModTime() time.Time { return time.Time{} }
func (i previewInfo) IsDir() bool        { return i.mode.IsDir() }
func (i previewInfo) Sys() any           { return nil }

func TestReadImagePreview(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nimage")
	client := &previewClient{info: previewInfo{size: int64(len(png))}, reader: bytes.NewReader(png)}
	result, err := readImagePreview(client, "/image")
	if err != nil || result.MimeType != "image/png" || result.Data != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("unexpected preview: %#v, %v", result, err)
	}

	for _, test := range []struct {
		name    string
		info    previewInfo
		data    []byte
		want    string
		open    bool
		maxRead int
	}{
		{"symlink", previewInfo{mode: os.ModeSymlink}, png, "普通文件", false, 0},
		{"directory", previewInfo{mode: os.ModeDir}, png, "普通文件", false, 0},
		{"large metadata", previewInfo{size: maxImagePreviewSize + 1}, png, "4 MiB", false, 0},
		{"negative size", previewInfo{size: -1}, png, "大小无效", false, 0},
		{"growing file", previewInfo{size: 1}, append(png, bytes.Repeat([]byte("x"), maxImagePreviewSize)...), "4 MiB", true, maxImagePreviewSize + 1},
		{"unsupported", previewInfo{size: 4}, []byte("test"), "不支持", true, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &previewClient{info: test.info, reader: bytes.NewReader(test.data)}
			_, err := readImagePreview(client, "/image")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
			if client.opened != test.open || client.readBytes > test.maxRead {
				t.Fatalf("opened=%v readBytes=%d; want opened=%v at most %d bytes", client.opened, client.readBytes, test.open, test.maxRead)
			}
		})
	}
}

func TestImagePreviewMIMEDoesNotReadBeyondSignature(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 64)
	copy(data, []byte("RIFF"))
	copy(data[8:], []byte("WEBP"))
	if got := imagePreviewMIME(data); got != "image/webp" {
		t.Fatalf("imagePreviewMIME() = %q, want image/webp", got)
	}
}
