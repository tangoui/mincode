package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/backend/backup"
)

var backupHex32Pattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// backupStep plays the frontend for one request.
type backupStep func(event backupRequestEvent) map[string]any

// runBackupCall calls method and answers the frontend requests it makes with
// steps, in order. It fails the test when the call makes more requests than
// steps or finishes before all of them were used, and returns the requests.
func runBackupCall(t *testing.T, events *capturedUIEvents, caller *processRuntime, method, params string, steps ...backupStep) (any, *rpcError, []backupRequestEvent) {
	t.Helper()
	type outcome struct {
		result any
		err    *rpcError
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := backupCall(t, caller, method, params)
		done <- outcome{result, err}
	}()
	var seen []backupRequestEvent
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case got := <-done:
			if len(seen) < len(steps) {
				t.Errorf("%s finished after %d of %d frontend requests (%v)", method, len(seen), len(steps), got.err)
			}
			return got.result, got.err, seen
		default:
		}
		event, ok := nextBackupRequest(events, 10*time.Millisecond)
		if !ok {
			continue
		}
		seen = append(seen, event)
		args := map[string]any{"cancelled": true}
		if len(seen) > len(steps) {
			t.Errorf("%s: unexpected %s request", method, event.Kind)
		} else {
			args = steps[len(seen)-1](event)
		}
		if args == nil {
			continue // the step leaves the request unanswered
		}
		args["id"] = event.ID
		raw, _ := json.Marshal(args)
		if _, err := pluginHostBackupReplyHandler("", string(raw)); err != nil {
			t.Errorf("reply to %s: %v", event.Kind, err)
		}
	}
	t.Fatalf("%s did not finish", method)
	return nil, nil, nil
}

func requestKinds(events []backupRequestEvent) string {
	kinds := []string{}
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return strings.Join(kinds, ",")
}

func payloadJSON(event backupRequestEvent) string {
	raw, _ := json.Marshal(event.Payload)
	return string(raw)
}

func appEntry(key, storage, value string) backup.Entry {
	return backup.Entry{Path: "app/" + key + ".json", Data: []byte(`{"storage":"` + storage + `","value":` + value + `}`)}
}

