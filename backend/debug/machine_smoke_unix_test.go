//go:build !windows

package debug

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMachineSmokePythonShutdownEndsDebuggee launches a blocking script under
// debugpy and checks that Shutdown (the app-exit path) ends the debuggee too.
// debugpy's launcher puts the debuggee in its own process group, so killing
// the adapter's group alone would leave it running; only the graceful
// disconnect ends it.
func TestMachineSmokePythonShutdownEndsDebuggee(t *testing.T) {
	python := smokePython(t)
	script := smokeScript(t, "block.py")
	pidFile := filepath.Join(t.TempDir(), "debuggee.pid")

	ctx, cancel := context.WithTimeout(context.Background(), machineSmokeTimeout)
	defer cancel()
	rec := captureEmits(t)
	t.Cleanup(resetShutdownForTest)
	cmds := captureCmds(t)

	s, err := StartAdapter(ctx, AdapterSpec{
		Kind:          "stdio",
		Executable:    python,
		Args:          []string{"-m", "debugpy.adapter"},
		ProjectRoot:   filepath.Dir(script),
		OwnerPluginID: "machine-smoke",
		DebuggerType:  "python",
	})
	if err != nil {
		t.Fatalf("StartAdapter: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			rec.logAll(t)
		}
	})
	launchArgs, err := json.Marshal(map[string]any{
		"type":       "python",
		"program":    script,
		"args":       []string{pidFile},
		"console":    "internalConsole",
		"justMyCode": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(ctx, BeginParams{AdapterID: "debugpy", Request: "launch", Arguments: launchArgs}); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	pid := waitPIDFile(ctx, t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) }) // never leak it
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("debuggee %d not running before Shutdown: %v", pid, err)
	}
	// Document the premise: the debuggee is outside the adapter's group.
	if adapters := cmds(); len(adapters) == 1 {
		debuggeePgid, err1 := syscall.Getpgid(pid)
		adapterPgid, err2 := syscall.Getpgid(adapters[0].Process.Pid)
		if err1 == nil && err2 == nil {
			t.Logf("adapter pgid %d, debuggee pgid %d", adapterPgid, debuggeePgid)
		}
	}

	Shutdown(ctx)
	if infos := List(); len(infos) != 0 {
		t.Fatalf("List() after Shutdown = %+v", infos)
	}
	// debugpy ends the debuggee and sends `terminated` while handling the
	// disconnect, which may close the session before the response arrives.
	if reason := endedReason(rec, s.ID()); reason != shutdownReason && reason != "adapter terminated" {
		t.Fatalf("end reason = %q, want %q or adapter terminated (not killed after the grace)", reason, shutdownReason)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("debuggee %d still alive after Shutdown (kill 0: %v)", pid, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitPIDFile waits until path holds a pid.
func waitPIDFile(ctx context.Context, t *testing.T, path string) int {
	t.Helper()
	for {
		if b, err := os.ReadFile(path); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || pid <= 0 {
				t.Fatalf("pid file %s = %q", path, b)
			}
			return pid
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("debuggee never wrote %s: %v", path, ctx.Err())
		}
	}
}
