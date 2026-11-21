package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	backendsftp "mincode/app/backend/sftp"
	"mincode/app/plugins/api"
)

// servicesCommandCall runs a plugin_services_* handler with args encoded as
// JSON and decodes its result into a map.
func servicesCommandCall(t *testing.T, handler func(string, string) (string, error), args any) (map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := handler("", string(raw))
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("result %s: %v", result, err)
	}
	return decoded, nil
}

// servicesCommandStart starts a host call and returns its call ID.
func servicesCommandStart(t *testing.T, args map[string]any) string {
	t.Helper()
	started, err := servicesCommandCall(t, pluginServicesStartHandler, args)
	if err != nil {
		t.Fatal(err)
	}
	if started["state"] != serviceCallRunning {
		t.Fatalf("started = %v", started)
	}
	return started["callId"].(string)
}

func wantCommandCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), code+":") && err.Error() != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

// noPromptEvents fails when a prompt was shown.
func noPromptEvents(t *testing.T, events *capturedUIEvents) {
	t.Helper()
	events.mu.Lock()
	defer events.mu.Unlock()
	for _, event := range events.events {
		if event.name == pluginPromptEvent {
			t.Fatalf("unexpected prompt %+v", event.data)
		}
	}
}

func setProviderSurfaces(fixture *serviceCallFixture, surfaces ...string) {
	record := fixture.record
	record.mu.Lock()
	defer record.mu.Unlock()
	entry := servicesLocked(record)[0]
	entry.Surfaces = surfaces
	record.manifest.contributions[servicePointKey] = &resolvedContribution{Entries: []any{entry}}
	fixture.provider.Entry = entry
}

func TestServicesCommandList(t *testing.T) {
	useTemporaryPluginData(t)
	alpha := serviceRegistryRecord(t, "alpha-tools", "Alpha Tools")
	zeta := serviceRegistryRecord(t, "zeta-tools", "Zeta Tools")
	useServicesManager(t, serviceRegistryManager(alpha, zeta))
	if _, err := servicesCommandCall(t, pluginServicesDefaultSetHandler, map[string]any{"service": "demo.text.reverse", "pluginId": "zeta-tools"}); err != nil {
		t.Fatal(err)
	}
	listed, err := servicesCommandCall(t, pluginServicesListHandler, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	services := listed["services"].([]any)
	if len(services) != 2 {
		t.Fatalf("services = %v", services)
	}
	first := services[0].(map[string]any)
	for _, key := range []string{"pluginId", "pluginName", "entryId", "service", "title", "description", "input", "output", "surfaces", "risk", "timeoutMs", "isDefault"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("entry lacks %q: %v", key, first)
		}
	}
	if first["pluginId"] != "alpha-tools" || first["pluginName"] != "Alpha Tools" || first["service"] != "demo.text.reverse" || first["isDefault"] != false {
		t.Fatalf("first = %v", first)
	}
	if localized, _ := first["titleLocalized"].(map[string]any); localized["zh-CN"] != "反转文字" {
		t.Fatalf("titleLocalized = %v", first["titleLocalized"])
	}
	if second := services[1].(map[string]any); second["pluginId"] != "zeta-tools" || second["isDefault"] != true {
		t.Fatalf("second = %v", second)
	}
}

