package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Terminal colour schemes (contributes.terminalColorSchemes) add xterm
// palettes to the terminal colour scheme setting. They are pure data: a fixed
// set of strictly typed hex colours, no CSS text and no fonts. Contrast floors
// keep text, the cursor, selections and the ANSI colours visible on the
// scheme background, so a scheme cannot hide terminal output or prompts.
//
// An entry is inline or a {"path": "terminal-schemes/<name>.json"} reference
// (fileRef mode "entry"). Ids are "<pluginId>.<name>" so a stored setting
// names its provider; when the provider is missing the frontend falls back to
// the default palette and keeps the stored id.

const terminalColorSchemePointKey = "terminalColorSchemes"

var (
	terminalColorSchemeSpec        = mustContributionSpec(terminalColorSchemePointKey)
	terminalColorSchemeTitleMax    = int(terminalColorSchemeSpec.Limit("titleMaxRunes"))
	terminalColorSchemeLocalesMax  = int(terminalColorSchemeSpec.Limit("localesMax"))
	terminalColorSchemeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	terminalColorSchemeHexPattern  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	terminalColorSchemeHexAlpha    = regexp.MustCompile(`^#[0-9a-fA-F]{8}$`)

	// terminalColorSchemeAnsiChecked are the chromatic ANSI colours held to
	// the ANSI contrast floor; black, white and their bright variants
	// conventionally sit near the background.
	terminalColorSchemeAnsiChecked = []string{
		"red", "green", "yellow", "blue", "magenta", "cyan",
		"brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan",
	}
)

// TerminalColorSchemeContribution is one validated scheme as sent to the
// frontend. Colour values are lower-case hex.
type TerminalColorSchemeContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Kind           string            `json:"kind"`
	Colors         map[string]string `json:"colors"`
}

type terminalColorSchemeManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized"`
	Kind           string            `json:"kind"`
	Colors         map[string]string `json:"colors"`
}

