package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"mincode/app/backend/backup"
)

const backupConfigurationJSON = `{"version":1,"global":{"x":1,"y":1},"workspaces":{}}`

func entryMap(entries []backup.Entry) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		out[entry.Path] = string(entry.Data)
	}
	return out
}

func mustSetSecret(t *testing.T, id, key, value string) {
	t.Helper()
	if err := pluginSecretSet(id, key, value); err != nil {
		t.Fatal(err)
	}
}

func mustSetConfiguration(t *testing.T, id, document string) {
	t.Helper()
	mustSetPluginKV(t, hostPluginID, configurationKVKey(id), document)
}

func configurationGlobal(t *testing.T, id string) map[string]string {
	t.Helper()
	document, readErr, err := loadConfigurationDocument(id)
	if err != nil || readErr != nil {
		t.Fatalf("configuration: %v %v", err, readErr)
	}
	out := map[string]string{}
	for key, value := range document.Global {
		out[key] = string(value)
	}
	return out
}

func secretContent(t *testing.T, id string) map[string]string {
	t.Helper()
	keys, err := pluginSecretList(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, key := range keys {
		value, _, err := pluginSecretGet(id, key)
		if err != nil {
			t.Fatal(err)
		}
		out[key] = value
	}
	return out
}

// seedBackupSource stores KV, files, configuration and secrets for
// dataTestPlugin, plus a plugin that only has a secret.
func seedBackupSource(t *testing.T) {
	t.Helper()
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	mustSetPluginKV(t, dataTestPlugin, "notes", `{"count":2}`)
	mustWriteStorage(t, dataTestPlugin, "bodies/a.md", "alpha")
	mustWriteStorage(t, dataTestPlugin, "b.bin", "bee")
	mustSetConfiguration(t, dataTestPlugin, backupConfigurationJSON)
	mustSetSecret(t, dataTestPlugin, "token", "plain-token")
	mustSetSecret(t, dataTestPlugin, configurationSecretKey("apiKey"), "reserved-value")
	mustSetSecret(t, "secret.only", "password", "only-secret")
	mustSetPluginKV(t, hostPluginID, dataTestPlugin+".enabled", `true`)
	mustSetPluginKV(t, hostPluginID, dataTestPlugin+".scope", `"global"`)
}

func TestCollectPluginBackupOmitsSecretsUnlessAsked(t *testing.T) {
	seedBackupSource(t)
	manager, _, _ := pluginDataTestManager()
	entries, sections, err := manager.collectPluginBackupEntries(false)
	if err != nil {
		t.Fatal(err)
	}
	got := entryMap(entries)
	want := map[string]string{
		"plugins/data.plugin/kv.json":            `{"notes":{"count":2}}`,
		"plugins/data.plugin/files/bodies/a.md":  "alpha",
		"plugins/data.plugin/files/b.bin":        "bee",
		"plugins/data.plugin/configuration.json": backupConfigurationJSON,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %#v", got)
	}
	for _, entry := range entries {
		if strings.Contains(string(entry.Data), "plain-token") || strings.Contains(string(entry.Data), "only-secret") {
			t.Fatalf("secret leaked into %s", entry.Path)
		}
	}
	wantSections := []backup.SectionSummary{{ID: backup.PluginSectionID(dataTestPlugin), Title: "Test Plugin", Items: 4, Bytes: 8, Plugin: dataTestPlugin}}
	if !reflect.DeepEqual(sections, wantSections) {
		t.Fatalf("sections = %#v", sections)
	}
}

func TestCollectPluginBackupIncludesSecretsWhenAsked(t *testing.T) {
	seedBackupSource(t)
	manager, _, _ := pluginDataTestManager()
	ciphertext := storedSecretCiphertext(t, dataTestPlugin, "token")
	entries, sections, err := manager.collectPluginBackupEntries(true)
	if err != nil {
		t.Fatal(err)
	}
	got := entryMap(entries)
	if got["plugins/data.plugin/secrets.json"] != `{"token":"plain-token"}` {
		t.Fatalf("secrets = %q", got["plugins/data.plugin/secrets.json"])
	}
	if got["plugins/secret.only/secrets.json"] != `{"password":"only-secret"}` {
		t.Fatalf("secret-only plugin = %#v", got)
	}
	for _, entry := range entries {
		if strings.Contains(string(entry.Data), ciphertext) || strings.Contains(string(entry.Data), "reserved-value") {
			t.Fatalf("ciphertext or reserved secret in %s", entry.Path)
		}
	}
	if len(sections) != 2 || sections[0].Plugin != dataTestPlugin || sections[1].Plugin != "secret.only" || sections[1].Title != "secret.only" {
		t.Fatalf("sections = %#v", sections)
	}
}

func TestCollectSkipsHostNamespace(t *testing.T) {
	seedBackupSource(t)
	manager, _, _ := pluginDataTestManager()
	entries, sections, err := manager.collectPluginBackupEntries(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Path, "plugins/"+hostPluginID+"/") || strings.Contains(string(entry.Data), ".enabled") || strings.Contains(string(entry.Data), ".scope") {
			t.Fatalf("host namespace in backup: %s %s", entry.Path, entry.Data)
		}
	}
	for _, section := range sections {
		if section.Plugin == hostPluginID {
			t.Fatalf("host section: %#v", section)
		}
	}
}

