package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// File decoration providers (contributes.fileDecorations) are a process-only
// data provider point that requires workspace.read: the single entry shows a
// short badge or a colour dot after file names in the project file tree. The
// entry names one of the plugin's own read actions that is not aiCallable;
// the host calls it with one directory and the names of its visible children
// (sensitive names left out) and accepts decorations only for those names.
// This file validates the manifest; the call is in file_decorations_invoke.go
// and the data-free refresh hint in host_decorations.go.
// Mirrors: tools/mincode-cli/data_providers.go (CLI) and
// vue-app/src/domain/plugins/file-decorations.ts (frontend revalidation).
const fileDecorationPointKey = "fileDecorations"

var (
	fileDecorationSpec          = mustContributionSpec(fileDecorationPointKey)
	fileDecorationTitleMaxRunes = int(fileDecorationSpec.Limit("titleMaxRunes"))
	fileDecorationLocalesMax    = int(fileDecorationSpec.Limit("localesMax"))

	fileDecorationIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// FileDecorationContribution is a validated file decoration provider.
type FileDecorationContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Action         string            `json:"action"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:    fileDecorationPointKey,
		Decode: decodeFileDecorationEntry,
		DuplicateMessage: func(id string) string {
			return fmt.Sprintf("duplicate file decoration provider id %q", id)
		},
		IssuePrefix: "invalid fileDecorations contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped file decoration provider %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.fileDecorations[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun: "file decoration providers",
		// Adding a provider or changing its action in a development
		// generation sends file names to new code.
		ExpandsPermission: func(current, candidate *resolvedContribution) bool {
			return contributionActionsExpand(current, candidate, func(entry any) (string, []string, bool) {
				provider, ok := entry.(FileDecorationContribution)
				return provider.ID, []string{provider.Action}, ok
			})
		},
	})
}

func decodeFileDecorationEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry FileDecorationContribution
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse file decoration provider: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !fileDecorationIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("file decoration provider id %q must match %s", id, fileDecorationIDPattern)
	}
	if title := strings.TrimSpace(entry.Title); utf8.ValidString(title) && utf8.RuneCountInString(title) > fileDecorationTitleMaxRunes {
		return nil, id, fmt.Errorf("file decoration provider %q title exceeds %d characters", id, fileDecorationTitleMaxRunes)
	}
	title, err := validateContributionLine(entry.Title, fileDecorationTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("file decoration provider %q title %w", id, err)
	}
	titleLocalized, err := validateContributionLocalized(entry.TitleLocalized, fileDecorationTitleMaxRunes, fileDecorationLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("file decoration provider %q titleLocalized %w", id, err)
	}
	actionID, err := contributionProviderActionRef(ctx, "file decoration provider", id, entry.Action)
	if err != nil {
		return nil, id, err
	}
	return FileDecorationContribution{ID: id, Title: title, TitleLocalized: titleLocalized, Action: actionID}, id, nil
}

// FileDecorationProviders returns the validated file decoration providers
// of a manifest.
func (manifest Manifest) FileDecorationProviders() []FileDecorationContribution {
	resolved := manifest.contribution(fileDecorationPointKey)
	if resolved == nil {
		return nil
	}
	providers := make([]FileDecorationContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		providers = append(providers, entry.(FileDecorationContribution))
	}
	return providers
}
