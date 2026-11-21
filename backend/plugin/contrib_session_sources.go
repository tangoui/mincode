package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Session sources (contributes.sessionSources) are a process-only data
// provider point: each entry adds a read-only source to the AI Sessions page.
// An entry names one of the plugin's own read actions that is not aiCallable;
// the host calls it only when the user expands the source (request "list")
// or opens one of its sessions (request "messages"), and renders the
// validated messages as plain text. Plugin sessions cannot be deleted,
// renamed or resumed. This file validates the manifest; the calls are in
// session_source_invoke.go and their results are checked in
// session_source_results.go. Mirrors: tools/mincode-cli/data_providers.go (CLI) and
// vue-app/src/domain/plugins/session-sources.ts (frontend revalidation).
const sessionSourcePointKey = "sessionSources"

const sessionSourceDefaultIcon = "chat"

// sessionSourceFallbackMark is the badge of a source whose title has no
// ASCII letter or digit.
const sessionSourceFallbackMark = "PL"

var (
	sessionSourceSpec          = mustContributionSpec(sessionSourcePointKey)
	sessionSourceTitleMaxRunes = int(sessionSourceSpec.Limit("titleMaxRunes"))
	sessionSourceLocalesMax    = int(sessionSourceSpec.Limit("localesMax"))
	sessionSourceIcons         = sessionSourceSpec.List("icons")

	sessionSourceIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	sessionSourceMarkPattern = regexp.MustCompile(`^[A-Z0-9]{1,3}$`)
)

type sessionSourceManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Icon           string            `json:"icon,omitempty"`
	Mark           string            `json:"mark,omitempty"`
	Action         string            `json:"action"`
}

// SessionSourceContribution is the snapshot entry; Icon and Mark always
// carry their effective values.
type SessionSourceContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Icon           string            `json:"icon"` // defaults to "chat"
	Mark           string            `json:"mark"` // defaults to defaultSessionSourceMark
	Action         string            `json:"action"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              sessionSourcePointKey,
		Decode:           decodeSessionSourceEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate session source id %q", id) },
		IssuePrefix:      "invalid sessionSources contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped session source %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.sessionSources[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun: "session sources",
		// Adding a source or changing its action in a development generation
		// sends session requests to new code.
		ExpandsPermission: func(current, candidate *resolvedContribution) bool {
			return contributionActionsExpand(current, candidate, func(entry any) (string, []string, bool) {
				source, ok := entry.(SessionSourceContribution)
				return source.ID, []string{source.Action}, ok
			})
		},
	})
}

func decodeSessionSourceEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry sessionSourceManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse session source: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !sessionSourceIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("session source id %q must match %s", id, sessionSourceIDPattern)
	}
	if title := strings.TrimSpace(entry.Title); utf8.ValidString(title) && utf8.RuneCountInString(title) > sessionSourceTitleMaxRunes {
		return nil, id, fmt.Errorf("session source %q title exceeds %d characters", id, sessionSourceTitleMaxRunes)
	}
	title, err := validateContributionLine(entry.Title, sessionSourceTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("session source %q title %w", id, err)
	}
	titleLocalized, err := validateContributionLocalized(entry.TitleLocalized, sessionSourceTitleMaxRunes, sessionSourceLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("session source %q titleLocalized %w", id, err)
	}
	icon := strings.TrimSpace(entry.Icon)
	if icon == "" {
		icon = sessionSourceDefaultIcon
	} else if !slices.Contains(sessionSourceIcons, icon) {
		return nil, id, fmt.Errorf("session source %q icon %q is not one of %s", id, icon, strings.Join(sessionSourceIcons, ", "))
	}
	mark := strings.TrimSpace(entry.Mark)
	if mark == "" {
		pluginID := ""
		if ctx.manifest != nil {
			pluginID = ctx.manifest.ID
		}
		mark = defaultSessionSourceMark(title, pluginID)
	} else if !sessionSourceMarkPattern.MatchString(mark) {
		return nil, id, fmt.Errorf("session source %q mark %q must match %s", id, mark, sessionSourceMarkPattern)
	}
	actionID, err := contributionProviderActionRef(ctx, "session source", id, entry.Action)
	if err != nil {
		return nil, id, err
	}
	return SessionSourceContribution{ID: id, Title: title, TitleLocalized: titleLocalized, Icon: icon, Mark: mark, Action: actionID}, id, nil
}

// defaultSessionSourceMark is the badge of a source without its own mark:
// the first two ASCII letters or digits of the title in upper case, or "PL"
// when the title has none. The plugin ID is not used (a badge derived from
// it would look the same for every source of the plugin); the parameter
// keeps the signature shared with the frontend helper.
func defaultSessionSourceMark(title, _ string) string {
	mark := make([]byte, 0, 2)
	for index := 0; index < len(title) && len(mark) < 2; index++ {
		switch char := title[index]; {
		case char >= 'a' && char <= 'z':
			mark = append(mark, char-'a'+'A')
		case char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
			mark = append(mark, char)
		}
	}
	if len(mark) == 0 {
		return sessionSourceFallbackMark
	}
	return string(mark)
}

// SessionSources returns the validated session sources of a manifest.
func (manifest Manifest) SessionSources() []SessionSourceContribution {
	resolved := manifest.contribution(sessionSourcePointKey)
	if resolved == nil {
		return nil
	}
	sources := make([]SessionSourceContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		sources = append(sources, entry.(SessionSourceContribution))
	}
	return sources
}
