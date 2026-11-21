package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/terminal"
	"mincode/app/plugins/api"
)

const terminalProfileTestPlugin = "term.fixture"

type terminalProfileLaunchFixture struct {
	manager *pluginManager
	record  *pluginRecord
	root    string
	started []terminal.ProfileShellSpec
	// live simulates the terminal package's session table.
	live   map[string]bool
	onExit map[string]func()
}

func newTerminalProfileLaunchFixture(t *testing.T) *terminalProfileLaunchFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	useTemporaryPluginData(t)
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/repl")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "plugin.mjs"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"id": terminalProfileTestPlugin, "name": "Terminal fixture", "version": "1.0.0", "apiVersion": 2,
		"category": "development", "capabilities": []string{"shell"},
		"runtime": map[string]any{"type": "process", "entry": "bin/plugin.mjs"},
		"contributes": map[string]any{"terminalProfiles": []any{
			map[string]any{
				"id": "repl", "title": "REPL", "command": "${pluginRoot}/bin/repl",
				"args": []string{"--root", "${pluginRoot}", "--ws", "${workspaceFolder}"},
				"env":  map[string]string{"FIXTURE": "${userHome}/x"},
			},
		}},
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
		id: terminalProfileTestPlugin, root: root, source: pluginSourceLocal, manifest: &parsed,
		enabled: true, state: pluginStateRunning, capabilities: append([]api.Capability(nil), parsed.Capabilities...),
	}
	fixture := &terminalProfileLaunchFixture{
		manager: &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}},
		record:  record, root: root,
		live: map[string]bool{}, onExit: map[string]func(){},
	}
	previousStart, previousAttach, previousPath := terminalProfileStartShell, terminalProfileAttachShell, terminalProfileLoginPath
	terminalProfileStartShell = func(spec terminal.ProfileShellSpec) (string, error) {
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
	terminalProfileAttachShell = func(sessionID string) bool { return fixture.live[sessionID] }
	terminalProfileLoginPath = func() string { return "" }
	t.Cleanup(func() {
		terminalProfileStartShell, terminalProfileAttachShell, terminalProfileLoginPath = previousStart, previousAttach, previousPath
		terminalProfileSessionsMu.Lock()
		terminalProfileSessions = map[string]terminalProfileSession{}
		terminalProfileSessionsMu.Unlock()
	})
	return fixture
}

func (fixture *terminalProfileLaunchFixture) start(fingerprint, session string, attach bool) (string, error) {
	return fixture.manager.startTerminalProfile(terminalProfileStartRequest{
		PluginID: terminalProfileTestPlugin, ProfileID: "repl", Fingerprint: fingerprint, SessionID: session, AttachIfExists: attach,
	})
}

// exit simulates the session's process ending (or close_local_shell).
func (fixture *terminalProfileLaunchFixture) exit(session string) {
	delete(fixture.live, session)
	if onExit := fixture.onExit[session]; onExit != nil {
		delete(fixture.onExit, session)
		onExit()
	}
}

func (fixture *terminalProfileLaunchFixture) trust(t *testing.T) TerminalProfileLaunch {
	t.Helper()
	launch, err := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.trustTerminalProfile(terminalProfileTrustRequest{PluginID: terminalProfileTestPlugin, ProfileID: "repl", Fingerprint: launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	return launch
}

func terminalProfileSessionTracked(session string) bool {
	_, ok := lookupTerminalProfileSession(session)
	return ok
}

func expectTerminalProfileCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), code+":") {
		t.Fatalf("expected %s error, got %v", code, err)
	}
}

