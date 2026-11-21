package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Language packs (contributes.localizations) add UI languages that MinCode
// does not ship. Each entry names one BCP 47 tag that is not built in (zh/en
// and their aliases stay host-owned) and references up to two JSON assets:
//
//   - messages: a flat {"<host message key>": "<text>"} object, and
//   - pluginTitles: {"<pluginId>": {"commands": {...}, "views": {...}}},
//     translations of other plugins' command and view titles.
//
// Assets are served on demand (plugin_host_contribution_assets); the snapshot
// carries only their content-hash keys and counts. Go validates structure and
// characters only; whether a key exists, whether it is a protected
// security/approval key and whether placeholders match the source text are
// frontend concerns (vue-app/src/domain/i18n/locale-pack.ts), because a pack
// may target several host versions. Pack text is always rendered as text.

const localizationPointKey = "localizations"

var (
	localizationSpec             = mustContributionSpec(localizationPointKey)
	localizationMessagesMaxKeys  = int(localizationSpec.Limit("messagesMaxKeys"))
	localizationKeyMaxLength     = int(localizationSpec.Limit("keyMaxLength"))
	localizationValueMaxRunes    = int(localizationSpec.Limit("valueMaxRunes"))
	localizationNameMaxRunes     = int(localizationSpec.Limit("languageNameMaxRunes"))
	localizationTitlesMaxPlugins = int(localizationSpec.Limit("pluginTitlesMaxPlugins"))
	localizationTitlesPerPlugin  = int(localizationSpec.Limit("titlesPerPlugin"))
	localizationTitleMaxRunes    = int(localizationSpec.Limit("titleMaxRunes"))
	localizationBuiltinLocales   = localizationSpec.List("builtinLocales")

	localizationTagPattern        = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z][a-z]{3})?(-(?:[A-Z]{2}|[0-9]{3}))?$`)
	localizationMessageKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/+-]*$`)
)

// Asset hints: the entry field that referenced the asset.
const (
	localizationMessagesHint     = "messages"
	localizationPluginTitlesHint = "pluginTitles"
)

// LocalizationContribution is one validated language pack entry as sent to
// the frontend consumer.
type LocalizationContribution struct {
	Locale                string                          `json:"locale"`
	LanguageName          string                          `json:"languageName"`
	LocalizedLanguageName string                          `json:"localizedLanguageName"`
	Messages              *LocalizationMessagesAsset      `json:"messages,omitempty"`
	PluginTitles          *LocalizationPluginTitlesAssets `json:"pluginTitles,omitempty"`
}

// LocalizationMessagesAsset references the canonical messages asset.
type LocalizationMessagesAsset struct {
	Asset string `json:"asset"`
	Count int    `json:"count"`
}

// LocalizationPluginTitlesAssets references the canonical plugin titles asset
// and lists the plugin IDs it translates (sorted).
type LocalizationPluginTitlesAssets struct {
	Asset   string   `json:"asset"`
	Plugins []string `json:"plugins"`
}

type localizationEntryReference struct {
	Locale                string  `json:"locale"`
	LanguageName          string  `json:"languageName"`
	LocalizedLanguageName string  `json:"localizedLanguageName"`
	Messages              *string `json:"messages"`
	PluginTitles          *string `json:"pluginTitles"`
}

