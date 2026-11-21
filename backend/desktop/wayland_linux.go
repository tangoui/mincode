package desktop

import (
	"os"
	"strings"
)

// DetectWayland reports whether MinCode runs in a Wayland session, with the
// test the desktop host uses to pick its XDG GlobalShortcuts portal backend:
// XDG_SESSION_TYPE when set, otherwise WAYLAND_DISPLAY.
func DetectWayland() bool {
	switch strings.ToLower(os.Getenv("XDG_SESSION_TYPE")) {
	case "wayland":
		return true
	case "x11":
		return false
	}
	return os.Getenv("WAYLAND_DISPLAY") != ""
}
