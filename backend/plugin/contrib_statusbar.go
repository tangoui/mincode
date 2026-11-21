package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"mincode/app/plugins/api"
)

// Status bar items (contributes.statusBar) are declared by process plugins and
// rendered by the host in a compact bar (vue-app/src/components/layout/
// AppStatusBar.vue). Declared data is static; items flagged "dynamic": true
// may additionally be updated through host.statusBar.update / reset while the
// plugin runs. Dynamic state is in-memory only, keyed by the runtime instance,
// coalesced (latest wins, at most one push per statusBarFlushInterval),
// rate-limited (statusBarRateLimit updates per second, excess silently
// dropped) and cleared whenever the runtime ends (stop, crash, disable,
// reload, uninstall, development generation change).
//
// The host methods reuse the existing commands capability: an item can only
// run one of the plugin's own commands, and the bar never renders plugin HTML.

const (
	statusBarPointKey = "statusBar"

	statusBarUpdateMethod = "host.statusBar.update"
	statusBarResetMethod  = "host.statusBar.reset"

	statusBarRateLimit  = 50
	statusBarRateWindow = time.Second
)

var (
	statusBarSpec             = mustContributionSpec(statusBarPointKey)
	statusBarMaxTextRunes     = int(statusBarSpec.Limit("textMaxRunes"))
	statusBarMaxTooltipRunes  = int(statusBarSpec.Limit("tooltipMaxRunes"))
	statusBarPriorityMin      = int(statusBarSpec.Limit("priorityMin"))
	statusBarPriorityMax      = int(statusBarSpec.Limit("priorityMax"))
	statusBarItemIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	statusBarSeverities       = map[string]struct{}{"normal": {}, "warning": {}, "error": {}}
	statusBarLocalizedMaxKeys = 16

	// Test seams.
	statusBarFlushInterval = 250 * time.Millisecond
	statusBarNow           = time.Now
	statusBarNotify        = func(pluginID string) {
		var manager *pluginManager
		manager.emitContributionsChanged(pluginID, []string{statusBarPointKey})
	}
)

// StatusBarItem is one validated contributes.statusBar entry as sent to the
// frontend consumer.
type StatusBarItem struct {
	ID               string            `json:"id"`
	Text             string            `json:"text,omitempty"`
	TextLocalized    map[string]string `json:"textLocalized,omitempty"`
	Tooltip          string            `json:"tooltip,omitempty"`
	TooltipLocalized map[string]string `json:"tooltipLocalized,omitempty"`
	Icon             string            `json:"icon,omitempty"`
	Command          string            `json:"command,omitempty"`
	Alignment        string            `json:"alignment"`
	Priority         int               `json:"priority"`
	When             string            `json:"when,omitempty"`
	Dynamic          bool              `json:"dynamic,omitempty"`
}

// StatusBarRuntimeItem is the dynamic override of one item. Nil fields keep
// the declared value.
type StatusBarRuntimeItem struct {
	Text     *string `json:"text,omitempty"`
	Tooltip  *string `json:"tooltip,omitempty"`
	Severity string  `json:"severity,omitempty"`
	Busy     *bool   `json:"busy,omitempty"`
	Visible  *bool   `json:"visible,omitempty"`
}

