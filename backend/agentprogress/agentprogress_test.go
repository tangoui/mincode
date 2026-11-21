package agentprogress

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func newTestRegistry() *registry {
	return &registry{runs: make(map[string]AgentRun)}
}

func validStartArgs() startArgs {
	return startArgs{
		SessionID:        "session-1",
		WorkspaceID:      "workspace-1",
		WorkspaceScopeID: "workspace:1",
		Source:           "agentic",
		Title:            "Refactor API key=secret-value",
		Phase:            PhaseThinking,
		Summary:          "Authorization: Bearer abcdefghijklmnop",
		Navigation: Navigation{
			Type:           "ai-conversation",
			ConversationID: "conversation-1",
		},
	}
}

func TestRunIdentityAndIndependentUpdates(t *testing.T) {
	registry := newTestRegistry()
	first, err := registry.start(validStartArgs())
	if err != nil {
		t.Fatal(err)
	}
	secondArgs := validStartArgs()
	secondArgs.SessionID = "session-2"
	secondArgs.Navigation.ConversationID = "conversation-2"
	second, err := registry.start(secondArgs)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID || first.SessionID == second.SessionID {
		t.Fatalf("runs must have independent stable identities: %#v %#v", first, second)
	}

	updated, changed, err := registry.update(updateArgs{RunID: first.RunID, Phase: PhaseWaitingApproval})
	if err != nil || !changed {
		t.Fatalf("update failed: changed=%v err=%v", changed, err)
	}
	if updated.Progress.Phase != PhaseWaitingApproval {
		t.Fatalf("unexpected phase: %s", updated.Progress.Phase)
	}
	listed := registry.list()
	for _, run := range listed {
		if run.RunID == second.RunID && run.Progress.Phase != PhaseThinking {
			t.Fatalf("updating one run changed another: %#v", run)
		}
	}
}

func TestTerminalRunIsImmutableAndCancellationIsPerRun(t *testing.T) {
	registry := newTestRegistry()
	first, _ := registry.start(validStartArgs())
	secondArgs := validStartArgs()
	secondArgs.SessionID = "session-2"
	secondArgs.Navigation.ConversationID = "conversation-2"
	second, _ := registry.start(secondArgs)

	cancelled, changed, err := registry.requestCancel(first.RunID)
	if err != nil || !changed || cancelled.Progress.Phase != PhaseCancelling {
		t.Fatalf("cancel request failed: %#v changed=%v err=%v", cancelled, changed, err)
	}
	if cancelled.Progress.CancelRequested != true {
		t.Fatal("cancel request flag was not preserved")
	}
	completed, _, err := registry.update(updateArgs{RunID: first.RunID, Phase: PhaseCancelled})
	if err != nil || completed.Progress.CompletedAt == nil {
		t.Fatalf("terminal update failed: %#v err=%v", completed, err)
	}
	unchanged, changed, err := registry.update(updateArgs{RunID: first.RunID, Phase: PhaseStreaming})
	if err != nil || changed || unchanged.Progress.Phase != PhaseCancelled {
		t.Fatalf("terminal run changed: %#v changed=%v err=%v", unchanged, changed, err)
	}
	for _, run := range registry.list() {
		if run.RunID == second.RunID && run.Progress.CancelRequested {
			t.Fatal("cancellation leaked to another run")
		}
	}
}