func TestServicesCommandDefaultSet(t *testing.T) {
	useTemporaryPluginData(t)
	alpha := serviceRegistryRecord(t, "alpha-tools", "Alpha Tools")
	useServicesManager(t, serviceRegistryManager(alpha))
	set, err := servicesCommandCall(t, pluginServicesDefaultSetHandler, map[string]any{"service": "demo.text.reverse", "pluginId": "alpha-tools"})
	if err != nil {
		t.Fatal(err)
	}
	if defaults := set["defaults"].(map[string]any); defaults["demo.text.reverse"] != "alpha-tools" {
		t.Fatalf("defaults = %v", defaults)
	}
	for name, args := range map[string]map[string]any{
		"not a provider": {"service": "demo.text.reverse", "pluginId": "zeta-tools"},
		"bad service":    {"service": "Bad Name", "pluginId": "alpha-tools"},
		"bad plugin":     {"service": "demo.text.reverse", "pluginId": "A B"},
	} {
		if _, err := servicesCommandCall(t, pluginServicesDefaultSetHandler, args); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	cleared, err := servicesCommandCall(t, pluginServicesDefaultSetHandler, map[string]any{"service": "demo.text.reverse", "pluginId": ""})
	if err != nil {
		t.Fatal(err)
	}
	if defaults := cleared["defaults"].(map[string]any); len(defaults) != 0 {
		t.Fatalf("cleared defaults = %v", defaults)
	}
}

func TestServicesCommandStartPaths(t *testing.T) {
	events := captureHostUI(t)
	entry := checksumServiceEntry()
	entry.Surfaces = []string{serviceSurfacePalette}
	fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", entry, 16384)
	useServicesManager(t, fixture.manager)
	content := []byte(strings.Repeat("snapshot bytes ", 1000))
	source := filepath.Join(t.TempDir(), "input.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	callID := servicesCommandStart(t, map[string]any{"service": "demo.file.checksum", "surface": "palette", "paths": []string{source}})
	// The host took a snapshot: the source can go away while the call runs.
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	sum := sha256.Sum256(content)
	if final.State != serviceCallSucceeded || outputText(t, final) != hex.EncodeToString(sum[:]) {
		t.Fatalf("final = %+v", final)
	}
	status, err := servicesCommandCall(t, pluginServicesStatusHandler, map[string]any{"callId": callID})
	if err != nil || status["state"] != serviceCallSucceeded {
		t.Fatalf("status %v %v", status, err)
	}
	encoded, _ := json.Marshal(status)
	if strings.Contains(string(encoded), filepath.Dir(source)) {
		t.Fatalf("snapshot leaks the source path: %s", encoded)
	}
	noPromptEvents(t, events)

	for name, args := range map[string]map[string]any{
		"relative path":  {"service": "demo.file.checksum", "surface": "palette", "paths": []string{"input.bin"}},
		"missing file":   {"service": "demo.file.checksum", "surface": "palette", "paths": []string{source}},
		"directory":      {"service": "demo.file.checksum", "surface": "palette", "paths": []string{t.TempDir()}},
		"not accepted":   {"service": "demo.file.checksum", "surface": "palette", "paths": []string{writeTempFile(t, "a.txt", "x")}},
		"text for files": {"service": "demo.file.checksum", "surface": "palette", "input": "x"},
		"unknown":        {"service": "demo.file.checksum", "surface": "toolbar", "paths": []string{writeTempFile(t, "a.bin", "x")}},
	} {
		if _, err := servicesCommandCall(t, pluginServicesStartHandler, args); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return target
}

// TestServiceSurfacesWithoutWorkspaceRead: fileMenu and selection hand the
// provider only what the user picked there (a snapshot token or the selected
// text, never a path), so they need no workspace.read (design §2b).
func TestServiceSurfacesWithoutWorkspaceRead(t *testing.T) {
	t.Run("default surfaces", testServiceDefaultSurfacesWithoutWorkspaceRead)
	t.Run("host fileMenu call", testServiceHostFileMenuWithoutWorkspaceRead)
}

func testServiceDefaultSurfacesWithoutWorkspaceRead(t *testing.T) {
	useTemporaryPluginData(t)
	withoutRead := []string{"commands", "ui"}
	manifest, err := readManifestStrict(writeServicePackage(t, []any{
		serviceEntry(nil),
		fileServiceEntry(func(e map[string]any) { e["id"] = "scan"; e["service"] = "demo.file.scan" }),
	}, withoutRead, nil))
	if err != nil {
		t.Fatal(err)
	}
	services := servicesLocked(&pluginRecord{manifest: &manifest})
	if len(services) != 2 || !reflect.DeepEqual(services[0].Surfaces, []string{"selection", "palette"}) ||
		!reflect.DeepEqual(services[1].Surfaces, []string{"fileMenu", "palette"}) {
		t.Fatalf("services = %+v", services)
	}
}

func testServiceHostFileMenuWithoutWorkspaceRead(t *testing.T) {
	events := captureHostUI(t)
	entry := checksumServiceEntry()
	entry.Surfaces = []string{serviceSurfaceFileMenu}
	fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", entry, 16384)
	useServicesManager(t, fixture.manager)
	if manifestHasCapability(fixture.record.manifest, api.CapabilityWorkspaceRead) {
		t.Fatal("fixture provider declares workspace.read")
	}
	content := "file menu bytes"
	callID := servicesCommandStart(t, map[string]any{"service": "demo.file.checksum", "surface": "fileMenu", "paths": []string{writeTempFile(t, "a.bin", content)}})
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	sum := sha256.Sum256([]byte(content))
	if final.State != serviceCallSucceeded || outputText(t, final) != hex.EncodeToString(sum[:]) {
		t.Fatalf("fileMenu = %+v", final)
	}
	// A surface the entry does not list is still refused.
	_, err := servicesCommandCall(t, pluginServicesStartHandler, map[string]any{"service": "demo.file.checksum", "surface": "palette", "paths": []string{writeTempFile(t, "b.bin", content)}})
	if err == nil {
		t.Fatal("unlisted surface accepted")
	}
	noPromptEvents(t, events)
}

func TestServicesCommandDangerousConfirm(t *testing.T) {
	events := captureHostUI(t)
	entry := serviceTestEntry()
	entry.Risk = "dangerous"
	fixture := serviceCallProvider(t, "service.short", entry, 16384)
	makeServiceProviderTrustable(t, fixture)
	useServicesManager(t, fixture.manager)
	start := map[string]any{"service": "demo.text.reverse", "surface": "palette", "input": "abc"}

	// Not trusted: the user confirms each dangerous call.
	go answerNext(t, events, func(event promptEvent) promptReply {
		if event.Kind != serviceConfirmPromptKind || event.PluginID != serviceCallHostOwner || event.Call == nil ||
			event.Call.Risk != "dangerous" || event.Call.TargetPluginID != "service.short" || event.Call.Service != "demo.text.reverse" ||
			event.Call.ServiceTitle != "Reverse" || event.Call.Payload != "abc" {
			t.Errorf("confirm prompt %+v %+v", event, event.Call)
		}
		return promptReply{IDs: []string{"once"}}
	})
	callID := servicesCommandStart(t, start)
	if final := waitServiceCall(t, serviceCallHostOwner, callID); final.State != serviceCallSucceeded {
		t.Fatalf("confirmed = %+v", final)
	}
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{Cancelled: true} })
	_, err := servicesCommandCall(t, pluginServicesStartHandler, start)
	wantCommandCode(t, err, contributionErrCancelled)

	// Full trust in the provider: no prompt, the skip is audited.
	setLocked := unlockAppForTest(t)
	subject, err := fixture.manager.fullTrustSubject("service.short")
	if err != nil || !subject.Eligible {
		t.Fatalf("subject = %+v %v", subject, err)
	}
	if _, err := fixture.manager.setFullTrust("service.short", true, subject.Fingerprint); err != nil {
		t.Fatal(err)
	}
	unprompted := func(label string) {
		t.Helper()
		stop := refusePrompts(t, events)
		started, err := servicesCommandCall(t, pluginServicesStartHandler, start)
		stop()
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if final := waitServiceCall(t, serviceCallHostOwner, started["callId"].(string)); final.State != serviceCallSucceeded {
			t.Fatalf("%s = %+v", label, final)
		}
		noPromptEvents(t, events)
	}
	prompted := func(label string) {
		t.Helper()
		go answerNext(t, events, func(event promptEvent) promptReply {
			if event.Kind != serviceConfirmPromptKind || event.Call == nil || event.Call.Risk != "dangerous" {
				t.Errorf("%s: prompt %+v", label, event)
			}
			return promptReply{IDs: []string{"once"}}
		})
		callID := servicesCommandStart(t, start)
		if final := waitServiceCall(t, serviceCallHostOwner, callID); final.State != serviceCallSucceeded {
			t.Fatalf("%s = %+v", label, final)
		}
	}
	unprompted("full trust")
	entries := skippedAudit(t, "service.short")
	if len(entries) != 1 || entries[0].Surface != "hostService" || entries[0].Risk != "dangerous" || entries[0].Subject != "demo.text.reverse" ||
		entries[0].Counterpart != "" || entries[0].Reason != "" {
		t.Fatalf("full trust audit = %+v", entries)
	}
	// Locked: neither full trust nor the legacy record applies.
	setLocked(true)
	prompted("full trust while locked")
	setLocked(false)
	if _, err := fixture.manager.setFullTrust("service.short", false, ""); err != nil {
		t.Fatal(err)
	}

	// Only a legacy dangerous-action record (earlier versions): no prompt,
	// audited with reason "legacy".
	if err := fixture.manager.setDangerousActionsTrusted("service.short", true); err != nil {
		t.Fatal(err)
	}
	unprompted("legacy")
	entries = skippedAudit(t, "service.short")
	if last := entries[len(entries)-1]; len(entries) != 2 || last.Surface != "hostService" || last.Risk != "dangerous" ||
		last.Subject != "demo.text.reverse" || last.Reason != "legacy" {
		t.Fatalf("legacy audit = %+v", entries)
	}
	setLocked(true)
	prompted("legacy while locked")
	setLocked(false)

	// Both gone: asked again.
	if err := fixture.manager.setDangerousActionsTrusted("service.short", false); err != nil {
		t.Fatal(err)
	}
	prompted("revoked")
	if entries := skippedAudit(t, "service.short"); len(entries) != 2 {
		t.Fatalf("audit after revoke = %+v", entries)
	}

	// Write needs no confirmation from the host, trusted or not.
	setServiceRisk(fixture, "write", true)
	callID = servicesCommandStart(t, start)
	if final := waitServiceCall(t, serviceCallHostOwner, callID); final.State != serviceCallSucceeded {
		t.Fatalf("write = %+v", final)
	}
	noPromptEvents(t, events)
}

func TestServicesCommandOutputSave(t *testing.T) {
	captureHostUI(t)
	entry := createServiceEntry()
	entry.Surfaces = []string{serviceSurfacePalette}
	fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", entry, 16384)
	useServicesManager(t, fixture.manager)
	callID := servicesCommandStart(t, map[string]any{"service": "demo.file.create", "surface": "palette", "input": "create:hello host"})
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	if final.State != serviceCallSucceeded || len(final.Files) != 1 {
		t.Fatalf("final = %+v", final)
	}
	token := final.Files[0].Token
	dir := t.TempDir()
	target := filepath.Join(dir, "saved.txt")
	for name, args := range map[string]map[string]any{
		"relative path": {"callId": callID, "token": token, "path": "saved.txt"},
		"unknown token": {"callId": callID, "token": serviceExchangeTokenPrefix + strings.Repeat("0", 48), "path": target},
		"bad call":      {"callId": serviceCallIDPrefix + strings.Repeat("0", 48), "token": token, "path": target},
	} {
		if _, err := servicesCommandCall(t, pluginServicesOutputSaveHandler, args); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := servicesCommandCall(t, pluginServicesOutputSaveHandler, map[string]any{"callId": callID, "token": token, "path": target}); err != nil {
		t.Fatal(err)
	}
	if saved, err := os.ReadFile(target); err != nil || string(saved) != "hello host" {
		t.Fatalf("saved %q %v", saved, err)
	}
	// Plugins never reach the host's outputs, not even one named "host".
	for _, pluginID := range []string{"host", serviceFilesHelperID} {
		if _, err := serviceFiles.read(pluginID, token, 0, 16); err == nil {
			t.Fatalf("%s read a host output", pluginID)
		}
	}
	if _, err := servicesCommandCall(t, pluginServicesReleaseHandler, map[string]any{"callId": callID}); err != nil {
		t.Fatal(err)
	}
	if _, err := serviceFiles.read(serviceCallHostOwner, token, 0, 16); err == nil {
		t.Fatal("released output still readable")
	}
	if _, err := servicesCommandCall(t, pluginServicesOutputSaveHandler, map[string]any{"callId": callID, "token": token, "path": filepath.Join(dir, "again.txt")}); err == nil {
		t.Fatal("released output saved")
	}
}

// Ruling R13: the host's owner name is not a plugin ID, so a plugin with the
// ID "host" cannot see, cancel or read host calls, and dropping it leaves
// the host's outputs alone.
func TestServiceCallHostOwnerIsNotAPluginID(t *testing.T) {
	if pluginIDPattern.MatchString(serviceCallHostOwner) {
		t.Fatalf("serviceCallHostOwner %q is a legal plugin ID", serviceCallHostOwner)
	}
	captureHostUI(t)
	fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", createServiceEntry(), 16384)
	setProviderSurfaces(fixture, serviceSurfacePalette)
	request := serviceCallRequest{Caller: serviceCaller{Kind: "host", Surface: serviceSurfacePalette}, Provider: fixture.provider, Input: json.RawMessage(`"create:host data"`)}
	final, err := fixture.manager.runServiceCallSync(context.Background(), request)
	if err != nil || final.State != serviceCallSucceeded || len(final.Files) != 1 {
		t.Fatalf("final = %+v, %v", final, err)
	}
	_, err = serviceCallStatus("host", final.CallID)
	wantContributionCode(t, err, contributionErrNotFound)
	_, err = cancelServiceCall("host", final.CallID)
	wantContributionCode(t, err, contributionErrNotFound)
	token := final.Files[0].Token
	if _, err := serviceFiles.read("host", token, 0, 16); err == nil {
		t.Fatal(`plugin "host" read a host output`)
	}
	serviceFiles.release("host", token)
	serviceFiles.dropPlugin("host")
	if data, err := serviceFiles.read(serviceCallHostOwner, token, 0, 16); err != nil || string(data) != "host data" {
		t.Fatalf("host output after dropPlugin(\"host\"): %q %v", data, err)
	}
	// A plugin caller named like the host owner is refused.
	_, err = fixture.manager.startServiceCall(context.Background(), serviceCallRequest{Caller: serviceCaller{Kind: "plugin", PluginID: serviceCallHostOwner}, Provider: fixture.provider})
	wantContributionCode(t, err, contributionErrInvalidRequest)
}

func TestServicesCommandProviderChoice(t *testing.T) {
	events := captureHostUI(t)
	candidates := []serviceProvider{
		{PluginID: "alpha-tools", PluginName: "Alpha", Entry: ServiceContribution{ID: "a", Title: "Alpha reverse"}},
		{PluginID: "zeta-tools", PluginName: "Zeta", Entry: ServiceContribution{ID: "z", Title: "Zeta reverse"}},
	}
	var failure error
	choose := hostServiceProviderChooser(time.Now().Add(5*time.Second), &failure)
	go answerNext(t, events, func(event promptEvent) promptReply {
		if event.Kind != serviceProviderPromptKind || event.PluginID != serviceCallHostOwner || event.Service != "demo.text.reverse" || len(event.Items) != 2 {
			t.Errorf("choice %+v", event)
		}
		return promptReply{IDs: []string{event.Items[0].ID, serviceProviderRememberID}}
	})
	chosen, remember, err := choose(context.Background(), "demo.text.reverse", candidates)
	if err != nil || chosen.PluginID != "alpha-tools" || !remember || failure != nil {
		t.Fatalf("chosen %+v %v %v %v", chosen, remember, err, failure)
	}
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{Cancelled: true} })
	if _, _, err := choose(context.Background(), "demo.text.reverse", candidates); err == nil {
		t.Fatal("cancelled choice picked a provider")
	}
	wantCommandCode(t, failure, contributionErrCancelled)

	// Only providers that offer the surface and take the input are offered.
	text := ServiceContribution{ID: "t", Input: ServiceShape{Kind: serviceInputText, MaxBytes: 8}, Surfaces: []string{serviceSurfaceSelection, serviceSurfacePalette}}
	files := ServiceContribution{ID: "f", Input: ServiceShape{Kind: serviceInputFiles, Accepts: []string{".png"}, MaxFiles: 2, MaxBytes: 1 << 20}, Surfaces: []string{serviceSurfaceFileMenu}}
	cases := []struct {
		entry   ServiceContribution
		request hostServiceInput
		want    bool
	}{
		{text, hostServiceInput{surface: serviceSurfaceSelection, text: json.RawMessage(`"x"`)}, true},
		{text, hostServiceInput{surface: serviceSurfaceFileMenu, text: json.RawMessage(`"x"`)}, false},
		{text, hostServiceInput{surface: serviceSurfacePalette, names: []string{"a.png"}}, false},
		{files, hostServiceInput{surface: serviceSurfaceFileMenu, names: []string{"a.png", "b.PNG"}}, true},
		{files, hostServiceInput{surface: serviceSurfaceFileMenu, names: []string{"a.png", "b.txt"}}, false},
		{files, hostServiceInput{surface: serviceSurfaceFileMenu, names: []string{"a.png", "b.png", "c.png"}}, false},
		{files, hostServiceInput{surface: serviceSurfaceFileMenu, text: json.RawMessage(`"x"`)}, false},
	}
	for index, item := range cases {
		if got := item.request.fits(item.entry); got != item.want {
			t.Fatalf("case %d: fits = %v", index, got)
		}
	}
}

