package desktop

import (
	"encoding/json"
	"errors"
	"regexp"
)

// Plugin tray items (contributes.menus "tray"): the plugin host resolves the
// frontend's {pluginId, command} references against the manifests and hands
// the result here; the native adapter (app/tray_menu.go) rebuilds the
// system tray menu below MinCode's own items. Every title comes from a
// manifest, never from the WebView.

var errPluginTrayUnavailable = errors.New("plugin tray is not available")

// PluginTrayItem is one row of a plugin's tray submenu; Separator marks the
// line between two command groups.
type PluginTrayItem struct {
	CommandID string
	Title     string
	Separator bool
}

// PluginTrayGroup is one plugin's submenu, named after the plugin.
type PluginTrayGroup struct {
	PluginID string
	Name     string
	Items    []PluginTrayItem
}

// SetPluginTray replaces the plugin section of the tray menu (nil or empty
// removes it). Safe from any goroutine except the UI main thread.
func SetPluginTray(groups []PluginTrayGroup) error {
	fn := currentAdapter().SetPluginTray
	if fn == nil {
		return errPluginTrayUnavailable
	}
	fn(groups)
	return nil
}

// PluginCommandTarget is the navigation target of a tray click: the main
// window comes to the front and the frontend runs the command through its
// shared command path. Deep links never produce this kind.
func PluginCommandTarget(pluginID, commandID string) NavigationTarget {
	value, _ := json.Marshal(pluginCommandTargetValue{PluginID: pluginID, CommandID: commandID})
	return NavigationTarget{Kind: pluginCommandTargetKind, Value: string(value)}
}

type pluginCommandTargetValue struct {
	PluginID  string `json:"pluginId"`
	CommandID string `json:"commandId"`
}

const pluginCommandTargetKind = "plugin-command"

// pluginCommandIDPattern equals contributionIDPattern of
// backend/plugin/manifest.go: a manifest command ID may contain ':'.
var pluginCommandIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// validPluginCommandTarget accepts exactly {"pluginId","commandId"}: the
// plugin ID in the plugin target shape, the command ID in the manifest
// contribution ID shape.
func validPluginCommandTarget(raw string) bool {
	var value pluginCommandTargetValue
	if err := decodeExactJSON(raw, &value); err != nil {
		return false
	}
	return pluginTargetIDPattern.MatchString(value.PluginID) && pluginCommandIDPattern.MatchString(value.CommandID)
}
