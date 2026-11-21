package plugin

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/backend/desktop"
)

// System tray plugin section (contributes.menus "tray", host 0.3.0).
//
// The frontend (vue-app services/plugin-tray.ts) decides which declared
// (plugin, command) pairs are visible now (language, workspace, command
// when, app lock) and sends only references through plugin_host_tray_set.
// This file checks every reference against the installed manifests (plugin
// enabled, listed and effective in the active workspace, command in its
// menus.tray), takes the plugin name and command titles from the manifest
// (the WebView supplies no text), sorts, sanitizes and bounds them, and
// hands the groups to desktop.SetPluginTray. The native menu is rebuilt in
// app/tray_menu.go; a click navigates to a plugin-command target that
// the frontend runs through its shared command path.
//
//   - While the app is locked an empty set is applied (fail closed), and an
//     app lock change re-applies the remembered references.
//   - A plugin that is disabled, uninstalled or failed loses its group at
//     once through the record lifecycle hook, without waiting for the
//     frontend.
//   - Only the number of dropped references is logged, never titles.

const (
	trayCommandName = "plugin_host_tray_set"
	// trayCandidatePluginsMax bounds the plugins one sync request may name
	// (the other host-wide plugin lists use the same 512).
	trayCandidatePluginsMax = 512
	// trayLocalesMax bounds the language chain of one sync request.
	trayLocalesMax = 8
)

var (
	// trayPluginsMax plugins get a submenu; the rest are omitted.
	trayPluginsMax = int(menuSpec.Limit("trayPluginsMax"))
	// trayLabelMaxRunes bounds plugin names and command titles.
	trayLabelMaxRunes = int(menuSpec.Limit("trayLabelMaxRunes"))
	// trayItemsMax bounds one sync request. The frontend sends every visible
	// candidate (not only trayPluginsMax plugins' worth) so resolveTray picks
	// the first plugins by name; mirrored by TRAY_ITEMS_MAX in
	// vue-app/src/domain/plugins/tray.ts.
	trayItemsMax = trayCandidatePluginsMax * menuTrayMaxPerPlugin
)

// Seams replaced by tests.
var (
	trayApply     = desktop.SetPluginTray
	trayAppLocked = appLocked
	trayManager   = host
)

type trayReference struct {
	PluginID string `json:"pluginId"`
	Command  string `json:"command"`
}

type traySetArgs struct {
	Locale []string        `json:"locale"`
	Items  []trayReference `json:"items"`
}

type trayResult struct {
	Shown   int `json:"shown"`
	Dropped int `json:"dropped"`
	Omitted int `json:"omitted"`
}

// trayState remembers the last accepted request and the groups applied for
// it. mu also serializes every trayApply call.
var trayState struct {
	mu      sync.Mutex
	known   bool
	locale  []string
	refs    []trayReference
	applied []desktop.PluginTrayGroup
}

func resetTrayState() {
	trayState.mu.Lock()
	trayState.known, trayState.locale, trayState.refs, trayState.applied = false, nil, nil, nil
	trayState.mu.Unlock()
}

func init() {
	core.Register(trayCommandName, pluginHostTraySetHandler)
	registerRecordLifecycleHook(trayRecordLifecycle)
}

