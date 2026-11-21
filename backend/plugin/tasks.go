package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// taskPollInterval is the steady-state status poll; it bounds RPC volume for
// long-running tasks.
const taskPollInterval = time.Second

// taskPollSchedule polls quickly right after a task starts or changes so short
// tasks complete promptly, then backs off to taskPollInterval.
var taskPollSchedule = []time.Duration{100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, taskPollInterval}

// taskWakeMinInterval bounds how often plugin.task.changed hints can trigger a
// status read and frontend push for one task.
const taskWakeMinInterval = 50 * time.Millisecond

// taskEventPollSchedule is the fallback poll for runtimes that negotiated
// features.taskEvents: their plugin.task.changed notifications trigger an
// immediate status read, so polling only guards against a lost hint.
var taskEventPollSchedule = []time.Duration{500 * time.Millisecond, taskPollInterval, 2 * time.Second}

const (
	// pluginTaskChangedNotification is the plugin-to-host hint
	// {"taskId": "..."} (features.taskEvents).
	pluginTaskChangedNotification = "plugin.task.changed"
	// pluginUITaskChangedEvent pushes a changed snapshot to the frontend; the
	// owning view frame forwards it only to the session that started the task.
	pluginUITaskChangedEvent = "plugin:ui-task-changed"
)

type taskPollBackoff struct {
	step     int
	schedule []time.Duration
}

// next returns the delay before the next status poll and advances the schedule.
func (b *taskPollBackoff) next() time.Duration {
	schedule := b.schedule
	if len(schedule) == 0 {
		schedule = taskPollSchedule
	}
	delay := schedule[min(b.step, len(schedule)-1)]
	if b.step < len(schedule)-1 {
		b.step++
	}
	return delay
}

func (b *taskPollBackoff) reset() { b.step = 0 }

// taskStatusFingerprint identifies observable status changes for backoff reset.
func taskStatusFingerprint(snapshot taskSnapshot) string {
	return snapshot.State + "\x00" + string(snapshot.Progress) + "\x00" + string(snapshot.Result) + "\x00" + snapshot.Error
}

// taskRetention keeps a finished task's terminal snapshot readable for its
// owner. Once the owner has received that snapshot (hostTask.delivered) the
// task no longer reserves a slot: startTask evicts delivered tasks, oldest
// finished first, when taskLimit would otherwise refuse a new one.
const taskRetention = 5 * time.Minute

// taskLimit bounds the tasks one runtime holds at once: running tasks plus
// finished tasks whose result the owner has not received yet.
const taskLimit = 64
const taskResultLimit = 256 * 1024

type taskSnapshot struct {
	TaskID             string          `json:"taskId"`
	State              string          `json:"state"`
	CancelRequested    bool            `json:"cancelRequested"`
	OutcomeUnconfirmed bool            `json:"outcomeUnconfirmed,omitempty"`
	Progress           json.RawMessage `json:"progress,omitempty"`
	Result             json.RawMessage `json:"result,omitempty"`
	Error              string          `json:"error,omitempty"`
}

// taskOwner is who started a task: a plugin page session (SessionID, the
// view frame receives plugin:ui-task-changed) or a service call (CallID, the
// host's service call record receives every change through OnChange and no
// page ever sees the task). Generation is the runtime generation the task
// runs against.
type taskOwner struct {
	SessionID  string
	Generation uint64
	CallID     string
	// OnChange receives every observable snapshot change of a service call
	// task, terminal one included. It runs on the task monitor goroutine
	// without runtime locks held and must not block.
	OnChange func(taskSnapshot)
	// ResultLimit bounds the task's result in bytes; 0 means taskResultLimit.
	ResultLimit int
}

type hostTask struct {
	snapshot   taskSnapshot
	sessionID  string
	generation uint64
	callID     string
	onChange   func(taskSnapshot)
	// resultLimit bounds snapshot.Result; 0 means taskResultLimit.
	resultLimit int
	finished    time.Time
	// delivered: the owner has received the terminal snapshot (a UI get or
	// cancel returned it, the view frame acknowledged forwarding the pushed
	// terminal snapshot, or a service call's OnChange saw it). A delivered
	// task may be evicted before taskRetention to make room for new tasks.
	delivered bool
	cancel    context.CancelFunc
	// wake (capacity 1) coalesces plugin.task.changed hints for the monitor.
	wake chan struct{}
	// events: the generation negotiated features.taskEvents at start.
	events bool
}

func (task *hostTask) resultLimitBytes() int {
	if task.resultLimit > 0 {
		return task.resultLimit
	}
	return taskResultLimit
}

