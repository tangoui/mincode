package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

func activationManager(t *testing.T, activation string) (*pluginManager, *pluginRecord) {
	t.Helper()
	useTemporaryPluginData(t)
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "helper.exe"), data, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{ID: "runtime.test", Name: "Lazy", APIVersion: 2, Runtime: ProcessRuntimeManifest{Type: "process", Entry: "helper.exe", Args: []string{"-test.run=^TestPluginHelperProcess$"}, Activation: activation}, Contributes: ManifestContributions{
		Commands: []ManifestCommand{{ID: "run", Title: "Run", Action: "run"}},
		Views:    []ManifestView{{ID: "view", Title: "View", Entry: "ui/index.html"}},
		Actions:  []ManifestUIAction{{ID: "run", Risk: "read", AICallable: true, MaxPayloadBytes: 1024, InputSchema: map[string]any{"type": "object"}, Permissions: []api.Capability{}}},
	}}
	manager := &pluginManager{records: map[string]*pluginRecord{}, uiHost: newPluginUIHost()}
	if err := manager.registerLocal(root, manifest); err != nil {
		t.Fatal(err)
	}
	record, _ := manager.record(manifest.ID)
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	return manager, record
}

func TestActivationShutdownCancelsStartup(t *testing.T) {
	plugin := &testLifecyclePlugin{waitForCancel: true}
	manager, record := testManagerWithPlugin(plugin, true)
	done := make(chan error, 1)
	go func() { done <- manager.startRecord(context.Background(), record) }()
	deadline := time.After(time.Second)
	for plugin.startupCalls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("startup did not begin")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	manager.Shutdown(context.Background())
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("startup not canceled: %v", err)
	}
	if record.state == pluginStateRunning {
		t.Fatal("startup survived shutdown")
	}
}

func TestActivationDirectoryRemovalRetiresRecord(t *testing.T) {
	manager, record := activationManager(t, "")
	if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	record.root = filepath.Join(pluginsDirectory(), record.id)
	if err := manager.syncInstalled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.startRecord(context.Background(), record); err == nil {
		t.Fatal("stale removed record activated")
	}
	if record.runtime.Generation() != 0 {
		t.Fatal("directory sync started code")
	}
}

func TestActivationManifest(t *testing.T) {
	for _, value := range []string{"", "on-demand", "startup", "background", "STARTUP"} {
		t.Run(value, func(t *testing.T) {
			root := writeTestManifest(t, func(m *Manifest) { m.Runtime.Activation = value })
			m, err := readManifest(root)
			valid := value == "" || value == "on-demand" || value == "startup"
			if (err == nil) != valid {
				t.Fatalf("activation %q: %v", value, err)
			}
			if value == "" && m.Runtime.Activation != "on-demand" {
				t.Fatal("missing default")
			}
		})
	}
}