func TestServicesCommandDeclared(t *testing.T) {
	useTemporaryPluginData(t)
	active := serviceRegistryRecord(t, "alpha-tools", "Alpha Tools")
	disabled := serviceRegistryRecord(t, "beta-tools", "Beta Tools")
	disabled.enabled = false
	disabled.state = pluginStateDisabled
	scoped := serviceRegistryRecord(t, "gamma-tools", "Gamma Tools")
	restrictToOtherWorkspace(scoped)
	failed := serviceRegistryRecord(t, "delta-tools", "Delta Tools")
	failed.state = pluginStateError
	useServicesManager(t, serviceRegistryManager(active, disabled, scoped, failed))
	if _, err := servicesCommandCall(t, pluginServicesDefaultSetHandler, map[string]any{"service": "demo.text.reverse", "pluginId": "alpha-tools"}); err != nil {
		t.Fatal(err)
	}

	declared := func(pluginID string) []any {
		t.Helper()
		result, err := servicesCommandCall(t, pluginServicesDeclaredHandler, map[string]any{"pluginId": pluginID})
		if err != nil {
			t.Fatal(err)
		}
		services, ok := result["services"].([]any)
		if !ok {
			t.Fatalf("result = %v", result)
		}
		return services
	}

	activeServices := declared("alpha-tools")
	if len(activeServices) != 1 {
		t.Fatalf("active services = %v", activeServices)
	}
	first := activeServices[0].(map[string]any)
	if first["active"] != true || first["isDefault"] != true || first["service"] != "demo.text.reverse" || first["pluginName"] != "Alpha Tools" {
		t.Fatalf("active entry = %v", first)
	}
	if _, ok := first["inactiveReason"]; ok {
		t.Fatalf("active entry has a reason: %v", first)
	}
	if localized, _ := first["titleLocalized"].(map[string]any); localized["zh-CN"] != "反转文字" {
		t.Fatalf("titleLocalized = %v", first["titleLocalized"])
	}

	for pluginID, reason := range map[string]string{"beta-tools": "disabled", "gamma-tools": "inactiveWorkspace", "delta-tools": "unavailable"} {
		services := declared(pluginID)
		if len(services) != 1 {
			t.Fatalf("%s services = %v", pluginID, services)
		}
		entry := services[0].(map[string]any)
		if entry["active"] != false || entry["inactiveReason"] != reason || entry["entryId"] == "" || entry["isDefault"] != false {
			t.Fatalf("%s entry = %v, want inactive (%s)", pluginID, entry, reason)
		}
	}

	// The effective list is unchanged: only the active plugin's service.
	listed, err := servicesCommandCall(t, pluginServicesListHandler, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if services := listed["services"].([]any); len(services) != 1 || services[0].(map[string]any)["pluginId"] != "alpha-tools" {
		t.Fatalf("listed = %v", services)
	}

	if services := declared("unknown-tools"); len(services) != 0 {
		t.Fatalf("unknown plugin services = %v", services)
	}
	for _, bad := range []any{"", "../etc", 42} {
		_, err := servicesCommandCall(t, pluginServicesDeclaredHandler, map[string]any{"pluginId": bad})
		wantCommandCode(t, err, contributionErrInvalidRequest)
	}
}

// fakeRemoteFile is a remote file served by useRemoteFiles: its reader hands
// out chunks, then blocks until the file is released (or ends when block is
// false). While busy is open the opener waits like a busy FTP session,
// until busy closes or its ctx ends.
type fakeRemoteFile struct {
	reader   *stepReader
	source   io.Reader // read instead of reader when set
	size     int64
	openErr  error
	busy     chan struct{}
	opened   int
	gaveUp   int // opens abandoned because ctx ended while busy
	released chan struct{}
	once     sync.Once
}

func newFakeRemoteFile(size int64, block bool, chunks ...string) *fakeRemoteFile {
	reader := &stepReader{}
	for _, chunk := range chunks {
		reader.chunks = append(reader.chunks, []byte(chunk))
	}
	if block {
		reader.release = make(chan struct{})
	}
	return &fakeRemoteFile{reader: reader, size: size, released: make(chan struct{})}
}

func (file *fakeRemoteFile) release() {
	file.once.Do(func() {
		if file.reader.release != nil {
			close(file.reader.release)
		}
		close(file.released)
	})
}

func (file *fakeRemoteFile) waitReleased(t *testing.T) {
	t.Helper()
	select {
	case <-file.released:
	case <-time.After(5 * time.Second):
		t.Fatal("the remote file was not released")
	}
}

// remoteFiles serves fakeRemoteFiles by remote path.
type remoteFiles struct {
	mu    sync.Mutex
	files map[string]*fakeRemoteFile
}

// counts returns how often path was opened and given up while busy.
func (served *remoteFiles) counts(remotePath string) (opened, gaveUp int) {
	served.mu.Lock()
	defer served.mu.Unlock()
	file := served.files[remotePath]
	return file.opened, file.gaveUp
}

// useRemoteFiles serves files by remote path in place of the SFTP sessions;
// the session ID must be "sess-1".
func useRemoteFiles(t *testing.T, files map[string]*fakeRemoteFile) *remoteFiles {
	t.Helper()
	served := &remoteFiles{files: files}
	previous := serviceOpenRemoteFile
	serviceOpenRemoteFile = func(ctx context.Context, sessionID, remotePath string) (io.ReadCloser, int64, func(), error) {
		served.mu.Lock()
		file := files[remotePath]
		served.mu.Unlock()
		if sessionID != "sess-1" {
			return nil, 0, nil, backendsftp.ErrSessionUnavailable
		}
		if file == nil {
			return nil, 0, nil, backendsftp.ErrRemoteFileNotFound
		}
		if file.busy != nil {
			select {
			case <-file.busy:
			case <-ctx.Done():
				served.mu.Lock()
				file.gaveUp++
				served.mu.Unlock()
				return nil, 0, nil, ctx.Err()
			}
		}
		served.mu.Lock()
		file.opened++
		served.mu.Unlock()
		if file.openErr != nil {
			return nil, 0, nil, file.openErr
		}
		var source io.Reader = file.reader
		if file.source != nil {
			source = file.source
		}
		return io.NopCloser(source), file.size, file.release, nil
	}
	t.Cleanup(func() { serviceOpenRemoteFile = previous })
	return served
}

// remoteServiceProvider is a checksum provider offered on sftpFile that
// accepts .log files of at most maxBytes.
func remoteServiceProvider(t *testing.T, maxBytes int64) *serviceCallFixture {
	t.Helper()
	captureHostUI(t)
	entry := checksumServiceEntry()
	entry.Input.Accepts = []string{".log"}
	entry.Input.MaxBytes = maxBytes
	entry.Surfaces = []string{serviceSurfaceSFTPFile}
	fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", entry, 16384)
	useServicesManager(t, fixture.manager)
	return fixture
}

func remoteStartArgs(remotePath string) map[string]any {
	return map[string]any{"service": "demo.file.checksum", "surface": serviceSurfaceSFTPFile, "remoteFile": map[string]any{"sessionId": "sess-1", "path": remotePath}}
}

func TestHostServiceStartSourceMatrix(t *testing.T) {
	remoteServiceProvider(t, serviceFileMaxBytes)
	file := newFakeRemoteFile(5, false, "hello")
	served := useRemoteFiles(t, map[string]*fakeRemoteFile{"/var/log/app.log": file})
	remote := map[string]any{"sessionId": "sess-1", "path": "/var/log/app.log"}
	content := map[string]any{"text": "{}", "contentType": "application/json"}
	local := writeTempFile(t, "app.log", "local")
	for name, args := range map[string]map[string]any{
		"sftpFile with paths":          {"service": "demo.file.checksum", "surface": "sftpFile", "remoteFile": remote, "paths": []string{local}},
		"sftpFile with input":          {"service": "demo.file.checksum", "surface": "sftpFile", "remoteFile": remote, "input": "x"},
		"sftpFile with content":        {"service": "demo.file.checksum", "surface": "sftpFile", "remoteFile": remote, "content": content},
		"sftpFile paths only":          {"service": "demo.file.checksum", "surface": "sftpFile", "paths": []string{local}},
		"sftpFile input only":          {"service": "demo.file.checksum", "surface": "sftpFile", "input": "x"},
		"sftpFile nothing":             {"service": "demo.file.checksum", "surface": "sftpFile"},
		"palette remoteFile":           {"service": "demo.file.checksum", "surface": "palette", "remoteFile": remote},
		"fileMenu remoteFile":          {"service": "demo.file.checksum", "surface": "fileMenu", "remoteFile": remote},
		"selection content":            {"service": "demo.file.checksum", "surface": "selection", "content": content},
		"dbResult remoteFile":          {"service": "demo.file.checksum", "surface": "dbResult", "remoteFile": remote},
		"httpResponse input":           {"service": "demo.file.checksum", "surface": "httpResponse", "input": "x"},
		"terminalSelection paths":      {"service": "demo.file.checksum", "surface": "terminalSelection", "paths": []string{local}},
		"terminalSelection remoteFile": {"service": "demo.file.checksum", "surface": "terminalSelection", "remoteFile": remote},
		"relative remote path":         {"service": "demo.file.checksum", "surface": "sftpFile", "remoteFile": map[string]any{"sessionId": "sess-1", "path": "var/log/app.log"}},
		"remote root":                  {"service": "demo.file.checksum", "surface": "sftpFile", "remoteFile": map[string]any{"sessionId": "sess-1", "path": "/"}},
		"remote path with NUL":         {"service": "demo.file.checksum", "surface": "sftpFile", "remoteFile": map[string]any{"sessionId": "sess-1", "path": "/a\x00.log"}},
		"missing session":              {"service": "demo.file.checksum", "surface": "sftpFile", "remoteFile": map[string]any{"path": "/var/log/app.log"}},
		"context on an older surface":  {"service": "demo.file.checksum", "surface": "palette", "paths": []string{local}, "surfaceContext": map[string]any{"a": 1}},
		"context not an object":        {"service": "demo.file.checksum", "surface": "sftpFile", "remoteFile": remote, "surfaceContext": []int{1}},
		"context too large":            {"service": "demo.file.checksum", "surface": "sftpFile", "remoteFile": remote, "surfaceContext": map[string]any{"a": strings.Repeat("x", 4096)}},
	} {
		_, err := servicesCommandCall(t, pluginServicesStartHandler, args)
		if err == nil || !strings.HasPrefix(err.Error(), contributionErrInvalidRequest+":") {
			t.Fatalf("%s: err = %v, want invalid_request", name, err)
		}
	}
	if opened, _ := served.counts("/var/log/app.log"); opened != 0 {
		t.Fatalf("a refused request opened the remote file %d times", opened)
	}
	// The one valid combination.
	callID := servicesCommandStart(t, remoteStartArgs("/var/log/app.log"))
	if final := waitServiceCall(t, serviceCallHostOwner, callID); final.State != serviceCallSucceeded {
		t.Fatalf("final = %+v", final)
	}
	file.waitReleased(t)
}

func TestHostServiceRemoteFileAcceptsByRemoteName(t *testing.T) {
	remoteServiceProvider(t, serviceFileMaxBytes)
	content := strings.Repeat("log line\n", 200)
	logFile := newFakeRemoteFile(int64(len(content)), false, content)
	textFile := newFakeRemoteFile(4, false, "text")
	served := useRemoteFiles(t, map[string]*fakeRemoteFile{"/srv/app/logs/app.log": logFile, "/srv/app/logs/app.txt": textFile})

	started, err := servicesCommandCall(t, pluginServicesStartHandler, remoteStartArgs("/srv/app/logs/app.log"))
	if err != nil {
		t.Fatal(err)
	}
	if started["state"] != serviceCallRunning || started["phase"] != serviceCallPhaseInput {
		t.Fatalf("started = %v", started)
	}
	callID := started["callId"].(string)
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	sum := sha256.Sum256([]byte(content))
	if final.State != serviceCallSucceeded || final.Phase != "" || outputText(t, final) != hex.EncodeToString(sum[:]) {
		t.Fatalf("final = %+v", final)
	}
	encoded, _ := json.Marshal(final)
	if strings.Contains(string(encoded), "/srv/app") || strings.Contains(string(encoded), "sess-1") {
		t.Fatalf("snapshot leaks the remote reference: %s", encoded)
	}
	logFile.waitReleased(t)

	// The provider accepts .log only: app.txt finds no provider and is
	// never opened.
	_, err = servicesCommandCall(t, pluginServicesStartHandler, remoteStartArgs("/srv/app/logs/app.txt"))
	if err == nil {
		t.Fatal("app.txt matched a .log provider")
	}
	if opened, _ := served.counts("/srv/app/logs/app.txt"); opened != 0 {
		t.Fatal("a file no provider accepts was opened")
	}
}

// TestHostServiceRemoteFileTooLargeBeforeRead: the size is checked in the
// input phase, after the start returned, and before a single byte is read;
// the call fails too_large.
func TestHostServiceRemoteFileTooLargeBeforeRead(t *testing.T) {
	fixture := remoteServiceProvider(t, 100)
	file := newFakeRemoteFile(101, false, strings.Repeat("x", 101))
	served := useRemoteFiles(t, map[string]*fakeRemoteFile{"/var/log/big.log": file})
	usedBefore := exchangeUsed()
	callID := servicesCommandStart(t, remoteStartArgs("/var/log/big.log"))
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	if final.State != serviceCallFailed || final.Unconfirmed || !strings.HasPrefix(final.Error, contributionErrTooLarge+":") {
		t.Fatalf("final = %+v", final)
	}
	file.waitReleased(t)
	if opened, _ := served.counts("/var/log/big.log"); opened != 1 || file.reader.bytesRead() != 0 {
		t.Fatalf("opened %d, read %d bytes", opened, file.reader.bytesRead())
	}
	if providerStarted(fixture) || exchangeHasCall(callID) {
		t.Fatal("a refused file activated the provider or kept its exchange slot")
	}
	if used := exchangeUsed(); used != usedBefore {
		t.Fatalf("exchange bytes = %d, before %d", used, usedBefore)
	}
}

// TestHostServiceRemoteFileOpenErrors: open failures end the call (the start
// already returned) with a coded English error that names no path.
func TestHostServiceRemoteFileOpenErrors(t *testing.T) {
	remoteServiceProvider(t, serviceFileMaxBytes)
	gone := newFakeRemoteFile(1, false)
	gone.openErr = backendsftp.ErrSessionUnavailable
	folder := newFakeRemoteFile(1, false)
	folder.openErr = backendsftp.ErrRemoteNotRegular
	useRemoteFiles(t, map[string]*fakeRemoteFile{"/gone.log": gone, "/folder.log": folder})
	check := func(args map[string]any, code, remotePath string) {
		t.Helper()
		callID := servicesCommandStart(t, args)
		final := waitServiceCall(t, serviceCallHostOwner, callID)
		if final.State != serviceCallFailed || final.Unconfirmed || !strings.HasPrefix(final.Error, code+":") {
			t.Fatalf("%s: final = %+v, want %s", remotePath, final, code)
		}
		if strings.Contains(final.Error, remotePath) || strings.ContainsFunc(final.Error, func(r rune) bool { return r > 0x7f }) {
			t.Fatalf("%s: error %q names the path or is not plain English", remotePath, final.Error)
		}
		if exchangeHasCall(callID) {
			t.Fatalf("%s: failed call kept its exchange slot", remotePath)
		}
	}
	for remotePath, code := range map[string]string{
		"/gone.log":    contributionErrUnavailable,
		"/folder.log":  contributionErrInvalidRequest,
		"/missing.log": contributionErrNotFound,
	} {
		check(remoteStartArgs(remotePath), code, remotePath)
	}
	args := remoteStartArgs("/gone.log")
	args["remoteFile"] = map[string]any{"sessionId": "other", "path": "/gone.log"}
	check(args, contributionErrUnavailable, "/gone.log")
}

// TestHostServiceRemoteFileStartsWhileSessionBusy: a session held by a
// transfer does not block the start; the call waits in phase "input".
func TestHostServiceRemoteFileStartsWhileSessionBusy(t *testing.T) {
	remoteServiceProvider(t, serviceFileMaxBytes)
	file := newFakeRemoteFile(5, false, "hello")
	file.busy = make(chan struct{})
	served := useRemoteFiles(t, map[string]*fakeRemoteFile{"/var/log/app.log": file})
	returned := make(chan map[string]any, 1)
	go func() {
		started, err := servicesCommandCall(t, pluginServicesStartHandler, remoteStartArgs("/var/log/app.log"))
		if err != nil {
			t.Error(err)
		}
		returned <- started
	}()
	var started map[string]any
	select {
	case started = <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the start waited for the busy session")
	}
	if started["state"] != serviceCallRunning || started["phase"] != serviceCallPhaseInput || started["callId"] == "" {
		t.Fatalf("started = %v", started)
	}
	callID := started["callId"].(string)
	status, err := serviceCallStatus(serviceCallHostOwner, callID)
	if err != nil || status.Phase != serviceCallPhaseInput || status.State != serviceCallRunning {
		t.Fatalf("status while busy = %+v %v", status, err)
	}
	if opened, _ := served.counts("/var/log/app.log"); opened != 0 {
		t.Fatal("opened while the session was busy")
	}
	close(file.busy) // the transfer ends
	if final := waitServiceCall(t, serviceCallHostOwner, callID); final.State != serviceCallSucceeded {
		t.Fatalf("final = %+v", final)
	}
	file.waitReleased(t)
}

func TestHostServiceRemoteFileCancelWhileWaitingForSession(t *testing.T) {
	fixture := remoteServiceProvider(t, serviceFileMaxBytes)
	file := newFakeRemoteFile(5, false, "hello")
	file.busy = make(chan struct{}) // never freed
	served := useRemoteFiles(t, map[string]*fakeRemoteFile{"/var/log/app.log": file})
	usedBefore := exchangeUsed()
	callID := servicesCommandStart(t, remoteStartArgs("/var/log/app.log"))
	if !exchangeHasCall(callID) {
		t.Fatal("the waiting call holds no exchange call")
	}
	if _, err := servicesCommandCall(t, pluginServicesCancelHandler, map[string]any{"callId": callID}); err != nil {
		t.Fatal(err)
	}
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	if final.State != serviceCallCancelled || final.Unconfirmed || final.Error != "" || final.Phase != "" {
		t.Fatalf("final = %+v", final)
	}
	if opened, gaveUp := served.counts("/var/log/app.log"); opened != 0 || gaveUp != 1 {
		t.Fatalf("opened %d, gave up %d", opened, gaveUp)
	}
	if providerStarted(fixture) || exchangeHasCall(callID) {
		t.Fatal("a call cancelled while waiting activated the provider or kept its exchange slot")
	}
	if used := exchangeUsed(); used != usedBefore {
		t.Fatalf("exchange bytes = %d, before %d", used, usedBefore)
	}
}

func TestHostServiceRemoteFileCancelDuringInputPhase(t *testing.T) {
	fixture := remoteServiceProvider(t, serviceFileMaxBytes)
	file := newFakeRemoteFile(1000, true, "first")
	useRemoteFiles(t, map[string]*fakeRemoteFile{"/var/log/app.log": file})
	usedBefore := exchangeUsed()
	callID := servicesCommandStart(t, remoteStartArgs("/var/log/app.log"))
	waitServiceCallSnapshot(t, serviceCallHostOwner, callID, func(snapshot ServiceCallSnapshot) bool {
		return snapshot.Progress != nil && snapshot.Progress.Completed != nil && *snapshot.Progress.Completed == 5
	})
	if !exchangeHasCall(callID) {
		t.Fatal("the input phase holds no exchange call")
	}
	if exchangeUsed() != usedBefore+1000 {
		t.Fatal("the remote file's size is not reserved while it is read")
	}
	if _, err := servicesCommandCall(t, pluginServicesCancelHandler, map[string]any{"callId": callID}); err != nil {
		t.Fatal(err)
	}
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	if final.State != serviceCallCancelled || final.Unconfirmed || final.Error != "" || final.Phase != "" {
		t.Fatalf("final = %+v", final)
	}
	file.waitReleased(t)
	if providerStarted(fixture) {
		t.Fatal("the provider ran for a call cancelled while reading")
	}
	if exchangeHasCall(callID) {
		t.Fatal("the cancelled call kept its exchange slot")
	}
	if used := exchangeUsed(); used != usedBefore {
		t.Fatalf("exchange bytes after cancel = %d, before %d", used, usedBefore)
	}
}

func TestHostServiceRemoteFileStall(t *testing.T) {
	previous := serviceRemoteInputStall
	serviceRemoteInputStall = 50 * time.Millisecond
	t.Cleanup(func() { serviceRemoteInputStall = previous })
	fixture := remoteServiceProvider(t, serviceFileMaxBytes)
	file := newFakeRemoteFile(1000, true, "some")
	useRemoteFiles(t, map[string]*fakeRemoteFile{"/var/log/app.log": file})
	usedBefore := exchangeUsed()
	callID := servicesCommandStart(t, remoteStartArgs("/var/log/app.log"))
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	if final.State != serviceCallFailed || final.Unconfirmed || !strings.HasPrefix(final.Error, contributionErrTimeout+":") {
		t.Fatalf("final = %+v", final)
	}
	file.waitReleased(t)
	if providerStarted(fixture) || exchangeHasCall(callID) {
		t.Fatal("a stalled read activated the provider or kept its exchange slot")
	}
	if used := exchangeUsed(); used != usedBefore {
		t.Fatalf("exchange bytes after the stall = %d, before %d", used, usedBefore)
	}
}

// trickleReader behaves like pkg/sftp's File.Read: each Read returns only
// once p is full (or the data ends), while the data arrives step bytes per
// interval. maxRead records the largest buffer it was asked to fill.
type trickleReader struct {
	mu       sync.Mutex
	data     []byte
	step     int
	interval time.Duration
	maxRead  int
}

func (r *trickleReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	r.maxRead = max(r.maxRead, len(p))
	r.mu.Unlock()
	filled := 0
	for filled < len(p) {
		r.mu.Lock()
		if len(r.data) == 0 {
			r.mu.Unlock()
			if filled == 0 {
				return 0, io.EOF
			}
			return filled, nil
		}
		n := copy(p[filled:], r.data[:min(r.step, len(r.data))])
		r.data = r.data[n:]
		r.mu.Unlock()
		filled += n
		time.Sleep(r.interval) // the network's pace
	}
	return filled, nil
}

// TestHostServiceRemoteFileSlowTrickleIsNotAStall: data that keeps arriving
// slowly never trips the stall timer, because the host reads in pieces small
// enough that one fills well within the stall window. (With 256 KiB pieces a
// single Read of this source would take longer than the window.)
func TestHostServiceRemoteFileSlowTrickleIsNotAStall(t *testing.T) {
	previous := serviceRemoteInputStall
	serviceRemoteInputStall = 100 * time.Millisecond
	t.Cleanup(func() { serviceRemoteInputStall = previous })
	remoteServiceProvider(t, serviceFileMaxBytes)
	content := []byte(strings.Repeat("0123456789abcdef", 8*1024)) // 128 KiB
	trickle := &trickleReader{data: slices.Clone(content), step: 4 * 1024, interval: 5 * time.Millisecond}
	file := newFakeRemoteFile(int64(len(content)), false)
	file.source = trickle
	useRemoteFiles(t, map[string]*fakeRemoteFile{"/var/log/slow.log": file})
	callID := servicesCommandStart(t, remoteStartArgs("/var/log/slow.log"))
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	sum := sha256.Sum256(content)
	if final.State != serviceCallSucceeded || outputText(t, final) != hex.EncodeToString(sum[:]) {
		t.Fatalf("final = %+v", final)
	}
	trickle.mu.Lock()
	maxRead := trickle.maxRead
	trickle.mu.Unlock()
	if maxRead > serviceSnapshotChunkBytes || serviceSnapshotChunkBytes > 32*1024 {
		t.Fatalf("read pieces of %d bytes (chunk %d)", maxRead, serviceSnapshotChunkBytes)
	}
	file.waitReleased(t)
}

// envelopeFileProvider registers pluginID (prefixed serviceEnvelopeHelperPrefix)
// in manager as a file provider of demo.file.inspect that echoes its
// envelope, offered on surfaces and declaring capabilities.
func envelopeFileProvider(t *testing.T, manager *pluginManager, pluginID string, maxBytes int64, surfaces []string, capabilities ...api.Capability) *serviceCallFixture {
	t.Helper()
	entry := checksumServiceEntry()
	entry.ID, entry.Service = "inspect", "demo.file.inspect"
	entry.Input.Accepts = []string{".json", ".html", ".log", "text/*"}
	entry.Input.MaxBytes = maxBytes
	entry.Surfaces = surfaces
	fixture := addServiceProcessProvider(t, manager, pluginID, "TestServiceFilesHelperProcess", entry, 16384)
	fixture.record.mu.Lock()
	fixture.record.manifest.Capabilities = capabilities
	fixture.record.mu.Unlock()
	return fixture
}

func newEnvelopeManager(t *testing.T) *pluginManager {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	captureHostUI(t)
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}, uiHost: newPluginUIHost()}
	useServicesManager(t, manager)
	return manager
}

