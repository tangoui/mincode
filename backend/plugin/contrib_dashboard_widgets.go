package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Dashboard widgets (contributes.dashboardWidgets) are process-plugin cards
// on the dashboard. A "view" widget embeds one of the plugin's
// contributes.views in the sandboxed plugin UI iframe (widget variant); a
// "data" widget calls one of the plugin's read-risk actions through
// invokeContributedAction (plugin_host_dashboard_widget_data) and the host
// renders the validated result as plain text. Nothing runs until a card is
// visible, refresh is bounded (refreshMinSeconds) and the user can hide or
// reorder cards per workspace. Mirrors: tools/mincode-cli/dashboard_widgets.go
// (CLI), assertDashboardWidgetsEntry (cloud) and
// vue-app/src/domain/plugins/dashboard-widgets.ts (frontend revalidation).
const (
	dashboardWidgetPointKey        = "dashboardWidgets"
	dashboardWidgetKindView        = "view"
	dashboardWidgetKindData        = "data"
	dashboardWidgetSizeSmall       = "small"
	dashboardWidgetIDPatternSource = "^[a-z0-9][a-z0-9-]{0,31}$"
)

var (
	dashboardWidgetSpec             = mustContributionSpec(dashboardWidgetPointKey)
	dashboardWidgetTitleMaxRunes    = int(dashboardWidgetSpec.Limit("titleMaxRunes"))
	dashboardWidgetLocalesMax       = int(dashboardWidgetSpec.Limit("localesMax"))
	dashboardWidgetRefreshMin       = int(dashboardWidgetSpec.Limit("refreshMinSeconds"))
	dashboardWidgetRefreshMax       = int(dashboardWidgetSpec.Limit("refreshMaxSeconds"))
	dashboardWidgetRefreshDefault   = int(dashboardWidgetSpec.Limit("refreshDefaultSeconds"))
	dashboardWidgetPayloadMaxBytes  = int(dashboardWidgetSpec.Limit("payloadMaxBytes"))
	dashboardWidgetResultMaxBytes   = int(dashboardWidgetSpec.Limit("resultMaxBytes"))
	dashboardWidgetItemsMax         = int(dashboardWidgetSpec.Limit("itemsMax"))
	dashboardWidgetLabelMaxRunes    = int(dashboardWidgetSpec.Limit("labelMaxRunes"))
	dashboardWidgetValueMaxRunes    = int(dashboardWidgetSpec.Limit("valueMaxRunes"))
	dashboardWidgetHintMaxRunes     = int(dashboardWidgetSpec.Limit("hintMaxRunes"))
	dashboardWidgetFootnoteMaxRunes = int(dashboardWidgetSpec.Limit("footnoteMaxRunes"))
	dashboardWidgetTimeout          = time.Duration(dashboardWidgetSpec.Limit("timeoutMs")) * time.Millisecond
	dashboardWidgetConcurrency      = int(dashboardWidgetSpec.Limit("concurrentPerPlugin"))
	dashboardWidgetKinds            = dashboardWidgetSetOf(dashboardWidgetSpec.List("kinds"))
	dashboardWidgetSizes            = dashboardWidgetSetOf(dashboardWidgetSpec.List("sizes"))
	dashboardWidgetSeverities       = dashboardWidgetSetOf(dashboardWidgetSpec.List("severities"))
	dashboardWidgetIDPattern        = regexp.MustCompile(dashboardWidgetIDPatternSource)
)

func dashboardWidgetSetOf(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

type dashboardWidgetManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Kind           string            `json:"kind"`
	View           *string           `json:"view,omitempty"`
	Action         *string           `json:"action,omitempty"`
	Size           *string           `json:"size,omitempty"`
	RefreshSeconds *int              `json:"refreshSeconds,omitempty"`
	Command        *string           `json:"command,omitempty"`
}

