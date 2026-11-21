package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// TestTaskEventsHelperProcess is a minimal task runtime for the tests below.
// runtime.task-events negotiates features.taskEvents and announces every
// finished task with plugin.task.changed; runtime.task-poll does not;
// runtime.task-rogue sends the notification without negotiating it.
func TestTaskEventsHelperProcess(t *testing.T) {
	pluginID := os.Getenv("MINCODE_PLUGIN_ID")
	if pluginID != "runtime.task-events" && pluginID != "runtime.task-poll" && pluginID != "runtime.task-rogue" && pluginID != "runtime.task-flood" {
		return
	}
	var mu sync.Mutex
	writeJSON := func(message any) {
		encoded, _ := json.Marshal(message)
		mu.Lock()
		defer mu.Unlock()
		_, _ = os.Stdout.Write(append(encoded, '\n'))
	}
	type snapshot struct {
		TaskID          string          `json:"taskId"`
		State           string          `json:"state"`
		CancelRequested bool            `json:"cancelRequested"`
		Result          json.RawMessage `json:"result,omitempty"`
		Progress        json.RawMessage `json:"progress,omitempty"`
		controlled      bool
	}
	tasks := map[string]*snapshot{}
	statusCalls := 0
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var request rpcEnvelope
		if json.Unmarshal(line, &request) != nil {
			continue
		}
		response := rpcEnvelope{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "plugin.initialize":
			features := map[string]int{"tasks": 1}
			if pluginID == "runtime.task-events" || pluginID == "runtime.task-flood" {
				features["taskEvents"] = 1
			}
			response.Result, _ = json.Marshal(map[string]any{"runtimeProtocolVersion": api.RuntimeProtocolVersion, "features": features})
		case "plugin.task.start":
			var params struct {
				TaskID  string `json:"taskId"`
				Payload string `json:"payload"`
			}
			_ = json.Unmarshal(request.Params, &params)
			var payload struct {
				DelayMs    int  `json:"delayMs"`
				Controlled bool `json:"controlled"`
			}
			_ = json.Unmarshal([]byte(params.Payload), &payload)
			task := &snapshot{TaskID: params.TaskID, State: "running", controlled: payload.Controlled}
			if task.controlled {
				task.Progress = json.RawMessage(`{"phase":"accepted"}`)
			}
			mu.Lock()
			tasks[task.TaskID] = task
			mu.Unlock()
			if pluginID == "runtime.task-flood" {
				// Hint as fast as the pipe allows while the task runs.
				go func() {
					for {
						mu.Lock()
						running := task.State == "running"
						mu.Unlock()
						if !running {
							return
						}
						writeJSON(map[string]any{"jsonrpc": "2.0", "method": "plugin.task.changed", "params": map[string]string{"taskId": task.TaskID}})
					}
				}()
			}
			if !task.controlled {
				go func() {
					time.Sleep(time.Duration(payload.DelayMs) * time.Millisecond)
					mu.Lock()
					task.State = "succeeded"
					task.Result, _ = json.Marshal(map[string]int64{"doneAt": time.Now().UnixNano()})
					mu.Unlock()
					if pluginID != "runtime.task-poll" {
						writeJSON(map[string]any{"jsonrpc": "2.0", "method": "plugin.task.changed", "params": map[string]string{"taskId": task.TaskID}})
					}
				}()
			}
			mu.Lock()
			response.Result, _ = json.Marshal(task)
			mu.Unlock()
		case "plugin.task.status", "plugin.task.cancel":
			var params struct {
				TaskID string `json:"taskId"`
			}
			_ = json.Unmarshal(request.Params, &params)
			mu.Lock()
			task := tasks[params.TaskID]
			if task != nil {
				if request.Method == "plugin.task.status" {
					statusCalls++
				}
				if pluginID == "runtime.task-flood" && request.Method == "plugin.task.status" {
					reply := *task
					reply.Result, _ = json.Marshal(map[string]int{"statusCalls": statusCalls})
					response.Result, _ = json.Marshal(reply)
				} else {
					response.Result, _ = json.Marshal(task)
				}
			}
			mu.Unlock()
			if task == nil {
				response.Error = &rpcError{Code: -32602, Message: "Task not found"}
			}
		case "plugin.invoke":
			var params struct {
				Action  string `json:"action"`
				Payload string `json:"payload"`
			}
			var payload struct {
				TaskID string `json:"taskId"`
			}
			_ = json.Unmarshal(request.Params, &params)
			_ = json.Unmarshal([]byte(params.Payload), &payload)
			mu.Lock()
			task := tasks[payload.TaskID]
			valid := task != nil && task.controlled && (params.Action == "complete" || params.Action == "inspect")
			if valid {
				if params.Action == "complete" {
					task.State = "succeeded"
					task.Result, _ = json.Marshal(map[string]int64{"doneAt": time.Now().UnixNano()})
				}
				response.Result, _ = json.Marshal(map[string]int{"statusCalls": statusCalls})
			} else {
				response.Error = &rpcError{Code: -32602, Message: "invalid controlled task"}
			}
			mu.Unlock()
			// The notification precedes the invoke reply in one stdout stream:
			// receiving that reply proves the host processed (or ignored) it.
			if valid && params.Action == "complete" && pluginID != "runtime.task-poll" {
				writeJSON(map[string]any{"jsonrpc": "2.0", "method": "plugin.task.changed", "params": map[string]string{"taskId": task.TaskID}})
			}
		case "plugin.ping", "plugin.shutdown":
			response.Result = json.RawMessage(`{}`)
		default:
			response.Error = &rpcError{Code: -32601, Message: "unknown"}
		}
		writeJSON(response)
		if request.Method == "plugin.shutdown" {
			os.Exit(0)
		}
	}
}

