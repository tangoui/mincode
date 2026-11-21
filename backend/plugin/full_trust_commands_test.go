package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// runFullTrustCommand calls a plugin_full_trust_* command against manager
// exactly as core.Dispatch would against host().
func runFullTrustCommand(t *testing.T, manager *pluginManager, name, args string) (string, error) {
	t.Helper()
	command, ok := fullTrustCommands[name]
	if !ok {
		t.Fatalf("command %s not defined", name)
	}
	result, err := command(manager, args)
	if err != nil {
		return "", err
	}
	return jsonOf(t, result), nil
}

func mustFullTrustCommand(t *testing.T, manager *pluginManager, name, args string, into any) {
	t.Helper()
	raw, err := runFullTrustCommand(t, manager, name, args)
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	if into != nil {
		if err := json.Unmarshal([]byte(raw), into); err != nil {
			t.Fatal(err)
		}
	}
}

type fullTrustView struct {
	PluginID        string `json:"pluginId"`
	Trusted         bool   `json:"trusted"`
	GrantedAt       int64  `json:"grantedAt"`
	LegacyDangerous bool   `json:"legacyDangerous"`
	Subject         struct {
		PluginID     string   `json:"pluginId"`
		Name         string   `json:"name"`
		Version      string   `json:"version"`
		Capabilities []string `json:"capabilities"`
		Fingerprint  string   `json:"fingerprint"`
		Eligible     bool     `json:"eligible"`
		Reason       string   `json:"reason"`
	} `json:"subject"`
}

