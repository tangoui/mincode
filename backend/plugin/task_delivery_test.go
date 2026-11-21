package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// deliveryTestRuntime is a task-capable runtime without a process. Tasks it
// starts use a cancelled parent, so runTask ends them as "cancelled" without
// sending any RPC.
func deliveryTestRuntime() *processRuntime {
	r := newProcessRuntime("", Manifest{ID: "task.delivery"}, nil, nil)
	r.tasksSupported = true
	r.generation = 1
	r.tasks = map[string]*hostTask{}
	return r
}

func cancelledParent() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// fillTasks adds n tasks; finishedAgo < 0 keeps them running.
func fillTasks(r *processRuntime, prefix string, n int, finishedAgo time.Duration, delivered bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%02d", prefix, i)
		task := &hostTask{snapshot: taskSnapshot{TaskID: id, State: "running"}, sessionID: "s", generation: 1, delivered: delivered, cancel: func() {}}
		if finishedAgo >= 0 {
			task.snapshot.State = "succeeded"
			task.finished = time.Now().Add(-finishedAgo)
		}
		r.tasks[id] = task
	}
}

func taskIDs(r *processRuntime, prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for id := range r.tasks {
		if strings.HasPrefix(id, prefix) {
			count++
		}
	}
	return count
}

func TestTaskLimitEvictsDeliveredFinishedTasks(t *testing.T) {
	r := deliveryTestRuntime()
	fillTasks(r, "done", taskLimit, time.Second, true)
	s, err := r.startTask(cancelledParent(), taskOwner{SessionID: "s", Generation: 1}, "a", "{}", time.Second)
	if err != nil {
		t.Fatalf("delivered results blocked a new task: %v", err)
	}
	if got := taskIDs(r, "done"); got != taskLimit-1 {
		t.Fatalf("evicted %d delivered tasks, want exactly 1", taskLimit-got)
	}
	r.mu.Lock()
	_, ok := r.tasks[s.TaskID]
	total := len(r.tasks)
	r.mu.Unlock()
	if !ok || total != taskLimit {
		t.Fatalf("new task missing or table over limit: ok=%t len=%d", ok, total)
	}
}

func TestTaskLimitKeepsUndeliveredResults(t *testing.T) {
	r := deliveryTestRuntime()
	fillTasks(r, "unread", taskLimit, time.Second, false)
	if _, err := r.startTask(cancelledParent(), taskOwner{SessionID: "s", Generation: 1}, "a", "{}", time.Second); err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("expected limit error, got %v", err)
	}
	if got := taskIDs(r, "unread"); got != taskLimit {
		t.Fatalf("undelivered results evicted: %d left", got)
	}
}

func TestTaskLimitNeverEvictsRunningTasks(t *testing.T) {
	r := deliveryTestRuntime()
	// delivered is meaningless while running; it must not make a task evictable.
	fillTasks(r, "running", taskLimit, -1, true)
	if _, err := r.startTask(cancelledParent(), taskOwner{SessionID: "s", Generation: 1}, "a", "{}", time.Second); err == nil {
		t.Fatal("running task evicted")
	}
	if got := taskIDs(r, "running"); got != taskLimit {
		t.Fatalf("running tasks evicted: %d left", got)
	}

	r = deliveryTestRuntime()
	fillTasks(r, "running", taskLimit-1, -1, false)
	fillTasks(r, "done", 1, time.Second, true)
	if _, err := r.startTask(cancelledParent(), taskOwner{SessionID: "s", Generation: 1}, "a", "{}", time.Second); err != nil {
		t.Fatal(err)
	}
	if taskIDs(r, "running") != taskLimit-1 || taskIDs(r, "done") != 0 {
		t.Fatal("eviction removed a running task instead of the delivered one")
	}
}

func TestTaskLimitEvictsOldestFinishedFirst(t *testing.T) {
	r := deliveryTestRuntime()
	fillTasks(r, "unread", taskLimit-3, 4*time.Minute, false) // older but undelivered (and unexpired): kept
	fillTasks(r, "newer", 1, time.Second, true)
	fillTasks(r, "oldest", 1, 3*time.Minute, true)
	fillTasks(r, "middle", 1, time.Minute, true)
	owner := taskOwner{SessionID: "s", Generation: 1}
	if _, err := r.startTask(cancelledParent(), owner, "a", "{}", time.Second); err != nil {
		t.Fatal(err)
	}
	if taskIDs(r, "oldest") != 0 || taskIDs(r, "middle") != 1 || taskIDs(r, "newer") != 1 {
		t.Fatal("first eviction did not pick the oldest finished task")
	}
	if _, err := r.startTask(cancelledParent(), owner, "a", "{}", time.Second); err != nil {
		t.Fatal(err)
	}
	if taskIDs(r, "middle") != 0 || taskIDs(r, "newer") != 1 || taskIDs(r, "unread") != taskLimit-3 {
		t.Fatal("second eviction did not pick the next oldest delivered task")
	}
}

