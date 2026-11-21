package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Command palette search providers (contributes.searchProviders) are a
// process-only contribution point. An entry reserves a palette prefix
// ("jira:") and names one of the plugin's own read-risk actions; while the
// user types that prefix the host calls the action through plugin_host_search
// (search_provider_invoke.go) with the typed query and shows the validated
// results. Mirrors: tools/mincode-cli/search_providers.go (CLI),
// assertSearchProvidersEntry (cloud) and
// vue-app/src/domain/plugins/search-providers.ts (frontend revalidation).
const searchProviderPointKey = "searchProviders"

var (
	searchProviderSpec                = mustContributionSpec(searchProviderPointKey)
	searchProviderTitleMaxRunes       = int(searchProviderSpec.Limit("titleMaxRunes"))
	searchProviderLocalesMax          = int(searchProviderSpec.Limit("localesMax"))
	searchProviderPlaceholderMaxRunes = int(searchProviderSpec.Limit("placeholderMaxRunes"))
	searchProviderMinQueryLengthMax   = int(searchProviderSpec.Limit("minQueryLengthMax"))

	searchProviderIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	searchProviderPrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,15}$`)
	searchProviderReserved      = contributionListSet(searchProviderSpec.List("reservedPrefixes"))
)

type searchProviderManifestEntry struct {
	ID                   string            `json:"id"`
	Prefix               string            `json:"prefix"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized,omitempty"`
	Placeholder          string            `json:"placeholder,omitempty"`
	PlaceholderLocalized map[string]string `json:"placeholderLocalized,omitempty"`
	Action               string            `json:"action"`
	MinQueryLength       *int              `json:"minQueryLength,omitempty"`
}

// SearchProviderContribution is the snapshot entry.
type SearchProviderContribution struct {
	ID                   string            `json:"id"`
	Prefix               string            `json:"prefix"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized,omitempty"`
	Placeholder          string            `json:"placeholder,omitempty"`
	PlaceholderLocalized map[string]string `json:"placeholderLocalized,omitempty"`
	Action               string            `json:"action"`
	MinQueryLength       int               `json:"minQueryLength"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              searchProviderPointKey,
		Decode:           decodeSearchProviderEntry,
		Finalize:         finalizeSearchProviderEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate search provider id %q", id) },
		IssuePrefix:      "invalid searchProviders contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped search provider %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.searchProviders[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "search providers",
		ExpandsPermission: searchProvidersExpandPermission,
	})
}

func contributionListSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func decodeSearchProviderEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry searchProviderManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse search provider: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !searchProviderIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("search provider id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	prefix := entry.Prefix
	if !searchProviderPrefixPattern.MatchString(prefix) {
		return nil, id, fmt.Errorf("search provider %q prefix %q must match ^[a-z][a-z0-9-]{1,15}$", id, prefix)
	}
	if searchProviderReserved[prefix] {
		return nil, id, fmt.Errorf("search provider %q prefix %q is reserved", id, prefix)
	}
	title, err := validateContributionLine(entry.Title, searchProviderTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("search provider %q title %w", id, err)
	}
	titleLocalized, err := validateContributionLocalized(entry.TitleLocalized, searchProviderTitleMaxRunes, searchProviderLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("search provider %q titleLocalized %w", id, err)
	}
	placeholder, err := validateContributionLine(entry.Placeholder, searchProviderPlaceholderMaxRunes, false)
	if err != nil {
		return nil, id, fmt.Errorf("search provider %q placeholder %w", id, err)
	}
	placeholderLocalized, err := validateContributionLocalized(entry.PlaceholderLocalized, searchProviderPlaceholderMaxRunes, searchProviderLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("search provider %q placeholderLocalized %w", id, err)
	}
	actionID := strings.TrimSpace(entry.Action)
	action, ok := ctx.actions[actionID]
	if actionID == "" || !ok {
		return nil, id, fmt.Errorf("search provider %q action %q is not one of this plugin's contributes.actions", id, actionID)
	}
	if action.Risk != "read" {
		return nil, id, fmt.Errorf("search provider %q action %q must have risk \"read\", not %q", id, actionID, action.Risk)
	}
	minQueryLength := 1
	if entry.MinQueryLength != nil {
		minQueryLength = *entry.MinQueryLength
		if minQueryLength < 0 || minQueryLength > searchProviderMinQueryLengthMax {
			return nil, id, fmt.Errorf("search provider %q minQueryLength must be 0-%d", id, searchProviderMinQueryLengthMax)
		}
	}
	return SearchProviderContribution{
		ID: id, Prefix: prefix, Title: title, TitleLocalized: titleLocalized,
		Placeholder: placeholder, PlaceholderLocalized: placeholderLocalized,
		Action: actionID, MinQueryLength: minQueryLength,
	}, id, nil
}

// finalizeSearchProviderEntries rejects a prefix repeated within the plugin;
// the first declaration keeps it. Conflicts across plugins are resolved by
// the frontend (smallest plugin id wins).
func finalizeSearchProviderEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	seen := map[string]string{}
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		provider := entry.Value.(SearchProviderContribution)
		if owner, duplicate := seen[provider.Prefix]; duplicate {
			issues = append(issues, ContributionIssue{
				Point: searchProviderPointKey, Entry: entry.Index, ID: entry.ID,
				Message: fmt.Sprintf("prefix %q is already used by search provider %q", provider.Prefix, owner),
			})
			continue
		}
		seen[provider.Prefix] = provider.ID
		kept = append(kept, entry)
	}
	return kept, issues
}

// searchProvidersExpandPermission reports a development generation that
// sends typed queries to new code: a provider was added or changed its action.
func searchProvidersExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	known := map[string]string{}
	if current != nil {
		for _, entry := range current.Entries {
			if provider, ok := entry.(SearchProviderContribution); ok {
				known[provider.ID] = provider.Action
			}
		}
	}
	for _, entry := range candidate.Entries {
		provider, ok := entry.(SearchProviderContribution)
		if !ok {
			continue
		}
		if action, exists := known[provider.ID]; !exists || action != provider.Action {
			return true
		}
	}
	return false
}