func fullTrustAudit(t *testing.T, id string) []FullTrustAuditEntry {
	t.Helper()
	entries, err := readFullTrustAudit(id)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func legacyTrustFound(t *testing.T, id string) bool {
	t.Helper()
	_, found, err := pluginKVGetRaw(hostPluginID, pluginDangerousActionTrustKey(id))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// grantByCommand enables full trust through plugin_full_trust_set with the
// fingerprint plugin_full_trust_get reported.
func grantByCommand(t *testing.T, manager *pluginManager, id string) fullTrustView {
	t.Helper()
	var view fullTrustView
	mustFullTrustCommand(t, manager, "plugin_full_trust_get", jsonOf(t, map[string]any{"pluginId": id, "full": true}), &view)
	if !view.Subject.Eligible || view.Subject.Fingerprint == "" {
		t.Fatalf("not eligible: %+v", view)
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_set",
		jsonOf(t, map[string]any{"pluginId": id, "trusted": true, "fingerprint": view.Subject.Fingerprint}), &view)
	if !view.Trusted {
		t.Fatalf("not trusted after set: %+v", view)
	}
	return view
}

func TestFullTrustSetRequiresReviewedFingerprint(t *testing.T) {
	fixture := fullTrustManager(t)
	events := captureHostUI(t)
	manager := fixture.manager

	var view fullTrustView
	mustFullTrustCommand(t, manager, "plugin_full_trust_get", `{"pluginId":" trust.demo ","full":true}`, &view)
	if view.PluginID != fullTrustDemoID || view.Trusted || !view.Subject.Eligible || len(view.Subject.Fingerprint) != 64 ||
		view.Subject.Name != "Trust Demo" || view.Subject.Version != "1.0.0" || !slices.Equal(view.Subject.Capabilities, []string{"plugins", "storage"}) {
		t.Fatalf("get = %+v", view)
	}
	reviewed := view.Subject.Fingerprint

	if _, err := runFullTrustCommand(t, manager, "plugin_full_trust_set", `{"pluginId":"trust.demo","trusted":true,"fingerprint":"0"}`); err == nil || !strings.HasPrefix(err.Error(), "changed:") {
		t.Fatalf("wrong fingerprint: %v", err)
	}
	if _, err := runFullTrustCommand(t, manager, "plugin_full_trust_set", `{"pluginId":"trust.demo","trusted":true}`); err == nil || !strings.HasPrefix(err.Error(), "changed:") {
		t.Fatalf("missing fingerprint: %v", err)
	}
	if fullTrustRecordFound(t, fullTrustDemoID) {
		t.Fatal("record written without the reviewed fingerprint")
	}

	// The package changed after the user reviewed it.
	rewriteByte(t, filepath.Join(fixture.root, "ui", "index.html"))
	if _, err := runFullTrustCommand(t, manager, "plugin_full_trust_set", jsonOf(t, map[string]any{"pluginId": fullTrustDemoID, "trusted": true, "fingerprint": reviewed})); err == nil || !strings.HasPrefix(err.Error(), "changed:") {
		t.Fatalf("stale fingerprint: %v", err)
	}
	if fullTrustRecordFound(t, fullTrustDemoID) {
		t.Fatal("record written for a stale fingerprint")
	}

	if err := manager.setDangerousActionsTrusted(fullTrustDemoID, true); err != nil {
		t.Fatal(err)
	}
	view = grantByCommand(t, manager, fullTrustDemoID)
	if view.GrantedAt == 0 || view.LegacyDangerous {
		t.Fatalf("set = %+v", view)
	}
	if !fullTrustRecordFound(t, fullTrustDemoID) || legacyTrustFound(t, fullTrustDemoID) {
		t.Fatal("grant must store the record and drop the legacy one")
	}
	entries := fullTrustAudit(t, fullTrustDemoID)
	if len(entries) != 1 || entries[0].Kind != "granted" {
		t.Fatalf("audit = %+v", entries)
	}
	if event := jsonOf(t, events.last(t, fullTrustChangedEvent)); event != `{"pluginId":"trust.demo","reason":"granted","trusted":true}` {
		t.Fatalf("event = %s", event)
	}

	if err := manager.setDangerousActionsTrusted(fullTrustDemoID, true); err != nil {
		t.Fatal(err)
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_set", `{"pluginId":"trust.demo","trusted":false}`, &view)
	if view.Trusted || fullTrustRecordFound(t, fullTrustDemoID) || legacyTrustFound(t, fullTrustDemoID) {
		t.Fatalf("revoke kept trust: %+v", view)
	}
	entries = fullTrustAudit(t, fullTrustDemoID)
	if last := entries[len(entries)-1]; last.Kind != "revoked" {
		t.Fatalf("audit = %+v", entries)
	}
	if event := jsonOf(t, events.last(t, fullTrustChangedEvent)); event != `{"pluginId":"trust.demo","reason":"revoked","trusted":false}` {
		t.Fatalf("event = %s", event)
	}

	for _, args := range []string{
		`{"pluginId":"trust.demo","trusted":true,"extra":1}`,
		`{"pluginId":"trust.demo"}`,
		`{"pluginId":"","trusted":false}`,
		`{"pluginId":"a:b","trusted":false}`,
		`{"pluginId":"trust.demo","trusted":false} {}`,
	} {
		if _, err := runFullTrustCommand(t, manager, "plugin_full_trust_set", args); err == nil {
			t.Fatalf("set %s accepted", args)
		}
	}
	if _, err := runFullTrustCommand(t, manager, "plugin_full_trust_get", `{"pluginId":"trust.demo","x":1}`); err == nil {
		t.Fatal("get accepted an unknown field")
	}
}

func TestFullTrustSetRefusesIneligible(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		change func(t *testing.T, fixture *fullTrustFixture)
	}{
		{"declarative", "declarative", func(t *testing.T, f *fullTrustFixture) {
			record := f.record(t)
			record.mu.Lock()
			manifest := *record.manifest
			manifest.Runtime = ProcessRuntimeManifest{Type: "declarative"}
			record.manifest = &manifest
			record.mu.Unlock()
		}},
		{"development", "development", func(t *testing.T, f *fullTrustFixture) {
			record := f.record(t)
			record.mu.Lock()
			record.source = pluginSourceDevelopment
			record.mu.Unlock()
		}},
		{"disabled", "disabled", func(t *testing.T, f *fullTrustFixture) { f.setEnabled(t, false) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fullTrustManager(t)
			var view fullTrustView
			mustFullTrustCommand(t, fixture.manager, "plugin_full_trust_get", `{"pluginId":"trust.demo","full":true}`, &view)
			tc.change(t, fixture)
			args := jsonOf(t, map[string]any{"pluginId": fullTrustDemoID, "trusted": true, "fingerprint": view.Subject.Fingerprint})
			if _, err := runFullTrustCommand(t, fixture.manager, "plugin_full_trust_set", args); err == nil || err.Error() != "not_eligible:"+tc.reason {
				t.Fatalf("set = %v", err)
			}
			if fullTrustRecordFound(t, fullTrustDemoID) {
				t.Fatal("record written for an ineligible plugin")
			}
		})
	}
	t.Run("not installed", func(t *testing.T) {
		fixture := fullTrustManager(t)
		if _, err := runFullTrustCommand(t, fixture.manager, "plugin_full_trust_set", `{"pluginId":"missing.plugin","trusted":true,"fingerprint":"0"}`); err == nil || err.Error() != "not_eligible:not_installed" {
			t.Fatalf("set = %v", err)
		}
	})
}

func authorizeArgs(t *testing.T, surface, risk, subject string) string {
	return jsonOf(t, map[string]any{"pluginId": fullTrustDemoID, "surface": surface, "risk": risk, "subject": subject})
}

type authorizeResult struct {
	Allowed bool   `json:"allowed"`
	Lapsed  string `json:"lapsed"`
}

func TestFullTrustAuthorizeRefusedWhileLocked(t *testing.T) {
	fixture := fullTrustManager(t)
	setLocked := unlockAppForTest(t)
	captureHostUI(t)
	manager := fixture.manager
	view := grantByCommand(t, manager, fullTrustDemoID)

	var result authorizeResult
	raw, err := runFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, "viewAction", "write", "run"))
	if err != nil || raw != `{"allowed":true}` {
		t.Fatalf("authorize = %s %v", raw, err)
	}
	entries := fullTrustAudit(t, fullTrustDemoID)
	if last := entries[len(entries)-1]; len(entries) != 2 || last.Kind != "skipped" || last.Surface != "viewAction" || last.Risk != "write" || last.Subject != "run" {
		t.Fatalf("audit = %+v", entries)
	}

	setLocked(true)
	mustFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, "viewAction", "write", "run"), &result)
	if result.Allowed || result.Lapsed != "" {
		t.Fatalf("authorize while locked = %+v", result)
	}
	if got := fullTrustAudit(t, fullTrustDemoID); len(got) != 2 {
		t.Fatalf("audit changed while locked: %+v", got)
	}
	args := jsonOf(t, map[string]any{"pluginId": fullTrustDemoID, "trusted": true, "fingerprint": view.Subject.Fingerprint})
	if _, err := runFullTrustCommand(t, manager, "plugin_full_trust_set", args); err == nil || !strings.HasPrefix(err.Error(), "locked:") {
		t.Fatalf("set while locked = %v", err)
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_set", `{"pluginId":"trust.demo","trusted":false}`, &view)
	if view.Trusted || fullTrustRecordFound(t, fullTrustDemoID) {
		t.Fatal("revoke refused while locked")
	}
}

