package plugin

import (
	"context"
	"sync"
	"time"
)

// Idle stop: an on-demand process plugin goes back to dormant (stopped, starting
// again on demand) once nothing uses it — its last UI session closed, or the
// command / AI / contribution call that woke it finished — and no invocation,
// host-to-plugin request, long task, database-driver connection, service
// call it made or live test run is still in flight. Startup plugins are background services
// and development sessions are being debugged, so both keep running.
var (
	// Grace after the last view closes, so closing and reopening a view (tab
	// reload, quick switch) keeps the same process.
	pluginIdleStopDelay = 2 * time.Second
	// After a call without any open view: repeated calls reuse the process.
	pluginIdleAfterInvokeDelay = 30 * time.Second
	// While work is still in flight, check again this often.
	pluginIdleRecheckInterval = 5 * time.Second
)

type idleStopTimers struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

func (manager *pluginManager) scheduleIdleStop(pluginID string, delay time.Duration) {
	manager.mu.RLock()
	shuttingDown := manager.shuttingDown
	manager.mu.RUnlock()
	if pluginID == "" || shuttingDown {
		return
	}
	manager.idleStops.mu.Lock()
	defer manager.idleStops.mu.Unlock()
	if manager.idleStops.timers == nil {
		manager.idleStops.timers = map[string]*time.Timer{}
	}
	if timer := manager.idleStops.timers[pluginID]; timer != nil {
		timer.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		manager.idleStops.mu.Lock()
		current := manager.idleStops.timers[pluginID] == timer
		if current {
			delete(manager.idleStops.timers, pluginID)
		}
		manager.idleStops.mu.Unlock()
		if current && manager.stopIfIdle(pluginID) {
			manager.scheduleIdleStop(pluginID, pluginIdleRecheckInterval)
		}
	})
	manager.idleStops.timers[pluginID] = timer
}

func (manager *pluginManager) cancelIdleStops() {
	manager.idleStops.mu.Lock()
	for id, timer := range manager.idleStops.timers {
		timer.Stop()
		delete(manager.idleStops.timers, id)
	}
	manager.idleStops.mu.Unlock()
}

// idleStopEligibleLocked: a running, enabled, on-demand local process plugin
// outside a development session. Caller holds record.mu.
func idleStopEligibleLocked(record *pluginRecord) bool {
	return record.state == pluginStateRunning && record.enabled && record.runtime != nil && record.builtin == nil &&
		record.manifest != nil && record.manifest.Runtime.Activation != pluginActivationStartup && record.developmentID == ""
}

// pluginBusy reports work that must finish before the process may stop,
// including service calls the plugin made and still waits on, and an
// activation token it has not spent yet (stopping would revoke it, so the
// plugin keeps running until it spends the token or the token expires), and
// a live test run (test_runs.go: the plugin listens for its results).
func (manager *pluginManager) pluginBusy(record *pluginRecord, runtimeHost *processRuntime) bool {
	return record.inflight.Load() > 0 || runtimeHost.busy() || databaseDriverInUse(record.id) || serviceCallsRunningFor(record.id) ||
		activationTokenHeld(record.id) || testRunsLive(record.id)
}

// stopIfIdle stops the plugin when nothing uses it. It returns true when the
// plugin is still eligible but busy, so the caller checks again later.
func (manager *pluginManager) stopIfIdle(pluginID string) (retry bool) {
	record, err := manager.record(pluginID)
	if err != nil {
		return false
	}
	record.mu.Lock()
	eligible := idleStopEligibleLocked(record)
	runtimeHost := record.runtime
	record.mu.Unlock()
	// An open view holds the plugin; closing it schedules the next check.
	if !eligible || manager.uiHost.SessionCount(pluginID) > 0 {
		return false
	}
	if manager.pluginBusy(record, runtimeHost) || !record.lifecycleMu.TryLock() {
		return true
	}
	// Under the lifecycle lock no activation or admission can start; recheck.
	record.mu.Lock()
	eligible = idleStopEligibleLocked(record) && record.runtime == runtimeHost
	record.mu.Unlock()
	if !eligible || manager.uiHost.SessionCount(pluginID) > 0 {
		record.lifecycleMu.Unlock()
		return false
	}
	if manager.pluginBusy(record, runtimeHost) {
		record.lifecycleMu.Unlock()
		return true
	}
	record.appendLog("info", "no open view and nothing in flight; stopping until it is needed again")
	ctx, cancel := context.WithTimeout(context.Background(), pluginShutdownTimeout)
	_ = manager.stopRecordLocked(ctx, record, false)
	cancel()
	record.lifecycleMu.Unlock()
	manager.emitState(record)
	return false
}

// busy: a host-to-plugin request awaits its reply or a long task is running.
func (runtimeHost *processRuntime) busy() bool {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	if len(runtimeHost.pending) > 0 {
		return true
	}
	for _, task := range runtimeHost.tasks {
		if task.finished.IsZero() {
			return true
		}
	}
	return false
}

// databaseDriverInUse: the plugin backs an open database connection or call.
func databaseDriverInUse(pluginID string) bool {
	databaseDriverRegistry.mu.Lock()
	defer databaseDriverRegistry.mu.Unlock()
	state := databaseDriverRegistry.plugins[pluginID]
	return state != nil && (state.calls > 0 || len(state.sessions) > 0)
}
