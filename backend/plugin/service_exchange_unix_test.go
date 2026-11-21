//go:build !windows

package plugin

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO (or a symlink to one) must be refused before it is opened: opening
// it would block forever and hold one of the global file-call slots.
func TestExchangeSnapshotRejectsFIFO(t *testing.T) {
	x, _ := newTestExchange(t)
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	link := filepath.Join(dir, "pipe-link")
	if err := syscall.Symlink(fifo, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	mustBeginExchangeCall(t, x, "call-1")
	for _, source := range []string{fifo, link} {
		done := make(chan error, 1)
		go func() {
			_, err := x.snapshotPath("call-1", "provider.a", source)
			done <- err
		}()
		select {
		case err := <-done:
			requireExchangeCode(t, err, contributionErrInvalidRequest)
		case <-time.After(5 * time.Second):
			t.Fatalf("snapshot of %s blocked", filepath.Base(source))
		}
	}
	if x.used != 0 || x.calls["call-1"].bytes != 0 {
		t.Fatalf("FIFO snapshot left accounting: used=%d call=%d", x.used, x.calls["call-1"].bytes)
	}
}
