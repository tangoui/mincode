package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"mincode/app/plugins/api"
)

const jdtlsBundleTestPlugin = "jdtls.fixture"

var jdtlsBundleTestCommands = []string{"vscode.java.startDebugSession", "vscode.java.resolveClasspath"}

func writeJdtlsBundleJar(t *testing.T, root, relative, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func jdtlsBundleTestContext(t *testing.T) *pointContext {
	t.Helper()
	root := t.TempDir()
	writeJdtlsBundleJar(t, root, "lib/debug.jar", "jar-v1")
	return &pointContext{
		root:     root,
		manifest: &Manifest{ID: jdtlsBundleTestPlugin, APIVersion: 2, Capabilities: []api.Capability{api.CapabilityShell}},
		spec:     jdtlsBundleSpec,
	}
}

func decodeJdtlsBundleForTest(ctx *pointContext, raw string) (JdtlsBundleContribution, error) {
	value, _, err := decodeJdtlsBundleEntry(ctx, 0, "", json.RawMessage(raw))
	if err != nil {
		return JdtlsBundleContribution{}, err
	}
	return value.(JdtlsBundleContribution), nil
}

func TestJdtlsBundlesDecodeValid(t *testing.T) {
	ctx := jdtlsBundleTestContext(t)
	bundle, err := decodeJdtlsBundleForTest(ctx, `{"path":" ${pluginRoot}/lib/debug.jar ","commands":["vscode.java.startDebugSession","vscode.java.resolve-Main_1"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Path != "${pluginRoot}/lib/debug.jar" || !slices.Equal(bundle.Commands, []string{"vscode.java.startDebugSession", "vscode.java.resolve-Main_1"}) {
		t.Fatalf("bundle = %+v", bundle)
	}
	cases := map[string]string{
		`{"path":"${pluginRoot}/lib/debug.jar","commands":["java.reloadBundles"]}`:               "reserved java.*",
		`{"path":"${pluginRoot}/lib/debug.jar","commands":["java.project.getAll"]}`:              "reserved java.*",
		`{"path":"${pluginRoot}/lib/debug.jar","commands":[]}`:                                   "commands",
		`{"path":"${pluginRoot}/lib/debug.jar"}`:                                                 "commands",
		`{"path":"${pluginRoot}/lib/debug.jar","commands":["a b"]}`:                              "must match",
		`{"path":"${pluginRoot}/lib/debug.jar","commands":["` + strings.Repeat("a", 129) + `"]}`: "must match",
		`{"path":"${pluginRoot}/lib/debug.jar","commands":["x","x"]}`:                            "more than once",
		`{"path":"${pluginRoot}/lib/debug.jar","commands":["x"],"extra":1}`:                      "extra",
	}
	for raw, want := range cases {
		if _, err := decodeJdtlsBundleForTest(ctx, raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %q", raw, err, want)
		}
	}
	tooMany := make([]string, jdtlsBundleCommandsMax+1)
	for index := range tooMany {
		tooMany[index] = "cmd" + string(rune('a'+index%26)) + strings.Repeat("x", index)
	}
	encoded, _ := json.Marshal(map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": tooMany})
	if _, err := decodeJdtlsBundleForTest(ctx, string(encoded)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("too many commands: err = %v", err)
	}
}

func TestJdtlsBundlesRejectOutsidePackage(t *testing.T) {
	ctx := jdtlsBundleTestContext(t)
	outside := writeJdtlsBundleJar(t, t.TempDir(), "evil.jar", "evil")
	writeJdtlsBundleJar(t, ctx.root, "lib/notes.txt", "text")
	if err := os.MkdirAll(filepath.Join(ctx.root, "lib", "dir.jar"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		outside:                            "${pluginRoot}/",
		"lib/debug.jar":                    "${pluginRoot}/",
		"${pluginRoot}/../evil.jar":        ".. segments",
		"${pluginRoot}/lib/../debug.jar":   ".. segments",
		"${pluginRoot}/lib/notes.txt":      "must name a .jar file",
		"${pluginRoot}/.jar":               "must name a .jar file",
		"${pluginRoot}/lib/missing.jar":    "does not exist",
		"${pluginRoot}/lib/dir.jar":        "regular file",
		"${pluginRoot}/${projectRoot}.jar": "variable",
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(ctx.root, "lib", "link.jar")); err != nil {
			t.Fatal(err)
		}
		cases["${pluginRoot}/lib/link.jar"] = "symlink"
	}
	for path, want := range cases {
		encoded, _ := json.Marshal(map[string]any{"path": path, "commands": []string{"x"}})
		if _, err := decodeJdtlsBundleForTest(ctx, string(encoded)); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %q", path, err, want)
		}
	}
}

func writeJdtlsBundleManifest(t *testing.T, id string, capabilities []string, bundles []any) string {
	t.Helper()
	root := t.TempDir()
	writeJdtlsBundleJar(t, root, "bin/plugin.mjs", "\n")
	writeJdtlsBundleJar(t, root, "lib/debug.jar", "jar-v1")
	writeJdtlsBundleJar(t, root, "lib/extra.jar", "extra-v1")
	if bundles == nil {
		bundles = []any{map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": jdtlsBundleTestCommands}}
	}
	manifest := map[string]any{
		"id": id, "name": "Jdtls fixture", "version": "1.0.0", "apiVersion": 2,
		"category": "development", "capabilities": capabilities,
		"runtime":     map[string]any{"type": "process", "entry": "bin/plugin.mjs"},
		"contributes": map[string]any{"jdtlsBundles": bundles},
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestJdtlsBundlesRequireShell(t *testing.T) {
	useTemporaryPluginData(t)
	if _, err := inspectInstallDirectory(writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"ui"}, nil)); err == nil || !strings.Contains(err.Error(), "requires capability shell") {
		t.Fatalf("without shell: err = %v", err)
	}
	preview, err := inspectInstallDirectory(writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if resolved := preview.Manifest.contribution(jdtlsBundlePointKey); resolved == nil || len(resolved.Entries) != 1 {
		t.Fatalf("jdtlsBundles = %+v", resolved)
	}
	// A command repeated across the plugin's own entries skips the later entry.
	duplicate := []any{
		map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{"a", "b"}},
		map[string]any{"path": "${pluginRoot}/lib/extra.jar", "commands": []string{"c", "b"}},
	}
	if _, err := inspectInstallDirectory(writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, duplicate)); err == nil || !strings.Contains(err.Error(), `command "b"`) {
		t.Fatalf("duplicate command within plugin: err = %v", err)
	}
	sameJar := []any{
		map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{"a"}},
		map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{"b"}},
	}
	if _, err := inspectInstallDirectory(writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, sameJar)); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate jar: err = %v", err)
	}
}

type jdtlsBundleFixture struct {
	manager  *pluginManager
	record   *pluginRecord
	root     string
	project  string
	session  []string
	running  bool
	notified atomic.Int32
}

func addJdtlsBundleRecord(t *testing.T, manager *pluginManager, id string, bundles []any) *pluginRecord {
	t.Helper()
	root := writeJdtlsBundleManifest(t, id, []string{"shell"}, bundles)
	preview, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	parsed := preview.Manifest
	record := &pluginRecord{
		id: id, root: root, source: pluginSourceLocal, manifest: &parsed,
		enabled: true, state: pluginStateRunning, capabilities: append([]api.Capability(nil), parsed.Capabilities...),
	}
	manager.mu.Lock()
	if manager.records == nil {
		manager.records = map[string]*pluginRecord{}
	}
	manager.records[id] = record
	manager.order = append(manager.order, id)
	manager.mu.Unlock()
	return record
}

func newJdtlsBundleFixture(t *testing.T) *jdtlsBundleFixture {
	t.Helper()
	useTemporaryPluginData(t)
	fixture := &jdtlsBundleFixture{manager: &pluginManager{}, project: t.TempDir()}
	fixture.record = addJdtlsBundleRecord(t, fixture.manager, jdtlsBundleTestPlugin, nil)
	fixture.root = fixture.record.root
	previousSession, previousReady, previousNotify := jdtlsSessionBundles, jdtlsReady, notifyJdtlsRestartRequired
	jdtlsSessionBundles = func(string) ([]string, bool) { return slices.Clone(fixture.session), fixture.running }
	jdtlsReady = func(string) (bool, bool) { return fixture.running, fixture.running }
	notifyJdtlsRestartRequired = func() { fixture.notified.Add(1) }
	t.Cleanup(func() {
		jdtlsSessionBundles, jdtlsReady, notifyJdtlsRestartRequired = previousSession, previousReady, previousNotify
	})
	return fixture
}

func (fixture *jdtlsBundleFixture) status(t *testing.T) JdtlsBundlesStatus {
	t.Helper()
	status, err := fixture.manager.jdtlsBundlesStatus(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func (fixture *jdtlsBundleFixture) bundle(t *testing.T, pluginID string) JdtlsBundleStatus {
	t.Helper()
	for _, bundle := range fixture.status(t).Bundles {
		if bundle.PluginID == pluginID {
			return bundle
		}
	}
	t.Fatalf("no bundle status for %s", pluginID)
	return JdtlsBundleStatus{}
}

func (fixture *jdtlsBundleFixture) trust(t *testing.T, pluginID string) JdtlsBundleStatus {
	t.Helper()
	bundle := fixture.bundle(t, pluginID)
	trusted, err := fixture.manager.trustJdtlsBundles(pluginID, bundle.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return trusted
}

func TestUntrustedBundleNotProvided(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("untrusted paths = %v", paths)
	}
	bundle := fixture.bundle(t, jdtlsBundleTestPlugin)
	if bundle.Trusted || bundle.PreviouslyTrusted || bundle.Loaded || len(bundle.Fingerprint) != 64 ||
		bundle.PluginName != "Jdtls fixture" || bundle.PluginVersion != "1.0.0" ||
		len(bundle.Jars) != 1 || bundle.Jars[0].Name != "debug.jar" || bundle.Jars[0].Bytes != int64(len("jar-v1")) || len(bundle.Jars[0].SHA256) != 64 {
		t.Fatalf("bundle = %+v", bundle)
	}
	if _, err := fixture.manager.trustJdtlsBundles(jdtlsBundleTestPlugin, strings.Repeat("0", 64)); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("trust with a wrong fingerprint: %v", err)
	}
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("paths after rejected trust = %v", paths)
	}
}

func TestTrustedBundleProvided(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	trusted := fixture.trust(t, jdtlsBundleTestPlugin)
	if !trusted.Trusted || trusted.PreviouslyTrusted {
		t.Fatalf("trusted = %+v", trusted)
	}
	paths := fixture.manager.jdtlsBundlePaths()
	want, err := resolvePluginPath(fixture.root, "lib/debug.jar")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{want}) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	// Not running: nothing to restart. Running with the trusted set: loaded.
	if status := fixture.status(t); status.RestartRequired || status.Jdtls.Running {
		t.Fatalf("status without jdtls = %+v", status)
	}
	fixture.running, fixture.session = true, paths
	status := fixture.status(t)
	if status.RestartRequired || !status.Jdtls.Running || !status.Jdtls.Ready || len(status.Bundles) != 1 || !status.Bundles[0].Loaded {
		t.Fatalf("status with jdtls = %+v", status)
	}
	// Disabled or not shell-granted plugins provide nothing.
	fixture.record.mu.Lock()
	fixture.record.capabilities = nil
	fixture.record.mu.Unlock()
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("paths without shell = %v", paths)
	}
}

func TestBundleNotLoadedAfterJarChanged(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	fixture.trust(t, jdtlsBundleTestPlugin)
	if len(fixture.manager.jdtlsBundlePaths()) != 1 {
		t.Fatal("trusted bundle not provided")
	}
	jar := filepath.Join(fixture.root, "lib", "debug.jar")
	info, err := os.Stat(jar)
	if err != nil {
		t.Fatal(err)
	}
	// Same size and modification time: the hash cache must not hide the change.
	if err := os.WriteFile(jar, []byte("jar-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(jar, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("changed jar provided: %v", paths)
	}
	bundle := fixture.bundle(t, jdtlsBundleTestPlugin)
	if bundle.Trusted || !bundle.PreviouslyTrusted {
		t.Fatalf("bundle after change = %+v", bundle)
	}
	// Re-trusting the new fingerprint loads it again.
	fixture.trust(t, jdtlsBundleTestPlugin)
	if len(fixture.manager.jdtlsBundlePaths()) != 1 {
		t.Fatal("re-trusted bundle not provided")
	}
}

func TestBundleSetChangeEmitsRestartRequired(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	fixture.manager.checkJdtlsBundleSet()
	fixture.notified.Store(0)
	fixture.trust(t, jdtlsBundleTestPlugin)
	if fixture.notified.Load() != 1 {
		t.Fatalf("trust notified %d times", fixture.notified.Load())
	}
	fixture.running, fixture.session = true, fixture.manager.jdtlsBundlePaths()
	fixture.notified.Store(0)
	// An unrelated change does not announce anything.
	fixture.manager.emitContributionsChanged(jdtlsBundleTestPlugin, nil)
	if fixture.notified.Load() != 0 {
		t.Fatalf("unchanged set notified %d times", fixture.notified.Load())
	}
	if err := fixture.manager.SetEnabled(context.Background(), jdtlsBundleTestPlugin, false); err != nil {
		t.Fatal(err)
	}
	if fixture.notified.Load() != 1 {
		t.Fatalf("disable notified %d times", fixture.notified.Load())
	}
	status := fixture.status(t)
	if !status.RestartRequired || len(status.Bundles) != 0 {
		t.Fatalf("status after disable = %+v", status)
	}
	// Enabling again (through the contributions-changed signal) announces it.
	fixture.record.mu.Lock()
	fixture.record.enabled = true
	fixture.record.mu.Unlock()
	fixture.manager.emitContributionsChanged(jdtlsBundleTestPlugin, nil)
	if fixture.notified.Load() != 2 {
		t.Fatalf("enable notified %d times", fixture.notified.Load())
	}
	if status := fixture.status(t); status.RestartRequired {
		t.Fatalf("status after enable = %+v", status)
	}
}

// Stopping the plugin process (explicit or idle stop, which both run
// stopRecordLocked and emitState) changes nothing for jdtls.
func TestBundleSetStableAcrossPluginStop(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	fixture.trust(t, jdtlsBundleTestPlugin)
	want := fixture.manager.jdtlsBundlePaths()
	fixture.running, fixture.session = true, want
	fixture.notified.Store(0)
	if err := fixture.manager.stopRecord(context.Background(), fixture.record, false); err != nil {
		t.Fatal(err)
	}
	fixture.record.mu.Lock()
	state := fixture.record.state
	fixture.record.mu.Unlock()
	if state != pluginStateStopped {
		t.Fatalf("state = %s", state)
	}
	if fixture.notified.Load() != 0 {
		t.Fatalf("stop notified %d times", fixture.notified.Load())
	}
	if paths := fixture.manager.jdtlsBundlePaths(); !slices.Equal(paths, want) {
		t.Fatalf("paths after stop = %v, want %v", paths, want)
	}
	// While stopping, the set is the same as well.
	fixture.record.mu.Lock()
	fixture.record.state = pluginStateStopping
	fixture.record.mu.Unlock()
	if paths := fixture.manager.jdtlsBundlePaths(); !slices.Equal(paths, want) {
		t.Fatalf("paths while stopping = %v, want %v", paths, want)
	}
	fixture.manager.emitContributionsChanged(jdtlsBundleTestPlugin, nil)
	if status := fixture.status(t); fixture.notified.Load() != 0 || status.RestartRequired || !status.Bundles[0].Loaded {
		t.Fatalf("stopping: notified %d, status %+v", fixture.notified.Load(), status)
	}
	// A failed plugin is no longer loadable.
	fixture.record.mu.Lock()
	fixture.record.state = pluginStateError
	fixture.record.mu.Unlock()
	fixture.manager.emitState(fixture.record)
	if fixture.notified.Load() != 1 || len(fixture.manager.jdtlsBundlePaths()) != 0 {
		t.Fatalf("error state: notified %d", fixture.notified.Load())
	}
}

// Widening the declared command list needs a new confirmation.
func TestBundleCommandsInFingerprint(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	before := fixture.trust(t, jdtlsBundleTestPlugin)
	wider := []any{map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": append(slices.Clone(jdtlsBundleTestCommands), "vscode.java.extra")}}
	replacement := writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, wider)
	preview, err := inspectInstallDirectory(replacement)
	if err != nil {
		t.Fatal(err)
	}
	fixture.record.mu.Lock()
	fixture.record.root, fixture.record.manifest = replacement, &preview.Manifest
	fixture.record.mu.Unlock()
	after := fixture.bundle(t, jdtlsBundleTestPlugin)
	if after.Fingerprint == before.Fingerprint || after.Trusted || !after.PreviouslyTrusted || after.Jars[0].SHA256 != before.Jars[0].SHA256 {
		t.Fatalf("before %+v, after %+v", before, after)
	}
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("widened commands loaded without confirmation: %v", paths)
	}
	// Order of the declared commands does not matter.
	reordered := []any{map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{jdtlsBundleTestCommands[1], jdtlsBundleTestCommands[0]}}}
	replacement = writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, reordered)
	if preview, err = inspectInstallDirectory(replacement); err != nil {
		t.Fatal(err)
	}
	fixture.record.mu.Lock()
	fixture.record.root, fixture.record.manifest = replacement, &preview.Manifest
	fixture.record.mu.Unlock()
	if again := fixture.bundle(t, jdtlsBundleTestPlugin); again.Fingerprint != before.Fingerprint || !again.Trusted {
		t.Fatalf("reordered commands: %+v", again)
	}
}

// Command ownership follows the persisted install order, not the directory
// order a restarted manager loads plugins in.
func TestJdtlsBundleOwnershipSurvivesRestart(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	shared := []any{map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": jdtlsBundleTestCommands}}
	first := addJdtlsBundleRecord(t, fixture.manager, "zeta.first", shared)
	fixture.manager.mu.Lock()
	fixture.manager.records = map[string]*pluginRecord{first.id: first}
	fixture.manager.order = []string{first.id}
	fixture.manager.mu.Unlock()
	fixture.manager.checkJdtlsBundleSet() // first seen alone
	second := addJdtlsBundleRecord(t, fixture.manager, "alpha.second", shared)
	fixture.trust(t, first.id)
	if owner := fixture.manager.jdtlsCommandOwner(jdtlsBundleTestCommands[0]); owner != first.id {
		t.Fatalf("owner = %q", owner)
	}
	// Restart: a new manager loads the plugins in directory (ID) order.
	restarted := &pluginManager{
		records: map[string]*pluginRecord{first.id: first, second.id: second},
		order:   []string{second.id, first.id},
	}
	if owner := restarted.jdtlsCommandOwner(jdtlsBundleTestCommands[0]); owner != first.id {
		t.Fatalf("owner after restart = %q", owner)
	}
	statuses := restarted.jdtlsBundleStatuses(true, false)
	if len(statuses) != 1 || statuses[0].PluginID != first.id || !statuses[0].Trusted {
		t.Fatalf("statuses after restart = %+v", statuses)
	}
	// Uninstalling the owner hands the commands to the next plugin; a
	// reinstall of the old owner then comes after it.
	restarted.mu.Lock()
	delete(restarted.records, first.id)
	restarted.order = []string{second.id}
	restarted.mu.Unlock()
	restarted.runRecordRemovalHooks(first.id, removalReasonUninstall)
	restarted.mu.Lock()
	restarted.records[first.id] = first
	restarted.order = []string{first.id, second.id}
	restarted.mu.Unlock()
	if owner := resolveJdtlsBundleOwnership(restarted.jdtlsBundleDeclarations(nil)).owners[jdtlsBundleTestCommands[0]]; owner != second.id {
		t.Fatalf("owner after reinstall = %q", owner)
	}
}

func TestUninstallDeletesBundleTrust(t *testing.T) {
	if !slices.Contains(processLaunchTrustPoints, jdtlsBundlePointKey) {
		t.Fatalf("processLaunchTrustPoints = %v", processLaunchTrustPoints)
	}
	for _, reason := range []removalReason{removalReasonUninstall, removalReasonClearData} {
		fixture := newJdtlsBundleFixture(t)
		fixture.trust(t, jdtlsBundleTestPlugin)
		fixture.manager.runRecordRemovalHooks(jdtlsBundleTestPlugin, reason)
		store, err := readProcessLaunchTrust(jdtlsBundleTestPlugin, jdtlsBundlePointKey)
		if err != nil || len(store.Entries) != 0 {
			t.Fatalf("%s: trust = %+v, %v", reason, store, err)
		}
		if bundle := fixture.bundle(t, jdtlsBundleTestPlugin); bundle.Trusted || bundle.PreviouslyTrusted {
			t.Fatalf("%s: bundle = %+v", reason, bundle)
		}
	}
}

func TestJdtlsBundlesRejectDuplicateCommandAcrossPlugins(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	fixture.trust(t, jdtlsBundleTestPlugin)
	later := []any{
		map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{"later.only", "vscode.java.resolveClasspath"}},
		map[string]any{"path": "${pluginRoot}/lib/extra.jar", "commands": []string{"later.extra"}},
	}
	// The install preview of a later plugin reports the conflicting entry.
	preview, err := fixture.manager.PreviewInstall(writeJdtlsBundleManifest(t, "jdtls.later", []string{"shell"}, later))
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.ContributionIssues) != 1 || preview.ContributionIssues[0].Point != jdtlsBundlePointKey ||
		preview.ContributionIssues[0].Entry != 0 || !strings.Contains(preview.ContributionIssues[0].Message, "vscode.java.resolveClasspath") ||
		!strings.Contains(preview.ContributionIssues[0].Message, jdtlsBundleTestPlugin) {
		t.Fatalf("preview issues = %+v", preview.ContributionIssues)
	}
	// An update of the owner itself is not a conflict.
	own, err := fixture.manager.PreviewInstall(writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, nil))
	if err != nil || len(own.ContributionIssues) != 0 {
		t.Fatalf("own update issues = %+v, %v", own.ContributionIssues, err)
	}
	// Installed, the later plugin keeps only its conflict-free entry.
	laterRecord := addJdtlsBundleRecord(t, fixture.manager, "jdtls.later", later)
	bundle := fixture.bundle(t, "jdtls.later")
	if len(bundle.Jars) != 1 || bundle.Jars[0].Name != "extra.jar" {
		t.Fatalf("later bundle = %+v", bundle)
	}
	if owner := fixture.manager.jdtlsCommandOwner("vscode.java.resolveClasspath"); owner != jdtlsBundleTestPlugin {
		t.Fatalf("owner = %q", owner)
	}
	if owner := fixture.manager.jdtlsCommandOwner("later.only"); owner != "" {
		t.Fatalf("command of a rejected entry has owner %q", owner)
	}
	// A command routes only to a confirmed owner.
	if owner := fixture.manager.jdtlsCommandOwner("later.extra"); owner != "" {
		t.Fatalf("later.extra owner before trust = %q", owner)
	}
	fixture.trust(t, "jdtls.later")
	if owner := fixture.manager.jdtlsCommandOwner("later.extra"); owner != "jdtls.later" {
		t.Fatalf("later.extra owner = %q", owner)
	}
	laterExtra, err := resolvePluginPath(laterRecord.root, "lib/extra.jar")
	if err != nil {
		t.Fatal(err)
	}
	ownerJar, err := resolvePluginPath(fixture.root, "lib/debug.jar")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{laterExtra, ownerJar}
	slices.Sort(want)
	if paths := fixture.manager.jdtlsBundlePaths(); !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestJdtlsBundleCancelledUpgradePreviewDoesNotPersistOrder(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	// Installed plugin has never declared bundles (no install sequence).
	fixture.record.mu.Lock()
	delete(fixture.record.manifest.contributions, jdtlsBundlePointKey)
	fixture.record.mu.Unlock()
	before, foundBefore, err := pluginKVGetRaw(hostPluginID, jdtlsBundleOrderKey)
	if err != nil {
		t.Fatal(err)
	}
	previewRoot := writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, nil)
	for i := 0; i < 2; i++ {
		if _, err := fixture.manager.PreviewInstall(previewRoot); err != nil {
			t.Fatal(err)
		}
	}
	after, foundAfter, err := pluginKVGetRaw(hostPluginID, jdtlsBundleOrderKey)
	if err != nil || foundBefore != foundAfter || string(before) != string(after) {
		t.Fatalf("cancelled preview changed KV: before=%s/%v after=%s/%v err=%v", before, foundBefore, after, foundAfter, err)
	}
	// Another plugin is actually installed before the previewed upgrade.
	other := addJdtlsBundleRecord(t, fixture.manager, "later.actual", nil)
	fixture.manager.jdtlsBundleDeclarations(nil)
	preview, err := inspectInstallDirectory(previewRoot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.record.mu.Lock()
	fixture.record.manifest = &preview.Manifest
	fixture.record.mu.Unlock()
	fixture.manager.jdtlsBundleDeclarations(nil)
	store, _ := readJdtlsBundleOrder()
	if store.Plugins[other.id] <= 0 || store.Plugins[jdtlsBundleTestPlugin] <= store.Plugins[other.id] {
		t.Fatalf("real upgrade did not receive fresh sequence: %+v", store)
	}
}

func TestJdtlsBundleUnseenPreviewSortsAfterUnseenInstalledPlugins(t *testing.T) {
	fixture := newJdtlsBundleFixture(t)
	fixture.record.mu.Lock()
	delete(fixture.record.manifest.contributions, jdtlsBundlePointKey)
	fixture.record.mu.Unlock()
	addJdtlsBundleRecord(t, fixture.manager, "zeta.installed", nil)
	preview, err := fixture.manager.PreviewInstall(writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.ContributionIssues) != 1 || !strings.Contains(preview.ContributionIssues[0].Message, "zeta.installed") {
		t.Fatalf("unseen preview did not sort last: %+v", preview.ContributionIssues)
	}
	if _, found, err := pluginKVGetRaw(hostPluginID, jdtlsBundleOrderKey); err != nil || found {
		t.Fatalf("preview persisted order: found=%v err=%v", found, err)
	}
}
