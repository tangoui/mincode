// Package desktop coordinates desktop-only capabilities behind MinCode's command facade.
package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/backend/storage"
	pluginapi "mincode/app/plugins/api"
)

const windowStateKey = "desktop_window_state_v1"

type NavigationTarget struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
	// Seq identifies a target handed over through the cold-start queue
	// (desktop_take_pending_navigation); zero for live events.
	Seq uint64 `json:"seq,omitempty"`
}

type WindowState struct {
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximised bool `json:"maximised"`
}

type WorkArea struct{ X, Y, Width, Height int }

type Adapter struct {
	SetGlobalShortcut func(enabled bool, accelerator string) error
	SetBadge          func(label string) error
	FocusTarget       func(target NavigationTarget)
	// SetMenuAccelerators turns MinCode's own application-menu shortcuts off
	// while a plugin page holds the keyboard, and back on afterwards.
	SetMenuAccelerators func(enabled bool)
	// Plugin global shortcuts (backend/plugin/global_shortcuts.go) go through
	// the same desktop global shortcut manager as MinCode's own shortcut.
	// RegisterPluginShortcut reports an OS refusal (another application holds
	// the combo) as an error; HostShortcut is MinCode's own registered
	// accelerator ("" when off); IsWayland reports a Wayland session, where
	// plugin shortcuts are not offered.
	RegisterPluginShortcut   func(accelerator string, fn func()) error
	UnregisterPluginShortcut func(accelerator string) error
	HostShortcut             func() string
	IsWayland                func() bool
	// SetPluginTray rebuilds the plugin section of the system tray menu
	// (backend/plugin/tray_menu.go resolves the groups from manifests).
	SetPluginTray func(groups []PluginTrayGroup)
}

var (
	adapterMu sync.RWMutex
	adapter   Adapter
)

func init() {
	core.Register("desktop_set_global_shortcut", setGlobalShortcutHandler)
	core.Register("desktop_set_badge", setBadgeHandler)
	core.Register("desktop_navigate", navigateHandler)
	core.Register("desktop_take_pending_navigation", takePendingNavigationHandler)
	core.Register("desktop_set_menu_accelerators", setMenuAcceleratorsHandler)
}

// SetAdapter installs the native implementation and tells host shortcut
// listeners to re-read it (plugin shortcuts become registrable).
func SetAdapter(value Adapter) {
	adapterMu.Lock()
	adapter = value
	adapterMu.Unlock()
	notifyHostShortcutChange("")
}

func currentAdapter() Adapter { adapterMu.RLock(); defer adapterMu.RUnlock(); return adapter }

// desktop_set_global_shortcut errors are codes the frontend localizes
// (vue-app hostGlobalShortcutErrorNotice), not display text.
var errGlobalShortcutUnavailable = errors.New("global-shortcut-unavailable")

// GlobalShortcutOccupiedError: MinCode's own activation shortcut cannot be
// registered because the shared desktop shortcut manager already holds the combo for
// something else (a plugin global shortcut binding).
type GlobalShortcutOccupiedError struct{ Accelerator string }

func (e *GlobalShortcutOccupiedError) Error() string {
	return "global-shortcut-occupied: " + e.Accelerator
}