// DashboardWidgetContribution is the snapshot entry. RefreshSeconds is 0 for
// view widgets and for manual-only data widgets.
type DashboardWidgetContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Kind           string            `json:"kind"`
	View           string            `json:"view,omitempty"`
	Action         string            `json:"action,omitempty"`
	Size           string            `json:"size"`
	RefreshSeconds int               `json:"refreshSeconds"`
	Command        string            `json:"command,omitempty"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              dashboardWidgetPointKey,
		Decode:           decodeDashboardWidgetEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate dashboard widget id %q", id) },
		IssuePrefix:      "invalid dashboardWidgets contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped dashboard widget %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.dashboardWidgets[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "dashboard widgets",
		ExpandsPermission: dashboardWidgetsExpandPermission,
	})
}

func decodeDashboardWidgetEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry dashboardWidgetManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse dashboard widget: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !dashboardWidgetIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("dashboard widget id %q must match %s", id, dashboardWidgetIDPatternSource)
	}
	if ctx.manifest.Runtime.Type != "process" {
		return nil, id, fmt.Errorf("dashboard widget %q requires runtime.type process", id)
	}
	widget := DashboardWidgetContribution{ID: id, Kind: strings.TrimSpace(entry.Kind), Size: dashboardWidgetSizeSmall}
	var err error
	if widget.Title, err = validateContributionLine(entry.Title, dashboardWidgetTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("dashboard widget %q title %w", id, err)
	}
	if widget.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, dashboardWidgetTitleMaxRunes, dashboardWidgetLocalesMax); err != nil {
		return nil, id, fmt.Errorf("dashboard widget %q titleLocalized %w", id, err)
	}
	if !dashboardWidgetKinds[widget.Kind] {
		return nil, id, fmt.Errorf("dashboard widget %q kind must be one of %s", id, strings.Join(dashboardWidgetSpec.List("kinds"), ", "))
	}
	if entry.Size != nil {
		widget.Size = strings.TrimSpace(*entry.Size)
		if !dashboardWidgetSizes[widget.Size] {
			return nil, id, fmt.Errorf("dashboard widget %q size must be one of %s", id, strings.Join(dashboardWidgetSpec.List("sizes"), ", "))
		}
	}
	switch widget.Kind {
	case dashboardWidgetKindView:
		if entry.Action != nil || entry.RefreshSeconds != nil {
			return nil, id, fmt.Errorf("view dashboard widget %q must not declare action or refreshSeconds", id)
		}
		if entry.View == nil {
			return nil, id, fmt.Errorf("view dashboard widget %q needs view", id)
		}
		widget.View = strings.TrimSpace(*entry.View)
		if _, ok := ctx.views[widget.View]; !ok {
			return nil, id, fmt.Errorf("dashboard widget %q view %q is not one of this plugin's contributes.views", id, widget.View)
		}
	case dashboardWidgetKindData:
		if entry.View != nil {
			return nil, id, fmt.Errorf("data dashboard widget %q must not declare view", id)
		}
		if entry.Action == nil {
			return nil, id, fmt.Errorf("data dashboard widget %q needs action", id)
		}
		widget.Action = strings.TrimSpace(*entry.Action)
		action, ok := ctx.actions[widget.Action]
		if !ok {
			return nil, id, fmt.Errorf("dashboard widget %q action %q is not one of this plugin's contributes.actions", id, widget.Action)
		}
		if action.Risk != "read" {
			return nil, id, fmt.Errorf("dashboard widget %q action %q must have risk read (got %s)", id, widget.Action, action.Risk)
		}
		widget.RefreshSeconds = dashboardWidgetRefreshDefault
		if entry.RefreshSeconds != nil {
			refresh := *entry.RefreshSeconds
			if refresh != 0 && (refresh < dashboardWidgetRefreshMin || refresh > dashboardWidgetRefreshMax) {
				return nil, id, fmt.Errorf("dashboard widget %q refreshSeconds must be 0 or %d-%d", id, dashboardWidgetRefreshMin, dashboardWidgetRefreshMax)
			}
			widget.RefreshSeconds = refresh
		}
	}
	if entry.Command != nil {
		widget.Command = strings.TrimSpace(*entry.Command)
		if _, ok := ctx.commands[widget.Command]; widget.Command == "" || !ok {
			return nil, id, fmt.Errorf("dashboard widget %q command %q is not one of this plugin's contributes.commands", id, widget.Command)
		}
	}
	return widget, id, nil
}

// dashboardWidgetsExpandPermission: a data widget runs plugin code
// automatically while visible, so a development generation that adds a data
// widget or changes an existing one's action needs a new approval.
func dashboardWidgetsExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	actions := map[string]string{}
	if current != nil {
		for _, entry := range current.Entries {
			if widget, ok := entry.(DashboardWidgetContribution); ok && widget.Kind == dashboardWidgetKindData {
				actions[widget.ID] = widget.Action
			}
		}
	}
	for _, entry := range candidate.Entries {
		widget, ok := entry.(DashboardWidgetContribution)
		if !ok || widget.Kind != dashboardWidgetKindData {
			continue
		}
		if previous, exists := actions[widget.ID]; !exists || previous != widget.Action {
			return true
		}
	}
	return false
}

// dashboardWidgetLocked returns the plugin's current widget by id. The
// caller holds record.mu.
func dashboardWidgetLocked(record *pluginRecord, widgetID string) (DashboardWidgetContribution, bool) {
	if record.manifest == nil {
		return DashboardWidgetContribution{}, false
	}
	resolved := record.manifest.contributions[dashboardWidgetPointKey]
	if resolved == nil {
		return DashboardWidgetContribution{}, false
	}
	for _, entry := range resolved.Entries {
		if widget, ok := entry.(DashboardWidgetContribution); ok && widget.ID == widgetID {
			return widget, true
		}
	}
	return DashboardWidgetContribution{}, false
}

var errDashboardWidgetGone = errors.New("dashboard widget is no longer contributed")
