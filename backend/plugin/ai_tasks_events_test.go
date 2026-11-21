package plugin

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"mincode/app/backend/agentprogress"
	"mincode/app/plugins/api"
)

type aiTasksRecorder struct {
	mu     sync.Mutex
	events []aiTasksEvent
}

func (r *aiTasksRecorder) all() []aiTasksEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]aiTasksEvent(nil), r.events...)
}

// useAITasks isolates the publisher: a settable run list and a recorder instead of the plugin manager.
func useAITasks(t *testing.T, throttle, interval time.Duration) (*aiTasksRecorder, func([]agentprogress.AgentRun)) {
	t.Helper()
	recorder := &aiTasksRecorder{}
	var current []agentprogress.AgentRun
	var currentMu sync.Mutex
	previousRuns, previousPublish, previousThrottle, previousInterval := aiTasksRuns, aiTasksPublish, aiTasksThrottle, aiTasksSnapshotInterval
	aiTasksRuns = func() []agentprogress.AgentRun {
		currentMu.Lock()
		defer currentMu.Unlock()
		return append([]agentprogress.AgentRun(nil), current...)
	}
	aiTasksPublish = func(event aiTasksEvent) {
		recorder.mu.Lock()
		recorder.events = append(recorder.events, event)
		recorder.mu.Unlock()
	}
	aiTasksThrottle, aiTasksSnapshotInterval = throttle, interval
	t.Cleanup(func() {
		aiTasksMu.Lock()
		if aiTasksTimer != nil {
			aiTasksTimer.Stop()
			aiTasksTimer = nil
		}
		aiTasksRuns, aiTasksPublish, aiTasksThrottle, aiTasksSnapshotInterval = previousRuns, previousPublish, previousThrottle, previousInterval
		aiTasksMu.Unlock()
	})
	return recorder, func(runs []agentprogress.AgentRun) { currentMu.Lock(); current = runs; currentMu.Unlock() }
}

func waitAITasks(t *testing.T, recorder *aiTasksRecorder, count int) []aiTasksEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if events := recorder.all(); len(events) >= count {
			return events
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d ai.tasks events, got %+v", count, recorder.all())
	return nil
}

func TestAITasksPublishesTitlesAndSummariesWithoutIdentifiers(t *testing.T) {
	recorder, set := useAITasks(t, 20*time.Millisecond, time.Hour)
	now := time.Now().UnixMilli()
	old := time.Now().Add(-aiTasksRecent - time.Minute).UnixMilli()
	set([]agentprogress.AgentRun{
		{
			RunID: "run-1", TaskID: "task-secret", Branch: "branch-secret", WorktreeName: "worktree-secret",
			SessionID: "session-secret", WorkspaceID: "ws-secret", WorkspaceScopeID: "scope-secret", Source: "agentic",
			Title:      "Refactor\n  billing " + strings.Repeat("x", 200),
			Navigation: agentprogress.Navigation{Type: "ai-conversation", ConversationID: "conversation-secret"},
			Progress:   agentprogress.SessionProgress{Phase: "tool_running", Summary: "Editing files", UpdatedAt: now, ErrorCode: "error-secret", ApprovalID: "approval-secret"},
		},
		{RunID: "child-1", ParentRunID: "run-1", Source: "acp", Title: "Sub-agent", Progress: agentprogress.SessionProgress{Phase: "thinking", UpdatedAt: now}},
		{RunID: "run-2", Source: "acp", Title: "Agent task", Progress: agentprogress.SessionProgress{Phase: "completed", UpdatedAt: now, CompletedAt: &now}},
		{RunID: "run-old", Source: "acp", Title: "Old task", Progress: agentprogress.SessionProgress{Phase: "completed", UpdatedAt: old, CompletedAt: &old}},
		{RunID: "run-other", Source: "terminal", Title: "Other", Progress: agentprogress.SessionProgress{Phase: "thinking", UpdatedAt: now}},
	})
	// A burst of changes is coalesced into one snapshot.
	for range 5 {
		aiTasksChanged(agentprogress.AgentRun{RunID: "run-1", Source: "agentic"})
	}
	aiTasksChanged(agentprogress.AgentRun{RunID: "run-other", Source: "terminal"})
	events := waitAITasks(t, recorder, 1)
	time.Sleep(60 * time.Millisecond)
	if events = recorder.all(); len(events) != 1 {
		t.Fatalf("burst not coalesced: %+v", events)
	}
	tasks := events[0].Tasks
	if events[0].Seq == 0 || len(tasks) != 2 || tasks[0].RunID != "run-1" || tasks[1].RunID != "run-2" {
		t.Fatalf("tasks = %+v", tasks)
	}
	if tasks[0].Source != "assistant" || tasks[1].Source != "agent" || tasks[0].Phase != "tool_running" || tasks[0].Summary != "Editing files" {
		t.Fatalf("task fields = %+v", tasks[0])
	}
	if !strings.HasPrefix(tasks[0].Title, "Refactor billing x") || utf8.RuneCountInString(tasks[0].Title) != aiTasksTitleRunes || !strings.HasSuffix(tasks[0].Title, "…") {
		t.Fatalf("title not normalized and truncated: %q", tasks[0].Title)
	}
	raw, _ := json.Marshal(events[0])
	for _, secret := range []string{"secret", "child-1", "run-old", "run-other", "sessionId", "workspaceId", "navigation"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("ai.tasks leaked %q: %s", secret, raw)
		}
	}
}

