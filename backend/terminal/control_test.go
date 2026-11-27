package terminal

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/agentcontrol"
)

func TestOutputRingKeepsRecentPlainText(t *testing.T) {
	ring := newOutputRing(24, 3)
	for _, chunk := range []string{"old\r\nline\r\n", "\x1b[3", "1mred\x1b[0m\n", "\x1b]8;;secret", "\x1b\\link\x1b]8;;\x07\n", "\x1bPignored\x1b\\last"} {
		ring.write(chunk)
	}
	if got := ring.read(0); got != "red\nlink\nlast" {
		t.Fatalf("output = %q", got)
	}
	if got := ring.read(2); got != "link\nlast" {
		t.Fatalf("last two lines = %q", got)
	}
	ring.write(strings.Repeat("中", 14))
	if got := ring.read(0); got != strings.Repeat("中", 8) {
		t.Fatalf("bounded output = %q", got)
	}
}

func registerTestControlPane(t *testing.T, claude bool) (string, *controlledPane, chan string) {
	t.Helper()
	id := t.Name()
	input := make(chan string, 1)
	proc := &shellProcess{stdinTx: input, stdinDone: make(chan struct{})}
	proc.control = addControlledPane(id, "", "", claude, nil)
	sessions.put(id, proc)
	t.Cleanup(func() { CloseLocalShell(id) })
	return id, proc.control, input
}

func TestControlWaitRecordsQuietOutputAfterSend(t *testing.T) {
	id, pane, input := registerTestControlPane(t, false)
	if err := ControlSend(id, "echo OK", true); err != nil {
		t.Fatal(err)
	}
	if got := <-input; got != "echo OK\r" {
		t.Fatalf("input = %q", got)
	}
	pane.observeOutput("OK\r\n", time.Now().Add(-attentionQuietAfter-time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if state, err := ControlWait(ctx, id); state != "idle" || err != nil {
		t.Fatalf("wait = %q, %v", state, err)
	}
	if got, err := ControlRead(id, 1); got != "OK\n" || err != nil {
		t.Fatalf("read = %q, %v", got, err)
	}
}

func TestControlWaitReturnsExitedAndKeepsOutputUntilClose(t *testing.T) {
	id, pane, _ := registerTestControlPane(t, false)
	pane.observeOutput("completed\n", time.Now())
	pane.end()
	if state, err := ControlWait(context.Background(), id); state != "exited" || err != nil {
		t.Fatalf("wait = %q, %v", state, err)
	}
	if got, err := ControlRead(id, 0); got != "completed\n" || err != nil {
		t.Fatalf("read before close = %q, %v", got, err)
	}
	CloseLocalShell(id)
	if _, err := ControlRead(id, 0); err != agentcontrol.ErrPaneNotFound {
		t.Fatalf("read after close error = %v", err)
	}
}

func TestControlWaitReturnsClaudeWaitingInputAndIgnoresOldHookAfterSend(t *testing.T) {
	id, _, input := registerTestControlPane(t, true)
	old := time.Now()
	observeClaudeActivity(id, "waiting_input", "SessionStart", old)
	if state, err := ControlWait(context.Background(), id); state != "waiting_input" || err != nil {
		t.Fatalf("wait = %q, %v", state, err)
	}
	if err := ControlSend(id, "next", true); err != nil {
		t.Fatal(err)
	}
	<-input
	observeClaudeActivity(id, "waiting_input", "Notification", old)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if state, err := ControlWait(ctx, id); state != "timeout" || err != nil {
		t.Fatalf("wait after send = %q, %v", state, err)
	}
	observeClaudeActivity(id, "waiting_input", "Stop", time.Now())
	if state, err := ControlWait(context.Background(), id); state != "idle" || err != nil {
		t.Fatalf("completed Claude wait = %q, %v", state, err)
	}
}

func TestControlEnvironmentOnlyAddsCredentialsToLocalShell(t *testing.T) {
	t.Setenv("MINCODE_DATA_DIR", t.TempDir())
	if err := agentcontrol.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agentcontrol.Shutdown)
	dir := t.TempDir()
	env := []string{"PATH=/original", "MINCODE_CONTROL_TOKEN=inherited", "MINCODE_PANE_ID=old"}
	local, _ := paneEnvironment(env, "local", dir, true)
	remote, _ := paneEnvironment(env, "remote", dir, false)
	values := map[string]string{}
	for _, pair := range local {
		key, value, _ := strings.Cut(pair, "=")
		values[key] = value
	}
	if values["MINCODE_PANE_ID"] != "local" || len(values["MINCODE_CONTROL_TOKEN"]) != 64 || values["PATH"] != dir+string(os.PathListSeparator)+"/original" {
		t.Fatalf("local shell environment missing pane ID, token or helper PATH")
	}
	if strings.Contains(strings.Join(remote, "\n"), "MINCODE_") || strings.Join(remote, "\n") != "PATH=/original" {
		t.Fatal("remote shell received control credentials or helper PATH")
	}
	req, _ := http.NewRequest(http.MethodPost, values["MINCODE_CONTROL_URL"]+"/v1/tools/list", nil)
	req.Header.Set("Authorization", "Bearer "+values["MINCODE_CONTROL_TOKEN"])
	CloseLocalShell("local")
	res, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("closed pane token status = %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "running.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected helper-directory record: %v", err)
	}
}

func TestControlWaitReturnsFirstHookTurnWithoutSend(t *testing.T) {
	id, _, _ := registerTestControlPane(t, true)
	now := time.Now()
	observeClaudeActivity(id, "active", "UserPromptSubmit", now)
	observeClaudeActivity(id, "waiting_input", "Stop", now.Add(time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if state, err := ControlWait(ctx, id); state != "idle" || err != nil {
		t.Fatalf("first hook turn = %q, %v", state, err)
	}
}

func TestControlReadRedactsSecretsBeforeSelectingLines(t *testing.T) {
	id, pane, _ := registerTestControlPane(t, false)
	pane.observeOutput("password=fixture-secret\n-----BEGIN PRIVATE KEY-----\nfixture-key\n-----END PRIVATE KEY-----\n", time.Now())
	for _, lines := range []int{0, 2} {
		got, err := ControlRead(id, lines)
		if err != nil || strings.Contains(got, "fixture-key") || strings.Contains(got, "fixture-secret") || !strings.Contains(got, "REDACTED") {
			t.Fatalf("read with %d lines returned sensitive output or failed", lines)
		}
	}
}