func TestTerminalProfilePrepareResolvesFromHostSnapshot(t *testing.T) {
	fixture := newTerminalProfileLaunchFixture(t)
	workspace := t.TempDir()
	launch, err := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", workspace)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(fixture.root)
	home, _ := os.UserHomeDir()
	if launch.Argv[0] != filepath.Join(root, "bin", "repl") || launch.Argv[2] != root || launch.Argv[4] != workspace {
		t.Fatalf("argv = %v", launch.Argv)
	}
	if launch.Env["FIXTURE"] != home+"/x" || launch.Cwd != workspace || !launch.PluginExecutable || len(launch.ExecutableSHA256) != 64 {
		t.Fatalf("launch = %+v", launch)
	}
	if launch.Trusted || launch.PreviouslyTrusted || launch.PluginName != "Terminal fixture" {
		t.Fatalf("new launch must be untrusted: %+v", launch)
	}
	if _, err := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", "relative/dir"); err == nil {
		t.Fatal("relative workspace folder accepted")
	}
	if _, err := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "missing", ""); err == nil {
		t.Fatal("unknown profile accepted")
	}
	noWorkspace, err := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", "")
	if err != nil || noWorkspace.Cwd != home || noWorkspace.Fingerprint == launch.Fingerprint {
		t.Fatalf("empty workspace must fall back to home: %+v %v", noWorkspace, err)
	}
}

func TestTerminalProfileStartRequiresConfirmedFingerprint(t *testing.T) {
	fixture := newTerminalProfileLaunchFixture(t)
	launch, err := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.start(launch.Fingerprint, "s1", false)
	expectTerminalProfileCode(t, err, terminalProfileErrUntrusted)

	_, err = fixture.manager.trustTerminalProfile(terminalProfileTrustRequest{PluginID: terminalProfileTestPlugin, ProfileID: "repl", Fingerprint: strings.Repeat("0", 64)})
	expectTerminalProfileCode(t, err, terminalProfileErrChanged)
	if _, err := fixture.manager.trustTerminalProfile(terminalProfileTrustRequest{PluginID: terminalProfileTestPlugin, ProfileID: "repl", Fingerprint: launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}

	_, err = fixture.start(strings.Repeat("0", 64), "s1", false)
	expectTerminalProfileCode(t, err, terminalProfileErrChanged)
	if _, err := fixture.start(launch.Fingerprint, "s1", false); err != nil {
		t.Fatal(err)
	}
	if len(fixture.started) != 1 || fixture.started[0].Executable != launch.Argv[0] || fixture.started[0].Cwd != launch.Cwd || fixture.started[0].Env["FIXTURE"] != launch.Env["FIXTURE"] {
		t.Fatalf("started = %+v", fixture.started)
	}
	again, _ := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", "")
	if !again.Trusted {
		t.Fatal("confirmation was not stored")
	}
}

func TestTerminalProfileExecutableChangeRequiresReconfirmation(t *testing.T) {
	fixture := newTerminalProfileLaunchFixture(t)
	launch, _ := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", "")
	if _, err := fixture.manager.trustTerminalProfile(terminalProfileTrustRequest{PluginID: terminalProfileTestPlugin, ProfileID: "repl", Fingerprint: launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.root, "bin", "repl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
	changed, err := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", "")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Trusted || !changed.PreviouslyTrusted || changed.Fingerprint == launch.Fingerprint {
		t.Fatalf("binary change must invalidate trust: %+v", changed)
	}
	_, err = fixture.start(changed.Fingerprint, "s1", false)
	expectTerminalProfileCode(t, err, terminalProfileErrChanged)
	if len(fixture.started) != 0 {
		t.Fatal("changed executable was launched")
	}
}

func TestTerminalProfileGates(t *testing.T) {
	cases := map[string]struct {
		mutate func(*terminalProfileLaunchFixture)
		code   string
	}{
		"disabled": {func(f *terminalProfileLaunchFixture) {
			f.record.enabled = false
			f.record.state = pluginStateDisabled
		}, terminalProfileErrUnavailable},
		"failed":             {func(f *terminalProfileLaunchFixture) { f.record.state = pluginStateError }, terminalProfileErrUnavailable},
		"capability revoked": {func(f *terminalProfileLaunchFixture) { f.record.capabilities = nil }, terminalProfileErrForbidden},
		"other workspace": {func(f *terminalProfileLaunchFixture) {
			f.record.scope = pluginScopeWorkspace
			f.record.workspaces = map[string]struct{}{"elsewhere": {}}
		}, terminalProfileErrUnavailable},
		"uninstalled": {func(f *terminalProfileLaunchFixture) { f.manager.records = map[string]*pluginRecord{} }, terminalProfileErrUnavailable},
		"denied env at launch": {func(f *terminalProfileLaunchFixture) {
			entries := f.record.manifest.contributions[terminalProfilePointKey].Entries
			profile := entries[0].(TerminalProfileContribution)
			profile.Env = map[string]string{"LD_PRELOAD": "/tmp/x.so"}
			entries[0] = profile
		}, terminalProfileErrForbidden},
		"symlink escape at launch": {func(f *terminalProfileLaunchFixture) {
			outside := writeTerminalProfileExecutable(&testing.T{}, f.root+"-outside", "tool")
			path := filepath.Join(f.root, "bin", "repl")
			_ = os.Remove(path)
			_ = os.Symlink(outside, path)
		}, terminalProfileErrForbidden},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newTerminalProfileLaunchFixture(t)
			launch, err := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.manager.trustTerminalProfile(terminalProfileTrustRequest{PluginID: terminalProfileTestPlugin, ProfileID: "repl", Fingerprint: launch.Fingerprint}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(fixture.root + "-outside") })
			testCase.mutate(fixture)
			_, err = fixture.start(launch.Fingerprint, "s1", false)
			expectTerminalProfileCode(t, err, testCase.code)
			if len(fixture.started) != 0 {
				t.Fatal("gated profile was launched")
			}
		})
	}
}

