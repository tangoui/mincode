package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Editor color themes (contributes.editorThemes) add Monaco color themes to
// the editor theme setting, independent of the UI theme. They are pure data:
// allowlisted Monaco color ids with hex values and Monaco token rules, no CSS
// text and no fonts. Contrast floors keep text, syntax tokens, the cursor and
// selections readable on the editor background.
//
// An entry is inline or a {"path": "editor-themes/<name>.json"} reference
// (fileRef mode "entry"). Ids are "<pluginId>.<name>" so a stored setting
// names its provider; when the provider is missing the frontend follows the
// UI theme and keeps the stored id.

const editorThemePointKey = "editorThemes"

var (
	editorThemeSpec        = mustContributionSpec(editorThemePointKey)
	editorThemeTitleMax    = int(editorThemeSpec.Limit("titleMaxRunes"))
	editorThemeLocalesMax  = int(editorThemeSpec.Limit("localesMax"))
	editorThemeRulesMax    = int(editorThemeSpec.Limit("tokenRulesMax"))
	editorThemeColors      = themeStringSet(editorThemeSpec.MonacoColors)
	editorThemeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

// EditorThemeContribution is one validated theme as sent to the frontend.
// Colors are lower-case "#rrggbb" or "#rrggbbaa"; rule colors omit '#'.
type EditorThemeContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Kind           string            `json:"kind"`
	Colors         map[string]string `json:"colors"`
	Rules          []ThemeTokenRule  `json:"rules,omitempty"`
}

type editorThemeManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized"`
	Kind           string            `json:"kind"`
	Colors         map[string]string `json:"colors"`
	Rules          []ThemeTokenRule  `json:"rules"`
}