type localizationPluginTitles struct {
	Commands map[string]string `json:"commands,omitempty"`
	Views    map[string]string `json:"views,omitempty"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              localizationPointKey,
		Decode:           decodeLocalizationEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate language pack locale %q", id) },
		IssuePrefix:      "invalid localization contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped language pack %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.localizations[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun: "language packs",
		ValidateAsset: func(hint, ext string, data []byte) ([]byte, string, error) {
			canonical, _, err := validateLocalizationAsset(hint, ext, data)
			return canonical, "application/json", err
		},
	})
}

func decodeLocalizationEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var reference localizationEntryReference
	if err := decodeStrictJSON(raw, &reference, true); err != nil {
		return nil, "", fmt.Errorf("parse language pack entry: %w", err)
	}
	locale := strings.TrimSpace(reference.Locale)
	if !localizationTagPattern.MatchString(locale) {
		return nil, "", fmt.Errorf("locale %q must be a language tag such as ja, pt-BR or zh-Hant-TW", reference.Locale)
	}
	if isBuiltinLocalizationLocale(locale) {
		return nil, locale, fmt.Errorf("locale %q is built in; language packs may only add other languages or regional variants", locale)
	}
	item := LocalizationContribution{Locale: locale}
	var err error
	if item.LanguageName, err = validateLocalizationLine(reference.LanguageName, localizationNameMaxRunes); err != nil {
		return nil, locale, fmt.Errorf("languageName: %w", err)
	}
	if item.LocalizedLanguageName, err = validateLocalizationLine(reference.LocalizedLanguageName, localizationNameMaxRunes); err != nil {
		return nil, locale, fmt.Errorf("localizedLanguageName: %w", err)
	}
	if reference.Messages == nil && reference.PluginTitles == nil {
		return nil, locale, errors.New(`at least one of "messages" or "pluginTitles" is required`)
	}
	if reference.Messages != nil {
		key, summary, err := readLocalizationAsset(ctx, *reference.Messages, localizationMessagesHint)
		if err != nil {
			return nil, locale, err
		}
		item.Messages = &LocalizationMessagesAsset{Asset: key, Count: summary.count}
	}
	if reference.PluginTitles != nil {
		key, summary, err := readLocalizationAsset(ctx, *reference.PluginTitles, localizationPluginTitlesHint)
		if err != nil {
			return nil, locale, err
		}
		item.PluginTitles = &LocalizationPluginTitlesAssets{Asset: key, Plugins: summary.plugins}
	}
	return item, locale, nil
}

type localizationAssetSummary struct {
	count   int
	plugins []string
}

// readLocalizationAsset reads an asset through ctx.readAsset and captures the
// summary the validator computed, so the snapshot can show counts without the
// frontend fetching the asset. The point's own validator is wrapped for the
// duration of the call only.
func readLocalizationAsset(ctx *pointContext, relative, hint string) (string, localizationAssetSummary, error) {
	var summary localizationAssetSummary
	previous := ctx.validateAsset
	ctx.validateAsset = func(hint, ext string, data []byte) ([]byte, string, error) {
		canonical, result, err := validateLocalizationAsset(hint, ext, data)
		summary = result
		return canonical, "application/json", err
	}
	defer func() { ctx.validateAsset = previous }()
	key, err := ctx.readAsset(relative, hint)
	return key, summary, err
}

func isBuiltinLocalizationLocale(locale string) bool {
	for _, builtin := range localizationBuiltinLocales {
		if strings.EqualFold(builtin, locale) {
			return true
		}
	}
	return false
}

// validateLocalizationAsset validates one pack file (hint selects the shape)
// and returns its canonical JSON: sorted keys, no insignificant whitespace.
func validateLocalizationAsset(hint, ext string, data []byte) ([]byte, localizationAssetSummary, error) {
	var summary localizationAssetSummary
	if ext != ".json" {
		return nil, summary, errors.New("must be a .json file")
	}
	// Strict JSON like every other point, so the CLI and the cloud accept the same files.
	if bytes.HasPrefix(data, []byte("\ufeff")) {
		return nil, summary, errors.New("must not start with a byte order mark")
	}
	root, err := parseLanguageJSON(data)
	if err != nil {
		return nil, summary, err
	}
	if root.kind != 'o' {
		return nil, summary, errors.New("must be a JSON object")
	}
	if len(root.keys) == 0 {
		return nil, summary, errors.New("must contain at least one entry")
	}
	switch hint {
	case localizationMessagesHint:
		messages, err := validateLocalizationMessages(root)
		if err != nil {
			return nil, summary, err
		}
		summary.count = len(messages)
		canonical, err := json.Marshal(messages)
		return canonical, summary, err
	case localizationPluginTitlesHint:
		titles, err := validateLocalizationPluginTitles(root)
		if err != nil {
			return nil, summary, err
		}
		summary.plugins = make([]string, 0, len(titles))
		for pluginID := range titles {
			summary.plugins = append(summary.plugins, pluginID)
		}
		sort.Strings(summary.plugins)
		canonical, err := json.Marshal(titles)
		return canonical, summary, err
	}
	return nil, summary, fmt.Errorf("unknown language pack asset %q", hint)
}

func validateLocalizationMessages(root *languageJSONNode) (map[string]string, error) {
	if len(root.keys) > localizationMessagesMaxKeys {
		return nil, fmt.Errorf("must contain at most %d messages", localizationMessagesMaxKeys)
	}
	messages := make(map[string]string, len(root.keys))
	for index, key := range root.keys {
		if len(key) > localizationKeyMaxLength || !localizationMessageKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("message key %q must match [A-Za-z0-9][A-Za-z0-9_.:@/+-]* and be at most %d characters", truncateLocalizationText(key), localizationKeyMaxLength)
		}
		node := root.items[index]
		if node.kind != 's' {
			return nil, fmt.Errorf("message %q must be a string (messages are a flat object)", key)
		}
		if err := validateLocalizationText(node.text, localizationValueMaxRunes, true); err != nil {
			return nil, fmt.Errorf("message %q: %w", key, err)
		}
		messages[key] = node.text
	}
	return messages, nil
}

func validateLocalizationPluginTitles(root *languageJSONNode) (map[string]localizationPluginTitles, error) {
	if len(root.keys) > localizationTitlesMaxPlugins {
		return nil, fmt.Errorf("must translate at most %d plugins", localizationTitlesMaxPlugins)
	}
	result := make(map[string]localizationPluginTitles, len(root.keys))
	for index, pluginID := range root.keys {
		if !pluginIDPattern.MatchString(pluginID) {
			return nil, fmt.Errorf("plugin id %q is invalid", truncateLocalizationText(pluginID))
		}
		node := root.items[index]
		if node.kind != 'o' {
			return nil, fmt.Errorf("plugin %q must be an object with commands and/or views", pluginID)
		}
		var titles localizationPluginTitles
		count := 0
		for groupIndex, group := range node.keys {
			groupNode := node.items[groupIndex]
			if groupNode.kind != 'o' {
				return nil, fmt.Errorf("plugin %q %s must be an object", pluginID, truncateLocalizationText(group))
			}
			values := make(map[string]string, len(groupNode.keys))
			for itemIndex, id := range groupNode.keys {
				if !contributionIDPattern.MatchString(id) {
					return nil, fmt.Errorf("plugin %q %s id %q is invalid", pluginID, group, truncateLocalizationText(id))
				}
				value := groupNode.items[itemIndex]
				if value.kind != 's' {
					return nil, fmt.Errorf("plugin %q %s %q must be a string", pluginID, group, id)
				}
				if err := validateLocalizationText(value.text, localizationTitleMaxRunes, false); err != nil {
					return nil, fmt.Errorf("plugin %q %s %q: %w", pluginID, group, id, err)
				}
				values[id] = value.text
			}
			count += len(values)
			switch group {
			case "commands":
				titles.Commands = values
			case "views":
				titles.Views = values
			default:
				return nil, fmt.Errorf(`plugin %q may only contain "commands" and "views"`, pluginID)
			}
		}
		if count > localizationTitlesPerPlugin {
			return nil, fmt.Errorf("plugin %q has more than %d titles", pluginID, localizationTitlesPerPlugin)
		}
		result[pluginID] = titles
	}
	return result, nil
}

// validateLocalizationText checks one translated string: non-blank, bounded,
// and free of controls, line/paragraph separators, bidi controls and
// zero-width characters. multiline permits "\n" (host messages only).
func validateLocalizationText(value string, maxRunes int, multiline bool) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("must not be blank")
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("exceeds %d characters", maxRunes)
	}
	for _, r := range value {
		if multiline && r == '\n' {
			continue
		}
		if isLocalizationHiddenRune(r) {
			return fmt.Errorf("contains a control, bidirectional or zero-width character (U+%04X)", r)
		}
	}
	return nil
}

func isLocalizationHiddenRune(r rune) bool {
	switch {
	case unicode.IsControl(r), r == '\u2028', r == '\u2029':
		return true
	case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
		return true
	case r >= '\u200b' && r <= '\u200f', r == '\u2060', r == '\ufeff':
		return true
	}
	return false
}

// validateLocalizationLine checks a single-line language name.
func validateLocalizationLine(value string, maxRunes int) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errors.New("must not be blank")
	}
	if err := validateLocalizationText(trimmed, maxRunes, false); err != nil {
		return "", err
	}
	return trimmed, nil
}

func truncateLocalizationText(value string) string {
	if utf8.RuneCountInString(value) <= 64 {
		return value
	}
	return string([]rune(value)[:64]) + "…"
}

// localizations returns the validated entries of a manifest.
func (manifest Manifest) localizations() []LocalizationContribution {
	resolved := manifest.contribution(localizationPointKey)
	if resolved == nil {
		return nil
	}
	items := make([]LocalizationContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if item, ok := entry.(LocalizationContribution); ok {
			items = append(items, item)
		}
	}
	return items
}