// pluginBackupEntries builds the backup entries for one plugin. Empty
// strings leave that entry out.
func pluginBackupEntries(id, kv string, files map[string]string, configuration, secrets string) []backup.Entry {
	prefix := "plugins/" + id + "/"
	var entries []backup.Entry
	if kv != "" {
		entries = append(entries, backup.Entry{Path: prefix + "kv.json", Data: []byte(kv)})
	}
	for name, content := range files {
		entries = append(entries, backup.Entry{Path: prefix + "files/" + name, Data: []byte(content)})
	}
	if configuration != "" {
		entries = append(entries, backup.Entry{Path: prefix + "configuration.json", Data: []byte(configuration)})
	}
	if secrets != "" {
		entries = append(entries, backup.Entry{Path: prefix + "secrets.json", Data: []byte(secrets)})
	}
	return entries
}

func mustPlan(t *testing.T, id string, entries []backup.Entry, mode backup.Mode) pluginRestorePlan {
	t.Helper()
	plan, err := planPluginRestore(id, entries, mode)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func seedRestoreTarget(t *testing.T, id string) {
	t.Helper()
	mustSetPluginKV(t, id, "a", `1`)
	mustSetPluginKV(t, id, "b", `2`)
	mustWriteStorage(t, id, "keep.txt", "keep")
	mustWriteStorage(t, id, "both.txt", "old")
	mustSetConfiguration(t, id, backupConfigurationJSON)
	mustSetSecret(t, id, "local", "local-secret")
	mustSetSecret(t, id, "shared", "old-secret")
	mustSetSecret(t, id, configurationSecretKey("apiKey"), "reserved")
}

func incomingRestoreEntries(id string) []backup.Entry {
	return pluginBackupEntries(id, `{"b":20,"c":3}`,
		map[string]string{"both.txt": "new", "dir/added.txt": "added"},
		`{"version":1,"global":{"y":2,"z":3},"workspaces":{}}`,
		`{"shared":"new-secret","added":"added-secret"}`)
}

func TestApplyPluginRestoreMerge(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	seedRestoreTarget(t, dataTestPlugin)
	manager, _, lifecycle := pluginDataTestManager()
	plan := mustPlan(t, dataTestPlugin, incomingRestoreEntries(dataTestPlugin), backup.ModeMerge)
	labels := map[string]string{}
	for _, change := range plan.Changes {
		labels[change.Label] = change.Kind
	}
	for label, kind := range map[string]string{"kv/a": backup.ChangeKeep, "kv/b": backup.ChangeUpdate, "kv/c": backup.ChangeAdd,
		"files/keep.txt": backup.ChangeKeep, "files/both.txt": backup.ChangeUpdate, "files/dir/added.txt": backup.ChangeAdd,
		"configuration": backup.ChangeUpdate, "secrets/local": backup.ChangeKeep, "secrets/shared": backup.ChangeUpdate, "secrets/added": backup.ChangeAdd} {
		if labels[label] != kind {
			t.Fatalf("change %q = %q, want %q (all %#v)", label, labels[label], kind, plan.Changes)
		}
	}
	for _, change := range plan.Changes {
		if strings.Contains(change.Label, "secret-") {
			t.Fatalf("secret value in change label: %#v", change)
		}
	}

	applied, notInstalled, err := manager.applyPluginRestore(context.Background(), []pluginRestorePlan{plan}, "")
	if err != nil || !reflect.DeepEqual(applied, []string{dataTestPlugin}) || len(notInstalled) != 0 {
		t.Fatalf("apply = %v %v %v", applied, notInstalled, err)
	}
	if got := kvContent(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"a": "1", "b": "20", "c": "3"}) {
		t.Fatalf("kv = %#v", got)
	}
	if got := storageContent(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"keep.txt": "keep", "both.txt": "new", "dir/added.txt": "added"}) {
		t.Fatalf("files = %#v", got)
	}
	if got := configurationGlobal(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"x": "1", "y": "2", "z": "3"}) {
		t.Fatalf("configuration = %#v", got)
	}
	if got := secretContent(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"local": "local-secret", "shared": "new-secret", "added": "added-secret", configurationSecretKey("apiKey"): "reserved"}) {
		t.Fatalf("secrets = %#v", got)
	}
	if lifecycle.shutdownCalls.Load() != 1 || lifecycle.startupCalls.Load() != 1 {
		t.Fatalf("lifecycle stop=%d start=%d", lifecycle.shutdownCalls.Load(), lifecycle.startupCalls.Load())
	}
}

