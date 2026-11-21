package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/backup"
	"mincode/app/plugins/api"
)

const backupTestPassword = "correct horse battery staple"

// useBackupHost isolates plugin data, the staging area, the remembered
// password and the frontend request channel for one test.
func useBackupHost(t *testing.T) *capturedUIEvents {
	t.Helper()
	useTemporaryPluginData(t)
	events := captureHostUI(t)
	resetBackupStagingForTest(t)
	remembered := &fakeRememberedPassword{}
	previousGet, previousSet, previousForget := backupRememberedPassword, backupRememberPassword, backupForgetPassword
	backupRememberedPassword = remembered.get
	backupRememberPassword = remembered.set
	backupForgetPassword = remembered.forget
	t.Cleanup(func() {
		backupRememberedPassword, backupRememberPassword, backupForgetPassword = previousGet, previousSet, previousForget
		clearBackupSessionPasswords()
		backupRestoreMu.Lock()
		backupOpenSessions = map[string]*backupOpenSession{}
		backupRestoreRecords = map[string]*backupRestoreRecord{}
		backupRestoreMu.Unlock()
	})
	return events
}

func resetBackupStagingForTest(t *testing.T) {
	t.Helper()
	backupStagingOnce = sync.Once{}
	backupStagingArea = nil
	t.Cleanup(func() {
		backupStagingOnce = sync.Once{}
		backupStagingArea = nil
	})
}

type fakeRememberedPassword struct {
	mu       sync.Mutex
	password []byte
}

func (f *fakeRememberedPassword) get() ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.password == nil {
		return nil, false, nil
	}
	return append([]byte(nil), f.password...), true, nil
}

func (f *fakeRememberedPassword) set(password []byte) error {
	if err := backup.ValidatePassword(password); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.password = append([]byte(nil), password...)
	return nil
}

func (f *fakeRememberedPassword) forget() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.password = nil
	return nil
}

func backupRuntime(id string, manager *pluginManager) *processRuntime {
	runtimeHost := uiRuntime(id)
	runtimeHost.capabilities = map[api.Capability]struct{}{api.CapabilityBackup: {}}
	runtimeHost.ownerManager = manager
	return runtimeHost
}

func backupCall(t *testing.T, runtimeHost *processRuntime, method, params string) (any, *rpcError) {
	t.Helper()
	var result any
	var rpcErr *rpcError
	if !runtimeHost.serveRegisteredHostMethod(rpcEnvelope{Method: method, Params: json.RawMessage(params)}, func(value any, err *rpcError) {
		result, rpcErr = value, err
	}) {
		t.Fatalf("%s is not registered", method)
	}
	return result, rpcErr
}

func backupResult(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("result %s: %v", raw, err)
	}
	return out
}

// answerBackupNext plays the frontend: it waits for the next backup request
// and replies with whatever reply returns (marshalled as the IPC args).
func answerBackupNext(t *testing.T, events *capturedUIEvents, reply func(event backupRequestEvent) map[string]any) {
	t.Helper()
	event, ok := nextBackupRequest(events, 2*time.Second)
	if !ok {
		t.Error("no backup request event")
		return
	}
	args := reply(event)
	args["id"] = event.ID
	raw, _ := json.Marshal(args)
	if _, err := pluginHostBackupReplyHandler("", string(raw)); err != nil {
		t.Errorf("reply: %v", err)
	}
}

func nextBackupRequest(events *capturedUIEvents, wait time.Duration) (backupRequestEvent, bool) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		events.mu.Lock()
		for i, captured := range events.events {
			if captured.name == pluginBackupRequestEvent {
				event := captured.data.(backupRequestEvent)
				events.events = append(events.events[:i], events.events[i+1:]...)
				events.mu.Unlock()
				return event, true
			}
		}
		events.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return backupRequestEvent{}, false
}

func collectReply(values map[string]any) func(backupRequestEvent) map[string]any {
	return func(event backupRequestEvent) map[string]any {
		return map[string]any{"result": map[string]any{"values": values}}
	}
}