func TestCancellingRunIgnoresLateNonTerminalProgress(t *testing.T) {
	registry := newTestRegistry()
	run, err := registry.start(validStartArgs())
	if err != nil {
		t.Fatal(err)
	}

	cancelling, changed, err := registry.update(updateArgs{RunID: run.RunID, Phase: PhaseCancelling})
	if err != nil || !changed || !cancelling.Progress.CancelRequested {
		t.Fatalf("cancelling update failed: %#v changed=%v err=%v", cancelling, changed, err)
	}
	revision := cancelling.Revision

	unchanged, changed, err := registry.update(updateArgs{RunID: run.RunID, Phase: PhaseStreaming})
	if err != nil || changed || unchanged.Progress.Phase != PhaseCancelling || unchanged.Revision != revision {
		t.Fatalf("late progress regressed cancelling run: %#v changed=%v err=%v", unchanged, changed, err)
	}
	if _, changed := registry.updateActiveSession(run.SessionID, run.Source, PhaseToolRunning, "late tool event"); changed {
		t.Fatal("native late progress regressed cancelling run")
	}

	failed, changed, err := registry.update(updateArgs{RunID: run.RunID, Phase: PhaseFailed})
	if err != nil || !changed || failed.Progress.Phase != PhaseFailed {
		t.Fatalf("terminal update after cancellation failed: %#v changed=%v err=%v", failed, changed, err)
	}
}

func TestCancellationFallbackSettlesOrphanedRun(t *testing.T) {
	registry := newTestRegistry()
	run, err := registry.start(validStartArgs())
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := registry.requestCancel(run.RunID); err != nil || !changed {
		t.Fatalf("cancel request failed: changed=%v err=%v", changed, err)
	}

	settled, changed := registry.settleCancellation(run.RunID)
	if !changed || settled.Progress.Phase != PhaseCancelled || settled.Progress.CompletedAt == nil {
		t.Fatalf("orphaned cancellation did not settle: %#v changed=%v", settled, changed)
	}
	if _, changed := registry.settleCancellation(run.RunID); changed {
		t.Fatal("terminal cancellation must not settle twice")
	}
}

func TestCancellationFallbackDoesNotOverrideTerminalResult(t *testing.T) {
	registry := newTestRegistry()
	run, err := registry.start(validStartArgs())
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := registry.requestCancel(run.RunID); err != nil || !changed {
		t.Fatalf("cancel request failed: changed=%v err=%v", changed, err)
	}
	failed, changed, err := registry.update(updateArgs{RunID: run.RunID, Phase: PhaseFailed})
	if err != nil || !changed {
		t.Fatalf("terminal update failed: %#v changed=%v err=%v", failed, changed, err)
	}

	settled, changed := registry.settleCancellation(run.RunID)
	if changed || settled.Progress.Phase != PhaseFailed {
		t.Fatalf("fallback overwrote terminal result: %#v changed=%v", settled, changed)
	}
}

func TestSafeTextRedactsSecretsAndBoundsLength(t *testing.T) {
	registry := newTestRegistry()
	run, err := registry.start(validStartArgs())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(run.Title, "secret-value") || strings.Contains(run.Progress.Summary, "abcdefghijklmnop") {
		t.Fatalf("secret was not redacted: %#v", run)
	}
	long := strings.Repeat("界", 400)
	updated, _, err := registry.update(updateArgs{RunID: run.RunID, Phase: PhaseStreaming, Summary: &long})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(updated.Progress.Summary)) != 240 {
		t.Fatalf("summary length = %d", len([]rune(updated.Progress.Summary)))
	}
}