type terminalColorSchemeFileReference struct {
	Path string `json:"path"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:    terminalColorSchemePointKey,
		Decode: decodeTerminalColorSchemeEntry,
		DuplicateMessage: func(id string) string {
			return fmt.Sprintf("terminal colour scheme id %q is duplicated", id)
		},
		IssuePrefix: "invalid terminalColorSchemes contribution",
		IssueText:   terminalColorSchemeIssueText,
		EntryNoun:   "terminal colour schemes",
		FileLabels: contributionFileLabels{
			Noun: "terminal colour scheme", Entry: "terminal colour scheme", ShapeHint: "a colour scheme object",
		},
		DecodeReference: func(raw json.RawMessage) (string, error) {
			var reference terminalColorSchemeFileReference
			if err := decodeStrictJSON(raw, &reference, true); err != nil {
				return "", err
			}
			return reference.Path, nil
		},
	})
}

func terminalColorSchemeIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("terminal colour scheme %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("terminal colour scheme entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeTerminalColorSchemeEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry terminalColorSchemeManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse terminal colour scheme: %w", err)
	}
	scheme, err := validateTerminalColorScheme(ctx.manifest.ID, entry)
	return scheme, scheme.ID, err
}

func validateTerminalColorScheme(pluginID string, entry terminalColorSchemeManifestEntry) (TerminalColorSchemeContribution, error) {
	id := strings.TrimSpace(entry.ID)
	scheme := TerminalColorSchemeContribution{ID: id}
	name, ok := strings.CutPrefix(id, pluginID+".")
	if !ok || !terminalColorSchemeNamePattern.MatchString(name) {
		return scheme, fmt.Errorf("terminal colour scheme id %q must be %q followed by a name matching ^[a-z0-9][a-z0-9-]{0,31}$", id, pluginID+".")
	}
	title, err := validateContributionLine(entry.Title, terminalColorSchemeTitleMax, true)
	if err != nil {
		return scheme, fmt.Errorf("terminal colour scheme %q title %w", id, err)
	}
	scheme.Title = title
	if scheme.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, terminalColorSchemeTitleMax, terminalColorSchemeLocalesMax); err != nil {
		return scheme, fmt.Errorf("terminal colour scheme %q titleLocalized %w", id, err)
	}
	if !slices.Contains(terminalColorSchemeSpec.List("kinds"), entry.Kind) {
		return scheme, fmt.Errorf("terminal colour scheme %q kind must be light or dark", id)
	}
	scheme.Kind = entry.Kind
	colors, err := normalizeTerminalColorSchemeColors(entry.Colors)
	if err != nil {
		return scheme, fmt.Errorf("terminal colour scheme %q %w", id, err)
	}
	if err := validateTerminalColorSchemeContrast(scheme.Kind, colors); err != nil {
		return scheme, fmt.Errorf("terminal colour scheme %q %w", id, err)
	}
	scheme.Colors = colors
	return scheme, nil
}

// normalizeTerminalColorSchemeColors requires exactly the contract's required
// keys plus optional keys, #rrggbb values and #rrggbbaa only for the alpha
// keys. Values are lower-cased.
func normalizeTerminalColorSchemeColors(colors map[string]string) (map[string]string, error) {
	if colors == nil {
		return nil, errors.New("colors is required")
	}
	required := terminalColorSchemeSpec.List("requiredColors")
	optional := terminalColorSchemeSpec.List("optionalColors")
	alpha := terminalColorSchemeSpec.List("alphaColors")
	for _, key := range required {
		if _, ok := colors[key]; !ok {
			return nil, fmt.Errorf("colors is missing required color %q", key)
		}
	}
	out := make(map[string]string, len(colors))
	for key, value := range colors {
		if !slices.Contains(required, key) && !slices.Contains(optional, key) {
			return nil, fmt.Errorf("colors has unknown color %q", key)
		}
		switch {
		case terminalColorSchemeHexPattern.MatchString(value):
		case terminalColorSchemeHexAlpha.MatchString(value) && slices.Contains(alpha, key):
		case terminalColorSchemeHexAlpha.MatchString(value):
			return nil, fmt.Errorf("color %q must be opaque #rrggbb (alpha is allowed only for %s)", key, strings.Join(alpha, ", "))
		default:
			return nil, fmt.Errorf("color %q value %q must be a #rrggbb hex color", key, value)
		}
		out[key] = strings.ToLower(value)
	}
	return out, nil
}

// validateTerminalColorSchemeContrast enforces the contract floors (limits are
// in tenths). Translucent selection colours are composited on the background.
func validateTerminalColorSchemeContrast(kind string, colors map[string]string) error {
	parse := func(key string) themeRGBA {
		color, _ := parseThemeColor(colors[key]) // syntax already validated
		return color
	}
	floor := func(limit string) float64 { return float64(terminalColorSchemeSpec.Limit(limit)) / 10 }
	background := parse("background")
	check := func(foregroundKey string, foreground, backdrop themeRGBA, limit, against string) error {
		ratio := themeContrastRatio(foreground, backdrop)
		if minimum := floor(limit); ratio+1e-9 < minimum {
			return fmt.Errorf("contrast of %s on %s is %.2f:1, below %.1f:1", foregroundKey, against, ratio, minimum)
		}
		return nil
	}
	if err := check("foreground", parse("foreground"), background, "contrastForegroundTenths", "background"); err != nil {
		return err
	}
	if err := check("cursor", parse("cursor"), background, "contrastCursorTenths", "background"); err != nil {
		return err
	}
	selectionKey := "foreground"
	if _, ok := colors["selectionForeground"]; ok {
		selectionKey = "selectionForeground"
	}
	selection := parse("selectionBackground").over(background)
	if err := check(selectionKey, parse(selectionKey), selection, "contrastSelectionTenths", "selectionBackground"); err != nil {
		return err
	}
	for _, key := range terminalColorSchemeAnsiChecked {
		if err := check(key, parse(key), background, "contrastAnsiTenths", "background"); err != nil {
			return err
		}
	}
	if kind == "dark" {
		if err := check("brightBlack", parse("brightBlack"), background, "contrastBrightBlackTenths", "background"); err != nil {
			return err
		}
	}
	return nil
}