type taskArgs struct {
	SessionID string `json:"sessionId"`
	Nonce     string `json:"nonce"`
	Action    string `json:"action"`
	Payload   string `json:"payload"`
	TaskID    string `json:"taskId"`
	TimeoutMs int    `json:"timeoutMs"`
}

func init() {
	// ack: the owning session's view frame forwarded a pushed terminal
	// snapshot (plugin:ui-task-changed) to its page.
	for _, method := range []string{"start", "get", "cancel", "ack"} {
		operation := method
		core.Register("plugin_ui_task_"+operation, func(_, raw string) (string, error) {
			var args taskArgs
			if err := core.DecodeArgs(raw, &args); err != nil {
				return "", err
			}
			result, err := host().uiTask(context.Background(), operation, args)
			if err != nil {
				return "", err
			}
			return core.EncodeResult(result)
		})
	}
}

func (r *processRuntime) describeAPI() any {
	methods := []map[string]any{}
	for _, name := range []string{"host.api.describe", "host.info", "host.log"} {
		methods = append(methods, map[string]any{"name": name, "available": true})
	}
	for _, spec := range []struct {
		name       string
		capability api.Capability
	}{
		{"host.storage.get", api.CapabilityStorage}, {"host.storage.set", api.CapabilityStorage},
		{"host.storage.delete", api.CapabilityStorage}, {"host.storage.list", api.CapabilityStorage},
		{"host.storage.file.read", api.CapabilityStorage}, {"host.storage.file.write", api.CapabilityStorage}, {"host.storage.file.delete", api.CapabilityStorage},
		{"host.storage.file.list", api.CapabilityStorage}, {"host.storage.file.stat", api.CapabilityStorage}, {"host.shell.exec", api.CapabilityShell},
		{"host.secrets.get", api.CapabilitySecrets}, {"host.secrets.set", api.CapabilitySecrets},
		{"host.secrets.delete", api.CapabilitySecrets}, {"host.secrets.list", api.CapabilitySecrets},
		{"host.context.set", api.CapabilityCommands},
		{"host.ai.turn.start", api.CapabilityAI}, {"host.ai.turn.poll", api.CapabilityAI}, {"host.ai.turn.cancel", api.CapabilityAI}, {"host.ai.plugins", api.CapabilityAI}, {"host.ai.grants", api.CapabilityAI}, {"host.ai.turn.approve", api.CapabilityAI},
	} {
		_, available := r.capabilities[spec.capability]
		methods = append(methods, map[string]any{"name": spec.name, "capability": spec.capability, "available": available})
	}
	// Methods added by contribution points through registerHostMethod.
	for _, registration := range registeredHostMethods() {
		entry := map[string]any{"name": registration.name, "available": registration.available(r)}
		if registration.capability != "" {
			entry["capability"] = registration.capability
		}
		methods = append(methods, entry)
	}
	return map[string]any{"apiVersion": 1, "runtimeProtocolVersion": 1, "methods": methods, "events": r.describeEvents(), "features": offeredRuntimeFeatures(r.capabilities), "limits": map[string]int{"taskCount": taskLimit, "taskResultBytes": taskResultLimit, "taskProgressBytes": 4096, "taskTimeoutMs": 300000, "taskRetentionMs": 300000, "secretKeys": pluginSecretMaxKeys, "secretValueBytes": pluginSecretMaxValueBytes, "workspaceReadBytes": workspaceReadMaxBytes, "workspaceWriteBytes": workspaceWriteMaxBytes, "workspaceListEntries": workspaceListMax}}
}

