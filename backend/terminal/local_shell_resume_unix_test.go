//go:build (darwin && cgo) || linux

package terminal

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"mincode/app/backend/core"
)

func TestCaptureTerminalResumeFromForegroundPTY(t *testing.T) {
	t.Setenv("MINCODE_DATA_DIR", t.TempDir())
	const sessionID = "terminal-resume-foreground-test"
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sleep, err := filepath.EvalSymlinks("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	shell := "/bin/bash"
	payload := StartLocalShellPayload{SessionID: sessionID, CustomPath: &shell, Cwd: &cwd, InitialArgs: []string{"--noprofile", "--norc", "-i"}}
	start, err := json.Marshal(map[string]any{"payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Dispatch("start_local_shell", string(start)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseLocalShell(sessionID) })
	if records := captureTerminalResume(); len(records) != 0 {
		t.Fatalf("idle shell produced resume records: %v", records)
	}
	if err := writeToShell(WriteToShellPayload{SessionID: sessionID, Input: "/bin/sleep 1000\r"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		record := captureTerminalResume()[sessionID]
		if record == (terminalResumeRecord{Command: sleep + " 1000", Cwd: cwd}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreground resume = %#v, want /bin/sleep 1000 in %s", record, cwd)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := SaveLocalShellResume(); err != nil {
		t.Fatal(err)
	}
	result, err := core.Dispatch("take_local_shell_resume", "{}")
	if err != nil {
		t.Fatal(err)
	}
	var records map[string]terminalResumeRecord
	if err := json.Unmarshal([]byte(result), &records); err != nil {
		t.Fatal(err)
	}
	if records[sessionID].Cwd != cwd {
		t.Fatalf("saved foreground record = %#v", records[sessionID])
	}
}
