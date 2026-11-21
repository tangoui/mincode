package terminalactivity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMapHookEventUsesOnlySafeFields(t *testing.T) {
	phase, summary, label := mapHookEvent(hookInput{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Fix the login screen",
	})
	if phase != "active" || summary != "" || label != "Fix the login screen" {
		t.Fatalf("unexpected prompt mapping: %q %q %q", phase, summary, label)
	}
	phase, summary, _ = mapHookEvent(hookInput{
		HookEventName: "PermissionRequest",
		ToolName:      "Bash",
	})
	if phase != "waiting_approval" || summary != "Waiting for approval Bash" {
		t.Fatalf("unexpected approval mapping: %q %q", phase, summary)
	}
}

func TestMapHookEventMarksClaudeTurnAndSessionTerminalStates(t *testing.T) {
	phase, summary, _ := mapHookEvent(hookInput{HookEventName: "Stop"})
	if phase != "waiting_input" || summary != "Claude turn completed" {
		t.Fatalf("unexpected Stop mapping: %q %q", phase, summary)
	}
	phase, summary, _ = mapHookEvent(hookInput{HookEventName: "SessionEnd"})
	if phase != "completed" || summary != "Claude session ended" {
		t.Fatalf("unexpected SessionEnd mapping: %q %q", phase, summary)
	}
	// 会话刚起来（含 --resume）只是回到提示符等输入，标成 active 会让标签一直转圈。
	phase, summary, _ = mapHookEvent(hookInput{HookEventName: "SessionStart"})
	if phase != "waiting_input" || summary != "Claude session ready" {
		t.Fatalf("unexpected SessionStart mapping: %q %q", phase, summary)
	}
}

func TestResolveClaudeExecutableUsesExpandedProbePath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if resolved := findExecutableOnPath("claude", mergeProbePaths("/usr/bin", directory)); resolved != path {
		t.Fatalf("resolved = %q, want %q", resolved, path)
	}
}

// resetClaudeProbeState 隔离能力探测缓存，并把重试间隔交给调用方控制。
func resetClaudeProbeState(t *testing.T, retryInterval time.Duration) {
	t.Helper()
	state.mu.Lock()
	previousSupported := state.claudeSupportsSettings
	previousFailedAt := state.claudeProbeFailedAt
	state.claudeSupportsSettings = false
	state.claudeProbeFailedAt = time.Time{}
	state.mu.Unlock()
	previousResolver := resolveClaudeExecutableForProbe
	previousRunner := runClaudeHelpForProbe
	previousInterval := claudeProbeRetryInterval
	claudeProbeRetryInterval = retryInterval
	t.Cleanup(func() {
		resolveClaudeExecutableForProbe = previousResolver
		runClaudeHelpForProbe = previousRunner
		claudeProbeRetryInterval = previousInterval
		state.mu.Lock()
		state.claudeSupportsSettings = previousSupported
		state.claudeProbeFailedAt = previousFailedAt
		state.mu.Unlock()
	})
}

func TestClaudeCapabilityProbeRetriesFailuresAndCachesSuccess(t *testing.T) {
	resetClaudeProbeState(t, 0)

	resolveCalls := 0
	runCalls := 0
	resolveClaudeExecutableForProbe = func() string {
		resolveCalls++
		return "/tmp/fake-claude"
	}
	runClaudeHelpForProbe = func(string) ([]byte, error) {
		runCalls++
		if runCalls == 1 {
			return nil, os.ErrNotExist
		}
		return []byte("Usage: claude --settings path"), nil
	}

	if claudeSupportsSettings() {
		t.Fatal("failed capability probe must not be cached as supported")
	}
	if !claudeSupportsSettings() {
		t.Fatal("capability probe must retry after a transient failure")
	}
	if !claudeSupportsSettings() {
		t.Fatal("successful capability probe must remain supported")
	}
	if resolveCalls != 2 || runCalls != 2 {
		t.Fatalf("unexpected probe calls: resolve=%d run=%d", resolveCalls, runCalls)
	}
}

func TestClaudeCapabilityProbeSuppressesRepeatedFailuresWithinRetryInterval(t *testing.T) {
	resetClaudeProbeState(t, time.Hour)

	resolveCalls := 0
	resolveClaudeExecutableForProbe = func() string {
		resolveCalls++
		return ""
	}
	runClaudeHelpForProbe = func(string) ([]byte, error) {
		t.Fatal("help must not run when Claude cannot be resolved")
		return nil, nil
	}

	for attempt := 0; attempt < 3; attempt++ {
		if claudeSupportsSettings() {
			t.Fatalf("attempt %d reported support without a Claude executable", attempt)
		}
	}
	if resolveCalls != 1 {
		t.Fatalf("probe ran %d times within the retry interval, want 1", resolveCalls)
	}
}

