package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Global shortcuts (contributes.globalShortcuts) let a process plugin declare
// system-wide keys that open one of its tab views, toggle one of its floating
// views, or run one of its UI-callable actions while MinCode is in the
// background. Entries are off by default: this file only validates the
// manifest; the registry that registers enabled keys with the OS lives
// elsewhere. Mirrors: tools/mincode-cli/global_shortcuts.go (CLI) and the
// cloud marketplace (MarketplaceArtifact).
const (
	globalShortcutPointKey        = "globalShortcuts"
	globalShortcutIDPatternSource = "^[a-z0-9-]{1,32}$"
	// globalShortcutTriggerKey is the payload key the host adds on every
	// trigger ({"trigger": {"type": "globalShortcut", "id": ...}}).
	globalShortcutTriggerKey = "trigger"
)

var (
	globalShortcutSpec             = mustContributionSpec(globalShortcutPointKey)
	globalShortcutTitleMaxRunes    = int(globalShortcutSpec.Limit("titleMaxRunes"))
	globalShortcutLocalesMax       = int(globalShortcutSpec.Limit("localesMax"))
	globalShortcutKeyMaxLength     = int(globalShortcutSpec.Limit("keyMaxLength"))
	globalShortcutPayloadMaxBytes  = int(globalShortcutSpec.Limit("payloadMaxBytes"))
	globalShortcutTriggerReserve   = int(globalShortcutSpec.Limit("triggerReserveBytes"))
	globalShortcutRunTypes         = globalShortcutSpec.List("runTypes")
	globalShortcutIDPattern        = regexp.MustCompile(globalShortcutIDPatternSource)
	reservedAccelerators           = globalShortcutSpec.List("reservedAccelerators")
	reservedAcceleratorsByPlatform = mustReservedAcceleratorTable(reservedAccelerators)
	// reservedDefaultAccelerators are application-standard combinations (the
	// primary modifier plus C, V, X, Z, A, S, W, Q, F, N, T, O or P) that a
	// manifest may not use as its default key; a user may still choose one in
	// the settings.
	reservedDefaultAccelerators           = globalShortcutSpec.List("reservedDefaultAccelerators")
	reservedDefaultAcceleratorsByPlatform = mustReservedAcceleratorTable(reservedDefaultAccelerators)
)

// GlobalShortcutRun is what a trigger does.
type GlobalShortcutRun struct {
	// Type is "view", "action" or "floating".
	Type   string `json:"type"`
	View   string `json:"view,omitempty"`
	Action string `json:"action,omitempty"`
	// Payload is the compact fixed JSON object sent with an action (nil when absent).
	Payload json.RawMessage `json:"payload,omitempty"`
}

// GlobalShortcutEntry is one declared shortcut and the snapshot entry.
// Key is the plugin's default accelerator as written in the manifest.
type GlobalShortcutEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Key            string            `json:"key"`
	Run            GlobalShortcutRun `json:"run"`
}

type globalShortcutManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Key            string            `json:"key"`
	Run            *struct {
		Type    string          `json:"type"`
		View    string          `json:"view"`
		Action  string          `json:"action"`
		Payload json.RawMessage `json:"payload"`
	} `json:"run"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              globalShortcutPointKey,
		Decode:           decodeGlobalShortcutEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate global shortcut id %q", id) },
		IssuePrefix:      "invalid globalShortcuts contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped global shortcut %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.globalShortcuts[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "global shortcuts",
		ExpandsPermission: globalShortcutsExpandPermission,
	})
}

func decodeGlobalShortcutEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry globalShortcutManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse global shortcut: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !globalShortcutIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("global shortcut id %q must match %s", id, globalShortcutIDPatternSource)
	}
	if ctx.manifest.Runtime.Type != "process" {
		return nil, id, fmt.Errorf("global shortcut %q requires runtime.type process", id)
	}
	shortcut := GlobalShortcutEntry{ID: id, Key: strings.TrimSpace(entry.Key)}
	var err error
	if shortcut.Title, err = validateContributionLine(entry.Title, globalShortcutTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("global shortcut %q title %w", id, err)
	}
	if shortcut.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, globalShortcutTitleMaxRunes, globalShortcutLocalesMax); err != nil {
		return nil, id, fmt.Errorf("global shortcut %q titleLocalized %w", id, err)
	}
	if err := validDefaultAccelerator(shortcut.Key); err != nil {
		return nil, id, fmt.Errorf("global shortcut %q key %w", id, err)
	}
	if entry.Run == nil {
		return nil, id, fmt.Errorf("global shortcut %q needs run", id)
	}
	run := GlobalShortcutRun{Type: strings.TrimSpace(entry.Run.Type), View: strings.TrimSpace(entry.Run.View), Action: strings.TrimSpace(entry.Run.Action)}
	payload := bytes.TrimSpace(entry.Run.Payload)
	hasPayload := len(payload) > 0 && !bytes.Equal(payload, []byte("null"))
	if !slices.Contains(globalShortcutRunTypes, run.Type) {
		return nil, id, fmt.Errorf("global shortcut %q run.type must be one of %s", id, strings.Join(globalShortcutRunTypes, ", "))
	}
	switch run.Type {
	case "view", "floating":
		if run.Action != "" || hasPayload {
			return nil, id, fmt.Errorf("global shortcut %q run.type %s must not set action or payload", id, run.Type)
		}
		// ctx.views leaves out floating views, which this point may target.
		index := slices.IndexFunc(ctx.manifest.Contributes.Views, func(view ManifestView) bool { return view.ID == run.View })
		if index < 0 {
			return nil, id, fmt.Errorf("global shortcut %q view %q is not one of this plugin's contributes.views", id, run.View)
		}
		view := ctx.manifest.Contributes.Views[index]
		if run.Type == "view" && view.Floating != nil {
			return nil, id, fmt.Errorf("global shortcut %q view %q is a floating view; use run.type floating", id, run.View)
		}
		if run.Type == "floating" && view.Floating == nil {
			return nil, id, fmt.Errorf("global shortcut %q run.type floating needs a view that declares floating (%q does not)", id, run.View)
		}
	case "action":
		if run.View != "" {
			return nil, id, fmt.Errorf("global shortcut %q run.type action must not set view", id)
		}
		if hasPayload {
			if run.Payload, err = compactGlobalShortcutPayload(payload); err != nil {
				return nil, id, fmt.Errorf("global shortcut %q %w", id, err)
			}
		}
		action, ok := ctx.actions[run.Action]
		if !ok {
			return nil, id, fmt.Errorf("global shortcut %q action %q is not one of this plugin's contributes.actions", id, run.Action)
		}
		if action.Risk == "dangerous" {
			return nil, id, fmt.Errorf("global shortcut %q action %q must not have risk dangerous", id, run.Action)
		}
		if !action.UICallable {
			return nil, id, fmt.Errorf("global shortcut %q action %q must be uiCallable", id, run.Action)
		}
		if need := len(run.Payload) + globalShortcutTriggerReserve; action.MaxPayloadBytes < need {
			return nil, id, fmt.Errorf("global shortcut %q action %q needs maxPayloadBytes of at least %d (payload plus trigger)", id, run.Action, need)
		}
	}
	shortcut.Run = run
	return shortcut, id, nil
}

// compactGlobalShortcutPayload checks the fixed action payload: a JSON
// object without the host-reserved trigger key, compact size within the limit.
func compactGlobalShortcutPayload(payload []byte) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if payload[0] != '{' || json.Unmarshal(payload, &object) != nil {
		return nil, errors.New("run.payload must be a JSON object")
	}
	if _, reserved := object[globalShortcutTriggerKey]; reserved {
		return nil, fmt.Errorf("run.payload must not contain the host-reserved %q key", globalShortcutTriggerKey)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		return nil, errors.New("run.payload must be a JSON object")
	}
	if compact.Len() > globalShortcutPayloadMaxBytes {
		return nil, fmt.Errorf("run.payload must be at most %d bytes", globalShortcutPayloadMaxBytes)
	}
	return json.RawMessage(compact.Bytes()), nil
}

// parseGlobalShortcuts returns the plugin's validated global shortcuts. Install
// (strict) inspection has already failed on any invalid entry; for a leniently
// loaded package the valid entries are returned together with an error that
// names the skipped ones.
func parseGlobalShortcuts(manifest *Manifest) ([]GlobalShortcutEntry, error) {
	if manifest == nil {
		return nil, nil
	}
	resolved := manifest.contribution(globalShortcutPointKey)
	if resolved == nil {
		return nil, nil
	}
	out := make([]GlobalShortcutEntry, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if shortcut, ok := entry.(GlobalShortcutEntry); ok {
			out = append(out, shortcut)
		}
	}
	if len(resolved.Issues) > 0 {
		messages := make([]string, 0, len(resolved.Issues))
		for _, issue := range resolved.Issues {
			messages = append(messages, issue.Message)
		}
		return out, fmt.Errorf("invalid globalShortcuts contribution: %s", strings.Join(messages, "; "))
	}
	return out, nil
}

// globalShortcutsExpandPermission: entries are off by default, so a new entry
// needs no approval, but once the user turned one on a development generation
// must not silently rebind it to another key or make it do something else.
func globalShortcutsExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil || current == nil {
		return false
	}
	previous := map[string]GlobalShortcutEntry{}
	for _, entry := range current.Entries {
		if shortcut, ok := entry.(GlobalShortcutEntry); ok {
			previous[shortcut.ID] = shortcut
		}
	}
	for _, entry := range candidate.Entries {
		shortcut, ok := entry.(GlobalShortcutEntry)
		if !ok {
			continue
		}
		old, exists := previous[shortcut.ID]
		if !exists {
			continue
		}
		if old.Key != shortcut.Key || old.Run.Type != shortcut.Run.Type || old.Run.View != shortcut.Run.View ||
			old.Run.Action != shortcut.Run.Action || !bytes.Equal(old.Run.Payload, shortcut.Run.Payload) {
			return true
		}
	}
	return false
}

// ---- Accelerators ----
//
// Manifests use accelerator syntax, which is platform-neutral: the same
// string means different physical keys per OS. The host maps CmdOrCtrl (and its
// aliases Cmd/Command) to Command on macOS and Control elsewhere, Super to
// Command on macOS and the Windows/Super key elsewhere, and OptionOrAlt
// (Option/Alt) to Option/Alt. Validation therefore resolves a key for every
// desktop platform and rejects it when any of them is reserved.

type acceleratorModifier int

const (
	modifierCmdOrCtrl acceleratorModifier = iota
	modifierCtrl
	modifierAlt
	modifierShift
	modifierSuper
)

// acceleratorModifierNames are the accelerator modifier spellings (case-insensitive).
var acceleratorModifierNames = map[string]acceleratorModifier{
	"cmdorctrl": modifierCmdOrCtrl, "cmd": modifierCmdOrCtrl, "command": modifierCmdOrCtrl,
	"ctrl":        modifierCtrl,
	"optionoralt": modifierAlt, "alt": modifierAlt, "option": modifierAlt,
	"shift": modifierShift,
	"super": modifierSuper,
}

// acceleratorNamedKeys are the accelerator named keys.
var acceleratorNamedKeys = func() map[string]bool {
	names := map[string]bool{
		"backspace": true, "tab": true, "return": true, "enter": true, "escape": true,
		"left": true, "right": true, "up": true, "down": true, "space": true, "delete": true,
		"forward delete": true, "home": true, "end": true, "page up": true, "page down": true, "numlock": true,
	}
	for index := 1; index <= 35; index++ {
		names[fmt.Sprintf("f%d", index)] = true
	}
	return names
}()

// acceleratorPlatforms are the desktop platforms a key is resolved for.
var acceleratorPlatforms = []string{"mac", "windows", "linux"}

// Physical modifier names in canonical order.
var physicalModifierOrder = []string{"Ctrl", "Alt", "Shift", "Cmd", "Win"}

type parsedAccelerator struct {
	modifiers []acceleratorModifier
	key       string // lower case; "+" for Plus
}

// ValidAccelerator checks a accelerator for a global shortcut: valid
// syntax, at least one modifier other than Shift, and not a reserved system
// shortcut on any desktop platform.
func ValidAccelerator(key string) error {
	parsed, err := parseGlobalAccelerator(key)
	if err != nil {
		return err
	}
	nonShift := false
	for _, modifier := range parsed.modifiers {
		nonShift = nonShift || modifier != modifierShift
	}
	if !nonShift {
		return fmt.Errorf("%q needs at least one modifier other than Shift", key)
	}
	for _, platform := range acceleratorPlatforms {
		chord := parsed.physical(platform)
		if reservedAcceleratorsByPlatform[platform][chord] {
			return fmt.Errorf("%q is a reserved system shortcut (%s on %s)", key, chord, platform)
		}
	}
	return nil
}

// validDefaultAccelerator checks a manifest's default key: ValidAccelerator,
// and not an application-standard combination (lists.reservedDefaultAccelerators)
// on any desktop platform. Once turned on, a global shortcut on such a combo
// would take it away from every application.
func validDefaultAccelerator(key string) error {
	if err := ValidAccelerator(key); err != nil {
		return err
	}
	parsed, _ := parseGlobalAccelerator(key)
	for _, platform := range acceleratorPlatforms {
		chord := parsed.physical(platform)
		if reservedDefaultAcceleratorsByPlatform[platform][chord] {
			return fmt.Errorf("%q is an application-standard shortcut and cannot be a default key (%s on %s)", key, chord, platform)
		}
	}
	return nil
}

func parseGlobalAccelerator(key string) (parsedAccelerator, error) {
	invalid := func(reason string) (parsedAccelerator, error) {
		return parsedAccelerator{}, fmt.Errorf("%q is not a valid accelerator: %s", key, reason)
	}
	if key == "" {
		return invalid("empty")
	}
	if len(key) > globalShortcutKeyMaxLength {
		return invalid(fmt.Sprintf("longer than %d characters", globalShortcutKeyMaxLength))
	}
	if strings.TrimSpace(key) != key {
		return invalid("leading or trailing space")
	}
	parts := strings.Split(key, "+")
	var parsed parsedAccelerator
	seen := map[acceleratorModifier]bool{}
	for _, part := range parts[:len(parts)-1] {
		modifier, ok := acceleratorModifierNames[strings.ToLower(part)]
		if !ok {
			return invalid(fmt.Sprintf("%q is not a modifier (CmdOrCtrl, Cmd, Command, Ctrl, OptionOrAlt, Option, Alt, Shift, Super)", part))
		}
		if seen[modifier] {
			return parsedAccelerator{}, fmt.Errorf("%q repeats modifier %q", key, part)
		}
		seen[modifier] = true
		parsed.modifiers = append(parsed.modifiers, modifier)
	}
	last := strings.ToLower(parts[len(parts)-1])
	switch {
	case last == "plus":
		parsed.key = "+"
	case acceleratorNamedKeys[last]:
		parsed.key = last
	case len(last) == 1 && last[0] > ' ' && last[0] < 0x7f:
		parsed.key = last
	default:
		return invalid(fmt.Sprintf("%q is not a key (one printable ASCII character, Plus, or a named key such as F1, Space, Delete, Page Up)", parts[len(parts)-1]))
	}
	return parsed, nil
}

// physical returns the canonical physical chord ("Ctrl+Alt+Delete") on platform.
func (parsed parsedAccelerator) physical(platform string) string {
	names := map[string]bool{}
	for _, modifier := range parsed.modifiers {
		switch modifier {
		case modifierCmdOrCtrl:
			if platform == "mac" {
				names["Cmd"] = true
			} else {
				names["Ctrl"] = true
			}
		case modifierSuper:
			if platform == "mac" {
				names["Cmd"] = true
			} else {
				names["Win"] = true
			}
		case modifierCtrl:
			names["Ctrl"] = true
		case modifierAlt:
			names["Alt"] = true
		case modifierShift:
			names["Shift"] = true
		}
	}
	parts := make([]string, 0, len(names)+1)
	for _, name := range physicalModifierOrder {
		if names[name] {
			parts = append(parts, name)
		}
	}
	return strings.Join(append(parts, parsed.key), "+")
}

// parseReservedAccelerator reads one contract lists.reservedAccelerators entry,
// "<mac|windows|linux|any>:<physical chord>", where modifiers are the physical
// keys Cmd, Ctrl, Alt/Option, Shift, Win/Super and Del is an alias of Delete
// (accelerators have no Del key name). It returns the platforms and canonical chord.
func parseReservedAccelerator(entry string) ([]string, string, error) {
	platform, chord, ok := strings.Cut(entry, ":")
	if !ok || chord == "" {
		return nil, "", fmt.Errorf("reserved accelerator %q must be <platform>:<chord>", entry)
	}
	var platforms []string
	switch platform {
	case "any":
		platforms = acceleratorPlatforms
	case "mac", "windows", "linux":
		platforms = []string{platform}
	default:
		return nil, "", fmt.Errorf("reserved accelerator %q has unknown platform %q", entry, platform)
	}
	physical := map[string]string{"cmd": "Cmd", "ctrl": "Ctrl", "alt": "Alt", "option": "Alt", "shift": "Shift", "win": "Win", "super": "Win"}
	parts := strings.Split(chord, "+")
	names := map[string]bool{}
	for _, part := range parts[:len(parts)-1] {
		name, ok := physical[strings.ToLower(part)]
		if !ok || names[name] {
			return nil, "", fmt.Errorf("reserved accelerator %q has invalid modifier %q", entry, part)
		}
		names[name] = true
	}
	key := strings.ToLower(parts[len(parts)-1])
	if key == "del" {
		key = "delete"
	}
	if !acceleratorNamedKeys[key] && (len(key) != 1 || key[0] <= ' ' || key[0] >= 0x7f) {
		return nil, "", fmt.Errorf("reserved accelerator %q has invalid key %q", entry, parts[len(parts)-1])
	}
	canonical := make([]string, 0, len(names)+1)
	for _, name := range physicalModifierOrder {
		if names[name] {
			canonical = append(canonical, name)
		}
	}
	return platforms, strings.Join(append(canonical, key), "+"), nil
}

func mustReservedAcceleratorTable(entries []string) map[string]map[string]bool {
	table := map[string]map[string]bool{}
	for _, platform := range acceleratorPlatforms {
		table[platform] = map[string]bool{}
	}
	for _, entry := range entries {
		platforms, chord, err := parseReservedAccelerator(entry)
		if err != nil {
			panic(err)
		}
		for _, platform := range platforms {
			table[platform][chord] = true
		}
	}
	return table
}
