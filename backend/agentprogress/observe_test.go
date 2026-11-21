package agentprogress

import (
	"encoding/json"
	"testing"
	"time"
)

func TestObserversSeePublishedRunsAndSummaryCountsActive(t *testing.T) {
	previous := runs.runs
	runs.runs = make(map[string]AgentRun)
	t.Cleanup(func() { runs.runs = previous })
	var seen []string
	Observe(func(run AgentRun) { seen = append(seen, run.Progress.Phase) })

	args, _ := json.Marshal(validStartArgs())
	if _, err := startHandler("", string(args)); err != nil {
		t.Fatal(err)
	}
	if activity := CurrentActivity(); activity != (Activity{Active: 1, Thinking: 1}) {
		t.Fatalf("activity after start = %+v", activity)
	}
	var runID string
	for id := range runs.runs {
		runID = id
	}
	update, _ := json.Marshal(updateArgs{RunID: runID, Phase: PhaseWaitingApproval})
	if _, err := updateHandler("", string(update)); err != nil {
		t.Fatal(err)
	}
	if activity := CurrentActivity(); activity != (Activity{Active: 1, Waiting: 1}) {
		t.Fatalf("activity while waiting = %+v", activity)
	}
	done, _ := json.Marshal(updateArgs{RunID: runID, Phase: PhaseCompleted})
	if _, err := updateHandler("", string(done)); err != nil {
		t.Fatal(err)
	}
	if activity := CurrentActivity(); activity != (Activity{}) {
		t.Fatalf("finished run still active: %+v", activity)
	}
	if len(seen) != 3 || seen[0] != PhaseThinking || seen[1] != PhaseWaitingApproval || seen[2] != PhaseCompleted {
		t.Fatalf("observed %v", seen)
	}
}

func TestCurrentActivityGroupsSubAgentsIntoTheirTask(t *testing.T) {
	previous := runs.runs
	t.Cleanup(func() { runs.runs = previous })
	run := func(id, parent, phase string) AgentRun {
		return AgentRun{RunID: id, ParentRunID: parent, Progress: SessionProgress{Phase: phase, UpdatedAt: time.Now().UnixMilli()}}
	}
	runs.runs = map[string]AgentRun{
		// Task A: the lead thinks while a sub-agent's sub-agent runs a tool.
		"a": run("a", "", PhaseThinking), "a1": run("a1", "a", PhaseStreaming), "a2": run("a2", "a1", PhaseToolRunning),
		// Task B: a sub-agent waits for approval.
		"b": run("b", "", PhaseToolRunning), "b1": run("b1", "b", PhaseWaitingApproval),
		// Task C thinks; task D and an orphaned sub-agent of it are done.
		"c": run("c", "", PhaseQueued), "d": run("d", "", PhaseCompleted), "d1": run("d1", "d", PhaseFailed),
	}
	if got := CurrentActivity(); got != (Activity{Active: 3, Thinking: 1, Working: 1, Waiting: 1}) {
		t.Fatalf("activity = %+v", got)
	}
}
