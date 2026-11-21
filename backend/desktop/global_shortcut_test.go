package desktop

import (
	"errors"
	"testing"
)

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
