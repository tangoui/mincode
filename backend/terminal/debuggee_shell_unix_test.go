//go:build !windows

package terminal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
)

type attachLocalShellResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Offset int64  `json:"offset"`
	Ended  bool   `json:"ended"`
}

func attachForTest(t *testing.T, id string) (attachLocalShellResult, error) {
	t.Helper()
	raw, err := core.DispatchContext(context.Background(), "attach_local_shell", `{"sessionId":"`+id+`"}`)
	if err != nil {
		return attachLocalShellResult{}, err
	}
	var r attachLocalShellResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("attach result %s: %v", raw, err)
	}
	return r, nil
}

func waitAttach(t *testing.T, id string, done func(attachLocalShellResult) bool) attachLocalShellResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last attachLocalShellResult
	for time.Now().Before(deadline) {
		r, err := attachForTest(t, id)
		if err != nil {
			t.Fatalf("attach %s: %v", id, err)
		}
		last = r
		if done(r) {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("attach %s never matched; last %+v", id, last)
	return last
}

func TestStartDebuggeeShellBuffersOutputForLateAttach(t *testing.T) {
	const id = "debuggee-test-late-attach"
	t.Setenv("MINCODE_TERMINAL_UNSET_ME", "inherited")
	pid, err := StartDebuggeeShell(DebuggeeShellSpec{
		SessionID:  id,
		Executable: "/bin/sh",
		Args:       []string{"-c", `printf 'A=%s U=%s\n' "$MINCODE_A" "${MINCODE_TERMINAL_UNSET_ME-unset}"; sleep 5`},
		Env:        map[string]string{"MINCODE_A": "1"},
		UnsetEnv:   []string{"MINCODE_TERMINAL_UNSET_ME"},
		Cwd:        t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseLocalShell(id) })
	if pid <= 0 {
		t.Fatalf("pid = %d", pid)
	}
	r := waitAttach(t, id, func(r attachLocalShellResult) bool { return strings.Contains(r.Output, "\n") })
	if !r.OK || r.Ended || !strings.Contains(r.Output, "A=1 U=unset") || r.Offset != int64(len(r.Output)) {
		t.Fatalf("attach = %+v", r)
	}
	CloseLocalShell(id)
	if _, err := attachForTest(t, id); err == nil || !strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("after close: err = %v", err)
	}
}

func TestDebuggeeOutputSurvivesExitUntilClosed(t *testing.T) {
	const id = "debuggee-test-exit"
	_, err := StartDebuggeeShell(DebuggeeShellSpec{
		SessionID: id, Executable: "/bin/sh", Args: []string{"-c", "printf bye"}, Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseLocalShell(id) })
	r := waitAttach(t, id, func(r attachLocalShellResult) bool { return r.Ended })
	if !r.OK || r.Output != "bye" || r.Offset != 3 {
		t.Fatalf("attach after exit = %+v", r)
	}
	if _, ok := sessions.get(id); ok {
		t.Fatal("exited debuggee still has a live session")
	}
	CloseLocalShell(id)
	if _, err := attachForTest(t, id); err == nil || !strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("after close: err = %v", err)
	}
}

func TestAttachLocalShellPlainSessionHasNoBuffer(t *testing.T) {
	spec := ProfileShellSpec{SessionID: "attach-plain-profile", Executable: "/bin/sh", Args: []string{"-c", "sleep 5"}, Cwd: t.TempDir()}
	if _, err := StartProfileShell(spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseLocalShell(spec.SessionID) })
	r, err := attachForTest(t, spec.SessionID)
	if err != nil || !r.OK || r.Output != "" || r.Ended {
		t.Fatalf("attach = %+v, %v", r, err)
	}
}
