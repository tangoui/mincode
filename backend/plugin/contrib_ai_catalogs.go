package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// AI catalogs (contributes.aiCatalogs) are a process-only contribution point.
// An entry adds a searchable source to the "Discover" tab of Settings → AI →
// Connectors or Skills. The host calls searchAction through
// plugin_host_ai_catalog_search while the user types and installAction through
// plugin_host_ai_catalog_install only when the user adds an item
// (ai_catalog_invoke.go). The plugin returns data; the host never fetches a
// URL the plugin names, and an installed item becomes ordinary user
// configuration. Mirrors: tools/mincode-cli/ai_catalogs.go (CLI),
// assertAiCatalogsEntry (cloud) and vue-app/src/domain/plugins/ai-catalogs.ts
// (frontend revalidation).
const aiCatalogPointKey = "aiCatalogs"

var (
	aiCatalogSpec           = mustContributionSpec(aiCatalogPointKey)
	aiCatalogTitleMaxRunes  = int(aiCatalogSpec.Limit("titleMaxRunes"))
	aiCatalogDescriptionMax = int(aiCatalogSpec.Limit("descriptionMaxRunes"))
	aiCatalogLocalesMax     = int(aiCatalogSpec.Limit("localesMax"))
	aiCatalogKinds          = contributionListSet(aiCatalogSpec.List("kinds"))
	aiCatalogIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type aiCatalogManifestEntry struct {
	ID                   string            `json:"id"`
	Kind                 string            `json:"kind"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized,omitempty"`
	Description          string            `json:"description,omitempty"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized,omitempty"`
	SearchAction         string            `json:"searchAction"`
	InstallAction        string            `json:"installAction"`
}

// AiCatalogContribution is the snapshot entry.
type AiCatalogContribution struct {
	ID                   string            `json:"id"`
	Kind                 string            `json:"kind"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized,omitempty"`
	Description          string            `json:"description,omitempty"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized,omitempty"`
	SearchAction         string            `json:"searchAction"`
	InstallAction        string            `json:"installAction"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              aiCatalogPointKey,
		Decode:           decodeAiCatalogEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate AI catalog id %q", id) },
		IssuePrefix:      "invalid aiCatalogs contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped AI catalog %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.aiCatalogs[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "AI catalogs",
		ExpandsPermission: aiCatalogsExpandPermission,
	})
}

func decodeAiCatalogEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry aiCatalogManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse AI catalog: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !aiCatalogIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("AI catalog id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	if !aiCatalogKinds[entry.Kind] {
		return nil, id, fmt.Errorf("AI catalog %q kind %q must be one of %s", id, entry.Kind, strings.Join(aiCatalogSpec.List("kinds"), ", "))
	}
	title, err := validateContributionLine(entry.Title, aiCatalogTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("AI catalog %q title %w", id, err)
	}
	titleLocalized, err := validateContributionLocalized(entry.TitleLocalized, aiCatalogTitleMaxRunes, aiCatalogLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("AI catalog %q titleLocalized %w", id, err)
	}
	description, err := validateContributionLine(entry.Description, aiCatalogDescriptionMax, false)
	if err != nil {
		return nil, id, fmt.Errorf("AI catalog %q description %w", id, err)
	}
	descriptionLocalized, err := validateContributionLocalized(entry.DescriptionLocalized, aiCatalogDescriptionMax, aiCatalogLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("AI catalog %q descriptionLocalized %w", id, err)
	}
	searchAction, err := aiCatalogActionReference(ctx, id, "searchAction", entry.SearchAction)
	if err != nil {
		return nil, id, err
	}
	installAction, err := aiCatalogActionReference(ctx, id, "installAction", entry.InstallAction)
	if err != nil {
		return nil, id, err
	}
	return AiCatalogContribution{
		ID: id, Kind: entry.Kind, Title: title, TitleLocalized: titleLocalized,
		Description: description, DescriptionLocalized: descriptionLocalized,
		SearchAction: searchAction, InstallAction: installAction,
	}, id, nil
}

// aiCatalogActionReference accepts only the plugin's own read-risk actions
// that the model cannot call: a catalog is user-driven discovery, never a
// tool.
func aiCatalogActionReference(ctx *pointContext, id, field, value string) (string, error) {
	actionID := strings.TrimSpace(value)
	action, ok := ctx.actions[actionID]
	if actionID == "" || !ok {
		return "", fmt.Errorf("AI catalog %q %s %q is not one of this plugin's contributes.actions", id, field, actionID)
	}
	if action.Risk != "read" {
		return "", fmt.Errorf("AI catalog %q %s %q must have risk \"read\", not %q", id, field, actionID, action.Risk)
	}
	if action.AICallable {
		return "", fmt.Errorf("AI catalog %q %s %q must not be aiCallable", id, field, actionID)
	}
	return actionID, nil
}

// aiCatalogsExpandPermission reports a development generation that sends
// queries or install requests to new code: a catalog was added or changed its
// kind or actions.
func aiCatalogsExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	known := map[string]AiCatalogContribution{}
	if current != nil {
		for _, entry := range current.Entries {
			if catalog, ok := entry.(AiCatalogContribution); ok {
				known[catalog.ID] = catalog
			}
		}
	}
	for _, entry := range candidate.Entries {
		catalog, ok := entry.(AiCatalogContribution)
		if !ok {
			continue
		}
		previous, exists := known[catalog.ID]
		if !exists || previous.Kind != catalog.Kind || previous.SearchAction != catalog.SearchAction || previous.InstallAction != catalog.InstallAction {
			return true
		}
	}
	return false
}
