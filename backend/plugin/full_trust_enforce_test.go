package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

// Full trust at the Go confirmation points: plugin-to-plugin calls and
// service calls skip their prompt when the caller or the callee is trusted
// (and the app is unlocked); grants, the provider picker, prompts the plugin
// asks for and process launch confirmations never do.

// addTrustablePlugin registers id in manager as an enabled local plugin with
// a small package, so fullTrustSubject finds it eligible. It does not start
// it or switch the plugin data directory.
func addTrustablePlugin(t *testing.T, manager *pluginManager, id string, capabilities []api.Capability, actions ...ManifestUIAction) {
	t.Helper()
	root := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "plugin"), []byte("#!/bin/sh\necho "+id+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range actions {
		if actions[i].Permissions == nil {
			actions[i].Permissions = []api.Capability{}
		}
	}
	manifest := Manifest{
		APIVersion: 2, ID: id, Name: "Plugin " + id, Version: "1.0.0", Category: "development",
		Capabilities: capabilities, Runtime: ProcessRuntimeManifest{Type: "process", Entry: "bin/plugin"},
		Contributes: ManifestContributions{Actions: actions},
	}
	writeFullTrustManifest(t, root, manifest)
	if err := manager.registerLocal(root, manifest); err != nil {
		t.Fatal(err)
	}
	record, err := manager.record(id)
	if err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	record.enabled = true
	record.mu.Unlock()
}

// grantFullTrustFor stores id's current candidate record, as
// plugin_full_trust_set does, without the audit entry or event.
func grantFullTrustFor(t *testing.T, manager *pluginManager, id string) FullTrustSubject {
	t.Helper()
	subject, err := manager.fullTrustSubject(id)
	if err != nil {
		t.Fatal(err)
	}
	if !subject.Eligible {
		t.Fatalf("%s not eligible: %+v", id, subject)
	}
	record := subject.record
	record.GrantedAt = 1762128000000
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := pluginKVSetRaw(hostPluginID, fullTrustKey(id), raw); err != nil {
		t.Fatal(err)
	}
	return subject
}

func dropFullTrustFor(t *testing.T, id string) {
	t.Helper()
	if err := pluginKVDeleteRaw(hostPluginID, fullTrustKey(id)); err != nil {
		t.Fatal(err)
	}
}

// makeServiceProviderTrustable swaps the provider's hard-linked test binary
// (far over fullTrustPackageMaxBytes) for a small script that runs it, so
// the provider's package can be fingerprinted. Call it before the provider
// starts.
func makeServiceProviderTrustable(t *testing.T, fixture *serviceCallFixture) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(executable, "'") {
		t.Skip("test binary path contains a quote")
	}
	path := filepath.Join(fixture.record.root, "helper.exe")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec '"+executable+"' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// refusePrompts cancels (and reports) any prompt shown until the returned
