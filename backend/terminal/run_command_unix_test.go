//go:build !windows

package terminal

import (
	"testing"
	"time"
)

func TestShellRunCommandTimeoutKillsBackgroundedChildren(t *testing.T) {
	started := time.Now()
	result := invokeShellRunCommand(t, `{"payload":{"command":"sleep 30 & sleep 30","timeoutMs":300}}`)
	if !result.TimedOut || result.ExitCode != -1 {
		t.Fatalf("expected timed out result, got %+v", result)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("timeout did not terminate the process tree: %v", elapsed)
	}
}

func TestShellRunCommandReturnsWhenBackgroundChildHoldsPipe(t *testing.T) {
	started := time.Now()
	result := invokeShellRunCommand(t, `{"payload":{"command":"echo ready; sleep 30 &","timeoutMs":60000}}`)
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("Run stayed blocked on a pipe held by a background child: %v", elapsed)
	}
	if result.TimedOut || result.ExitCode != 0 || result.Stdout != "ready\n" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