// echoedEnvelope is what an envelope provider returned for a host call.
type echoedEnvelope struct {
	Envelope struct {
		Input          []exchangeFile    `json:"input"`
		Caller         map[string]string `json:"caller"`
		SurfaceContext json.RawMessage   `json:"surfaceContext"`
	} `json:"envelope"`
	SHA256 string `json:"sha256"`
	raw    string
}

func runEnvelopeCall(t *testing.T, args map[string]any) echoedEnvelope {
	t.Helper()
	callID := servicesCommandStart(t, args)
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	if final.State != serviceCallSucceeded {
		t.Fatalf("final = %+v", final)
	}
	var echoed echoedEnvelope
	echoed.raw = outputText(t, final)
	if err := json.Unmarshal([]byte(echoed.raw), &echoed); err != nil {
		t.Fatalf("output %s: %v", echoed.raw, err)
	}
	return echoed
}

func TestHostServiceContentFileNames(t *testing.T) {
	manager := newEnvelopeManager(t)
	envelopeFileProvider(t, manager, "service.envelope.content", 1<<20, []string{serviceSurfaceDBResult, serviceSurfaceHTTPResponse})

	result := `{"format":"mincode.dbResult","version":1,"columns":[],"rows":[]}`
	// The frontend cannot pick the name or type: a contentType is ignored.
	echoed := runEnvelopeCall(t, map[string]any{"service": "demo.file.inspect", "surface": serviceSurfaceDBResult,
		"content": map[string]any{"text": result, "contentType": "text/html"}})
	if len(echoed.Envelope.Input) != 1 || echoed.Envelope.Input[0].Name != "result.json" || echoed.Envelope.Input[0].Mime != "application/json" ||
		echoed.Envelope.Input[0].Size != int64(len(result)) || echoed.SHA256 != sha256Hex([]byte(result)) {
		t.Fatalf("dbResult envelope = %s", echoed.raw)
	}
	if echoed.Envelope.Caller["surface"] != serviceSurfaceDBResult || echoed.Envelope.SurfaceContext != nil {
		t.Fatalf("dbResult caller / context = %s", echoed.raw)
	}

	for contentType, want := range map[string][2]string{
		"application/vnd.api+json; charset=utf-8": {"response.json", "application/json"},
		"text/html": {"response.html", "text/html"},
	} {
		body := "<p>hello</p>"
		echoed := runEnvelopeCall(t, map[string]any{"service": "demo.file.inspect", "surface": serviceSurfaceHTTPResponse,
			"content": map[string]any{"text": body, "contentType": contentType}, "surfaceContext": httpSurfaceContextFixture(nil)})
		if len(echoed.Envelope.Input) != 1 || echoed.Envelope.Input[0].Name != want[0] || echoed.Envelope.Input[0].Mime != want[1] || echoed.SHA256 != sha256Hex([]byte(body)) {
			t.Fatalf("%s envelope = %s", contentType, echoed.raw)
		}
	}

	// Binary types are refused before any provider is chosen.
	_, err := servicesCommandCall(t, pluginServicesStartHandler, map[string]any{"service": "demo.file.inspect", "surface": serviceSurfaceHTTPResponse,
		"content": map[string]any{"text": "PNG", "contentType": "image/png"}, "surfaceContext": httpSurfaceContextFixture(nil)})
	wantCommandCode(t, err, contributionErrInvalidRequest)
	// Over the surface limits.
	_, err = servicesCommandCall(t, pluginServicesStartHandler, map[string]any{"service": "demo.file.inspect", "surface": serviceSurfaceDBResult,
		"content": map[string]any{"text": strings.Repeat("x", int(serviceDBResultMaxBytes)+1)}})
	wantCommandCode(t, err, contributionErrTooLarge)
	_, err = servicesCommandCall(t, pluginServicesStartHandler, map[string]any{"service": "demo.file.inspect", "surface": serviceSurfaceHTTPResponse,
		"content": map[string]any{"text": strings.Repeat("x", int(serviceHTTPResponseMaxBytes)+1), "contentType": "text/plain"}, "surfaceContext": httpSurfaceContextFixture(nil)})
	wantCommandCode(t, err, contributionErrTooLarge)
	// Over the provider's maxBytes.
	_, err = servicesCommandCall(t, pluginServicesStartHandler, map[string]any{"service": "demo.file.inspect", "surface": serviceSurfaceHTTPResponse,
		"content": map[string]any{"text": strings.Repeat("x", 1<<20+1), "contentType": "text/plain"}, "surfaceContext": httpSurfaceContextFixture(nil)})
	wantCommandCode(t, err, contributionErrTooLarge)
	// httpResponse always carries its response line.
	_, err = servicesCommandCall(t, pluginServicesStartHandler, map[string]any{"service": "demo.file.inspect", "surface": serviceSurfaceHTTPResponse,
		"content": map[string]any{"text": "{}", "contentType": "application/json"}})
	wantCommandCode(t, err, contributionErrInvalidRequest)
}

