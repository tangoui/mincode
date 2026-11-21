//go:build windows

package debug

import "testing"

// waitProcessGone is a no-op on Windows: the closed ConPTY ends the debuggee
// and the not_found attach already shows the terminal is gone.
func waitProcessGone(t *testing.T, _ int) { t.Helper() }