// StatusBarRuntime is the snapshot runtime overlay: item id -> override.
type StatusBarRuntime struct {
	Items map[string]StatusBarRuntimeItem `json:"items"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              statusBarPointKey,
		Decode:           decodeStatusBarEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate status bar item id %q", id) },
		IssuePrefix:      "invalid statusBar contribution",
		IssueText:        statusBarIssueText,
		EntryNoun:        "items",
		RuntimeOverlay:   statusBarRuntimeOverlay,
	})
	registerHostMethod(statusBarUpdateMethod, statusBarGate, api.CapabilityCommands, handleStatusBarUpdate)
	registerHostMethod(statusBarResetMethod, statusBarGate, api.CapabilityCommands, handleStatusBarReset)
	registerRecordLifecycleHook(clearStatusBarRuntime)
}

func statusBarIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("status bar item %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("status bar entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeStatusBarEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var item StatusBarItem
	if err := decodeStrictJSON(raw, &item, true); err != nil {
		return nil, item.ID, fmt.Errorf("parse status bar item: %w", err)
	}
	if !statusBarItemIDPattern.MatchString(item.ID) {
		return nil, item.ID, fmt.Errorf("status bar item id %q is invalid", item.ID)
	}
	if item.Text == "" {
		if !item.Dynamic {
			return nil, item.ID, errors.New("text is required unless the item is dynamic")
		}
	} else if err := validateStatusBarText(item.Text); err != nil {
		return nil, item.ID, err
	}
	if err := validateStatusBarLocalized(item.TextLocalized, validateStatusBarText); err != nil {
		return nil, item.ID, fmt.Errorf("textLocalized: %w", err)
	}
	if err := validateStatusBarTooltip(item.Tooltip); err != nil {
		return nil, item.ID, err
	}
	if err := validateStatusBarLocalized(item.TooltipLocalized, validateStatusBarTooltip); err != nil {
		return nil, item.ID, fmt.Errorf("tooltipLocalized: %w", err)
	}
	if item.Icon != "" {
		if _, ok := pluginViewIcons[item.Icon]; !ok {
			return nil, item.ID, fmt.Errorf("icon %q is not supported", item.Icon)
		}
	}
	if item.Command != "" {
		if _, ok := ctx.commands[item.Command]; !ok {
			return nil, item.ID, fmt.Errorf("command %q is not one of this plugin's commands", item.Command)
		}
	}
	switch item.Alignment {
	case "":
		item.Alignment = "left"
	case "left", "right":
	default:
		return nil, item.ID, fmt.Errorf("alignment %q must be left or right", item.Alignment)
	}
	if item.Priority < statusBarPriorityMin || item.Priority > statusBarPriorityMax {
		return nil, item.ID, fmt.Errorf("priority must be between %d and %d", statusBarPriorityMin, statusBarPriorityMax)
	}
	if item.When != "" {
		if _, err := parseWhenExpression(item.When); err != nil {
			return nil, item.ID, fmt.Errorf("when: %w", err)
		}
	}
	return item, item.ID, nil
}

// isStatusBarUnsafeRune rejects control characters, line breaks and bidi
// overrides/isolates so a plugin cannot spoof neighbouring host text.
func isStatusBarUnsafeRune(r rune, allowNewline bool) bool {
	if r == '\n' && allowNewline {
		return false
	}
	switch {
	case unicode.IsControl(r), r == '\u2028', r == '\u2029':
		return true
	case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
		return true
	}
	return false
}

func validateStatusBarText(text string) error {
	if !utf8.ValidString(text) {
		return errors.New("text must be valid UTF-8")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("text must not be blank")
	}
	if utf8.RuneCountInString(text) > statusBarMaxTextRunes {
		return fmt.Errorf("text exceeds %d characters", statusBarMaxTextRunes)
	}
	for _, r := range text {
		if isStatusBarUnsafeRune(r, false) {
			return errors.New("text must be a single line without control or bidirectional override characters")
		}
	}
	return nil
}

func validateStatusBarTooltip(tooltip string) error {
	if !utf8.ValidString(tooltip) {
		return errors.New("tooltip must be valid UTF-8")
	}
	if utf8.RuneCountInString(tooltip) > statusBarMaxTooltipRunes {
		return fmt.Errorf("tooltip exceeds %d characters", statusBarMaxTooltipRunes)
	}
	for _, r := range tooltip {
		if isStatusBarUnsafeRune(r, true) {
			return errors.New("tooltip must not contain control or bidirectional override characters")
		}
	}
	return nil
}

func validateStatusBarLocalized(values map[string]string, validate func(string) error) error {
	if len(values) > statusBarLocalizedMaxKeys {
		return fmt.Errorf("exceeds %d locales", statusBarLocalizedMaxKeys)
	}
	for locale, value := range values {
		if strings.TrimSpace(locale) == "" || len(locale) > 32 {
			return fmt.Errorf("locale %q is invalid", locale)
		}
		if err := validate(value); err != nil {
			return fmt.Errorf("%s: %w", locale, err)
		}
	}
	return nil
}

// statusBarItems returns the validated items of a manifest.
func (manifest Manifest) statusBarItems() []StatusBarItem {
	resolved := manifest.contribution(statusBarPointKey)
	if resolved == nil {
		return nil
	}
	items := make([]StatusBarItem, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if item, ok := entry.(StatusBarItem); ok {
			items = append(items, item)
		}
	}
	return items
}

func statusBarDynamicItem(runtimeHost *processRuntime, id string) bool {
	for _, item := range runtimeHost.manifest.statusBarItems() {
		if item.ID == id {
			return item.Dynamic
		}
	}
	return false
}

// statusBarGate admits the host methods only for plugins that declare at
// least one dynamic item.
func statusBarGate(runtimeHost *processRuntime) bool {
	for _, item := range runtimeHost.manifest.statusBarItems() {
		if item.Dynamic {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Runtime state.

type statusBarState struct {
	pluginID    string
	generation  uint64
	items       map[string]StatusBarRuntimeItem
	windowStart time.Time
	windowCount int
	lastEmit    time.Time
	timer       *time.Timer
}

var (
	statusBarMu     sync.Mutex
	statusBarStates = map[*processRuntime]*statusBarState{}
)

func runtimeStopping(runtimeHost *processRuntime) bool {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	return runtimeHost.stopping
}

// statusBarStateLocked returns (creating) the state of a runtime and drops
// states of earlier runtimes of the same plugin, for example after a
// development generation switch. Caller holds statusBarMu.
func statusBarStateLocked(runtimeHost *processRuntime, generation uint64) *statusBarState {
	state := statusBarStates[runtimeHost]
	if state != nil && state.generation != generation {
		stopStatusBarTimer(state)
		state = nil
	}
	if state == nil {
		for other, previous := range statusBarStates {
			if other != runtimeHost && previous.pluginID == runtimeHost.pluginID {
				stopStatusBarTimer(previous)
				delete(statusBarStates, other)
			}
		}
		state = &statusBarState{pluginID: runtimeHost.pluginID, generation: generation, items: map[string]StatusBarRuntimeItem{}}
		statusBarStates[runtimeHost] = state
	}
	return state
}

func stopStatusBarTimer(state *statusBarState) {
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
}

// admitStatusBarUpdateLocked applies the per-runtime rate limit.
func admitStatusBarUpdateLocked(state *statusBarState, now time.Time) bool {
	if state.windowStart.IsZero() || now.Sub(state.windowStart) >= statusBarRateWindow {
		state.windowStart = now
		state.windowCount = 0
	}
	if state.windowCount >= statusBarRateLimit {
		return false
	}
	state.windowCount++
	return true
}

// scheduleStatusBarEmitLocked coalesces pushes: the frontend re-reads the
// latest state, so one notification per interval is enough.
func scheduleStatusBarEmitLocked(runtimeHost *processRuntime, state *statusBarState, now time.Time) (emitNow bool) {
	if state.timer != nil {
		return false
	}
	elapsed := now.Sub(state.lastEmit)
	if state.lastEmit.IsZero() || elapsed >= statusBarFlushInterval {
		state.lastEmit = now
		return true
	}
	state.timer = time.AfterFunc(statusBarFlushInterval-elapsed, func() {
		statusBarMu.Lock()
		current := statusBarStates[runtimeHost]
		if current != state {
			statusBarMu.Unlock()
			return
		}
		state.timer = nil
		state.lastEmit = statusBarNow()
		pluginID := state.pluginID
		statusBarMu.Unlock()
		statusBarNotify(pluginID)
	})
	return false
}

type statusBarUpdateParams struct {
	ID       string  `json:"id"`
	Text     *string `json:"text"`
	Tooltip  *string `json:"tooltip"`
	Severity *string `json:"severity"`
	Busy     *bool   `json:"busy"`
	Visible  *bool   `json:"visible"`
}

func handleStatusBarUpdate(runtimeHost *processRuntime, raw json.RawMessage) (any, *rpcError) {
	var params statusBarUpdateParams
	if err := decodeStrictJSON(raw, &params, true); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid statusBar update params"}
	}
	if !statusBarDynamicItem(runtimeHost, params.ID) {
		return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("status bar item %q is not declared with dynamic: true", params.ID)}
	}
	if params.Text != nil {
		if err := validateStatusBarText(*params.Text); err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
	}
	if params.Tooltip != nil {
		if err := validateStatusBarTooltip(*params.Tooltip); err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
	}
	if params.Severity != nil {
		if _, ok := statusBarSeverities[*params.Severity]; !ok {
			return nil, &rpcError{Code: -32602, Message: "severity must be normal, warning or error"}
		}
	}
	if runtimeStopping(runtimeHost) {
		return nil, &rpcError{Code: -32001, Message: "plugin runtime is stopping"}
	}
	generation := runtimeHost.Generation()
	now := statusBarNow()
	statusBarMu.Lock()
	state := statusBarStateLocked(runtimeHost, generation)
	if !admitStatusBarUpdateLocked(state, now) {
		// Silently dropped: rate-limited updates are not an error.
		statusBarMu.Unlock()
		return true, nil
	}
	item := state.items[params.ID]
	if params.Text != nil {
		text := *params.Text
		item.Text = &text
	}
	if params.Tooltip != nil {
		tooltip := *params.Tooltip
		item.Tooltip = &tooltip
	}
	if params.Severity != nil {
		item.Severity = *params.Severity
	}
	if params.Busy != nil {
		busy := *params.Busy
		item.Busy = &busy
	}
	if params.Visible != nil {
		visible := *params.Visible
		item.Visible = &visible
	}
	state.items[params.ID] = item
	emitNow := scheduleStatusBarEmitLocked(runtimeHost, state, now)
	pluginID := state.pluginID
	statusBarMu.Unlock()
	if emitNow {
		statusBarNotify(pluginID)
	}
	return true, nil
}

func handleStatusBarReset(runtimeHost *processRuntime, raw json.RawMessage) (any, *rpcError) {
	var params struct {
		ID string `json:"id"`
	}
	if len(strings.TrimSpace(string(raw))) > 0 && strings.TrimSpace(string(raw)) != "null" {
		if err := decodeStrictJSON(raw, &params, true); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid statusBar reset params"}
		}
	}
	if params.ID != "" && !statusBarDynamicItem(runtimeHost, params.ID) {
		return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("status bar item %q is not declared with dynamic: true", params.ID)}
	}
	now := statusBarNow()
	statusBarMu.Lock()
	state := statusBarStates[runtimeHost]
	if state == nil {
		statusBarMu.Unlock()
		return true, nil
	}
	changed := false
	if params.ID == "" {
		changed = len(state.items) > 0
		state.items = map[string]StatusBarRuntimeItem{}
	} else if _, exists := state.items[params.ID]; exists {
		delete(state.items, params.ID)
		changed = true
	}
	emitNow := changed && scheduleStatusBarEmitLocked(runtimeHost, state, now)
	pluginID := state.pluginID
	statusBarMu.Unlock()
	if emitNow {
		statusBarNotify(pluginID)
	}
	return true, nil
}

// statusBarRuntimeOverlay is called with record.mu held. Lock order:
// record.mu -> runtimeHost.mu (Generation) -> statusBarMu.
func statusBarRuntimeOverlay(record *pluginRecord) any {
	runtimeHost := record.runtime
	if runtimeHost == nil || record.state != pluginStateRunning {
		return nil
	}
	generation := runtimeHost.Generation()
	statusBarMu.Lock()
	defer statusBarMu.Unlock()
	state := statusBarStates[runtimeHost]
	if state == nil || state.generation != generation || len(state.items) == 0 {
		return nil
	}
	items := make(map[string]StatusBarRuntimeItem, len(state.items))
	for id, item := range state.items {
		items[id] = item
	}
	return StatusBarRuntime{Items: items}
}

// clearStatusBarRuntime drops dynamic state whose runtime is no longer the
// record's running runtime. Runs without record.mu held.
func clearStatusBarRuntime(_ *pluginManager, record *pluginRecord) {
	record.mu.Lock()
	current := record.runtime
	running := record.state == pluginStateRunning
	pluginID := record.id
	record.mu.Unlock()
	statusBarMu.Lock()
	cleared := false
	for runtimeHost, state := range statusBarStates {
		if state.pluginID != pluginID || (running && runtimeHost == current) {
			continue
		}
		stopStatusBarTimer(state)
		delete(statusBarStates, runtimeHost)
		cleared = cleared || len(state.items) > 0
	}
	statusBarMu.Unlock()
	if cleared {
		statusBarNotify(pluginID)
	}
}
