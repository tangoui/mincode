package desktop

import (
	"errors"
	"sync"
)

// Plugin global shortcuts: the plugin host's registry registers the combos
// the user turned on through these wrappers around the installed Adapter.
// Without an adapter (tests, before the window exists) nothing registers.

var errPluginShortcutsUnavailable = errors.New("全局快捷键暂不可用")

// PluginShortcutsAvailable reports whether the native adapter can register
// plugin shortcuts.
func PluginShortcutsAvailable() bool {
	current := currentAdapter()
	return current.RegisterPluginShortcut != nil && current.UnregisterPluginShortcut != nil
}

// RegisterPluginShortcut binds accelerator system-wide; fn runs on its own
// goroutine on every press.
func RegisterPluginShortcut(accelerator string, fn func()) error {
	if fn == nil {
		return errors.New("plugin shortcut callback is required")
	}
	register := currentAdapter().RegisterPluginShortcut
	if register == nil {
		return errPluginShortcutsUnavailable
	}
	return register(accelerator, fn)
}

// UnregisterPluginShortcut releases accelerator; releasing a combo that is
// not registered is not an error.
func UnregisterPluginShortcut(accelerator string) error {
	unregister := currentAdapter().UnregisterPluginShortcut
	if unregister == nil {
		return nil
	}
	return unregister(accelerator)
}

// HostShortcut is MinCode's own registered global shortcut, "" when off.
func HostShortcut() string {
	if fn := currentAdapter().HostShortcut; fn != nil {
		return fn()
	}
	return ""
}

// IsWayland reports a Wayland session (the adapter's answer, else the
// environment).
func IsWayland() bool {
	if fn := currentAdapter().IsWayland; fn != nil {
		return fn()
	}
	return DetectWayland()
}

var hostShortcutListeners struct {
	mu  sync.RWMutex
	fns []func(pending string)
}

// OnHostShortcutChange registers fn to run synchronously around every change
// of MinCode's own global shortcut: first with the accelerator about to be
// registered (pending; "" when turning it off), then with "" once the
// adapter holds the result, and with "" whenever an adapter is installed.
// Call from init().
func OnHostShortcutChange(fn func(pending string)) {
	if fn == nil {
		panic("nil host shortcut listener")
	}
	hostShortcutListeners.mu.Lock()
	hostShortcutListeners.fns = append(hostShortcutListeners.fns, fn)
	hostShortcutListeners.mu.Unlock()
}

func notifyHostShortcutChange(pending string) {
	hostShortcutListeners.mu.RLock()
	fns := append([]func(string){}, hostShortcutListeners.fns...)
	hostShortcutListeners.mu.RUnlock()
	for _, fn := range fns {
		fn(pending)
	}
}

// resetHostShortcutListeners is for tests.
func resetHostShortcutListeners() {
	hostShortcutListeners.mu.Lock()
	hostShortcutListeners.fns = nil
	hostShortcutListeners.mu.Unlock()
}