func TestApplyPluginRestoreOverwrite(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	seedRestoreTarget(t, dataTestPlugin)
	manager, _, _ := pluginDataTestManager()
	plan := mustPlan(t, dataTestPlugin, incomingRestoreEntries(dataTestPlugin), backup.ModeOverwrite)
	if !reflect.DeepEqual(plan.Remove, []string{"keep.txt"}) {
		t.Fatalf("remove = %v", plan.Remove)
	}
	if _, _, err := manager.applyPluginRestore(context.Background(), []pluginRestorePlan{plan}, ""); err != nil {
		t.Fatal(err)
	}
	if got := kvContent(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"b": "20", "c": "3"}) {
		t.Fatalf("kv = %#v", got)
	}
	if got := storageContent(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"both.txt": "new", "dir/added.txt": "added"}) {
		t.Fatalf("files = %#v", got)
	}
	if got := configurationGlobal(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"y": "2", "z": "3"}) {
		t.Fatalf("configuration = %#v", got)
	}
	// Reserved configuration secrets are not plugin-facing and stay.
	if got := secretContent(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"shared": "new-secret", "added": "added-secret", configurationSecretKey("apiKey"): "reserved"}) {
		t.Fatalf("secrets = %#v", got)
	}
}

func TestApplyPluginRestoreWithoutSecretsKeepsLocalSecrets(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	seedRestoreTarget(t, dataTestPlugin)
	manager, _, _ := pluginDataTestManager()
	entries := pluginBackupEntries(dataTestPlugin, `{"b":20}`, nil, "", "")
	plan := mustPlan(t, dataTestPlugin, entries, backup.ModeOverwrite)
	if _, _, err := manager.applyPluginRestore(context.Background(), []pluginRestorePlan{plan}, ""); err != nil {
		t.Fatal(err)
	}
	if got := secretContent(t, dataTestPlugin); len(got) != 3 || got["local"] != "local-secret" {
		t.Fatalf("secrets = %#v", got)
	}
	if got := configurationGlobal(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"x": "1", "y": "1"}) {
		t.Fatalf("configuration = %#v", got)
	}
	if got := storageContent(t, dataTestPlugin); len(got) != 0 {
		t.Fatalf("files = %#v", got)
	}
}

