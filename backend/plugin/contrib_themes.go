package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Color themes (contributes.themes) are a global, data-only contribution
// point shared by declarative and process plugins. A theme supplies the
// MinCode semantic --app-* tokens, optional Monaco colors/token rules and an
// optional xterm palette. The host validates everything structurally here;
// no theme value ever reaches CSS as free text: only hex colors for
// allowlisted token names, Monaco color ids and xterm keys pass. Fonts,
// images, URLs and arbitrary CSS are rejected by the strict decoder. The
// contrast floors below keep plugin themes readable; failing entries become
// issues (fatal at install, skipped when loading an existing package). The
// frontend mirror lives in vue-app/src/domain/themes/.
const (
	themePointKey = "themes"

	themeKindLight = "light"
	themeKindDark  = "dark"

	themeTitleMaxRunes = 200
	themeTokenMaxLen   = 128

	themeContrastText      = 4.5
	themeContrastSecondary = 3.0
	themeDefaultDanger     = "#d14343"
)

var (
	themeSpec          = mustContributionSpec(themePointKey)
	themeRulesMax      = int(themeSpec.Limit("tokenRulesMax"))
	themeRequiredToken = themeStringSet(themeSpec.Tokens.Required)
	themeKnownToken    = themeStringSet(append(append([]string{}, themeSpec.Tokens.Required...), themeSpec.Tokens.Optional...))
	themeMonacoColors  = themeStringSet(themeSpec.MonacoColors)
	themeTerminalKeys  = themeStringSet(themeSpec.TerminalKeys)

	themeIDPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	themeHexColorPattern  = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
	themeRuleColorPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
	themeRuleTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)
	themeFontStyles       = map[string]struct{}{"italic": {}, "bold": {}, "underline": {}, "strikethrough": {}}
)

func themeStringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// ThemeTokenRule is one Monaco token color rule. Colors omit the leading '#'
// as Monaco expects.
type ThemeTokenRule struct {
	Token      string `json:"token"`
	Foreground string `json:"foreground,omitempty"`
	Background string `json:"background,omitempty"`
	FontStyle  string `json:"fontStyle,omitempty"`
}

type ThemeEditor struct {
	Colors map[string]string `json:"colors,omitempty"`
	Rules  []ThemeTokenRule  `json:"rules,omitempty"`
}

// ThemeContribution mirrors the frontend PluginThemeData shape.
type ThemeContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Kind           string            `json:"kind"`
	Colors         map[string]string `json:"colors"`
	Editor         *ThemeEditor      `json:"editor,omitempty"`
	Terminal       map[string]string `json:"terminal,omitempty"`
}