type editorThemeFileReference struct {
	Path string `json:"path"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:    editorThemePointKey,
		Decode: decodeEditorThemeEntry,
		DuplicateMessage: func(id string) string {
			return fmt.Sprintf("editor theme id %q is duplicated", id)
		},
		IssuePrefix: "invalid editorThemes contribution",
		IssueText:   editorThemeIssueText,
		EntryNoun:   "editor themes",
		FileLabels: contributionFileLabels{
			Noun: "editor theme", Entry: "editor theme", ShapeHint: "an editor theme object",
		},
		DecodeReference: func(raw json.RawMessage) (string, error) {
			var reference editorThemeFileReference
			if err := decodeStrictJSON(raw, &reference, true); err != nil {
				return "", err
			}
			return reference.Path, nil
		},
	})
}

func editorThemeIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("editor theme %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("editor theme entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeEditorThemeEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry editorThemeManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse editor theme: %w", err)
	}
	theme, err := validateEditorTheme(ctx.manifest.ID, entry)
	return theme, theme.ID, err
}

func validateEditorTheme(pluginID string, entry editorThemeManifestEntry) (EditorThemeContribution, error) {
	id := strings.TrimSpace(entry.ID)
	theme := EditorThemeContribution{ID: id}
	name, ok := strings.CutPrefix(id, pluginID+".")
	if !ok || !editorThemeNamePattern.MatchString(name) {
		return theme, fmt.Errorf("editor theme id %q must be %q followed by a name matching ^[a-z0-9][a-z0-9-]{0,31}$", id, pluginID+".")
	}
	title, err := validateContributionLine(entry.Title, editorThemeTitleMax, true)
	if err != nil {
		return theme, fmt.Errorf("editor theme %q title %w", id, err)
	}
	theme.Title = title
	if theme.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, editorThemeTitleMax, editorThemeLocalesMax); err != nil {
		return theme, fmt.Errorf("editor theme %q titleLocalized %w", id, err)
	}
	if !slices.Contains(editorThemeSpec.List("kinds"), entry.Kind) {
		return theme, fmt.Errorf("editor theme %q kind must be light or dark", id)
	}
	theme.Kind = entry.Kind
	colors, err := normalizeEditorThemeColors(entry.Colors)
	if err != nil {
		return theme, fmt.Errorf("editor theme %q %w", id, err)
	}
	if len(entry.Rules) > editorThemeRulesMax {
		return theme, fmt.Errorf("editor theme %q rules exceeds %d rules", id, editorThemeRulesMax)
	}
	rules := make([]ThemeTokenRule, len(entry.Rules))
	for index, rule := range entry.Rules {
		if err := validateThemeTokenRule(&rule); err != nil {
			return theme, fmt.Errorf("editor theme %q rules[%d]: %w", id, index, err)
		}
		rule.Foreground = strings.ToLower(rule.Foreground)
		rule.Background = strings.ToLower(rule.Background)
		rule.FontStyle = strings.Join(strings.Fields(rule.FontStyle), " ")
		rules[index] = rule
	}
	if err := validateEditorThemeContrast(colors, rules); err != nil {
		return theme, fmt.Errorf("editor theme %q %w", id, err)
	}
	theme.Colors = colors
	if len(rules) > 0 {
		theme.Rules = rules
	}
	return theme, nil
}

// normalizeEditorThemeColors requires the contract's required colors (opaque)
// and allows only the Monaco color allowlist. Values are expanded to
// #rrggbb/#rrggbbaa and lower-cased.
func normalizeEditorThemeColors(colors map[string]string) (map[string]string, error) {
	if colors == nil {
		return nil, errors.New("colors is required")
	}
	if err := validateThemeColorMap(colors, editorThemeColors, "colors"); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(colors))
	for key, value := range colors {
		out[key] = expandEditorThemeHex(value)
	}
	for _, key := range editorThemeSpec.List("requiredColors") {
		value, ok := out[key]
		if !ok {
			return nil, fmt.Errorf("colors is missing required color %q", key)
		}
		if len(value) == 9 && !strings.HasSuffix(value, "ff") {
			return nil, fmt.Errorf("color %q must be opaque", key)
		}
	}
	return out, nil
}

// expandEditorThemeHex turns #rgb into #rrggbb and lower-cases the value.
func expandEditorThemeHex(value string) string {
	value = strings.ToLower(value)
	if len(value) == 4 {
		return string([]byte{'#', value[1], value[1], value[2], value[2], value[3], value[3]})
	}
	return value
}

// validateEditorThemeContrast enforces the contract floors (limits are in
// tenths). Translucent colors are composited on the editor background.
func validateEditorThemeContrast(colors map[string]string, rules []ThemeTokenRule) error {
	parse := func(value string) themeRGBA {
		color, _ := parseThemeColor(value) // syntax already validated
		return color
	}
	floor := func(limit string) float64 { return float64(editorThemeSpec.Limit(limit)) / 10 }
	background := parse(colors["editor.background"])
	check := func(label string, foreground, backdrop themeRGBA, limit, against string) error {
		ratio := themeContrastRatio(foreground.over(backdrop), backdrop)
		if minimum := floor(limit); ratio+1e-9 < minimum {
			return fmt.Errorf("contrast of %s on %s is %.2f:1, below %.1f:1", label, against, ratio, minimum)
		}
		return nil
	}
	foreground := parse(colors["editor.foreground"])
	if err := check("editor.foreground", foreground, background, "contrastForegroundTenths", "editor.background"); err != nil {
		return err
	}
	if cursor, ok := colors["editorCursor.foreground"]; ok {
		if err := check("editorCursor.foreground", parse(cursor), background, "contrastCursorTenths", "editor.background"); err != nil {
			return err
		}
	}
	if selection, ok := colors["editor.selectionBackground"]; ok {
		backdrop := parse(selection).over(background)
		if err := check("editor.foreground", foreground, backdrop, "contrastSelectionTenths", "editor.selectionBackground"); err != nil {
			return err
		}
	}
	for index, rule := range rules {
		if rule.Foreground == "" {
			continue
		}
		backdrop, against := background, "editor.background"
		if rule.Background != "" {
			backdrop, against = parse("#"+rule.Background).over(background), "its background"
		}
		label := fmt.Sprintf("rules[%d] (%q) foreground", index, rule.Token)
		if err := check(label, parse("#"+rule.Foreground), backdrop, "contrastRuleTenths", against); err != nil {
			return err
		}
	}
	return nil
}

// EditorThemeContributions returns the validated editor themes of a manifest.
func (manifest Manifest) EditorThemeContributions() []EditorThemeContribution {
	resolved := manifest.contribution(editorThemePointKey)
	if resolved == nil {
		return nil
	}
	themes := make([]EditorThemeContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		themes = append(themes, entry.(EditorThemeContribution))
	}
	return themes
}