func TestFullTrustAuthorizeValidation(t *testing.T) {
	fixture := fullTrustManager(t)
	captureHostUI(t)
	manager := fixture.manager
	var result authorizeResult

	for _, surface := range []string{"pluginCall", "serviceCall", "hostService", "", "other"} {
		if _, err := runFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, surface, "write", "run")); err == nil {
			t.Fatalf("surface %q accepted", surface)
		}
	}
	if _, err := runFullTrustCommand(t, manager, "plugin_full_trust_authorize", `{"pluginId":"trust.demo","surface":"viewAction","risk":"write","subject":"run","x":1}`); err == nil {
		t.Fatal("unknown field accepted")
	}

	mustFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, "viewAction", "dangerous", "wipe"), &result)
	if result.Allowed {
		t.Fatal("allowed without trust")
	}

	// Legacy dangerous-action trust: only page actions, only dangerous.
	if err := manager.setDangerousActionsTrusted(fullTrustDemoID, true); err != nil {
		t.Fatal(err)
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, "viewAction", "dangerous", "wipe"), &result)
	if !result.Allowed {
		t.Fatal("legacy trust did not allow a dangerous page action")
	}
	entries := fullTrustAudit(t, fullTrustDemoID)
	if len(entries) != 1 || entries[0].Kind != "skipped" || entries[0].Reason != "legacy" || entries[0].Surface != "viewAction" || entries[0].Risk != "dangerous" || entries[0].Subject != "wipe" {
		t.Fatalf("legacy audit = %+v", entries)
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, "viewAction", "write", "run"), &result)
	if result.Allowed {
		t.Fatal("legacy trust allowed a write action")
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, "codeLens", "dangerous", "run"), &result)
	if result.Allowed {
		t.Fatal("legacy trust allowed a CodeLens command")
	}
	if err := manager.setDangerousActionsTrusted(fullTrustDemoID, false); err != nil {
		t.Fatal(err)
	}

	// Unknown risks are recorded as dangerous; the subject is cleaned.
	grantByCommand(t, manager, fullTrustDemoID)
	for _, risk := range []string{"read", "weird"} {
		mustFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, "tray", risk, "run\x1b[31m"), &result)
		if !result.Allowed {
			t.Fatalf("risk %s refused", risk)
		}
		entries = fullTrustAudit(t, fullTrustDemoID)
		if last := entries[len(entries)-1]; last.Risk != "dangerous" || last.Surface != "tray" || last.Subject != "run[31m" {
			t.Fatalf("risk %s audit = %+v", risk, last)
		}
	}
	long := strings.Repeat("x", 300)
	mustFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, "testRun", "network", long), &result)
	entries = fullTrustAudit(t, fullTrustDemoID)
	if last := entries[len(entries)-1]; len([]rune(last.Subject)) > fullTrustAuditSubjectMaxRunes || last.Risk != "network" {
		t.Fatalf("long subject audit = %+v", last)
	}

	rewriteByte(t, filepath.Join(fixture.root, "bin", "plugin"))
	raw, err := runFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, "notification", "write", "run"))
	if err != nil || raw != `{"allowed":false,"lapsed":"package_changed"}` {
		t.Fatalf("lapsed authorize = %s %v", raw, err)
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_authorize", authorizeArgs(t, "notification", "write", "run"), &result)
	if result.Allowed || result.Lapsed != "" {
		t.Fatalf("second authorize after lapse = %+v", result)
	}
}