// stop function runs, so a prompt that should have been skipped fails fast
// instead of waiting for its timeout.
func refusePrompts(t *testing.T, events *capturedUIEvents) func() {
	t.Helper()
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
			events.mu.Lock()
			var found *promptEvent
			for i, event := range events.events {
				if event.name == pluginPromptEvent {
					prompt := event.data.(promptEvent)
					found = &prompt
					events.events = append(events.events[:i], events.events[i+1:]...)
					break
				}
			}
			events.mu.Unlock()
			if found != nil {
				t.Errorf("unexpected %s prompt", found.Kind)
				raw, _ := json.Marshal(map[string]any{"id": found.ID, "reply": promptReply{Cancelled: true}})
				_, _ = pluginHostPromptReplyHandler("", string(raw))
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func skippedAudit(t *testing.T, id string) []FullTrustAuditEntry {
	t.Helper()
	entries, err := readFullTrustAudit(id)
	if err != nil {
		t.Fatal(err)
	}
	var skipped []FullTrustAuditEntry
	for _, entry := range entries {
		if entry.Kind == fullTrustAuditSkipped {
			skipped = append(skipped, entry)
		}
	}
	return skipped
}

type pluginCallTrustFixture struct {
	events    *capturedUIEvents
	manager   *pluginManager
	fake      *fakePluginCalls
	caller    *processRuntime
	setLocked func(bool)
}

// newPluginCallTrustFixture registers caller.a (plugins capability) and
// target.b (aiCallable "save" write and "activity" read) in a fresh manager.
// The target's actions and the invocation are faked like
// host_plugins_test.go; the grant store and the full trust check are real.
func newPluginCallTrustFixture(t *testing.T) *pluginCallTrustFixture {
	t.Helper()
	useTemporaryPluginData(t)
	setLocked := unlockAppForTest(t)
	events := captureHostUI(t)
	realSkip, realGranted, realGrant := pluginCallFullTrustSkip, pluginCallGranted, grantPluginCall
	fake := usePluginCallFakes(t, map[string]ManifestUIAction{
		"target.b/save":     {ID: "save", AICallable: true, Risk: "write"},
		"target.b/activity": {ID: "activity", AICallable: true, Risk: "read"},
	})
	pluginCallFullTrustSkip, pluginCallGranted, grantPluginCall = realSkip, realGranted, realGrant
	manager := &pluginManager{records: map[string]*pluginRecord{}, uiHost: newPluginUIHost()}
	addTrustablePlugin(t, manager, "caller.a", []api.Capability{api.CapabilityPlugins}, ManifestUIAction{ID: "ping", Risk: "read", AICallable: true})
	addTrustablePlugin(t, manager, "target.b", nil,
		ManifestUIAction{ID: "save", Risk: "write", AICallable: true}, ManifestUIAction{ID: "activity", Risk: "read", AICallable: true})
	useServicesManager(t, manager)
	caller := uiRuntime("caller.a")
	caller.ownerManager = manager
	return &pluginCallTrustFixture{events: events, manager: manager, fake: fake, caller: caller, setLocked: setLocked}
}

func (fixture *pluginCallTrustFixture) invoke(t *testing.T, action string) *rpcError {
	t.Helper()
	_, err := uiCall(hostPluginsInvoke, fixture.caller, `{"pluginId":"target.b","action":"`+action+`","timeoutMs":5000}`)
	return err
}

// invokeUnprompted invokes action and fails if any prompt was shown.
func (fixture *pluginCallTrustFixture) invokeUnprompted(t *testing.T, action string) {
	t.Helper()
	stop := refusePrompts(t, fixture.events)
	err := fixture.invoke(t, action)
	stop()
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	noPromptEvents(t, fixture.events)
}

// invokePrompted invokes the write action, expects the pluginCall prompt and
// approves it once.
func (fixture *pluginCallTrustFixture) invokePrompted(t *testing.T) {
	t.Helper()
	go answerNext(t, fixture.events, func(event promptEvent) promptReply {
		if event.Kind != "pluginCall" || event.PluginID != "caller.a" || event.Call == nil || event.Call.TargetPluginID != "target.b" || event.Call.Risk != "write" {
			t.Errorf("approval %+v %+v", event, event.Call)
		}
		return promptReply{IDs: []string{"once"}}
	})
	if err := fixture.invoke(t, "save"); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmPluginCallUnderFullTrust(t *testing.T) {
	t.Run("neither", func(t *testing.T) {
		fixture := newPluginCallTrustFixture(t)
		fixture.invokePrompted(t)
		if len(fixture.fake.calls) != 1 {
			t.Fatalf("calls = %d", len(fixture.fake.calls))
		}
		for _, id := range []string{"caller.a", "target.b"} {
			if entries := skippedAudit(t, id); len(entries) != 0 {
				t.Fatalf("%s audit without trust: %+v", id, entries)
			}
		}
	})

	t.Run("caller", func(t *testing.T) {
		fixture := newPluginCallTrustFixture(t)
		grantFullTrustFor(t, fixture.manager, "caller.a")
		fixture.invokeUnprompted(t, "save")
		if len(fixture.fake.calls) != 1 || fixture.fake.calls[0].PluginID != "target.b" || fixture.fake.calls[0].EntryID != "caller.a" {
			t.Fatalf("calls = %+v", fixture.fake.calls)
		}
		entries := skippedAudit(t, "caller.a")
		if len(entries) != 1 || entries[0].Surface != "pluginCall" || entries[0].Risk != "write" || entries[0].Subject != "save" ||
			entries[0].Counterpart != "target.b" || entries[0].Count != 1 || entries[0].Reason != "" {
			t.Fatalf("caller audit = %+v", entries)
		}
		if entries := skippedAudit(t, "target.b"); len(entries) != 0 {
			t.Fatalf("untrusted target audited: %+v", entries)
		}
	})

	t.Run("target", func(t *testing.T) {
		fixture := newPluginCallTrustFixture(t)
		grantFullTrustFor(t, fixture.manager, "target.b")
		fixture.invokeUnprompted(t, "save")
		if len(fixture.fake.calls) != 1 {
			t.Fatalf("calls = %d", len(fixture.fake.calls))
		}
		entries := skippedAudit(t, "target.b")
		if len(entries) != 1 || entries[0].Surface != "pluginCall" || entries[0].Risk != "write" || entries[0].Subject != "save" || entries[0].Counterpart != "caller.a" {
			t.Fatalf("target audit = %+v", entries)
		}
		if entries := skippedAudit(t, "caller.a"); len(entries) != 0 {
			t.Fatalf("untrusted caller audited: %+v", entries)
		}
	})

	t.Run("both", func(t *testing.T) {
		fixture := newPluginCallTrustFixture(t)
		grantFullTrustFor(t, fixture.manager, "caller.a")
		grantFullTrustFor(t, fixture.manager, "target.b")
		fixture.invokeUnprompted(t, "save")
		callerAudit, targetAudit := skippedAudit(t, "caller.a"), skippedAudit(t, "target.b")
		if len(callerAudit) != 1 || callerAudit[0].Counterpart != "target.b" || len(targetAudit) != 1 || targetAudit[0].Counterpart != "caller.a" {
			t.Fatalf("audit caller %+v target %+v", callerAudit, targetAudit)
		}
	})

	t.Run("service", func(t *testing.T) {
		events := captureHostUI(t)
		provider := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
		unlockAppForTest(t)
		makeServiceProviderTrustable(t, provider)
		useServicesManager(t, provider.manager)
		grantFullTrustFor(t, provider.manager, "service.short")
		stop := refusePrompts(t, events)
		result, rpcErr := servicesCall(t, hostServicesInvoke, servicesCaller("caller.a", api.CapabilityPlugins), `{"service":"demo.text.reverse","input":"plain","timeoutMs":5000}`)
		stop()
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		if result["output"] != "done" {
			t.Fatalf("result = %v", result)
		}
		noPromptEvents(t, events)
		entries := skippedAudit(t, "service.short")
		if len(entries) != 1 || entries[0].Surface != "serviceCall" || entries[0].Risk != "write" ||
			entries[0].Subject != "demo.text.reverse" || entries[0].Counterpart != "caller.a" {
			t.Fatalf("provider audit = %+v", entries)
		}
	})

	t.Run("readGrantUntouched", func(t *testing.T) {
		fixture := newPluginCallTrustFixture(t)
		grantFullTrustFor(t, fixture.manager, "caller.a")
		fixture.invokeUnprompted(t, "activity")
		if targets := readPluginCallGrants("caller.a").Targets; len(targets) != 0 {
			t.Fatalf("trust wrote a read grant: %v", targets)
		}
		entries := skippedAudit(t, "caller.a")
		if len(entries) != 1 || entries[0].Risk != "read" || entries[0].Subject != "activity" {
			t.Fatalf("audit = %+v", entries)
		}
	})

	t.Run("locked", func(t *testing.T) {
		fixture := newPluginCallTrustFixture(t)
		grantFullTrustFor(t, fixture.manager, "caller.a")
		grantFullTrustFor(t, fixture.manager, "target.b")
		fixture.setLocked(true)
		fixture.invokePrompted(t)
		for _, id := range []string{"caller.a", "target.b"} {
			if entries := skippedAudit(t, id); len(entries) != 0 {
				t.Fatalf("%s audited while locked: %+v", id, entries)
			}
		}
		// Unlocking restores the skip; the records survived the lock.
		fixture.setLocked(false)
		fixture.invokeUnprompted(t, "save")
	})

	t.Run("audit", func(t *testing.T) {
		fixture := newPluginCallTrustFixture(t)
		grantFullTrustFor(t, fixture.manager, "target.b")
		for range 3 {
			fixture.invokeUnprompted(t, "save")
		}
		entries := skippedAudit(t, "target.b")
		if len(entries) != 1 || entries[0].Count != 3 || entries[0].Counterpart != "caller.a" {
			t.Fatalf("audit = %+v", entries)
		}
		if len(fixture.fake.calls) != 3 {
			t.Fatalf("calls = %d", len(fixture.fake.calls))
		}
	})
}

// trustStates runs check with the plugin untrusted, trusted and trusted
// while the app is locked; want reports what fullTrustActive must say.
func trustStates(t *testing.T, manager *pluginManager, setLocked func(bool), id string, check func(t *testing.T, state string)) {
	t.Helper()
	for _, state := range []string{"off", "on", "locked"} {
		dropFullTrustFor(t, id)
		setLocked(false)
		if state != "off" {
			grantFullTrustFor(t, manager, id)
		}
		if state == "locked" {
			setLocked(true)
		}
		if active := manager.fullTrustActive(id); active != (state == "on") {
			t.Fatalf("%s: fullTrustActive = %v", state, active)
		}
		check(t, state)
	}
	setLocked(false)
}

func TestFullTrustKeepsGrantPrompts(t *testing.T) {
	t.Run("databaseSchema", func(t *testing.T) {
		events := captureHostUI(t)
		useDatabaseSchemaFakes(t)
		setLocked := unlockAppForTest(t)
		manager := &pluginManager{records: map[string]*pluginRecord{}, uiHost: newPluginUIHost()}
		addTrustablePlugin(t, manager, "schema-studio", []api.Capability{api.CapabilityDatabaseSchema}, ManifestUIAction{ID: "run", Risk: "write"})
		useServicesManager(t, manager)
		caller := uiRuntime("schema-studio")
		caller.ownerManager = manager
		trustStates(t, manager, setLocked, "schema-studio", func(t *testing.T, state string) {
			go answerNext(t, events, func(event promptEvent) promptReply {
				if event.Kind != databaseSchemaPromptKind || event.Database == nil || event.Database.ConnectionID != "c1" {
					t.Errorf("%s: prompt %+v", state, event)
				}
				return promptReply{Cancelled: true}
			})
			if _, err := uiCall(hostDatabaseTables, caller, `{"connectionId":"c1"}`); err == nil || err.Code != -32003 {
				t.Fatalf("%s: tables without the grant: %v", state, err)
			}
			if databaseSchemaGranted("schema-studio", "c1") {
				t.Fatalf("%s: grant written", state)
			}
		})
	})

	t.Run("sshFiles", func(t *testing.T) {
		_, events := useSSHFilesFakes(t)
		setLocked := unlockAppForTest(t)
		manager := &pluginManager{records: map[string]*pluginRecord{}, uiHost: newPluginUIHost()}
		addTrustablePlugin(t, manager, sshFilesTestPlugin, []api.Capability{api.CapabilityBackup}, ManifestUIAction{ID: "run", Risk: "write"})
		useServicesManager(t, manager)
		caller := backupRuntime(sshFilesTestPlugin, manager)
		trustStates(t, manager, setLocked, sshFilesTestPlugin, func(t *testing.T, state string) {
			if err := authorizeSSHFiles(t, events, caller, "~/backups/mincode", ""); sshFilesErrorCode(err) != "declined" {
				t.Fatalf("%s: authorize = %v", state, err)
			}
			if grants := readSSHFilesGrants(sshFilesTestPlugin); len(grants) != 0 {
				t.Fatalf("%s: grants %v", state, grants)
			}
		})
	})

	t.Run("serviceProvider", func(t *testing.T) {
		events := captureHostUI(t)
		first := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
		addServiceProcessProvider(t, first.manager, "service.tasks", "TestServiceHelperProcess", serviceTestEntry(), 16384)
		setLocked := unlockAppForTest(t)
		addTrustablePlugin(t, first.manager, "caller.a", []api.Capability{api.CapabilityPlugins}, ManifestUIAction{ID: "ping", Risk: "read", AICallable: true})
		useServicesManager(t, first.manager)
		caller := servicesCaller("caller.a", api.CapabilityPlugins)
		caller.ownerManager = first.manager
		trustStates(t, first.manager, setLocked, "caller.a", func(t *testing.T, state string) {
			go answerNext(t, events, func(event promptEvent) promptReply {
				if event.Kind != serviceProviderPromptKind || event.Service != "demo.text.reverse" || len(event.Items) != 2 {
					t.Errorf("%s: prompt %+v", state, event)
				}
				return promptReply{Cancelled: true}
			})
			if _, rpcErr := servicesCall(t, hostServicesInvoke, caller, `{"service":"demo.text.reverse","input":"x","timeoutMs":5000}`); rpcErrorCode(rpcErr) != "declined" {
				t.Fatalf("%s: invoke = %v", state, rpcErr)
			}
		})
	})

	// Prompts the plugin asks for itself, and the requests whose
	// confirmation is the desktop's (terminal command, project open): the
	// host shows or forwards them exactly as without trust.
	t.Run("pluginRequested", func(t *testing.T) {
		fixture := fullTrustManager(t)
		setLocked := unlockAppForTest(t)
		events := captureHostUI(t)
		root := useWorkspaceRoot(t)
		project := t.TempDir()
		useServicesManager(t, fixture.manager)
		caller := uiRuntime(fullTrustDemoID)
		caller.ownerManager = fixture.manager
		var terminalEvent, projectEvent string
		trustStates(t, fixture.manager, setLocked, fullTrustDemoID, func(t *testing.T, state string) {
			go answerNext(t, events, func(event promptEvent) promptReply {
				if event.Kind != "quickPick" {
					t.Errorf("%s: prompt %+v", state, event)
				}
				return promptReply{Cancelled: true}
			})
			if result, err := uiCall(hostQuickPick, caller, `{"title":"Pick","items":[{"id":"a","label":"A"}],"timeoutMs":5000}`); err != nil || result.(map[string]any)["cancelled"] != true {
				t.Fatalf("%s: quickPick = %v %v", state, result, err)
			}
			go answerNext(t, events, func(event promptEvent) promptReply {
				if event.Kind != "inputBox" {
					t.Errorf("%s: prompt %+v", state, event)
				}
				return promptReply{Cancelled: true}
			})
			if result, err := uiCall(hostInputBox, caller, `{"title":"Name","timeoutMs":5000}`); err != nil || result.(map[string]any)["cancelled"] != true {
				t.Fatalf("%s: inputBox = %v %v", state, result, err)
			}
			if _, err := uiCall(hostTerminalOpen, caller, `{"command":"make build"}`); err != nil {
				t.Fatalf("%s: terminal.open = %v", state, err)
			}
			got := jsonOf(t, events.last(t, pluginTerminalOpenEvent))
			if terminalEvent == "" {
				terminalEvent = got
				if !strings.Contains(got, `"command":"make build"`) || !strings.Contains(got, root) {
					t.Fatalf("terminal event %s", got)
				}
			} else if got != terminalEvent {
				t.Fatalf("%s: terminal event %s, untrusted %s", state, got, terminalEvent)
			}
			if _, err := uiCall(hostProjectOpen, caller, `{"path":`+jsonOf(t, project)+`}`); err != nil {
				t.Fatalf("%s: project.open = %v", state, err)
			}
			got = jsonOf(t, events.last(t, pluginProjectOpenEvent))
			if projectEvent == "" {
				projectEvent = got
			} else if got != projectEvent {
				t.Fatalf("%s: project event %s, untrusted %s", state, got, projectEvent)
			}
		})
		if entries := skippedAudit(t, fullTrustDemoID); len(entries) != 0 {
			t.Fatalf("audited %+v", entries)
		}
	})
}

func TestFullTrustKeepsProcessLaunchConfirmation(t *testing.T) {
	fixture := newTaskLaunchFixture(t)
	setLocked := unlockAppForTest(t)
	useServicesManager(t, fixture.manager)
	trustStates(t, fixture.manager, setLocked, taskTestPlugin, func(t *testing.T, state string) {
		launch, err := fixture.manager.prepareTask(taskPrepareRequest{PluginID: taskTestPlugin, TaskKey: "static:build", ProjectRoot: fixture.project})
		if err != nil {
			t.Fatalf("%s: prepare = %v", state, err)
		}
		if launch.Trusted {
			t.Fatalf("%s: prepare reports trusted", state)
		}
		for _, fingerprint := range []string{"", launch.Fingerprint} {
			if _, err := fixture.start("static:build", fixture.project, fingerprint, "s-"+state, false); processLaunchErrorCode(err) != processLaunchErrUntrusted {
				t.Fatalf("%s: unconfirmed start (fingerprint %q) = %v", state, fingerprint, err)
			}
		}
		if len(fixture.started) != 0 {
			t.Fatalf("%s: started %d", state, len(fixture.started))
		}
	})
	if entries := skippedAudit(t, taskTestPlugin); len(entries) != 0 {
		t.Fatalf("audited %+v", entries)
	}
}
