package desktop

import (
	"errors"
	"reflect"
	"runtime"
	"testing"
)

func TestPluginShortcutAdapter(t *testing.T) {
	t.Cleanup(func() { SetAdapter(Adapter{}) })
	SetAdapter(Adapter{})
	if PluginShortcutsAvailable() {
		t.Fatal("available without an adapter")
	}
	if err := RegisterPluginShortcut("CmdOrCtrl+Shift+A", func() {}); err == nil {
		t.Fatal("registered without an adapter")
	}
	if err := UnregisterPluginShortcut("CmdOrCtrl+Shift+A"); err != nil {
		t.Fatalf("unregister without an adapter: %v", err)
	}
	if HostShortcut() != "" {
		t.Fatal("host shortcut without an adapter")
	}

	registered := map[string]func(){}
	SetAdapter(Adapter{
		RegisterPluginShortcut: func(accelerator string, fn func()) error {
			if accelerator == "busy" {
				return errors.New("taken")
			}
			registered[accelerator] = fn
			return nil
		},
		UnregisterPluginShortcut: func(accelerator string) error { delete(registered, accelerator); return nil },
		HostShortcut:             func() string { return "CmdOrCtrl+Shift+Space" },
		IsWayland:                func() bool { return true },
	})
	if !PluginShortcutsAvailable() || HostShortcut() != "CmdOrCtrl+Shift+Space" || !IsWayland() {
		t.Fatal("adapter not used")
	}
	if err := RegisterPluginShortcut("CmdOrCtrl+Shift+A", func() {}); err != nil || registered["CmdOrCtrl+Shift+A"] == nil {
		t.Fatalf("register: %v", err)
	}
	if err := RegisterPluginShortcut("busy", func() {}); err == nil {
		t.Fatal("register error swallowed")
	}
	if err := RegisterPluginShortcut("CmdOrCtrl+Shift+B", nil); err == nil {
		t.Fatal("nil callback accepted")
	}
	if err := UnregisterPluginShortcut("CmdOrCtrl+Shift+A"); err != nil || len(registered) != 0 {
		t.Fatalf("unregister: %v", err)
	}
}

func TestHostShortcutChangeNotifiesBeforeAndAfter(t *testing.T) {
	t.Cleanup(func() { SetAdapter(Adapter{}); resetHostShortcutListeners() })
	var events []string
	OnHostShortcutChange(func(pending string) { events = append(events, "listener:"+pending) })
	SetAdapter(Adapter{SetGlobalShortcut: func(enabled bool, accelerator string) error {
		events = append(events, "set:"+accelerator)
		return nil
	}})
	events = nil
	if _, err := setGlobalShortcutHandler("", `{"enabled":true,"accelerator":"Cmd+Shift+K"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := setGlobalShortcutHandler("", `{"enabled":false,"accelerator":"Cmd+Shift+K"}`); err != nil {
		t.Fatal(err)
	}
	want := []string{"listener:Cmd+Shift+K", "set:Cmd+Shift+K", "listener:", "listener:", "set:Cmd+Shift+K", "listener:"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events %v", events)
	}
	// A failed host registration still settles the listeners.
	events = nil
	SetAdapter(Adapter{SetGlobalShortcut: func(bool, string) error { return errors.New("taken") }})
	events = nil
	if _, err := setGlobalShortcutHandler("", `{"enabled":true,"accelerator":"Cmd+Shift+K"}`); err == nil {
		t.Fatal("error swallowed")
	}
	if !reflect.DeepEqual(events, []string{"listener:Cmd+Shift+K", "listener:"}) {
		t.Fatalf("events %v", events)
	}
	// Installing an adapter tells listeners to re-read it.
	events = nil
	SetAdapter(Adapter{})
	if !reflect.DeepEqual(events, []string{"listener:"}) {
		t.Fatalf("events %v", events)
	}
}

func TestSetGlobalShortcutErrorCodes(t *testing.T) {
	t.Cleanup(func() { SetAdapter(Adapter{}) })
	SetAdapter(Adapter{})
	if _, err := setGlobalShortcutHandler("", `{"enabled":true}`); err == nil || err.Error() != "global-shortcut-unavailable" {
		t.Fatalf("no adapter: %v", err)
	}
	occupied := &GlobalShortcutOccupiedError{Accelerator: "CmdOrCtrl+Shift+Space"}
	SetAdapter(Adapter{SetGlobalShortcut: func(bool, string) error { return occupied }})
	_, err := setGlobalShortcutHandler("", `{"enabled":true}`)
	var got *GlobalShortcutOccupiedError
	if !errors.As(err, &got) || err.Error() != "global-shortcut-occupied: CmdOrCtrl+Shift+Space" {
		t.Fatalf("occupied: %v", err)
	}
}

func TestDetectWayland(t *testing.T) {
	for _, tc := range []struct {
		session, display string
		want             bool
	}{
		{"wayland", "", true},
		{"x11", "wayland-0", false},
		{"", "wayland-0", true},
		{"", "", false},
		{"WAYLAND", "", true},
	} {
		t.Setenv("XDG_SESSION_TYPE", tc.session)
		t.Setenv("WAYLAND_DISPLAY", tc.display)
		want := tc.want && runtime.GOOS == "linux"
		if got := DetectWayland(); got != want {
			t.Fatalf("session %q display %q: %v", tc.session, tc.display, got)
		}
	}
}