// TestHostServiceSurfaceContextPerProvider: the same call to providers of one
// service with and without the capabilities delivers the same content, the
// identifiers only to the provider that declares the capability.
func TestHostServiceSurfaceContextPerProvider(t *testing.T) {
	manager := newEnvelopeManager(t)
	surfaces := []string{serviceSurfaceHTTPResponse, serviceSurfaceSFTPFile, serviceSurfaceDBResult}
	envelopeFileProvider(t, manager, "service.envelope.with", 1<<20, surfaces, api.CapabilityRemoteContext)
	envelopeFileProvider(t, manager, "service.envelope.without", 1<<20, surfaces)
	envelopeFileProvider(t, manager, "service.envelope.schema", 1<<20, surfaces, api.CapabilityDatabaseSchema)
	body := `{"ok":true}`
	headers := [][]string{{"Content-Type", "application/json"}, {"Set-Cookie", "sid=secret-cookie"}, {"X-Session-Id", "s-123456"}}
	call := func(pluginID, surface string, extra map[string]any) echoedEnvelope {
		args := map[string]any{"service": "demo.file.inspect", "surface": surface, "provider": map[string]any{"pluginId": pluginID}}
		for key, value := range extra {
			args[key] = value
		}
		return runEnvelopeCall(t, args)
	}
	httpArgs := map[string]any{"content": map[string]any{"text": body, "contentType": "application/json"}, "surfaceContext": httpSurfaceContextFixture(headers)}

	with := call("service.envelope.with", serviceSurfaceHTTPResponse, httpArgs)
	without := call("service.envelope.without", serviceSurfaceHTTPResponse, httpArgs)
	if with.SHA256 != sha256Hex([]byte(body)) || without.SHA256 != sha256Hex([]byte(body)) {
		t.Fatal("the content differs between providers")
	}
	var withContext, withoutContext map[string]any
	_ = json.Unmarshal(with.Envelope.SurfaceContext, &withContext)
	_ = json.Unmarshal(without.Envelope.SurfaceContext, &withoutContext)
	if withContext["request"] == nil || withContext["response"] == nil {
		t.Fatalf("with remote.context: %s", with.Envelope.SurfaceContext)
	}
	if _, present := withoutContext["request"]; present || withoutContext["response"] == nil {
		t.Fatalf("without remote.context: %s", without.Envelope.SurfaceContext)
	}
	for _, context := range []map[string]any{withContext, withoutContext} {
		if redacted := context["response"].(map[string]any)["headersRedacted"]; redacted != float64(2) {
			t.Fatalf("headersRedacted = %v", redacted)
		}
	}
	for _, echoed := range []echoedEnvelope{with, without} {
		if strings.Contains(echoed.raw, "secret-cookie") || strings.Contains(echoed.raw, "s-123456") {
			t.Fatalf("a sensitive header reached the provider: %s", echoed.raw)
		}
	}

	// dbResult: database only with database.schema.
	dbArgs := map[string]any{"content": map[string]any{"text": "{}"}, "surfaceContext": dbResultSurfaceContextFixture()}
	if echoed := call("service.envelope.schema", serviceSurfaceDBResult, dbArgs); !strings.Contains(string(echoed.Envelope.SurfaceContext), `"database":{"connection"`) {
		t.Fatalf("with database.schema: %s", echoed.raw)
	}
	if echoed := call("service.envelope.with", serviceSurfaceDBResult, dbArgs); echoed.Envelope.SurfaceContext != nil || strings.Contains(echoed.raw, "orders") {
		t.Fatalf("without database.schema: %s", echoed.raw)
	}

	// sftpFile: remote only with remote.context.
	useRemoteFiles(t, map[string]*fakeRemoteFile{"/var/log/app.log": newFakeRemoteFile(5, false, "hello")})
	sftpArgs := map[string]any{"remoteFile": map[string]any{"sessionId": "sess-1", "path": "/var/log/app.log"}, "surfaceContext": sftpSurfaceContextFixture()}
	if echoed := call("service.envelope.with", serviceSurfaceSFTPFile, sftpArgs); !strings.Contains(string(echoed.Envelope.SurfaceContext), `"path":"/var/log/app.log"`) {
		t.Fatalf("with remote.context: %s", echoed.raw)
	}
	useRemoteFiles(t, map[string]*fakeRemoteFile{"/var/log/app.log": newFakeRemoteFile(5, false, "hello")})
	if echoed := call("service.envelope.without", serviceSurfaceSFTPFile, sftpArgs); echoed.Envelope.SurfaceContext != nil ||
		strings.Contains(echoed.raw, "/var/log") || strings.Contains(echoed.raw, "files.example.com") {
		t.Fatalf("without remote.context: %s", echoed.raw)
	}
}

