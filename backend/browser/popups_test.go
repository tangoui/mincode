package browser

import (
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
)

// recordEvents replaces the event emitter for the test and returns a reader.
func recordEvents(t *testing.T) func() []recordedEvent {
	t.Helper()
	var mu sync.Mutex
	var emitted []recordedEvent
	core.SetEmitter(func(name string, data any) {
		mu.Lock()
		defer mu.Unlock()
		emitted = append(emitted, recordedEvent{name, data})
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	return func() []recordedEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedEvent{}, emitted...)
	}
}

func eventsNamed(events []recordedEvent, name string) []recordedEvent {
	var out []recordedEvent
	for _, event := range events {
		if event.Name == name {
			out = append(out, event)
		}
	}
	return out
}

func TestAdmitPopupRejectsDisallowedSchemes(t *testing.T) {
	events := recordEvents(t)
	t.Cleanup(resetAIPopupWindows)
	allowAIPopupWindow("opener-tab")
	for _, raw := range []string{"javascript:alert(1)", "file:///etc/passwd", "foo:bar", "data:text/html,hi"} {
		if id, err := admitPopup("opener-tab", raw); err == nil {
			t.Fatalf("admitPopup(%q) should fail, got tab %q", raw, id)
		}
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("rejected popups must not emit events, got %+v", got)
	}
	if !aiPopupAllowed("opener-tab") {
		t.Fatal("a refused address must not use up the allowance")
	}

	for _, raw := range []string{"https://example.test/", "about:blank", ""} {
		allowAIPopupWindow("opener-tab")
		id, err := admitPopup("opener-tab", raw)
		if err != nil {
			t.Fatalf("admitPopup(%q) should succeed, got %v", raw, err)
		}
		hex := strings.TrimPrefix(id, popupTabIDPrefix)
		if !strings.HasPrefix(id, popupTabIDPrefix) || len(hex) != 16 || strings.Trim(hex, "0123456789abcdef") != "" {
			t.Fatalf("popup tab id should be %s<16 hex>, got %q", popupTabIDPrefix, id)
		}
	}
	opened := eventsNamed(events(), popupOpenedEvent)
	if len(opened) != 3 {
		t.Fatalf("expected 3 popup-opened events, got %+v", events())
	}
	first, ok := opened[0].Data.(popupOpened)
	if !ok || first.OpenerTabID != "opener-tab" || first.URL != "https://example.test/" || !strings.HasPrefix(first.TabID, popupTabIDPrefix) {
		t.Fatalf("unexpected popup-opened payload %+v", opened[0].Data)
	}
	if blank := opened[2].Data.(popupOpened); blank.URL != "about:blank" {
		t.Fatalf("an empty popup URL should be reported as about:blank, got %q", blank.URL)
	}
}

func TestAIPopupWindowExpires(t *testing.T) {
	current := time.Unix(1000, 0)
	original := now
	now = func() time.Time { return current }
	t.Cleanup(func() { now = original; resetAIPopupWindows() })

	if aiPopupAllowed("tab-a") {
		t.Fatal("no allowance before an AI click")
	}
	allowAIPopupWindow("tab-a")
	if !aiPopupAllowed("tab-a") {
		t.Fatal("allowance should be open right after the AI click")
	}
	if aiPopupAllowed("tab-b") {
		t.Fatal("allowance is per tab")
	}
	current = current.Add(999 * time.Millisecond)
	if !aiPopupAllowed("tab-a") {
		t.Fatal("allowance should still be open at 999ms")
	}
	current = current.Add(time.Millisecond)
	if aiPopupAllowed("tab-a") {
		t.Fatal("allowance must expire after 1 second")
	}
}

func TestPopupClosedCancelsDialogsAndEmitsTabClosed(t *testing.T) {
	events := recordEvents(t)
	resetPendingDialogs()
	t.Cleanup(resetPendingDialogs)
	openDialog(pendingDialog{ID: "d1", TabID: "browser:popup:0123456789abcdef", Kind: dialogKindAlert})
	popupClosed("browser:popup:0123456789abcdef")
	if _, ok := dialogForTab("browser:popup:0123456789abcdef"); ok {
		t.Fatal("closing a popup must cancel its dialog")
	}
	closed := eventsNamed(events(), tabClosedEvent)
	if len(closed) != 1 {
		t.Fatalf("expected one tab-closed event, got %+v", events())
	}
	if payload := closed[0].Data.(tabClosed); payload.TabID != "browser:popup:0123456789abcdef" || payload.Reason != "window.close" {
		t.Fatalf("unexpected tab-closed payload %+v", payload)
	}
}

