//go:build windows

package git

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

var statusProcessGoneError error = windows.ERROR_INVALID_PARAMETER

func TestStatusWindowsGonePIDAndLookupErrors(t *testing.T) {
	// Wait closes the final process handle; OpenProcess can legitimately fail
	// with ERROR_INVALID_PARAMETER after successful containment and handle close.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if process != nil {
		defer process.Release()
	}
	if err != nil && !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		t.Fatalf("unexpected gone lookup: %v", err)
	}
	if statusProcessLookupFailed(err, statusProcessGoneError) {
		t.Fatalf("gone PID failed cleanup: %v", err)
	}
	if !statusProcessLookupFailed(windows.ERROR_ACCESS_DENIED, statusProcessGoneError) {
		t.Fatal("access denied was swallowed")
	}
	if !statusProcessLookupFailed(windows.ERROR_INVALID_HANDLE, statusProcessGoneError) {
		t.Fatal("unrelated Windows error was swallowed")
	}
}
