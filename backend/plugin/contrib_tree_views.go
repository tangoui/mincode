package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Sidebar tree views (contributes.treeViews) are a process-only contribution
// point. An entry names one of the plugin's own read-risk actions; when the
// user expands the view (or one of its items) in the project sidebar "Views"
// tab the host calls the action through plugin_host_tree_view
// (tree_view_invoke.go) and shows the validated children. Mirrors:
// tools/mincode-cli/provider_points.go (CLI), assertTreeViewsEntry (cloud) and
// vue-app/src/domain/plugins/tree-views.ts (frontend revalidation).
const treeViewPointKey = "treeViews"

var (
	treeViewSpec          = mustContributionSpec(treeViewPointKey)
	treeViewTitleMaxRunes = int(treeViewSpec.Limit("titleMaxRunes"))
	treeViewLocalesMax    = int(treeViewSpec.Limit("localesMax"))

	treeViewIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type treeViewManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Action         string            `json:"action"`
}

// TreeViewContribution is the snapshot entry.
type TreeViewContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Action         string            `json:"action"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              treeViewPointKey,
		Decode:           decodeTreeViewEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate tree view id %q", id) },
		IssuePrefix:      "invalid treeViews contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped tree view %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.treeViews[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun: "tree views",
		ExpandsPermission: func(current, candidate *resolvedContribution) bool {
			return contributionActionsExpand(current, candidate, func(entry any) (string, []string, bool) {
				view, ok := entry.(TreeViewContribution)
				return view.ID, []string{view.Action}, ok
			})
		},
	})
}

func decodeTreeViewEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry treeViewManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse tree view: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !treeViewIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("tree view id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	title, err := validateContributionLine(entry.Title, treeViewTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("tree view %q title %w", id, err)
	}
	titleLocalized, err := validateContributionLocalized(entry.TitleLocalized, treeViewTitleMaxRunes, treeViewLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("tree view %q titleLocalized %w", id, err)
	}
	actionID, err := contributionReadActionRef(ctx, "tree view", id, "action", entry.Action)
	if err != nil {
		return nil, id, err
	}
	return TreeViewContribution{ID: id, Title: title, TitleLocalized: titleLocalized, Action: actionID}, id, nil
}

// contributionReadActionRef resolves an entry field naming one of the
// plugin's own contributes.actions with risk "read".
func contributionReadActionRef(ctx *pointContext, noun, id, field, value string) (string, error) {
	actionID, action, err := contributionActionRef(ctx, noun, id, field, value)
	if err != nil {
		return "", err
	}
	if action.Risk != "read" {
		return "", fmt.Errorf("%s %q %s %q must have risk \"read\", not %q", noun, id, field, actionID, action.Risk)
	}
	return actionID, nil
}

// contributionProviderActionRef resolves the "action" field of a data
// provider entry (sessionSources, scmCommitMessage, fileDecorations): one of
// the plugin's own contributes.actions with risk "read" that is not
// aiCallable. The action receives a host-built envelope, and the data it
// answers with (session transcripts, staged diffs, file names) must not be
// reachable by the model through the same action.
func contributionProviderActionRef(ctx *pointContext, noun, id, value string) (string, error) {
	actionID, err := contributionReadActionRef(ctx, noun, id, "action", value)
	if err != nil {
		return "", err
	}
	if ctx.actions[actionID].AICallable {
		return "", fmt.Errorf("%s %q action %q must not be aiCallable", noun, id, actionID)
	}
	return actionID, nil
}

// contributionActionRef resolves an entry field naming one of the plugin's
// own contributes.actions (any risk).
func contributionActionRef(ctx *pointContext, noun, id, field, value string) (string, ManifestUIAction, error) {
	actionID := strings.TrimSpace(value)
	action, ok := ctx.actions[actionID]
	if actionID == "" || !ok {
		return "", ManifestUIAction{}, fmt.Errorf("%s %q %s %q is not one of this plugin's contributes.actions", noun, id, field, actionID)
	}
	return actionID, action, nil
}

// contributionActionsExpand reports a development generation that sends
// data to new code: an entry was added or one of its actions changed.
// describe returns the entry id and its actions in a fixed order.
func contributionActionsExpand(current, candidate *resolvedContribution, describe func(entry any) (string, []string, bool)) bool {
	if candidate == nil {
		return false
	}
	known := map[string][]string{}
	if current != nil {
		for _, entry := range current.Entries {
			if id, actions, ok := describe(entry); ok {
				known[id] = actions
			}
		}
	}
	for _, entry := range candidate.Entries {
		id, actions, ok := describe(entry)
		if !ok {
			continue
		}
		previous, exists := known[id]
		if !exists || strings.Join(previous, "\x00") != strings.Join(actions, "\x00") {
			return true
		}
	}
	return false
}
