package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Document formatters (contributes.formatters) are a process-only
// contribution point. An entry names 1-16 Monaco language ids and one of the
// plugin's own read-risk actions; the host invokes that action through
// plugin_host_format (formatter_invoke.go) only when the user formats a
// document or opted in to format-on-save for that language. The frontend
// registers a Monaco provider only for languages whose machine-local
// preference selects the formatter. Mirrors: tools/mincode-cli/formatters.go
// (CLI), assertFormattersEntry (cloud) and
// vue-app/src/domain/plugins/formatters.ts (frontend revalidation).
const formatterPointKey = "formatters"

var (
	formatterSpec          = mustContributionSpec(formatterPointKey)
	formatterTitleMaxRunes = int(formatterSpec.Limit("titleMaxRunes"))
	formatterLocalesMax    = int(formatterSpec.Limit("localesMax"))
	formatterLanguagesMax  = int(formatterSpec.Limit("languagesMax"))

	formatterIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	formatterLanguagePattern = regexp.MustCompile(`^[a-z][a-z0-9+#._-]{0,31}$`)
)

type formatterManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Languages      []string          `json:"languages"`
	Action         string            `json:"action"`
	Range          bool              `json:"range,omitempty"`
}

// FormatterContribution is the snapshot entry.
type FormatterContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Languages      []string          `json:"languages"`
	Action         string            `json:"action"`
	Range          bool              `json:"range"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              formatterPointKey,
		Decode:           decodeFormatterEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate formatter id %q", id) },
		IssuePrefix:      "invalid formatters contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped formatter %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.formatters[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "formatters",
		ExpandsPermission: formattersExpandPermission,
	})
}

func decodeFormatterEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry formatterManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse formatter: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !formatterIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("formatter id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	title, err := validateContributionLine(entry.Title, formatterTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("formatter %q title %w", id, err)
	}
	localized, err := validateContributionLocalized(entry.TitleLocalized, formatterTitleMaxRunes, formatterLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("formatter %q titleLocalized %w", id, err)
	}
	if len(entry.Languages) == 0 || len(entry.Languages) > formatterLanguagesMax {
		return nil, id, fmt.Errorf("formatter %q needs 1-%d languages", id, formatterLanguagesMax)
	}
	languages := make([]string, 0, len(entry.Languages))
	seen := map[string]bool{}
	for _, language := range entry.Languages {
		if !formatterLanguagePattern.MatchString(language) {
			return nil, id, fmt.Errorf("formatter %q language %q must match ^[a-z][a-z0-9+#._-]{0,31}$", id, language)
		}
		if seen[language] {
			return nil, id, fmt.Errorf("formatter %q repeats language %q", id, language)
		}
		seen[language] = true
		languages = append(languages, language)
	}
	actionID := strings.TrimSpace(entry.Action)
	action, ok := ctx.actions[actionID]
	if actionID == "" || !ok {
		return nil, id, fmt.Errorf("formatter %q action %q is not one of this plugin's contributes.actions", id, actionID)
	}
	if action.Risk != "read" {
		return nil, id, fmt.Errorf("formatter %q action %q must have risk \"read\", not %q", id, actionID, action.Risk)
	}
	return FormatterContribution{
		ID: id, Title: title, TitleLocalized: localized,
		Languages: languages, Action: actionID, Range: entry.Range,
	}, id, nil
}

// formattersExpandPermission reports a development generation that sends
// document text to new code: a formatter gained a language or changed its
// action (a new formatter counts as both).
func formattersExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	known := map[string]FormatterContribution{}
	if current != nil {
		for _, entry := range current.Entries {
			if formatter, ok := entry.(FormatterContribution); ok {
				known[formatter.ID] = formatter
			}
		}
	}
	for _, entry := range candidate.Entries {
		formatter, ok := entry.(FormatterContribution)
		if !ok {
			continue
		}
		previous, exists := known[formatter.ID]
		if !exists || previous.Action != formatter.Action {
			return true
		}
		languages := map[string]bool{}
		for _, language := range previous.Languages {
			languages[language] = true
		}
		for _, language := range formatter.Languages {
			if !languages[language] {
				return true
			}
		}
	}
	return false
}
