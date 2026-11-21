package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"mincode/app/plugins/api"
)

// Declarative plugins contribute data only: no entry, no process, no UI, no
// capabilities. Keymap presets (contributes.keymaps) are a registered
// contribution point (contributions.go); their bounds come from
// plugins/api/contribution-points.json. The host validates presets
// structurally here; the frontend validator
// (vue-app/src/domain/keybindings/preset.ts) additionally checks the command
// catalog and OS-reserved keys before registering a preset.
const (
	pluginRuntimeProcess     = "process"
	pluginRuntimeDeclarative = "declarative"

	keymapPointKey      = "keymaps"
	keymapBasePresetID  = "mincode"
	keymapEditorModeVim = "vim"
)

var (
	keymapSpec               = mustContributionSpec(keymapPointKey)
	keymapMaxPresets         = keymapSpec.MaxEntries
	keymapMaxRules           = int(keymapSpec.Limit("maxRules"))
	keymapMaxPresetBytes     = int(keymapSpec.InlineMaxBytes)
	keymapMaxTitleRunes      = int(keymapSpec.Limit("titleMaxRunes"))
	keymapMaxDescriptionRune = int(keymapSpec.Limit("descriptionMaxRunes"))
	keymapMaxKeyLength       = int(keymapSpec.Limit("keyMaxLength"))
	keymapMaxWhenLength      = int(keymapSpec.Limit("whenMaxLength"))
	keymapMaxCommandLength   = int(keymapSpec.Limit("commandMaxLength"))
	keymapMaxExtendsDepth    = int(keymapSpec.Limit("extendsMaxDepth"))
)

func mustContributionSpec(key string) api.ContributionPointSpec {
	spec, ok := api.ContributionPointSpecFor(key)
	if !ok {
		panic(fmt.Sprintf("contribution point %q is missing from contribution-points.json", key))
	}
	return spec
}

var (
	keymapPresetIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	keymapCommandPattern  = regexp.MustCompile(`^-?(builtin|workbench|editor|terminal|project|ai|database|plugin):[A-Za-z0-9][A-Za-z0-9._:\-]*$`)
	keymapModifiers       = map[string]string{
		"mod": "mod", "cmdorctrl": "mod", "ctrl": "ctrl", "control": "ctrl", "shift": "shift",
		"alt": "alt", "option": "alt", "opt": "alt", "meta": "meta", "cmd": "meta",
		"command": "meta", "win": "meta", "super": "meta",
	}
	keymapNamedKeys = map[string]struct{}{
		"up": {}, "down": {}, "left": {}, "right": {}, "enter": {}, "return": {}, "escape": {}, "esc": {},
		"tab": {}, "space": {}, "backspace": {}, "delete": {}, "del": {}, "insert": {}, "home": {}, "end": {},
		"pageup": {}, "pagedown": {}, "/": {}, "\\": {}, ".": {}, ",": {}, ";": {}, "'": {}, "`": {},
		"[": {}, "]": {}, "-": {}, "=": {},
	}
	keymapFunctionKey = regexp.MustCompile(`^f([1-9]|1[0-9]|2[0-4])$`)
	keymapPlatformSet = map[string]struct{}{"mac": {}, "win": {}, "linux": {}}
)

// KeymapText is a zh/en pair (title or description).
type KeymapText struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// KeymapPlatforms accepts "mac" or ["mac", "win"] in JSON.
type KeymapPlatforms []string

func (platforms *KeymapPlatforms) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*platforms = KeymapPlatforms{single}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return errors.New("platform must be a string or a list of strings")
	}
	if len(list) == 0 {
		return errors.New("platform list must not be empty")
	}
	*platforms = KeymapPlatforms(list)
	return nil
}

func (platforms KeymapPlatforms) MarshalJSON() ([]byte, error) {
	if len(platforms) == 1 {
		return json.Marshal(platforms[0])
	}
	return json.Marshal([]string(platforms))
}

type KeymapRule struct {
	Key      string          `json:"key,omitempty"`
	Command  string          `json:"command"`
	When     string          `json:"when,omitempty"`
	Platform KeymapPlatforms `json:"platform,omitempty"`
}

