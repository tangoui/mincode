package fs

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
)

func dispatchLocalPath(command, path string, reveal bool) (string, error) {
	args, _ := json.Marshal(map[string]any{"path": path, "reveal": reveal})
	return core.Dispatch(command, string(args))
}

func TestLocalPathInfo(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "中文 file.txt")
	if err := os.WriteFile(file, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path            string
		exists, isFile, isDir bool
		size                  int64
	}{
		{"file", dir + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(file), true, true, false, 5},
		{"directory", dir, true, false, true, 0},
		{"missing", filepath.Join(dir, "missing.txt"), false, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := dispatchLocalPath("local_path_info", tc.path, false)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Exists, IsFile, IsDir bool
				Size                  int64
				Path, Mime            string
				ModifiedAt            time.Time
			}
			if err := json.Unmarshal([]byte(result), &got); err != nil {
				t.Fatal(err)
			}
			if got.Exists != tc.exists || got.IsFile != tc.isFile || got.IsDir != tc.isDir || got.Size != tc.size {
				t.Fatalf("unexpected info: %s", result)
			}
			if tc.isFile && (filepath.Base(got.Path) != filepath.Base(file) || !strings.HasPrefix(got.Mime, "text/plain") || got.ModifiedAt.IsZero()) {
				t.Fatalf("unexpected metadata: %s", result)
			}
		})
	}
}

func TestLocalPathCommandsRejectInvalidTargets(t *testing.T) {
	paths := []string{"relative.txt", "~/file.txt", "file:///tmp/file.txt", "/invalid\x00name"}
	if runtime.GOOS == "windows" {
		paths = append(paths, `\\.\NUL`, `\\?\GLOBALROOT\Device\Null`, `C:\NUL`, `C:\file.txt:stream`)
	} else if _, err := os.Stat("/dev/null"); err == nil {
		paths = append(paths, "/dev/null")
	}
	for _, command := range []string{"local_path_info", "local_path_open", "local_image_thumbnail"} {
		for _, path := range paths {
			if _, err := dispatchLocalPath(command, path, false); err == nil || strings.Contains(err.Error(), "unknown command") {
				t.Errorf("%s(%q): expected path validation error, got %v", command, path, err)
			}
		}
	}
}

func TestLocalPathOpenUsesLiteralArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses an executable test script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "中文 ; $(echo unexpected).txt")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$LOCAL_PATH_TEST_LOG\"\n"
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LOCAL_PATH_TEST_LOG", log)

	for _, reveal := range []bool{false, true} {
		if _, err := dispatchLocalPath("local_path_open", path, reveal); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{absolute, filepath.Dir(absolute)}
	if runtime.GOOS == "darwin" {
		want = []string{absolute, "-R", absolute}
	}
	if got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"); !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
}

func TestLocalImageThumbnail(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "picture.png")
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 1024, 512))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, original.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := dispatchLocalPath("local_image_thumbnail", file, false)
	if err != nil {
		t.Fatal(err)
	}
	var dataURL string
	if err := json.Unmarshal([]byte(result), &dataURL); err != nil {
		t.Fatal(err)
	}
	encoded, ok := strings.CutPrefix(dataURL, "data:image/png;base64,")
	if !ok {
		t.Fatal("expected a PNG data URL")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width != 512 || config.Height != 256 {
		t.Fatalf("unexpected thumbnail dimensions: %+v, %v", config, err)
	}
	t.Run("rejects a file disguised as an image", func(t *testing.T) {
		if err := os.WriteFile(file, []byte("not a PNG"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := dispatchLocalPath("local_image_thumbnail", file, false); err == nil {
			t.Fatal("expected an invalid image error")
		}
	})
	t.Run("rejects a file larger than 20 MiB", func(t *testing.T) {
		if err := os.WriteFile(file, original.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(file, 20*1024*1024+1); err != nil {
			t.Fatal(err)
		}
		if _, err := dispatchLocalPath("local_image_thumbnail", file, false); err == nil {
			t.Fatal("expected an image size error")
		}
	})
}
