package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestTerminalProfileFingerprintGolden pins fingerprint v1 byte for byte:
// stored terminal profile confirmations depend on it. The expected values were
// computed with the pre-extraction implementation.
func TestTerminalProfileFingerprintGolden(t *testing.T) {
	cases := []struct {
		launch TerminalProfileLaunch
		want   string
	}{
		{
			launch: TerminalProfileLaunch{
				PluginID: "acme.repl", ProfileID: "repl",
				Argv:             []string{"/opt/acme/bin/repl", "--root", "/opt/acme", "--ws", "/home/u/p <&>"},
				Env:              map[string]string{"B": "2", "A": "/home/u/x"},
				Cwd:              "/home/u/p",
				ExecutableSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
			want: "d3aced089aa45034bd747c7d8829f2dc85278f110b0db86f291e2eb356a33693",
		},
		{
			launch: TerminalProfileLaunch{
				PluginID: "acme.repl", ProfileID: "bash", Argv: []string{"/bin/bash", "-l"}, Env: map[string]string{}, Cwd: "/home/u",
			},
			want: "dcb0ecc8067160cd48abceb646acfc921a20cd6469d7b92733e4f4131a728330",
		},
	}
	for _, testCase := range cases {
		if got := terminalProfileLaunchFingerprint(testCase.launch); got != testCase.want {
			t.Fatalf("fingerprint(%s) = %s, want %s", testCase.launch.ProfileID, got, testCase.want)
		}
		// Title, trust state and plugin files are not part of v1.
		launch := testCase.launch
		launch.Title, launch.Trusted, launch.PluginName = "changed", true, "other"
		if got := terminalProfileLaunchFingerprint(launch); got != testCase.want {
			t.Fatalf("fingerprint depends on display fields: %s", got)
		}
	}
}

type processLaunchFixture struct {
	root   string
	source processLaunchRecord
	spec   processLaunchSpec
	policy processLaunchPolicy
}

func newProcessLaunchFixture(t *testing.T) *processLaunchFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	useTemporaryPluginData(t)
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/tool")
	for name, content := range map[string]string{"lib/main.js": "main\n", "lib/conf.json": "{}\n", "lib/extra.txt": "x\n"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pluginRoot, err := processLaunchPluginRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	return &processLaunchFixture{
		root:   root,
		source: processLaunchRecord{pluginID: "acme.tasks", pluginName: "Acme", pluginVersion: "1.0.0", root: root},
		spec: processLaunchSpec{
			Command: "${pluginRoot}/bin/tool",
			Args:    []string{"${pluginRoot}/lib/main.js", "--config=${pluginRoot}/lib/conf.json", "${pluginRoot}/lib", "${input}", "${pluginRoot}/lib/main.js"},
			Env:     map[string]string{"TOOL_HOME": "${projectRoot}/out"},
			Cwd:     "${projectRoot}",
		},
		policy: processLaunchPolicy{
			Point: "tasks", EntryKey: "build",
			Variables: map[string]string{"${pluginRoot}": pluginRoot, "${projectRoot}": project},
			Deferred:  []string{"${input}"}, HashPluginFiles: true,
		},
	}
}

func (fixture *processLaunchFixture) resolve(t *testing.T) ResolvedProcessLaunch {
	t.Helper()
	launch, err := resolveProcessLaunch(fixture.source, fixture.spec, fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	return launch
}

func TestProcessLaunchV2FingerprintCoversFilesVersionAndGeneration(t *testing.T) {
	fixture := newProcessLaunchFixture(t)
	launch := fixture.resolve(t)
	if !launch.PluginExecutable || launch.ExecutableSHA256 == "" || launch.Trusted || launch.PreviouslyTrusted {
		t.Fatalf("launch = %+v", launch)
	}
	if len(launch.PluginFiles) != 2 || launch.PluginFiles[0].Relative != "lib/conf.json" || launch.PluginFiles[1].Relative != "lib/main.js" {
		t.Fatalf("plugin files = %+v", launch.PluginFiles)
	}
	if launch.Argv[4] != "${input}" || !strings.HasSuffix(launch.Env["TOOL_HOME"], "/out") {
		t.Fatalf("deferred variable resolved or env not expanded: %v %v", launch.Argv, launch.Env)
	}
	if again := fixture.resolve(t); again.Fingerprint != launch.Fingerprint {
		t.Fatal("fingerprint is not stable")
	}

	fixture.source.pluginVersion = "1.0.1"
	if fixture.resolve(t).Fingerprint == launch.Fingerprint {
		t.Fatal("plugin version is not part of the fingerprint")
	}
	fixture.source.pluginVersion = "1.0.0"
	fixture.source.devGeneration = "00000000000000000002"
	if fixture.resolve(t).Fingerprint == launch.Fingerprint {
		t.Fatal("development generation is not part of the fingerprint")
	}
	fixture.source.devGeneration = ""
	fixture.policy.Extra = json.RawMessage(`{"group":"build"}`)
	if fixture.resolve(t).Fingerprint == launch.Fingerprint {
		t.Fatal("extra is not part of the fingerprint")
	}
	fixture.policy.Extra = nil

	later := time.Now().Add(2 * time.Second)
	path := filepath.Join(fixture.root, "lib", "main.js")
	if err := os.WriteFile(path, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, later, later)
	if fixture.resolve(t).Fingerprint == launch.Fingerprint {
		t.Fatal("plugin file content is not part of the fingerprint")
	}
}

func TestProcessLaunchVerifyFilesRefusesTamperAfterReview(t *testing.T) {
	fixture := newProcessLaunchFixture(t)
	launch := fixture.resolve(t)
	beforeExec, err := verifyProcessLaunchFiles(launch)
	if err != nil || beforeExec == nil {
		t.Fatalf("verify = %v", err)
	}
	if err := beforeExec(); err != nil {
		t.Fatalf("unchanged files refused: %v", err)
	}
	path := filepath.Join(fixture.root, "lib", "conf.json")
	if err := os.WriteFile(path, []byte(`{"evil":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
	if err := beforeExec(); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("swap before exec: %v", err)
	}
	if _, err := verifyProcessLaunchFiles(launch); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("re-hash after tamper: %v", err)
	}
}

func TestProcessLaunchPolicyBounds(t *testing.T) {
	fixture := newProcessLaunchFixture(t)
	fixture.spec.Args = []string{
		"${pluginRoot}/lib/main.js", "${pluginRoot}/lib/conf.json", "${pluginRoot}/lib/extra.txt", "${pluginRoot}/bin/tool",
		"--x=${pluginRoot}/plugin-missing", "${pluginRoot}/lib",
	}
	if _, err := resolveProcessLaunch(fixture.source, fixture.spec, fixture.policy); err != nil {
		t.Fatalf("four files must be accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "lib", "fifth"), []byte("5"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.spec.Args = append(fixture.spec.Args, "${pluginRoot}/lib/fifth")
	if _, err := resolveProcessLaunch(fixture.source, fixture.spec, fixture.policy); processLaunchErrorCode(err) != processLaunchErrLimit {
		t.Fatalf("fifth plugin file: %v", err)
	}

	fixture = newProcessLaunchFixture(t)
	fixture.spec.Command = "/bin/sh"
	if _, err := resolveProcessLaunch(fixture.source, fixture.spec, fixture.policy); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("absolute command without AllowAbsolute: %v", err)
	}
	fixture.spec.Command = "${pluginRoot}/bin/tool"
	fixture.spec.Env = map[string]string{"NODE_OPTIONS": "--require x"}
	if _, err := resolveProcessLaunch(fixture.source, fixture.spec, fixture.policy); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("denied env: %v", err)
	}
	fixture.spec.Env = map[string]string{"dyld_insert_libraries": "x"}
	if _, err := resolveProcessLaunch(fixture.source, fixture.spec, fixture.policy); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("denied env prefix: %v", err)
	}
	if err := validateProcessLaunchText("${userHome}/x", 64, []string{"${projectRoot}", "${pluginRoot}"}); err == nil {
		t.Fatal("unknown variable accepted")
	}
	if err := validateProcessLaunchText("${pluginRoot}", 64, nil); err == nil {
		t.Fatal("variable accepted where none are allowed")
	}
}

func TestProcessLaunchTrustStore(t *testing.T) {
	fixture := newProcessLaunchFixture(t)
	launch := fixture.resolve(t)
	what := `task "build"`
	if err := checkProcessLaunchStart(launch, launch.Fingerprint, what); processLaunchErrorCode(err) != processLaunchErrUntrusted {
		t.Fatalf("unconfirmed start: %v", err)
	}
	if _, err := trustProcessLaunch(launch, "stale", what); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("stale review stored: %v", err)
	}
	if _, err := trustProcessLaunch(launch, launch.Fingerprint, what); err != nil {
		t.Fatal(err)
	}
	launch = fixture.resolve(t)
	if !launch.Trusted || checkProcessLaunchStart(launch, launch.Fingerprint, what) != nil {
		t.Fatalf("confirmed launch = %+v", launch)
	}
	fixture.source.pluginVersion = "2.0.0"
	upgraded := fixture.resolve(t)
	if upgraded.Trusted || !upgraded.PreviouslyTrusted {
		t.Fatalf("upgrade kept confirmation: %+v", upgraded)
	}
	if err := checkProcessLaunchStart(upgraded, upgraded.Fingerprint, what); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("changed start: %v", err)
	}

	// The terminalProfiles v1 layout stays readable.
	if err := pluginKVSetRaw(hostPluginID, processLaunchTrustKey("acme.legacy", "terminalProfiles"), json.RawMessage(`{"version":1,"profiles":{"repl":"abc"}}`)); err != nil {
		t.Fatal(err)
	}
	legacy, err := readProcessLaunchTrust("acme.legacy", "terminalProfiles")
	if err != nil || legacy.Entries["repl"].Fingerprint != "abc" {
		t.Fatalf("legacy trust = %+v %v", legacy, err)
	}

	store := processLaunchTrust{Entries: map[string]processLaunchTrustEntry{
		"a": {Fingerprint: "1", At: 30}, "b": {Fingerprint: "2", At: 10}, "c": {Fingerprint: "3", At: 20},
	}}
	if err := writeProcessLaunchTrust("acme.cap", "tasks", store, 2); err != nil {
		t.Fatal(err)
	}
	capped, _ := readProcessLaunchTrust("acme.cap", "tasks")
	if len(capped.Entries) != 2 || capped.Entries["b"].Fingerprint != "" || capped.Entries["a"].Fingerprint != "1" {
		t.Fatalf("capped trust = %+v", capped)
	}
	// Extra confirmations of a stored entry are kept only up to what the
	// point allows: none for tasks, processLaunchFingerprintsPerEntry for
	// debuggers. A hand-edited record cannot widen trust.
	many := make([]string, 20)
	for i := range many {
		many[i] = fmt.Sprintf("old-%d", i)
	}
	for _, point := range []string{"tasks", debuggerPointKey} {
		raw, _ := json.Marshal(processLaunchTrust{Version: processLaunchTrustVersion, Entries: map[string]processLaunchTrustEntry{
			"k": {Fingerprint: "latest", Others: append([]string{"", "latest"}, many...), At: 1},
		}})
		if err := pluginKVSetRaw(hostPluginID, processLaunchTrustKey("acme.many", point), raw); err != nil {
			t.Fatal(err)
		}
		got, err := readProcessLaunchTrust("acme.many", point)
		if err != nil {
			t.Fatal(err)
		}
		entry := got.Entries["k"]
		want := processLaunchFingerprintsPerEntry[point] - 1
		if want < 0 {
			want = 0
		}
		if !entry.confirms("latest") || len(entry.Others) != want || (want > 0 && entry.Others[0] != "old-0") {
			t.Fatalf("%s entry = %+v, want latest plus %d others", point, entry, want)
		}
	}
}

// No "<pluginId>.<point>.trust" key is ever exported, and uninstall removes
// every point's confirmations.
func TestProcessLaunchTrustNeverExportedAndRemovedOnUninstall(t *testing.T) {
	useTemporaryPluginData(t)
	const pluginID = "acme.trust"
	for _, point := range processLaunchTrustPoints {
		if err := pluginKVSetRaw(hostPluginID, processLaunchTrustKey(pluginID, point), json.RawMessage(`{"version":2,"entries":{"x":{"fingerprint":"f","at":1}}}`)); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(t.TempDir(), "config.json")
	config := map[string]json.RawMessage{"format": json.RawMessage(`"mincode-config"`), "version": json.RawMessage(`2`), "values": json.RawMessage(`{}`)}
	if _, err := writeConfigExportWithPlugins(target, config); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(target); strings.Contains(string(raw), ".trust") || strings.Contains(string(raw), hostPluginID) {
		t.Fatalf("configuration export contains launch trust: %s", raw)
	}
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	manager.runRecordRemovalHooks(pluginID, removalReasonClearData)
	for _, point := range processLaunchTrustPoints {
		if _, found, _ := pluginKVGetRaw(hostPluginID, processLaunchTrustKey(pluginID, point)); found {
			t.Fatalf("%s trust survived Clear Plugin Data", point)
		}
	}
}