// KeymapPreset mirrors the frontend KeymapPresetData shape.
type KeymapPreset struct {
	ID             string       `json:"id"`
	Title          KeymapText   `json:"title"`
	Description    *KeymapText  `json:"description,omitempty"`
	Extends        string       `json:"extends,omitempty"`
	DoubleModifier *bool        `json:"doubleModifier,omitempty"`
	EditorMode     string       `json:"editorMode,omitempty"`
	Rules          []KeymapRule `json:"rules"`
}

// KeymapIssue reports a skipped preset (or manifest entry) without failing the plugin load.
type KeymapIssue struct {
	Entry   int    `json:"entry"`
	Preset  string `json:"preset,omitempty"`
	Message string `json:"message"`
}

type keymapFileReference struct {
	Path string `json:"path"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              keymapPointKey,
		Decode:           decodeKeymapEntry,
		Finalize:         finalizeKeymapEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate keymap preset id %q", id) },
		IssuePrefix:      "invalid keymap contribution",
		IssueText:        contributionKeymapIssueText,
		EntryNoun:        "presets",
		FileLabels:       contributionFileLabels{Noun: "keymap", Entry: "keymap preset", ShapeHint: "a preset object"},
		DecodeReference: func(raw json.RawMessage) (string, error) {
			var reference keymapFileReference
			if err := decodeStrictJSON(raw, &reference, true); err != nil {
				return "", err
			}
			return reference.Path, nil
		},
	})
}

// decodeKeymapEntry parses and validates one preset. File references were
// already resolved by the framework. The id is returned even on failure so
// issues keep naming the preset.
func decodeKeymapEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var preset KeymapPreset
	if err := decodeStrictJSON(raw, &preset, true); err != nil {
		return nil, preset.ID, fmt.Errorf("parse keymap preset: %w", err)
	}
	if err := validateKeymapPreset(ctx.manifest.ID, &preset); err != nil {
		return nil, preset.ID, err
	}
	return preset, preset.ID, nil
}

func finalizeKeymapEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	presets := make([]KeymapPreset, 0, len(entries))
	byID := make(map[string]contributionEntry, len(entries))
	for _, entry := range entries {
		presets = append(presets, entry.Value.(KeymapPreset))
		byID[entry.ID] = entry
	}
	var pruneIssues []KeymapIssue
	presets, pruneIssues = pruneKeymapExtends(presets, nil)
	kept := make([]contributionEntry, 0, len(presets))
	for _, preset := range presets {
		kept = append(kept, byID[preset.ID])
	}
	for _, issue := range pruneIssues {
		issues = append(issues, ContributionIssue{Point: keymapPointKey, Entry: issue.Entry, ID: issue.Preset, Message: issue.Message})
	}
	return kept, issues
}

func validateKeymapText(text *KeymapText, field string, limit int) error {
	text.Zh = strings.TrimSpace(text.Zh)
	text.En = strings.TrimSpace(text.En)
	for _, value := range []string{text.Zh, text.En} {
		if value == "" || utf8.RuneCountInString(value) > limit {
			return fmt.Errorf("keymap %s needs zh and en text of 1-%d characters", field, limit)
		}
	}
	return nil
}

func validateKeymapPreset(pluginID string, preset *KeymapPreset) error {
	preset.ID = strings.TrimSpace(preset.ID)
	if !keymapPresetIDPattern.MatchString(preset.ID) {
		return fmt.Errorf("keymap preset id %q must match [a-z0-9][a-z0-9._-]{0,63}", preset.ID)
	}
	prefix := pluginID + "."
	if !strings.HasPrefix(preset.ID, prefix) || len(preset.ID) == len(prefix) {
		return fmt.Errorf("keymap preset id %q must start with %q", preset.ID, prefix)
	}
	if err := validateKeymapText(&preset.Title, "title", keymapMaxTitleRunes); err != nil {
		return err
	}
	if preset.Description != nil {
		if err := validateKeymapText(preset.Description, "description", keymapMaxDescriptionRune); err != nil {
			return err
		}
	}
	preset.Extends = strings.TrimSpace(preset.Extends)
	if preset.Extends != "" && preset.Extends != keymapBasePresetID &&
		(!strings.HasPrefix(preset.Extends, prefix) || preset.Extends == preset.ID) {
		return fmt.Errorf("keymap preset %q may only extend %q or another preset of the same plugin", preset.ID, keymapBasePresetID)
	}
	if preset.EditorMode != "" && preset.EditorMode != keymapEditorModeVim {
		return fmt.Errorf("keymap preset %q editorMode must be %q when present", preset.ID, keymapEditorModeVim)
	}
	if preset.Rules == nil {
		return fmt.Errorf("keymap preset %q must define rules", preset.ID)
	}
	if len(preset.Rules) > keymapMaxRules {
		return fmt.Errorf("keymap preset %q exceeds %d rules", preset.ID, keymapMaxRules)
	}
	for index := range preset.Rules {
		if err := validateKeymapRule(&preset.Rules[index]); err != nil {
			return fmt.Errorf("keymap preset %q rules[%d]: %w", preset.ID, index, err)
		}
	}
	encoded, err := json.Marshal(preset)
	if err != nil || len(encoded) > keymapMaxPresetBytes {
		return fmt.Errorf("keymap preset %q exceeds %d bytes", preset.ID, keymapMaxPresetBytes)
	}
	return nil
}

func validateKeymapRule(rule *KeymapRule) error {
	rule.Command = strings.TrimSpace(rule.Command)
	if len(rule.Command) > keymapMaxCommandLength || !keymapCommandPattern.MatchString(rule.Command) {
		return fmt.Errorf("command %q is not a bindable command id", rule.Command)
	}
	removal := strings.HasPrefix(rule.Command, "-")
	rule.Key = strings.TrimSpace(rule.Key)
	if rule.Key == "" {
		if !removal {
			return errors.New("key is required unless the rule removes a command")
		}
	} else if err := validateKeySequence(rule.Key); err != nil {
		return err
	}
	rule.When = strings.TrimSpace(rule.When)
	if rule.When != "" {
		if len(rule.When) > keymapMaxWhenLength {
			return fmt.Errorf("when exceeds %d characters", keymapMaxWhenLength)
		}
		if err := validateWhenExpression(rule.When); err != nil {
			return fmt.Errorf("when: %w", err)
		}
	}
	if rule.Platform != nil {
		seen := map[string]struct{}{}
		for _, platform := range rule.Platform {
			if _, known := keymapPlatformSet[platform]; !known {
				return fmt.Errorf("platform %q must be mac, win or linux", platform)
			}
			if _, duplicate := seen[platform]; duplicate {
				return fmt.Errorf("duplicate platform %q", platform)
			}
			seen[platform] = struct{}{}
		}
	}
	return nil
}

// validateKeySequence mirrors the structure accepted by the frontend parser
// (keys.ts): one or two space-separated chords, or a double modifier
// ("shift shift"); each chord is modifiers joined by "+" and one key token.
func validateKeySequence(text string) error {
	if len(text) > keymapMaxKeyLength {
		return fmt.Errorf("key exceeds %d characters", keymapMaxKeyLength)
	}
	parts := strings.Fields(text)
	if len(parts) == 2 {
		first, firstOK := keymapModifiers[strings.ToLower(parts[0])]
		second, secondOK := keymapModifiers[strings.ToLower(parts[1])]
		if firstOK && secondOK {
			if first != second {
				return fmt.Errorf("key %q mixes two different double modifiers", text)
			}
			return nil
		}
	}
	if len(parts) == 0 || len(parts) > 2 {
		return fmt.Errorf("key %q must contain one or two chords", text)
	}
	for _, part := range parts {
		tokens := strings.Split(part, "+")
		modifiers := map[string]struct{}{}
		for index, token := range tokens {
			token = strings.ToLower(token)
			if token == "" {
				return fmt.Errorf("key %q has an empty token", text)
			}
			if index < len(tokens)-1 {
				modifier, ok := keymapModifiers[token]
				if !ok {
					return fmt.Errorf("key %q uses unknown modifier %q", text, token)
				}
				if _, duplicate := modifiers[modifier]; duplicate {
					return fmt.Errorf("key %q repeats modifier %q", text, token)
				}
				modifiers[modifier] = struct{}{}
				continue
			}
			if !isKeymapKeyToken(token) {
				return fmt.Errorf("key %q uses unknown key %q", text, token)
			}
		}
	}
	return nil
}

func isKeymapKeyToken(token string) bool {
	if len(token) == 1 && (token[0] >= 'a' && token[0] <= 'z' || token[0] >= '0' && token[0] <= '9') {
		return true
	}
	if keymapFunctionKey.MatchString(token) {
		return true
	}
	_, named := keymapNamedKeys[token]
	return named
}

// pruneKeymapExtends drops presets whose extends chain is missing, cyclic or
// too deep. Iterates until stable so a child of a dropped parent is dropped too.
func pruneKeymapExtends(presets []KeymapPreset, issues []KeymapIssue) ([]KeymapPreset, []KeymapIssue) {
	for {
		byID := make(map[string]KeymapPreset, len(presets))
		for _, preset := range presets {
			byID[preset.ID] = preset
		}
		kept := presets[:0:0]
		removed := false
		for _, preset := range presets {
			if message := keymapChainProblem(preset, byID); message != "" {
				issues = append(issues, KeymapIssue{Entry: -1, Preset: preset.ID, Message: message})
				removed = true
				continue
			}
			kept = append(kept, preset)
		}
		presets = kept
		if !removed {
			return presets, issues
		}
	}
}

func keymapChainProblem(preset KeymapPreset, byID map[string]KeymapPreset) string {
	seen := map[string]struct{}{preset.ID: {}}
	current := preset
	for depth := 0; current.Extends != "" && current.Extends != keymapBasePresetID; depth++ {
		if depth >= keymapMaxExtendsDepth {
			return fmt.Sprintf("keymap preset %q extends chain is deeper than %d", preset.ID, keymapMaxExtendsDepth)
		}
		parent, ok := byID[current.Extends]
		if !ok {
			return fmt.Sprintf("keymap preset %q extends unknown preset %q", preset.ID, current.Extends)
		}
		if _, cyclic := seen[parent.ID]; cyclic {
			return fmt.Sprintf("keymap preset %q extends chain is cyclic", preset.ID)
		}
		seen[parent.ID] = struct{}{}
		current = parent
	}
	return ""
}

// keymapIssues returns the skipped keymap presets of a manifest.
func (manifest Manifest) keymapIssues() []KeymapIssue {
	resolved := manifest.contribution(keymapPointKey)
	if resolved == nil {
		return nil
	}
	issues := make([]KeymapIssue, 0, len(resolved.Issues))
	for _, issue := range resolved.Issues {
		issues = append(issues, KeymapIssue{Entry: issue.Entry, Preset: issue.ID, Message: issue.Message})
	}
	return issues
}

// keymapIssuesError makes skipped presets fatal. Install, upgrade and
// preflight use contributionIssuesError, which produces the same text for a
// keymaps-only package.
func (manifest Manifest) keymapIssuesError() error {
	issues := manifest.keymapIssues()
	if len(issues) == 0 {
		return nil
	}
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		messages = append(messages, issue.Message)
	}
	return fmt.Errorf("invalid keymap contribution: %s", strings.Join(messages, "; "))
}

// keymapIssueText renders a skipped preset for logs and development diagnostics.
func keymapIssueText(issue KeymapIssue) string {
	if issue.Preset != "" {
		return fmt.Sprintf("skipped keymap preset %q: %s", issue.Preset, issue.Message)
	}
	if issue.Entry >= 0 {
		return fmt.Sprintf("skipped contributes.keymaps[%d]: %s", issue.Entry, issue.Message)
	}
	return "skipped keymap preset: " + issue.Message
}

func contributionKeymapIssueText(issue ContributionIssue) string {
	return keymapIssueText(KeymapIssue{Entry: issue.Entry, Preset: issue.ID, Message: issue.Message})
}

// KeymapPresets returns the validated presets of a declarative manifest.
func (manifest Manifest) KeymapPresets() []KeymapPreset {
	resolved := manifest.contribution(keymapPointKey)
	if resolved == nil {
		return nil
	}
	presets := make([]KeymapPreset, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		presets = append(presets, entry.(KeymapPreset))
	}
	return presets
}

func (manifest Manifest) isDeclarative() bool {
	return manifest.Runtime.Type == pluginRuntimeDeclarative
}

// declarativeLocked reports a data-only plugin; caller holds record.mu.
func (record *pluginRecord) declarativeLocked() bool {
	return record.manifest != nil && record.manifest.isDeclarative()
}