func TestOnlyTerminalRunsCanBeRemoved(t *testing.T) {
	registry := newTestRegistry()
	run, err := registry.start(validStartArgs())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.remove(run.RunID); err == nil {
		t.Fatal("active run removal must be rejected")
	}
	if _, _, err := registry.update(updateArgs{RunID: run.RunID, Phase: PhaseCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := registry.remove(run.RunID); err != nil {
		t.Fatalf("terminal run removal failed: %v", err)
	}
}

func TestPruneRetainsActiveRunsAndBoundsTerminalHistory(t *testing.T) {
	registry := newTestRegistry()
	now := time.Now().UnixMilli()
	oldCompletedAt := now - completedRetention.Milliseconds() - 1
	oldFailedAt := now - failedRetention.Milliseconds() - 1
	registry.runs["active"] = AgentRun{
		RunID: "active",
		Progress: SessionProgress{
			Phase:     PhaseStreaming,
			CreatedAt: oldFailedAt,
			UpdatedAt: oldFailedAt,
		},
	}
	registry.runs["old-completed"] = AgentRun{
		RunID: "old-completed",
		Progress: SessionProgress{
			Phase:       PhaseCompleted,
			CreatedAt:   oldCompletedAt,
			UpdatedAt:   oldCompletedAt,
			CompletedAt: &oldCompletedAt,
		},
	}
	registry.runs["old-failed"] = AgentRun{
		RunID: "old-failed",
		Progress: SessionProgress{
			Phase:       PhaseFailed,
			CreatedAt:   oldFailedAt,
			UpdatedAt:   oldFailedAt,
			CompletedAt: &oldFailedAt,
		},
	}
	for index := 0; index < maxRetainedRuns+5; index++ {
		updatedAt := now - int64(maxRetainedRuns-index)
		registry.runs[fmt.Sprintf("terminal-%03d", index)] = AgentRun{
			RunID: fmt.Sprintf("terminal-%03d", index),
			Progress: SessionProgress{
				Phase:       PhaseCompleted,
				CreatedAt:   updatedAt,
				UpdatedAt:   updatedAt,
				CompletedAt: &updatedAt,
			},
		}
	}

	registry.pruneLocked(now)
	if _, ok := registry.runs["active"]; !ok {
		t.Fatal("active run was pruned")
	}
	if _, ok := registry.runs["old-completed"]; ok {
		t.Fatal("expired completed run was retained")
	}
	if _, ok := registry.runs["old-failed"]; ok {
		t.Fatal("expired failed run was retained")
	}
	if len(registry.runs) != maxRetainedRuns {
		t.Fatalf("retained run count = %d, want %d", len(registry.runs), maxRetainedRuns)
	}
}

func TestSubagentMetadataRequiresParentAndRoundTrips(t *testing.T) {
	registry := newTestRegistry()
	invalid := validStartArgs()
	invalid.TaskID = "child"
	if _, err := registry.start(invalid); err == nil {
		t.Fatal("subagent metadata without parentRunId must be rejected")
	}

	child := validStartArgs()
	child.ParentRunID = "parent-run"
	child.TaskID = "child"
	child.Branch = "mincode/agent/child"
	child.WorktreeName = "child-worktree"
	run, err := registry.start(child)
	if err != nil {
		t.Fatal(err)
	}
	if run.ParentRunID != child.ParentRunID || run.TaskID != child.TaskID ||
		run.Branch != child.Branch || run.WorktreeName != child.WorktreeName {
		t.Fatalf("subagent metadata did not round trip: %#v", run)
	}
}

func TestStepProgressIsClampedAndClearedByLaterUpdates(t *testing.T) {
	registry := newTestRegistry()
	run, err := registry.start(validStartArgs())
	if err != nil {
		t.Fatal(err)
	}
	updated, _, err := registry.update(updateArgs{RunID: run.RunID, Phase: PhaseToolRunning, StepsCompleted: 2, StepsTotal: 5})
	if err != nil || updated.Progress.StepsCompleted != 2 || updated.Progress.StepsTotal != 5 {
		t.Fatalf("steps not stored: %#v err=%v", updated.Progress, err)
	}
	clamped, _, err := registry.update(updateArgs{RunID: run.RunID, Phase: PhaseToolRunning, StepsCompleted: 9, StepsTotal: 5})
	if err != nil || clamped.Progress.StepsCompleted != 5 {
		t.Fatalf("completed steps must be clamped to total: %#v err=%v", clamped.Progress, err)
	}
	cleared, _, err := registry.update(updateArgs{RunID: run.RunID, Phase: PhaseStreaming})
	if err != nil || cleared.Progress.StepsCompleted != 0 || cleared.Progress.StepsTotal != 0 {
		t.Fatalf("updates without steps must clear them: %#v err=%v", cleared.Progress, err)
	}
}