func TestBackupCapabilityRequired(t *testing.T) {
	useBackupHost(t)
	manager, _, _ := pluginDataTestManager()
	without := uiRuntime("data-backup")
	without.capabilities = map[api.Capability]struct{}{api.CapabilityStorage: {}}
	if _, err := backupCall(t, without, "host.backup.status", `{}`); err == nil || err.Code != -32001 || !strings.Contains(err.Message, "backup") {
		t.Fatalf("status without capability: %v", err)
	}
	if result, err := backupCall(t, backupRuntime("data-backup", manager), "host.backup.status", `{}`); err != nil {
		t.Fatalf("status with capability: %v", err)
	} else if got := backupResult(t, result); got["passwordSet"] != false || got["remembered"] != false || got["includeSecrets"] != false {
		t.Fatalf("status %v", got)
	}
	want := []string{"host.backup.applyRestore", "host.backup.close", "host.backup.create", "host.backup.open", "host.backup.prepareRestore",
		"host.backup.read", "host.backup.release", "host.backup.setOptions", "host.backup.setPassword",
		"host.backup.stage.append", "host.backup.stage.begin", "host.backup.stage.commit", "host.backup.status"}
	got := []string{}
	for _, registration := range registeredHostMethods() {
		if !strings.HasPrefix(registration.name, "host.backup.") {
			continue
		}
		got = append(got, registration.name)
		if registration.capability != api.CapabilityBackup {
			t.Errorf("%s capability %q", registration.name, registration.capability)
		}
	}
	for _, name := range want {
		if !slices.Contains(got, name) {
			t.Errorf("%s not registered (have %v)", name, got)
		}
	}
}

func TestBackupNotAnActionPermission(t *testing.T) {
	declared := []api.Capability{api.CapabilityBackup, api.CapabilityStorage}
	got, err := resolveAIActionPermissions(ManifestUIAction{ID: "probe", Risk: "read"}, declared)
	if err != nil || slices.Contains(got.Permissions, api.CapabilityBackup) {
		t.Fatalf("inherit kept backup: %+v %v", got, err)
	}
	manifest := &Manifest{ID: "backup.probe", APIVersion: 2, Capabilities: declared}
	manifest.Runtime.Type = "process"
	manifest.Contributes.Actions = []ManifestUIAction{{ID: "run", Risk: "dangerous", UICallable: true, Permissions: []api.Capability{api.CapabilityBackup}}}
	if err := validateContributions(newPluginPathResolver(t.TempDir()), manifest); err == nil || !strings.Contains(err.Error(), "not an action permission") {
		t.Fatalf("explicit backup permission accepted: %v", err)
	}
}

func TestBackupCapabilityRejectsAICallableActions(t *testing.T) {
	build := func(capabilities []api.Capability, aiCallable bool) *Manifest {
		manifest := &Manifest{ID: "backup.probe", APIVersion: 2, Capabilities: capabilities}
		manifest.Runtime.Type = "process"
		manifest.Contributes.Actions = []ManifestUIAction{
			{ID: "page", Risk: "read", UICallable: true},
			{ID: "ask", Risk: "read", AICallable: aiCallable},
		}
		return manifest
	}
	err := validateContributions(newPluginPathResolver(t.TempDir()), build([]api.Capability{api.CapabilityBackup, api.CapabilityUI}, true))
	if err == nil || !strings.Contains(err.Error(), "backup") || !strings.Contains(err.Error(), "aiCallable") || !strings.Contains(err.Error(), `"ask"`) {
		t.Fatalf("backup with aiCallable action accepted or unclear: %v", err)
	}
	if err := validateContributions(newPluginPathResolver(t.TempDir()), build([]api.Capability{api.CapabilityBackup, api.CapabilityUI}, false)); err != nil {
		t.Fatalf("backup without aiCallable rejected: %v", err)
	}
	if err := validateContributions(newPluginPathResolver(t.TempDir()), build([]api.Capability{api.CapabilityUI}, true)); err != nil {
		t.Fatalf("aiCallable without backup rejected: %v", err)
	}
}

