package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

// countingStartupPlugin is a built-in whose OnStartup records concurrency and
// blocks until release is closed.
type countingStartupPlugin struct {
	id       string
	active   *atomic.Int64
	peak     *atomic.Int64
	started  chan string
	release  chan struct{}
	startups atomic.Int64
}

func (plugin *countingStartupPlugin) Meta() api.Meta {
	return api.Meta{ID: plugin.id, Name: plugin.id, Version: "1.0.0"}
}

func (plugin *countingStartupPlugin) OnStartup(ctx context.Context) error {
	plugin.startups.Add(1)
	current := plugin.active.Add(1)
	defer plugin.active.Add(-1)
	for {
		peak := plugin.peak.Load()
		if current <= peak || plugin.peak.CompareAndSwap(peak, current) {
			break
		}
	}
	if plugin.started != nil {
		plugin.started <- plugin.id
	}
	select {
	case <-plugin.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (plugin *countingStartupPlugin) OnShutdown(context.Context) error { return nil }

func (plugin *countingStartupPlugin) Invoke(context.Context, string, string) (string, error) {
	return "{}", nil
}

func TestStartupInBackgroundWaitsForDelayBeforeActivating(t *testing.T) {
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	manager.startupDelay = 200 * time.Millisecond
	finished := make(chan error, 1)
	started := time.Now()
	if err := manager.StartupInBackground(5*time.Second, func(err error) { finished <- err }); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("StartupInBackground blocked the caller for %s", elapsed)
	}
	time.Sleep(50 * time.Millisecond)
	if calls := plugin.startupCalls.Load(); calls != 0 {
		t.Fatalf("startup plugin activated before the delay elapsed (calls=%d)", calls)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("background startup failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background startup did not finish")
	}
	if elapsed := time.Since(started); elapsed < manager.startupDelay {
		t.Fatalf("startup finished after %s, before the %s delay", elapsed, manager.startupDelay)
	}
	if plugin.startupCalls.Load() != 1 || record.state != pluginStateRunning {
		t.Fatalf("expected one activation and running state, got calls=%d state=%s", plugin.startupCalls.Load(), record.state)
	}
}

func TestStartupInBackgroundSkipsActivationWhenShutDownDuringDelay(t *testing.T) {
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	manager.startupDelay = time.Hour
	finished := make(chan error, 1)
	if err := manager.StartupInBackground(5*time.Second, func(err error) { finished <- err }); err != nil {
		t.Fatal(err)
	}
	manager.Shutdown(context.Background())
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("skipped startup reported an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not observe shutdown during its delay")
	}
	if plugin.startupCalls.Load() != 0 || record.state == pluginStateRunning {
		t.Fatalf("plugin activated after shutdown: calls=%d state=%s", plugin.startupCalls.Load(), record.state)
	}
	// awaitStartup must not hang once startup was skipped.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.awaitStartup(ctx); err != nil {
		t.Fatalf("awaitStartup after skipped startup: %v", err)
	}
}

func TestOnDemandActivationIsNotDelayedByStartupDelay(t *testing.T) {
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	manager.startupDelay = time.Hour
	if err := manager.StartupInBackground(5*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.startRecord(ctx, record); err != nil {
		t.Fatalf("on-demand activation during the startup delay: %v", err)
	}
	if record.state != pluginStateRunning {
		t.Fatalf("expected running, got %s", record.state)
	}
}

func TestActivateStartupRecordsLimitsConcurrentColdStarts(t *testing.T) {
	var active, peak atomic.Int64
	release := make(chan struct{})
	started := make(chan string, 8)
	manager := &pluginManager{records: map[string]*pluginRecord{}, startupConcurrency: 2}
	plugins := make([]*countingStartupPlugin, 0, 5)
	for index := range 5 {
		plugin := &countingStartupPlugin{id: fmt.Sprintf("test.startup.%d", index), active: &active, peak: &peak, started: started, release: release}
		plugins = append(plugins, plugin)
		record := &pluginRecord{id: plugin.id, source: pluginSourceBuiltin, builtin: plugin, enabled: true, state: pluginStateStopped}
		manager.records[record.id] = record
		manager.order = append(manager.order, record.id)
	}
	done := make(chan error, 1)
	go func() { done <- manager.activateStartupRecords(context.Background()) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("first startup slots were not used")
		}
	}
	select {
	case id := <-started:
		t.Fatalf("%s started while both slots were busy", id)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("startup batch failed: %v", err)
	}
	if got := peak.Load(); got != 2 {
		t.Fatalf("expected at most 2 concurrent cold starts and full use of both slots, peak=%d", got)
	}
	for _, plugin := range plugins {
		if plugin.startups.Load() != 1 {
			t.Fatalf("%s started %d times", plugin.id, plugin.startups.Load())
		}
	}
}