// TestHostServiceSurfaceContextCapabilityDroppedBeforeDispatch: a provider
// that loses remote.context while the remote file is read does not get the
// identifiers it was approved for; the call fails instead.
func TestHostServiceSurfaceContextCapabilityDroppedBeforeDispatch(t *testing.T) {
	manager := newEnvelopeManager(t)
	fixture := envelopeFileProvider(t, manager, "service.envelope.with", 1<<20, []string{serviceSurfaceSFTPFile}, api.CapabilityRemoteContext)
	file := newFakeRemoteFile(5, false, "hello")
	file.busy = make(chan struct{})
	useRemoteFiles(t, map[string]*fakeRemoteFile{"/var/log/app.log": file})
	callID := servicesCommandStart(t, map[string]any{"service": "demo.file.inspect", "surface": serviceSurfaceSFTPFile,
		"remoteFile": map[string]any{"sessionId": "sess-1", "path": "/var/log/app.log"}, "surfaceContext": sftpSurfaceContextFixture()})
	fixture.record.mu.Lock()
	fixture.record.manifest.Capabilities = nil
	fixture.record.mu.Unlock()
	close(file.busy)
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	if final.State != serviceCallFailed || !strings.HasPrefix(final.Error, contributionErrForbidden) {
		t.Fatalf("final = %+v", final)
	}
}

