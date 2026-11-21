package plugin

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"mincode/app/backend/core"
)

// Stopping an AI run cancels the IPC call's context; workspace_exec must kill the
// command instead of letting it run until its own timeout.
func TestWorkspaceExecStopsWhenCallerCancels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh sleep")
	}
	args, _ := json.Marshal(map[string]any{"cmd": "sleep 30", "cwd": t.TempDir(), "timeoutSecs": 60})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	started := time.Now()
	_, err := core.DispatchContext(ctx, "workspace_exec", string(args))
	if err == nil {
		t.Fatal("cancelled command reported success")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("command kept running after cancellation: %v", elapsed)
	}
}
