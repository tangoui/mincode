package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Settings pages (contributes.settingsPages) let a process plugin show one of
// its own contributes.views under Settings > Plugins > (plugin name), next to
// the host-rendered configuration form. The page is the ordinary sandboxed
// plugin UI frame with the host context surface "settings"; the host draws a
// header outside the frame naming the plugin, so the page cannot pass itself
// off as a MinCode setting. A page gives the plugin no new data, so adding
// one in an update never asks for approval again (no ExpandsPermission).
// Floating views are rejected: they only render in their floating window.
// Mirrors: tools/mincode-cli/settings_pages.go (CLI) and
// vue-app/src/domain/plugins/settings-pages.ts (frontend revalidation).
const settingsPagesPointKey = "settingsPages"

var (
	settingsPageSpec          = mustContributionSpec(settingsPagesPointKey)
	settingsPageTitleMaxRunes = int(settingsPageSpec.Limit("titleMaxRunes"))
	settingsPageLocalesMax    = int(settingsPageSpec.Limit("localesMax"))
)

type settingsPageManifestEntry struct {
	ID             string            `json:"id"`
	View           string            `json:"view"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
}

// SettingsPageContribution is the snapshot entry. Without its own title the
// entry takes the view title (and, without its own titleLocalized, the view's
// translations), so the frontend renders it without a second lookup.
type SettingsPageContribution struct {
	ID             string            `json:"id"`
	View           string            `json:"view"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              settingsPagesPointKey,
		Decode:           decodeSettingsPageEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate settings page id %q", id) },
		IssuePrefix:      "invalid settingsPages contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped settings page %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.settingsPages[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun: "settings pages",
	})
}

func decodeSettingsPageEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry settingsPageManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse settings page: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !contributionIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("settings page id %q must match %s", id, contributionIDPattern)
	}
	page := SettingsPageContribution{ID: id, View: strings.TrimSpace(entry.View)}
	if page.View == "" {
		return nil, id, fmt.Errorf("settings page %q view is required", id)
	}
	view, ok := ctx.views[page.View]
	if !ok {
		if settingsPageFloatingView(ctx.manifest, page.View) {
			return nil, id, fmt.Errorf("settings page %q view %q is a floating view", id, page.View)
		}
		return nil, id, fmt.Errorf("settings page %q view %q is not one of this plugin's contributes.views", id, page.View)
	}
	if title := strings.TrimSpace(entry.Title); utf8.ValidString(title) && utf8.RuneCountInString(title) > settingsPageTitleMaxRunes {
		return nil, id, fmt.Errorf("settings page %q title exceeds %d characters", id, settingsPageTitleMaxRunes)
	}
	var err error
	if page.Title, err = validateContributionLine(entry.Title, settingsPageTitleMaxRunes, false); err != nil {
		return nil, id, fmt.Errorf("settings page %q title %w", id, err)
	}
	if page.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, settingsPageTitleMaxRunes, settingsPageLocalesMax); err != nil {
		return nil, id, fmt.Errorf("settings page %q titleLocalized %w", id, err)
	}
	if page.Title == "" {
		// View titles allow up to 200 bytes and are not checked for unsafe
		// runes; the inherited copy follows this point's title rules so the
		// frontend never drops a page the host accepted.
		page.Title = settingsPageInheritedTitle(view.Title)
		if page.Title == "" {
			page.Title = settingsPageInheritedTitle(view.ID)
		}
		if page.TitleLocalized == nil {
			page.TitleLocalized = settingsPageInheritedTitles(view.TitleLocalized)
		}
	}
	return page, id, nil
}

// settingsPageInheritedTitle turns a view title into a settings page title:
// unsafe runes removed, trimmed and cut to settingsPageTitleMaxRunes at a
// rune boundary.
func settingsPageInheritedTitle(value string) string {
	value = strings.Map(func(r rune) rune {
		if isUnsafeContributionRune(r, false, false) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, ""))
	return strings.TrimSpace(truncateRunes(value, settingsPageTitleMaxRunes))
}

// settingsPageInheritedTitles copies view translations with
// settingsPageInheritedTitle applied; locales left empty are dropped.
func settingsPageInheritedTitles(titles map[string]string) map[string]string {
	result := cloneStringMap(titles)
	for locale, title := range result {
		if title = settingsPageInheritedTitle(title); title == "" {
			delete(result, locale)
		} else {
			result[locale] = title
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// settingsPageFloatingView reports whether view names one of the manifest's
// floating views (pointContext.views lists tab views only).
func settingsPageFloatingView(manifest *Manifest, view string) bool {
	for _, candidate := range manifest.Contributes.Views {
		if candidate.ID == view && candidate.Floating != nil {
			return true
		}
	}
	return false
}

// SettingsPages returns the validated settings pages of a manifest.
func (manifest Manifest) SettingsPages() []SettingsPageContribution {
	resolved := manifest.contribution(settingsPagesPointKey)
	if resolved == nil {
		return nil
	}
	pages := make([]SettingsPageContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		pages = append(pages, entry.(SettingsPageContribution))
	}
	return pages
}