func TestPlanPluginRestoreRejectsBadEntries(t *testing.T) {
	useTemporaryPluginData(t)
	for name, tc := range map[string]struct {
		id      string
		entries []backup.Entry
	}{
		"host namespace":   {hostPluginID, pluginBackupEntries(hostPluginID, `{}`, nil, "", "")},
		"kv not object":    {"p.plugin", pluginBackupEntries("p.plugin", `[1]`, nil, "", "")},
		"bad kv key":       {"p.plugin", pluginBackupEntries("p.plugin", `{"a:b":1}`, nil, "", "")},
		"bad file path":    {"p.plugin", []backup.Entry{{Path: "plugins/p.plugin/files/.storage-x", Data: []byte("x")}}},
		"bad config":       {"p.plugin", pluginBackupEntries("p.plugin", "", nil, `{"version":9}`, "")},
		"reserved secret":  {"p.plugin", pluginBackupEntries("p.plugin", "", nil, "", `{"__config.k":"v"}`)},
		"empty secret":     {"p.plugin", pluginBackupEntries("p.plugin", "", nil, "", `{"k":""}`)},
		"unknown entry":    {"p.plugin", []backup.Entry{{Path: "plugins/p.plugin/other.json", Data: []byte("{}")}}},
		"secrets not json": {"p.plugin", pluginBackupEntries("p.plugin", "", nil, "", `nope`)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := planPluginRestore(tc.id, tc.entries, backup.ModeMerge); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestApplyPluginRestoreRollsBackOnFailure(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	const first, second = "alpha.plugin", "beta.plugin"
	seedRestoreTarget(t, first)
	mustSetPluginKV(t, second, "z", `9`)
	manager, _, _ := pluginDataTestManager()
	plans := []pluginRestorePlan{
		mustPlan(t, first, incomingRestoreEntries(first), backup.ModeOverwrite),
		mustPlan(t, second, pluginBackupEntries(second, `{"z":10}`, nil, "", ""), backup.ModeOverwrite),
	}
	wantKV, wantFiles, wantConfig, wantSecrets := kvContent(t, first), storageContent(t, first), configurationGlobal(t, first), secretContent(t, first)

	injected := errors.New("injected failure")
	replacePluginDataHook = func(id string) error {
		if id == second {
			return injected
		}
		return nil
	}
	t.Cleanup(func() { replacePluginDataHook = nil })

	applied, _, err := manager.applyPluginRestore(context.Background(), plans, "")
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), second) {
		t.Fatalf("err = %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied after rollback = %v", applied)
	}
	if got := kvContent(t, first); !reflect.DeepEqual(got, wantKV) {
		t.Fatalf("kv = %#v, want %#v", got, wantKV)
	}
	if got := storageContent(t, first); !reflect.DeepEqual(got, wantFiles) {
		t.Fatalf("files = %#v, want %#v", got, wantFiles)
	}
	if got := configurationGlobal(t, first); !reflect.DeepEqual(got, wantConfig) {
		t.Fatalf("configuration = %#v, want %#v", got, wantConfig)
	}
	if got := secretContent(t, first); !reflect.DeepEqual(got, wantSecrets) {
		t.Fatalf("secrets = %#v, want %#v", got, wantSecrets)
	}
	if got := kvContent(t, second); !reflect.DeepEqual(got, map[string]string{"z": "9"}) {
		t.Fatalf("second kv = %#v", got)
	}
}

func TestApplyPluginRestoreRollsBackConfigurationAddedByRestore(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	const first, second = "alpha.plugin", "beta.plugin"
	mustSetPluginKV(t, first, "a", `1`)
	manager, _, _ := pluginDataTestManager()
	plans := []pluginRestorePlan{
		mustPlan(t, first, pluginBackupEntries(first, `{"a":2}`, nil, backupConfigurationJSON, `{"new":"value"}`), backup.ModeMerge),
		mustPlan(t, second, pluginBackupEntries(second, `{}`, nil, "", ""), backup.ModeMerge),
	}
	replacePluginDataHook = func(id string) error {
		if id == second {
			return errors.New("boom")
		}
		return nil
	}
	t.Cleanup(func() { replacePluginDataHook = nil })
	if _, _, err := manager.applyPluginRestore(context.Background(), plans, ""); err == nil {
		t.Fatal("expected error")
	}
	if _, found, err := pluginKVGetRaw(hostPluginID, configurationKVKey(first)); err != nil || found {
		t.Fatalf("configuration left behind: %v %v", found, err)
	}
	if got := secretContent(t, first); len(got) != 0 {
		t.Fatalf("secrets left behind: %#v", got)
	}
	if got := kvContent(t, first); !reflect.DeepEqual(got, map[string]string{"a": "1"}) {
		t.Fatalf("kv = %#v", got)
	}
}

func TestApplyPluginRestoreWritesUninstalledPlugin(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	manager, _, lifecycle := pluginDataTestManager()
	const ghost = "ghost.plugin"
	plans := []pluginRestorePlan{
		mustPlan(t, ghost, pluginBackupEntries(ghost, `{"k":"v"}`, map[string]string{"f.txt": "file"}, "", ""), backup.ModeMerge),
		mustPlan(t, dataTestPlugin, pluginBackupEntries(dataTestPlugin, `{"x":1}`, nil, "", ""), backup.ModeMerge),
	}
	applied, notInstalled, err := manager.applyPluginRestore(context.Background(), plans, "")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(applied)
	if !reflect.DeepEqual(applied, []string{dataTestPlugin, ghost}) || !reflect.DeepEqual(notInstalled, []string{ghost}) {
		t.Fatalf("applied=%v notInstalled=%v", applied, notInstalled)
	}
	if got := kvContent(t, ghost); !reflect.DeepEqual(got, map[string]string{"k": `"v"`}) {
		t.Fatalf("kv = %#v", got)
	}
	if got := storageContent(t, ghost); !reflect.DeepEqual(got, map[string]string{"f.txt": "file"}) {
		t.Fatalf("files = %#v", got)
	}
	if lifecycle.startupCalls.Load() != 1 {
		t.Fatalf("installed plugin start calls = %d", lifecycle.startupCalls.Load())
	}
}

func TestApplyPluginRestoreSkipsDeferred(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	manager, _, lifecycle := pluginDataTestManager()
	const other = "other.plugin"
	mustSetPluginKV(t, dataTestPlugin, "old", `true`)
	plans := []pluginRestorePlan{
		mustPlan(t, dataTestPlugin, pluginBackupEntries(dataTestPlugin, `{"new":true}`, nil, "", ""), backup.ModeOverwrite),
		mustPlan(t, other, pluginBackupEntries(other, `{"k":1}`, nil, "", ""), backup.ModeOverwrite),
	}
	applied, _, err := manager.applyPluginRestore(context.Background(), plans, dataTestPlugin)
	if err != nil || !reflect.DeepEqual(applied, []string{other}) {
		t.Fatalf("apply = %v %v", applied, err)
	}
	if got := kvContent(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"old": "true"}) {
		t.Fatalf("deferred plugin written early: %#v", got)
	}
	if lifecycle.shutdownCalls.Load() != 0 {
		t.Fatalf("deferred plugin stopped")
	}
	if err := manager.applyDeferredPluginRestore(context.Background(), plans[0]); err != nil {
		t.Fatal(err)
	}
	if got := kvContent(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"new": "true"}) {
		t.Fatalf("deferred kv = %#v", got)
	}
	if lifecycle.shutdownCalls.Load() != 1 || lifecycle.startupCalls.Load() != 1 {
		t.Fatalf("deferred lifecycle stop=%d start=%d", lifecycle.shutdownCalls.Load(), lifecycle.startupCalls.Load())
	}
}