func startTaskEventsHelper(t *testing.T, id string) *processRuntime {
	t.Helper()
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entry := "plugin-helper"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, entry), data, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		APIVersion: api.APIVersion, ID: id, Name: "Task Events Test", Version: "1.0.0",
		Runtime: ProcessRuntimeManifest{Type: "process", Entry: entry, Args: []string{"-test.run=TestTaskEventsHelperProcess"}},
	}
	r := newProcessRuntime(root, manifest, func(string, string) {}, nil)
	t.Cleanup(func() { _ = r.forceStop(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return r
}

type pushedTask struct {
	at   time.Time
	data map[string]any
}

// completionPhase holds the real monitor at its first observable running push.
// There are no runtime/record locks held by emitTaskChanged here. The completion
// RPC reply is an ordered wire barrier after the helper's hint, so the wake
// queue can be observed without consuming the hint or depending on poll phase.
type completionPhase struct {
	entered, release       chan struct{}
	enterOnce, releaseOnce sync.Once
}

func (p *completionPhase) unblock() { p.releaseOnce.Do(func() { close(p.release) }) }

func taskCompletionPhase(t *testing.T, id string, pushes chan pushedTask, phases *sync.Map) time.Duration {
	t.Helper()
	r := startTaskEventsHelper(t, id)
	phase := &completionPhase{entered: make(chan struct{}), release: make(chan struct{})}
	phases.Store(id, phase)
	generation := r.Generation()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	monitorCtx, cancelMonitor := context.WithTimeout(ctx, 10*time.Second)
	defer cancelMonitor()
	r.mu.Lock()
	negotiated := r.taskEventsSupported
	if !r.tasksSupported || negotiated != (id == "runtime.task-events") {
		r.mu.Unlock()
		t.Fatal("actual initialize reply lost task/taskEvents negotiation")
	}
	task := &hostTask{snapshot: taskSnapshot{TaskID: "phase-task", State: "running"}, sessionID: "session-1", generation: generation,
		wake: make(chan struct{}, 1), events: negotiated, cancel: cancelMonitor}
	r.tasks = map[string]*hostTask{task.snapshot.TaskID: task}
	r.mu.Unlock()
	monitorDone := make(chan struct{})
	go func() { r.runTask(monitorCtx, task, "work", `{"controlled":true}`); close(monitorDone) }()
	t.Cleanup(func() {
		phase.unblock()
		cancel()
		select {
		case <-monitorDone:
		case <-time.After(8 * time.Second):
			t.Error("controlled task monitor did not join")
		}
	})
	select {
	case <-phase.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("initial running push did not enter monitor barrier")
	}
	control := func(action string) int {
		t.Helper()
		raw, err := r.InvokeGeneration(ctx, generation, action, `{"taskId":"phase-task"}`)
		if err != nil {
			t.Fatal(err)
		}
		var reply struct {
			StatusCalls int `json:"statusCalls"`
		}
		if err := json.Unmarshal([]byte(raw), &reply); err != nil {
			t.Fatal(err)
		}
		return reply.StatusCalls
	}
	if calls := control("complete"); calls != 0 {
		t.Fatalf("status read while monitor held: %d", calls)
	}
	// The complete reply comes after the real hint in stdout. Never dequeue
	// the wake here: the production monitor must consume it after release.
	r.mu.Lock()
	wakeCount, taskGeneration, events := len(task.wake), task.generation, task.events
	r.mu.Unlock()
	wantWake := 0
	if negotiated {
		wantWake = 1
	}
	if wakeCount != wantWake || taskGeneration != generation || events != negotiated {
		t.Fatalf("%s after wire barrier: wake=%d want=%d generation=%d/%d events=%v/%v", id, wakeCount, wantWake, taskGeneration, generation, events, negotiated)
	}
	releasedAt := time.Now()
	phase.unblock()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case push := <-pushes:
			if push.data["pluginId"] != id || push.data["taskId"] != "phase-task" {
				continue
			}
			if push.data["sessionId"] != "session-1" || push.data["generation"] != generation {
				t.Fatalf("push lost its owner: %+v", push.data)
			}
			snapshot := push.data["snapshot"].(taskSnapshot)
			if snapshot.State == "running" {
				continue
			}
			if snapshot.State != "succeeded" {
				t.Fatalf("terminal = %+v", snapshot)
			}
			var result struct {
				DoneAt int64 `json:"doneAt"`
			}
			if json.Unmarshal(snapshot.Result, &result) != nil || result.DoneAt == 0 {
				t.Fatalf("lost helper completion: %s", snapshot.Result)
			}
			select {
			case <-monitorDone:
			case <-deadline:
				t.Fatal("monitor did not join after terminal push")
			}
			if calls := control("inspect"); calls != 1 {
				t.Fatalf("terminal requires exactly one real status read, got %d", calls)
			}
			// With no accepted hint, that read can only be the fallback timer.
			// With a negotiated queued hint, it precedes the 500ms fallback.
			return push.at.Sub(releasedAt)
		case <-deadline:
			t.Fatal("no terminal task push through hint/fallback")
		}
	}
}

