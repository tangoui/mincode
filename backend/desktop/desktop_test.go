package desktop

import (
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
	for _, target := range []NavigationTarget{{Kind: "command", Value: "rm"}, {Kind: "view", Value: "shell"}, {Kind: "ai", Value: "payload"}, {Kind: "ai-conversation"}, {Kind: "ai-conversation", Value: `{"workspaceScopeId":"workspace:one"}`}, {Kind: "ai-conversation", Value: "not-json"}, {Kind: "app", Value: "x"}, {Kind: "agent-runs", Value: "x"}, {Kind: "agent-run"}, {Kind: "agent-run", Value: "run-../../etc"}, {Kind: "terminal-pane"}, {Kind: "terminal-pane", Value: "../x"}, {Kind: "terminal-pane", Value: "a b"}} {
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
		if _, queued := DeferUntilFrontendReady(NavigationTarget{Kind: "ai-conversation", Value: "conversation-" + string(rune('a'+index))}); !queued {
			t.Fatal("conversation must queue before the frontend is ready")
		}
	}
	now = now.Add(pendingNavigationTTL - time.Second)
	_, _ = DeferUntilFrontendReady(NavigationTarget{Kind: "ai-conversation", Value: "conversation-late"})
	now = now.Add(2 * time.Second)
	targets := TakePendingNavigation()
	if len(targets) != 1 || targets[0].Value != "conversation-late" || targets[0].Seq == 0 {
		t.Fatalf("targets = %+v", targets)
	}
	if _, queued := DeferUntilFrontendReady(NavigationTarget{Kind: "ai-conversation", Value: "conversation-x"}); queued {
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
		DeferUntilFrontendReady(NavigationTarget{Kind: "ai-conversation", Value: "conversation-" + string(rune('a'+index))})
	}
	targets := TakePendingNavigation()
	if len(targets) != pendingNavigationMax || targets[0].Value != "conversation-d" {
		t.Fatalf("targets = %+v", targets)
	}
}
