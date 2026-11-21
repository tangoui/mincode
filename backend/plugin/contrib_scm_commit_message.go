package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Commit message providers (contributes.scmCommitMessage) are a process-only
// data provider point that requires workspace.read: the single entry adds a
// "plugin generate" button to the Git commit box. The entry names one of the
// plugin's own read actions that is not aiCallable; the host calls it only
// when the user clicks the button, with the staged changes it read itself
// (sensitive files left out, see backend/git/staged_context.go), and only
// fills the commit message draft with the validated answer. This file
// validates the manifest; the call is in scm_commit_message_invoke.go.
// Mirrors: tools/mincode-cli/data_providers.go (CLI) and
// vue-app/src/domain/plugins/scm-commit-message.ts (frontend revalidation).
const scmCommitMessagePointKey = "scmCommitMessage"

var (
	scmCommitMessageSpec          = mustContributionSpec(scmCommitMessagePointKey)
	scmCommitMessageTitleMaxRunes = int(scmCommitMessageSpec.Limit("titleMaxRunes"))
	scmCommitMessageLocalesMax    = int(scmCommitMessageSpec.Limit("localesMax"))

	scmCommitMessageIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// ScmCommitMessageContribution is a validated commit message provider.
type ScmCommitMessageContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Action         string            `json:"action"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:    scmCommitMessagePointKey,
		Decode: decodeScmCommitMessageEntry,
		DuplicateMessage: func(id string) string {
			return fmt.Sprintf("duplicate commit message provider id %q", id)
		},
		IssuePrefix: "invalid scmCommitMessage contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped commit message provider %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.scmCommitMessage[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun: "commit message providers",
		// Adding a provider or changing its action in a development
		// generation sends staged changes to new code.
		ExpandsPermission: func(current, candidate *resolvedContribution) bool {
			return contributionActionsExpand(current, candidate, func(entry any) (string, []string, bool) {
				provider, ok := entry.(ScmCommitMessageContribution)
				return provider.ID, []string{provider.Action}, ok
			})
		},
	})
}

func decodeScmCommitMessageEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry ScmCommitMessageContribution
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse commit message provider: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !scmCommitMessageIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("commit message provider id %q must match %s", id, scmCommitMessageIDPattern)
	}
	if title := strings.TrimSpace(entry.Title); utf8.ValidString(title) && utf8.RuneCountInString(title) > scmCommitMessageTitleMaxRunes {
		return nil, id, fmt.Errorf("commit message provider %q title exceeds %d characters", id, scmCommitMessageTitleMaxRunes)
	}
	title, err := validateContributionLine(entry.Title, scmCommitMessageTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("commit message provider %q title %w", id, err)
	}
	titleLocalized, err := validateContributionLocalized(entry.TitleLocalized, scmCommitMessageTitleMaxRunes, scmCommitMessageLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("commit message provider %q titleLocalized %w", id, err)
	}
	actionID, err := contributionProviderActionRef(ctx, "commit message provider", id, entry.Action)
	if err != nil {
		return nil, id, err
	}
	return ScmCommitMessageContribution{ID: id, Title: title, TitleLocalized: titleLocalized, Action: actionID}, id, nil
}

// ScmCommitMessageProviders returns the validated commit message providers
// of a manifest.
func (manifest Manifest) ScmCommitMessageProviders() []ScmCommitMessageContribution {
	resolved := manifest.contribution(scmCommitMessagePointKey)
	if resolved == nil {
		return nil
	}
	providers := make([]ScmCommitMessageContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		providers = append(providers, entry.(ScmCommitMessageContribution))
	}
	return providers
}