func setGlobalShortcutHandler(_, argsJSON string) (string, error) {
	var args struct {
		Enabled     bool   `json:"enabled"`
		Accelerator string `json:"accelerator"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	fn := currentAdapter().SetGlobalShortcut
	if fn == nil {
		return "", errGlobalShortcutUnavailable
	}
	if args.Accelerator == "" {
		args.Accelerator = "CmdOrCtrl+Shift+Space"
	}
	// MinCode's own shortcut wins over plugin shortcuts: listeners release a
	// plugin binding of the same combo before it is registered, then settle
	// on whatever the adapter actually holds afterwards.
	pending := ""
	if args.Enabled {
		pending = args.Accelerator
	}
	notifyHostShortcutChange(pending)
	err := fn(args.Enabled, args.Accelerator)
	notifyHostShortcutChange("")
	if err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func setMenuAcceleratorsHandler(_, argsJSON string) (string, error) {
	var args struct {
		Enabled *bool `json:"enabled"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if args.Enabled == nil {
		return "", fmt.Errorf("enabled is required")
	}
	// Without a native application menu (Linux, tests) there is nothing to toggle.
	if fn := currentAdapter().SetMenuAccelerators; fn != nil {
		fn(*args.Enabled)
	}
	return core.EncodeResult(nil)
}

func navigateHandler(_, argsJSON string) (string, error) {
	var target NavigationTarget
	if err := core.DecodeArgs(argsJSON, &target); err != nil {
		return "", err
	}
	// Only the native tray closure produces plugin-command targets (through
	// Navigate); the frontend runs plugin commands directly.
	if target.Kind == pluginCommandTargetKind {
		return "", fmt.Errorf("不支持的桌面导航目标")
	}
	if err := Navigate(target); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// Navigate brings the main window to the front and opens target there.
func Navigate(target NavigationTarget) error {
	if err := ValidateTarget(target); err != nil {
		return err
	}
	fn := currentAdapter().FocusTarget
	if fn == nil {
		return fmt.Errorf("桌面导航暂不可用")
	}
	fn(target)
	return nil
}

func setBadgeHandler(_, argsJSON string) (string, error) {
	var args struct {
		Label string `json:"label"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if len([]rune(args.Label)) > 4 {
		return "", fmt.Errorf("徽标文本过长")
	}
	fn := currentAdapter().SetBadge
	if fn == nil {
		return "", fmt.Errorf("应用徽标暂不可用")
	}
	if err := fn(args.Label); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func ParseDeepLink(raw string) (NavigationTarget, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, "mincode") {
		return NavigationTarget{}, fmt.Errorf("无效的 MinCode 深链")
	}
	action := strings.ToLower(strings.Trim(parsed.Host+parsed.Path, "/"))
	host := strings.ToLower(parsed.Host)
	if host != "settings" && host != "ai" && host != "workspace" && host != "" {
		return parsePluginDeepLink(strings.TrimSpace(raw), parsed)
	}
	switch action {
	case "settings":
		return NavigationTarget{Kind: "view", Value: "settings"}, nil
	case "ai":
		return NavigationTarget{Kind: "ai"}, nil
	case "workspace":
		view := strings.TrimSpace(parsed.Query().Get("view"))
		if !allowedView(view) {
			return NavigationTarget{}, fmt.Errorf("不支持的工作区")
		}
		return NavigationTarget{Kind: "view", Value: view}, nil
	default:
		return NavigationTarget{}, fmt.Errorf("不支持的深链动作")
	}
}

var pluginTargetIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// agentRunIDPattern matches agentprogress run IDs ("run-" + 32 hex digits).
var agentRunIDPattern = regexp.MustCompile(`^run-[0-9a-f]{32}$`)

// terminalPaneIDPattern matches the frontend's terminal pane IDs (UUIDs and
// prefixed live-session IDs); a terminal-pane target focuses that pane's tab.
var terminalPaneIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// Plugin links (contributes.uriHandlers) use mincode://<pluginId>/<path>?<query>.
// The desktop layer only checks the URL shape and forwards it as a
// "plugin-uri" target; the plugin host resolves it against the plugin's
// handlers (plugin_host_uri_resolve) and the frontend always asks the user
// before anything opens.
const pluginLinkMaxLength = 2048

var (
	pluginLinkHostPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,127}$`)
	pluginLinkReserved    = reservedLinkHosts()
)

func reservedLinkHosts() map[string]bool {
	reserved := map[string]bool{"settings": true, "ai": true, "workspace": true}
	if spec, ok := pluginapi.ContributionPointSpecFor("uriHandlers"); ok {
		for _, host := range spec.List("reservedHosts") {
			reserved[strings.ToLower(host)] = true
		}
	}
	return reserved
}

func parsePluginDeepLink(raw string, parsed *url.URL) (NavigationTarget, error) {
	decodedQuery, _ := url.QueryUnescape(parsed.RawQuery)
	if len(raw) > pluginLinkMaxLength || !utf8.ValidString(raw) || strings.IndexFunc(raw, unsafeLinkRune) >= 0 ||
		strings.IndexFunc(parsed.Path, unsafeDecodedRune) >= 0 || strings.IndexFunc(decodedQuery, unsafeDecodedRune) >= 0 {
		return NavigationTarget{}, fmt.Errorf("插件链接过长或包含非法字符")
	}
	if parsed.Opaque != "" || parsed.User != nil || strings.Contains(parsed.Host, "@") || strings.Contains(parsed.Host, ":") {
		return NavigationTarget{}, fmt.Errorf("插件链接不能包含账号或端口")
	}
	host := strings.ToLower(parsed.Host)
	if !pluginLinkHostPattern.MatchString(host) || pluginLinkReserved[host] {
		return NavigationTarget{}, fmt.Errorf("不支持的深链动作")
	}
	path := parsed.EscapedPath()
	if path == "" || path == "/" {
		return NavigationTarget{}, fmt.Errorf("插件链接缺少路径")
	}
	value := "mincode://" + host + path
	if parsed.RawQuery != "" {
		value += "?" + parsed.RawQuery
	}
	return NavigationTarget{Kind: "plugin-uri", Value: value}, nil
}

func unsafeLinkRune(r rune) bool {
	return unicode.IsSpace(r) || unsafeDecodedRune(r)
}

// unsafeDecodedRune rejects control and format (bidi) characters in the
// percent-decoded path and query; encoded spaces are allowed.
func unsafeDecodedRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

func ValidateTarget(target NavigationTarget) error {
	switch target.Kind {
	case "ai":
		if target.Value == "" {
			return nil
		}
	// app only brings the main window to the front; agent-runs opens the AI
	// task list; agent-run opens the conversation or session of one run.
	case "app", "agent-runs":
		if target.Value == "" {
			return nil
		}
	case "agent-run":
		if agentRunIDPattern.MatchString(target.Value) {
			return nil
		}
	case "terminal-pane":
		if terminalPaneIDPattern.MatchString(target.Value) {
			return nil
		}
	case "ai-conversation":
		var value struct {
			ConversationID   string `json:"conversationId"`
			WorkspaceScopeID string `json:"workspaceScopeId"`
		}
		if err := json.Unmarshal([]byte(target.Value), &value); err == nil &&
			strings.TrimSpace(value.ConversationID) != "" &&
			len(value.ConversationID) <= 512 && len(value.WorkspaceScopeID) <= 1024 {
			return nil
		}
	case "view":
		if allowedView(target.Value) {
			return nil
		}
	case "plugin-uri":
		if strings.HasPrefix(target.Value, "mincode://") {
			if parsed, err := ParseDeepLink(target.Value); err == nil && parsed.Kind == "plugin-uri" && parsed.Value == target.Value {
				return nil
			}
		}
	case "plugin":
		// A plugin notification opens that plugin's page; the view is optional.
		var value struct {
			PluginID string `json:"pluginId"`
			ViewID   string `json:"viewId"`
		}
		if err := json.Unmarshal([]byte(target.Value), &value); err == nil &&
			pluginTargetIDPattern.MatchString(value.PluginID) &&
			(value.ViewID == "" || pluginTargetIDPattern.MatchString(value.ViewID)) {
			return nil
		}
	case pluginCommandTargetKind:
		// A tray click runs a plugin command. Only the native tray closure
		// produces it: desktop_navigate refuses it and ParseDeepLink never
		// yields it.
		if validPluginCommandTarget(target.Value) {
			return nil
		}
	}
	return fmt.Errorf("不支持的桌面导航目标")
}

// Cold-start queue: navigation targets that arrive before the frontend has
// drained once (app launch via a mincode:// link, a notification click while
// the page loads) wait here instead of being emitted to a WebView that is not
// listening yet. The frontend drains with
// desktop_take_pending_navigation on mount, which also marks it ready; later
// links are emitted live. Bounded (oldest evicted) and expiring.
var (
	pendingNavigationMax = uriHandlerLimit("coldStartQueueMax", 8)
	pendingNavigationTTL = time.Duration(uriHandlerLimit("coldStartTtlSeconds", 120)) * time.Second
)

func uriHandlerLimit(name string, fallback int64) int {
	if spec, ok := pluginapi.ContributionPointSpecFor("uriHandlers"); ok {
		if value := spec.Limit(name); value > 0 {
			return int(value)
		}
	}
	return int(fallback)
}

type pendingNavigation struct {
	target NavigationTarget
	queued time.Time
}

var pendingNav struct {
	mu    sync.Mutex
	ready bool
	seq   uint64
	items []pendingNavigation
	now   func() time.Time
}

func pendingNow() time.Time {
	if pendingNav.now != nil {
		return pendingNav.now()
	}
	return time.Now()
}

// DeferUntilFrontendReady queues a target until the frontend has drained the
// cold-start queue once. It reports false (the caller emits the target live)
// once the frontend is ready.
func DeferUntilFrontendReady(target NavigationTarget) (NavigationTarget, bool) {
	pendingNav.mu.Lock()
	defer pendingNav.mu.Unlock()
	if pendingNav.ready {
		return target, false
	}
	pendingNav.seq++
	target.Seq = pendingNav.seq
	if len(pendingNav.items) >= pendingNavigationMax {
		pendingNav.items = append(pendingNav.items[:0], pendingNav.items[1:]...)
	}
	pendingNav.items = append(pendingNav.items, pendingNavigation{target: target, queued: pendingNow()})
	return target, true
}

// TakePendingNavigation returns and clears the unexpired queued targets and
// marks the frontend ready.
func TakePendingNavigation() []NavigationTarget {
	pendingNav.mu.Lock()
	defer pendingNav.mu.Unlock()
	pendingNav.ready = true
	now := pendingNow()
	targets := make([]NavigationTarget, 0, len(pendingNav.items))
	for _, item := range pendingNav.items {
		if now.Sub(item.queued) <= pendingNavigationTTL {
			targets = append(targets, item.target)
		}
	}
	pendingNav.items = nil
	return targets
}

// decodeExactJSON decodes one JSON object with no unknown fields and no
// trailing data.
func decodeExactJSON(raw string, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func takePendingNavigationHandler(_, _ string) (string, error) {
	return core.EncodeResult(TakePendingNavigation())
}

func allowedView(value string) bool {
	switch value {
	case "dashboard", "projects", "terminal", "ssh", "sftp", "database", "http", "sessions", "browser", "settings", "plugins":
		return true
	default:
		return false
	}
}

func LaunchPayload(args []string, workingDir string) (files []string, targets []NavigationTarget) {
	seen := map[string]bool{}
	for _, arg := range args[1:] {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(arg)), "mincode://") {
			if target, err := ParseDeepLink(arg); err == nil {
				targets = append(targets, target)
			}
			continue
		}
		if strings.Contains(arg, "://") || strings.HasPrefix(strings.ToLower(arg), "magnet:") {
			continue
		}
		path := strings.TrimSpace(arg)
		if path == "" || strings.HasPrefix(path, "-") {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(workingDir, path)
		}
		path = filepath.Clean(path)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && !seen[path] {
			seen[path] = true
			files = append(files, path)
		}
	}
	return files, targets
}

func ClampWindowState(state WindowState, areas []WorkArea) WindowState {
	if state.Width < 800 {
		state.Width = 800
	}
	if state.Height < 600 {
		state.Height = 600
	}
	for _, area := range areas {
		if state.X+100 >= area.X && state.X < area.X+area.Width && state.Y+40 >= area.Y && state.Y < area.Y+area.Height {
			if state.Width > area.Width {
				state.Width = area.Width
			}
			if state.Height > area.Height {
				state.Height = area.Height
			}
			if state.X+state.Width > area.X+area.Width {
				state.X = area.X + area.Width - state.Width
			}
			if state.Y+state.Height > area.Y+area.Height {
				state.Y = area.Y + area.Height - state.Height
			}
			return state
		}
	}
	if len(areas) > 0 {
		a := areas[0]
		if state.Width > a.Width {
			state.Width = a.Width
		}
		if state.Height > a.Height {
			state.Height = a.Height
		}
		state.X = a.X + (a.Width-state.Width)/2
		state.Y = a.Y + (a.Height-state.Height)/2
	}
	return state
}

func LoadWindowState() (WindowState, bool) {
	db, err := storage.DB()
	if err != nil {
		return WindowState{}, false
	}
	var raw []byte
	if err := db.QueryRow("SELECT value FROM app_kv WHERE key = ?", windowStateKey).Scan(&raw); err != nil {
		return WindowState{}, false
	}
	var state WindowState
	if json.Unmarshal(raw, &state) != nil || state.Width <= 0 || state.Height <= 0 {
		return WindowState{}, false
	}
	return state, true
}
func SaveWindowState(state WindowState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	db, err := storage.DB()
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO app_kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`, windowStateKey, raw)
	return err
}
