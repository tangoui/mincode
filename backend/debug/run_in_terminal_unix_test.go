//go:build !windows

package debug

import "testing"

// waitProcessGone waits until the debuggee pid no longer exists.
func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	waitGone(t, pid)
}
