package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	backendgit "mincode/app/backend/git"
	"mincode/app/plugins/api"
	"time"
)

func statusEventAvailable(rt *processRuntime, name string) bool {
	for _, e := range rt.describeEvents() {
		if e["name"] == name {
			return e["available"] == true
		}
	}
	return false
}

// Removing the interval gate, baseline comparison, error guard or final scope
// check must fail observable delivery assertions, with no real three-second wait.
func TestStatusEventsBoundedPoll(t *testing.T) {
	m, rt, _, output := fileEventsFixture(t)
	if !statusEventAvailable(rt, "git.statusChanged") {
		t.Fatal("git.statusChanged missing or unavailable")
	}
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	m.statusEvents.gitMu.Lock()
	absent := m.statusEvents.gitSource == nil
	m.statusEvents.gitMu.Unlock()
	if !absent {
		t.Fatal("no subscribers retained a source")
	}
	ctx, err := m.workspaceScopeContext(m.workspaceScope())
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	calls := 0
	next := backendgit.PluginStatusSnapshot{Digest: "baseline", Unstaged: 1}
	var failure error
	source := &gitStatusSource{manager: m, scope: m.workspaceScope(), ctx: ctx, sample: func(context.Context, string, func(string) bool) (backendgit.PluginStatusSnapshot, error) {
		calls++
		return next, failure
	}}
	start := time.Unix(1000, 0)
	source.poll(start)
	if calls != 0 {
		t.Fatal("sample started without subscribers")
	}
	rt.mu.Lock()
	rt.eventSubscriptions = map[string]struct{}{"git.statusChanged": {}}
	rt.mu.Unlock()
	source.poll(start)
	assertNoStatusEvent(t, output)
	source.poll(start.Add(2999 * time.Millisecond))
	if calls != 1 {
		t.Fatal("polled sooner than three seconds")
	}
	source.poll(start.Add(3 * time.Second))
	if calls != 2 {
		t.Fatal("three-second poll not admitted")
	}
	assertNoStatusEvent(t, output)
	next = backendgit.PluginStatusSnapshot{Digest: "next", Staged: 2, Unstaged: 3, Untracked: 4, Conflicted: 5}
	source.poll(start.Add(6 * time.Second))
	select {
	case raw := <-output:
		var event struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Event != "git.statusChanged" || len(event.Data) != 5 || event.Data["scopeRevision"] != source.scope.Revision || event.Data["staged"] != float64(2) || event.Data["unstaged"] != float64(3) || event.Data["untracked"] != float64(4) || event.Data["conflicted"] != float64(5) {
			t.Fatalf("unsafe/wrong status payload: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("changed digest not delivered")
	}
	// All failures leave the last known baseline alone, never fabricate clean.
	for i, err := range []error{context.DeadlineExceeded, errors.New("4 MiB overflow"), errors.New("non repository")} {
		failure = err
		next = backendgit.PluginStatusSnapshot{}
		source.poll(start.Add(time.Duration(9+3*i) * time.Second))
		assertNoStatusEvent(t, output)
	}
	if source.baseline != "next" {
		t.Fatal("unknown sample replaced last known status")
	}
	// Slow samples cannot reenter; cancellation/scope switch drops late success.
	entered, release := make(chan struct{}), make(chan struct{})
	var concurrent atomic.Int32
	source.sample = func(context.Context, string, func(string) bool) (backendgit.PluginStatusSnapshot, error) {
		concurrent.Add(1)
		close(entered)
		<-release
		return backendgit.PluginStatusSnapshot{Digest: "late"}, nil
	}
	done := make(chan struct{})
	go func() { source.poll(start.Add(30 * time.Second)); close(done) }()
	<-entered
	source.poll(start.Add(40 * time.Second))
	if concurrent.Load() != 1 {
		t.Fatal("overlapping root samples")
	}
	if _, err := m.beginWorkspaceScope(context.Background(), "B", false); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	assertNoStatusEvent(t, output)
	if source.baseline != "next" {
		t.Fatal("late canceled result changed baseline")
	}
}

func assertNoStatusEvent(t *testing.T, output <-chan []byte) {
	t.Helper()
	select {
	case raw := <-output:
		t.Fatalf("unexpected event: %s", raw)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestPowerAvailabilityRequiresConfirmedSource(t *testing.T) {
	m, rt, _, output := fileEventsFixture(t)
	source, ok := any(m).(interface {
		setPowerEventAvailability(bool, bool)
		publishPowerEvent(string)
	})
	if !ok {
		t.Fatal("power source confirmation seam is missing")
	}
	for _, name := range []string{"app.suspend", "app.resume"} {
		if statusEventAvailable(rt, name) {
			t.Fatal("unconfirmed native source advertised available")
		}
		if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":["`+name+`"]}`)); err == nil || err.Code != -32002 || err.Data.(map[string]string)["code"] != "unavailable" {
			t.Fatal("unsupported subscription silently accepted")
		}
	}
	source.setPowerEventAvailability(true, true)
	if !statusEventAvailable(rt, "app.suspend") || !statusEventAvailable(rt, "app.resume") {
		t.Fatal("confirmed source not available")
	}
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":["app.suspend","app.resume"]}`)); err != nil {
		t.Fatal(err)
	}
	source.publishPowerEvent("app.suspend")
	source.publishPowerEvent("app.resume")
	for i := 0; i < 2; i++ {
		select {
		case raw := <-output:
			var event struct {
				Event string         `json:"event"`
				Data  map[string]any `json:"data"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if len(event.Data) != 0 {
				t.Fatalf("power payload leaked state: %s", raw)
			}
		case <-time.After(time.Second):
			t.Fatal("confirmed event not delivered")
		}
	}
	source.setPowerEventAvailability(false, false)
	if statusEventAvailable(rt, "app.resume") {
		t.Fatal("revoked source still available")
	}
}

func TestLockEventTransitionsOnly(t *testing.T) {
	m, rt, _, output := fileEventsFixture(t)
	source, ok := any(m).(interface{ appLockStateChanged(bool) })
	if !ok {
		t.Fatal("Go lock state transition seam is missing")
	}
	source.appLockStateChanged(false) // establishes initial state, no replay
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":["app.lockChanged"]}`)); err != nil {
		t.Fatal(err)
	}
	source.appLockStateChanged(false)
	source.appLockStateChanged(true)
	select {
	case raw := <-output:
		var event struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		_ = json.Unmarshal(raw, &event)
		if event.Event != "app.lockChanged" || len(event.Data) != 1 || event.Data["locked"] != true {
			t.Fatalf("unsafe lock event: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("lock transition not delivered")
	}
	source.appLockStateChanged(true)
	select {
	case raw := <-output:
		t.Fatalf("lock event replayed: %s", raw)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestStatusEventsLifecycleAndNames(t *testing.T) {
	m, rt, _, _ := fileEventsFixture(t)
	names := []string{"workspace.changed", "workspace.rootChanged", "editor.activeFileChanged", "editor.fileSaved", "editor.selectionChanged", hostEventGitHeadChanged, hostEventAIActivity, hostEventAITasks, "workspace.fileCreated", "workspace.fileDeleted", "workspace.fileRenamed", "workspace.rescanRequired", "git.statusChanged", "app.lockChanged", "app.suspend", "app.resume", testRunEndedEvent}
	if len(hostEvents) != 17 {
		t.Fatalf("expected eight old, eight search/status and tests.runEnded names, got %d", len(hostEvents))
	}
	for _, name := range names {
		if _, ok := hostEvents[name]; !ok {
			t.Fatalf("missing name %s", name)
		}
	}
	if len(frontendHostEvents) != 3 || frontendHostEvents["app.lockChanged"] || frontendHostEvents["app.resume"] {
		t.Fatal("expanded WebView publisher authority")
	}
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":["git.statusChanged"]}`)); err != nil {
		t.Fatal(err)
	}
	m.statusEvents.gitMu.Lock()
	first := m.statusEvents.gitSource
	m.statusEvents.gitMu.Unlock()
	if first == nil {
		t.Fatal("running subscriber did not start source")
	}
	m.syncGitStatusEvents()
	m.statusEvents.gitMu.Lock()
	same := m.statusEvents.gitSource == first
	m.statusEvents.gitMu.Unlock()
	if !same {
		t.Fatal("root acquired a duplicate poll source")
	}
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("last unsubscribe did not join source")
	}
	rt.mu.Lock()
	old := rt.generation
	rt.generation++
	rt.eventSubscriptions = map[string]struct{}{}
	rt.mu.Unlock()
	if _, err := hostEventsSubscribeGeneration(rt, old, json.RawMessage(`{"events":["git.statusChanged"]}`)); err == nil {
		t.Fatal("stale handler sampled current generation")
	}
	m.statusEvents.gitMu.Lock()
	absent := m.statusEvents.gitSource == nil
	m.statusEvents.gitMu.Unlock()
	if !absent || rt.subscribedTo("git.statusChanged") {
		t.Fatal("stale subscription started source")
	}
	rt.mu.Lock()
	rt.generation = old
	rt.eventSubscriptions = map[string]struct{}{"git.statusChanged": {}}
	rt.mu.Unlock()
	rec := rt.ownerRecord
	rec.mu.Lock()
	rec.enabled = false
	rec.mu.Unlock()
	m.syncGitStatusEvents()
	m.statusEvents.gitMu.Lock()
	absent = m.statusEvents.gitSource == nil
	m.statusEvents.gitMu.Unlock()
	if !absent {
		t.Fatal("disabled subscriber drove sampling")
	}
	rec.mu.Lock()
	rec.enabled = true
	rec.scope = "workspace"
	rec.workspaces = map[string]struct{}{"elsewhere": {}}
	rec.mu.Unlock()
	m.syncGitStatusEvents()
	m.statusEvents.gitMu.Lock()
	absent = m.statusEvents.gitSource == nil
	m.statusEvents.gitMu.Unlock()
	if !absent {
		t.Fatal("ineffective workspace subscriber drove sampling")
	}
	rt.mu.Lock()
	delete(rt.capabilities, api.CapabilityWorkspaceRead)
	rt.mu.Unlock()
	if statusEventAvailable(rt, "git.statusChanged") {
		t.Fatal("status describe ignored capability")
	}
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":["git.statusChanged"]}`)); err == nil {
		t.Fatal("status subscribe ignored capability")
	}
}

func TestStatusEventsPathFilter(t *testing.T) {
	_, _, root, _ := fileEventsFixture(t)
	for _, p := range []string{".env", "a/.git/config", "a/.env/old.txt", ".HG/store", "../escape", "/absolute", "x/../../escape"} {
		if gitStatusPathAllowed(root, p) {
			t.Fatalf("unsafe Git count path accepted: %s", p)
		}
	}
	if !gitStatusPathAllowed(root, "deleted.txt") || !gitStatusPathAllowed(root, "old/deleted.txt") {
		t.Fatal("safe deleted/rename old side excluded")
	}
	writeWorkspaceFile(t, root, "ordinary.txt", "x")
	if !gitStatusPathAllowed(root, "ordinary.txt") {
		t.Fatal("ordinary path excluded")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if gitStatusPathAllowed(root, "outside/missing.txt") {
		t.Fatal("missing child beneath outside symlink counted")
	}
	if err := os.Symlink(filepath.Join(root, "ordinary.txt"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if gitStatusPathAllowed(root, "alias") {
		t.Fatal("symlink counted")
	}
}

func TestPowerAvailabilityResumeUsesExistingBoundedSource(t *testing.T) {
	m, _, _, output := fileEventsFixture(t)
	source := m.fileEvents.Load()
	if source == nil {
		t.Fatal("fixture source absent")
	}
	m.setPowerEventAvailability(true, true)
	m.publishPowerEvent("app.resume")
	events := readFileEvents(t, output, 400*time.Millisecond)
	found := false
	for _, event := range events {
		if event.Event == "workspace.rescanRequired" && event.Data.Reason == "sourceLost" && event.Data.ScopeRevision == source.scope.Revision {
			found = true
		}
	}
	if !found {
		t.Fatalf("resume did not mark bounded rescan: %+v", events)
	}
	if m.fileEvents.Load() != source {
		t.Fatal("resume replaced/duplicated watcher")
	}
	m.closeWorkspaceFileEvents()
	m.publishPowerEvent("app.resume")
	if m.fileEvents.Load() != nil {
		t.Fatal("resume started source without file subscribers")
	}
}

func TestStatusEventsRejectedSubscriptionReleasesSource(t *testing.T) {
	m, rt, root, _ := fileEventsFixture(t)
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":["git.statusChanged","workspace.fileCreated"]}`)); err == nil {
		t.Fatal("failed file source accepted")
	}
	m.statusEvents.gitMu.Lock()
	retained := m.statusEvents.gitSource != nil
	m.statusEvents.gitMu.Unlock()
	if rt.subscribedTo("git.statusChanged") || retained {
		t.Fatal("rejected mixed subscription retained a Git source")
	}
}

func TestLockEventPreservesShortcutReconciliation(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("open", "CmdOrCtrl+Shift+O", "view", "main"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	enableShortcut(t, h.registry, "acme.shot", "open")
	token := issueActivationToken("acme.shot", "open")
	h.setLocked(true)
	AppLockStateChanged(true)
	if h.desk.isRegistered("CmdOrCtrl+Shift+O") {
		t.Fatal("new lock observer lost shortcut unregistration")
	}
	if _, ok := consumeActivationToken("acme.shot", token); ok {
		t.Fatal("new lock observer lost token revocation")
	}
	h.setLocked(false)
	AppLockStateChanged(false)
	if !h.desk.isRegistered("CmdOrCtrl+Shift+O") {
		t.Fatal("new observer lost shortcut restoration")
	}
}

// A native transition between the initial snapshot and host publication must
// become the initial baseline, not a replay on the next preference-only update.
func TestLockEventBootstrapInterleaving(t *testing.T) {
	m, rt, _, output := fileEventsFixture(t)
	previous := workspaceMutationHost.Load()
	workspaceMutationHost.Store(nil)
	t.Cleanup(func() { workspaceMutationHost.Store(previous) })
	readStale := func() bool {
		AppLockStateChanged(true) // actual Go observer before singleton publication
		return false              // previously captured snapshot, delayed until after transition
	}
	m.publishStatusHost(readStale)
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":["app.lockChanged"]}`)); err != nil {
		t.Fatal(err)
	}
	AppLockStateChanged(true) // only preferences changed; boolean is still true
	assertNoStatusEvent(t, output)
	AppLockStateChanged(false)
	select {
	case raw := <-output:
		var event struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		_ = json.Unmarshal(raw, &event)
		if event.Event != "app.lockChanged" || len(event.Data) != 1 || event.Data["locked"] != false {
			t.Fatalf("real unlock lost after bootstrap: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("real unlock transition was lost")
	}
}

func TestLockEventPublishedLookupDoesNotWaitForObserver(t *testing.T) {
	m, rt, _, _ := fileEventsFixture(t)
	previous := workspaceMutationHost.Load()
	workspaceMutationHost.Store(nil)
	previousLockSource := appLockSource.Load()
	SetAppLockSource(func() bool { return true })
	t.Cleanup(func() { workspaceMutationHost.Store(previous); appLockSource.Store(previousLockSource) })
	m.publishStatusHost(func() bool { return false })
	record := rt.ownerRecord
	record.mu.Lock()
	observerDone := make(chan struct{})
	go func() { AppLockStateChanged(true); close(observerDone) }()
	// Wait until the real observer has updated the bool and is waiting for the
	// record lock to publish. No sleep or scheduler guess determines the window.
	deadline := time.NewTimer(time.Second)
	tick := time.NewTicker(time.Millisecond)
	updated := false
	for !updated {
		m.statusEvents.lockMu.Lock()
		updated = m.statusEvents.locked
		m.statusEvents.lockMu.Unlock()
		if updated {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			record.mu.Unlock()
			<-observerDone
			t.Fatal("observer did not reach publication")
		}
	}
	deadline.Stop()
	tick.Stop()
	lookupDone := make(chan struct{})
	go func() {
		m.publishStatusHost(func() bool { t.Error("published lookup re-read state"); return false })
		close(lookupDone)
	}()
	blocked := false
	select {
	case <-lookupDone:
	case <-time.After(50 * time.Millisecond):
		blocked = true
	}
	record.mu.Unlock()
	<-observerDone
	if blocked {
		<-lookupDone
		t.Fatal("published host lookup waited for an observer blocked on its caller's record lock")
	}
}