func pluginHostTraySetHandler(_, argsJSON string) (string, error) {
	if strings.TrimSpace(argsJSON) == "" {
		argsJSON = "{}"
	}
	var args traySetArgs
	if err := decodeStrictJSONValue([]byte(argsJSON), &args, true, "tray request"); err != nil {
		return "", fmt.Errorf("invalid tray request: %w", err)
	}
	if len(args.Items) > trayItemsMax {
		return "", fmt.Errorf("invalid tray request: at most %d items", trayItemsMax)
	}
	if len(args.Locale) > trayLocalesMax {
		return "", fmt.Errorf("invalid tray request: at most %d locales", trayLocalesMax)
	}
	for _, locale := range args.Locale {
		if len(locale) > 35 {
			return "", errors.New("invalid tray request: locale is too long")
		}
	}
	trayState.mu.Lock()
	defer trayState.mu.Unlock()
	trayState.known = true
	trayState.locale = append([]string(nil), args.Locale...)
	trayState.refs = append([]trayReference(nil), args.Items...)
	var groups []desktop.PluginTrayGroup
	result := trayResult{}
	if !trayAppLocked() {
		groups, result.Dropped, result.Omitted = trayManager().resolveTray(args.Locale, args.Items)
		result.Shown = trayItemCount(groups)
	}
	if result.Dropped > 0 {
		log.Printf("plugin tray: dropped %d undeclared or inactive references", result.Dropped)
	}
	if err := trayApplyLocked(groups); err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

// trayApplyLocked applies groups and remembers them. Caller holds
// trayState.mu.
func trayApplyLocked(groups []desktop.PluginTrayGroup) error {
	if len(groups) == 0 {
		groups = nil
	}
	if err := trayApply(groups); err != nil {
		return fmt.Errorf("apply plugin tray: %w", err)
	}
	trayState.applied = groups
	return nil
}

func trayItemCount(groups []desktop.PluginTrayGroup) int {
	count := 0
	for _, group := range groups {
		for _, item := range group.Items {
			if !item.Separator {
				count++
			}
		}
	}
	return count
}

// trayAppLockChanged empties the tray on lock and re-resolves the remembered
// references on unlock (the frontend syncs again as well).
func trayAppLockChanged() {
	trayState.mu.Lock()
	defer trayState.mu.Unlock()
	if !trayState.known {
		return
	}
	var groups []desktop.PluginTrayGroup
	if !trayAppLocked() && len(trayState.refs) > 0 {
		groups, _, _ = trayManager().resolveTray(trayState.locale, trayState.refs)
	}
	if err := trayApplyLocked(groups); err != nil {
		log.Printf("plugin tray: %v", err)
	}
}

// trayRecordLifecycle removes a plugin's group once the plugin no longer
// qualifies: disabled, uninstalled (enabled is cleared before the hooks run),
// crashed or force-stopped into the error state, or no longer effective in
// the active workspace. A normal stop (idle stop, reload) keeps it: the
// running state does not matter, and a click activates the plugin again
// through the frontend command path. Development sessions that go away call
// trayForgetPlugin themselves (detachDevelopmentSession). Only record.mu is
// taken: callers may hold other plugin locks.
func trayRecordLifecycle(manager *pluginManager, record *pluginRecord) {
	record.mu.Lock()
	eligible := trayRecordEligibleLocked(manager, record)
	pluginID := record.id
	record.mu.Unlock()
	if !eligible {
		trayForgetPlugin(pluginID)
	}
}

// trayForgetPlugin drops a plugin's remembered references and, when its
// group is shown, rebuilds the tray without it.
func trayForgetPlugin(pluginID string) {
	trayState.mu.Lock()
	defer trayState.mu.Unlock()
	kept := make([]trayReference, 0, len(trayState.refs))
	for _, ref := range trayState.refs {
		if ref.PluginID != pluginID {
			kept = append(kept, ref)
		}
	}
	trayState.refs = kept
	present := false
	groups := make([]desktop.PluginTrayGroup, 0, len(trayState.applied))
	for _, group := range trayState.applied {
		if group.PluginID == pluginID {
			present = true
			continue
		}
		groups = append(groups, group)
	}
	if !present {
		return
	}
	if err := trayApplyLocked(groups); err != nil {
		log.Printf("plugin tray: %v", err)
	}
}

// trayRecordEligibleLocked: an installed, enabled, valid process plugin that
// has not failed and is effective in the active workspace, whatever its
// running state (stopped, starting, running, stopping). Caller holds
// record.mu.
func trayRecordEligibleLocked(manager *pluginManager, record *pluginRecord) bool {
	return record.builtin == nil && record.manifest != nil && !record.manifest.isDeclarative() &&
		record.runtime != nil && record.enabled && record.state != pluginStateError &&
		record.state != pluginStateDisabled && manager.workspaceActiveLocked(record)
}

type trayEntry struct {
	item  MenuItemContribution
	index int
}

type trayCandidate struct {
	group   desktop.PluginTrayGroup
	sortKey string
}

// resolveTray turns references into groups: references to unknown,
// disabled or inactive plugins and to commands outside the plugin's
// menus.tray (and repeats) are dropped; plugins beyond trayPluginsMax are
// omitted. Plugins sort by lower-cased name then ID; items by (group, order,
// manifest order) with a separator between groups.
func (manager *pluginManager) resolveTray(locale []string, refs []trayReference) (groups []desktop.PluginTrayGroup, dropped, omitted int) {
	wanted := map[string]map[string]bool{}
	var pluginOrder []string
	for _, ref := range refs {
		commands := wanted[ref.PluginID]
		if commands == nil {
			commands = map[string]bool{}
			wanted[ref.PluginID] = commands
			pluginOrder = append(pluginOrder, ref.PluginID)
		}
		if commands[ref.Command] {
			dropped++ // repeated reference
			continue
		}
		commands[ref.Command] = true
	}
	candidates := make([]trayCandidate, 0, len(pluginOrder))
	for _, pluginID := range pluginOrder {
		commands := wanted[pluginID]
		group, matched, ok := manager.trayGroup(pluginID, commands, locale)
		dropped += len(commands) - matched
		if ok {
			candidates = append(candidates, trayCandidate{group: group, sortKey: strings.ToLower(group.Name)})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].sortKey != candidates[j].sortKey {
			return candidates[i].sortKey < candidates[j].sortKey
		}
		return candidates[i].group.PluginID < candidates[j].group.PluginID
	})
	if len(candidates) > trayPluginsMax {
		omitted = len(candidates) - trayPluginsMax
		candidates = candidates[:trayPluginsMax]
	}
	for _, candidate := range candidates {
		groups = append(groups, candidate.group)
	}
	return groups, dropped, omitted
}

