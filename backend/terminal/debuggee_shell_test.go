package terminal

import (
	"context"
	"strings"
	"testing"

	"mincode/app/backend/core"
)

func TestAttachLocalShellOnlyAttaches(t *testing.T) {
	const id = "attach-only-unknown-session"
	_, err := core.DispatchContext(context.Background(), "attach_local_shell", `{"sessionId":"`+id+`"}`)
	if err == nil || !strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("err = %v, want not_found:", err)
	}
	if _, ok := sessions.get(id); ok {
		t.Fatal("attach_local_shell created a session")
	}
	_, err = core.DispatchContext(context.Background(), "attach_local_shell", `{"sessionId":"  "}`)
	if err == nil || !strings.HasPrefix(err.Error(), "invalid_request:") {
		t.Fatalf("empty id: err = %v, want invalid_request:", err)
	}
}

func TestOutputLogKeepsTailOnRuneBoundary(t *testing.T) {
	l := newOutputLog(8)
	if end := l.append("abc"); end != 3 {
		t.Fatalf("end = %d", end)
	}
	if end := l.append("dé€f"); end != 3+1+2+3+1 { // bytes
		t.Fatalf("end = %d", end)
	}
	out, end := l.snapshot()
	if end != 10 {
		t.Fatalf("snapshot end = %d", end)
	}
	if len(out) > 8 || !strings.HasSuffix("abcdé€f", out) || !strings.HasSuffix(out, "€f") {
		t.Fatalf("snapshot = %q", out)
	}
}

func TestValidateDebuggeeExecutable(t *testing.T) {
	spec := DebuggeeShellSpec{SessionID: "s", Executable: "relative", Cwd: t.TempDir()}
	if _, err := StartDebuggeeShell(spec); err == nil {
		t.Fatal("relative executable accepted")
	}
	spec = DebuggeeShellSpec{SessionID: "s", Executable: "/x", Cwd: t.TempDir(), UnsetEnv: []string{"A=B"}}
	if _, err := StartDebuggeeShell(spec); err == nil {
		t.Fatal("invalid unset name accepted")
	}
}

func TestEndedOutputsAreBounded(t *testing.T) {
	store := &sessionStore{procs: map[string]*shellProcess{}, starting: map[string]chan struct{}{}, ended: map[string]*outputLog{}}
	for i := 0; i < endedOutputsMax+3; i++ {
		id := "debug-s-" + string(rune('a'+i))
		store.procs[id] = &shellProcess{output: newOutputLog(16)}
		if _, ok := store.removeKeepingOutput(id); !ok {
			t.Fatalf("%s not removed", id)
		}
	}
	if len(store.ended) != endedOutputsMax || len(store.endedOrder) != endedOutputsMax {
		t.Fatalf("kept %d outputs (order %d), want %d", len(store.ended), len(store.endedOrder), endedOutputsMax)
	}
	for _, evicted := range []string{"debug-s-a", "debug-s-b", "debug-s-c"} {
		if _, ok := store.ended[evicted]; ok {
			t.Fatalf("oldest output %s was not evicted", evicted)
		}
	}
	// Closing one drops it from both the map and the order.
	newest := "debug-s-" + string(rune('a'+endedOutputsMax+2))
	store.removeWithOutput(newest)
	if _, ok := store.ended[newest]; ok || len(store.endedOrder) != endedOutputsMax-1 {
		t.Fatalf("removeWithOutput kept %s (order %v)", newest, store.endedOrder)
	}
	// A session without kept output (an ordinary shell) leaves nothing behind.
	store.procs["shell"] = &shellProcess{}
	store.removeKeepingOutput("shell")
	if _, ok := store.ended["shell"]; ok {
		t.Fatal("an ordinary shell's output was kept")
	}
}
