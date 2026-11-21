package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The jdtls started by opening a Java file is initialized with exactly the
// provider's jars (lsp prepareJdtlsInitialize → jdtlsBundlePaths). These
// tests pin what the frontend's first debug then sees: a trusted, unchanged
// bundle is already loaded (no restart, no restart-required), while an
// untrusted or changed one still goes through confirmation and a restart.

// startJdtlsFromFileOpen simulates jdtls initializing now: the session loads
// what the provider returns at this moment.
func (fixture *jdtlsBundleFixture) startJdtlsFromFileOpen() {
	fixture.session = fixture.manager.jdtlsBundlePaths()
	fixture.running = true
}

func TestTrustedBundleLoadedAtFirstJdtlsStartNeedsNoRestart(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	fixture.trust(t, jdtlsBundleTestPlugin)
	fixture.manager.checkJdtlsBundleSet()
	fixture.notified.Store(0)

	fixture.startJdtlsFromFileOpen()
	if len(fixture.session) != 1 {
		t.Fatalf("jdtls started without the trusted bundle: %v", fixture.session)
	}
	// What ensureJdtlsBundlesReady reads on the first debug.
	status := fixture.status(t)
	if status.RestartRequired || !status.Jdtls.Running || len(status.Bundles) != 1 {
		t.Fatalf("status = %+v", status)
	}
	if bundle := status.Bundles[0]; !bundle.Trusted || !bundle.Loaded {
		t.Fatalf("bundle = %+v, want trusted and loaded", bundle)
	}
	// Nothing about the set changed: no restart-required on the way.
	fixture.manager.checkJdtlsBundleSet()
	fixture.manager.emitContributionsChanged(jdtlsBundleTestPlugin, nil)
	if fixture.notified.Load() != 0 {
		t.Fatalf("restart-required emitted %d times for an unchanged trusted bundle", fixture.notified.Load())
	}
}

