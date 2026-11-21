package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mincode/app/backend/terminal"
	"mincode/app/plugins/api"
)

const taskTestPlugin = "tasks.fixture"

func taskTestContext(t *testing.T) *pointContext {
	t.Helper()
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/run")
	return &pointContext{
		root:     root,
		manifest: &Manifest{ID: taskTestPlugin, APIVersion: 2, Capabilities: []api.Capability{api.CapabilityShell}},
		spec:     taskSpec,
		actions: map[string]ManifestUIAction{
			"detect":  {ID: "detect", Risk: "read"},
			"write":   {ID: "write", Risk: "write"},
			"aiRead":  {ID: "aiRead", Risk: "read", AICallable: true},
			"missing": {},
		},
	}
}

func decodeTaskForTest(ctx *pointContext, raw string) (TaskContribution, error) {
	value, _, err := decodeTaskEntry(ctx, 0, "", json.RawMessage(raw))
	if err != nil {
		return TaskContribution{}, err
	}
	return value.(TaskContribution), nil
}

func TestTaskDecodeStaticAndProvider(t *testing.T) {
	ctx := taskTestContext(t)
	task, err := decodeTaskForTest(ctx, `{"id":"build","kind":"static","title":"Build","titleLocalized":{"zh":"构建"},"group":"build","command":"${pluginRoot}/bin/run","args":["--root","${projectRoot}"],"env":{"MODE":"${pluginRoot}"},"cwd":"${projectRoot}/sub","when":{"files":["package.json","src/main.go"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if task.Group != "build" || task.Cwd != "${projectRoot}/sub" || len(task.WhenFiles) != 2 || len(task.Platforms) != 3 || len(task.SpecFingerprint) != 64 {
		t.Fatalf("static = %+v", task)
	}
	defaults, err := decodeTaskForTest(ctx, `{"id":"lint","kind":"static","title":"Lint","command":"npm","args":["run","lint"]}`)
	if err != nil || defaults.Group != taskDefaultGroup || defaults.Cwd != taskVarProjectRoot {
		t.Fatalf("defaults = %+v, %v", defaults, err)
	}
	provider, err := decodeTaskForTest(ctx, `{"id":"npm","kind":"provider","title":"npm scripts","action":"detect","platforms":["darwin","linux"]}`)
	if err != nil || provider.Action != "detect" || len(provider.Platforms) != 2 {
		t.Fatalf("provider = %+v, %v", provider, err)
	}
}

func TestTaskDecodeRejects(t *testing.T) {
	ctx := taskTestContext(t)
	cases := map[string]string{
		"bad id":             `{"id":"Build","kind":"static","title":"B","command":"npm"}`,
		"bad kind":           `{"id":"b","kind":"shell","title":"B","command":"npm"}`,
		"absolute command":   `{"id":"b","kind":"static","title":"B","command":"/bin/sh"}`,
		"relative command":   `{"id":"b","kind":"static","title":"B","command":"./run"}`,
		"workspace variable": `{"id":"b","kind":"static","title":"B","command":"npm","args":["${workspaceFolder}"]}`,
		"private variable":   `{"id":"b","kind":"static","title":"B","command":"npm","args":["${taskCwd}"]}`,
		"missing executable": `{"id":"b","kind":"static","title":"B","command":"${pluginRoot}/bin/none"}`,
		"escaping command":   `{"id":"b","kind":"static","title":"B","command":"${pluginRoot}/../x"}`,
		"denied env":         `{"id":"b","kind":"static","title":"B","command":"npm","env":{"LD_PRELOAD":"x"}}`,
		"cwd escape":         `{"id":"b","kind":"static","title":"B","command":"npm","cwd":"${projectRoot}/../x"}`,
		"cwd plugin root":    `{"id":"b","kind":"static","title":"B","command":"npm","cwd":"${pluginRoot}"}`,
		"control arg":        `{"id":"b","kind":"static","title":"B","command":"npm","args":["a\u0007"]}`,
		"bidi arg":           `{"id":"b","kind":"static","title":"B","command":"npm","args":["a‮"]}`,
		"when glob":          `{"id":"b","kind":"static","title":"B","command":"npm","when":{"files":["*.json"]}}`,
		"when dotdot":        `{"id":"b","kind":"static","title":"B","command":"npm","when":{"files":["../x"]}}`,
		"when absolute":      `{"id":"b","kind":"static","title":"B","command":"npm","when":{"files":["/etc/passwd"]}}`,
		"when empty":         `{"id":"b","kind":"static","title":"B","command":"npm","when":{"files":[]}}`,
		"static action":      `{"id":"b","kind":"static","title":"B","command":"npm","action":"detect"}`,
		"provider command":   `{"id":"p","kind":"provider","title":"P","action":"detect","command":"npm"}`,
		"provider write":     `{"id":"p","kind":"provider","title":"P","action":"write"}`,
		"provider ai":        `{"id":"p","kind":"provider","title":"P","action":"aiRead"}`,
		"provider unknown":   `{"id":"p","kind":"provider","title":"P","action":"nope"}`,
		"bad group":          `{"id":"b","kind":"static","title":"B","command":"npm","group":"publish"}`,
		"bad platform":       `{"id":"b","kind":"static","title":"B","command":"npm","platforms":["plan9"]}`,
		"unknown field":      `{"id":"b","kind":"static","title":"B","command":"npm","shell":true}`,
	}
	for name, raw := range cases {
		if _, err := decodeTaskForTest(ctx, raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestTaskKeyRoundTrip(t *testing.T) {
	if source, provider, id, err := parseTaskKey(staticTaskKey("build")); err != nil || source != taskKindStatic || provider != "" || id != "build" {
		t.Fatalf("static = %s %s %s %v", source, provider, id, err)
	}
	if source, provider, id, err := parseTaskKey(detectedTaskKey("npm", "script:test")); err != nil || source != "detected" || provider != "npm" || id != "script:test" {
		t.Fatalf("detected = %s %s %s %v", source, provider, id, err)
	}
	for _, key := range []string{"", "build", "static:Bad", "detected:npm", "detected:NPM:x", "detected:npm:/x"} {
		if _, _, _, err := parseTaskKey(key); err == nil {
			t.Errorf("%q must be rejected", key)
		}
	}
}

func TestParseDetectedTasksDropsInvalid(t *testing.T) {
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/run")
	raw := json.RawMessage(`{"tasks":[
		{"id":"script:test","title":"test","command":"npm","args":["test"]},
		{"id":"script:test","title":"duplicate","command":"npm"},
		{"id":"abs","title":"abs","command":"/bin/sh"},
		{"id":"env","title":"env","command":"npm","env":{"DYLD_INSERT_LIBRARIES":"x"}},
		{"id":"cwd","title":"cwd","command":"npm","cwd":"${projectRoot}/../up"},
		{"id":"extra","title":"extra","command":"npm","when":{"files":["x"]}},
		{"id":"script:build","title":"build","command":"${pluginRoot}/bin/run","cwd":"${projectRoot}/web","group":"build"}
	]}`)
	tasks, dropped, err := parseDetectedTasks(root, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || dropped != 5 || tasks[0].ID != "script:build" || tasks[0].Group != "build" || tasks[1].Group != taskDefaultGroup {
		t.Fatalf("tasks = %+v dropped = %d", tasks, dropped)
	}
	for name, envelope := range map[string]string{
		"not object": `[]`,
		"no tasks":   `{}`,
		"extra":      `{"tasks":[],"more":1}`,
		"too many":   `{"tasks":[` + strings.TrimSuffix(strings.Repeat(`{"id":"a","title":"a","command":"npm"},`, taskDetectedMax+1), ",") + `]}`,
	} {
		if _, _, err := parseDetectedTasks(root, json.RawMessage(envelope)); contributionErrorCode(err) != contributionErrInvalidResult {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestTaskDetectCacheEvictsAndPurges(t *testing.T) {
	t.Cleanup(func() { purgeTaskDetectCache("cache.plugin") })
	for index := 0; index <= taskDetectCacheRootMax; index++ {
		taskDetectStore("cache.plugin", "/root/"+string(rune('a'+index%26))+strings.Repeat("x", index), "npm", taskDetectState{generation: "g"})
	}
	taskDetectCache.mu.Lock()
	count := len(taskDetectCache.plugins["cache.plugin"])
	taskDetectCache.mu.Unlock()
	if count != taskDetectCacheRootMax {
		t.Fatalf("roots = %d", count)
	}
	if _, ok := taskDetectLookup("cache.plugin", "/root/a", "npm", "g"); ok {
		t.Fatal("the least recently used root must be evicted")
	}
	last := "/root/" + string(rune('a'+taskDetectCacheRootMax%26)) + strings.Repeat("x", taskDetectCacheRootMax)
	if _, ok := taskDetectLookup("cache.plugin", last, "npm", "other"); ok {
		t.Fatal("another generation must miss")
	}
	if _, ok := taskDetectLookup("cache.plugin", last, "npm", "g"); !ok {
		t.Fatal("the newest root must be kept")
	}
	purgeTaskDetectCache("cache.plugin")
	if _, ok := taskDetectLookup("cache.plugin", last, "npm", "g"); ok {
		t.Fatal("purge must drop the plugin")
	}
}

type taskLaunchFixture struct {
	manager *pluginManager
	record  *pluginRecord
	root    string
	project string
	started []terminal.ProfileShellSpec
	live    map[string]bool
	onExit  map[string]func()
}

func newTaskLaunchFixture(t *testing.T) *taskLaunchFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	useTemporaryPluginData(t)
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/run")
	for path, content := range map[string]string{"bin/plugin.mjs": "\n", "scripts/build.mjs": "console.log(1)\n"} {
		full := filepath.Join(root, filepath.FromSlash(path))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := map[string]any{
		"id": taskTestPlugin, "name": "Task fixture", "version": "1.0.0", "apiVersion": 2,
		"category": "development", "capabilities": []string{"shell", "ui"},
		"runtime": map[string]any{"type": "process", "entry": "bin/plugin.mjs"},
		"contributes": map[string]any{
			"actions": []any{map[string]any{"id": "detect", "risk": "read"}},
			"tasks": []any{
				map[string]any{
					"id": "build", "kind": "static", "title": "Build", "command": "${pluginRoot}/bin/run",
					"args": []string{"${pluginRoot}/scripts/build.mjs", "${projectRoot}"}, "cwd": "${projectRoot}/web",
					"when": map[string]any{"files": []string{"package.json"}},
				},
				map[string]any{"id": "npm", "kind": "provider", "title": "npm", "action": "detect"},
			},
		},
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	preview, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	parsed := preview.Manifest
	record := &pluginRecord{
		id: taskTestPlugin, root: root, source: pluginSourceLocal, manifest: &parsed,
		enabled: true, state: pluginStateRunning, capabilities: append([]api.Capability(nil), parsed.Capabilities...),
	}
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture := &taskLaunchFixture{
		manager: &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}},
		record:  record, root: root, project: project,
		live: map[string]bool{}, onExit: map[string]func(){},
	}
	previousStart, previousAttach, previousPath := taskStartShell, taskAttachShell, terminalProfileLoginPath
	taskStartShell = func(spec terminal.ProfileShellSpec) (string, error) {
		if spec.BeforeExec != nil {
			if err := spec.BeforeExec(); err != nil {
				return "", err
			}
		}
		fixture.started = append(fixture.started, spec)
		fixture.live[spec.SessionID] = true
		fixture.onExit[spec.SessionID] = spec.OnExit
		return "Shell ready (PID: 1)", nil
	}
	taskAttachShell = func(sessionID string) bool { return fixture.live[sessionID] }
	terminalProfileLoginPath = func() string { return "" }
	t.Cleanup(func() {
		taskStartShell, taskAttachShell, terminalProfileLoginPath = previousStart, previousAttach, previousPath
		taskSessionsMu.Lock()
		taskSessions = map[string]processLaunchSession{}
		taskSessionsMu.Unlock()
		purgeTaskDetectCache(taskTestPlugin)
	})
	return fixture
}

func (fixture *taskLaunchFixture) trust(t *testing.T, key, project string) TaskLaunch {
	t.Helper()
	launch, err := fixture.manager.prepareTask(taskPrepareRequest{PluginID: taskTestPlugin, TaskKey: key, ProjectRoot: project})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Trusted {
		t.Fatal("a new task must not be trusted")
	}
	trusted, err := fixture.manager.trustTask(taskTrustRequest{PluginID: taskTestPlugin, TaskKey: key, ProjectRoot: project, Fingerprint: launch.Fingerprint})
	if err != nil || !trusted.Trusted {
		t.Fatalf("trust = %+v, %v", trusted, err)
	}
	return trusted
}

func (fixture *taskLaunchFixture) start(key, project, fingerprint, session string, attach bool) (string, error) {
	return fixture.manager.startTask(taskStartRequest{
		PluginID: taskTestPlugin, TaskKey: key, ProjectRoot: project, Fingerprint: fingerprint, SessionID: session, AttachIfExists: attach,
	})
}

func TestTaskListFiltersByWhenFiles(t *testing.T) {
	fixture := newTaskLaunchFixture(t)
	list, err := fixture.manager.listTasks(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].Key != "static:build" || len(list.Providers) != 1 || list.Providers[0].State != "idle" {
		t.Fatalf("list = %+v", list)
	}
	empty := t.TempDir()
	if list, err := fixture.manager.listTasks(empty); err != nil || len(list.Tasks) != 0 {
		t.Fatalf("project without package.json = %+v, %v", list, err)
	}
	// A when.files symlink leaving the project does not count.
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "package.json"), []byte("{}"), 0o644)
	escaping := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "package.json"), filepath.Join(escaping, "package.json")); err != nil {
		t.Fatal(err)
	}
	if list, err := fixture.manager.listTasks(escaping); err != nil || len(list.Tasks) != 0 {
		t.Fatalf("escaping symlink = %+v, %v", list, err)
	}
}

func TestTaskProjectRootRefusals(t *testing.T) {
	fixture := newTaskLaunchFixture(t)
	for name, root := range map[string]string{"relative": "project", "filesystem root": "/", "data dir": userDataDir(), "missing": filepath.Join(fixture.project, "none")} {
		if _, err := fixture.manager.listTasks(root); err == nil {
			t.Errorf("%s: expected refusal", name)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := canonicalTaskProjectRoot(home); err == nil {
			t.Error("the user home must be refused")
		}
	}
}

func TestTaskStaticLaunchLifecycle(t *testing.T) {
	fixture := newTaskLaunchFixture(t)
	// A restored layout only re-attaches: nothing runs without a live session.
	if _, err := fixture.manager.startTask(taskStartRequest{PluginID: taskTestPlugin, TaskKey: "static:build", ProjectRoot: fixture.project, SessionID: "s0", AttachOnly: true}); processLaunchErrorCode(err) != processLaunchErrNotFound || len(fixture.started) != 0 {
		t.Fatalf("attachOnly without a session err = %v", err)
	}
	if _, err := fixture.start("static:build", fixture.project, "", "s1", false); processLaunchErrorCode(err) != processLaunchErrUntrusted {
		t.Fatalf("unconfirmed start err = %v", err)
	}
	launch := fixture.trust(t, "static:build", fixture.project)
	project, _ := filepath.EvalSymlinks(fixture.project)
	if launch.Cwd != filepath.Join(project, "web") || launch.Argv[len(launch.Argv)-1] != project || len(launch.PluginFiles) != 1 {
		t.Fatalf("launch = %+v", launch)
	}
	if _, err := fixture.start("static:build", fixture.project, launch.Fingerprint, "s1", false); err != nil {
		t.Fatal(err)
	}
	if len(fixture.started) != 1 || fixture.started[0].Cwd != launch.Cwd {
		t.Fatalf("started = %+v", fixture.started)
	}
	// Re-attach only with attachIfExists; never a second process.
	if _, err := fixture.start("static:build", fixture.project, launch.Fingerprint, "s1", false); err == nil {
		t.Fatal("a live session must not be restarted")
	}
	if message, err := fixture.start("static:build", fixture.project, launch.Fingerprint, "s1", true); err != nil || message != "Shell attached" || len(fixture.started) != 1 {
		t.Fatalf("reattach = %q, %v", message, err)
	}
	if message, err := fixture.manager.startTask(taskStartRequest{PluginID: taskTestPlugin, TaskKey: "static:build", ProjectRoot: fixture.project, Fingerprint: launch.Fingerprint, SessionID: "s1", AttachOnly: true}); err != nil || message != "Shell attached" || len(fixture.started) != 1 {
		t.Fatalf("attachOnly = %q, %v", message, err)
	}

	// Trust is per project: another project needs its own confirmation.
	other := t.TempDir()
	_ = os.MkdirAll(filepath.Join(other, "web"), 0o755)
	_ = os.WriteFile(filepath.Join(other, "package.json"), []byte("{}"), 0o644)
	otherLaunch, err := fixture.manager.prepareTask(taskPrepareRequest{PluginID: taskTestPlugin, TaskKey: "static:build", ProjectRoot: other})
	if err != nil || otherLaunch.Trusted || otherLaunch.EntryKey == launch.EntryKey {
		t.Fatalf("other project = %+v, %v", otherLaunch, err)
	}

	// Tampering with a ${pluginRoot} file named by an argument changes the launch.
	if err := os.WriteFile(filepath.Join(fixture.root, "scripts", "build.mjs"), []byte("console.log(2)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := fixture.manager.prepareTask(taskPrepareRequest{PluginID: taskTestPlugin, TaskKey: "static:build", ProjectRoot: fixture.project})
	if err != nil || changed.Trusted || !changed.PreviouslyTrusted || changed.Fingerprint == launch.Fingerprint {
		t.Fatalf("tampered = %+v, %v", changed, err)
	}
	if _, err := fixture.start("static:build", fixture.project, launch.Fingerprint, "s2", false); err == nil {
		t.Fatal("a stale fingerprint must be refused")
	}

	// The task disappears when its when.files no longer exist.
	_ = os.Remove(filepath.Join(fixture.project, "package.json"))
	if _, err := fixture.manager.prepareTask(taskPrepareRequest{PluginID: taskTestPlugin, TaskKey: "static:build", ProjectRoot: fixture.project}); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("missing when.files err = %v", err)
	}
}

func TestTaskCwdSymlinkEscapeRefused(t *testing.T) {
	fixture := newTaskLaunchFixture(t)
	_ = os.RemoveAll(filepath.Join(fixture.project, "web"))
	if err := os.Symlink(t.TempDir(), filepath.Join(fixture.project, "web")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.prepareTask(taskPrepareRequest{PluginID: taskTestPlugin, TaskKey: "static:build", ProjectRoot: fixture.project}); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("escaping cwd err = %v", err)
	}
}

func TestTaskDetectedLaunchRevalidates(t *testing.T) {
	fixture := newTaskLaunchFixture(t)
	project, _ := canonicalTaskProjectRoot(fixture.project)
	key := detectedTaskKey("npm", "script:test")
	if _, err := fixture.manager.prepareTask(taskPrepareRequest{PluginID: taskTestPlugin, TaskKey: key, ProjectRoot: fixture.project}); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("undetected err = %v", err)
	}
	src, err := fixture.manager.processLaunchSource(taskTestPlugin, taskPointKey, "tasks")
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, err := parseDetectedTasks(fixture.root, json.RawMessage(`{"tasks":[{"id":"script:test","title":"test","command":"npm","args":["test"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	taskDetectStore(taskTestPlugin, project, "npm", taskDetectState{generation: taskGeneration(src), tasks: tasks})
	list, err := fixture.manager.listTasks(fixture.project)
	if err != nil || len(list.Tasks) != 2 || list.Providers[0].State != "ready" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	launch, err := fixture.manager.prepareTask(taskPrepareRequest{PluginID: taskTestPlugin, TaskKey: key, ProjectRoot: fixture.project})
	if err != nil || launch.Cwd != project || launch.Title != "test" {
		t.Fatalf("detected launch = %+v, %v", launch, err)
	}
	// A cached entry that no longer passes validation (for example after a
	// contract denylist update) is refused at launch.
	tasks[0].Definition.Env = map[string]string{"LD_PRELOAD": "x"}
	taskDetectStore(taskTestPlugin, project, "npm", taskDetectState{generation: taskGeneration(src), tasks: tasks})
	if _, err := fixture.manager.prepareTask(taskPrepareRequest{PluginID: taskTestPlugin, TaskKey: key, ProjectRoot: fixture.project}); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("revalidation err = %v", err)
	}
	// Removal purges the cache.
	fixture.manager.runRecordRemovalHooks(taskTestPlugin, removalReasonUninstall)
	if _, ok := taskDetectLookup(taskTestPlugin, project, "npm", taskGeneration(src)); ok {
		t.Fatal("removal must purge detected tasks")
	}
}

func TestTaskLaunchGates(t *testing.T) {
	fixture := newTaskLaunchFixture(t)
	fixture.record.mu.Lock()
	fixture.record.capabilities = nil
	fixture.record.mu.Unlock()
	if _, err := fixture.manager.prepareTask(taskPrepareRequest{PluginID: taskTestPlugin, TaskKey: "static:build", ProjectRoot: fixture.project}); err == nil {
		t.Fatal("a plugin without the shell grant must not launch tasks")
	}
	if list, err := fixture.manager.listTasks(fixture.project); err != nil || len(list.Tasks) != 0 || len(list.Providers) != 0 {
		t.Fatalf("list without grant = %+v, %v", list, err)
	}
}