func TestPasswordTooShortRejected(t *testing.T) {
	events := useBackupHost(t)
	manager, _, _ := pluginDataTestManager()
	caller := backupRuntime("data-backup", manager)

	go answerBackupNext(t, events, func(event backupRequestEvent) map[string]any {
		payload, _ := json.Marshal(event.Payload)
		if event.Kind != backupKindPassword || string(payload) != `{"mode":"set"}` || event.PluginID != "data-backup" || event.PluginName != "Notes Plugin" {
			t.Errorf("password request %+v %s", event, payload)
		}
		return map[string]any{"result": map[string]any{"password": "short", "remember": true}}
	})
	if _, err := backupCall(t, caller, "host.backup.setPassword", `{}`); err == nil || err.Code != -32602 || err.Message != backup.ErrPasswordTooShort.Error() {
		t.Fatalf("short password: %v", err)
	}
	if status := backupResult(t, must(backupCall(t, caller, "host.backup.status", `{}`))); status["passwordSet"] != false {
		t.Fatalf("short password stored: %v", status)
	}

	// Cancelled: -32800, nothing set.
	go answerBackupNext(t, events, func(backupRequestEvent) map[string]any { return map[string]any{"cancelled": true} })
	if _, err := backupCall(t, caller, "host.backup.setPassword", `{}`); err == nil || err.Code != -32800 {
		t.Fatalf("cancelled: %v", err)
	}

	// Not remembered: usable for this run only.
	go answerBackupNext(t, events, func(backupRequestEvent) map[string]any {
		return map[string]any{"result": map[string]any{"password": backupTestPassword, "remember": false}}
	})
	result, err := backupCall(t, caller, "host.backup.setPassword", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := backupResult(t, result); got["set"] != true || got["remembered"] != false {
		t.Fatalf("setPassword %v", got)
	}
	if status := backupResult(t, must(backupCall(t, caller, "host.backup.status", `{}`))); status["passwordSet"] != true || status["remembered"] != false {
		t.Fatalf("status after session password: %v", status)
	}
	caller.generation++ // the plugin restarted
	if status := backupResult(t, must(backupCall(t, caller, "host.backup.status", `{}`))); status["passwordSet"] != false {
		t.Fatalf("session password survived a restart: %v", status)
	}

	// Remembered.
	go answerBackupNext(t, events, func(backupRequestEvent) map[string]any {
		return map[string]any{"result": map[string]any{"password": backupTestPassword, "remember": true}}
	})
	if _, err := backupCall(t, caller, "host.backup.setPassword", `{}`); err != nil {
		t.Fatal(err)
	}
	if status := backupResult(t, must(backupCall(t, caller, "host.backup.status", `{}`))); status["passwordSet"] != true || status["remembered"] != true {
		t.Fatalf("status after remembered password: %v", status)
	}
}

func must(value any, err *rpcError) any {
	if err != nil {
		panic(err.Message)
	}
	return value
}

func TestBackupRequestRepliesAreValidated(t *testing.T) {
	events := useBackupHost(t)
	caller := backupRuntime("data-backup", nil)
	done := make(chan *rpcError, 1)
	go func() {
		_, err := backupCall(t, caller, "host.backup.setPassword", `{}`)
		done <- err
	}()
	event, ok := nextBackupRequest(events, 2*time.Second)
	if !ok {
		t.Fatal("no request")
	}
	for name, args := range map[string]string{
		"wrong type":      `{"id":"` + event.ID + `","result":{"password":5,"remember":true}}`,
		"missing":         `{"id":"` + event.ID + `","result":{"remember":true}}`,
		"remember string": `{"id":"` + event.ID + `","result":{"password":"x","remember":"yes"}}`,
		"not an object":   `{"id":"` + event.ID + `","result":[]}`,
		"unknown id":      `{"id":"backup-0","result":{"password":"x","remember":true}}`,
		"invalid json":    `{"id":`,
		"too large":       `{"id":"` + event.ID + `","result":"` + strings.Repeat("a", backupReplyMaxBytes) + `"}`,
	} {
		if _, err := pluginHostBackupReplyHandler("", args); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A second request while one is open is busy.
	if _, err := backupCall(t, caller, "host.backup.setPassword", `{}`); err == nil || err.Code != -32004 {
		t.Fatalf("concurrent request: %v", err)
	}
	// Unknown fields are ignored.
	if _, err := pluginHostBackupReplyHandler("", `{"id":"`+event.ID+`","result":{"password":"`+backupTestPassword+`","remember":false,"extra":1}}`); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := pluginHostBackupReplyHandler("", `{"id":"`+event.ID+`","cancelled":true}`); err == nil {
		t.Fatal("answered twice")
	}

	// Collect replies: storage must be plain or secure, values an object.
	for name, result := range map[string]string{
		"bad storage": `{"values":{"app_x":{"storage":"disk","value":1}}}`,
		"no value":    `{"values":{"app_x":{"storage":"plain"}}}`,
		"values list": `{"values":[]}`,
	} {
		if _, err := decodeBackupCollectReply(json.RawMessage(result)); err == nil {
			t.Errorf("collect %s accepted", name)
		}
	}
	if got, err := decodeBackupCollectReply(json.RawMessage(`{"values":{"app_x":{"storage":"secure","value":null,"x":1}},"extra":true}`)); err != nil || len(got) != 1 {
		t.Fatalf("collect: %v %v", got, err)
	}

	// A frontend error becomes an rpc error for the plugin.
	go answerBackupNext(t, events, func(backupRequestEvent) map[string]any { return map[string]any{"error": "decrypt failed"} })
	if _, err := backupCall(t, caller, "host.backup.setPassword", `{}`); err == nil || err.Code != -32000 || !strings.Contains(err.Message, "decrypt failed") {
		t.Fatalf("frontend error: %v", err)
	}
}

func TestBackupRequestCancelledWhenPluginStops(t *testing.T) {
	events := useBackupHost(t)
	caller := backupRuntime("data-backup", nil)
	ctx, cancel := contextWithCancelForTest()
	caller.runContext = ctx
	done := make(chan *rpcError, 1)
	go func() {
		_, err := backupCall(t, caller, "host.backup.setPassword", `{}`)
		done <- err
	}()
	event, ok := nextBackupRequest(events, 2*time.Second)
	if !ok {
		t.Fatal("no request")
	}
	cancel()
	if err := <-done; err == nil || err.Code != -32800 {
		t.Fatalf("stopped plugin: %v", err)
	}
	cancelled := events.last(t, pluginBackupRequestCancelEvent).(map[string]string)
	if cancelled["id"] != event.ID {
		t.Fatalf("cancel event %v", cancelled)
	}
}

func TestBackupOptionsPersistPerPlugin(t *testing.T) {
	useBackupHost(t)
	caller := backupRuntime("data-backup", nil)
	if _, err := backupCall(t, caller, "host.backup.setOptions", `{"includeSecrets":true}`); err != nil {
		t.Fatal(err)
	}
	for _, params := range []string{`{}`, `{"includeSecrets":"yes"}`, `{"includeSecrets":true,"other":1}`} {
		if _, err := backupCall(t, caller, "host.backup.setOptions", params); err == nil || err.Code != -32602 {
			t.Errorf("setOptions %s: %v", params, err)
		}
	}
	if status := backupResult(t, must(backupCall(t, caller, "host.backup.status", `{}`))); status["includeSecrets"] != true {
		t.Fatalf("status %v", status)
	}
	if status := backupResult(t, must(backupCall(t, backupRuntime("other", nil), "host.backup.status", `{}`))); status["includeSecrets"] != false {
		t.Fatalf("options leaked: %v", status)
	}
}

func TestCreateRequiresPassword(t *testing.T) {
	useBackupHost(t)
	caller := backupRuntime("data-backup", nil)
	_, err := backupCall(t, caller, "host.backup.create", `{}`)
	if err == nil || err.Code != -32010 {
		t.Fatalf("create without password: %v", err)
	}
	if data, _ := json.Marshal(err.Data); string(data) != `{"code":"passwordRequired"}` {
		t.Fatalf("data %s", data)
	}
}

// readStaged reads a staged backup through host.backup.read in small chunks.
func readStaged(t *testing.T, caller *processRuntime, token string) []byte {
	t.Helper()
	var out []byte
	for i := 0; i < 10000; i++ {
		params, _ := json.Marshal(map[string]any{"token": token, "offset": len(out), "length": 64 << 10})
		result, err := backupCall(t, caller, "host.backup.read", string(params))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		chunk := backupResult(t, result)
		data, decodeErr := base64.StdEncoding.DecodeString(chunk["data"].(string))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		out = append(out, data...)
		if chunk["eof"] == true {
			return out
		}
	}
	t.Fatal("read never reached eof")
	return nil
}

func TestCreateUnchangedWhenSameContent(t *testing.T) {
	events := useBackupHost(t)
	useTestSecretCipher(t)
	mustSetPluginKV(t, dataTestPlugin, "notes", `{"count":2}`)
	previousVersion := currentAppVersion()
	SetAppVersion("9.9.9")
	t.Cleanup(func() { SetAppVersion(previousVersion) })
	manager, _, _ := pluginDataTestManager()
	caller := backupRuntime("data-backup", manager)
	if err := backupRememberPassword([]byte(backupTestPassword)); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{
		"app_settings_v2":        map[string]any{"storage": "plain", "value": map[string]any{"theme": "dark"}},
		"app_ssh_connections_v1": map[string]any{"storage": "plain", "value": []any{map[string]any{"id": "s1", "host": "h"}}},
	}

	go answerBackupNext(t, events, func(event backupRequestEvent) map[string]any {
		payload, _ := json.Marshal(event.Payload)
		if event.Kind != backupKindCollect || string(payload) != `{"includeSecrets":false}` {
			t.Errorf("collect request %+v %s", event, payload)
		}
		return collectReply(values)(event)
	})
	result, err := backupCall(t, caller, "host.backup.create", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	first := backupResult(t, result)
	if !regexp.MustCompile(`^mincode-backup-\d{8}T\d{6}Z-[0-9a-f]{8}\.mcbak$`).MatchString(first["name"].(string)) {
		t.Fatalf("name %v", first["name"])
	}
	meta := first["meta"].(map[string]any)
	if meta["format"] != "mincode-backup" || meta["version"] != float64(1) || meta["includesSecrets"] != false ||
		meta["size"] != first["size"] || meta["sha256"] != first["sha256"] || meta["appVersion"] != "9.9.9" || meta["createdAt"] == "" {
		t.Fatalf("meta %v / %v", meta, first)
	}
	for _, field := range []string{"sections", "contentSha256", "plugins"} {
		if _, leaked := meta[field]; leaked {
			t.Fatalf("meta carries %s: %v", field, meta)
		}
	}
	contentHash, _ := first["contentSha256"].(string)
	if len(contentHash) != 64 {
		t.Fatalf("contentSha256 %v", first)
	}

	// Same data again: unchanged, no new file, no plaintext.
	go answerBackupNext(t, events, collectReply(values))
	result, err = backupCall(t, caller, "host.backup.create", `{"lastContentSha256":"`+contentHash+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if again := backupResult(t, result); again["unchanged"] != true || again["contentSha256"] != contentHash || again["token"] != nil {
		t.Fatalf("unchanged %v", again)
	}

	// Changed data: a new backup even though lastContentSha256 is given.
	values["app_settings_v2"] = map[string]any{"storage": "plain", "value": map[string]any{"theme": "light"}}
	go answerBackupNext(t, events, collectReply(values))
	result, err = backupCall(t, caller, "host.backup.create", `{"lastContentSha256":"`+contentHash+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if changed := backupResult(t, result); changed["unchanged"] != nil || changed["token"] == nil || changed["contentSha256"] == contentHash {
		t.Fatalf("changed %v", changed)
	}

	// The staged file decrypts to the collected data.
	raw := readStaged(t, caller, first["token"].(string))
	manifest, opened, openErr := backup.Open(bytes.NewReader(raw), []byte(backupTestPassword))
	if openErr != nil {
		t.Fatal(openErr)
	}
	got := map[string]string{}
	for _, entry := range opened {
		got[entry.Path] = string(entry.Data)
	}
	if got["app/app_settings_v2.json"] != `{"storage":"plain","value":{"theme":"dark"}}` || got["plugins/"+dataTestPlugin+"/kv.json"] == "" {
		t.Fatalf("entries %v", got)
	}
	ids := []string{}
	for _, section := range manifest.Sections {
		ids = append(ids, section.ID)
	}
	if strings.Join(ids, ",") != "settings,ssh,plugin:"+dataTestPlugin || manifest.ContentSHA256 != contentHash {
		t.Fatalf("manifest %+v", manifest)
	}

	// Released tokens are gone; other plugins never see them.
	token := first["token"].(string)
	if _, err := backupCall(t, backupRuntime("other", nil), "host.backup.read", `{"token":"`+token+`","offset":0,"length":10}`); err == nil || err.Code != -32002 {
		t.Fatalf("other plugin read: %v", err)
	}
	if result, err := backupCall(t, caller, "host.backup.release", `{"token":"`+token+`"}`); err != nil || result != true {
		t.Fatalf("release %v %v", result, err)
	}
	if _, err := backupCall(t, caller, "host.backup.read", `{"token":"`+token+`","offset":0,"length":10}`); err == nil || err.Code != -32002 {
		t.Fatalf("read after release: %v", err)
	}
}

// buildBackupEntriesForTest runs the same entry building as create, feeding
// it the given frontend values directly.
func buildBackupEntriesForTest(t *testing.T, caller *processRuntime, values map[string]any) ([]backup.Entry, []backup.SectionSummary, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"values": values})
	collected, err := decodeBackupCollectReply(raw)
	if err != nil {
		return nil, nil, err
	}
	return buildBackupEntries(caller.ownerManager, collected, false)
}

func TestCreateFiltersFrontendKeys(t *testing.T) {
	useBackupHost(t)
	entries, sections, err := buildBackupEntriesForTest(t, backupRuntime("data-backup", &pluginManager{records: map[string]*pluginRecord{}}), map[string]any{
		"app_settings_v2":  map[string]any{"storage": "plain", "value": 1},
		"app_db_conns":     map[string]any{"storage": "secure", "value": "x"},
		"app_license":      map[string]any{"storage": "plain", "value": "L"},
		"mincode_license":  map[string]any{"storage": "plain", "value": "L"},
		"app_lock_v1":      map[string]any{"storage": "plain", "value": "L"},
		"other_key":        map[string]any{"storage": "plain", "value": 1},
		"app_../escape":    map[string]any{"storage": "plain", "value": 1},
		"app_with space":   map[string]any{"storage": "plain", "value": 1},
		"app_workspaces_1": map[string]any{"storage": "plain", "value": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	slices.Sort(paths)
	if strings.Join(paths, ",") != "app/app_db_conns.json,app/app_settings_v2.json,app/app_workspaces_1.json" {
		t.Fatalf("paths %v", paths)
	}
	ids := []string{}
	for _, section := range sections {
		ids = append(ids, section.ID)
		if section.Items != 1 || section.Bytes <= 0 || section.Title != section.ID {
			t.Errorf("section %+v", section)
		}
	}
	if strings.Join(ids, ",") != "settings,database,workspaces" {
		t.Fatalf("sections %v", ids)
	}
}

func TestCreateNeverExposesPlaintextToPlugin(t *testing.T) {
	events := useBackupHost(t)
	useTestSecretCipher(t)
	const frontendSecret = "sk-frontend-SECRET-7f3a91"
	const pluginSecret = "plugin-secret-VALUE-0c44"
	const connectionHost = "db.internal.example"
	mustSetSecret(t, dataTestPlugin, "token", pluginSecret)
	mustSetPluginKV(t, dataTestPlugin, "notes", `{"host":"`+connectionHost+`"}`)
	manager, _, _ := pluginDataTestManager()
	caller := backupRuntime("data-backup", manager)
	if _, err := backupCall(t, caller, "host.backup.setOptions", `{"includeSecrets":true}`); err != nil {
		t.Fatal(err)
	}
	go answerBackupNext(t, events, func(backupRequestEvent) map[string]any {
		return map[string]any{"result": map[string]any{"password": backupTestPassword, "remember": false}}
	})
	if _, err := backupCall(t, caller, "host.backup.setPassword", `{}`); err != nil {
		t.Fatal(err)
	}
	go answerBackupNext(t, events, func(event backupRequestEvent) map[string]any {
		payload, _ := json.Marshal(event.Payload)
		if string(payload) != `{"includeSecrets":true}` {
			t.Errorf("collect payload %s", payload)
		}
		return collectReply(map[string]any{
			"app_settings_v2":    map[string]any{"storage": "plain", "value": map[string]any{"apiKey": frontendSecret}},
			"app_db_connections": map[string]any{"storage": "secure", "value": []any{map[string]any{"id": "c1", "host": connectionHost}}},
		})(event)
	})
	result, err := backupCall(t, caller, "host.backup.create", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, _ := json.Marshal(result)
	raw := readStaged(t, caller, backupResult(t, result)["token"].(string))
	for _, secret := range []string{frontendSecret, pluginSecret, connectionHost, backupTestPassword} {
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(resultJSON, []byte(secret)) {
			t.Fatalf("plaintext %q reached the plugin", secret)
		}
	}
	if bytes.Contains(resultJSON, []byte(dataTestPlugin)) || bytes.Contains(resultJSON, []byte("app_settings")) {
		t.Fatalf("result names contents: %s", resultJSON)
	}
	// The secrets are in the backup, encrypted.
	_, entries, openErr := backup.Open(bytes.NewReader(raw), []byte(backupTestPassword))
	if openErr != nil {
		t.Fatal(openErr)
	}
	joined := []byte{}
	for _, entry := range entries {
		joined = append(joined, entry.Data...)
	}
	for _, secret := range []string{frontendSecret, pluginSecret} {
		if !bytes.Contains(joined, []byte(secret)) {
			t.Fatalf("backup lost %q", secret)
		}
	}
}

func TestBackupStageUploadRoundTrip(t *testing.T) {
	useBackupHost(t)
	caller := backupRuntime("data-backup", nil)
	result, err := backupCall(t, caller, "host.backup.stage.begin", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	token := backupResult(t, result)["token"].(string)
	body := []byte("encrypted bytes from a remote")
	params, _ := json.Marshal(map[string]any{"token": token, "offset": 0, "data": base64.StdEncoding.EncodeToString(body[:10])})
	if result, err := backupCall(t, caller, "host.backup.stage.append", string(params)); err != nil || backupResult(t, result)["size"] != float64(10) {
		t.Fatalf("append %v %v", result, err)
	}
	params, _ = json.Marshal(map[string]any{"token": token, "offset": 3, "data": base64.StdEncoding.EncodeToString(body[10:])})
	if _, err := backupCall(t, caller, "host.backup.stage.append", string(params)); err == nil || err.Code != -32602 {
		t.Fatalf("gap accepted: %v", err)
	}
	// A refused chunk leaves the upload unusable; start over on a fresh token.
	result, _ = backupCall(t, caller, "host.backup.stage.begin", `{}`)
	token = backupResult(t, result)["token"].(string)
	params, _ = json.Marshal(map[string]any{"token": token, "offset": 0, "data": base64.StdEncoding.EncodeToString(body)})
	if _, err := backupCall(t, caller, "host.backup.stage.append", string(params)); err != nil {
		t.Fatal(err)
	}
	if _, err := backupCall(t, caller, "host.backup.stage.append", `{"token":"`+token+`","offset":0,"data":"***"}`); err == nil || err.Code != -32602 {
		t.Fatalf("bad base64: %v", err)
	}
	if _, err := backupCall(t, caller, "host.backup.stage.commit", `{"token":"`+token+`","sha256":"`+strings.Repeat("0", 64)+`"}`); err == nil {
		t.Fatal("wrong checksum committed")
	}
	result, _ = backupCall(t, caller, "host.backup.stage.begin", `{}`)
	token = backupResult(t, result)["token"].(string)
	params, _ = json.Marshal(map[string]any{"token": token, "offset": 0, "data": base64.StdEncoding.EncodeToString(body)})
	if _, err := backupCall(t, caller, "host.backup.stage.append", string(params)); err != nil {
		t.Fatal(err)
	}
	sum := sha256Hex(body)
	if result, err := backupCall(t, caller, "host.backup.stage.commit", `{"token":"`+token+`","sha256":"`+sum+`"}`); err != nil || backupResult(t, result)["token"] != token {
		t.Fatalf("commit %v %v", result, err)
	}
	if got := readStaged(t, caller, token); !bytes.Equal(got, body) {
		t.Fatalf("read back %q", got)
	}
	if _, err := backupCall(t, caller, "host.backup.read", `{"token":"`+token+`","offset":0,"length":0}`); err == nil || err.Code != -32602 {
		t.Fatalf("zero length: %v", err)
	}
	if _, err := backupCall(t, caller, "host.backup.release", `{}`); err == nil || err.Code != -32602 {
		t.Fatalf("release without token: %v", err)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func contextWithCancelForTest() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
