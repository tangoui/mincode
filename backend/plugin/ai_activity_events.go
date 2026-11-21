package plugin

import (
	"sync"
	"time"

	"mincode/app/backend/agentprogress"
)

// ai.activity tells subscribed plugins (capability ai.activity) what the AI
// assistant and native agents are doing, without saying what about. Every
// event carries a snapshot of all unfinished tasks (a run and its sub-agents
// count as one task, in their most urgent state) and a sequence number:
// deliveries run concurrently and may arrive out of order, so a plugin keeps
// only the event with the highest seq. A phase change of one run also names
// that run's random ID, source and phase; while any task is active the host
// repeats the snapshot every aiActivitySnapshotInterval and once more when the
// last task ends, so a plugin that missed an event recovers.
//
// Titles, summaries, conversation text, tool names and arguments, session,
// conversation and workspace identifiers never leave the host. Streaming
// progress that keeps a run in the same phase is not published.
const (
	hostEventAIActivity = "ai.activity"
	// aiActivityMaxTracked bounds the per-run phase memory used for deduplication.
	aiActivityMaxTracked = 256
)

// aiActivitySources maps agentprogress sources to plugin-facing names.
var aiActivitySources = map[string]string{"agentic": "assistant", "acp": "agent"}

type aiActivityEvent struct {
	Seq uint64 `json:"seq"`
	// RunID, Source and Phase describe the run that changed; a periodic snapshot has none.
	RunID  string `json:"runId,omitempty"`
	Source string `json:"source,omitempty"`
	Phase  string `json:"phase,omitempty"`
	// Active tasks, split by their most urgent state.
	Active   int `json:"active"`
	Thinking int `json:"thinking"`
	Working  int `json:"working"`
	Waiting  int `json:"waiting"`
}

var (
	aiActivityMu    sync.Mutex
	aiActivitySeq   uint64
	aiActivityLast  = map[string]string{}
	aiActivityOrder []string
	aiActivityBeat  bool
	// aiActivityStop ends the running heartbeat early (tests only).
	aiActivityStop chan struct{}

	// Variables so tests need not touch the run registry or the plugin manager.
	aiActivityCurrent          = agentprogress.CurrentActivity
	aiActivityPublish          = func(event aiActivityEvent) { host().publishHostEvent(hostEventAIActivity, event) }
	aiActivitySnapshotInterval = time.Minute
)

// snapshotLocked numbers a snapshot of the current tasks. Taking the snapshot
// and the number under one lock keeps higher numbers at least as recent.
func snapshotLocked() aiActivityEvent {
	aiActivitySeq++
	activity := aiActivityCurrent()
	return aiActivityEvent{Seq: aiActivitySeq, Active: activity.Active, Thinking: activity.Thinking, Working: activity.Working, Waiting: activity.Waiting}
}

// aiActivityChanged publishes run's phase change (ignoring repeats of the
// same phase) and starts the periodic snapshot while tasks are active.
func aiActivityChanged(run agentprogress.AgentRun) {
	source, known := aiActivitySources[run.Source]
	if !known || run.RunID == "" {
		return
	}
	aiActivityMu.Lock()
	if previous, seen := aiActivityLast[run.RunID]; seen && previous == run.Progress.Phase {
		aiActivityMu.Unlock()
		return
	} else if !seen {
		aiActivityOrder = append(aiActivityOrder, run.RunID)
		if len(aiActivityOrder) > aiActivityMaxTracked {
			delete(aiActivityLast, aiActivityOrder[0])
			aiActivityOrder = aiActivityOrder[1:]
		}
	}
	aiActivityLast[run.RunID] = run.Progress.Phase
	event := snapshotLocked()
	event.RunID, event.Source, event.Phase = run.RunID, source, run.Progress.Phase
	aiActivityPublish(event)
	if event.Active > 0 && !aiActivityBeat {
		aiActivityBeat = true
		aiActivityStop = make(chan struct{})
		go aiActivityHeartbeat(aiActivitySnapshotInterval, aiActivityStop)
	}
	aiActivityMu.Unlock()
}

// aiActivityHeartbeat repeats the snapshot while any task is active and
// publishes one last snapshot once none is.
func aiActivityHeartbeat(interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		aiActivityMu.Lock()
		event := snapshotLocked()
		aiActivityPublish(event)
		if event.Active == 0 {
			aiActivityBeat = false
			aiActivityMu.Unlock()
			return
		}
		aiActivityMu.Unlock()
	}
}

func init() {
	agentprogress.Observe(aiActivityChanged)
}
