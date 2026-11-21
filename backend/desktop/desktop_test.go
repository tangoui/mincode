package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseDeepLinkAllowlist(t *testing.T) {
	for _, raw := range []string{"mincode://settings", "mincode://ai", "mincode://workspace?view=database"} {
		if _, err := ParseDeepLink(raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"https://example.com", "mincode://workspace?view=shell", "mincode://run?command=rm"} {
		if _, err := ParseDeepLink(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestLaunchPayloadOnlyExistingFiles(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.txt")
	if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	files, targets := LaunchPayload([]string{"mincode", p, "--debug", "mincode://settings", filepath.Join(root, "missing")}, root)
	if len(files) != 1 || files[0] != p || len(targets) != 1 {
		t.Fatalf("%v %v", files, targets)
	}
}
func TestClampWindowStateToVisibleArea(t *testing.T) {
	area := WorkArea{X: 0, Y: 0, Width: 1440, Height: 900}
	got := ClampWindowState(WindowState{X: 3000, Y: 2000, Width: 1600, Height: 1000}, []WorkArea{area})
	if got.X < 0 || got.Y < 0 || got.Width > 1440 || got.Height > 900 {
		t.Fatalf("%+v", got)
	}
}

func TestValidateTarget(t *testing.T) {
	for _, target := range []NavigationTarget{
		{Kind: "ai"},
		{Kind: "ai-conversation", Value: `{"conversationId":"conversation-1","workspaceScopeId":"workspace:one"}`},
		{Kind: "view", Value: "settings"},
		{Kind: "plugin", Value: `{"pluginId":"focus-timer","viewId":"main"}`},
		{Kind: "plugin", Value: `{"pluginId":"focus-timer"}`},
		{Kind: "app"},
		{Kind: "agent-runs"},
		{Kind: "agent-run", Value: "run-0123456789abcdef0123456789abcdef"},
		{Kind: "terminal-pane", Value: "6f1c2a9e-1b2c-4d5e-8f90-0123456789ab"},
		{Kind: "terminal-pane", Value: "cc-live-lx3k2-1"},
	} {
		if err := ValidateTarget(target); err != nil {
			t.Fatalf("%+v: %v", target, err)
		}
	}
	for _, target := range []NavigationTarget{{Kind: "command", Value: "rm"}, {Kind: "view", Value: "shell"}, {Kind: "ai", Value: "payload"}, {Kind: "ai-conversation"}, {Kind: "ai-conversation", Value: `{"workspaceScopeId":"workspace:one"}`}, {Kind: "ai-conversation", Value: "not-json"}, {Kind: "plugin", Value: `{"pluginId":"../x"}`}, {Kind: "plugin", Value: `{"pluginId":"p","viewId":"a b"}`}, {Kind: "plugin"}, {Kind: "app", Value: "x"}, {Kind: "agent-runs", Value: "x"}, {Kind: "agent-run"}, {Kind: "agent-run", Value: "run-../../etc"}, {Kind: "terminal-pane"}, {Kind: "terminal-pane", Value: "../x"}, {Kind: "terminal-pane", Value: "a b"}} {
		if err := ValidateTarget(target); err == nil {
			t.Fatalf("accepted %+v", target)
		}
	}
}

func TestNavigateHandlerValidatesAndDispatchesTarget(t *testing.T) {
	previous := currentAdapter()
	t.Cleanup(func() { SetAdapter(previous) })

	var received NavigationTarget
	SetAdapter(Adapter{FocusTarget: func(target NavigationTarget) { received = target }})
	result, err := navigateHandler("", `{"kind":"view","value":"database"}`)
	if err != nil || result == "" || received.Kind != "view" || received.Value != "database" {
		t.Fatalf("result=%q target=%+v err=%v", result, received, err)
	}
	if _, err := navigateHandler("", `{"kind":"view","value":"shell"}`); err == nil {
		t.Fatal("accepted invalid navigation target")
	}
}

func TestParseDeepLinkPluginLinks(t *testing.T) {
	target, err := ParseDeepLink(" mincode://Jira-Tools/issues/ABC%2F1?focus=comments#frag ")
	if err != nil || target.Kind != "plugin-uri" || target.Value != "mincode://jira-tools/issues/ABC%2F1?focus=comments" {
		t.Fatalf("target = %+v, %v", target, err)
	}
	if err := ValidateTarget(target); err != nil {
		t.Fatalf("validate = %v", err)
	}
	if _, err := ParseDeepLink("mincode://jira-tools/search?q=a+b%20c"); err != nil {
		t.Fatalf("encoded spaces are allowed: %v", err)
	}
	if target, err := ParseDeepLink("mincode://Settings"); err != nil || target.Kind != "view" {
		t.Fatalf("built-in host must keep precedence: %+v, %v", target, err)
	}
	for _, raw := range []string{
		"mincode://plugin/x",
		"mincode://run/x",
		"mincode://user@jira-tools/x",
		"mincode://jira-tools:8080/x",
		"mincode://jira-tools",
		"mincode://jira-tools/",
		"mincode://bad_host!/x",
		"mincode://jira-tools/" + strings.Repeat("a", 2100),
		"mincode://jira-tools/a%0Ab",
		"mincode://jira-tools/a?x=%E2%80%AE",
	} {
		if target, err := ParseDeepLink(raw); err == nil {
			t.Errorf("accepted %q as %+v", raw, target)
		}
	}
	for _, target := range []NavigationTarget{
		{Kind: "plugin-uri", Value: "https://jira-tools/x"},
		{Kind: "plugin-uri", Value: "mincode://Jira-Tools/x"},
		{Kind: "plugin-uri", Value: "mincode://settings"},
		{Kind: "plugin-uri"},
	} {
		if err := ValidateTarget(target); err == nil {
			t.Errorf("accepted %+v", target)
		}
	}
	_, targets := LaunchPayload([]string{"mincode", "mincode://jira-tools/issues/1"}, t.TempDir())
	if len(targets) != 1 || targets[0].Kind != "plugin-uri" {
		t.Fatalf("launch targets = %+v", targets)
	}
}

func TestPendingNavigationQueue(t *testing.T) {
	reset := func() {
		pendingNav.mu.Lock()
		pendingNav.ready, pendingNav.items, pendingNav.now = false, nil, nil
		pendingNav.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
	now := time.Unix(1_000_000, 0)
	pendingNav.now = func() time.Time { return now }

	// Every kind waits for the frontend: a mincode://settings link that
	// launched the app must not be emitted before App.vue listens.
	if _, queued := DeferUntilFrontendReady(NavigationTarget{Kind: "view", Value: "settings"}); !queued {
		t.Fatal("view targets must queue before the frontend is ready")
	}
	for index := 0; index < pendingNavigationMax+2; index++ {
		if _, queued := DeferUntilFrontendReady(NavigationTarget{Kind: "plugin-uri", Value: "mincode://p.q/" + string(rune('a'+index))}); !queued {
			t.Fatal("plugin link must queue before the frontend is ready")
		}
	}
	now = now.Add(pendingNavigationTTL - time.Second)
	_, _ = DeferUntilFrontendReady(NavigationTarget{Kind: "plugin-uri", Value: "mincode://p.q/late"})
	now = now.Add(2 * time.Second)
	targets := TakePendingNavigation()
	if len(targets) != 1 || targets[0].Value != "mincode://p.q/late" || targets[0].Seq == 0 {
		t.Fatalf("targets = %+v", targets)
	}
	if _, queued := DeferUntilFrontendReady(NavigationTarget{Kind: "plugin-uri", Value: "mincode://p.q/x"}); queued {
		t.Fatal("after the first drain links are emitted live")
	}
	if len(TakePendingNavigation()) != 0 {
		t.Fatal("queue must be empty after draining")
	}
	result, err := takePendingNavigationHandler("", "{}")
	if err != nil || !strings.Contains(result, "[]") {
		t.Fatalf("handler = %q, %v", result, err)
	}
}

func TestPendingNavigationEvictsOldest(t *testing.T) {
	pendingNav.mu.Lock()
	pendingNav.ready, pendingNav.items, pendingNav.now = false, nil, nil
	pendingNav.mu.Unlock()
	t.Cleanup(func() {
		pendingNav.mu.Lock()
		pendingNav.ready, pendingNav.items = false, nil
		pendingNav.mu.Unlock()
	})
	for index := 0; index < pendingNavigationMax+3; index++ {
		DeferUntilFrontendReady(NavigationTarget{Kind: "plugin-uri", Value: "mincode://p.q/" + string(rune('a'+index))})
	}
	targets := TakePendingNavigation()
	if len(targets) != pendingNavigationMax || targets[0].Value != "mincode://p.q/d" {
		t.Fatalf("targets = %+v", targets)
	}
}

// A plugin page that holds the keyboard turns MinCode's own menu shortcuts off
// and back on; without a native menu the call is a no-op rather than an error.
func TestSetMenuAcceleratorsHandler(t *testing.T) {
	previous := currentAdapter()
	t.Cleanup(func() { SetAdapter(previous) })

	SetAdapter(Adapter{})
	if _, err := setMenuAcceleratorsHandler("", `{"enabled":false}`); err != nil {
		t.Fatalf("missing native menu reported an error: %v", err)
	}
	var calls []bool
	SetAdapter(Adapter{SetMenuAccelerators: func(enabled bool) { calls = append(calls, enabled) }})
	for _, args := range []string{`{"enabled":false}`, `{"enabled":true}`} {
		if _, err := setMenuAcceleratorsHandler("", args); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 2 || calls[0] || !calls[1] {
		t.Fatalf("calls = %v", calls)
	}
	if _, err := setMenuAcceleratorsHandler("", `{}`); err == nil {
		t.Fatal("missing enabled flag accepted")
	}
}

func TestValidatePluginCommandTarget(t *testing.T) {
	valid := PluginCommandTarget("acme.capture", "proxy.toggle")
	if valid.Kind != "plugin-command" {
		t.Fatalf("kind %q", valid.Kind)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(valid.Value), &decoded); err != nil ||
		!reflect.DeepEqual(decoded, map[string]string{"pluginId": "acme.capture", "commandId": "proxy.toggle"}) {
		t.Fatalf("value %q (%v)", valid.Value, err)
	}
	if err := ValidateTarget(valid); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
	if err := ValidateTarget(NavigationTarget{Kind: "plugin-command", Value: `{"pluginId":"p","commandId":"c_1-x.y"}`}); err != nil {
		t.Fatalf("valid literal rejected: %v", err)
	}
	// Manifest command IDs may contain ':' (proxy:toggle); plugin IDs may not.
	if err := ValidateTarget(PluginCommandTarget("acme.capture", "proxy:toggle")); err != nil {
		t.Fatalf("proxy:toggle rejected: %v", err)
	}
	if err := ValidateTarget(NavigationTarget{Kind: "plugin-command", Value: `{"pluginId":"acme:x","commandId":"c"}`}); err == nil {
		t.Fatal("plugin ID with ':' accepted")
	}
	for _, value := range []string{
		`{"pluginId":"p","commandId":"c","title":"Quit"}`,
		`{"pluginId":"p","commandId":""}`,
		`{"pluginId":"p"}`,
		`{"commandId":"c"}`,
		`{"pluginId":"p","commandId":"a/b"}`,
		`{"pluginId":"../x","commandId":"c"}`,
		`{"pluginId":"p","commandId":"c"} {}`,
		`{"pluginId":"p","commandId":"c"} x`,
		`not-json`,
		``,
		`{"pluginId":"p","commandId":".c"}`,
	} {
		if err := ValidateTarget(NavigationTarget{Kind: "plugin-command", Value: value}); err == nil {
			t.Fatalf("accepted plugin-command value %q", value)
		}
	}
}

func TestParseDeepLinkNeverYieldsPluginCommand(t *testing.T) {
	for _, raw := range []string{
		"mincode://plugin-command/x",
		"mincode://p.id/x?commandId=y",
		"mincode://plugin-command?pluginId=p&commandId=c",
		`mincode://workspace?view=plugin-command`,
		"mincode://acme.capture/plugin-command?kind=plugin-command",
		"mincode:plugin-command",
	} {
		target, err := ParseDeepLink(raw)
		if err == nil && target.Kind == "plugin-command" {
			t.Fatalf("%s produced a plugin-command target", raw)
		}
	}
}

// desktop_navigate (the WebView) never produces a plugin-command target; the
// native tray closure goes through Navigate directly.
func TestNavigateHandlerRejectsPluginCommand(t *testing.T) {
	var focused []NavigationTarget
	previous := currentAdapter()
	SetAdapter(Adapter{FocusTarget: func(target NavigationTarget) { focused = append(focused, target) }})
	t.Cleanup(func() { SetAdapter(previous) })
	target := PluginCommandTarget("acme.capture", "proxy:toggle")
	args, _ := json.Marshal(target)
	if _, err := navigateHandler("", string(args)); err == nil {
		t.Fatal("desktop_navigate accepted a plugin-command target")
	}
	if len(focused) != 0 {
		t.Fatalf("focused %+v", focused)
	}
	if err := Navigate(target); err != nil {
		t.Fatalf("Navigate rejected the tray target: %v", err)
	}
	if _, err := navigateHandler("", `{"kind":"view","value":"settings"}`); err != nil {
		t.Fatalf("view target rejected: %v", err)
	}
	if len(focused) != 2 || focused[0] != target {
		t.Fatalf("focused %+v", focused)
	}
}