func TestFullTrustListAndAudit(t *testing.T) {
	fixture := fullTrustManager(t)
	captureHostUI(t)
	manager := fixture.manager
	var list struct {
		Plugins []map[string]any `json:"plugins"`
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_list", `{}`, &list)
	if list.Plugins == nil || len(list.Plugins) != 0 {
		t.Fatalf("list = %+v", list)
	}
	view := grantByCommand(t, manager, fullTrustDemoID)
	mustFullTrustCommand(t, manager, "plugin_full_trust_list", ``, &list)
	if len(list.Plugins) != 1 || list.Plugins[0]["pluginId"] != fullTrustDemoID || list.Plugins[0]["name"] != "Trust Demo" ||
		list.Plugins[0]["version"] != "1.0.0" || int64(list.Plugins[0]["grantedAt"].(float64)) != view.GrantedAt {
		t.Fatalf("list = %+v", list)
	}
	if _, err := runFullTrustCommand(t, manager, "plugin_full_trust_list", `{"x":1}`); err == nil {
		t.Fatal("list accepted an unknown field")
	}

	var audit struct {
		Entries []FullTrustAuditEntry `json:"entries"`
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_audit", `{"pluginId":"trust.demo"}`, &audit)
	if len(audit.Entries) != 1 || audit.Entries[0].Kind != "granted" {
		t.Fatalf("audit = %+v", audit)
	}
	raw, err := runFullTrustCommand(t, manager, "plugin_full_trust_audit_clear", `{"pluginId":"trust.demo"}`)
	if err != nil || raw != "null" {
		t.Fatalf("clear = %s %v", raw, err)
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_audit", `{"pluginId":"trust.demo"}`, &audit)
	if audit.Entries == nil || len(audit.Entries) != 0 {
		t.Fatalf("audit after clear = %+v", audit)
	}

	// A lapsed record drops out of the list.
	rewriteByte(t, filepath.Join(fixture.root, "plugin.json"))
	mustFullTrustCommand(t, manager, "plugin_full_trust_list", `{}`, &list)
	if len(list.Plugins) != 0 {
		t.Fatalf("lapsed plugin listed: %+v", list)
	}
}

func TestFullTrustLifecycleRevokes(t *testing.T) {
	t.Run("disable", func(t *testing.T) {
		fixture := fullTrustManager(t)
		captureHostUI(t)
		grantByCommand(t, fixture.manager, fullTrustDemoID)
		if err := fixture.manager.SetEnabled(context.Background(), fullTrustDemoID, false); err != nil {
			t.Fatal(err)
		}
		if fullTrustRecordFound(t, fullTrustDemoID) {
			t.Fatal("disable kept the record")
		}
		entries := fullTrustAudit(t, fullTrustDemoID)
		if last := entries[len(entries)-1]; last.Kind != "lapsed" || last.Reason != "disabled" {
			t.Fatalf("audit = %+v", entries)
		}
		if err := fixture.manager.SetEnabled(context.Background(), fullTrustDemoID, true); err != nil {
			t.Fatal(err)
		}
		if status, err := fixture.manager.fullTrustStatus(fullTrustDemoID); err != nil || status.Trusted || fullTrustRecordFound(t, fullTrustDemoID) {
			t.Fatalf("re-enable restored trust: %+v %v", status, err)
		}
	})
	t.Run("disabled on read", func(t *testing.T) {
		fixture := fullTrustManager(t)
		captureHostUI(t)
		grantByCommand(t, fixture.manager, fullTrustDemoID)
		fixture.setEnabled(t, false)
		if status, _ := fixture.manager.fullTrustStatus(fullTrustDemoID); status.Lapsed != "disabled" {
			t.Fatalf("lapse = %+v", status)
		}
	})
	t.Run("uninstall", func(t *testing.T) {
		fixture := fullTrustManager(t)
		captureHostUI(t)
		grantByCommand(t, fixture.manager, fullTrustDemoID)
		if err := fixture.manager.Uninstall(context.Background(), fullTrustDemoID, false); err != nil {
			t.Fatal(err)
		}
		assertFullTrustKeysGone(t, fullTrustDemoID)
	})
	t.Run("clear data", func(t *testing.T) {
		fixture := fullTrustManager(t)
		captureHostUI(t)
		grantByCommand(t, fixture.manager, fullTrustDemoID)
		if err := fixture.manager.ClearPluginData(context.Background(), fullTrustDemoID); err != nil {
			t.Fatal(err)
		}
		assertFullTrustKeysGone(t, fullTrustDemoID)
	})
	t.Run("upgrade", func(t *testing.T) {
		manager, id, source := upgradeFixture(t)
		unlockAppForTest(t)
		captureHostUI(t)
		record, _ := manager.record(id)
		record.mu.Lock()
		record.enabled, record.state = true, pluginStateStopped
		record.mu.Unlock()
		if err := writePluginEnabled(id, true); err != nil {
			t.Fatal(err)
		}
		grantByCommand(t, manager, id)
		if _, err := manager.UpgradeLocal(context.Background(), id, source); err != nil {
			t.Fatal(err)
		}
		if fullTrustRecordFound(t, id) {
			t.Fatal("upgrade kept the record")
		}
		entries := fullTrustAudit(t, id)
		if len(entries) != 2 || entries[0].Kind != "granted" || entries[1].Kind != "lapsed" || entries[1].Reason != "upgraded" {
			t.Fatalf("audit = %+v", entries)
		}
	})
}

func assertFullTrustKeysGone(t *testing.T, id string) {
	t.Helper()
	for _, key := range []string{fullTrustKey(id), fullTrustAuditKeyPrefix + id} {
		if _, found, err := pluginKVGetRaw(hostPluginID, key); err != nil || found {
			t.Fatalf("%s still stored (%v)", key, err)
		}
	}
}

func TestFullTrustHasNoHostMethod(t *testing.T) {
	names := []string{}
	for _, registration := range registeredHostMethods() {
		names = append(names, registration.name)
	}
	if len(names) == 0 {
		t.Fatal("no host methods registered")
	}
	file, err := parser.ParseFile(token.NewFileSet(), "process_runtime.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fixed := 0
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		if value, err := strconv.Unquote(literal.Value); err == nil && strings.HasPrefix(value, "host.") {
			names = append(names, value)
			fixed++
		}
		return true
	})
	if fixed == 0 {
		t.Fatal("no fixed host method names found in process_runtime.go")
	}
	for _, name := range names {
		if strings.Contains(strings.ToLower(name), "trust") {
			t.Fatalf("host method %q reaches trust", name)
		}
	}
	commands := core.Commands()
	for _, removed := range []string{"plugin_host_dangerous_action_trust_get", "plugin_host_dangerous_action_trust_set"} {
		if slices.Contains(commands, removed) {
			t.Fatalf("%s is still registered", removed)
		}
	}
	for name := range fullTrustCommands {
		if !slices.Contains(commands, name) {
			t.Fatalf("%s is not registered", name)
		}
	}
}

func TestPluginListCarriesFullTrust(t *testing.T) {
	fixture := fullTrustManager(t)
	captureHostUI(t)
	record := fixture.record(t)
	if raw := jsonOf(t, fixture.manager.recordMeta(record)); strings.Contains(raw, "fullTrust") {
		t.Fatalf("untrusted meta carries fullTrust: %s", raw)
	}
	grantByCommand(t, fixture.manager, fullTrustDemoID)
	meta := fixture.manager.recordMeta(record)
	if !meta.FullTrust || !strings.Contains(jsonOf(t, meta), `"fullTrust":true`) {
		t.Fatalf("trusted meta = %+v", meta)
	}
}

func TestFullTrustPermissionsDigestIgnoresDuplicatePermissions(t *testing.T) {
	manifest := fullTrustDemoManifest()
	manifest.Contributes.Actions[0].Permissions = []api.Capability{api.CapabilityStorage}
	once := fullTrustPermissionsDigest(&manifest)
	manifest.Contributes.Actions[0].Permissions = []api.Capability{api.CapabilityStorage, api.CapabilityStorage}
	if twice := fullTrustPermissionsDigest(&manifest); twice != once {
		t.Fatal("a repeated permission changed the permissions digest")
	}
}

// countFullTrustHashes counts package digests from now on.
func countFullTrustHashes(t *testing.T) *int {
	t.Helper()
	count := 0
	previous := fullTrustHashPackage
	fullTrustHashPackage = func(root string) (string, error) {
		count++
		return previous(root)
	}
	t.Cleanup(func() { fullTrustHashPackage = previous })
	return &count
}

func TestFullTrustHashesOnlyWhenEnabling(t *testing.T) {
	fixture := fullTrustManager(t)
	captureHostUI(t)
	manager := fixture.manager
	hashes := countFullTrustHashes(t)

	// Plugin list, card mount and popover show of an untrusted plugin.
	if meta := manager.recordMeta(fixture.record(t)); meta.FullTrust {
		t.Fatal("untrusted plugin listed as trusted")
	}
	var view fullTrustView
	mustFullTrustCommand(t, manager, "plugin_full_trust_get", `{"pluginId":"trust.demo"}`, &view)
	if view.Trusted || !view.Subject.Eligible || view.Subject.Fingerprint != "" || view.Subject.Name != "Trust Demo" {
		t.Fatalf("cheap get = %+v", view)
	}
	var list struct {
		Plugins []fullTrustListEntry `json:"plugins"`
	}
	mustFullTrustCommand(t, manager, "plugin_full_trust_list", `{}`, &list)
	if *hashes != 0 {
		t.Fatalf("listing or mounting hashed the package %d times", *hashes)
	}
	fixture.setEnabled(t, false)
	mustFullTrustCommand(t, manager, "plugin_full_trust_get", `{"pluginId":"trust.demo"}`, &view)
	if view.Subject.Eligible || view.Subject.Reason != "disabled" || *hashes != 0 {
		t.Fatalf("cheap get of a disabled plugin = %+v (hashes %d)", view, *hashes)
	}
	fixture.setEnabled(t, true)

	// Starting to enable computes the fingerprint.
	mustFullTrustCommand(t, manager, "plugin_full_trust_get", `{"pluginId":"trust.demo","full":true}`, &view)
	if *hashes != 1 || len(view.Subject.Fingerprint) != 64 || !view.Subject.Eligible {
		t.Fatalf("full get = %+v (hashes %d)", view, *hashes)
	}
}

func TestFullTrustLogsUnreadablePackageOnce(t *testing.T) {
	fixture := fullTrustManager(t)
	var output bytes.Buffer
	previousOutput, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(previousOutput); log.SetFlags(previousFlags) })
	fullTrustUnreadableLogged.Delete(fullTrustDemoID)
	t.Cleanup(func() { fullTrustUnreadableLogged.Delete(fullTrustDemoID) })
	if err := os.Symlink(filepath.Join(fixture.root, "bin", "plugin"), filepath.Join(fixture.root, "ui", "link")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	for range 3 {
		if subject, _ := fixture.manager.fullTrustSubject(fullTrustDemoID); subject.Reason != "package_unreadable" {
			t.Fatalf("subject = %+v", subject)
		}
	}
	if got := strings.Count(output.String(), "full trust package digest"); got != 1 {
		t.Fatalf("logged %d times:\n%s", got, output.String())
	}
}

func TestFullTrustRevokingLegacyOnlyLeavesNoAudit(t *testing.T) {
	fixture := fullTrustManager(t)
	events := captureHostUI(t)
	if err := fixture.manager.setDangerousActionsTrusted(fullTrustDemoID, true); err != nil {
		t.Fatal(err)
	}
	var view fullTrustView
	mustFullTrustCommand(t, fixture.manager, "plugin_full_trust_set", `{"pluginId":"trust.demo","trusted":false}`, &view)
	if view.LegacyDangerous || legacyTrustFound(t, fullTrustDemoID) {
		t.Fatal("legacy trust kept")
	}
	if entries := fullTrustAudit(t, fullTrustDemoID); len(entries) != 0 {
		t.Fatalf("audit = %+v", entries)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	if len(events.events) != 0 {
		t.Fatalf("events = %+v", events.events)
	}
}

func TestFullTrustDoesNotSurviveFolderRemoval(t *testing.T) {
	useTemporaryPluginData(t)
	unlockAppForTest(t)
	captureHostUI(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	source := writeTestManifest(t, nil)
	const id = "example.plugin"
	target := filepath.Join(pluginsDirectory(), id)
	sync := func() {
		t.Helper()
		if err := manager.syncInstalled(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	enable := func() {
		t.Helper()
		record, err := manager.record(id)
		if err != nil {
			t.Fatal(err)
		}
		record.mu.Lock()
		record.enabled, record.state = true, pluginStateStopped
		record.mu.Unlock()
	}
	if err := copyPluginTree(source, target); err != nil {
		t.Fatal(err)
	}
	sync()
	enable()
	grantByCommand(t, manager, id)
	stored, _, err := pluginKVGetRaw(hostPluginID, fullTrustKey(id))
	if err != nil {
		t.Fatal(err)
	}

	// Deleted while the app runs.
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	sync()
	if fullTrustRecordFound(t, id) {
		t.Fatal("folder removal kept the record")
	}
	entries := fullTrustAudit(t, id)
	if last := entries[len(entries)-1]; last.Kind != "lapsed" || last.Reason != "uninstalled" {
		t.Fatalf("audit = %+v", entries)
	}

	// Deleted while the app was closed: the record is still stored when the
	// same package appears again.
	if err := pluginKVSetRaw(hostPluginID, fullTrustKey(id), stored); err != nil {
		t.Fatal(err)
	}
	if err := copyPluginTree(source, target); err != nil {
		t.Fatal(err)
	}
	sync()
	enable()
	if fullTrustRecordFound(t, id) {
		t.Fatal("a reappearing package inherited full trust")
	}
	if status, err := manager.fullTrustStatus(id); err != nil || status.Trusted {
		t.Fatalf("status = %+v %v", status, err)
	}
}