func TestTaskDeliveryThroughUISession(t *testing.T) {
	r := deliveryTestRuntime()
	ui := newPluginUIHost()
	defer ui.Shutdown()
	record := &pluginRecord{id: r.pluginID, enabled: true, state: pluginStateRunning, runtime: r, manifest: &Manifest{}}
	m := &pluginManager{records: map[string]*pluginRecord{record.id: record}, uiHost: ui}
	session, err := ui.Begin(record.id, "view", 1, t.TempDir(), "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}
	args := func(id string) taskArgs {
		return taskArgs{SessionID: session.SessionID, Nonce: session.Nonce, TaskID: id}
	}
	add := func(id, state string) *hostTask {
		task := &hostTask{snapshot: taskSnapshot{TaskID: id, State: state, Result: []byte(`{"rows":50}`)}, sessionID: session.SessionID, generation: 1, cancel: func() {}}
		if state != "running" {
			task.finished = time.Now()
		}
		r.mu.Lock()
		r.tasks[id] = task
		r.mu.Unlock()
		return task
	}
	delivered := func(task *hostTask) bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return task.delivered
	}

	running := add("running", "running")
	if _, err := m.uiTask(context.Background(), "get", args("running")); err != nil || delivered(running) {
		t.Fatalf("a running snapshot counted as delivered: %v", err)
	}
	read := add("read", "succeeded")
	if s, err := m.uiTask(context.Background(), "get", args("read")); err != nil || string(s.Result) != `{"rows":50}` || !delivered(read) {
		t.Fatalf("get of a terminal snapshot not recorded: %+v %v", s, err)
	}
	pushed := add("pushed", "succeeded")
	ack, err := m.uiTask(context.Background(), "ack", args("pushed"))
	if err != nil || !delivered(pushed) || ack.State != "succeeded" || len(ack.Result) != 0 {
		t.Fatalf("ack not recorded or echoed the result: %+v %v", ack, err)
	}
	ackRunning := add("ack-running", "running")
	if _, err := m.uiTask(context.Background(), "ack", args("ack-running")); err != nil || delivered(ackRunning) {
		t.Fatalf("ack of a running task counted as delivered: %v", err)
	}

	// Another session cannot mark this session's results delivered.
	other, err := ui.Begin(record.id, "view", 1, t.TempDir(), "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign := add("foreign", "succeeded")
	if _, err := m.uiTask(context.Background(), "ack", taskArgs{SessionID: other.SessionID, Nonce: other.Nonce, TaskID: "foreign"}); err == nil || delivered(foreign) {
		t.Fatal("cross-session ack accepted")
	}

	// Once evicted for a new task, a get fails like any unknown task.
	r.mu.Lock()
	for i := len(r.tasks); i < taskLimit; i++ {
		id := fmt.Sprintf("fill-%02d", i)
		r.tasks[id] = &hostTask{snapshot: taskSnapshot{TaskID: id, State: "running"}, sessionID: session.SessionID, generation: 1, cancel: func() {}}
	}
	r.tasks["read"].finished = time.Now().Add(-time.Minute) // oldest delivered
	r.mu.Unlock()
	if _, err := r.startTask(cancelledParent(), taskOwner{SessionID: session.SessionID, Generation: 1}, "a", "{}", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := m.uiTask(context.Background(), "get", args("read")); err == nil || !strings.Contains(err.Error(), "task not found") {
		t.Fatalf("evicted task lookup: %v", err)
	}
	if _, err := m.uiTask(context.Background(), "get", args("pushed")); err != nil {
		t.Fatalf("only one task should have been evicted: %v", err)
	}
}

func TestServiceCallTaskDeliveredThroughOnChange(t *testing.T) {
	r := deliveryTestRuntime()
	task := &hostTask{snapshot: taskSnapshot{TaskID: "call", State: "running"}, callID: "c1", generation: 1, cancel: func() {}}
	var seen []string
	task.onChange = func(s taskSnapshot) { seen = append(seen, s.State) }
	r.emitTaskChanged(task, taskSnapshot{TaskID: "call", State: "running"})
	if task.delivered {
		t.Fatal("running OnChange counted as delivery")
	}
	r.emitTaskChanged(task, taskSnapshot{TaskID: "call", State: "succeeded"})
	if !task.delivered || strings.Join(seen, ",") != "running,succeeded" {
		t.Fatalf("terminal OnChange not recorded: delivered=%t seen=%v", task.delivered, seen)
	}

	// End to end: the terminal snapshot reaches OnChange and the slot is free.
	r = deliveryTestRuntime()
	fillTasks(r, "unread", taskLimit-1, time.Second, false)
	var mu sync.Mutex
	terminal := make(chan taskSnapshot, 1)
	owner := taskOwner{Generation: 1, CallID: "c2", OnChange: func(s taskSnapshot) {
		mu.Lock()
		defer mu.Unlock()
		if s.State != "running" {
			terminal <- s
		}
	}}
	s, err := r.startTask(cancelledParent(), owner, "a", "{}", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case snapshot := <-terminal:
		if snapshot.TaskID != s.TaskID || snapshot.State != "cancelled" {
			t.Fatalf("unexpected terminal snapshot: %+v", snapshot)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnChange never saw the terminal snapshot")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		_, held := r.tasks[s.TaskID]
		r.mu.Unlock()
		if !held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delivered service call task still holds a slot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := r.startTask(cancelledParent(), taskOwner{SessionID: "s", Generation: 1}, "a", "{}", time.Second); err != nil {
		t.Fatalf("slot of the delivered service call task not reusable: %v", err)
	}
}
