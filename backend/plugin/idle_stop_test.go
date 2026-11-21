package plugin

import (
	"context"
	"testing"
	"time"
)

func shortIdleDelays(t *testing.T) {
	t.Helper()
	previous := [3]time.Duration{pluginIdleStopDelay, pluginIdleAfterInvokeDelay, pluginIdleRecheckInterval}
	pluginIdleStopDelay, pluginIdleAfterInvokeDelay, pluginIdleRecheckInterval = 20*time.Millisecond, 40*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		pluginIdleStopDelay, pluginIdleAfterInvokeDelay, pluginIdleRecheckInterval = previous[0], previous[1], previous[2]
	})
}

func recordState(record *pluginRecord) string {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.state
}

func waitRecordState(t *testing.T, record *pluginRecord, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for recordState(record) != want {
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want %s", recordState(record), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func enabledOnDemand(t *testing.T, activation string) (*pluginManager, *pluginRecord) {
	t.Helper()
	shortIdleDelays(t)
	manager, record := activationManager(t, activation)
	if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	return manager, record
}

// Closing the plugin's last view stops its process; it starts again on demand.
func TestIdleStopAfterLastViewCloses(t *testing.T) {
	manager, record := enabledOnDemand(t, "")
	first, err := manager.BeginUI(record.id, "view", "tango://app")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.BeginUI(record.id, "view", "tango://app")
	if err != nil {
		t.Fatal(err)
	}
	manager.CloseUI(first.SessionID)
	time.Sleep(10 * pluginIdleStopDelay)
	if recordState(record) != pluginStateRunning {
		t.Fatal("stopped while another view was still open")
	}
	manager.CloseUI(second.SessionID)
	waitRecordState(t, record, pluginStateStopped)

	if _, err := manager.BeginUI(record.id, "view", "tango://app"); err != nil {
		t.Fatal(err)
	}
	if recordState(record) != pluginStateRunning || record.runtime.Generation() != 2 {
		t.Fatalf("not reactivated on demand: %s gen %d", recordState(record), record.runtime.Generation())
	}
}

// Reopening a view before the grace delay keeps the same process.
func TestIdleStopCancelledByReopen(t *testing.T) {
	manager, record := enabledOnDemand(t, "")
	session, err := manager.BeginUI(record.id, "view", "tango://app")
	if err != nil {
		t.Fatal(err)
	}
	manager.CloseUI(session.SessionID)
	if _, err := manager.BeginUI(record.id, "view", "tango://app"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * pluginIdleStopDelay)
	if recordState(record) != pluginStateRunning || record.runtime.Generation() != 1 {
		t.Fatalf("reopened plugin was stopped: %s gen %d", recordState(record), record.runtime.Generation())
	}
}

// Work still in flight postpones the stop until it finishes.
func TestIdleStopWaitsForInFlightWork(t *testing.T) {
	manager, record := enabledOnDemand(t, "")
	session, err := manager.BeginUI(record.id, "view", "tango://app")
	if err != nil {
		t.Fatal(err)
	}
	record.inflight.Add(1)
	manager.CloseUI(session.SessionID)
	time.Sleep(10 * pluginIdleStopDelay)
	if recordState(record) != pluginStateRunning {
		t.Fatal("stopped while an invocation was in flight")
	}
	record.inflight.Add(-1)
	waitRecordState(t, record, pluginStateStopped)
}

// A plugin started only for a command/AI call stops again after it.
func TestIdleStopAfterInvoke(t *testing.T) {
	manager, record := enabledOnDemand(t, "")
	if _, err := manager.Invoke(context.Background(), record.id, "run", "{}"); err != nil {
		t.Fatal(err)
	}
	if recordState(record) != pluginStateRunning {
		t.Fatal("invoke did not activate")
	}
	if record.inflight.Load() != 0 {
		t.Fatalf("inflight leaked: %d", record.inflight.Load())
	}
	waitRecordState(t, record, pluginStateStopped)
}

// Startup plugins are background services and keep running.
func TestIdleStopSkipsStartupPlugins(t *testing.T) {
	manager, record := enabledOnDemand(t, "startup")
	session, err := manager.BeginUI(record.id, "view", "tango://app")
	if err != nil {
		t.Fatal(err)
	}
	manager.CloseUI(session.SessionID)
	time.Sleep(10 * pluginIdleAfterInvokeDelay)
	if recordState(record) != pluginStateRunning {
		t.Fatal("startup plugin was stopped")
	}
}

// runningServiceCallFor registers a plugin call owned by pluginID that is
// still running, as startServiceCall leaves it after dispatch.
func runningServiceCallFor(t *testing.T, manager *pluginManager, pluginID string) *serviceCall {
	t.Helper()
	call := &serviceCall{id: "sc_idle_" + pluginID, owner: pluginID, manager: manager, done: make(chan struct{}),
		snapshot: ServiceCallSnapshot{CallID: "sc_idle_" + pluginID, State: serviceCallRunning}}
	serviceCalls.mu.Lock()
	serviceCalls.calls[call.id] = call
	serviceCalls.mu.Unlock()
	t.Cleanup(func() {
		serviceCalls.mu.Lock()
		delete(serviceCalls.calls, call.id)
		serviceCalls.mu.Unlock()
	})
	return call
}

// An on-demand caller waiting on its service call is not stopped; that
// would cancel the call and drop the outputs handed to it.
func TestIdleStopWaitsForCallersServiceCall(t *testing.T) {
	manager, record := enabledOnDemand(t, "")
	call := runningServiceCallFor(t, manager, record.id)
	if _, err := manager.Invoke(context.Background(), record.id, "run", "{}"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * pluginIdleAfterInvokeDelay)
	if recordState(record) != pluginStateRunning {
		t.Fatal("caller stopped while its service call was running")
	}
	call.finish(serviceCallSucceeded, nil, nil, "", false)
	waitRecordState(t, record, pluginStateStopped)
}

// A finished service call gives its caller the post-invoke grace to read
// the result and outputs before it may stop.
func TestIdleStopGraceAfterCallersServiceCall(t *testing.T) {
	manager, record := enabledOnDemand(t, "")
	pluginIdleAfterInvokeDelay = 400 * time.Millisecond
	call := runningServiceCallFor(t, manager, record.id)
	if _, err := manager.Invoke(context.Background(), record.id, "run", "{}"); err != nil {
		t.Fatal(err)
	}
	// Past the post-invoke grace: the caller is held only by its call now and
	// rechecked every pluginIdleRecheckInterval.
	time.Sleep(pluginIdleAfterInvokeDelay + 10*pluginIdleRecheckInterval)
	if recordState(record) != pluginStateRunning {
		t.Fatal("caller stopped while its service call was running")
	}
	call.finish(serviceCallSucceeded, nil, nil, "", false)
	time.Sleep(10 * pluginIdleRecheckInterval)
	if recordState(record) != pluginStateRunning {
		t.Fatal("caller stopped before the grace after its service call ended")
	}
	waitRecordState(t, record, pluginStateStopped)
}
