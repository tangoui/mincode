//go:build windows

package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPluginDirectoryRenameWindowsErrorClassification(t *testing.T) {
	for _, code := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION} {
		if !retryablePluginRenameError(&os.LinkError{Op: "rename", Err: code}) {
			t.Fatalf("not retryable: %v", code)
		}
	}
	for _, code := range []error{windows.ERROR_FILE_NOT_FOUND, windows.ERROR_ALREADY_EXISTS, windows.ERROR_DISK_FULL} {
		if retryablePluginRenameError(code) {
			t.Fatalf("unexpected retry: %v", code)
		}
	}
}

func TestPluginDirectoryRenameWindowsLockedDirectory(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, ".install-locked"), filepath.Join(root, "plugin")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(source)
	if err != nil {
		t.Fatal(err)
	}
	// Hold a real directory handle without FILE_SHARE_DELETE, like a scanner.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = windows.CloseHandle(handle)
		}
	}()
	if err := os.Rename(source, target); err == nil || !retryablePluginRenameError(err) {
		t.Fatalf("expected Windows lock error, got %v", err)
	}
	calls := 0
	err = renamePluginDirectoryWith(source, target, func(from, to string) error {
		calls++
		return os.Rename(from, to)
	}, func(time.Duration) {
		if err := windows.CloseHandle(handle); err != nil {
			t.Fatal(err)
		}
		closed = true
	}, retryablePluginRenameError)
	if err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging still present: %v", err)
	}
}
