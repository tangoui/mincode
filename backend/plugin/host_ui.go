package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/backend/desktop"
	"mincode/app/plugins/api"
)

// Generic host UI requests. Every plugin uses the same methods; the host
// decides how each request is presented and never branches on plugin IDs.
//
//   - host.editor.open    (workspace.read) open a workspace file at a position
//   - host.view.open      (ui)             open a plugin view, optionally with an open request;
//     raise + activation also brings the main window forward (own tab view,
//     single-use global shortcut activation token)
//   - host.diff.open      (ui)             show a read-only diff of two texts
//   - host.terminal.open  (shell)          open a terminal tab; a command runs only after the user confirms
//   - host.quickPick      (ui)             ask the user to pick from a list
//   - host.inputBox       (ui)             ask the user for one line of text
//
// The first four are fire-and-forget navigation (rate limited per plugin);
// the prompts wait for the user's answer, one at a time per plugin.
const (
	pluginEditorOpenEvent   = "plugin:editor-open"
	pluginViewOpenEvent     = "plugin:view-open"
	pluginDiffOpenEvent     = "plugin:diff-open"
	pluginTerminalOpenEvent = "plugin:terminal-open"
	pluginPromptEvent       = "plugin:prompt"
	pluginPromptCancelEvent = "plugin:prompt-cancel"
	pluginPromptReplyCmd    = "plugin_host_prompt_reply"

	hostUIRequestsPerMinute = 20
	hostUITitleMaxRunes     = 120
	hostUITextMaxRunes      = 500
	hostViewParamsMax       = 16
	hostViewParamMaxRunes   = 1024
	hostViewParamsMaxBytes  = 8 * 1024
	hostDiffSideMaxBytes    = 256 << 10
	hostTerminalCmdMaxRunes = 1024
	hostPositionMax         = 10_000_000
	hostQuickPickItemsMax   = 200
	hostPromptValueMaxBytes = 4096
	// A prompt waits timeoutMs (default 25 s, at most 5 min). The caller's
	// host-call timeout must be longer: the SDK helpers set it from timeoutMs.
	// Prompts sent while handling a plain invoke must finish within its 30 s.
	hostPromptDefaultTimeout = 25 * time.Second
	hostPromptMinTimeout     = 5 * time.Second
	hostPromptMaxTimeout     = 5 * time.Minute
)

var (
	hostViewIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	hostEntryPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	hostItemIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._:/@#+-]{1,128}$`)
	hostLanguagePattern = regexp.MustCompile(`^[A-Za-z0-9+#._-]{1,32}$`)
)

// hostUIEmit is core.Emit; a variable so tests can observe events.
var hostUIEmit = core.Emit

// hostViewRaise brings the main window to the front (host.view.open with
// raise); a variable so tests need no desktop.
var hostViewRaise = func() error { return desktop.Navigate(desktop.NavigationTarget{Kind: "app"}) }

// hostRateLimiter allows `limit` requests per plugin within `window`.
type hostRateLimiter struct {
	mu     sync.Mutex
	sent   map[string][]time.Time
	now    func() time.Time
	limit  int
	window time.Duration
}

func newHostRateLimiter(limit int, window time.Duration) *hostRateLimiter {
	return &hostRateLimiter{sent: map[string][]time.Time{}, now: time.Now, limit: limit, window: window}
}

func (l *hostRateLimiter) allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.sent[key][:0]
	for _, at := range l.sent[key] {
		if now.Sub(at) < l.window {
			recent = append(recent, at)
		}
	}
	if len(recent) >= l.limit {
		l.sent[key] = recent
		return false
	}
	l.sent[key] = append(recent, now)
	return true
}

var hostUILimiter = newHostRateLimiter(hostUIRequestsPerMinute, time.Minute)

func init() {
	registerHostMethod("host.editor.open", nil, api.CapabilityWorkspaceRead, hostEditorOpen)
	registerHostMethod("host.view.open", nil, api.CapabilityUI, hostViewOpen)
	registerHostMethod("host.diff.open", nil, api.CapabilityUI, hostDiffOpen)
	registerHostMethod("host.terminal.open", nil, api.CapabilityShell, hostTerminalOpen)
	registerHostMethod("host.quickPick", nil, api.CapabilityUI, hostQuickPick)
	registerHostMethod("host.inputBox", nil, api.CapabilityUI, hostInputBox)
	core.Register(pluginPromptReplyCmd, pluginHostPromptReplyHandler)
}