func TestRandomTokensAreUnique(t *testing.T) {
	first, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || first == second {
		t.Fatalf("unexpected tokens: %q %q", first, second)
	}
}

func TestRunHookWritesPrivateAtomicEnvelope(t *testing.T) {
	inbox := t.TempDir()
	secretMarker := "private" + "-test-value"
	t.Setenv("MINCODE_CLI_TAB_TOKEN", strings.Repeat("a", 64))
	t.Setenv("MINCODE_TERMINAL_ACTIVITY_INBOX", inbox)
	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"session-1","prompt":"Fix login. Do not persist this complete prompt password=` + secretMarker + `"}`
	if err := RunHook([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".ready") {
		t.Fatalf("unexpected inbox entries: %#v", entries)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("event permissions = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(inbox, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "Do not persist") || strings.Contains(text, secretMarker) {
		t.Fatalf("event persisted complete or sensitive prompt: %s", text)
	}
	if !strings.Contains(text, `"prompt":"Fix login."`) {
		t.Fatalf("event missing safe prompt summary: %s", text)
	}
}

func TestWriteSettingsUsesDirectHookHelper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := writeSettings(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string   `json:"type"`
				Command string   `json:"command"`
				Args    []string `json:"args"`
				Async   bool     `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"UserPromptSubmit", "PermissionRequest", "Stop", "StopFailure"} {
		entries := settings.Hooks[event]
		if len(entries) != 1 || len(entries[0].Hooks) != 1 {
			t.Fatalf("settings missing %s hook: %s", event, data)
		}
		handler := entries[0].Hooks[0]
		if handler.Type != "command" || handler.Command == "" || len(handler.Args) != 1 || handler.Args[0] != hookModeArgument || !handler.Async {
			t.Fatalf("invalid %s helper: %#v", event, handler)
		}
	}
}

func TestExpiredRegistrationIsRejected(t *testing.T) {
	state.mu.Lock()
	state.registrations = map[string]registration{
		"expired": {PaneID: "pane-1", CreatedAt: time.Now().Add(-registrationTTL - time.Minute)},
	}
	state.mu.Unlock()
	path := filepath.Join(t.TempDir(), "expired.ready")
	if err := os.WriteFile(path, []byte(`{"token":"expired","received":"2026-08-20T00:00:00Z","input":{"hook_event_name":"Stop"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	processEventFile(path)
	state.mu.Lock()
	_, exists := state.registrations["expired"]
	state.mu.Unlock()
	if exists {
		t.Fatal("expired token remains registered")
	}
}

func TestUnregisterPaneRemovesOnlyMatchingTokens(t *testing.T) {
	now := time.Now()
	state.mu.Lock()
	state.registrations = map[string]registration{
		"one":   {PaneID: "pane-1", CreatedAt: now},
		"two":   {PaneID: "pane-1", CreatedAt: now},
		"other": {PaneID: "pane-2", CreatedAt: now},
	}
	state.mu.Unlock()
	unregisterPane("pane-1")
	state.mu.Lock()
	_, oneExists := state.registrations["one"]
	_, twoExists := state.registrations["two"]
	_, otherExists := state.registrations["other"]
	state.mu.Unlock()
	if oneExists || twoExists || !otherExists {
		t.Fatalf("unexpected registrations after unregister: one=%v two=%v other=%v", oneExists, twoExists, otherExists)
	}
}

func TestSweepExpiredRegistrationsKeepsLiveTokens(t *testing.T) {
	now := time.Now()
	state.mu.Lock()
	state.registrations = map[string]registration{
		"expired": {PaneID: "pane-1", CreatedAt: now.Add(-registrationTTL - time.Minute)},
		"live":    {PaneID: "pane-2", CreatedAt: now.Add(-time.Minute)},
	}
	state.mu.Unlock()
	sweepExpiredRegistrations(now)
	state.mu.Lock()
	_, expiredExists := state.registrations["expired"]
	_, liveExists := state.registrations["live"]
	state.mu.Unlock()
	if expiredExists || !liveExists {
		t.Fatalf("unexpected registrations after sweep: expired=%v live=%v", expiredExists, liveExists)
	}
}
