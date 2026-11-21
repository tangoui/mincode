package git

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

// Only the platform's explicit gone error may replace a process handle. Other
// lookup failures cannot establish successful containment.
func statusProcessLookupFailed(err, gone error) bool {
	return err != nil && !errors.Is(err, gone)
}

func TestStatusProcessLookupGoneAndLive(t *testing.T) {
	gone := errors.New("platform process is gone")
	if statusProcessLookupFailed(fmt.Errorf("OpenProcess: %w", gone), gone) {
		t.Error("a definitely gone PID must not fail cleanup")
	}
	if !statusProcessLookupFailed(os.ErrPermission, gone) {
		t.Error("access denied must not count as gone")
	}
	if !statusProcessLookupFailed(errors.New("unknown lookup failure"), gone) {
		t.Error("unknown lookup failure must not count as gone")
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	if process == nil || statusProcessLookupFailed(err, gone) {
		t.Fatal("real live process must retain its cleanup handle")
	}
}
