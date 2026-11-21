package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// Notifications and clipboard access for manifest API v2 plugins.
const (
	pluginNotifyEvent        = "plugin:notify"
	notifyMaxTitleRunes      = 80
	notifyMaxBodyRunes       = 500
	notifyPerMinute          = 10
	clipboardMaxTextBytes    = 256 << 10
	clipboardMaxWriteBytes   = 1 << 20
	clipboardStateCommand    = "clipboard_state"
	clipboardReadCommand     = "clipboard_read_text"
	clipboardWriteCommand    = "clipboard_write_text"
	notificationWindowLength = time.Minute
	// Notification action buttons (host 0.3.0); mirrored by the frontend, not
	// part of the contribution contract.
	notifyActionsMax          = 2
	notifyActionTitleMaxRunes = 24
)

var (
	notifyViewIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	notifyActionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
)

// pluginNotificationAction is a button on a plugin notification. Command must
// be one of the posting plugin's own contributes.commands; v1 actions carry no
// arguments. ID only keys the button and is never sent back to the plugin.
type pluginNotificationAction struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Command string `json:"command"`
}

// pluginNotification is what the frontend receives on plugin:notify. The
// plugin name is taken from the manifest, never from the request, so a
// notification cannot pretend to come from another plugin.
type pluginNotification struct {
	PluginID   string `json:"pluginId"`
	PluginName string `json:"pluginName"`
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	Level      string `json:"level"`
	// System asks for an operating-system notification even while MinCode is
	// in the foreground; otherwise one is shown only when it is in the background.
	System bool   `json:"system,omitempty"`
	ViewID string `json:"viewId,omitempty"`
	// Actions come only from host.notify; host-authored notices (Code) never
	// carry any.
	Actions []pluginNotificationAction `json:"actions,omitempty"`
	// Code marks a host-authored notice (global shortcut rejections,
	// "globalShortcut.<reason>") the frontend may localize; plugins cannot set it.
	Code string `json:"code,omitempty"`
}

type notifyLimiter struct {
	mu    sync.Mutex
	sent  map[string][]time.Time
	now   func() time.Time
	emit  func(name string, data any)
	limit int
}

var pluginNotifier = &notifyLimiter{sent: map[string][]time.Time{}, now: time.Now, emit: core.Emit, limit: notifyPerMinute}

func init() {
	registerHostMethod("host.notify", nil, api.CapabilityNotifications, hostNotify)
	registerHostMethod("host.clipboard.read", nil, api.CapabilityClipboard, hostClipboardRead)
	registerHostMethod("host.clipboard.write", nil, api.CapabilityClipboard, hostClipboardWrite)
}

func cleanNotificationText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, ""))
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > limit {
		value = string([]rune(value)[:limit-1]) + "…"
	}
	return value
}

// validateNotificationActions checks the optional host.notify actions array:
// absent, null and [] mean none; otherwise at most notifyActionsMax strictly
// decoded {id, title, command} objects with unique IDs, single-line titles of
// at most notifyActionTitleMaxRunes characters (too long is refused, not
// truncated, so a button never changes meaning) and commands from commands.
func validateNotificationActions(raw json.RawMessage, commands map[string]struct{}) ([]pluginNotificationAction, *rpcError) {
	invalid := func(format string, args ...any) *rpcError {
		return &rpcError{Code: -32602, Message: fmt.Sprintf(format, args...)}
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, invalid("notification actions must be an array")
	}
	if len(items) > notifyActionsMax {
		return nil, invalid("at most %d notification actions", notifyActionsMax)
	}
	if len(items) == 0 {
		return nil, nil
	}
	actions := make([]pluginNotificationAction, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		number := index + 1
		var action pluginNotificationAction
		if string(item) == "null" || decodeStrictJSONValue(item, &action, true, "notification action") != nil {
			return nil, invalid("notification action %d must be an object with only id, title and command", number)
		}
		if !notifyActionIDPattern.MatchString(action.ID) {
			return nil, invalid("notification action %d id must match %s", number, notifyActionIDPattern)
		}
		if _, duplicate := seen[action.ID]; duplicate {
			return nil, invalid("notification action %d id %q is not unique", number, action.ID)
		}
		seen[action.ID] = struct{}{}
		// Refused runes (Cc incl. newline and tab, Cf, U+2028/2029) are rejected
		// before cleanNotificationText could strip or keep them; the length is
		// checked on the untruncated text.
		title := action.Title
		if strings.ContainsFunc(title, contextRuneRefused) {
			title = ""
		}
		title = cleanNotificationText(title, len(title)+1)
		if title == "" || utf8.RuneCountInString(title) > notifyActionTitleMaxRunes {
			return nil, invalid("notification action %d title must be a single line of 1–%d characters", number, notifyActionTitleMaxRunes)
		}
		if _, ok := commands[action.Command]; !ok {
			return nil, invalid("notification action %d command %q is not one of this plugin's commands", number, action.Command)
		}
		actions = append(actions, pluginNotificationAction{ID: action.ID, Title: title, Command: action.Command})
	}
	return actions, nil
}