func hostUIParamsError(message string) *rpcError {
	return &rpcError{Code: -32602, Message: message, Data: map[string]string{"code": "invalid_params"}}
}

func decodeHostUIParams(params json.RawMessage, target any) *rpcError {
	decoder := json.NewDecoder(strings.NewReader(string(params)))
	decoder.DisallowUnknownFields()
	if len(params) == 0 || decoder.Decode(target) != nil {
		return hostUIParamsError("invalid params")
	}
	return nil
}

// cleanHostText drops control characters (keeping newlines only when
// multiline) and bounds the length in runes.
func cleanHostText(value string, limit int, multiline bool) string {
	value = strings.Map(func(r rune) rune {
		if (r == '\n' || r == '\t') && multiline {
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

func (runtimeHost *processRuntime) displayName() string {
	return cleanHostText(runtimeHost.manifest.Name, hostUITitleMaxRunes, false)
}

func rateLimited(runtimeHost *processRuntime) *rpcError {
	if hostUILimiter.allow(runtimeHost.pluginID) {
		return nil
	}
	return &rpcError{Code: -32005, Message: fmt.Sprintf("too many host UI requests; at most %d per minute", hostUIRequestsPerMinute)}
}

// unsafeTextRune reports characters that could make displayed text differ
// from what runs or is stored: C0/C1 controls, DEL, zero-width characters and
// bidirectional formatting.
func unsafeTextRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x061c, r == 0xfeff:
		return true
	}
	return false
}

func promptTimeout(ms int) (time.Duration, *rpcError) {
	if ms == 0 {
		return hostPromptDefaultTimeout, nil
	}
	timeout := time.Duration(ms) * time.Millisecond
	if timeout < hostPromptMinTimeout || timeout > hostPromptMaxTimeout {
		return 0, hostUIParamsError("timeoutMs must be 5000-300000")
	}
	return timeout, nil
}

// ---- host.editor.open ----

type editorOpenEvent struct {
	PluginID   string `json:"pluginId"`
	PluginName string `json:"pluginName"`
	Root       string `json:"root"`
	Path       string `json:"path"`
	Line       int    `json:"line,omitempty"`
	Column     int    `json:"column,omitempty"`
}

func hostEditorOpen(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Path   string `json:"path"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	if request.Line < 0 || request.Line > hostPositionMax || request.Column < 0 || request.Column > hostPositionMax || (request.Column > 0 && request.Line == 0) {
		return nil, hostUIParamsError("line and column must be positive; column needs a line")
	}
	file, rpcErr := resolveWorkspaceEntry(request.Path, false)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if file.RelativePath == "" {
		return nil, hostUIParamsError("path must name a file")
	}
	info, err := os.Stat(file.Path)
	if err != nil {
		return nil, workspaceRPCError("not_found", "workspace file not found")
	}
	if !info.Mode().IsRegular() {
		return nil, workspaceRPCError("not_file", "path is not a regular file")
	}
	if rpcErr := rateLimited(runtimeHost); rpcErr != nil {
		return nil, rpcErr
	}
	root := file.Root
	// The frontend matches saved projects by the root it announced, which may
	// differ from the canonical one (symlinks such as /tmp → /private/tmp).
	if announced := pluginWorkspaceRootInput(); announced != "" {
		root = announced
	}
	hostUIEmit(pluginEditorOpenEvent, editorOpenEvent{
		PluginID: runtimeHost.pluginID, PluginName: runtimeHost.displayName(), Root: root,
		Path: filepath.ToSlash(file.RelativePath), Line: request.Line, Column: request.Column,
	})
	return map[string]any{"requested": true}, nil
}

// ---- host.view.open ----

type viewOpenEvent struct {
	PluginID       string         `json:"pluginId"`
	PluginName     string         `json:"pluginName"`
	TargetPluginID string         `json:"targetPluginId"`
	ViewID         string         `json:"viewId"`
	Entry          string         `json:"entry,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
}

// viewOpenTarget checks that the target plugin is installed, enabled and
// declares the view. A test seam over the process-wide manager.
var viewOpenTarget = func(pluginID, viewID string) error {
	record, err := host().record(pluginID)
	if err != nil {
		return fmt.Errorf("plugin %q is not installed", pluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if !record.enabled || record.manifest == nil {
		return fmt.Errorf("plugin %q is not enabled", pluginID)
	}
	if !host().workspaceActiveLocked(record) {
		return fmt.Errorf("plugin %q is not active in the current workspace", pluginID)
	}
	for _, view := range record.manifest.Contributes.Views {
		if view.ID == viewID && view.Floating == nil {
			return nil
		}
	}
	return fmt.Errorf("plugin %q has no view %q", pluginID, viewID)
}

func cleanViewParams(raw map[string]json.RawMessage) (map[string]any, *rpcError) {
	if len(raw) > hostViewParamsMax {
		return nil, hostUIParamsError(fmt.Sprintf("at most %d params", hostViewParamsMax))
	}
	out := make(map[string]any, len(raw))
	for key, value := range raw {
		if key == "" || len(key) > 128 {
			return nil, hostUIParamsError("invalid param name")
		}
		var scalar any
		if json.Unmarshal(value, &scalar) != nil {
			return nil, hostUIParamsError("invalid param value")
		}
		switch typed := scalar.(type) {
		case string:
			if !utf8.ValidString(typed) || utf8.RuneCountInString(typed) > hostViewParamMaxRunes {
				return nil, hostUIParamsError("param value too long")
			}
		case float64, bool:
		default:
			return nil, hostUIParamsError("params must be strings, numbers or booleans")
		}
		out[key] = scalar
	}
	if encoded, _ := json.Marshal(out); len(encoded) > hostViewParamsMaxBytes {
		return nil, hostUIParamsError("params too large")
	}
	return out, nil
}

// hostViewOpen opens a view. With raise it also brings the main window to
// the front, which a background plugin may do only right after the user
// pressed one of its global shortcuts: activation must be a token from
// plugin.globalShortcut.activation (single use, 120 s, this plugin's), and
// the view must be the plugin's own tab view. Every check runs before the
// token is consumed, so a refused request does not spend it.
func hostViewOpen(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		PluginID   string                     `json:"pluginId"`
		ViewID     string                     `json:"viewId"`
		Entry      string                     `json:"entry"`
		Params     map[string]json.RawMessage `json:"params"`
		Raise      bool                       `json:"raise"`
		Activation string                     `json:"activation"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	target := strings.TrimSpace(request.PluginID)
	if target == "" {
		target = runtimeHost.pluginID
	}
	if request.Activation != "" && !request.Raise {
		return nil, hostUIParamsError("activation needs raise")
	}
	if request.Raise && target != runtimeHost.pluginID {
		return nil, &rpcError{Code: -32001, Message: "raise opens only the plugin's own view", Data: map[string]string{"code": contributionErrForbidden}}
	}
	if request.Raise && request.Activation == "" {
		return nil, raiseActivationError()
	}
	if !hostViewIDPattern.MatchString(request.ViewID) || !pluginIDPattern.MatchString(target) {
		return nil, hostUIParamsError("invalid pluginId or viewId")
	}
	if request.Entry != "" && !hostEntryPattern.MatchString(request.Entry) {
		return nil, hostUIParamsError("invalid entry")
	}
	if request.Entry == "" && len(request.Params) > 0 {
		return nil, hostUIParamsError("params need an entry")
	}
	cleaned, rpcErr := cleanViewParams(request.Params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if err := viewOpenTarget(target, request.ViewID); err != nil {
		return nil, &rpcError{Code: -32002, Message: err.Error(), Data: map[string]string{"code": "not_found"}}
	}
	if rpcErr := rateLimited(runtimeHost); rpcErr != nil {
		return nil, rpcErr
	}
	if request.Raise {
		// No raise while the app is locked: refuse before spending the token,
		// and again after it in case the lock landed in between (locking also
		// revokes every token).
		if appLocked() {
			return nil, raiseLockedError()
		}
		if _, ok := consumeActivationToken(runtimeHost.pluginID, request.Activation); !ok {
			return nil, raiseActivationError()
		}
		if appLocked() {
			return nil, raiseLockedError()
		}
	}
	event := viewOpenEvent{PluginID: runtimeHost.pluginID, PluginName: runtimeHost.displayName(), TargetPluginID: target, ViewID: request.ViewID, Entry: request.Entry}
	if len(cleaned) > 0 {
		event.Params = cleaned
	}
	hostUIEmit(pluginViewOpenEvent, event)
	if !request.Raise {
		return map[string]any{"requested": true}, nil
	}
	raised := hostViewRaise() == nil
	return map[string]any{"requested": true, "raised": raised}, nil
}

// raiseActivationError: raise without a valid activation token (missing,
// unknown, used, expired, revoked or another plugin's).
func raiseActivationError() *rpcError {
	return &rpcError{Code: -32001, Message: "raise needs a valid global shortcut activation token", Data: map[string]string{"code": globalShortcutActivationInvalid}}
}

// raiseLockedError: raise while the app is locked.
func raiseLockedError() *rpcError {
	return &rpcError{Code: -32001, Message: "MinCode is locked", Data: map[string]string{"code": globalShortcutRaiseLocked}}
}

// ---- host.diff.open ----

type diffSide struct {
	Title   string `json:"title,omitempty"`
	Content string `json:"content"`
}

type diffOpenEvent struct {
	PluginID   string   `json:"pluginId"`
	PluginName string   `json:"pluginName"`
	Title      string   `json:"title"`
	Language   string   `json:"language,omitempty"`
	Left       diffSide `json:"left"`
	Right      diffSide `json:"right"`
}

func hostDiffOpen(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Title    string   `json:"title"`
		Language string   `json:"language"`
		Left     diffSide `json:"left"`
		Right    diffSide `json:"right"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	title := cleanHostText(request.Title, hostUITitleMaxRunes, false)
	if title == "" {
		return nil, hostUIParamsError("title is required")
	}
	if request.Language != "" && !hostLanguagePattern.MatchString(request.Language) {
		return nil, hostUIParamsError("invalid language")
	}
	for _, side := range []diffSide{request.Left, request.Right} {
		if !utf8.ValidString(side.Content) || len(side.Content) > hostDiffSideMaxBytes {
			return nil, hostUIParamsError(fmt.Sprintf("each side must be UTF-8 text of at most %d bytes", hostDiffSideMaxBytes))
		}
	}
	if rpcErr := rateLimited(runtimeHost); rpcErr != nil {
		return nil, rpcErr
	}
	hostUIEmit(pluginDiffOpenEvent, diffOpenEvent{
		PluginID: runtimeHost.pluginID, PluginName: runtimeHost.displayName(), Title: title, Language: request.Language,
		Left:  diffSide{Title: cleanHostText(request.Left.Title, hostUITitleMaxRunes, false), Content: request.Left.Content},
		Right: diffSide{Title: cleanHostText(request.Right.Title, hostUITitleMaxRunes, false), Content: request.Right.Content},
	})
	return map[string]any{"requested": true}, nil
}

// ---- host.terminal.open ----

type terminalOpenEvent struct {
	PluginID   string `json:"pluginId"`
	PluginName string `json:"pluginName"`
	Title      string `json:"title,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Command    string `json:"command,omitempty"`
}

func hostTerminalOpen(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Title   string `json:"title"`
		Cwd     string `json:"cwd"`
		Command string `json:"command"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	command := strings.TrimSpace(request.Command)
	if !utf8.ValidString(command) || utf8.RuneCountInString(command) > hostTerminalCmdMaxRunes || strings.IndexFunc(command, unsafeTextRune) >= 0 {
		return nil, hostUIParamsError("command must be one line of at most 1024 characters without control, zero-width or bidirectional characters")
	}
	cwd := ""
	if strings.TrimSpace(request.Cwd) != "" || pluginWorkspaceRoot() != "" {
		dir, rpcErr := resolveWorkspaceEntry(request.Cwd, false)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if info, err := os.Stat(dir.Path); err != nil || !info.IsDir() {
			return nil, workspaceRPCError("not_dir", "cwd is not a workspace directory")
		}
		cwd = dir.Path
	}
	if rpcErr := rateLimited(runtimeHost); rpcErr != nil {
		return nil, rpcErr
	}
	hostUIEmit(pluginTerminalOpenEvent, terminalOpenEvent{
		PluginID: runtimeHost.pluginID, PluginName: runtimeHost.displayName(),
		Title: cleanHostText(request.Title, hostUITitleMaxRunes, false), Cwd: cwd, Command: command,
	})
	// The user still decides whether the command runs.
	return map[string]any{"requested": true}, nil
}

// ---- prompts: host.quickPick / host.inputBox ----

type quickPickItem struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type promptEvent struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	PluginID    string          `json:"pluginId"`
	PluginName  string          `json:"pluginName"`
	Title       string          `json:"title"`
	Placeholder string          `json:"placeholder,omitempty"`
	Prompt      string          `json:"prompt,omitempty"`
	Value       string          `json:"value,omitempty"`
	Password    bool            `json:"password,omitempty"`
	CanPickMany bool            `json:"canPickMany,omitempty"`
	Items       []quickPickItem `json:"items,omitempty"`
	// Call is set for kind "pluginCall" (host.plugins.invoke approvals) and
	// for kind "serviceConfirm" (a dangerous service call the host makes).
	Call *pluginCallDetails `json:"call,omitempty"`
	// Database is set for kind "databaseSchema" (host.database.* approvals).
	Database *databaseSchemaDetails `json:"database,omitempty"`
	// SSHFiles is set for kind "sshFiles" (host.ssh.files.authorize).
	SSHFiles *sshFilesDetails `json:"sshFiles,omitempty"`
	// Service is set for kind "serviceProvider": the user picks one of Items
	// as the provider of this service (host_services.go).
	Service string `json:"service,omitempty"`
}

type promptReply struct {
	Cancelled bool     `json:"cancelled"`
	IDs       []string `json:"ids,omitempty"`
	Value     *string  `json:"value,omitempty"`
}

type pendingPrompt struct {
	pluginID string
	kind     string
	items    map[string]bool
	many     bool
	reply    chan promptReply
}

type promptBroker struct {
	mu       sync.Mutex
	seq      uint64
	pending  map[string]*pendingPrompt
	byPlugin map[string]string
}

var hostPrompts = &promptBroker{pending: map[string]*pendingPrompt{}, byPlugin: map[string]string{}}

// ask shows a prompt and waits for the user, the timeout or the plugin
// process to stop. At most one prompt per plugin is open at a time.
func (b *promptBroker) ask(ctx context.Context, event promptEvent, prompt *pendingPrompt, timeout time.Duration) (promptReply, *rpcError) {
	b.mu.Lock()
	if _, busy := b.byPlugin[prompt.pluginID]; busy {
		b.mu.Unlock()
		return promptReply{}, &rpcError{Code: -32004, Message: "another prompt from this plugin is still open", Data: map[string]string{"code": "busy"}}
	}
	b.seq++
	id := fmt.Sprintf("prompt-%d-%d", time.Now().UnixNano(), b.seq)
	prompt.reply = make(chan promptReply, 1)
	b.pending[id] = prompt
	b.byPlugin[prompt.pluginID] = id
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		if b.byPlugin[prompt.pluginID] == id {
			delete(b.byPlugin, prompt.pluginID)
		}
		b.mu.Unlock()
	}()
	event.ID = id
	hostUIEmit(pluginPromptEvent, event)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case reply := <-prompt.reply:
		return reply, nil
	case <-ctx.Done():
	case <-timer.C:
	}
	hostUIEmit(pluginPromptCancelEvent, map[string]string{"id": id})
	return promptReply{Cancelled: true}, nil
}

// answer delivers the frontend's reply after validating it against the prompt.
func (b *promptBroker) answer(id string, reply promptReply) error {
	b.mu.Lock()
	prompt := b.pending[id]
	b.mu.Unlock()
	if prompt == nil {
		return fmt.Errorf("prompt %q is no longer open", id)
	}
	if reply.Cancelled {
		reply = promptReply{Cancelled: true}
	} else if prompt.kind == serviceProviderPromptKind {
		if !validServiceProviderReply(prompt.items, reply) {
			return fmt.Errorf("invalid service provider reply")
		}
	} else if prompt.kind == "quickPick" || prompt.kind == "pluginCall" || prompt.kind == databaseSchemaPromptKind || prompt.kind == sshFilesPromptKind || prompt.kind == serviceConfirmPromptKind {
		if reply.Value != nil || len(reply.IDs) == 0 || (!prompt.many && len(reply.IDs) != 1) {
			return fmt.Errorf("invalid quick pick reply")
		}
		seen := map[string]bool{}
		for _, itemID := range reply.IDs {
			if !prompt.items[itemID] || seen[itemID] {
				return fmt.Errorf("invalid quick pick reply")
			}
			seen[itemID] = true
		}
	} else if reply.Value == nil || len(reply.IDs) > 0 || len(*reply.Value) > hostPromptValueMaxBytes || !utf8.ValidString(*reply.Value) {
		return fmt.Errorf("invalid input box reply")
	}
	select {
	case prompt.reply <- reply:
		return nil
	default:
		return fmt.Errorf("prompt %q was already answered", id)
	}
}

func pluginHostPromptReplyHandler(_, argsJSON string) (string, error) {
	var request struct {
		ID    string      `json:"id"`
		Reply promptReply `json:"reply"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &request); err != nil {
		return "", fmt.Errorf("invalid prompt reply: %w", err)
	}
	if err := hostPrompts.answer(request.ID, request.Reply); err != nil {
		return "", err
	}
	return "{}", nil
}

func (runtimeHost *processRuntime) promptContext() context.Context {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	if runtimeHost.runContext != nil {
		return runtimeHost.runContext
	}
	return context.Background()
}

func hostQuickPick(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Title       string          `json:"title"`
		Placeholder string          `json:"placeholder"`
		CanPickMany bool            `json:"canPickMany"`
		Items       []quickPickItem `json:"items"`
		TimeoutMs   int             `json:"timeoutMs"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	timeout, rpcErr := promptTimeout(request.TimeoutMs)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if len(request.Items) == 0 || len(request.Items) > hostQuickPickItemsMax {
		return nil, hostUIParamsError(fmt.Sprintf("items must list 1-%d entries", hostQuickPickItemsMax))
	}
	ids := map[string]bool{}
	items := make([]quickPickItem, 0, len(request.Items))
	for _, item := range request.Items {
		label := cleanHostText(item.Label, hostUITitleMaxRunes, false)
		if !hostItemIDPattern.MatchString(item.ID) || ids[item.ID] || label == "" {
			return nil, hostUIParamsError("every item needs a unique id and a label")
		}
		ids[item.ID] = true
		items = append(items, quickPickItem{ID: item.ID, Label: label,
			Description: cleanHostText(item.Description, hostUITitleMaxRunes, false), Detail: cleanHostText(item.Detail, hostUITextMaxRunes, false)})
	}
	event := promptEvent{Kind: "quickPick", PluginID: runtimeHost.pluginID, PluginName: runtimeHost.displayName(),
		Title: cleanHostText(request.Title, hostUITitleMaxRunes, false), Placeholder: cleanHostText(request.Placeholder, hostUITitleMaxRunes, false),
		CanPickMany: request.CanPickMany, Items: items}
	reply, rpcErr := hostPrompts.ask(runtimeHost.promptContext(), event, &pendingPrompt{pluginID: runtimeHost.pluginID, kind: "quickPick", items: ids, many: request.CanPickMany}, timeout)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if reply.Cancelled {
		return map[string]any{"cancelled": true, "ids": []string{}}, nil
	}
	return map[string]any{"cancelled": false, "ids": reply.IDs}, nil
}

func hostInputBox(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Title       string `json:"title"`
		Prompt      string `json:"prompt"`
		Placeholder string `json:"placeholder"`
		Value       string `json:"value"`
		Password    bool   `json:"password"`
		TimeoutMs   int    `json:"timeoutMs"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	timeout, rpcErr := promptTimeout(request.TimeoutMs)
	if rpcErr != nil {
		return nil, rpcErr
	}
	title := cleanHostText(request.Title, hostUITitleMaxRunes, false)
	if title == "" {
		return nil, hostUIParamsError("title is required")
	}
	if len(request.Value) > hostPromptValueMaxBytes || strings.ContainsAny(request.Value, "\x00\r\n") {
		return nil, hostUIParamsError("value must be one line of at most 4096 bytes")
	}
	event := promptEvent{Kind: "inputBox", PluginID: runtimeHost.pluginID, PluginName: runtimeHost.displayName(), Title: title,
		Prompt: cleanHostText(request.Prompt, hostUITextMaxRunes, false), Placeholder: cleanHostText(request.Placeholder, hostUITitleMaxRunes, false),
		Value: request.Value, Password: request.Password}
	reply, rpcErr := hostPrompts.ask(runtimeHost.promptContext(), event, &pendingPrompt{pluginID: runtimeHost.pluginID, kind: "inputBox"}, timeout)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if reply.Cancelled || reply.Value == nil {
		return map[string]any{"cancelled": true, "value": ""}, nil
	}
	return map[string]any{"cancelled": false, "value": *reply.Value}, nil
}