func TestActivateStartupRecordsQueuedPluginsStayRecoverableOnDeadline(t *testing.T) {
	var active, peak atomic.Int64
	release := make(chan struct{})
	defer close(release)
	started := make(chan string, 4)
	manager := &pluginManager{records: map[string]*pluginRecord{}, startupConcurrency: 1}
	for index := range 2 {
		plugin := &countingStartupPlugin{id: fmt.Sprintf("test.queued.%d", index), active: &active, peak: &peak, started: started, release: release}
		record := &pluginRecord{id: plugin.id, source: pluginSourceBuiltin, builtin: plugin, enabled: true, state: pluginStateStopped}
		manager.records[record.id] = record
		manager.order = append(manager.order, record.id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := manager.activateStartupRecords(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a deadline error for the batch, got %v", err)
	}
	// Goroutines race for the single slot, so either record may hold it.
	states := map[string]int{}
	for _, record := range manager.records {
		record.mu.Lock()
		states[record.state]++
		record.mu.Unlock()
	}
	if states[pluginStateStopped] != 1 || states[pluginStateStarting] != 1 {
		t.Fatalf("expected one starting record and one still stopped (queued, lazily activatable), got %v", states)
	}
}

func TestStartRecordFastPathAdmitsRunningPluginWithoutReactivation(t *testing.T) {
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	if err := manager.startRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.startRecord(ctx, record); err != nil {
		t.Fatalf("running plugin admission: %v", err)
	}
	if plugin.startupCalls.Load() != 1 {
		t.Fatalf("fast path re-ran activation: calls=%d", plugin.startupCalls.Load())
	}
	// validate still runs, under record.mu, and its error is returned.
	denied := errors.New("approval invalidated")
	var calls atomic.Int64
	err := manager.startRecordValidated(ctx, record, func(current *pluginRecord) error {
		calls.Add(1)
		if current != record {
			t.Errorf("validate received another record")
		}
		if record.mu.TryLock() {
			record.mu.Unlock()
			t.Errorf("validate must run under record.mu")
		}
		return denied
	})
	if !errors.Is(err, denied) || calls.Load() != 1 {
		t.Fatalf("fast path validate: err=%v calls=%d", err, calls.Load())
	}
}

// A lifecycle operation (development generation swap, stop, reload) keeps the
// state "running" while it replaces the runtime. The fast path must not admit
// a caller then; the caller waits for the operation instead.
func TestStartRecordFastPathWaitsForLifecycleOperation(t *testing.T) {
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	if err := manager.startRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	record.lifecycleMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err := manager.startRecord(ctx, record)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		record.lifecycleMu.Unlock()
		t.Fatalf("admitted during a lifecycle operation: %v", err)
	}
	// Once the operation finishes the caller proceeds.
	done := make(chan error, 1)
	go func() { done <- manager.startRecord(context.Background(), record) }()
	time.Sleep(20 * time.Millisecond)
	record.lifecycleMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("admission after the operation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("caller still waiting after the lifecycle operation finished")
	}
}

func TestStartRecordFastPathKeepsAdmissionChecks(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*pluginManager, *pluginRecord)
		want   string
	}{
		{"disabled", func(_ *pluginManager, record *pluginRecord) { record.enabled = false }, "disabled"},
		{"workspace", func(manager *pluginManager, record *pluginRecord) {
			record.scope = pluginScopeWorkspace
			record.workspaces = map[string]struct{}{"other": {}}
			setWorkspaceScopeFixture(manager, "current")
		}, "not enabled in the active workspace"},
		{"error", func(_ *pluginManager, record *pluginRecord) {
			record.state = pluginStateError
			record.lastError = "boom"
		}, "reload explicitly"},
		{"removed", func(manager *pluginManager, record *pluginRecord) {
			manager.records[record.id] = &pluginRecord{id: record.id}
		}, "removed"},
		{"shutting-down", func(manager *pluginManager, _ *pluginRecord) { manager.shuttingDown = true }, "shutting down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plugin := &testLifecyclePlugin{}
			manager, record := testManagerWithPlugin(plugin, true)
			if err := manager.startRecord(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			record.mu.Lock()
			manager.mu.Lock()
			tc.mutate(manager, record)
			manager.mu.Unlock()
			record.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := manager.startRecord(ctx, record)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestStartRecordFastPathIsRaceFreeWithStop(t *testing.T) {
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	if err := manager.startRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = manager.startRecord(ctx, record)
				cancel()
			}
		}()
	}
	for range 5 {
		_ = manager.stopRecord(context.Background(), record, false)
		_ = manager.startRecord(context.Background(), record)
	}
	wg.Wait()
	record.mu.Lock()
	state := record.state
	record.mu.Unlock()
	if state != pluginStateRunning {
		t.Fatalf("expected running after concurrent start/stop, got %s", state)
	}
}

func TestActivationTimeoutCoversInitializeTimeout(t *testing.T) {
	if pluginActivationTimeout <= pluginInitializeTimeout {
		t.Fatalf("activation timeout %s must exceed plugin.initialize timeout %s", pluginActivationTimeout, pluginInitializeTimeout)
	}
	if pluginStartupTimeout >= pluginActivationTimeout {
		t.Fatalf("caller wait %s should be shorter than the activation bound %s", pluginStartupTimeout, pluginActivationTimeout)
	}
}

func TestNewPluginManagerUsesDefaultStartupDelay(t *testing.T) {
	useTemporaryPluginData(t)
	manager := newPluginManager()
	if manager.startupDelay != defaultStartupActivationDelay {
		t.Fatalf("expected default startup delay %s, got %s", defaultStartupActivationDelay, manager.startupDelay)
	}
	for _, record := range manager.records {
		if record.state == pluginStateRunning || record.state == pluginStateStarting {
			t.Fatalf("static discovery started %s", record.id)
		}
	}
}
