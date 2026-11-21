package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/license"
	"mincode/app/plugins/api"
)

const configurationTestPlugin = "config.test"

// writeConfigurationPackage writes a process plugin package declaring
// contributes.configuration; source is the plugin.mjs body.
func writeConfigurationPackage(t *testing.T, mutate func(map[string]any), configuration any, source string) string {
	t.Helper()
	root := t.TempDir()
	if source == "" {
		source = "#!/bin/sh\n"
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.mjs"), []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"apiVersion": 2, "id": configurationTestPlugin, "name": "Config Test", "version": "1.0.0",
		"category": "development", "platforms": []string{runtime.GOOS},
		"capabilities": []string{"secrets"},
		"runtime":      map[string]any{"type": "process", "entry": "plugin.mjs"},
		"contributes":  map[string]any{"configuration": configuration},
	}
	if mutate != nil {
		mutate(manifest)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func configurationTestSchema() map[string]any {
	return map[string]any{
		"title": "Connection",
		"properties": map[string]any{
			"endpoint": map[string]any{"type": "string", "format": "url", "default": "https://example.test", "title": "Endpoint"},
			"retries":  map[string]any{"type": "integer", "minimum": 0, "maximum": 10, "default": 3},
			"mode":     map[string]any{"type": "string", "enum": []string{"fast", "safe"}, "default": "safe", "scope": "workspace"},
			"verbose":  map[string]any{"type": "boolean", "scope": "workspace"},
			"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 4},
			"token":    map[string]any{"type": "string", "secret": true},
		},
	}
}

func readConfigurationSchema(t *testing.T, root string) (Manifest, *ConfigurationSchema) {
	t.Helper()
	manifest, err := readManifestStrict(root)
	if err != nil {
		t.Fatal(err)
	}
	schema := configurationSchemaOf(&manifest)
	if schema == nil {
		t.Fatal("configuration schema not resolved")
	}
	return manifest, schema
}

func TestConfigurationSchemaValidation(t *testing.T) {
	_, schema := readConfigurationSchema(t, writeConfigurationPackage(t, nil, configurationTestSchema(), ""))
	if len(schema.Properties) != 6 || schema.Properties["endpoint"].Scope != configurationScopeGlobal || schema.Properties["tags"].Items.Type != "string" {
		t.Fatalf("schema = %+v", schema)
	}
	property := func(value map[string]any) map[string]any {
		return map[string]any{"properties": map[string]any{"key": value}}
	}
	tooMany := map[string]any{}
	for index := 0; index <= configurationMaxProperties; index++ {
		tooMany["k"+strconv.Itoa(index)] = map[string]any{"type": "boolean"}
	}
	for name, configuration := range map[string]any{
		"no properties":         map[string]any{"properties": map[string]any{}},
		"too many":              map[string]any{"properties": tooMany},
		"bad key":               map[string]any{"properties": map[string]any{"Bad-Key": map[string]any{"type": "string"}}},
		"deep key":              map[string]any{"properties": map[string]any{"a.b.c.d.e": map[string]any{"type": "string"}}},
		"long key":              map[string]any{"properties": map[string]any{"a" + strings.Repeat("b", 64): map[string]any{"type": "string"}}},
		"object type":           property(map[string]any{"type": "object"}),
		"pattern":               property(map[string]any{"type": "string", "pattern": "^a$"}),
		"bad format":            property(map[string]any{"type": "string", "format": "email"}),
		"bad default":           property(map[string]any{"type": "integer", "default": 1.5}),
		"default out of range":  property(map[string]any{"type": "number", "maximum": 1, "default": 2}),
		"default not in enum":   property(map[string]any{"type": "string", "enum": []string{"a"}, "default": "b"}),
		"number array":          property(map[string]any{"type": "array", "items": map[string]any{"type": "number"}}),
		"enum on boolean":       property(map[string]any{"type": "boolean", "enum": []bool{true}}),
		"secret default":        property(map[string]any{"type": "string", "secret": true, "default": "x"}),
		"secret array":          property(map[string]any{"type": "array", "secret": true}),
		"secret workspace":      property(map[string]any{"type": "string", "secret": true, "scope": "workspace"}),
		"bad scope":             property(map[string]any{"type": "string", "scope": "user"}),
		"url with credentials":  property(map[string]any{"type": "string", "format": "url", "default": "https://u:p@example.test"}),
		"enum labels mismatch":  property(map[string]any{"type": "string", "enum": []string{"a", "b"}, "enumLabelsLocalized": map[string][]string{"zh": {"甲"}}}),
		"unknown property attr": property(map[string]any{"type": "string", "placeholder": "x"}),
	} {
		if _, err := readManifestStrict(writeConfigurationPackage(t, nil, configuration, "")); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// Secrets need the secrets capability.
	noSecrets := writeConfigurationPackage(t, func(manifest map[string]any) { delete(manifest, "capabilities") }, configurationTestSchema(), "")
	if _, err := readManifestStrict(noSecrets); err == nil || !strings.Contains(err.Error(), "secrets capability") {
		t.Fatalf("secret without capability: %v", err)
	}
	// Schema size limit.
	big := map[string]any{"properties": map[string]any{"key": map[string]any{"type": "string", "description": strings.Repeat("x", configurationSchemaMax)}}}
	if _, err := readManifestStrict(writeConfigurationPackage(t, nil, big, "")); err == nil {
		t.Fatal("oversized schema accepted")
	}
}

func TestConfigurationStoreRoundTripAndIgnoredValues(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	_, schema := readConfigurationSchema(t, writeConfigurationPackage(t, nil, configurationTestSchema(), ""))
	id := configurationTestPlugin
	set := func(update configurationUpdate) error { return applyConfigurationUpdate(id, schema, update) }
	if err := set(configurationUpdate{Scope: "global", Values: map[string]json.RawMessage{"retries": json.RawMessage(`5`), "tags": json.RawMessage(`["a","b"]`)}}); err != nil {
		t.Fatal(err)
	}
	if err := set(configurationUpdate{Scope: "workspace", WorkspaceID: "ws-1", Values: map[string]json.RawMessage{"mode": json.RawMessage(`"fast"`)}}); err != nil {
		t.Fatal(err)
	}
	for name, update := range map[string]configurationUpdate{
		"out of range":        {Scope: "global", Values: map[string]json.RawMessage{"retries": json.RawMessage(`11`)}},
		"global-only in ws":   {Scope: "workspace", WorkspaceID: "ws-1", Values: map[string]json.RawMessage{"retries": json.RawMessage(`1`)}},
		"secret as value":     {Scope: "global", Values: map[string]json.RawMessage{"token": json.RawMessage(`"x"`)}},
		"unknown key":         {Scope: "global", Values: map[string]json.RawMessage{"nope": json.RawMessage(`1`)}},
		"bad scope":           {Scope: "user", Values: map[string]json.RawMessage{"retries": json.RawMessage(`1`)}},
		"missing workspace":   {Scope: "workspace", Values: map[string]json.RawMessage{"mode": json.RawMessage(`"fast"`)}},
		"not in enum":         {Scope: "workspace", WorkspaceID: "ws-1", Values: map[string]json.RawMessage{"mode": json.RawMessage(`"slow"`)}},
		"too many array item": {Scope: "global", Values: map[string]json.RawMessage{"tags": json.RawMessage(`["1","2","3","4","5"]`)}},
	} {
		if err := set(update); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if err := setConfigurationSecret(id, schema, "token", ptr("s3cret")); err != nil {
		t.Fatal(err)
	}
	if err := setConfigurationSecret(id, schema, "endpoint", ptr("x")); err == nil {
		t.Fatal("non-secret accepted as secret")
	}

	values, err := effectiveConfigurationValues(id, schema, "ws-1", true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"endpoint": "https://example.test", "retries": float64(5), "mode": "fast", "tags": []any{"a", "b"}, "token": "s3cret"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("effective = %#v", values)
	}
	if values, _ := effectiveConfigurationValues(id, schema, "ws-2", false); values["mode"] != "safe" || values["token"] != nil {
		t.Fatalf("other workspace = %#v", values)
	}

	// Values that the schema no longer accepts are ignored but kept.
	raw, _, _ := pluginKVGetRaw(hostPluginID, configurationKVKey(id))
	var document map[string]any
	_ = json.Unmarshal(raw, &document)
	document["global"].(map[string]any)["retries"] = 99
	document["global"].(map[string]any)["removed"] = true
	encoded, _ := json.Marshal(document)
	mustSetPluginKV(t, hostPluginID, configurationKVKey(id), string(encoded))
	if err := pluginSecretSet(id, configurationSecretKey("oldToken"), "orphan"); err != nil {
		t.Fatal(err)
	}
	state, err := buildConfigurationState(id, schema, "ws-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Effective["retries"] != float64(3) || len(state.Ignored) != 2 || !reflect.DeepEqual(state.OrphanSecrets, []string{"oldToken"}) || !state.Secrets["token"] {
		t.Fatalf("state = %+v", state)
	}
	encodedState, _ := json.Marshal(state)
	if strings.Contains(string(encodedState), "s3cret") || strings.Contains(string(encodedState), "orphan\"") {
		t.Fatalf("settings state leaked a secret: %s", encodedState)
	}
	raw, _, _ = pluginKVGetRaw(hostPluginID, configurationKVKey(id))
	if !strings.Contains(string(raw), `"retries":99`) || !strings.Contains(string(raw), `"removed":true`) {
		t.Fatalf("ignored values were not kept: %s", raw)
	}
	// Ignored values can be reset; orphaned secrets can be deleted.
	if err := set(configurationUpdate{Scope: "global", Values: map[string]json.RawMessage{"removed": json.RawMessage(`null`), "retries": json.RawMessage(`null`)}}); err != nil {
		t.Fatal(err)
	}
	if err := setConfigurationSecret(id, schema, "oldToken", nil); err != nil {
		t.Fatal(err)
	}
	if state, _ := buildConfigurationState(id, schema, "ws-1", true); len(state.Ignored) != 0 || len(state.OrphanSecrets) != 0 {
		t.Fatalf("after cleanup = %+v", state)
	}

	// An unreadable document is reported and refuses ordinary writes.
	mustSetPluginKV(t, hostPluginID, configurationKVKey(id), `{"version":9}`)
	if state, _ := buildConfigurationState(id, schema, "", true); state.StoreError == "" {
		t.Fatal("store error not reported")
	}
	if err := set(configurationUpdate{Scope: "global", Values: map[string]json.RawMessage{"retries": json.RawMessage(`1`)}}); err == nil {
		t.Fatal("write over unreadable document accepted")
	}
	if err := set(configurationUpdate{ResetAll: true}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := pluginKVGetRaw(hostPluginID, configurationKVKey(id)); found {
		t.Fatal("reset did not delete the document")
	}
}

func ptr(value string) *string { return &value }

func configurationRuntime(t *testing.T, manifest Manifest) *processRuntime {
	t.Helper()
	runtimeHost := newProcessRuntime(t.TempDir(), manifest, nil, nil)
	runtimeHost.cmd = &exec.Cmd{}
	runtimeHost.generation = 1
	runtimeHost.runContext = context.Background()
	runtimeHost.writes = make(chan rpcWriteRequest, 1)
	return runtimeHost
}

func TestConfigurationGetRPCAndSecretPrefixGuard(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	manifest, schema := readConfigurationSchema(t, writeConfigurationPackage(t, nil, configurationTestSchema(), ""))
	previous := configurationActiveWorkspace
	configurationActiveWorkspace = func() string { return "ws-1" }
	t.Cleanup(func() { configurationActiveWorkspace = previous })
	if err := applyConfigurationUpdate(configurationTestPlugin, schema, configurationUpdate{Scope: "workspace", WorkspaceID: "ws-1", Values: map[string]json.RawMessage{"mode": json.RawMessage(`"fast"`)}}); err != nil {
		t.Fatal(err)
	}
	if err := setConfigurationSecret(configurationTestPlugin, schema, "token", ptr("s3cret")); err != nil {
		t.Fatal(err)
	}
	drains := map[*processRuntime]chan rpcEnvelope{}
	call := func(runtimeHost *processRuntime, method, params string) rpcEnvelope {
		t.Helper()
		replies, ok := drains[runtimeHost]
		if !ok {
			replies = drainRuntimeWrites(runtimeHost)
			drains[runtimeHost] = replies
		}
		runtimeHost.handleHostRequest(1, rpcEnvelope{ID: json.RawMessage(`"1"`), Method: method, Params: json.RawMessage(params)})
		return <-replies
	}
	runtimeHost := configurationRuntime(t, manifest)
	reply := call(runtimeHost, "host.configuration.get", `{}`)
	if reply.Error != nil || !strings.Contains(string(reply.Result), `"mode":"fast"`) || !strings.Contains(string(reply.Result), `"token":"s3cret"`) || !strings.Contains(string(reply.Result), `"retries":3`) {
		t.Fatalf("get: %+v %s", reply, reply.Result)
	}
	if reply := call(runtimeHost, "host.configuration.get", `{"keys":["retries","nope"]}`); reply.Error != nil || string(reply.Result) != `{"values":{"retries":3}}` {
		t.Fatalf("get keys: %+v %s", reply, reply.Result)
	}
	if reply := call(runtimeHost, "host.configuration.get", `{"keys":"retries"}`); reply.Error == nil || reply.Error.Code != -32602 {
		t.Fatalf("bad params: %+v", reply)
	}
	// A plugin without a schema does not get the method.
	plain := contextRuntime(t, []api.Capability{api.CapabilitySecrets}, nil)
	if reply := call(plain, "host.configuration.get", `{}`); reply.Error == nil {
		t.Fatalf("schema-less get: %+v", reply)
	}

	// host.secrets.* cannot reach the reserved configuration prefix.
	secretsHost := configurationRuntime(t, manifest)
	for _, request := range [][2]string{
		{"host.secrets.get", `{"key":"__config.token"}`},
		{"host.secrets.set", `{"key":"__config.token","value":"x"}`},
		{"host.secrets.delete", `{"key":"__config.token"}`},
	} {
		if reply := call(secretsHost, request[0], request[1]); reply.Error == nil || reply.Error.Code != -32002 {
			t.Fatalf("%s: %+v", request[0], reply)
		}
	}
	if reply := call(secretsHost, "host.secrets.set", `{"key":"own","value":"v"}`); reply.Error != nil {
		t.Fatalf("own secret: %+v", reply)
	}
	if reply := call(secretsHost, "host.secrets.list", `{}`); reply.Error != nil || string(reply.Result) != `["own"]` {
		t.Fatalf("list must hide configuration secrets: %+v %s", reply, reply.Result)
	}
	if value, _, _ := pluginSecretGet(configurationTestPlugin, configurationSecretKey("token")); value != "s3cret" {
		t.Fatal("guard did not protect the configuration secret")
	}
}

func TestConfigurationHandshakeAndChangedNotification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	sdk, err := filepath.Abs("../../packages/plugin-sdk/node/runtime.mjs")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal("file://" + filepath.ToSlash(sdk))
	marker := filepath.Join(t.TempDir(), "changed.json")
	markerJSON, _ := json.Marshal(marker)
	source := "#!/usr/bin/env node\nimport { writeFileSync } from 'node:fs';\nimport { start, createConfigurationAPI } from " + string(quoted) + `;
start(host => ({ read: () => createConfigurationAPI(host).get() }), {
  onConfigurationChanged: async (keys, { host }) => {
    const values = await createConfigurationAPI(host).get(keys);
    writeFileSync(` + string(markerJSON) + `, JSON.stringify({ keys, values }));
  },
});`
	root := writeConfigurationPackage(t, nil, configurationTestSchema(), source)
	manifest, _ := readConfigurationSchema(t, root)
	previous := configurationActiveWorkspace
	configurationActiveWorkspace = func() string { return "" }
	t.Cleanup(func() { configurationActiveWorkspace = previous })

	runtimeHost := newProcessRuntime(root, manifest, nil, nil)
	if err := runtimeHost.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		runtimeHost.Stop(ctx)
	})
	if !runtimeHost.configurationFeature() {
		t.Fatal("configuration feature was not negotiated")
	}
	result, err := runtimeHost.Invoke(context.Background(), "read", `{}`)
	if err != nil || !strings.Contains(result, `"retries":3`) {
		t.Fatalf("read: %s %v", result, err)
	}

	manager := &pluginManager{records: map[string]*pluginRecord{}}
	record := &pluginRecord{id: configurationTestPlugin, source: pluginSourceLocal, root: root, manifest: &manifest,
		enabled: true, scope: pluginScopeGlobal, state: pluginStateRunning, runtime: runtimeHost}
	manager.records[record.id] = record
	manager.order = append(manager.order, record.id)
	if err := manager.SetConfiguration(configurationTestPlugin, configurationUpdate{Scope: "global", Values: map[string]json.RawMessage{"retries": json.RawMessage(`7`)}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			if string(data) != `{"keys":["retries"],"values":{"retries":7}}` {
				t.Fatalf("notification = %s", data)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plugin.configuration.changed was not delivered")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestConfigurationUninstallAndClearSemantics(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	seed := func() (*pluginManager, *pluginRecord, *ConfigurationSchema) {
		root := writeConfigurationPackage(t, nil, configurationTestSchema(), "")
		manifest, schema := readConfigurationSchema(t, root)
		manager := &pluginManager{records: map[string]*pluginRecord{}}
		record := &pluginRecord{id: configurationTestPlugin, source: pluginSourceLocal, root: root, manifest: &manifest,
			scope: pluginScopeGlobal, state: pluginStateStopped}
		manager.records[record.id] = record
		manager.order = append(manager.order, record.id)
		if err := manager.SetConfiguration(record.id, configurationUpdate{Scope: "global", Values: map[string]json.RawMessage{"retries": json.RawMessage(`4`)}}); err != nil {
			t.Fatal(err)
		}
		if err := manager.SetConfigurationSecret(record.id, "token", ptr("s3cret")); err != nil {
			t.Fatal(err)
		}
		return manager, record, schema
	}
	manager, record, _ := seed()
	if err := manager.ClearPluginData(context.Background(), record.id); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := pluginKVGetRaw(hostPluginID, configurationKVKey(record.id)); found {
		t.Fatal("clear kept configuration values")
	}
	if keys, _ := pluginSecretList(record.id); len(keys) != 0 {
		t.Fatalf("clear kept configuration secrets: %v", keys)
	}

	manager, record, _ = seed()
	if err := manager.Uninstall(context.Background(), record.id, false); err != nil {
		t.Fatal(err)
	}
	if raw, found, _ := pluginKVGetRaw(hostPluginID, configurationKVKey(record.id)); !found || !strings.Contains(string(raw), `"retries":4`) {
		t.Fatalf("uninstall must keep configuration values: %s", raw)
	}
	if keys, _ := pluginSecretList(record.id); len(keys) == 0 {
		t.Fatal("uninstall without purge must keep configuration secrets")
	}

	manager, record, _ = seed()
	if err := manager.Uninstall(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := pluginKVGetRaw(hostPluginID, configurationKVKey(record.id)); found {
		t.Fatal("uninstall with purge kept configuration values")
	}
	if keys, _ := pluginSecretList(record.id); len(keys) != 0 {
		t.Fatalf("uninstall with purge kept configuration secrets: %v", keys)
	}
}

func TestConfigurationExportExcludesSecrets(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	root := writeConfigurationPackage(t, nil, configurationTestSchema(), "")
	manifest, schema := readConfigurationSchema(t, root)
	if err := applyConfigurationUpdate(configurationTestPlugin, schema, configurationUpdate{Scope: "global", Values: map[string]json.RawMessage{"retries": json.RawMessage(`6`)}}); err != nil {
		t.Fatal(err)
	}
	if err := setConfigurationSecret(configurationTestPlugin, schema, "token", ptr("s3cret")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "config.json")
	config := map[string]json.RawMessage{"format": json.RawMessage(`"mincode-config"`), "version": json.RawMessage(`2`), "values": json.RawMessage(`{}`)}
	summaries, err := writeConfigExportWithPlugins(target, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != configurationTestPlugin || !summaries[0].Configuration {
		t.Fatalf("summaries = %+v", summaries)
	}
	raw, _ := os.ReadFile(target)
	if !json.Valid(raw) || !strings.Contains(string(raw), `\"retries\":6`) && !strings.Contains(string(raw), `"retries":6`) {
		t.Fatalf("export = %s", raw)
	}
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "__config.") {
		t.Fatalf("export leaked a configuration secret: %s", raw)
	}
	archive := filepath.Join(t.TempDir(), "plugin.zip")
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	record := &pluginRecord{id: configurationTestPlugin, source: pluginSourceLocal, root: root, manifest: &manifest, scope: pluginScopeGlobal, state: pluginStateStopped}
	manager.records[record.id] = record
	manager.order = append(manager.order, record.id)
	if _, err := manager.ExportPluginDataArchive(record.id, archive); err != nil {
		t.Fatal(err)
	}
	archived, _ := os.ReadFile(archive)
	if strings.Contains(string(archived), "s3cret") {
		t.Fatal("archive leaked a configuration secret")
	}

	// Import on a fresh machine restores values; secrets stay absent.
	useTemporaryPluginData(t)
	_, stages, err := readConfigExportFile(target, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupStages(stages)
	if _, err := manager.applyPluginDataStages(context.Background(), stages); err != nil {
		t.Fatal(err)
	}
	values, err := effectiveConfigurationValues(configurationTestPlugin, schema, "", true)
	if err != nil || values["retries"] != float64(6) || values["token"] != nil {
		t.Fatalf("imported = %#v %v", values, err)
	}
	if _, err := manager.ImportPluginDataArchive(context.Background(), record.id, archive); err != nil {
		t.Fatal(err)
	}
	if values, _ := effectiveConfigurationValues(configurationTestPlugin, schema, "", true); values["retries"] != float64(6) {
		t.Fatalf("archive import = %#v", values)
	}
}

func TestConfigurationSyncCarriesOnlyGlobalNonSecretValues(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	root := writeConfigurationPackage(t, nil, configurationTestSchema(), "")
	manifest, _ := readConfigurationSchema(t, root)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	record := &pluginRecord{id: configurationTestPlugin, source: pluginSourceLocal, root: root, manifest: &manifest, scope: pluginScopeGlobal, state: pluginStateStopped}
	manager.records[record.id] = record
	manager.order = append(manager.order, record.id)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if ids := manager.ConfigurationPlugins(); !reflect.DeepEqual(ids, []string{configurationTestPlugin}) {
		t.Fatalf("configuration plugins = %v", ids)
	}
	must(manager.SetConfiguration(record.id, configurationUpdate{Scope: "global", Values: map[string]json.RawMessage{"retries": json.RawMessage(`2`)}}))
	must(manager.SetConfiguration(record.id, configurationUpdate{Scope: "workspace", WorkspaceID: "ws-1", Values: map[string]json.RawMessage{"mode": json.RawMessage(`"fast"`)}}))
	must(manager.SetConfigurationSecret(record.id, "token", ptr("s3cret")))
	data, err := manager.ConfigurationSyncExport()
	must(err)
	encoded, _ := json.Marshal(data)
	if string(encoded) != `{"version":1,"plugins":{"config.test":{"global":{"retries":2}}}}` {
		t.Fatalf("sync export = %s", encoded)
	}
	incoming := configurationSyncData{Version: 1, Plugins: map[string]configurationSyncPlugin{
		configurationTestPlugin: {Global: map[string]any{"retries": float64(9), "token": "stolen", "mode": "fast", "unknown": true}},
		"not.installed":         {Global: map[string]any{"a": 1}},
	}}
	result, err := manager.ConfigurationSyncImport(incoming)
	must(err)
	if !reflect.DeepEqual(result.Applied, []string{configurationTestPlugin}) || !reflect.DeepEqual(result.Skipped, []string{"not.installed"}) {
		t.Fatalf("sync import = %+v", result)
	}
	state, err := manager.ConfigurationState(record.id, "ws-1")
	must(err)
	if !reflect.DeepEqual(state.Global, map[string]any{"retries": float64(9), "mode": "fast"}) || state.Workspace["mode"] != "fast" {
		t.Fatalf("after sync = %+v", state)
	}
	if value, _, _ := pluginSecretGet(record.id, configurationSecretKey("token")); value != "s3cret" {
		t.Fatal("sync import touched the secret")
	}
	if _, err := manager.ConfigurationSyncImport(configurationSyncData{Version: 2}); err == nil {
		t.Fatal("unsupported sync version accepted")
	}
}

// Development-session plugins appear in the settings list, read and write
// through the same Go boundary, and a new generation's schema revalidates
// stored values on read.
func TestConfigurationDevelopmentSessionListedAndRevalidated(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	previous := enabledPluginQuota
	enabledPluginQuota = func() (license.QuotaPolicy, string, bool) { return license.QuotaPolicy{}, "", false }
	t.Cleanup(func() { enabledPluginQuota = previous })
	var eventsMu sync.Mutex
	var events []pluginContributionsChangedDTO
	core.SetEmitter(func(name string, data any) {
		if event, ok := data.(pluginContributionsChangedDTO); ok && name == pluginContributionsChangedEvent {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
		}
	})
	lastChanged := func() string {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		if len(events) == 0 {
			return ""
		}
		return events[len(events)-1].PluginID
	}
	t.Cleanup(func() { core.SetEmitter(nil) })

	manager := newPluginManager()
	session := pluginDevSession{ID: "dev-config", PluginID: configurationTestPlugin, PackageRoot: writeConfigurationPackage(t, nil, configurationTestSchema(), "")}
	if err := manager.attachDevelopmentSession(session); err != nil {
		t.Fatalf("attach development session: %v", err)
	}
	if ids := manager.ConfigurationPlugins(); !slices.Contains(ids, configurationTestPlugin) {
		t.Fatalf("development plugin missing from configuration list: %v", ids)
	}
	if lastChanged() != configurationTestPlugin {
		t.Fatal("attach did not announce a contributions change")
	}
	if err := manager.SetConfiguration(configurationTestPlugin, configurationUpdate{Scope: "global", Values: map[string]json.RawMessage{"retries": json.RawMessage(`8`)}}); err != nil {
		t.Fatalf("write development plugin setting: %v", err)
	}
	state, err := manager.ConfigurationState(configurationTestPlugin, "")
	if err != nil || state.Global["retries"] != float64(8) {
		t.Fatalf("read development plugin setting: %+v %v", state.Global, err)
	}

	// A later development generation narrows the schema: the stored value is
	// reported as ignored (not deleted) and writes use the new bounds.
	narrowed := configurationTestSchema()
	narrowed["properties"].(map[string]any)["retries"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 5, "default": 3}
	manifest, _ := readConfigurationSchema(t, writeConfigurationPackage(t, nil, narrowed, ""))
	record, err := manager.record(configurationTestPlugin)
	if err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	record.manifest = &manifest
	record.mu.Unlock()
	state, err = manager.ConfigurationState(configurationTestPlugin, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := state.Global["retries"]; kept || len(state.Ignored) != 1 || state.Ignored[0].Key != "retries" {
		t.Fatalf("narrowed schema must ignore the stored value: global=%+v ignored=%+v", state.Global, state.Ignored)
	}
	if err := manager.SetConfiguration(configurationTestPlugin, configurationUpdate{Scope: "global", Values: map[string]json.RawMessage{"retries": json.RawMessage(`8`)}}); err == nil {
		t.Fatal("write accepted a value outside the new generation's schema")
	}

	manager.detachDevelopmentSession(session)
	if ids := manager.ConfigurationPlugins(); slices.Contains(ids, configurationTestPlugin) {
		t.Fatalf("detached development plugin still listed: %v", ids)
	}
	if lastChanged() != configurationTestPlugin {
		t.Fatal("detach did not announce a contributions change")
	}
}
