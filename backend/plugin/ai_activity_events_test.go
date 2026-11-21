package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/agentprogress"
	"mincode/app/plugins/api"
)

type aiActivityRecorder struct {
	mu     sync.Mutex
	events []aiActivityEvent
}

func (r *aiActivityRecorder) all() []aiActivityEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]aiActivityEvent(nil), r.events...)
}

// useAIActivity isolates the publisher: a settable activity snapshot and a recorder instead of the plugin manager.
func useAIActivity(t *testing.T, interval time.Duration) (*aiActivityRecorder, func(agentprogress.Activity)) {
	t.Helper()
	recorder := &aiActivityRecorder{}
	var current agentprogress.Activity
	var currentMu sync.Mutex
	previousCurrent, previousPublish, previousInterval := aiActivityCurrent, aiActivityPublish, aiActivitySnapshotInterval
	aiActivityCurrent = func() agentprogress.Activity { currentMu.Lock(); defer currentMu.Unlock(); return current }
	aiActivityPublish = func(event aiActivityEvent) {
		recorder.mu.Lock()
		recorder.events = append(recorder.events, event)
		recorder.mu.Unlock()
	}
	aiActivitySnapshotInterval = interval
	aiActivityMu.Lock()
	aiActivityLast, aiActivityOrder = map[string]string{}, nil
	aiActivityMu.Unlock()
	t.Cleanup(func() {
		aiActivityMu.Lock()
		if aiActivityBeat {
			close(aiActivityStop)
			aiActivityBeat = false
		}
		aiActivityCurrent, aiActivityPublish, aiActivitySnapshotInterval = previousCurrent, previousPublish, previousInterval
		aiActivityMu.Unlock()
	})
	return recorder, func(activity agentprogress.Activity) { currentMu.Lock(); current = activity; currentMu.Unlock() }
}

func TestAIActivityPublishesSnapshotsWithoutContent(t *testing.T) {
	recorder, set := useAIActivity(t, time.Hour)
	set(agentprogress.Activity{Active: 2, Working: 1, Waiting: 1})
	run := agentprogress.AgentRun{
		RunID: "run-1", SessionID: "session-secret", WorkspaceID: "ws-1", Source: "agentic", Title: "Refactor billing.go",
		Navigation: agentprogress.Navigation{Type: "ai-conversation", ConversationID: "conversation-1"},
		Progress:   agentprogress.SessionProgress{Phase: agentprogress.PhaseToolRunning, Summary: "Running shell: rm -rf build"},
	}
	aiActivityChanged(run)
	events := recorder.all()
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	first := events[0]
	if first.Seq == 0 || first.RunID != "run-1" || first.Source != "assistant" || first.Phase != "tool_running" ||
		first.Active != 2 || first.Working != 1 || first.Waiting != 1 || first.Thinking != 0 {
		t.Fatalf("event = %+v", first)
	}
	encoded, _ := json.Marshal(first)
	for _, secret := range []string{"session-secret", "ws-1", "billing", "conversation-1", "rm -rf"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("event leaks %q: %s", secret, encoded)
		}
	}
	// Streaming progress in the same phase is not republished.
	run.Progress.Summary = "Running shell: go test"
	aiActivityChanged(run)
	if len(recorder.all()) != 1 {
		t.Fatal("same phase published twice")
	}
	run.Progress.Phase = agentprogress.PhaseCompleted
	aiActivityChanged(run)
	agent := run
	agent.RunID, agent.Source, agent.Progress.Phase = "run-2", "acp", agentprogress.PhaseThinking
	aiActivityChanged(agent)
	unknown := run
	unknown.RunID, unknown.Source = "run-3", "plugin"
	aiActivityChanged(unknown)
	events = recorder.all()
	if len(events) != 3 || events[1].Phase != "completed" || events[2].Source != "agent" || events[2].Seq <= events[1].Seq || events[1].Seq <= events[0].Seq {
		t.Fatalf("events = %+v", events)
	}
	for index := 0; index < aiActivityMaxTracked+10; index++ {
		spam := run
		spam.RunID = fmt.Sprintf("spam-%d", index)
		aiActivityChanged(spam)
	}
	aiActivityMu.Lock()
	tracked := len(aiActivityLast)
	aiActivityMu.Unlock()
	if tracked > aiActivityMaxTracked {
		t.Fatalf("phase memory grew to %d", tracked)
	}
}