func TestAITasksRepeatsSnapshotWhileTasksAreListed(t *testing.T) {
	recorder, set := useAITasks(t, 5*time.Millisecond, 20*time.Millisecond)
	now := time.Now().UnixMilli()
	set([]agentprogress.AgentRun{{RunID: "run-1", Source: "acp", Title: "Task", Progress: agentprogress.SessionProgress{Phase: "thinking", UpdatedAt: now}}})
	aiTasksChanged(agentprogress.AgentRun{RunID: "run-1", Source: "acp"})
	events := waitAITasks(t, recorder, 3)
	if events[1].Seq <= events[0].Seq || len(events[2].Tasks) != 1 {
		t.Fatalf("snapshots = %+v", events)
	}
	set(nil)
	waitAITasks(t, recorder, len(events)+1)
	time.Sleep(20 * time.Millisecond)
	count := len(recorder.all())
	if last := recorder.all()[count-1]; len(last.Tasks) != 0 {
		t.Fatalf("no empty snapshot after the tasks aged out: %+v", last)
	}
	time.Sleep(60 * time.Millisecond)
	if len(recorder.all()) != count {
		t.Fatal("snapshot kept repeating with no tasks")
	}
}

// A zero-delay callback may start before AfterFunc returns. Publication and
// repeated scheduling must still acquire a fully initialized timer owner.
func TestAITasksImmediateScheduleOwnsCallback(t *testing.T) {
	_, _ = useAITasks(t, 0, 0)
	events := make(chan aiTasksEvent, 1)
	aiTasksMu.Lock()
	aiTasksPublish = func(event aiTasksEvent) { events <- event }
	aiTasksMu.Unlock()
	for range 40 {
		aiTasksChanged(agentprogress.AgentRun{RunID: "immediate", Source: "acp"})
		select {
		case event := <-events:
			if len(event.Tasks) != 0 {
				t.Fatalf("unexpected tasks: %+v", event)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("immediate snapshot not published")
		}
	}
}

func TestAITasksReplacedCallbackCannotPublishOrRepeat(t *testing.T) {
	recorder, _ := useAITasks(t, time.Hour, time.Hour)
	aiTasksMu.Lock()
	aiTasksScheduleLocked(time.Hour)
	old := aiTasksTimer
	aiTasksScheduleLocked(time.Minute)
	current := aiTasksTimer
	aiTasksMu.Unlock()
	// Model a callback that already started when Stop was called.
	aiTasksMu.Lock()
	aiTasksFireLocked(old)
	unchanged := aiTasksTimer == current
	aiTasksMu.Unlock()
	if !unchanged || len(recorder.all()) != 0 {
		t.Fatal("replaced callback published or replaced the current schedule")
	}
}

func TestAITasksNeedsItsOwnCapability(t *testing.T) {
	subscribe := func(capabilities ...api.Capability) *rpcError {
		runtimeHost := newProcessRuntime(t.TempDir(), Manifest{APIVersion: 2, Capabilities: capabilities}, nil, nil)
		raw, _ := json.Marshal(map[string]any{"events": []string{hostEventAITasks}})
		_, rpcErr := hostEventsSubscribe(runtimeHost, raw)
		return rpcErr
	}
	if rpcErr := subscribe(api.CapabilityAI, api.CapabilityAIActivity); rpcErr == nil || rpcErr.Code != -32001 {
		t.Fatalf("ai and ai.activity must not imply ai.tasks: %+v", rpcErr)
	}
	if rpcErr := subscribe(api.CapabilityAITasks); rpcErr != nil {
		t.Fatalf("subscribe with ai.tasks = %+v", rpcErr)
	}
	if frontendHostEvents[hostEventAITasks] {
		t.Fatal("the frontend must not be able to forge ai.tasks")
	}
	if _, err := resolveAIActionPermissions(ManifestUIAction{ID: "x", Risk: "read", Permissions: []api.Capability{api.CapabilityAITasks}}, []api.Capability{api.CapabilityAITasks}); err == nil {
		t.Fatal("ai.tasks accepted as an action permission")
	}
}