// trayGroup builds one plugin's group from the commands it was asked for;
// matched counts the commands found in its menus.tray.
func (manager *pluginManager) trayGroup(pluginID string, commands map[string]bool, locale []string) (desktop.PluginTrayGroup, int, bool) {
	manager.mu.RLock()
	record := manager.records[pluginID]
	manager.mu.RUnlock()
	if record == nil {
		return desktop.PluginTrayGroup{}, 0, false
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if !trayRecordEligibleLocked(manager, record) {
		return desktop.PluginTrayGroup{}, 0, false
	}
	manifest := record.manifest
	var entries []trayEntry
	for _, item := range manifest.MenuItems() {
		if item.Menu != menuTray {
			continue
		}
		if commands[item.Command] {
			entries = append(entries, trayEntry{item: item, index: len(entries)})
		}
	}
	if len(entries) == 0 {
		return desktop.PluginTrayGroup{}, 0, false
	}
	if len(entries) > menuTrayMaxPerPlugin {
		entries = entries[:menuTrayMaxPerPlugin]
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i].item, entries[j].item
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return entries[i].index < entries[j].index
	})
	titles := map[string]ManifestCommand{}
	for _, command := range manifest.Contributes.Commands {
		titles[command.ID] = command
	}
	group := desktop.PluginTrayGroup{PluginID: record.id, Name: trayPluginName(manifest.Name, record.id)}
	for index, entry := range entries {
		if index > 0 && entry.item.Group != entries[index-1].item.Group {
			group.Items = append(group.Items, desktop.PluginTrayItem{Separator: true})
		}
		title, localized := entry.item.Title, entry.item.TitleLocalized
		if command, ok := titles[entry.item.Command]; ok {
			title, localized = command.Title, command.TitleLocalized
		}
		group.Items = append(group.Items, desktop.PluginTrayItem{
			CommandID: entry.item.Command,
			Title:     trayLabelText(trayLocalized(title, localized, locale), entry.item.Command),
		})
	}
	return group, len(entries), true
}

// trayLocalized picks the first language of the chain the manifest
// translates (exact key, then case-insensitive), else the default title.
func trayLocalized(title string, localized map[string]string, locale []string) string {
	for _, language := range locale {
		language = strings.TrimSpace(language)
		if language == "" {
			continue
		}
		if value, ok := localized[language]; ok && strings.TrimSpace(value) != "" {
			return value
		}
		for key, value := range localized {
			if strings.EqualFold(key, language) && strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return title
}

// trayLabelText makes manifest text a single visible line: unsafe characters
// (controls, line breaks, bidi and zero-width), other non-graphic characters
// and blank glyphs (U+2800 and the like) are removed, and the result is cut
// to trayLabelMaxRunes with "…". A result without any visible character
// falls back.
func trayLabelText(value, fallback string) string {
	value = strings.TrimSpace(trayVisibleText(value))
	if value == "" {
		value = fallback
	}
	if utf8.RuneCountInString(value) > trayLabelMaxRunes {
		value = string([]rune(value)[:trayLabelMaxRunes-1]) + "…"
	}
	return value
}

// trayVisibleText strips characters that do not render visibly; it returns
// "" when nothing but spaces is left.
func trayVisibleText(value string) string {
	value = strings.Map(func(r rune) rune {
		if isUnsafeContributionRune(r, false, false) || trayBlankGlyph(r) || (!unicode.IsGraphic(r) && r != ' ') {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, ""))
	if strings.IndexFunc(value, func(r rune) bool { return !unicode.IsSpace(r) }) < 0 {
		return ""
	}
	return value
}

// trayBlankGlyph reports graphic characters that render as nothing (Braille
// blank, Hangul fillers, invisible operators, ...).
func trayBlankGlyph(r rune) bool {
	switch r {
	case 0x2800, 0x3164, 0x115f, 0x1160, 0xffa0, 0x2062, 0x2063, 0x2064, 0x034f, 0x180e, 0x17b4, 0x17b5:
		return true
	}
	return false
}

// trayReservedNames are MinCode's own tray labels (in every UI language) and
// its name, compared lower-cased with spaces collapsed: a plugin submenu never
// reads like a host item.
var trayReservedNames = map[string]bool{
	"显示主窗口": true, "隐藏主窗口": true, "退出": true,
	"顯示主視窗": true, "隱藏主視窗": true, "結束": true,
	"show main window": true, "hide main window": true, "quit": true, "exit": true,
	"show": true, "hide": true, "quit mincode": true, "mincode": true,
}

// trayPluginName is the plugin's submenu label: the manifest name; the
// name followed by the plugin ID in parentheses when the name is a host
// label; the plugin ID when the name has no visible character.
func trayPluginName(name, pluginID string) string {
	visible := strings.Join(strings.Fields(trayVisibleText(name)), " ")
	if visible == "" {
		return trayLabelText(pluginID, pluginID)
	}
	if trayReservedNames[strings.ToLower(visible)] {
		return trayLabelText(visible+" ("+pluginID+")", pluginID)
	}
	return trayLabelText(visible, pluginID)
}