func TestTerminalProfileAttachOnlyReattachesOwnSession(t *testing.T) {
	fixture := newTerminalProfileLaunchFixture(t)
	launch, _ := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", "")
	// An unknown session is never attached without the full gate.
	fixture.live["foreign"] = true
	_, err := fixture.start("", "foreign", true)
	expectTerminalProfileCode(t, err, terminalProfileErrUntrusted)
	fixture.trust(t)
	// A live session this profile did not start is neither attached nor replaced.
	_, err = fixture.start(launch.Fingerprint, "foreign", true)
	expectTerminalProfileCode(t, err, terminalProfileErrUnavailable)
	if len(fixture.started) != 0 {
		t.Fatal("a live foreign session was replaced")
	}

	if _, err := fixture.start(launch.Fingerprint, "s1", true); err != nil {
		t.Fatal(err)
	}
	if message, err := fixture.start(launch.Fingerprint, "s1", true); err != nil || message != "Shell attached" {
		t.Fatalf("reattach = %q %v", message, err)
	}
	// A live session is never restarted without attachIfExists.
	_, err = fixture.start(launch.Fingerprint, "s1", false)
	expectTerminalProfileCode(t, err, terminalProfileErrForbidden)
	// Reattach needs the confirmed fingerprint too.
	_, err = fixture.start("", "s1", true)
	expectTerminalProfileCode(t, err, terminalProfileErrChanged)
	if len(fixture.started) != 1 {
		t.Fatalf("started %d sessions", len(fixture.started))
	}
}

func TestTerminalProfileReattachRunsEveryGate(t *testing.T) {
	cases := map[string]struct {
		mutate func(*testing.T, *terminalProfileLaunchFixture)
		code   string
	}{
		"disabled": {func(_ *testing.T, f *terminalProfileLaunchFixture) {
			f.record.enabled = false
			f.record.state = pluginStateDisabled
		}, terminalProfileErrUnavailable},
		"left the workspace": {func(_ *testing.T, f *terminalProfileLaunchFixture) {
			f.record.scope = pluginScopeWorkspace
			f.record.workspaces = map[string]struct{}{"elsewhere": {}}
		}, terminalProfileErrUnavailable},
		"capability revoked": {func(_ *testing.T, f *terminalProfileLaunchFixture) { f.record.capabilities = nil }, terminalProfileErrForbidden},
		"confirmation removed": {func(_ *testing.T, _ *terminalProfileLaunchFixture) {
			_ = pluginKVDeleteRaw(hostPluginID, terminalProfileTrustKey(terminalProfileTestPlugin))
		}, terminalProfileErrUntrusted},
		"fingerprint changed": {func(t *testing.T, f *terminalProfileLaunchFixture) {
			path := filepath.Join(f.root, "bin", "repl")
			if err := os.WriteFile(path, []byte("#!/bin/sh\necho upgraded\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			later := time.Now().Add(2 * time.Second)
			_ = os.Chtimes(path, later, later)
		}, terminalProfileErrChanged},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newTerminalProfileLaunchFixture(t)
			launch := fixture.trust(t)
			if _, err := fixture.start(launch.Fingerprint, "s1", true); err != nil {
				t.Fatal(err)
			}
			testCase.mutate(t, fixture)
			message, err := fixture.start(launch.Fingerprint, "s1", true)
			expectTerminalProfileCode(t, err, testCase.code)
			if message != "" || len(fixture.started) != 1 {
				t.Fatalf("reattach after %s: %q, %d starts", name, message, len(fixture.started))
			}
			if !fixture.live["s1"] {
				t.Fatal("the running process must keep running")
			}
		})
	}
}

