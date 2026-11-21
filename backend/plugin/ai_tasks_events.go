package plugin

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/agentprogress"
)

// ai.tasks tells subscribed plugins (capability ai.tasks) which AI assistant
// and native agent tasks exist, so a plugin can list them the way the task
// button in the main window does: a random run ID, the source, the phase, a
// truncated title and a truncated status summary per task. Sub-agent runs are
// folded into their top-level task, finished tasks stay listed for
// aiTasksRecent, and at most aiTasksMaxTasks tasks are sent.
//
// Any change, including a new summary in the same phase, is coalesced for
// aiTasksThrottle and then published as a full snapshot with a sequence
// number; deliveries may arrive out of order, so a plugin keeps only the
// event with the highest seq. While any task is listed the host repeats the
// snapshot every aiTasksSnapshotInterval, so finished tasks age out and a
// plugin that missed an event recovers.
//
// Conversation text, tool names and arguments, error codes, approval IDs,
// task IDs, branches, worktrees, session, conversation and workspace
// identifiers never leave the host.
const (
	hostEventAITasks    = "ai.tasks"
	aiTasksMaxTasks     = 20
	aiTasksTitleRunes   = 80
	aiTasksSummaryRunes = 160
	aiTasksRecent       = 10 * time.Minute
)

type aiTask struct {
	RunID     string `json:"runId"`
	Source    string `json:"source"`
	Phase     string `json:"phase"`
	Title     string `json:"title"`
	Summary   string `json:"summary,omitempty"`
	UpdatedAt int64  `json:"updatedAt"`
}

type aiTasksEvent struct {
	Seq   uint64   `json:"seq"`
	Tasks []aiTask `json:"tasks"`
}

var (
	aiTasksMu    sync.Mutex
	aiTasksSeq   uint64
	aiTasksTimer *time.Timer
	aiTasksDue   time.Time

	// Variables so tests need not touch the run registry or the plugin manager.
	aiTasksRuns             = agentprogress.Runs
	aiTasksPublish          = func(event aiTasksEvent) { host().publishHostEvent(hostEventAITasks, event) }
	aiTasksNow              = time.Now
	aiTasksThrottle         = 750 * time.Millisecond
	aiTasksSnapshotInterval = time.Minute
)

// aiTasksChanged schedules a snapshot after a change of an assistant or agent run.
func aiTasksChanged(run agentprogress.AgentRun) {
	if _, known := aiActivitySources[run.Source]; !known || run.RunID == "" {
		return
	}
	aiTasksMu.Lock()
	aiTasksScheduleLocked(aiTasksThrottle)
	aiTasksMu.Unlock()
}

// aiTasksScheduleLocked publishes a snapshot after delay unless one is already
// due no later than that.
func aiTasksScheduleLocked(delay time.Duration) {
	due := aiTasksNow().Add(delay)
	if aiTasksTimer != nil {
		if !aiTasksDue.After(due) {
			return
		}
		aiTasksTimer.Stop()
	}
	aiTasksDue = due
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		// AfterFunc may run before its return value is assigned. Read the
		// owner only after taking the same lock as schedule/cancel/repeat.
		aiTasksMu.Lock()
		defer aiTasksMu.Unlock()
		aiTasksFireLocked(timer)
	})
	aiTasksTimer = timer
}

// aiTasksFireLocked publishes timer's snapshot unless it was replaced/canceled.
// The caller holds aiTasksMu, including while reading the timer owner.
func aiTasksFireLocked(timer *time.Timer) {
	if aiTasksTimer != timer {
		return
	}
	aiTasksTimer = nil
	aiTasksSeq++
	event := aiTasksEvent{Seq: aiTasksSeq, Tasks: aiTasksSnapshot()}
	aiTasksPublish(event)
	if len(event.Tasks) > 0 {
		aiTasksScheduleLocked(aiTasksSnapshotInterval)
	}
}

// aiTasksSnapshot lists the top-level assistant and agent tasks that are
// unfinished or finished within aiTasksRecent, most recently updated first.
func aiTasksSnapshot() []aiTask {
	all := aiTasksRuns()
	known := make(map[string]bool, len(all))
	for _, run := range all {
		known[run.RunID] = true
	}
	cutoff := aiTasksNow().Add(-aiTasksRecent).UnixMilli()
	tasks := make([]aiTask, 0, min(len(all), aiTasksMaxTasks))
	for _, run := range all {
		source, ok := aiActivitySources[run.Source]
		if !ok || run.RunID == "" || known[run.ParentRunID] {
			continue
		}
		if run.Progress.CompletedAt != nil && *run.Progress.CompletedAt < cutoff {
			continue
		}
		tasks = append(tasks, aiTask{
			RunID:     run.RunID,
			Source:    source,
			Phase:     run.Progress.Phase,
			Title:     aiTasksText(run.Title, aiTasksTitleRunes),
			Summary:   aiTasksText(run.Progress.Summary, aiTasksSummaryRunes),
			UpdatedAt: run.Progress.UpdatedAt,
		})
		if len(tasks) == aiTasksMaxTasks {
			break
		}
	}
	return tasks
}

// aiTasksText collapses whitespace and truncates value to maxRunes runes.
func aiTasksText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.ToValidUTF8(value, "")), " ")
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes-1]) + "…"
}

func init() {
	agentprogress.Observe(aiTasksChanged)
}