func TestTaskEventsPushCompletionPromptly(t *testing.T) {
	pushes := make(chan pushedTask, 64)
	var phases sync.Map
	core.SetEmitter(func(name string, data any) {
		if name != pluginUITaskChangedEvent {
			return
		}
		payload := data.(map[string]any)
		if value, ok := phases.Load(payload["pluginId"]); ok && payload["snapshot"].(taskSnapshot).State == "running" {
			phase := value.(*completionPhase)
			phase.enterOnce.Do(func() { close(phase.entered); <-phase.release })
		}
		pushes <- pushedTask{at: time.Now(), data: payload}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	polled := taskCompletionPhase(t, "runtime.task-poll", pushes, &phases)
	rogue := taskCompletionPhase(t, "runtime.task-rogue", pushes, &phases)
	pushed := taskCompletionPhase(t, "runtime.task-events", pushes, &phases)
	t.Logf("released monitor -> UI push: fallback %v, ignored rogue hint %v, negotiated hint %v", polled, rogue, pushed)
	if pushed > 150*time.Millisecond {
		t.Fatalf("negotiated taskEvents latency %v exceeds unchanged150ms budget", pushed)
	}
}

func TestTaskChangedHintIsBoundToGenerationAndNegotiation(t *testing.T) {
	r := newProcessRuntime("", Manifest{}, nil, nil)
	r.generation = 2
	task := &hostTask{snapshot: taskSnapshot{TaskID: "t1", State: "running"}, generation: 2, wake: make(chan struct{}, 1)}
	r.tasks = map[string]*hostTask{"t1": task}
	woken := func() bool {
		select {
		case <-task.wake:
			return true
		default:
			return false
		}
	}
	r.taskChanged(2, json.RawMessage(`{"taskId":"t1"}`))
	if woken() {
		t.Fatal("hint accepted without negotiated taskEvents")
	}
	r.taskEventsSupported = true
	for _, params := range []string{`{"taskId":"t2"}`, `{"taskId":1}`, `[]`, `{"taskId":"` + string(make([]byte, 300)) + `"}`} {
		r.taskChanged(2, json.RawMessage(params))
		if woken() {
			t.Fatalf("invalid hint %q woke the task", params)
		}
	}
	r.taskChanged(1, json.RawMessage(`{"taskId":"t1"}`))
	if woken() {
		t.Fatal("hint from a previous generation accepted")
	}
	r.taskChanged(2, json.RawMessage(`{"taskId":"t1"}`))
	r.taskChanged(2, json.RawMessage(`{"taskId":"t1"}`)) // coalesced, never blocks
	if !woken() || woken() {
		t.Fatal("hints must coalesce into one wake-up")
	}
	task.finished = time.Now()
	r.taskChanged(2, json.RawMessage(`{"taskId":"t1"}`))
	if woken() {
		t.Fatal("finished task woken")
	}
}

func TestTaskEventsFeatureIsOffered(t *testing.T) {
	if offeredRuntimeFeatures(nil)["taskEvents"] != 1 {
		t.Fatal("taskEvents must be offered to every process plugin")
	}
}

// A plugin flooding plugin.task.changed must not turn each hint into a status
// read and a frontend event: wake-ups coalesce to taskWakeMinInterval, and the
// terminal state still reaches the UI promptly.
func TestTaskChangedFloodIsCoalesced(t *testing.T) {
	var pushes sync.Map
	var pushCount int64
	var countMu sync.Mutex
	terminal := make(chan pushedTask, 1)
	core.SetEmitter(func(name string, data any) {
		if name != pluginUITaskChangedEvent {
			return
		}
		countMu.Lock()
		pushCount++
		countMu.Unlock()
		payload := data.(map[string]any)
		if snapshot := payload["snapshot"].(taskSnapshot); snapshot.State != "running" {
			pushes.Store("terminal", true)
			select {
			case terminal <- pushedTask{at: time.Now(), data: payload}:
			default:
			}
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })

	r := startTaskEventsHelper(t, "runtime.task-flood")
	session, cancel := context.WithCancel(context.Background())
	defer cancel()
	const delay = 600 * time.Millisecond
	payload, _ := json.Marshal(map[string]int64{"delayMs": delay.Milliseconds()})
	started := time.Now()
	if _, err := r.startTask(session, taskOwner{SessionID: "session-1", Generation: r.Generation()}, "work", string(payload), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case push := <-terminal:
		snapshot := push.data["snapshot"].(taskSnapshot)
		if snapshot.State != "succeeded" {
			t.Fatalf("terminal snapshot %+v", snapshot)
		}
		if late := push.at.Sub(started) - delay; late > 200*time.Millisecond {
			t.Fatalf("terminal push %v after completion under a hint flood", late)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no terminal push under a hint flood")
	}
	// Read the helper's own count of status calls from a final status reply.
	var calls struct {
		StatusCalls int `json:"statusCalls"`
	}
	snapshot, err := r.taskRPC(context.Background(), r.Generation(), "status", map[string]string{"taskId": func() string {
		r.mu.Lock()
		defer r.mu.Unlock()
		for id := range r.tasks {
			return id
		}
		return ""
	}()})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(snapshot.Result, &calls); err != nil {
		t.Fatal(err)
	}
	// Fixed bound (not derived from taskWakeMinInterval) so the test fails if
	// coalescing is removed: 600 ms at one read per 50 ms plus fallback polls.
	const limit = 30
	countMu.Lock()
	events := pushCount
	countMu.Unlock()
	t.Logf("flood over %v: %d status reads, %d frontend events", delay, calls.StatusCalls, events)
	if calls.StatusCalls > limit {
		t.Fatalf("%d status reads during a %v flood, want at most %d", calls.StatusCalls, delay, limit)
	}
}