// stageTestBackup seals entries with password into owner's staging area.
func stageTestBackup(t *testing.T, owner, password string, includesSecrets bool, entries []backup.Entry) string {
	t.Helper()
	token, _, _, err := backupStaging().Create(owner, func(w io.Writer) error {
		return backup.Seal(w, []byte(password), backup.Manifest{CreatedAt: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
			AppVersion: "1.2.3", Device: "old-laptop", IncludesSecrets: includesSecrets}, entries)
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func passwordStep(password string, remember bool) backupStep {
	return func(event backupRequestEvent) map[string]any {
		return map[string]any{"result": map[string]any{"password": password, "remember": remember}}
	}
}

func confirmStep(confirmed bool) backupStep {
	return func(event backupRequestEvent) map[string]any {
		return map[string]any{"result": map[string]any{"confirmed": confirmed}}
	}
}

func applyOKStep(check func(payload string)) backupStep {
	return func(event backupRequestEvent) map[string]any {
		if check != nil {
			check(payloadJSON(event))
		}
		return map[string]any{"result": map[string]any{"ok": true}}
	}
}

func rpcCode(err *rpcError) string {
	if err == nil {
		return ""
	}
	data, _ := err.Data.(map[string]string)
	return data["code"]
}

func stagingFileCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(userDataDir(), appDataDirectory, backupStagingDirectory))
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// restoreFixture is an opened backup over a device with local data.
type restoreFixture struct {
	events    *capturedUIEvents
	manager   *pluginManager
	caller    *processRuntime
	sessionID string
	local     map[string]any
}

func (f *restoreFixture) collectStep(t *testing.T) backupStep {
	return func(event backupRequestEvent) map[string]any {
		if event.Kind != backupKindCollect || payloadJSON(event) != `{"includeSecrets":true}` {
			t.Errorf("collect request %s %s", event.Kind, payloadJSON(event))
		}
		return map[string]any{"result": map[string]any{"values": f.local}}
	}
}

func (f *restoreFixture) prepare(t *testing.T, sections string, steps ...backupStep) (map[string]any, *rpcError, []backupRequestEvent) {
	t.Helper()
	result, err, seen := runBackupCall(t, f.events, f.caller, "host.backup.prepareRestore",
		`{"sessionId":"`+f.sessionID+`","sections":`+sections+`,"mode":"overwrite"}`, steps...)
	if err != nil {
		return nil, err, seen
	}
	return backupResult(t, result), nil, seen
}

func (f *restoreFixture) prepareConfirmed(t *testing.T) map[string]any {
	t.Helper()
	got, err, _ := f.prepare(t, `["settings","plugin:`+dataTestPlugin+`"]`, f.collectStep(t), confirmStep(true))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return got
}

// newRestoreFixture: local settings {theme: dark} plus app_settings_extra,
// plugin data.plugin KV notes=1; the backup has settings {theme: light},
// app_settings_new and notes=2.
func newRestoreFixture(t *testing.T, callerID string) *restoreFixture {
	t.Helper()
	events := useBackupHost(t)
	useTestSecretCipher(t)
	mustSetPluginKV(t, dataTestPlugin, "notes", `1`)
	mustSetSecret(t, dataTestPlugin, "token", "local-plugin-secret")
	manager, _, _ := pluginDataTestManager()
	caller := backupRuntime(callerID, manager)
	if err := backupRememberPassword([]byte(backupTestPassword)); err != nil {
		t.Fatal(err)
	}
	token := stageTestBackup(t, callerID, backupTestPassword, false, append([]backup.Entry{
		appEntry("app_settings_v2", "plain", `{"theme":"light"}`),
		appEntry("app_settings_new", "plain", `"fresh"`),
		appEntry("app_ssh_connections_v1", "plain", `[{"id":"s1","host":"backup-host"}]`),
	}, append(pluginBackupEntries(dataTestPlugin, `{"notes":2}`, nil, "", ""),
		pluginBackupEntries("other.plugin", `{"x":1}`, nil, "", "")...)...))
	result, err, seen := runBackupCall(t, events, caller, "host.backup.open", `{"token":"`+token+`"}`)
	if err != nil || len(seen) != 0 {
		t.Fatalf("open: %v %v", err, requestKinds(seen))
	}
	return &restoreFixture{events: events, manager: manager, caller: caller, sessionID: backupResult(t, result)["sessionId"].(string),
		local: map[string]any{
			"app_settings_v2":        map[string]any{"storage": "plain", "value": map[string]any{"theme": "dark"}},
			"app_settings_extra":     map[string]any{"storage": "plain", "value": 1},
			"app_ssh_connections_v1": map[string]any{"storage": "plain", "value": []any{map[string]any{"id": "s9", "host": "local-host"}}},
		}}
}

func TestOpenPromptsOnlyWhenRememberedFails(t *testing.T) {
	events := useBackupHost(t)
	useTestSecretCipher(t)
	manager, _, _ := pluginDataTestManager()
	caller := backupRuntime("data-backup", manager)
	if err := backupRememberPassword([]byte(backupTestPassword)); err != nil {
		t.Fatal(err)
	}
	entries := append([]backup.Entry{
		appEntry("app_settings_v2", "plain", `{"theme":"light"}`),
		appEntry("app_ssh_connections_v1", "plain", `[{"id":"s1"}]`),
		appEntry("app_license", "plain", `"device-bound"`),
	}, append(pluginBackupEntries(dataTestPlugin, `{"notes":2}`, map[string]string{"a.txt": "A"}, "", ""),
		pluginBackupEntries("missing.plugin", `{"x":1}`, nil, "", "")...)...)
	token := stageTestBackup(t, "data-backup", backupTestPassword, false, entries)

	// The remembered password opens it without a prompt.
	result, err, seen := runBackupCall(t, events, caller, "host.backup.open", `{"token":"`+token+`"}`)
	if err != nil || len(seen) != 0 {
		t.Fatalf("open with remembered password: %v, requests %s", err, requestKinds(seen))
	}
	got := backupResult(t, result)
	firstSession, _ := got["sessionId"].(string)
	if !backupHex32Pattern.MatchString(firstSession) {
		t.Fatalf("sessionId %v", got["sessionId"])
	}
	meta := got["meta"].(map[string]any)
	if meta["format"] != "mincode-backup" || meta["version"] != float64(1) || meta["includesSecrets"] != false || meta["appVersion"] != "1.2.3" ||
		meta["device"] != "old-laptop" || meta["size"].(float64) <= 0 || !backupSHA256Pattern.MatchString(meta["sha256"].(string)) {
		t.Fatalf("meta %v", meta)
	}
	raw := readStaged(t, caller, token)
	if meta["sha256"] != sha256Hex(raw) || meta["size"] != float64(len(raw)) {
		t.Fatalf("meta does not describe the file: %v", meta)
	}
	sections := got["sections"].([]any)
	ids := []string{}
	for _, item := range sections {
		section := item.(map[string]any)
		ids = append(ids, section["id"].(string))
		switch section["id"] {
		case "settings", "ssh":
			if _, has := section["installed"]; has || section["items"] != float64(1) || section["bytes"].(float64) <= 0 {
				t.Errorf("core section %v", section)
			}
		case "plugin:" + dataTestPlugin:
			if section["installed"] != true || section["plugin"] != dataTestPlugin || section["title"] != "Test Plugin" {
				t.Errorf("installed plugin section %v", section)
			}
		case "plugin:missing.plugin":
			if section["installed"] != false || section["plugin"] != "missing.plugin" {
				t.Errorf("missing plugin section %v", section)
			}
		}
	}
	if strings.Join(ids, ",") != "settings,ssh,plugin:"+dataTestPlugin+",plugin:missing.plugin" {
		t.Fatalf("sections %v", ids)
	}

	// The remembered password no longer matches: one prompt; a wrong answer fails.
	if err := backupRememberPassword([]byte("a different long password")); err != nil {
		t.Fatal(err)
	}
	_, err, seen = runBackupCall(t, events, caller, "host.backup.open", `{"token":"`+token+`"}`, func(event backupRequestEvent) map[string]any {
		if event.Kind != backupKindPassword || payloadJSON(event) != `{"mode":"open","hasPassword":true}` {
			t.Errorf("prompt %s %s", event.Kind, payloadJSON(event))
		}
		return passwordStep("wrong password entered", true)(event)
	})
	if err == nil || err.Code != -32011 || rpcCode(err) != "wrongPassword" || len(seen) != 1 {
		t.Fatalf("wrong password: %v (%d prompts)", err, len(seen))
	}
	if strings.Contains(err.Message, "wrong password entered") {
		t.Fatalf("error echoes the password: %v", err.Message)
	}
	if remembered, _, _ := backupRememberedPassword(); string(remembered) != "a different long password" {
		t.Fatal("a wrong password was remembered")
	}
	// Cancelled prompt.
	_, err, _ = runBackupCall(t, events, caller, "host.backup.open", `{"token":"`+token+`"}`, func(backupRequestEvent) map[string]any {
		return map[string]any{"cancelled": true}
	})
	if err == nil || err.Code != -32800 {
		t.Fatalf("cancelled: %v", err)
	}
	// The right password, remembered.
	result, err, seen = runBackupCall(t, events, caller, "host.backup.open", `{"token":"`+token+`"}`, passwordStep(backupTestPassword, true))
	if err != nil || len(seen) != 1 {
		t.Fatalf("open after prompt: %v", err)
	}
	if remembered, _, _ := backupRememberedPassword(); string(remembered) != backupTestPassword {
		t.Fatal("password not remembered after a successful open")
	}
	secondSession := backupResult(t, result)["sessionId"].(string)
	if secondSession == firstSession {
		t.Fatal("session id reused")
	}
	// One session per plugin: the first one is gone.
	if _, err := backupCall(t, caller, "host.backup.prepareRestore", `{"sessionId":"`+firstSession+`","sections":["settings"],"mode":"merge"}`); err == nil || rpcCode(err) != "sessionExpired" {
		t.Fatalf("old session still usable: %v", err)
	}
	if result, err := backupCall(t, caller, "host.backup.close", `{"sessionId":"`+secondSession+`"}`); err != nil || result != true {
		t.Fatalf("close %v %v", result, err)
	}
	if _, err := backupCall(t, caller, "host.backup.prepareRestore", `{"sessionId":"`+secondSession+`","sections":["settings"],"mode":"merge"}`); err == nil || rpcCode(err) != "sessionExpired" {
		t.Fatalf("closed session usable: %v", err)
	}

	// Not a backup: invalidFormat, without a prompt.
	begin := backupResult(t, must(backupCall(t, caller, "host.backup.stage.begin", `{}`)))
	garbage := bytes.Repeat([]byte("not a backup "), 20)
	params, _ := json.Marshal(map[string]any{"token": begin["token"], "offset": 0, "data": base64.StdEncoding.EncodeToString(garbage)})
	must(backupCall(t, caller, "host.backup.stage.append", string(params)))
	must(backupCall(t, caller, "host.backup.stage.commit", `{"token":"`+begin["token"].(string)+`","sha256":"`+sha256Hex(garbage)+`"}`))
	_, err, seen = runBackupCall(t, events, caller, "host.backup.open", `{"token":"`+begin["token"].(string)+`"}`)
	if err == nil || err.Code != -32012 || rpcCode(err) != "invalidFormat" || len(seen) != 0 {
		t.Fatalf("garbage: %v", err)
	}
	// Unknown token, or another plugin's token.
	if _, err := backupCall(t, caller, "host.backup.open", `{"token":"nope"}`); err == nil || err.Code != -32002 {
		t.Fatalf("unknown token: %v", err)
	}
	if _, err := backupCall(t, backupRuntime("other", manager), "host.backup.open", `{"token":"`+token+`"}`); err == nil || err.Code != -32002 {
		t.Fatalf("other plugin's token: %v", err)
	}
	if _, err := backupCall(t, caller, "host.backup.open", `{}`); err == nil || err.Code != -32602 {
		t.Fatalf("missing token: %v", err)
	}
}

func TestOpenSessionEndsWithPluginGeneration(t *testing.T) {
	f := newRestoreFixture(t, "data-backup")
	f.caller.generation++
	if _, err := backupCall(t, f.caller, "host.backup.prepareRestore", `{"sessionId":"`+f.sessionID+`","sections":["settings"],"mode":"merge"}`); err == nil || rpcCode(err) != "sessionExpired" {
		t.Fatalf("session survived a restart: %v", err)
	}
}

func TestRestoreRequiresConfirm(t *testing.T) {
	f := newRestoreFixture(t, "data-backup")
	for name, params := range map[string]string{
		"unknown section": `{"sessionId":"` + f.sessionID + `","sections":["nope"],"mode":"merge"}`,
		"no sections":     `{"sessionId":"` + f.sessionID + `","sections":[],"mode":"merge"}`,
		"duplicate":       `{"sessionId":"` + f.sessionID + `","sections":["settings","settings"],"mode":"merge"}`,
		"bad mode":        `{"sessionId":"` + f.sessionID + `","sections":["settings"],"mode":"replace"}`,
		"extra field":     `{"sessionId":"` + f.sessionID + `","sections":["settings"],"mode":"merge","x":1}`,
	} {
		if _, err := backupCall(t, f.caller, "host.backup.prepareRestore", params); err == nil || err.Code != -32602 {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := backupCall(t, f.caller, "host.backup.prepareRestore", `{"sessionId":"`+strings.Repeat("0", 32)+`","sections":["settings"],"mode":"merge"}`); err == nil || err.Code != -32015 {
		t.Fatalf("unknown session: %v", err)
	}
	stagedBefore := stagingFileCount(t)

	var confirmPayload string
	_, err, seen := f.prepare(t, `["settings","plugin:`+dataTestPlugin+`"]`, f.collectStep(t), func(event backupRequestEvent) map[string]any {
		confirmPayload = payloadJSON(event)
		if event.Kind != backupKindRestoreConfirm {
			t.Errorf("confirm kind %s", event.Kind)
		}
		return confirmStep(false)(event)
	})
	if err == nil || err.Code != -32800 {
		t.Fatalf("declined: %v", err)
	}
	if requestKinds(seen) != backupKindCollect+","+backupKindRestoreConfirm {
		t.Fatalf("requests %s", requestKinds(seen))
	}
	var payload struct {
		Mode       string `json:"mode"`
		PluginName string `json:"pluginName"`
		Changes    []struct {
			Section      string `json:"section"`
			SectionTitle string `json:"sectionTitle"`
			Kind         string `json:"kind"`
			Label        string `json:"label"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(confirmPayload), &payload); err != nil {
		t.Fatal(err)
	}
	rows := []string{}
	for _, change := range payload.Changes {
		rows = append(rows, change.Section+"|"+change.SectionTitle+"|"+change.Kind+"|"+change.Label)
	}
	want := "settings|settings|add|app_settings_new;settings|settings|update|theme;settings|settings|remove|app_settings_extra;" +
		"plugin:" + dataTestPlugin + "|Test Plugin|update|kv/notes"
	if payload.Mode != "overwrite" || payload.PluginName != "Notes Plugin" || strings.Join(rows, ";") != want {
		t.Fatalf("confirm payload %s\nrows %s", confirmPayload, strings.Join(rows, ";"))
	}
	for _, value := range []string{"dark", "light", "fresh", "backup-host", "local-host", "local-plugin-secret"} {
		if strings.Contains(confirmPayload, value) {
			t.Fatalf("confirm payload carries the value %q: %s", value, confirmPayload)
		}
	}
	if got := kvContent(t, dataTestPlugin); got["notes"] != "1" {
		t.Fatalf("declined restore wrote plugin data: %v", got)
	}
	if stagingFileCount(t) != stagedBefore {
		t.Fatal("declined restore staged a snapshot")
	}

	// A cancelled dialog is the same.
	if _, err, _ := f.prepare(t, `["settings"]`, f.collectStep(t), func(backupRequestEvent) map[string]any {
		return map[string]any{"cancelled": true}
	}); err == nil || err.Code != -32800 {
		t.Fatalf("cancelled: %v", err)
	}
	if stagingFileCount(t) != stagedBefore {
		t.Fatal("cancelled restore staged a snapshot")
	}
}

func TestRestoreConfirmCapsChanges(t *testing.T) {
	changes := make([]backupConfirmChange, 0, 2105)
	for i := 0; i < 2105; i++ {
		changes = append(changes, backupConfirmChange{Section: "settings", SectionTitle: "settings", Kind: backup.ChangeAdd, Label: "k"})
	}
	capped := capBackupConfirmChanges(changes)
	if len(capped) != backupConfirmMaxChanges+1 || capped[len(capped)-1].Kind != "more" || capped[len(capped)-1].Label != "105" {
		t.Fatalf("capped %d %+v", len(capped), capped[len(capped)-1])
	}
	if got := capBackupConfirmChanges(changes[:3]); len(got) != 3 {
		t.Fatalf("uncapped %d", len(got))
	}
}

func TestPrepareRestoreProducesSnapshotBeforeApply(t *testing.T) {
	f := newRestoreFixture(t, "data-backup")
	got, err, seen := f.prepare(t, `["settings","plugin:`+dataTestPlugin+`"]`, f.collectStep(t), confirmStep(true))
	if err != nil {
		t.Fatal(err)
	}
	if requestKinds(seen) != backupKindCollect+","+backupKindRestoreConfirm {
		t.Fatalf("prepare wrote to the frontend: %s", requestKinds(seen))
	}
	if !backupHex32Pattern.MatchString(got["restoreId"].(string)) ||
		!regexp.MustCompile(`^mincode-backup-\d{8}T\d{6}Z-[0-9a-f]{8}\.mcbak$`).MatchString(got["preRestoreName"].(string)) {
		t.Fatalf("prepare result %v", got)
	}
	meta := got["preRestoreMeta"].(map[string]any)
	if meta["format"] != "mincode-backup" || meta["includesSecrets"] != true || meta["size"].(float64) <= 0 {
		t.Fatalf("snapshot meta %v", meta)
	}
	if got := kvContent(t, dataTestPlugin); got["notes"] != "1" {
		t.Fatalf("prepare changed plugin data: %v", got)
	}

	// The snapshot is this device's data, secrets included, sealed with the
	// remembered password.
	raw := readStaged(t, f.caller, got["preRestoreToken"].(string))
	if meta["sha256"] != sha256Hex(raw) {
		t.Fatal("snapshot meta sha256 mismatch")
	}
	manifest, entries, openErr := backup.Open(bytes.NewReader(raw), []byte(backupTestPassword))
	if openErr != nil {
		t.Fatal(openErr)
	}
	contents := map[string]string{}
	for _, entry := range entries {
		contents[entry.Path] = string(entry.Data)
	}
	if !manifest.IncludesSecrets || contents["app/app_settings_v2.json"] != `{"storage":"plain","value":{"theme":"dark"}}` ||
		contents["plugins/"+dataTestPlugin+"/kv.json"] != `{"notes":1}` || !strings.Contains(contents["plugins/"+dataTestPlugin+"/secrets.json"], "local-plugin-secret") {
		t.Fatalf("snapshot entries %v", contents)
	}
}

func TestPrepareRestoreSealsSnapshotWithOpenPassword(t *testing.T) {
	events := useBackupHost(t)
	useTestSecretCipher(t)
	manager, _, _ := pluginDataTestManager()
	caller := backupRuntime("data-backup", manager)
	token := stageTestBackup(t, "data-backup", backupTestPassword, false, []backup.Entry{appEntry("app_settings_v2", "plain", `{"theme":"light"}`)})
	result, err, _ := runBackupCall(t, events, caller, "host.backup.open", `{"token":"`+token+`"}`, passwordStep(backupTestPassword, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := backupRememberedPassword(); ok {
		t.Fatal("remembered without the box ticked")
	}
	f := &restoreFixture{events: events, manager: manager, caller: caller, sessionID: backupResult(t, result)["sessionId"].(string),
		local: map[string]any{"app_settings_v2": map[string]any{"storage": "plain", "value": map[string]any{"theme": "dark"}}}}
	got, err, _ := f.prepare(t, `["settings"]`, f.collectStep(t), confirmStep(true))
	if err != nil {
		t.Fatal(err)
	}
	raw := readStaged(t, caller, got["preRestoreToken"].(string))
	if _, _, err := backup.Open(bytes.NewReader(raw), []byte(backupTestPassword)); err != nil {
		t.Fatalf("snapshot not sealed with the open password: %v", err)
	}
}

func TestApplyRestoreIDSingleUseAndExpires(t *testing.T) {
	f := newRestoreFixture(t, "data-backup")
	prepared := f.prepareConfirmed(t)
	restoreID := prepared["restoreId"].(string)
	params := `{"restoreId":"` + restoreID + `"}`

	// Another plugin cannot use it.
	if _, err := backupCall(t, backupRuntime("other", f.manager), "host.backup.applyRestore", params); err == nil || err.Code != -32014 {
		t.Fatalf("other plugin: %v", err)
	}
	var applyPayload string
	result, err, seen := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", params, f.collectStep(t), applyOKStep(func(payload string) {
		applyPayload = payload
	}))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if requestKinds(seen) != backupKindCollect+","+backupKindApply {
		t.Fatalf("requests %s", requestKinds(seen))
	}
	if applyPayload != `{"sets":{"app_settings_new":{"storage":"plain","value":"fresh"},"app_settings_v2":{"storage":"plain","value":{"theme":"light"}}},"deletes":["app_settings_extra"]}` {
		t.Fatalf("apply payload %s", applyPayload)
	}
	got := backupResult(t, result)
	if restored, _ := json.Marshal(got["restored"]); string(restored) != `["settings","plugin:`+dataTestPlugin+`"]` || got["deferred"] != false {
		t.Fatalf("result %v", got)
	}
	if deferredSections, _ := json.Marshal(got["deferredSections"]); string(deferredSections) != `[]` {
		t.Fatalf("result %v", got)
	}
	if notInstalled, _ := json.Marshal(got["notInstalled"]); string(notInstalled) != `[]` {
		t.Fatalf("notInstalled %s", notInstalled)
	}
	if kv := kvContent(t, dataTestPlugin); kv["notes"] != "2" {
		t.Fatalf("plugin data not restored: %v", kv)
	}
	// The snapshot token is released once apply finished.
	if _, err := backupCall(t, f.caller, "host.backup.read", `{"token":"`+prepared["preRestoreToken"].(string)+`","offset":0,"length":10}`); err == nil || err.Code != -32002 {
		t.Fatalf("snapshot still staged: %v", err)
	}
	// Single use.
	_, err, seen = runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", params)
	if err == nil || err.Code != -32014 || rpcCode(err) != "restoreExpired" || len(seen) != 0 {
		t.Fatalf("second apply: %v", err)
	}
	if _, err := backupCall(t, f.caller, "host.backup.applyRestore", `{"restoreId":"`+strings.Repeat("a", 32)+`"}`); err == nil || err.Code != -32014 {
		t.Fatalf("unknown id: %v", err)
	}

	// A plugin restart ends it as well.
	prepared = f.prepareConfirmed(t)
	f.caller.generation++
	if _, err := backupCall(t, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`); err == nil || err.Code != -32014 {
		t.Fatalf("apply after restart: %v", err)
	}
	f.caller.generation--

	// Expires after 30 minutes.
	prepared = f.prepareConfirmed(t)
	start := time.Now()
	previousNow := backupNow
	backupNow = func() time.Time { return start.Add(31 * time.Minute) }
	t.Cleanup(func() { backupNow = previousNow })
	_, err, seen = runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`)
	if err == nil || err.Code != -32014 || len(seen) != 0 {
		t.Fatalf("expired apply: %v", err)
	}
	backupNow = previousNow

}

func TestApplyRestoreStopsWhenFrontendWriteFails(t *testing.T) {
	f := newRestoreFixture(t, "data-backup")
	prepared := f.prepareConfirmed(t)
	var pluginWrites atomic.Int64
	replacePluginDataHook = func(string) error { pluginWrites.Add(1); return nil }
	t.Cleanup(func() { replacePluginDataHook = nil })
	_, err, seen := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`,
		f.collectStep(t), func(backupRequestEvent) map[string]any { return map[string]any{"error": "quota exceeded"} })
	if err == nil || len(seen) != 2 {
		t.Fatalf("frontend failure: %v %s", err, requestKinds(seen))
	}
	if pluginWrites.Load() != 0 || kvContent(t, dataTestPlugin)["notes"] != "1" {
		t.Fatal("plugin data written after the frontend write failed")
	}
	if _, err := backupCall(t, f.caller, "host.backup.read", `{"token":"`+prepared["preRestoreToken"].(string)+`","offset":0,"length":10}`); err == nil || err.Code != -32002 {
		t.Fatalf("snapshot still staged after a failed apply: %v", err)
	}
}

func TestRestoreRollsBackFrontendWhenPluginsFail(t *testing.T) {
	f := newRestoreFixture(t, "data-backup")
	prepared := f.prepareConfirmed(t)
	replacePluginDataHook = func(id string) error {
		if id == dataTestPlugin {
			return errors.New("disk full at /private/secret/path")
		}
		return nil
	}
	t.Cleanup(func() { replacePluginDataHook = nil })
	var rollbackPayload string
	_, err, seen := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`,
		f.collectStep(t), applyOKStep(nil), applyOKStep(func(payload string) { rollbackPayload = payload }))
	if err == nil {
		t.Fatal("apply succeeded")
	}
	if requestKinds(seen) != backupKindCollect+","+backupKindApply+","+backupKindApply {
		t.Fatalf("requests %s", requestKinds(seen))
	}
	// Exactly the keys step 4 touched: changed and deleted keys get their old
	// values back, added keys are deleted.
	if rollbackPayload != `{"sets":{"app_settings_extra":{"storage":"plain","value":1},"app_settings_v2":{"storage":"plain","value":{"theme":"dark"}}},"deletes":["app_settings_new"]}` {
		t.Fatalf("rollback payload %s", rollbackPayload)
	}
	if !strings.Contains(err.Message, "put back") || strings.Contains(err.Message, "/private") || strings.Contains(err.Message, "dark") {
		t.Fatalf("error message %q", err.Message)
	}
	if kv := kvContent(t, dataTestPlugin); kv["notes"] != "1" {
		t.Fatalf("plugin data not rolled back: %v", kv)
	}
	if _, err := backupCall(t, f.caller, "host.backup.read", `{"token":"`+prepared["preRestoreToken"].(string)+`","offset":0,"length":10}`); err == nil || err.Code != -32002 {
		t.Fatalf("snapshot still staged: %v", err)
	}
}

func TestRestoreDefersCallerPlugin(t *testing.T) {
	f := newRestoreFixture(t, dataTestPlugin)
	got, err, _ := f.prepare(t, `["plugin:`+dataTestPlugin+`","plugin:other.plugin"]`, f.collectStep(t), confirmStep(true))
	if err != nil {
		t.Fatal(err)
	}
	result, err, seen := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+got["restoreId"].(string)+`"}`, f.collectStep(t))
	returned := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	if requestKinds(seen) != backupKindCollect {
		t.Fatalf("plugin-only restore wrote to the frontend: %s", requestKinds(seen))
	}
	out := backupResult(t, result)
	restored, _ := json.Marshal(out["restored"])
	notInstalled, _ := json.Marshal(out["notInstalled"])
	deferredSections, _ := json.Marshal(out["deferredSections"])
	if out["deferred"] != true || string(restored) != `["plugin:other.plugin"]` || string(notInstalled) != `["plugin:other.plugin"]` ||
		string(deferredSections) != `["plugin:`+dataTestPlugin+`"]` {
		t.Fatalf("result %v", out)
	}
	if kv := kvContent(t, "other.plugin"); kv["x"] != "1" {
		t.Fatalf("other plugin not restored: %v", kv)
	}
	if kv := kvContent(t, dataTestPlugin); kv["notes"] != "1" {
		t.Fatalf("caller's data written before the response: %v", kv)
	}
	deadline := time.Now().Add(5 * time.Second)
	for kvContent(t, dataTestPlugin)["notes"] != "2" {
		if time.Now().After(deadline) {
			t.Fatal("caller's data never restored")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if elapsed := time.Since(returned); elapsed < 400*time.Millisecond {
		t.Fatalf("deferred restore ran after %v", elapsed)
	}
	// Wait for the restart to settle before the temporary data goes away.
	waitForBackupDeferred(t)
}

func waitForBackupDeferred(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for backupDeferredRunning.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("deferred restore still running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRollbackNotBlockedByConcurrentBackupCall(t *testing.T) {
	f := newRestoreFixture(t, "data-backup")
	prepared := f.prepareConfirmed(t)
	slipCtx, cancelSlip := contextWithCancelForTest()
	slipDone := make(chan struct{})
	defer func() { cancelSlip(); <-slipDone }()
	var concurrent *rpcError
	replacePluginDataHook = func(id string) error {
		// While step 5 runs, the plugin's own backup calls are refused
		// before they reach the frontend.
		_, concurrent = backupCall(t, f.caller, "host.backup.setPassword", `{}`)
		// A request that slipped in earlier holds the plugin's slot.
		go func() {
			defer close(slipDone)
			_, _ = backupRequests.ask(slipCtx, f.caller, backupKindPassword, map[string]string{"mode": "set"}, decodeBackupPasswordReply, time.Minute)
		}()
		deadline := time.Now().Add(2 * time.Second)
		for {
			backupRequests.mu.Lock()
			_, held := backupRequests.byPlugin[f.caller.pluginID]
			backupRequests.mu.Unlock()
			if held || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		return errors.New("plugin write failed")
	}
	t.Cleanup(func() { replacePluginDataHook = nil })
	var rollbackPayload string
	_, err, seen := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`,
		f.collectStep(t), applyOKStep(nil),
		func(backupRequestEvent) map[string]any { return nil }, // the slipped-in password request stays open
		applyOKStep(func(payload string) { rollbackPayload = payload }))
	if concurrent == nil || concurrent.Code != -32004 || rpcCode(concurrent) != "restoreInProgress" {
		t.Fatalf("concurrent call during restore: %v", concurrent)
	}
	if requestKinds(seen) != backupKindCollect+","+backupKindApply+","+backupKindPassword+","+backupKindApply {
		t.Fatalf("requests %s", requestKinds(seen))
	}
	if err == nil || !strings.Contains(err.Message, "was put back") || rollbackPayload == "" {
		t.Fatalf("rollback blocked: %v (payload %q)", err, rollbackPayload)
	}
	// Once the restore is over, calls are accepted again.
	if _, err := backupCall(t, f.caller, "host.backup.status", `{}`); err != nil {
		t.Fatalf("status after restore: %v", err)
	}
}

func TestStep4TimeoutSendsRollback(t *testing.T) {
	f := newRestoreFixture(t, "data-backup")
	prepared := f.prepareConfirmed(t)
	previous := backupApplyTimeout
	backupApplyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { backupApplyTimeout = previous })
	var pluginWrites atomic.Int64
	replacePluginDataHook = func(string) error { pluginWrites.Add(1); return nil }
	t.Cleanup(func() { replacePluginDataHook = nil })
	// The plugin stopping must not cut step 4 short either.
	ctx, cancel := contextWithCancelForTest()
	f.caller.runContext = ctx
	var rollbackPayload string
	_, err, seen := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`,
		f.collectStep(t),
		func(backupRequestEvent) map[string]any { cancel(); return nil }, // step 4 never answers
		applyOKStep(func(payload string) { rollbackPayload = payload }))
	if requestKinds(seen) != backupKindCollect+","+backupKindApply+","+backupKindApply {
		t.Fatalf("requests %s", requestKinds(seen))
	}
	if rollbackPayload != `{"sets":{"app_settings_extra":{"storage":"plain","value":1},"app_settings_v2":{"storage":"plain","value":{"theme":"dark"}}},"deletes":["app_settings_new"]}` {
		t.Fatalf("rollback payload %s", rollbackPayload)
	}
	if err == nil || rpcCode(err) != "restoreUncertain" || !strings.Contains(err.Message, "uncertain") || !strings.Contains(err.Message, "rollback succeeded") {
		t.Fatalf("error %v", err)
	}
	if pluginWrites.Load() != 0 || kvContent(t, dataTestPlugin)["notes"] != "1" {
		t.Fatal("plugin data written after an uncertain frontend write")
	}

	// A step 4 the frontend explicitly refuses is not rolled back.
	f.caller.runContext = nil
	prepared = f.prepareConfirmed(t)
	_, err, seen = runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`,
		f.collectStep(t), func(backupRequestEvent) map[string]any { return map[string]any{"error": "quota exceeded"} })
	if err == nil || rpcCode(err) == "restoreUncertain" || len(seen) != 2 {
		t.Fatalf("explicit error: %v %s", err, requestKinds(seen))
	}
}

func TestDeferredRestoreCancelledOnRemoval(t *testing.T) {
	for _, reason := range []removalReason{removalReasonUninstall, removalReasonClearData} {
		t.Run(string(reason), func(t *testing.T) {
			f := newRestoreFixture(t, dataTestPlugin)
			got, err, _ := f.prepare(t, `["plugin:`+dataTestPlugin+`"]`, f.collectStep(t), confirmStep(true))
			if err != nil {
				t.Fatal(err)
			}
			result, err, _ := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+got["restoreId"].(string)+`"}`, f.collectStep(t))
			if err != nil || backupResult(t, result)["deferred"] != true {
				t.Fatalf("apply %v %v", result, err)
			}
			f.manager.runRecordRemovalHooks(dataTestPlugin, reason)
			waitForBackupDeferred(t)
			time.Sleep(backupDeferredRestoreDelay + 200*time.Millisecond)
			if kv := kvContent(t, dataTestPlugin); kv["notes"] != "1" {
				t.Fatalf("deferred restore ran after %s: %v", reason, kv)
			}
		})
	}
}

func TestExpiredSessionDropsEntries(t *testing.T) {
	f := newRestoreFixture(t, "data-backup")
	backupRestoreMu.Lock()
	session := backupOpenSessions[f.caller.pluginID]
	backupRestoreMu.Unlock()
	if session == nil || len(session.entries) == 0 {
		t.Fatal("no open session")
	}
	prepared := f.prepareConfirmed(t)
	start := time.Now()
	previousNow := backupNow
	backupNow = func() time.Time { return start.Add(31 * time.Minute) }
	t.Cleanup(func() { backupNow = previousNow })
	// Any backup call sweeps expired sessions and restores.
	if _, err := backupCall(t, f.caller, "host.backup.status", `{}`); err != nil {
		t.Fatal(err)
	}
	backupRestoreMu.Lock()
	_, sessionLeft := backupOpenSessions[f.caller.pluginID]
	_, recordLeft := backupRestoreRecords[f.caller.pluginID]
	entries, password := session.entries, session.password
	backupRestoreMu.Unlock()
	if sessionLeft || recordLeft || entries != nil || password != nil {
		t.Fatalf("expired session kept: session %v record %v entries %d password %d", sessionLeft, recordLeft, len(entries), len(password))
	}
	backupNow = previousNow
	if _, err := backupCall(t, f.caller, "host.backup.read", `{"token":"`+prepared["preRestoreToken"].(string)+`","offset":0,"length":10}`); err == nil || err.Code != -32002 {
		t.Fatalf("expired restore kept its snapshot: %v", err)
	}

	// Close drops the entries too.
	f2 := newRestoreFixture(t, "data-backup")
	backupRestoreMu.Lock()
	session = backupOpenSessions[f2.caller.pluginID]
	backupRestoreMu.Unlock()
	must(backupCall(t, f2.caller, "host.backup.close", `{"sessionId":"`+f2.sessionID+`"}`))
	backupRestoreMu.Lock()
	entries = session.entries
	backupRestoreMu.Unlock()
	if entries != nil {
		t.Fatal("closed session kept its entries")
	}
}

func restoreFinishedEvents(events *capturedUIEvents) []map[string]any {
	events.mu.Lock()
	defer events.mu.Unlock()
	var out []map[string]any
	for _, captured := range events.events {
		if captured.name == pluginBackupRestoreFinishedEvent {
			out = append(out, captured.data.(map[string]any))
		}
	}
	return out
}

func TestApplyRestoreEmitsFinished(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := newRestoreFixture(t, "data-backup")
		prepared := f.prepareConfirmed(t)
		restoreID := prepared["restoreId"].(string)
		var finishedDuringApply int
		_, err, _ := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+restoreID+`"}`,
			f.collectStep(t), applyOKStep(func(string) { finishedDuringApply = len(restoreFinishedEvents(f.events)) }))
		if err != nil {
			t.Fatal(err)
		}
		finished := restoreFinishedEvents(f.events)
		if finishedDuringApply != 0 || len(finished) != 1 || finished[0]["restoreId"] != restoreID || finished[0]["ok"] != true {
			t.Fatalf("finished events %v (during apply: %d)", finished, finishedDuringApply)
		}
	})
	t.Run("plugin failure after rollback", func(t *testing.T) {
		f := newRestoreFixture(t, "data-backup")
		prepared := f.prepareConfirmed(t)
		restoreID := prepared["restoreId"].(string)
		replacePluginDataHook = func(id string) error {
			if id == dataTestPlugin {
				return errors.New("disk full")
			}
			return nil
		}
		t.Cleanup(func() { replacePluginDataHook = nil })
		finishedAtRollback := -1
		_, err, seen := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+restoreID+`"}`,
			f.collectStep(t), applyOKStep(nil), applyOKStep(func(string) { finishedAtRollback = len(restoreFinishedEvents(f.events)) }))
		if err == nil || requestKinds(seen) != backupKindCollect+","+backupKindApply+","+backupKindApply {
			t.Fatalf("apply: %v %s", err, requestKinds(seen))
		}
		finished := restoreFinishedEvents(f.events)
		if finishedAtRollback != 0 || len(finished) != 1 || finished[0]["restoreId"] != restoreID || finished[0]["ok"] != false {
			t.Fatalf("finished events %v (at rollback: %d)", finished, finishedAtRollback)
		}
	})
	t.Run("not emitted before step 4", func(t *testing.T) {
		f := newRestoreFixture(t, "data-backup")
		prepared := f.prepareConfirmed(t)
		_, err, _ := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`,
			func(backupRequestEvent) map[string]any { return map[string]any{"error": "collect_failed"} })
		if err == nil {
			t.Fatal("apply succeeded without a collect")
		}
		if finished := restoreFinishedEvents(f.events); len(finished) != 0 {
			t.Fatalf("finished emitted before step 4: %v", finished)
		}
	})
}

// failPluginWrite fails the second plugin written (the first one is then
// rolled back), and that rollback too when failRollback is set.
func failPluginWrite(t *testing.T, failRollback bool) {
	t.Helper()
	var writes atomic.Int64
	replacePluginDataHook = func(string) error {
		if writes.Add(1) == 2 {
			return errors.New("disk full")
		}
		return nil
	}
	t.Cleanup(func() { replacePluginDataHook = nil })
	if failRollback {
		rollbackPluginDataHook = func(string) error { return errors.New("disk still full") }
		t.Cleanup(func() { rollbackPluginDataHook = nil })
	}
}

func snapshotStaged(t *testing.T, f *restoreFixture, prepared map[string]any) bool {
	t.Helper()
	_, err := backupCall(t, f.caller, "host.backup.read", `{"token":"`+prepared["preRestoreToken"].(string)+`","offset":0,"length":10}`)
	return err == nil
}

func prepareTwoPlugins(t *testing.T, f *restoreFixture, extra string) map[string]any {
	t.Helper()
	got, err, _ := f.prepare(t, `[`+extra+`"plugin:`+dataTestPlugin+`","plugin:other.plugin"]`, f.collectStep(t), confirmStep(true))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return got
}

func TestFailedRollbackIsReportedAndKeepsSnapshot(t *testing.T) {
	frontendRollbackFails := func(backupRequestEvent) map[string]any { return map[string]any{"error": "quota exceeded"} }
	t.Run("frontend rollback fails", func(t *testing.T) {
		f := newRestoreFixture(t, "data-backup")
		prepared := prepareTwoPlugins(t, f, `"settings",`)
		failPluginWrite(t, false)
		_, err, _ := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`,
			f.collectStep(t), applyOKStep(nil), frontendRollbackFails)
		if err == nil || rpcCode(err) != "restoreFailed" || strings.Contains(err.Message, "was put back") || !strings.Contains(err.Message, "pre-restore backup") {
			t.Fatalf("error %v", err)
		}
		finished := restoreFinishedEvents(f.events)
		if len(finished) != 1 || finished[0]["ok"] != false || finished[0]["rolledBack"] != false || finished[0]["preRestoreName"] != prepared["preRestoreName"] {
			t.Fatalf("finished %v", finished)
		}
		if !snapshotStaged(t, f, prepared) {
			t.Fatal("snapshot released although the rollback failed")
		}
	})
	t.Run("plugin rollback fails", func(t *testing.T) {
		f := newRestoreFixture(t, "data-backup")
		prepared := prepareTwoPlugins(t, f, `"settings",`)
		failPluginWrite(t, true)
		_, err, _ := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`,
			f.collectStep(t), applyOKStep(nil), applyOKStep(nil))
		if err == nil || rpcCode(err) != "restoreFailed" || strings.Contains(err.Message, "was put back") || !strings.Contains(err.Message, "pre-restore backup") {
			t.Fatalf("error %v", err)
		}
		finished := restoreFinishedEvents(f.events)
		if len(finished) != 1 || finished[0]["rolledBack"] != false {
			t.Fatalf("finished %v", finished)
		}
		if !snapshotStaged(t, f, prepared) {
			t.Fatal("snapshot released although the rollback failed")
		}
	})
	t.Run("plugin-only rollback fails", func(t *testing.T) {
		f := newRestoreFixture(t, "data-backup")
		got := prepareTwoPlugins(t, f, "")
		failPluginWrite(t, true)
		_, err, _ := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+got["restoreId"].(string)+`"}`, f.collectStep(t))
		if err == nil || strings.Contains(err.Message, "nothing was changed") || !strings.Contains(err.Message, "pre-restore backup") {
			t.Fatalf("error %v", err)
		}
		if !snapshotStaged(t, f, got) {
			t.Fatal("snapshot released although the rollback failed")
		}
	})
	t.Run("rollback succeeds", func(t *testing.T) {
		f := newRestoreFixture(t, "data-backup")
		prepared := prepareTwoPlugins(t, f, `"settings",`)
		failPluginWrite(t, false)
		_, err, _ := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`,
			f.collectStep(t), applyOKStep(nil), applyOKStep(nil))
		if err == nil {
			t.Fatal("apply succeeded")
		}
		finished := restoreFinishedEvents(f.events)
		if len(finished) != 1 || finished[0]["ok"] != false || finished[0]["rolledBack"] != true {
			t.Fatalf("finished %v", finished)
		}
		if snapshotStaged(t, f, prepared) {
			t.Fatal("snapshot kept after a clean rollback")
		}
	})
	t.Run("uncertain and rollback fails", func(t *testing.T) {
		f := newRestoreFixture(t, "data-backup")
		prepared := f.prepareConfirmed(t)
		previous := backupApplyTimeout
		backupApplyTimeout = 200 * time.Millisecond
		t.Cleanup(func() { backupApplyTimeout = previous })
		_, err, _ := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+prepared["restoreId"].(string)+`"}`,
			f.collectStep(t), func(backupRequestEvent) map[string]any { return nil }, frontendRollbackFails)
		if err == nil || rpcCode(err) != "restoreUncertain" {
			t.Fatalf("error %v", err)
		}
		finished := restoreFinishedEvents(f.events)
		if len(finished) != 1 || finished[0]["rolledBack"] != false {
			t.Fatalf("finished %v", finished)
		}
		if !snapshotStaged(t, f, prepared) {
			t.Fatal("snapshot released although the rollback failed")
		}
	})
}