func withTestClock(t *testing.T) *time.Time {
	t.Helper()
	current := time.Unix(1000, 0)
	original := now
	now = func() time.Time { return current }
	t.Cleanup(func() { now = original; resetAIPopupWindows() })
	return &current
}

func TestAdmitPopupNeedsAnAllowance(t *testing.T) {
	events := recordEvents(t)
	withTestClock(t)
	if id, err := admitPopup("tab-a", "https://example.test/"); err == nil {
		t.Fatalf("a popup without an AI click or real user input must be refused, got %q", id)
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("a refused popup must not emit events, got %+v", got)
	}
}

func TestPopupAllowanceAdmitsOnePopup(t *testing.T) {
	recordEvents(t)
	withTestClock(t)
	allowAIPopupWindow("tab-a")
	if _, err := admitPopup("tab-a", "https://example.test/one"); err != nil {
		t.Fatalf("first popup after an AI click should open: %v", err)
	}
	if _, err := admitPopup("tab-a", "https://example.test/two"); err == nil {
		t.Fatal("one AI click must open at most one popup")
	}
	noteUserInput("tab-a")
	if _, err := admitPopup("tab-a", "https://example.test/three"); err != nil {
		t.Fatalf("real user input should allow one popup: %v", err)
	}
	if _, err := admitPopup("tab-a", "https://example.test/four"); err == nil {
		t.Fatal("one user action must open at most one popup")
	}
}

func TestUserInputAllowanceExpires(t *testing.T) {
	recordEvents(t)
	current := withTestClock(t)
	noteUserInput("tab-a")
	*current = current.Add(time.Second)
	if _, err := admitPopup("tab-a", "https://example.test/"); err == nil {
		t.Fatal("user input allowance must expire after 1 second")
	}
	noteUserInput("tab-a")
	*current = current.Add(999 * time.Millisecond)
	if _, err := admitPopup("tab-b", "https://example.test/"); err == nil {
		t.Fatal("user input on one tab must not let another tab open windows")
	}
	if _, err := admitPopup("tab-a", "https://example.test/"); err != nil {
		t.Fatalf("user input should still allow a popup at 999ms: %v", err)
	}
}

func TestAdmitPopupStripsUserinfo(t *testing.T) {
	events := recordEvents(t)
	withTestClock(t)
	allowAIPopupWindow("tab-a")
	if _, err := admitPopup("tab-a", "https://user:secret@example.test/login?x=1"); err != nil {
		t.Fatalf("admit: %v", err)
	}
	opened := eventsNamed(events(), popupOpenedEvent)
	if len(opened) != 1 {
		t.Fatalf("expected one popup-opened event, got %+v", events())
	}
	if got := opened[0].Data.(popupOpened).URL; got != "https://example.test/login?x=1" {
		t.Fatalf("popup URL must not carry credentials, got %q", got)
	}
}

func TestComputerActionsThatOpenThePopupAllowance(t *testing.T) {
	x := 1.0
	cases := []struct {
		action browserComputerAction
		want   bool
	}{
		{browserComputerAction{Type: "click", X: &x, Y: &x}, true},
		{browserComputerAction{Type: "click", X: &x, Y: &x, Button: "left"}, true},
		{browserComputerAction{Type: "double_click", X: &x, Y: &x}, true},
		{browserComputerAction{Type: "click", X: &x, Y: &x, Button: "right"}, false},
		{browserComputerAction{Type: "keypress", Keys: []string{"Enter"}}, true},
		{browserComputerAction{Type: "keypress", Keys: []string{"a", "RETURN"}}, true},
		{browserComputerAction{Type: "keypress", Keys: []string{"Tab"}}, false},
		{browserComputerAction{Type: "move", X: &x, Y: &x}, false},
		{browserComputerAction{Type: "scroll", ScrollY: 10}, false},
		{browserComputerAction{Type: "type", Text: "hi"}, false},
		{browserComputerAction{Type: "drag"}, false},
	}
	for _, c := range cases {
		if got := computerActionMayOpenPopup(c.action); got != c.want {
			t.Errorf("computerActionMayOpenPopup(%+v) = %v, want %v", c.action, got, c.want)
		}
	}
}