func (m *pluginManager) uiTask(ctx context.Context, operation string, a taskArgs) (taskSnapshot, error) {
	if m.uiHost == nil {
		return taskSnapshot{}, errors.New("plugin UI host is unavailable")
	}
	session, err := m.uiHost.Session(a.SessionID, a.Nonce)
	if err != nil {
		return taskSnapshot{}, err
	}
	if err := session.ctx.Err(); err != nil {
		return taskSnapshot{}, err
	}
	record, err := m.record(session.PluginID)
	if err != nil {
		return taskSnapshot{}, err
	}
	// Static authorization precedes any lazy activation.
	record.mu.Lock()
	var allowed *ManifestUIAction
	if m.workspaceActiveLocked(record) && record.manifest != nil {
		for _, candidate := range record.manifest.Contributes.Actions {
			if candidate.ID == strings.TrimSpace(a.Action) && candidate.UICallable {
				copy := candidate
				allowed = &copy
				break
			}
		}
	}
	record.mu.Unlock()
	if operation == "start" {
		if allowed == nil || allowed.ResultHandling != "" {
			return taskSnapshot{}, errors.New("plugin UI action is not allowed for tasks")
		}
		if len(a.Payload) > allowed.MaxPayloadBytes {
			return taskSnapshot{}, errors.New("task payload exceeds action limit")
		}
		if a.TimeoutMs == 0 {
			a.TimeoutMs = 300000
		}
		if a.TimeoutMs < 1 || a.TimeoutMs > 300000 {
			return taskSnapshot{}, errors.New("task timeout must be between 1 and 300000ms")
		}
		activation, cancel := context.WithTimeout(ctx, pluginStartupTimeout)
		stop := context.AfterFunc(session.ctx, cancel)
		err = m.ensureRunning(activation, session.PluginID)
		stop()
		cancel()
		if err != nil {
			return taskSnapshot{}, err
		}
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	r := record.runtime
	if !record.enabled || record.state != pluginStateRunning || r == nil || r.Generation() != session.Generation || session.ctx.Err() != nil {
		return taskSnapshot{}, errors.New("plugin UI session is stale")
	}
	if operation == "start" {
		valid := false
		if record.manifest != nil {
			for _, candidate := range record.manifest.Contributes.Actions {
				if candidate.ID == allowed.ID && candidate.UICallable && candidate.Risk == allowed.Risk &&
					candidate.ResultHandling == "" && len(a.Payload) <= candidate.MaxPayloadBytes {
					valid = true
					break
				}
			}
		}
		if !valid {
			return taskSnapshot{}, errors.New("task authorization changed during activation")
		}
		return r.startTask(session.ctx, taskOwner{SessionID: session.ID, Generation: session.Generation}, allowed.ID, a.Payload, time.Duration(a.TimeoutMs)*time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	task := r.tasks[a.TaskID]
	if task == nil || task.callID != "" || task.sessionID != session.ID || task.generation != session.Generation {
		return taskSnapshot{}, errors.New("task not found for UI session")
	}
	if !task.finished.IsZero() && time.Since(task.finished) > taskRetention {
		delete(r.tasks, a.TaskID)
		return taskSnapshot{}, errors.New("task expired")
	}
	if operation == "cancel" && task.snapshot.State == "running" {
		task.snapshot.CancelRequested = true
		task.cancel()
	}
	// A finished task's snapshot is final; returning it (or the frame
	// confirming it forwarded the pushed copy) delivers its result.
	if !task.finished.IsZero() {
		task.delivered = true
	}
	if operation == "ack" {
		// The frame already holds the snapshot; do not send the result again.
		return taskSnapshot{TaskID: task.snapshot.TaskID, State: task.snapshot.State}, nil
	}
	return task.snapshot, nil
}

// startTask starts action as a long-running task owned by owner; parent
// (the page session, or the service call) cancels it.
// startTask errors callers tell apart (a global shortcut press reports the
// limit as busy).
var (
	errTasksUnsupported = errors.New("plugin runtime does not support tasks (features.tasks: 1 required)")
	errTaskLimitReached = errors.New("task retention/count limit reached")
)

func (r *processRuntime) startTask(parent context.Context, owner taskOwner, action, payload string, timeout time.Duration) (taskSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.tasksSupported {
		return taskSnapshot{}, errTasksUnsupported
	}
	if r.tasks == nil {
		r.tasks = map[string]*hostTask{}
	}
	for id, task := range r.tasks {
		if !task.finished.IsZero() && time.Since(task.finished) > taskRetention {
			delete(r.tasks, id)
		}
	}
	if len(r.tasks) >= taskLimit {
		r.evictDeliveredTasksLocked(len(r.tasks) - taskLimit + 1)
	}
	if len(r.tasks) >= taskLimit {
		return taskSnapshot{}, errTaskLimitReached
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return taskSnapshot{}, err
	}
	id := hex.EncodeToString(bytes[:])
	ctx, cancel := context.WithTimeout(parent, timeout)
	task := &hostTask{snapshot: taskSnapshot{TaskID: id, State: "running"}, sessionID: owner.SessionID, generation: owner.Generation,
		callID: owner.CallID, onChange: owner.OnChange, resultLimit: owner.ResultLimit, cancel: cancel, wake: make(chan struct{}, 1), events: r.taskEventsSupported}
	r.tasks[id] = task
	go r.runTask(ctx, task, action, payload)
	return task.snapshot, nil
}

// evictDeliveredTasksLocked removes up to n finished tasks whose terminal
// snapshot the owner already received, oldest finished first. Running tasks
// and undelivered results are never evicted. r.mu must be held.
func (r *processRuntime) evictDeliveredTasksLocked(n int) {
	if n <= 0 {
		return
	}
	candidates := make([]string, 0, len(r.tasks))
	for id, task := range r.tasks {
		if task.delivered && !task.finished.IsZero() {
			candidates = append(candidates, id)
		}
	}
	slices.SortFunc(candidates, func(a, b string) int {
		if order := r.tasks[a].finished.Compare(r.tasks[b].finished); order != 0 {
			return order
		}
		return strings.Compare(a, b)
	})
	for _, id := range candidates[:min(n, len(candidates))] {
		delete(r.tasks, id)
	}
}

// taskChanged handles a plugin.task.changed notification: for a task of the
// current generation it wakes the monitor, which reads the snapshot with
// plugin.task.status at once. The notification carries no state of its own,
// so a forged or stale hint costs at most one bounded status call. Hints from
// runtimes that did not negotiate taskEvents, unknown ids and malformed params
// are ignored.
func (r *processRuntime) taskChanged(generation uint64, params json.RawMessage) {
	if len(params) > 256 {
		return
	}
	var p struct {
		TaskID string `json:"taskId"`
	}
	if json.Unmarshal(params, &p) != nil || p.TaskID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation != r.generation || !r.taskEventsSupported {
		return
	}
	task := r.tasks[p.TaskID]
	if task == nil || task.generation != generation || task.wake == nil || !task.finished.IsZero() {
		return
	}
	select {
	case task.wake <- struct{}{}:
	default: // a refresh is already pending
	}
}

// emitTaskChanged reports a changed snapshot to the task's owner. A page
// task is pushed to the frontend: the event names the owning UI session and
// generation; PluginViewFrame forwards it only to that session's page, and
// only for a task the page started. A service call task goes to its call
// record only.
func (r *processRuntime) emitTaskChanged(task *hostTask, snapshot taskSnapshot) {
	if task.callID != "" {
		if task.onChange != nil {
			task.onChange(snapshot)
			if snapshot.State != "running" {
				r.mu.Lock()
				task.delivered = true
				r.mu.Unlock()
			}
		}
		return
	}
	core.Emit(pluginUITaskChangedEvent, map[string]any{
		"pluginId":   r.pluginID,
		"sessionId":  task.sessionID,
		"generation": task.generation,
		"taskId":     snapshot.TaskID,
		"snapshot":   snapshot,
	})
}

// taskPushFingerprint identifies snapshot changes the page can observe.
func taskPushFingerprint(snapshot taskSnapshot) string {
	return fmt.Sprintf("%s|%t|%t", taskStatusFingerprint(snapshot), snapshot.CancelRequested, snapshot.OutcomeUnconfirmed)
}

func (r *processRuntime) taskRPC(ctx context.Context, generation uint64, method string, params any) (taskSnapshot, error) {
	return r.taskRPCLimit(ctx, generation, taskResultLimit, method, params)
}

// taskRPCLimit is taskRPC with the result bounded by resultLimit bytes.
func (r *processRuntime) taskRPCLimit(ctx context.Context, generation uint64, resultLimit int, method string, params any) (taskSnapshot, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return taskSnapshot{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := r.callGeneration(callCtx, generation, "plugin.task."+method, raw)
	if err != nil {
		return taskSnapshot{}, err
	}
	if len(result) > resultLimit+8192 {
		return taskSnapshot{}, errors.New("task response exceeds limit")
	}
	var snapshot taskSnapshot
	if err = json.Unmarshal(result, &snapshot); err != nil {
		return snapshot, err
	}
	if len(snapshot.Progress) > 4096 || len(snapshot.Result) > resultLimit || len(snapshot.Error) > 4096 {
		return taskSnapshot{}, errors.New("task payload exceeds limit")
	}
	switch snapshot.State {
	case "running", "succeeded", "failed", "cancelled":
	default:
		return taskSnapshot{}, errors.New("invalid task state")
	}
	return snapshot, nil
}

func (r *processRuntime) runTask(ctx context.Context, task *hostTask, action, payload string) {
	defer task.cancel()
	r.mu.Lock()
	id := task.snapshot.TaskID
	pushed := taskPushFingerprint(task.snapshot)
	r.mu.Unlock()
	update := func(snapshot taskSnapshot, err error, cleanup bool) bool {
		done, current := r.applyTaskUpdate(task, id, snapshot, err, cleanup)
		// Push every observable change (including the terminal one) at once,
		// whether a notification or a poll revealed it.
		if fingerprint := taskPushFingerprint(current); fingerprint != pushed {
			pushed = fingerprint
			r.emitTaskChanged(task, current)
		}
		if done && task.callID != "" {
			// The service call record now holds the terminal snapshot for its
			// retention; no page can read this task, so free its slot at once.
			r.mu.Lock()
			if r.tasks[id] == task {
				delete(r.tasks, id)
			}
			r.mu.Unlock()
		}
		return done
	}
	// Revocation before dispatch is definitive: no action was sent. Once a
	// write is attempted, cancellation can race acceptance and needs cleanup.
	if ctx.Err() != nil {
		update(taskSnapshot{TaskID: id, State: "cancelled", CancelRequested: true}, nil, false)
		return
	}
	snapshot, err := r.taskRPCLimit(ctx, task.generation, task.resultLimitBytes(), "start", map[string]any{"taskId": id, "action": action, "payload": payload})
	if update(snapshot, err, true) {
		return
	}
	backoff := taskPollBackoff{schedule: taskPollSchedule}
	if task.events {
		backoff.schedule = taskEventPollSchedule
	}
	lastStatus := taskStatusFingerprint(snapshot)
	lastRefresh := time.Now()
	poll := time.NewTimer(backoff.next())
	defer poll.Stop()
	var grace <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	// refresh reads the status, records it and schedules the next poll; it
	// reports a terminal state.
	refresh := func() bool {
		lastRefresh = time.Now()
		snapshot, err = r.taskRPCLimit(context.Background(), task.generation, task.resultLimitBytes(), "status", map[string]string{"taskId": id})
		if update(snapshot, err, true) {
			return true
		}
		if status := taskStatusFingerprint(snapshot); status != lastStatus {
			lastStatus = status
			backoff.reset()
		}
		poll.Reset(backoff.next())
		return false
	}
	cancelled := false
	cancelSignal := ctx.Done()
	for {
		select {
		case <-cancelSignal:
			cancelSignal = nil
			if !cancelled {
				cancelled = true
				r.mu.Lock()
				task.snapshot.CancelRequested = true
				r.mu.Unlock()
				snapshot, err = r.taskRPCLimit(context.Background(), task.generation, task.resultLimitBytes(), "cancel", map[string]string{"taskId": id})
				if update(snapshot, err, false) {
					return
				}
				timer = time.NewTimer(3 * time.Second)
				grace = timer.C
			}
		case <-grace:
			update(taskSnapshot{}, errors.New("cancellation was not acknowledged; no rollback is implied"), false)
			return
		case <-poll.C:
			if refresh() {
				return
			}
		case <-task.wake:
			// plugin.task.changed: read the snapshot now and restart the
			// fallback schedule from there.
			if !poll.Stop() {
				select {
				case <-poll.C:
				default:
				}
			}
			// A plugin can send notifications as fast as it likes; coalesce
			// bursts into at most one status read (and one frontend event)
			// per taskWakeMinInterval. The deferred read still observes a
			// terminal state, so no completion is lost.
			if wait := taskWakeMinInterval - time.Since(lastRefresh); wait > 0 {
				poll.Reset(wait)
				continue
			}
			if refresh() {
				return
			}
		}
	}
}

// applyTaskUpdate records one task response (or monitoring failure) and
// returns whether the task reached a terminal state, plus the snapshot now
// visible to the UI.
func (r *processRuntime) applyTaskUpdate(task *hostTask, id string, snapshot taskSnapshot, err error, cleanup bool) (bool, taskSnapshot) {
	if err == nil && snapshot.TaskID != id {
		err = errors.New("invalid task response identity")
	}
	if err != nil && cleanup {
		// Monitoring failure does not stop remote work. Attempt cancellation
		// independently of the revoked context, but never clear uncertainty
		// based on this best-effort cleanup response.
		r.mu.Lock()
		task.snapshot.CancelRequested = true
		r.mu.Unlock()
		_, _ = r.taskRPCLimit(context.Background(), task.generation, task.resultLimitBytes(), "cancel", map[string]string{"taskId": id})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	requested := task.snapshot.CancelRequested
	if err != nil {
		snapshot = taskSnapshot{TaskID: id, State: "failed", OutcomeUnconfirmed: true, Error: fmt.Sprintf("Task outcome unconfirmed; side effects may have occurred: %.3000s", err)}
	}

	snapshot.CancelRequested = snapshot.CancelRequested || requested
	task.snapshot = snapshot
	if snapshot.State != "running" {
		task.finished = time.Now()
		time.AfterFunc(taskRetention, func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.tasks[id] == task {
				delete(r.tasks, id)
			}
		})
		return true, snapshot
	}
	return false, snapshot
}
