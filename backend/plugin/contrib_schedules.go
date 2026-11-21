package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Schedules (contributes.schedules) let a process plugin be woken up
// periodically: the host calls one of the plugin's read actions every
// intervalMinutes while the plugin is enabled and effective in the active
// workspace (schedules_runner.go). The action tells the user whatever needs
// telling through host.notify / host.statusBar itself. Mirrors:
// tools/mincode-cli/schedules.go (CLI), assertSchedulesEntry (cloud) and
// vue-app/src/domain/plugins/schedules.ts (frontend revalidation).
const (
	schedulePointKey        = "schedules"
	scheduleIDPatternSource = "^[a-z0-9][a-z0-9-]{0,31}$"
)

var (
	scheduleSpec            = mustContributionSpec(schedulePointKey)
	scheduleTitleMaxRunes   = int(scheduleSpec.Limit("titleMaxRunes"))
	scheduleLocalesMax      = int(scheduleSpec.Limit("localesMax"))
	scheduleIntervalMin     = int(scheduleSpec.Limit("intervalMinMinutes"))
	scheduleIntervalMax     = int(scheduleSpec.Limit("intervalMaxMinutes"))
	schedulePayloadMinBytes = int(scheduleSpec.Limit("payloadMinBytes"))
	scheduleTimeout         = time.Duration(scheduleSpec.Limit("timeoutMs")) * time.Millisecond
	scheduleResultMaxBytes  = int(scheduleSpec.Limit("resultMaxBytes"))
	scheduleConcurrency     = int(scheduleSpec.Limit("concurrentPerPlugin"))
	scheduleBackoffMax      = time.Duration(scheduleSpec.Limit("backoffMaxMinutes")) * time.Minute
	scheduleIDPattern       = regexp.MustCompile(scheduleIDPatternSource)
)

type scheduleManifestEntry struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	TitleLocalized  map[string]string `json:"titleLocalized,omitempty"`
	Action          string            `json:"action"`
	IntervalMinutes int               `json:"intervalMinutes"`
}

// ScheduleContribution is the snapshot entry.
type ScheduleContribution struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	TitleLocalized  map[string]string `json:"titleLocalized,omitempty"`
	Action          string            `json:"action"`
	IntervalMinutes int               `json:"intervalMinutes"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              schedulePointKey,
		Decode:           decodeScheduleEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate schedule id %q", id) },
		IssuePrefix:      "invalid schedules contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped schedule %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.schedules[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "schedules",
		ExpandsPermission: schedulesExpandPermission,
	})
}

func decodeScheduleEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry scheduleManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse schedule: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !scheduleIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("schedule id %q must match %s", id, scheduleIDPatternSource)
	}
	if ctx.manifest.Runtime.Type != "process" {
		return nil, id, fmt.Errorf("schedule %q requires runtime.type process", id)
	}
	schedule := ScheduleContribution{ID: id, Action: strings.TrimSpace(entry.Action), IntervalMinutes: entry.IntervalMinutes}
	var err error
	if schedule.Title, err = validateContributionLine(entry.Title, scheduleTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("schedule %q title %w", id, err)
	}
	if schedule.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, scheduleTitleMaxRunes, scheduleLocalesMax); err != nil {
		return nil, id, fmt.Errorf("schedule %q titleLocalized %w", id, err)
	}
	action, ok := ctx.actions[schedule.Action]
	if !ok {
		return nil, id, fmt.Errorf("schedule %q action %q is not one of this plugin's contributes.actions", id, schedule.Action)
	}
	if action.Risk != "read" {
		return nil, id, fmt.Errorf("schedule %q action %q must have risk read (got %s)", id, schedule.Action, action.Risk)
	}
	if action.MaxPayloadBytes < schedulePayloadMinBytes {
		return nil, id, fmt.Errorf("schedule %q action %q needs maxPayloadBytes of at least %d", id, schedule.Action, schedulePayloadMinBytes)
	}
	if schedule.IntervalMinutes < scheduleIntervalMin || schedule.IntervalMinutes > scheduleIntervalMax {
		return nil, id, fmt.Errorf("schedule %q intervalMinutes must be %d-%d", id, scheduleIntervalMin, scheduleIntervalMax)
	}
	return schedule, id, nil
}

// schedulesExpandPermission: a schedule runs plugin code without a user
// action, so a development generation that adds one, shortens its interval
// or changes its action needs a new approval.
func schedulesExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	previous := map[string]ScheduleContribution{}
	if current != nil {
		for _, entry := range current.Entries {
			if schedule, ok := entry.(ScheduleContribution); ok {
				previous[schedule.ID] = schedule
			}
		}
	}
	for _, entry := range candidate.Entries {
		schedule, ok := entry.(ScheduleContribution)
		if !ok {
			continue
		}
		old, exists := previous[schedule.ID]
		if !exists || old.Action != schedule.Action || schedule.IntervalMinutes < old.IntervalMinutes {
			return true
		}
	}
	return false
}

// schedulesLocked returns the plugin's current schedules. The caller holds record.mu.
func schedulesLocked(record *pluginRecord) []ScheduleContribution {
	if record.manifest == nil {
		return nil
	}
	resolved := record.manifest.contributions[schedulePointKey]
	if resolved == nil {
		return nil
	}
	out := make([]ScheduleContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if schedule, ok := entry.(ScheduleContribution); ok {
			out = append(out, schedule)
		}
	}
	return out
}