func TestActivationLazyDiscoveryAndConcurrentInvoke(t *testing.T) {
	manager, record := activationManager(t, "")
	if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if record.state != pluginStateStopped || record.runtime.Generation() != 0 {
		t.Fatal("eager activation")
	}
	if len(manager.Views()) != 1 {
		t.Fatal("missing static view")
	}
	record.mu.Lock()
	available := record.contributionsAvailableLocked()
	_, err := validateAIActionLocked(manager, record, "run", "{}")
	record.mu.Unlock()
	if !available || err != nil {
		t.Fatalf("static contributions unavailable: %v", err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := manager.Invoke(context.Background(), record.id, "run", "{}"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if record.runtime.Generation() != 1 {
		t.Fatalf("duplicate starts: %d", record.runtime.Generation())
	}
}

func TestActivationStartupAndBeginUI(t *testing.T) {
	for _, mode := range []string{"startup", "on-demand"} {
		t.Run(mode, func(t *testing.T) {
			manager, record := activationManager(t, mode)
			if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
				t.Fatal(err)
			}
			if mode == "startup" && record.runtime.Generation() != 1 {
				t.Fatal("background process not started")
			}
			if _, err := manager.BeginUI(record.id, "missing", "tango://app"); err == nil {
				t.Fatal("invalid view accepted")
			}
			if mode == "on-demand" && record.runtime.Generation() != 0 {
				t.Fatal("invalid view activated process")
			}
			if _, err := manager.BeginUI(record.id, "view", "tango://app"); err != nil {
				t.Fatal(err)
			}
			if record.runtime.Generation() != 1 {
				t.Fatal("UI did not activate")
			}
		})
	}
}

func TestActivationDisabledErrorDeadlineAndShutdown(t *testing.T) {
	manager, record := activationManager(t, "")
	if err := manager.ensureRunning(context.Background(), record.id); err == nil {
		t.Fatal("disabled activated")
	}
	if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	record.lifecycleMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := manager.ensureRunning(ctx, record.id)
	cancel()
	record.lifecycleMu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unbounded waiter: %v", err)
	}
	record.mu.Lock()
	record.state = pluginStateError
	record.lastError = "failed"
	record.mu.Unlock()
	if err := manager.ensureRunning(context.Background(), record.id); err == nil {
		t.Fatal("error retried")
	}
	if err := manager.SetEnabled(context.Background(), record.id, true); err == nil {
		t.Fatal("enable retried error")
	}
	if record.runtime.Generation() != 0 {
		t.Fatal("rejected requests started process")
	}
	if err := manager.Reload(context.Background(), record.id); err != nil {
		t.Fatal(err)
	}
	manager.Shutdown(context.Background())
	if err := manager.ensureRunning(context.Background(), record.id); err == nil {
		t.Fatal("activated after shutdown")
	}
}

func TestActivationAIApproval(t *testing.T) {
	manager, record := activationManager(t, "")
	// Initialize the singleton before temporarily routing the trusted host handlers.
	_ = host()
	previous := managerInst
	managerInst = manager
	t.Cleanup(func() { managerInst = previous })
	if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	prepare := func() string {
		t.Helper()
		raw, err := pluginAIPrepareHandler("", `{"id":"runtime.test","action":"run","payload":"{}"}`)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Ticket string `json:"ticket"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		return result.Ticket
	}
	ticket := prepare()
	if record.runtime.Generation() != 0 {
		t.Fatal("prepare activated")
	}
	args, _ := json.Marshal(map[string]any{"ticket": ticket, "approved": false})
	if _, err := pluginAIExecuteHandler("", string(args)); err == nil {
		t.Fatal("denial accepted")
	}
	if record.runtime.Generation() != 0 {
		t.Fatal("denial activated")
	}
	ticket = prepare()
	record.manifest.Contributes.Actions[0].Description = "changed"
	args, _ = json.Marshal(map[string]any{"ticket": ticket, "approved": true})
	if _, err := pluginAIExecuteHandler("", string(args)); err == nil {
		t.Fatal("changed contract accepted")
	}
	if record.runtime.Generation() != 0 {
		t.Fatal("stale ticket activated")
	}
	ticket = prepare()
	args, _ = json.Marshal(map[string]any{"ticket": ticket, "approved": true})
	if _, err := pluginAIExecuteHandler("", string(args)); err != nil {
		t.Fatal(err)
	}
	if record.runtime.Generation() != 1 {
		t.Fatal("approved execution did not activate")
	}
	if _, err := pluginAIExecuteHandler("", string(args)); err == nil {
		t.Fatal("ticket replay accepted")
	}
}

func TestDevelopmentContributionsAvailable(t *testing.T) {
	// 开发会话插件停止/启动中也应暴露静态贡献，否则 BeginUI 预校验会在懒激活前拒绝打开视图。
	record := &pluginRecord{
		source: pluginSourceDevelopment, enabled: true, state: pluginStateStopped,
		manifest: &Manifest{}, runtime: &processRuntime{},
	}
	record.mu.Lock()
	available := record.contributionsAvailableLocked()
	record.mu.Unlock()
	if !available {
		t.Fatal("development plugin static contributions unavailable while stopped")
	}
	record.state = pluginStateStarting
	record.mu.Lock()
	available = record.contributionsAvailableLocked()
	record.mu.Unlock()
	if !available {
		t.Fatal("development plugin static contributions unavailable while starting")
	}
	record.enabled = false
	record.mu.Lock()
	available = record.contributionsAvailableLocked()
	record.mu.Unlock()
	if available {
		t.Fatal("disabled development plugin must not expose contributions")
	}
}

type callerCancelingStartupPlugin struct {
	testLifecyclePlugin
	cancelCaller context.CancelFunc
}

func (plugin *callerCancelingStartupPlugin) OnStartup(ctx context.Context) error {
	plugin.startupCalls.Add(1)
	plugin.cancelCaller()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(20 * time.Millisecond):
		return nil
	}
}

// A lazy activation triggered by a short-lived request (e.g. a hover that the
// editor cancels) must finish starting instead of stranding the plugin in error.
func TestActivationSurvivesCallerCancellation(t *testing.T) {
	plugin := &callerCancelingStartupPlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	defer manager.Shutdown(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plugin.cancelCaller = cancel
	_ = manager.startRecord(ctx, record)
	deadline := time.Now().Add(time.Second)
	for {
		record.mu.Lock()
		state, lastError := record.state, record.lastError
		record.mu.Unlock()
		if state == pluginStateRunning && lastError == "" {
			break
		}
		if state == pluginStateError || time.Now().After(deadline) {
			t.Fatalf("caller cancellation aborted startup: state %q, lastError %q", state, lastError)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := manager.startRecord(context.Background(), record); err != nil {
		t.Fatalf("later request did not reuse running plugin: %v", err)
	}
	if calls := plugin.startupCalls.Load(); calls != 1 {
		t.Fatalf("startup calls = %d", calls)
	}
}
