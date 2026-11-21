package plugin

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPluginDirectoryRenameRetry(t *testing.T) {
	transient := errors.New("transient lock")
	permanent := errors.New("permanent error")
	for _, tc := range []struct {
		name      string
		failures  int
		err       error
		wantCalls int
		wantErr   error
	}{
		{"immediate", 0, nil, 1, nil},
		{"transient", 2, transient, 3, nil},
		{"bounded", 100, transient, 8, transient},
		{"permanent", 1, permanent, 1, permanent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, waits := 0, 0
			err := renamePluginDirectoryWith("source", filepath.Join(t.TempDir(), "target"), func(_, _ string) error {
				calls++
				if calls <= tc.failures {
					return tc.err
				}
				return nil
			}, func(delay time.Duration) {
				waits++
				if delay != time.Duration(waits)*100*time.Millisecond {
					t.Fatalf("unexpected delay: %v", delay)
				}
			}, func(err error) bool { return errors.Is(err, transient) })
			if !errors.Is(err, tc.wantErr) || calls != tc.wantCalls || waits != calls-1 {
				t.Fatalf("err=%v calls=%d waits=%d", err, calls, waits)
			}
		})
	}
}

func TestPluginDirectoryRenameDoesNotRetryExistingTarget(t *testing.T) {
	target := t.TempDir()
	calls := 0
	err := renamePluginDirectoryWith("source", target, func(_, _ string) error {
		calls++
		return fs.ErrPermission
	}, func(time.Duration) { t.Fatal("must not wait when target exists") }, func(error) bool { return true })
	if !errors.Is(err, fs.ErrPermission) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestPluginDirectoryRenameStopsWhenTargetAppearsDuringBackoff(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target")
	calls := 0
	err := renamePluginDirectoryWith("source", target, func(_, _ string) error {
		calls++
		return fs.ErrPermission
	}, func(time.Duration) {
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
	}, func(error) bool { return true })
	if !errors.Is(err, fs.ErrPermission) || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}

func TestPluginDirectoryRenameCommitsWholePackage(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, ".install-fixture"), filepath.Join(root, "fixture")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "marker"), []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renamePluginDirectory(source, target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "marker"))
	if err != nil || string(data) != "complete" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := os.Stat(source); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staging still exists: %v", err)
	}
}