// Concurrent runs publish from concurrent goroutines: every event still gets
// a unique, increasing number, so a plugin can drop the ones that arrive late.
func TestAIActivitySequenceIsUniqueUnderConcurrency(t *testing.T) {
	recorder, set := useAIActivity(t, time.Hour)
	set(agentprogress.Activity{Active: 1, Thinking: 1})
	var wait sync.WaitGroup
	for index := 0; index < 50; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			aiActivityChanged(agentprogress.AgentRun{RunID: fmt.Sprintf("r%d", index), Source: "agentic", Progress: agentprogress.SessionProgress{Phase: agentprogress.PhaseThinking}})
		}()
	}
	wait.Wait()
	seen := map[uint64]bool{}
	for _, event := range recorder.all() {
		if seen[event.Seq] {
			t.Fatalf("duplicate seq %d", event.Seq)
		}
		seen[event.Seq] = true
	}
	if len(seen) != 50 {
		t.Fatalf("published %d events", len(seen))
	}
}

func TestAIActivityRepeatsSnapshotsUntilIdle(t *testing.T) {
	recorder, set := useAIActivity(t, 10*time.Millisecond)
	set(agentprogress.Activity{Active: 1, Thinking: 1})
	aiActivityChanged(agentprogress.AgentRun{RunID: "long", Source: "acp", Progress: agentprogress.SessionProgress{Phase: agentprogress.PhaseStreaming}})
	deadline := time.Now().Add(2 * time.Second)
	for len(recorder.all()) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	snapshots := recorder.all()[1:]
	if len(snapshots) < 2 || snapshots[0].RunID != "" || snapshots[0].Phase != "" || snapshots[0].Active != 1 {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	set(agentprogress.Activity{})
	for time.Now().Before(deadline) {
		aiActivityMu.Lock()
		beating := aiActivityBeat
		aiActivityMu.Unlock()
		if !beating {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	events := recorder.all()
	if last := events[len(events)-1]; last.Active != 0 {
		t.Fatalf("no idle snapshot after the last task: %+v", last)
	}
	count := len(events)
	time.Sleep(40 * time.Millisecond)
	if len(recorder.all()) != count {
		t.Fatal("heartbeat kept running while idle")
	}
}

func TestAIActivityNeedsItsOwnCapability(t *testing.T) {
	subscribe := func(capabilities ...api.Capability) *rpcError {
		runtimeHost := newProcessRuntime(t.TempDir(), Manifest{APIVersion: 2, Capabilities: capabilities}, nil, nil)
		raw, _ := json.Marshal(map[string]any{"events": []string{hostEventAIActivity}})
		_, rpcErr := hostEventsSubscribe(runtimeHost, raw)
		return rpcErr
	}
	if rpcErr := subscribe(api.CapabilityUI, api.CapabilityAI); rpcErr == nil || rpcErr.Code != -32001 {
		t.Fatalf("ai capability must not imply ai.activity: %+v", rpcErr)
	}
	if rpcErr := subscribe(api.CapabilityAIActivity); rpcErr != nil {
		t.Fatalf("subscribe with ai.activity = %+v", rpcErr)
	}
	if frontendHostEvents[hostEventAIActivity] {
		t.Fatal("the frontend must not be able to forge ai.activity")
	}
	if _, err := resolveAIActionPermissions(ManifestUIAction{ID: "x", Risk: "read", Permissions: []api.Capability{api.CapabilityAIActivity}}, []api.Capability{api.CapabilityAIActivity}); err == nil {
		t.Fatal("ai.activity accepted as an action permission")
	}
}