func TestTerminalProfileReattachRefusesSessionOfEarlierLaunch(t *testing.T) {
	fixture := newTerminalProfileLaunchFixture(t)
	first := fixture.trust(t)
	if _, err := fixture.start(first.Fingerprint, "s1", true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.root, "bin", "repl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho upgraded\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
	// The user reviews the upgraded launch; the old process is still running
	// the earlier binary and is not adopted under the new confirmation.
	second := fixture.trust(t)
	if second.Fingerprint == first.Fingerprint {
		t.Fatal("upgrade did not change the fingerprint")
	}
	_, err := fixture.start(second.Fingerprint, "s1", true)
	expectTerminalProfileCode(t, err, terminalProfileErrUnavailable)
	fixture.exit("s1")
	if _, err := fixture.start(second.Fingerprint, "s1", true); err != nil {
		t.Fatalf("start after the old session ended: %v", err)
	}
	if len(fixture.started) != 2 {
		t.Fatalf("started %d sessions", len(fixture.started))
	}
}

func TestTerminalProfileSessionsArePurged(t *testing.T) {
	fixture := newTerminalProfileLaunchFixture(t)
	launch := fixture.trust(t)
	startSession := func(session string) {
		t.Helper()
		if _, err := fixture.start(launch.Fingerprint, session, true); err != nil {
			t.Fatal(err)
		}
		if !terminalProfileSessionTracked(session) {
			t.Fatalf("session %s not tracked", session)
		}
	}

	// Process exit and close both end in the session's exit callback.
	startSession("exit")
	fixture.exit("exit")
	if terminalProfileSessionTracked("exit") {
		t.Fatal("exited session still tracked")
	}
	// A late exit callback of an earlier start never drops a newer entry.
	startSession("restart")
	staleExit := fixture.onExit["restart"]
	fixture.exit("restart")
	startSession("restart")
	staleExit()
	if !terminalProfileSessionTracked("restart") {
		t.Fatal("stale exit callback removed the newer session")
	}

	// Disable: the lifecycle hook forgets the plugin's sessions but leaves them running.
	startSession("disable")
	fixture.record.mu.Lock()
	fixture.record.enabled = false
	fixture.record.mu.Unlock()
	fixture.manager.runRecordLifecycleHooks(fixture.record)
	if terminalProfileSessionTracked("disable") || terminalProfileSessionTracked("restart") {
		t.Fatal("disable did not purge the plugin's sessions")
	}
	if !fixture.live["disable"] {
		t.Fatal("purge must not end the running process")
	}
	// Re-enabling does not re-adopt the orphaned session, and never replaces it.
	fixture.record.mu.Lock()
	fixture.record.enabled = true
	fixture.record.mu.Unlock()
	_, err := fixture.start(launch.Fingerprint, "disable", true)
	expectTerminalProfileCode(t, err, terminalProfileErrUnavailable)

	// Stop/reload of an enabled plugin keeps re-attach rights.
	startSession("stop")
	fixture.manager.runRecordLifecycleHooks(fixture.record)
	if !terminalProfileSessionTracked("stop") {
		t.Fatal("stopping an enabled plugin purged its sessions")
	}
	// Uninstall uses the removal hook.
	fixture.manager.runRecordRemovalHooks(terminalProfileTestPlugin, removalReasonUninstall)
	if terminalProfileSessionTracked("stop") {
		t.Fatal("uninstall did not purge the plugin's sessions")
	}
}

func TestTerminalProfileExecutableRehashedRightBeforeStart(t *testing.T) {
	fixture := newTerminalProfileLaunchFixture(t)
	launch := fixture.trust(t)
	path := filepath.Join(fixture.root, "bin", "repl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same size and mtime: the (size, mtime) hash cache would still report
	// the confirmed hash, so only the uncached re-hash notices.
	original, _ := os.ReadFile(path)
	tampered := append([]byte(nil), original...)
	tampered[len(tampered)-2] ^= 0x01
	if err := os.WriteFile(path, tampered, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	_, err = fixture.start(launch.Fingerprint, "s1", true)
	expectTerminalProfileCode(t, err, terminalProfileErrChanged)
	if len(fixture.started) != 0 || terminalProfileSessionTracked("s1") {
		t.Fatal("tampered executable was started")
	}

	// Restore, then swap the file between the verification and the exec.
	if err := os.WriteFile(path, original, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	previous := terminalProfileStartShell
	terminalProfileStartShell = func(spec terminal.ProfileShellSpec) (string, error) {
		replacement := path + ".new"
		if err := os.WriteFile(replacement, original, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		return previous(spec)
	}
	_, err = fixture.start(launch.Fingerprint, "s2", true)
	expectTerminalProfileCode(t, err, terminalProfileErrChanged)
	if len(fixture.started) != 0 || terminalProfileSessionTracked("s2") {
		t.Fatal("swapped executable was started")
	}
	terminalProfileStartShell = previous

	// Unchanged file: the verification passes and the start proceeds.
	if _, err := fixture.start(launch.Fingerprint, "s3", true); err != nil {
		t.Fatal(err)
	}
	if len(fixture.started) != 1 || fixture.started[0].BeforeExec == nil || fixture.started[0].OnExit == nil {
		t.Fatalf("started = %+v", fixture.started)
	}
}

func TestTerminalProfileTrustRemovedOnUninstallAndNotExported(t *testing.T) {
	fixture := newTerminalProfileLaunchFixture(t)
	launch, _ := fixture.manager.prepareTerminalProfile(terminalProfileTestPlugin, "repl", "")
	if _, err := fixture.manager.trustTerminalProfile(terminalProfileTrustRequest{PluginID: terminalProfileTestPlugin, ProfileID: "repl", Fingerprint: launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := pluginKVGetRaw(hostPluginID, terminalProfileTrustKey(terminalProfileTestPlugin)); !found {
		t.Fatal("trust not stored in the host namespace")
	}
	target := filepath.Join(t.TempDir(), "config.json")
	config := map[string]json.RawMessage{"format": json.RawMessage(`"mincode-config"`), "version": json.RawMessage(`2`), "values": json.RawMessage(`{}`)}
	if _, err := writeConfigExportWithPlugins(target, config); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(target); strings.Contains(string(raw), "terminalProfiles") {
		t.Fatalf("configuration export contains terminal profile trust: %s", raw)
	}
	if values, err := pluginKVNamespaceRepository(terminalProfileTestPlugin); err != nil || len(values) != 0 {
		t.Fatalf("plugin namespace holds trust: %v %v", values, err)
	}

	fixture.manager.runRecordRemovalHooks(terminalProfileTestPlugin, removalReasonUninstall)
	if _, found, _ := pluginKVGetRaw(hostPluginID, terminalProfileTrustKey(terminalProfileTestPlugin)); found {
		t.Fatal("trust survived uninstall")
	}
}

func TestTerminalProfileSessionsPurgedWhenStoppedPluginIsDisabled(t *testing.T) {
	fixture := newTerminalProfileLaunchFixture(t)
	launch := fixture.trust(t)
	if _, err := fixture.start(launch.Fingerprint, "s1", true); err != nil {
		t.Fatal(err)
	}
	// On-demand process plugins are usually not running when disabled.
	fixture.record.mu.Lock()
	fixture.record.state = pluginStateStopped
	fixture.record.enabled = false
	fixture.record.mu.Unlock()
	if err := fixture.manager.stopRecord(context.Background(), fixture.record, true); err != nil {
		t.Fatal(err)
	}
	if terminalProfileSessionTracked("s1") || !fixture.live["s1"] {
		t.Fatal("disabling a stopped plugin must purge (not kill) its profile sessions")
	}
}