// Review Focus 5: jdtls already runs because a Java file is open and the
// bundle is not trusted (never, or its jar changed): the first debug must
// still ask and restart; the untrusted jar never reaches jdtls.
func TestUntrustedOrChangedBundleStillNeedsConfirmAndRestart(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	fixture.manager.checkJdtlsBundleSet()
	fixture.startJdtlsFromFileOpen()
	if len(fixture.session) != 0 {
		t.Fatalf("untrusted bundle loaded at jdtls start: %v", fixture.session)
	}
	bundle := fixture.bundle(t, jdtlsBundleTestPlugin)
	if bundle.Trusted || bundle.Loaded {
		t.Fatalf("untrusted bundle = %+v, want confirmation", bundle)
	}
	// The user confirms: the set changes, the running jdtls lacks it.
	fixture.trust(t, jdtlsBundleTestPlugin)
	if status := fixture.status(t); !status.RestartRequired || status.Bundles[0].Loaded || fixture.notified.Load() != 1 {
		t.Fatalf("after trust: status %+v, notified %d", status, fixture.notified.Load())
	}
	// Restarted with it, then the jar changes on disk.
	fixture.startJdtlsFromFileOpen()
	if status := fixture.status(t); status.RestartRequired || !status.Bundles[0].Loaded {
		t.Fatalf("after restart: %+v", status)
	}
	jar := filepath.Join(fixture.root, "lib", "debug.jar")
	if err := os.WriteFile(jar, []byte("jar-v2-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := fixture.bundle(t, jdtlsBundleTestPlugin)
	if changed.Trusted || !changed.PreviouslyTrusted || changed.Loaded {
		t.Fatalf("changed bundle = %+v, want a new confirmation", changed)
	}
	// A jdtls started now (another file open) does not load the changed jar.
	fixture.startJdtlsFromFileOpen()
	if len(fixture.session) != 0 {
		t.Fatalf("changed jar loaded: %v", fixture.session)
	}
}

// workspaceScopedTrustedBundle makes the fixture's plugin trusted and then
// scoped to workspace ws-a (trusting needs the plugin active).
func workspaceScopedTrustedBundle(t *testing.T) *jdtlsBundleFixture {
	t.Helper()
	fixture := newJdtlsBundleFixture(t)
	fixture.trust(t, jdtlsBundleTestPlugin)
	fixture.record.mu.Lock()
	fixture.record.scope, fixture.record.workspaces = pluginScopeWorkspace, map[string]struct{}{"ws-a": {}}
	fixture.record.mu.Unlock()
	return fixture
}

func setJdtlsBundleScopeWait(t *testing.T, wait time.Duration) {
	t.Helper()
	previous := jdtlsBundleScopeWait
	jdtlsBundleScopeWait = wait
	t.Cleanup(func() { jdtlsBundleScopeWait = previous })
}

// A jdtls that initializes before the main window has begun its first
// workspace scope must not lose a workspace-scoped plugin's bundle (which
// would cost a restart on the first debug): the provider waits for the scope.
func TestJdtlsBundlesWaitForFirstWorkspaceScope(t *testing.T) {
	fixture := workspaceScopedTrustedBundle(t)
	setJdtlsBundleScopeWait(t, 10*time.Second)
	result := make(chan []string, 1)
	go func() { result <- fixture.manager.jdtlsBundlePaths() }()
	select {
	case paths := <-result:
		t.Fatalf("provider answered %v before any workspace scope", paths)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := fixture.manager.beginWorkspaceScope(context.Background(), "ws-a", false); err != nil {
		t.Fatal(err)
	}
	select {
	case paths := <-result:
		if len(paths) != 1 {
			t.Fatalf("paths after the scope began = %v, want the trusted jar", paths)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provider still waiting after the workspace scope began")
	}
	// Once a scope exists there is no waiting: another workspace gets nothing.
	if _, err := fixture.manager.beginWorkspaceScope(context.Background(), "ws-b", false); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 || time.Since(began) > time.Second {
		t.Fatalf("other workspace: paths %v after %s", paths, time.Since(began))
	}
}

func TestJdtlsBundlesScopeWaitIsBoundedAndOnlyForWorkspaceScopedBundles(t *testing.T) {
	fixture := workspaceScopedTrustedBundle(t)
	setJdtlsBundleScopeWait(t, 50*time.Millisecond)
	began := time.Now()
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("paths without a workspace scope = %v", paths)
	}
	if waited := time.Since(began); waited < 50*time.Millisecond {
		t.Fatalf("waited only %s", waited)
	}
	// Globally scoped (the default) or disabled: no waiting at all.
	setJdtlsBundleScopeWait(t, 10*time.Second)
	for _, check := range []struct {
		name   string
		change func()
		want   int
	}{
		{"global", func() { fixture.record.scope = pluginScopeGlobal }, 1},
		{"disabled", func() { fixture.record.scope, fixture.record.enabled = pluginScopeWorkspace, false }, 0},
	} {
		fixture.record.mu.Lock()
		check.change()
		fixture.record.mu.Unlock()
		began = time.Now()
		paths := fixture.manager.jdtlsBundlePaths()
		if time.Since(began) > time.Second || len(paths) != check.want {
			t.Fatalf("%s: paths %v after %s", check.name, paths, time.Since(began))
		}
	}
	// A host shutting down does not wait either.
	fixture.record.mu.Lock()
	fixture.record.enabled = true
	fixture.record.mu.Unlock()
	fixture.manager.closeWorkspaceScope()
	began = time.Now()
	_ = fixture.manager.jdtlsBundlePaths()
	if time.Since(began) > time.Second {
		t.Fatalf("closed host: waited %s", time.Since(began))
	}
}

// Closing the scope system (host shutdown) wakes a provider that is waiting
// for the first workspace scope instead of holding it for the full wait.
func TestJdtlsBundlesScopeWaitWakesOnClose(t *testing.T) {
	fixture := workspaceScopedTrustedBundle(t)
	setJdtlsBundleScopeWait(t, 10*time.Second)
	result := make(chan []string, 1)
	go func() { result <- fixture.manager.jdtlsBundlePaths() }()
	select {
	case paths := <-result:
		t.Fatalf("provider answered %v before any workspace scope", paths)
	case <-time.After(100 * time.Millisecond):
	}
	fixture.manager.closeWorkspaceScope()
	select {
	case <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("provider still waiting after the scope system closed")
	}
}