func TestSurfaceContextNeverOnLegacySurfaces(t *testing.T) {
	entry := serviceTestEntry()
	entry.Surfaces = []string{serviceSurfaceSelection, serviceSurfacePalette, serviceSurfaceTerminalSelection}
	fixture := serviceCallProvider(t, "service.short", entry, 16384)
	useServicesManager(t, fixture.manager)
	captureHostUI(t)
	local := writeTempFile(t, "a.txt", "x")
	for _, args := range []map[string]any{
		{"service": "demo.text.reverse", "surface": serviceSurfaceSelection, "input": "echo", "surfaceContext": map[string]any{}},
		{"service": "demo.text.reverse", "surface": serviceSurfacePalette, "input": "echo", "surfaceContext": map[string]any{"terminal": map[string]any{"kind": "ssh"}}},
		{"service": "demo.text.reverse", "surface": serviceSurfaceFileMenu, "paths": []string{local}, "surfaceContext": map[string]any{}},
	} {
		_, err := servicesCommandCall(t, pluginServicesStartHandler, args)
		wantCommandCode(t, err, contributionErrInvalidRequest)
	}
	// A plugin caller's envelope never has one, even if a request carried it.
	request := fixture.request("caller.a", "echo")
	request.SurfaceContext = json.RawMessage(`{"terminal":{"kind":"ssh"}}`)
	started, err := fixture.manager.startServiceCall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	final := waitServiceCall(t, "caller.a", started.CallID)
	if final.State != serviceCallSucceeded || strings.Contains(outputText(t, final), "surfaceContext") {
		t.Fatalf("plugin caller envelope = %+v", final)
	}
	// Nor does a host call from an older surface.
	request = fixture.request("", "echo")
	request.Caller = serviceCaller{Kind: "host", Surface: serviceSurfacePalette}
	request.SurfaceContext = json.RawMessage(`{"terminal":{"kind":"ssh"}}`)
	hostCall, err := fixture.manager.runServiceCallSync(context.Background(), request)
	if err != nil || strings.Contains(outputText(t, hostCall), "surfaceContext") {
		t.Fatalf("palette envelope = %+v %v", hostCall, err)
	}
}