func TestRestoredSecretsResealedWithMachineKey(t *testing.T) {
	// The real machine-bound cipher derives its install salt under the user
	// data directory; point it at temporary directories.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	useTemporaryPluginData(t)
	manager, _, _ := pluginDataTestManager()
	secrets, _ := json.Marshal(map[string]string{"token": "restored-plain"})
	plan := mustPlan(t, dataTestPlugin, pluginBackupEntries(dataTestPlugin, `{}`, nil, "", string(secrets)), backup.ModeMerge)
	if _, _, err := manager.applyPluginRestore(context.Background(), []pluginRestorePlan{plan}, ""); err != nil {
		t.Fatal(err)
	}
	stored := storedSecretCiphertext(t, dataTestPlugin, "token")
	if !strings.HasPrefix(stored, "enc:v2:") || strings.Contains(stored, "restored-plain") {
		t.Fatalf("stored = %q", stored)
	}
	value, found, err := pluginSecretGet(dataTestPlugin, "token")
	if err != nil || !found || value != "restored-plain" {
		t.Fatalf("get = %q %v %v", value, found, err)
	}
}

func TestCollectedEntriesSealAndOpen(t *testing.T) {
	seedBackupSource(t)
	// Names plugin storage accepts that a strict archive path check refused.
	mustWriteStorage(t, dataTestPlugin, "notes/a..b.txt", "dots")
	mustWriteStorage(t, dataTestPlugin, "notes/tab\tname.txt", "tab")
	manager, _, _ := pluginDataTestManager()
	entries, sections, err := manager.collectPluginBackupEntries(false)
	if err != nil {
		t.Fatal(err)
	}
	collected := entryMap(entries)
	for _, name := range []string{"plugins/data.plugin/files/notes/a..b.txt", "plugins/data.plugin/files/notes/tab\tname.txt"} {
		if _, ok := collected[name]; !ok {
			t.Fatalf("%q not collected: %#v", name, collected)
		}
	}
	var sealed bytes.Buffer
	password := []byte("backup-password-123")
	if err := backup.Seal(&sealed, password, backup.Manifest{Sections: sections}, entries); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	_, opened, err := backup.Open(bytes.NewReader(sealed.Bytes()), password)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := entryMap(opened); !reflect.DeepEqual(got, collected) {
		t.Fatalf("round trip = %#v, want %#v", got, collected)
	}
}