// post validates a host.notify request, applies the per-plugin rate limit and
// emits plugin:notify. commands are the plugin's own command IDs, the only
// targets its notification actions may name.
func (l *notifyLimiter) post(pluginID, pluginName string, commands map[string]struct{}, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Title   string          `json:"title"`
		Body    string          `json:"body"`
		Level   string          `json:"level"`
		System  bool            `json:"system"`
		ViewID  string          `json:"viewId"`
		Actions json.RawMessage `json:"actions"`
	}
	if json.Unmarshal(params, &request) != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid notification params"}
	}
	title := cleanNotificationText(request.Title, notifyMaxTitleRunes)
	if title == "" {
		return nil, &rpcError{Code: -32602, Message: "notification title is required"}
	}
	level := request.Level
	switch level {
	case "":
		level = "info"
	case "info", "success", "warning", "error":
	default:
		return nil, &rpcError{Code: -32602, Message: "level must be info, success, warning or error"}
	}
	viewID := strings.TrimSpace(request.ViewID)
	if viewID != "" && !notifyViewIDPattern.MatchString(viewID) {
		return nil, &rpcError{Code: -32602, Message: "invalid viewId"}
	}
	actions, invalidActions := validateNotificationActions(request.Actions, commands)
	if invalidActions != nil {
		return nil, invalidActions
	}
	now := l.now()
	l.mu.Lock()
	recent := l.sent[pluginID][:0]
	for _, at := range l.sent[pluginID] {
		if now.Sub(at) < notificationWindowLength {
			recent = append(recent, at)
		}
	}
	if len(recent) >= l.limit {
		l.sent[pluginID] = recent
		l.mu.Unlock()
		return nil, &rpcError{Code: -32005, Message: "too many notifications; at most 10 per minute"}
	}
	l.sent[pluginID] = append(recent, now)
	l.mu.Unlock()
	l.emit(pluginNotifyEvent, pluginNotification{
		PluginID: pluginID, PluginName: cleanNotificationText(pluginName, notifyMaxTitleRunes), Title: title,
		Body: cleanNotificationText(request.Body, notifyMaxBodyRunes), Level: level, System: request.System, ViewID: viewID,
		Actions: actions,
	})
	return map[string]any{"delivered": true, "actions": len(actions)}, nil
}

func hostNotify(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	commands := make(map[string]struct{}, len(runtimeHost.manifest.Contributes.Commands))
	for _, command := range runtimeHost.manifest.Contributes.Commands {
		commands[command.ID] = struct{}{}
	}
	return pluginNotifier.post(runtimeHost.pluginID, runtimeHost.manifest.Name, commands, params)
}

// clipboardDispatch is core.Dispatch; a variable so tests need no native clipboard.
var clipboardDispatch = core.Dispatch

// hostClipboardRead returns the clipboard text when it changed since the
// caller's last seen sequence. Concealed content (password managers,
// one-time codes) is reported as changed but never returned.
func hostClipboardRead(_ *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Since int64 `json:"since"`
	}
	if len(params) > 0 && string(params) != "null" && json.Unmarshal(params, &request) != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid clipboard params"}
	}
	raw, err := clipboardDispatch(clipboardStateCommand, "{}")
	if err != nil {
		return nil, &rpcError{Code: -32002, Message: "clipboard is not available"}
	}
	var state struct {
		ChangeCount int64 `json:"changeCount"`
		Concealed   bool  `json:"concealed"`
	}
	if json.Unmarshal([]byte(raw), &state) != nil {
		return nil, &rpcError{Code: -32002, Message: "clipboard is not available"}
	}
	if request.Since != 0 && state.ChangeCount == request.Since {
		return map[string]any{"changed": false, "seq": state.ChangeCount}, nil
	}
	if state.Concealed {
		return map[string]any{"changed": true, "seq": state.ChangeCount, "concealed": true, "text": ""}, nil
	}
	rawText, err := clipboardDispatch(clipboardReadCommand, "{}")
	if err != nil {
		return nil, &rpcError{Code: -32002, Message: "clipboard is not available"}
	}
	var text string
	if json.Unmarshal([]byte(rawText), &text) != nil {
		return nil, &rpcError{Code: -32002, Message: "clipboard is not available"}
	}
	text = strings.ToValidUTF8(text, "")
	truncated := false
	if len(text) > clipboardMaxTextBytes {
		cut := clipboardMaxTextBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text, truncated = text[:cut], true
	}
	return map[string]any{"changed": true, "seq": state.ChangeCount, "text": text, "truncated": truncated}, nil
}

func hostClipboardWrite(_ *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(params, &request) != nil || request.Text == nil {
		return nil, &rpcError{Code: -32602, Message: "clipboard text is required"}
	}
	if len(*request.Text) > clipboardMaxWriteBytes || !utf8.ValidString(*request.Text) {
		return nil, &rpcError{Code: -32602, Message: "clipboard text must be valid UTF-8 of at most 1 MiB"}
	}
	payload, _ := json.Marshal(map[string]string{"text": *request.Text})
	if _, err := clipboardDispatch(clipboardWriteCommand, string(payload)); err != nil {
		return nil, &rpcError{Code: -32002, Message: "clipboard write failed"}
	}
	return map[string]any{"written": true}, nil
}
