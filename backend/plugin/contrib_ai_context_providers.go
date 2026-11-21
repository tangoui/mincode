package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// AI context providers (contributes.aiContextProviders) are a process-only
// contribution point. An entry declares an @mention the user can pick in the
// AI composer and one of the plugin's own read-risk actions that must NOT be
// aiCallable: the model can never invoke it. The host calls the action
// through plugin_host_ai_context (ai_context_invoke.go) only after the user
// picks the mention and presses Fetch in the preview dialog; the returned
// text is shown read-only and becomes a labelled, untrusted attachment only
// when the user inserts it. Mirrors: tools/mincode-cli/ai_context_providers.go
// (CLI), assertAiContextProvidersEntry (cloud) and
// vue-app/src/domain/ai/plugin-context.ts (frontend revalidation).
const aiContextProviderPointKey = "aiContextProviders"

var (
	aiContextProviderSpec             = mustContributionSpec(aiContextProviderPointKey)
	aiContextProviderTitleMaxRunes    = int(aiContextProviderSpec.Limit("titleMaxRunes"))
	aiContextProviderDescriptionMax   = int(aiContextProviderSpec.Limit("descriptionMaxRunes"))
	aiContextProviderLocalesMax       = int(aiContextProviderSpec.Limit("localesMax"))
	aiContextProviderPlaceholderMax   = int(aiContextProviderSpec.Limit("placeholderMaxRunes"))
	aiContextProviderReservedMentions = aiContextProviderSpec.List("reservedMentions")

	aiContextProviderIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	aiContextProviderMentionPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,23}$`)
)

type aiContextProviderArgumentEntry struct {
	Required             bool              `json:"required,omitempty"`
	Placeholder          string            `json:"placeholder,omitempty"`
	PlaceholderLocalized map[string]string `json:"placeholderLocalized,omitempty"`
}

type aiContextProviderManifestEntry struct {
	ID                   string                          `json:"id"`
	Mention              string                          `json:"mention"`
	Title                string                          `json:"title"`
	TitleLocalized       map[string]string               `json:"titleLocalized,omitempty"`
	Description          string                          `json:"description"`
	DescriptionLocalized map[string]string               `json:"descriptionLocalized,omitempty"`
	Action               string                          `json:"action"`
	Argument             *aiContextProviderArgumentEntry `json:"argument,omitempty"`
}

// AiContextProviderArgument is the optional free-text argument declaration.
type AiContextProviderArgument struct {
	Required             bool              `json:"required"`
	Placeholder          string            `json:"placeholder,omitempty"`
	PlaceholderLocalized map[string]string `json:"placeholderLocalized,omitempty"`
}

// AiContextProviderContribution is the snapshot entry.
type AiContextProviderContribution struct {
	ID                   string                     `json:"id"`
	Mention              string                     `json:"mention"`
	Title                string                     `json:"title"`
	TitleLocalized       map[string]string          `json:"titleLocalized,omitempty"`
	Description          string                     `json:"description"`
	DescriptionLocalized map[string]string          `json:"descriptionLocalized,omitempty"`
	Action               string                     `json:"action"`
	Argument             *AiContextProviderArgument `json:"argument,omitempty"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              aiContextProviderPointKey,
		Decode:           decodeAiContextProviderEntry,
		Finalize:         finalizeAiContextProviderEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate AI context provider id %q", id) },
		IssuePrefix:      "invalid aiContextProviders contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped AI context provider %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.aiContextProviders[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "AI context providers",
		ExpandsPermission: aiContextProvidersExpandPermission,
	})
}

func decodeAiContextProviderEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry aiContextProviderManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse AI context provider: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !aiContextProviderIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("AI context provider id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	mention := strings.TrimSpace(entry.Mention)
	if !aiContextProviderMentionPattern.MatchString(mention) {
		return nil, id, fmt.Errorf("AI context provider %q mention %q must match ^[a-z][a-z0-9-]{1,23}$", id, mention)
	}
	for _, reserved := range aiContextProviderReservedMentions {
		if mention == reserved {
			return nil, id, fmt.Errorf("AI context provider %q mention %q is reserved by the host", id, mention)
		}
	}
	title, err := validateContributionLine(entry.Title, aiContextProviderTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("AI context provider %q title %w", id, err)
	}
	titleLocalized, err := validateContributionLocalized(entry.TitleLocalized, aiContextProviderTitleMaxRunes, aiContextProviderLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("AI context provider %q titleLocalized %w", id, err)
	}
	description, err := validateContributionLine(entry.Description, aiContextProviderDescriptionMax, true)
	if err != nil {
		return nil, id, fmt.Errorf("AI context provider %q description %w", id, err)
	}
	descriptionLocalized, err := validateContributionLocalized(entry.DescriptionLocalized, aiContextProviderDescriptionMax, aiContextProviderLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("AI context provider %q descriptionLocalized %w", id, err)
	}
	actionID := strings.TrimSpace(entry.Action)
	action, ok := ctx.actions[actionID]
	if actionID == "" || !ok {
		return nil, id, fmt.Errorf("AI context provider %q action %q is not one of this plugin's contributes.actions", id, actionID)
	}
	if action.Risk != "read" {
		return nil, id, fmt.Errorf("AI context provider %q action %q must have risk \"read\", not %q", id, actionID, action.Risk)
	}
	if action.AICallable {
		return nil, id, fmt.Errorf("AI context provider %q action %q must not be aiCallable", id, actionID)
	}
	var argument *AiContextProviderArgument
	if entry.Argument != nil {
		placeholder, err := validateContributionLine(entry.Argument.Placeholder, aiContextProviderPlaceholderMax, false)
		if err != nil {
			return nil, id, fmt.Errorf("AI context provider %q argument.placeholder %w", id, err)
		}
		placeholderLocalized, err := validateContributionLocalized(entry.Argument.PlaceholderLocalized, aiContextProviderPlaceholderMax, aiContextProviderLocalesMax)
		if err != nil {
			return nil, id, fmt.Errorf("AI context provider %q argument.placeholderLocalized %w", id, err)
		}
		argument = &AiContextProviderArgument{Required: entry.Argument.Required, Placeholder: placeholder, PlaceholderLocalized: placeholderLocalized}
	}
	return AiContextProviderContribution{
		ID: id, Mention: mention, Title: title, TitleLocalized: titleLocalized,
		Description: description, DescriptionLocalized: descriptionLocalized,
		Action: actionID, Argument: argument,
	}, id, nil
}

// finalizeAiContextProviderEntries keeps the first entry for each mention; a
// later entry reusing a mention becomes an issue.
func finalizeAiContextProviderEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	seen := map[string]string{}
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		provider := entry.Value.(AiContextProviderContribution)
		if first, exists := seen[provider.Mention]; exists {
			issues = append(issues, ContributionIssue{
				Point: aiContextProviderPointKey, Entry: entry.Index, ID: entry.ID,
				Message: fmt.Sprintf("mention %q is already used by provider %q", provider.Mention, first),
			})
			continue
		}
		seen[provider.Mention] = provider.ID
		kept = append(kept, entry)
	}
	return kept, issues
}

// aiContextProvidersExpandPermission reports a development generation that
// runs new code on the user's behalf: a provider was added or changed its
// action.
func aiContextProvidersExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	known := map[string]string{}
	if current != nil {
		for _, entry := range current.Entries {
			if provider, ok := entry.(AiContextProviderContribution); ok {
				known[provider.ID] = provider.Action
			}
		}
	}
	for _, entry := range candidate.Entries {
		provider, ok := entry.(AiContextProviderContribution)
		if !ok {
			continue
		}
		if action, exists := known[provider.ID]; !exists || action != provider.Action {
			return true
		}
	}
	return false
}