type themeFileReference struct {
	Path string `json:"path"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              themePointKey,
		Decode:           decodeThemeEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate theme id %q", id) },
		IssuePrefix:      "invalid theme contribution",
		IssueText:        themeIssueText,
		EntryNoun:        "themes",
		FileLabels:       contributionFileLabels{Noun: "theme", Entry: "theme", ShapeHint: "a theme object"},
		DecodeReference: func(raw json.RawMessage) (string, error) {
			var reference themeFileReference
			if err := decodeStrictJSON(raw, &reference, true); err != nil {
				return "", err
			}
			return reference.Path, nil
		},
	})
}

func themeIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("skipped theme %q: %s", issue.ID, issue.Message)
	}
	if issue.Entry >= 0 {
		return fmt.Sprintf("skipped contributes.themes[%d]: %s", issue.Entry, issue.Message)
	}
	return "skipped theme: " + issue.Message
}

func decodeThemeEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var theme ThemeContribution
	if err := decodeStrictJSON(raw, &theme, true); err != nil {
		return nil, theme.ID, fmt.Errorf("parse theme: %w", err)
	}
	if err := validateThemeContribution(ctx.manifest.ID, &theme); err != nil {
		return nil, theme.ID, err
	}
	return theme, theme.ID, nil
}

func validateThemeContribution(pluginID string, theme *ThemeContribution) error {
	theme.ID = strings.TrimSpace(theme.ID)
	if !themeIDPattern.MatchString(theme.ID) {
		return fmt.Errorf("theme id %q must match [a-z0-9][a-z0-9._-]{0,63}", theme.ID)
	}
	prefix := pluginID + "."
	if !strings.HasPrefix(theme.ID, prefix) || len(theme.ID) == len(prefix) {
		return fmt.Errorf("theme id %q must start with %q", theme.ID, prefix)
	}
	theme.Title = strings.TrimSpace(theme.Title)
	if theme.Title == "" || utf8.RuneCountInString(theme.Title) > themeTitleMaxRunes {
		return fmt.Errorf("theme %q title must be 1-%d characters", theme.ID, themeTitleMaxRunes)
	}
	if err := validateLocalizedTitles(theme.TitleLocalized); err != nil {
		return fmt.Errorf("theme %q titleLocalized: %w", theme.ID, err)
	}
	if theme.Kind != themeKindLight && theme.Kind != themeKindDark {
		return fmt.Errorf("theme %q kind must be %q or %q", theme.ID, themeKindLight, themeKindDark)
	}
	if err := validateThemeColorMap(theme.Colors, themeKnownToken, "colors"); err != nil {
		return fmt.Errorf("theme %q %w", theme.ID, err)
	}
	for _, token := range themeSpec.Tokens.Required {
		if _, ok := theme.Colors[token]; !ok {
			return fmt.Errorf("theme %q colors is missing required token %q", theme.ID, token)
		}
	}
	if theme.Editor != nil {
		if err := validateThemeColorMap(theme.Editor.Colors, themeMonacoColors, "editor.colors"); err != nil {
			return fmt.Errorf("theme %q %w", theme.ID, err)
		}
		if len(theme.Editor.Rules) > themeRulesMax {
			return fmt.Errorf("theme %q editor.rules exceeds %d rules", theme.ID, themeRulesMax)
		}
		for index := range theme.Editor.Rules {
			if err := validateThemeTokenRule(&theme.Editor.Rules[index]); err != nil {
				return fmt.Errorf("theme %q editor.rules[%d]: %w", theme.ID, index, err)
			}
		}
	}
	if err := validateThemeColorMap(theme.Terminal, themeTerminalKeys, "terminal"); err != nil {
		return fmt.Errorf("theme %q %w", theme.ID, err)
	}
	if err := validateThemeContrast(theme); err != nil {
		return fmt.Errorf("theme %q %w", theme.ID, err)
	}
	return nil
}

func validateThemeColorMap(colors map[string]string, allowed map[string]struct{}, field string) error {
	keys := make([]string, 0, len(colors))
	for key := range colors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("%s uses unsupported key %q", field, key)
		}
		if !themeHexColorPattern.MatchString(colors[key]) {
			return fmt.Errorf("%s.%s must be a hex color (#rgb, #rrggbb or #rrggbbaa)", field, key)
		}
	}
	return nil
}

func validateThemeTokenRule(rule *ThemeTokenRule) error {
	if len(rule.Token) > themeTokenMaxLen || !themeRuleTokenPattern.MatchString(rule.Token) {
		return fmt.Errorf("token %q must be at most %d characters of [A-Za-z0-9._-]", rule.Token, themeTokenMaxLen)
	}
	for field, value := range map[string]string{"foreground": rule.Foreground, "background": rule.Background} {
		if value != "" && !themeRuleColorPattern.MatchString(value) {
			return fmt.Errorf("%s must be a hex color without '#'", field)
		}
	}
	seen := map[string]struct{}{}
	for _, style := range strings.Fields(rule.FontStyle) {
		if _, ok := themeFontStyles[style]; !ok {
			return fmt.Errorf("fontStyle %q may only combine italic, bold, underline and strikethrough", rule.FontStyle)
		}
		if _, duplicate := seen[style]; duplicate {
			return fmt.Errorf("fontStyle %q repeats %q", rule.FontStyle, style)
		}
		seen[style] = struct{}{}
	}
	return nil
}

type themeRGBA struct{ r, g, b, a float64 }

func parseThemeColor(value string) (themeRGBA, error) {
	hex := strings.TrimPrefix(value, "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	if len(hex) == 6 {
		hex += "ff"
	}
	if len(hex) != 8 {
		return themeRGBA{}, errors.New("invalid hex color")
	}
	parsed, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return themeRGBA{}, err
	}
	return themeRGBA{
		r: float64(parsed>>24&0xff) / 255,
		g: float64(parsed>>16&0xff) / 255,
		b: float64(parsed>>8&0xff) / 255,
		a: float64(parsed&0xff) / 255,
	}, nil
}

// over composites color on an opaque backdrop.
func (color themeRGBA) over(backdrop themeRGBA) themeRGBA {
	return themeRGBA{
		r: color.r*color.a + backdrop.r*(1-color.a),
		g: color.g*color.a + backdrop.g*(1-color.a),
		b: color.b*color.a + backdrop.b*(1-color.a),
		a: 1,
	}
}

func (color themeRGBA) luminance() float64 {
	channel := func(value float64) float64 {
		if value <= 0.03928 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(color.r) + 0.7152*channel(color.g) + 0.0722*channel(color.b)
}

func themeContrastRatio(foreground, background themeRGBA) float64 {
	first, second := foreground.luminance(), background.luminance()
	if first < second {
		first, second = second, first
	}
	return (first + 0.05) / (second + 0.05)
}

// validateThemeContrast enforces the WCAG floors. Translucent backgrounds
// are composited onto white (light) or black (dark); translucent text is
// composited onto its background.
func validateThemeContrast(theme *ThemeContribution) error {
	page := themeRGBA{r: 1, g: 1, b: 1, a: 1}
	if theme.Kind == themeKindDark {
		page = themeRGBA{a: 1}
	}
	danger := theme.Colors["danger"]
	if danger == "" {
		danger = themeDefaultDanger
	}
	checks := []struct {
		foreground, background string
		fgValue, bgValue       string
		floor                  float64
	}{
		{"text", "surface", theme.Colors["text"], theme.Colors["surface"], themeContrastText},
		{"text-muted", "surface", theme.Colors["text-muted"], theme.Colors["surface"], themeContrastSecondary},
		{"text-on-accent", "accent", theme.Colors["text-on-accent"], theme.Colors["accent"], themeContrastSecondary},
		{"danger", "surface", danger, theme.Colors["surface"], themeContrastSecondary},
	}
	for _, check := range checks {
		foreground, err := parseThemeColor(check.fgValue)
		if err != nil {
			return fmt.Errorf("colors.%s: %w", check.foreground, err)
		}
		background, err := parseThemeColor(check.bgValue)
		if err != nil {
			return fmt.Errorf("colors.%s: %w", check.background, err)
		}
		opaqueBackground := background.over(page)
		ratio := themeContrastRatio(foreground.over(opaqueBackground), opaqueBackground)
		if ratio+1e-9 < check.floor {
			return fmt.Errorf("contrast of %s on %s is %.2f:1, below the %.1f:1 floor", check.foreground, check.background, ratio, check.floor)
		}
	}
	return nil
}

// ThemeContributions returns the validated themes of a manifest.
func (manifest Manifest) ThemeContributions() []ThemeContribution {
	resolved := manifest.contribution(themePointKey)
	if resolved == nil {
		return nil
	}
	themes := make([]ThemeContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		themes = append(themes, entry.(ThemeContribution))
	}
	return themes
}