func TestDeferredRestoreNeverRestoresRunJournal(t *testing.T) {
	useBackupHost(t)
	mustSetPluginKV(t, dataTestPlugin, "activeRun", `{"token":"local"}`)
	plan := pluginRestorePlan{ID: dataTestPlugin, KV: map[string]json.RawMessage{"notes": json.RawMessage(`2`), "activeRun": json.RawMessage(`{"token":"backup"}`)}}
	got, err := keepLocalRunJournal(plan)
	if err != nil || string(got.KV["activeRun"]) != `{"token":"local"}` || string(got.KV["notes"]) != `2` {
		t.Fatalf("local journal not kept: %v %v", got.KV, err)
	}
	if string(plan.KV["activeRun"]) != `{"token":"backup"}` {
		t.Fatal("the plan was modified in place")
	}
	if err := pluginKVDeleteRaw(dataTestPlugin, "activeRun"); err != nil {
		t.Fatal(err)
	}
	got, err = keepLocalRunJournal(plan)
	if _, found := got.KV["activeRun"]; err != nil || found {
		t.Fatalf("backup journal restored: %v %v", got.KV, err)
	}
}

func TestDeferredRestoreKeepsLocalRunJournal(t *testing.T) {
	f := newRestoreFixture(t, dataTestPlugin)
	mustSetPluginKV(t, dataTestPlugin, "activeRun", `{"token":"local"}`)
	got, err, _ := f.prepare(t, `["plugin:`+dataTestPlugin+`"]`, f.collectStep(t), confirmStep(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err, _ := runBackupCall(t, f.events, f.caller, "host.backup.applyRestore", `{"restoreId":"`+got["restoreId"].(string)+`"}`, f.collectStep(t)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for kvContent(t, dataTestPlugin)["notes"] != "2" {
		if time.Now().After(deadline) {
			t.Fatal("caller's data never restored")
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitForBackupDeferred(t)
	if kv := kvContent(t, dataTestPlugin); kv["activeRun"] != `{"token":"local"}` {
		t.Fatalf("run journal replaced by the restore: %v", kv)
	}
}

func TestOpenPromptSaysWhetherAPasswordExists(t *testing.T) {
	events := useBackupHost(t)
	useTestSecretCipher(t)
	manager, _, _ := pluginDataTestManager()
	caller := backupRuntime("data-backup", manager)
	token := stageTestBackup(t, "data-backup", backupTestPassword, false, []backup.Entry{appEntry("app_settings_v2", "plain", `{}`)})
	// A new device: nothing remembered, nothing for this run.
	_, err, seen := runBackupCall(t, events, caller, "host.backup.open", `{"token":"`+token+`"}`, func(event backupRequestEvent) map[string]any {
		if payloadJSON(event) != `{"mode":"open","hasPassword":false}` {
			t.Errorf("prompt %s", payloadJSON(event))
		}
		return passwordStep(backupTestPassword, false)(event)
	})
	if err != nil || len(seen) != 1 {
		t.Fatalf("open: %v", err)
	}
	if _, found, _ := backupRememberedPassword(); found {
		t.Fatal("remembered without remember=true")
	}
}