func TestApplyPluginRestoreRollbackRestartsRunningPlugin(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	seedRestoreTarget(t, dataTestPlugin)
	manager, record, lifecycle := pluginDataTestManager()
	const second = "beta.plugin"
	plans := []pluginRestorePlan{
		mustPlan(t, dataTestPlugin, incomingRestoreEntries(dataTestPlugin), backup.ModeOverwrite),
		mustPlan(t, second, pluginBackupEntries(second, `{"z":10}`, nil, "", ""), backup.ModeOverwrite),
	}
	wantKV, wantFiles, wantConfig, wantSecrets := kvContent(t, dataTestPlugin), storageContent(t, dataTestPlugin), configurationGlobal(t, dataTestPlugin), secretContent(t, dataTestPlugin)
	injected := errors.New("injected failure")
	replacePluginDataHook = func(id string) error {
		if id == second {
			return injected
		}
		return nil
	}
	t.Cleanup(func() { replacePluginDataHook = nil })

	if _, _, err := manager.applyPluginRestore(context.Background(), plans, ""); !errors.Is(err, injected) {
		t.Fatalf("err = %v", err)
	}
	if got := kvContent(t, dataTestPlugin); !reflect.DeepEqual(got, wantKV) {
		t.Fatalf("kv = %#v, want %#v", got, wantKV)
	}
	if got := storageContent(t, dataTestPlugin); !reflect.DeepEqual(got, wantFiles) {
		t.Fatalf("files = %#v, want %#v", got, wantFiles)
	}
	if got := configurationGlobal(t, dataTestPlugin); !reflect.DeepEqual(got, wantConfig) {
		t.Fatalf("configuration = %#v, want %#v", got, wantConfig)
	}
	if got := secretContent(t, dataTestPlugin); !reflect.DeepEqual(got, wantSecrets) {
		t.Fatalf("secrets = %#v, want %#v", got, wantSecrets)
	}
	record.mu.Lock()
	state := record.state
	record.mu.Unlock()
	if state != pluginStateRunning {
		t.Fatalf("state after rollback = %q", state)
	}
	// Stopped and restarted once for the restore write and once more for the
	// rollback write.
	if stops, starts := lifecycle.shutdownCalls.Load(), lifecycle.startupCalls.Load(); stops != 2 || starts != 2 {
		t.Fatalf("lifecycle stop=%d start=%d, want 2/2", stops, starts)
	}
}