func TestHostServiceTerminalSelectionEnvelope(t *testing.T) {
	entry := serviceTestEntry()
	entry.Surfaces = []string{serviceSurfaceTerminalSelection}
	fixture := serviceCallProvider(t, "service.short", entry, 16384)
	useServicesManager(t, fixture.manager)
	captureHostUI(t)
	callID := servicesCommandStart(t, map[string]any{"service": "demo.text.reverse", "surface": serviceSurfaceTerminalSelection,
		"input": "echo", "surfaceContext": map[string]any{"terminal": map[string]any{"kind": "docker"}}})
	final := waitServiceCall(t, serviceCallHostOwner, callID)
	if final.State != serviceCallSucceeded {
		t.Fatalf("final = %+v", final)
	}
	var envelope struct {
		Input          any               `json:"input"`
		Caller         map[string]string `json:"caller"`
		SurfaceContext map[string]any    `json:"surfaceContext"`
	}
	if err := json.Unmarshal([]byte(outputText(t, final)), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Input != "echo" || envelope.Caller["surface"] != serviceSurfaceTerminalSelection ||
		envelope.SurfaceContext["terminal"].(map[string]any)["kind"] != "docker" || len(envelope.SurfaceContext) != 1 {
		t.Fatalf("envelope = %+v", envelope)
	}
	// Without a context the envelope has none.
	callID = servicesCommandStart(t, map[string]any{"service": "demo.text.reverse", "surface": serviceSurfaceTerminalSelection, "input": "echo"})
	final = waitServiceCall(t, serviceCallHostOwner, callID)
	if strings.Contains(outputText(t, final), "surfaceContext") {
		t.Fatalf("envelope = %s", outputText(t, final))
	}
}
