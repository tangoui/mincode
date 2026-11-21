package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

// Runtime and settings-page access to plugin configuration.
//
//   - host.configuration.get {keys?} returns the effective values (default ->
//     global -> active-workspace override) including set secrets. It needs no
//     capability: a plugin reads only its own settings, and secret settings
//     already required the secrets capability at install time.
//   - Plugins cannot write their own settings (v1). The settings page writes
//     through plugin_host_configuration_set/_set_secret, validated here.
//   - When a plugin negotiated features.configuration = 1 in plugin.initialize
//     and is running, the host sends plugin.configuration.changed {keys}
//     after a write or a workspace switch that changes an effective value.
//     The request is best-effort with a 3 second timeout.
//   - plugin:configuration-changed {pluginId, keys} tells the frontend (the
//     settings page and plugin iframes) to re-read; it never carries values.
const (
	pluginConfigurationChangedEvent  = "plugin:configuration-changed"
	configurationChangedMethod       = "plugin.configuration.changed"
	configurationChangedTimeout      = 3 * time.Second
	configurationSyncVersion         = 1
	configurationSyncMaxPlugins      = 512
	configurationGetMaxRequestedKeys = 256
)

var (
	// configurationMu serialises read-modify-write of configuration documents.
	configurationMu sync.Mutex
	// configurationActiveWorkspace is the workspace host.configuration.get
	// resolves against; a variable so tests need not touch the host singleton.
	configurationActiveWorkspace = func() string { return host().activeWorkspaceID() }
)

func init() {
	registerHostMethod("host.configuration.get", func(runtimeHost *processRuntime) bool {
		return configurationSchemaOf(&runtimeHost.manifest) != nil
	}, "", hostConfigurationGet)
	core.Register("plugin_host_configuration_list", pluginHostConfigurationListHandler)
	core.Register("plugin_host_configuration_get", pluginHostConfigurationGetHandler)
	core.Register("plugin_host_configuration_set", pluginHostConfigurationSetHandler)
	core.Register("plugin_host_configuration_set_secret", pluginHostConfigurationSetSecretHandler)
	core.Register("plugin_host_configuration_sync_export", pluginHostConfigurationSyncExportHandler)
	core.Register("plugin_host_configuration_sync_import", pluginHostConfigurationSyncImportHandler)
}

func hostConfigurationGet(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	schema := configurationSchemaOf(&runtimeHost.manifest)
	if schema == nil {
		return nil, &rpcError{Code: -32001, Message: "plugin declares no configuration"}
	}
	var request struct {
		Keys []string `json:"keys"`
	}
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &request); err != nil || len(request.Keys) > configurationGetMaxRequestedKeys {
			return nil, &rpcError{Code: -32602, Message: "invalid configuration params"}
		}
	}
	values, err := effectiveConfigurationValues(runtimeHost.pluginID, schema, configurationActiveWorkspace(), true)
	if err != nil {
		return nil, &rpcError{Code: -32002, Message: err.Error()}
	}
	if request.Keys != nil {
		filtered := map[string]any{}
		for _, key := range request.Keys {
			if value, ok := values[key]; ok {
				filtered[key] = value
			}
		}
		values = filtered
	}
	return map[string]any{"values": values}, nil
}

// configurationFeature reports whether the running generation negotiated
// configuration change notifications.
func (runtimeHost *processRuntime) configurationFeature() bool {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	return runtimeHost.configurationSupported && runtimeHost.cmd != nil && !runtimeHost.stopping
}

// configurationRecord returns a plugin's schema and whether it may hold secrets.
func (manager *pluginManager) configurationRecord(id string) (*pluginRecord, *ConfigurationSchema, error) {
	record, err := manager.record(id)
	if err != nil {
		return nil, nil, err
	}
	record.mu.Lock()
	schema := configurationSchemaOf(record.manifest)
	record.mu.Unlock()
	if schema == nil {
		return nil, nil, fmt.Errorf("plugin %q declares no configuration", record.id)
	}
	return record, schema, nil
}

func (manager *pluginManager) ConfigurationState(id, workspaceID string) (configurationState, error) {
	record, schema, err := manager.configurationRecord(id)
	if err != nil {
		return configurationState{}, err
	}
	if workspaceID == "" {
		workspaceID = manager.activeWorkspaceID()
	} else if err := validateConfigurationWorkspaceID(workspaceID); err != nil {
		return configurationState{}, err
	}
	secrets := false
	for _, property := range schema.Properties {
		secrets = secrets || property.Secret
	}
	return buildConfigurationState(record.id, schema, workspaceID, secrets)
}

// mutateConfiguration applies one write and notifies about effective changes
// in the active workspace.
func (manager *pluginManager) mutateConfiguration(id string, mutate func(pluginID string, schema *ConfigurationSchema) error) error {
	record, schema, err := manager.configurationRecord(id)
	if err != nil {
		return err
	}
	configurationMu.Lock()
	active := manager.activeWorkspaceID()
	before, _ := effectiveConfigurationValues(record.id, schema, active, true)
	if err := mutate(record.id, schema); err != nil {
		configurationMu.Unlock()
		return err
	}
	after, _ := effectiveConfigurationValues(record.id, schema, active, true)
	configurationMu.Unlock()
	manager.notifyConfigurationChanged(record, diffConfigurationValues(before, after))
	return nil
}

func (manager *pluginManager) SetConfiguration(id string, update configurationUpdate) error {
	return manager.mutateConfiguration(id, func(pluginID string, schema *ConfigurationSchema) error {
		return applyConfigurationUpdate(pluginID, schema, update)
	})
}

func (manager *pluginManager) SetConfigurationSecret(id, key string, value *string) error {
	return manager.mutateConfiguration(id, func(pluginID string, schema *ConfigurationSchema) error {
		return setConfigurationSecret(pluginID, schema, key, value)
	})
}

// notifyConfigurationChanged emits the frontend event and, when negotiated,
// the plugin.configuration.changed request. It never blocks the caller.
func (manager *pluginManager) notifyConfigurationChanged(record *pluginRecord, keys []string) {
	if len(keys) == 0 {
		return
	}
	core.Emit(pluginConfigurationChangedEvent, map[string]any{"pluginId": record.id, "keys": keys})
	record.mu.Lock()
	runtimeHost := record.runtime
	running := record.state == pluginStateRunning
	record.mu.Unlock()
	if runtimeHost == nil || !running || !runtimeHost.configurationFeature() {
		return
	}
	params, _ := json.Marshal(map[string]any{"keys": keys})
	generation := runtimeHost.Generation()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), configurationChangedTimeout)
		defer cancel()
		if _, err := runtimeHost.callGeneration(ctx, generation, configurationChangedMethod, params); err != nil {
			record.appendLog("warn", "configuration change notification failed: "+err.Error())
		}
	}()
}

// notifyConfigurationWorkspaceSwitch runs after the active workspace changed:
// plugins whose effective settings differ between the two workspaces are told.
func (manager *pluginManager) notifyConfigurationWorkspaceSwitch(records []*pluginRecord, previous, next string) {
	for _, record := range records {
		record.mu.Lock()
		schema := configurationSchemaOf(record.manifest)
		record.mu.Unlock()
		if schema == nil {
			continue
		}
		workspaceScoped := false
		for _, property := range schema.Properties {
			workspaceScoped = workspaceScoped || property.Scope == configurationScopeWorkspace
		}
		if !workspaceScoped {
			continue
		}
		configurationMu.Lock()
		document, _, err := loadConfigurationDocument(record.id)
		configurationMu.Unlock()
		if err != nil {
			continue
		}
		keys := diffConfigurationValues(effectiveNonSecretValues(schema, document, previous), effectiveNonSecretValues(schema, document, next))
		manager.notifyConfigurationChanged(record, keys)
	}
}

// clearConfiguration removes the stored values (Clear Plugin Data). The
// "__config." secrets go with the plugin's secret namespace.
func (manager *pluginManager) clearConfiguration(record *pluginRecord) error {
	configurationMu.Lock()
	err := deleteConfigurationDocument(record.id)
	configurationMu.Unlock()
	if err != nil {
		return err
	}
	core.Emit(pluginConfigurationChangedEvent, map[string]any{"pluginId": record.id, "keys": []string{}})
	return nil
}

// ConfigurationPlugins lists installed plugins (enabled or not) and attached
// development-session plugins that declare a valid configuration schema, in
// manager order, for the settings page. Every state change (install, enable,
// development attach/detach/generation swap) emits plugin:contributions-changed,
// which the settings page uses to refresh this list.
func (manager *pluginManager) ConfigurationPlugins() []string {
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	ids := []string{}
	for _, record := range records {
		record.mu.Lock()
		declared := configurationSchemaOf(record.manifest) != nil
		record.mu.Unlock()
		if declared {
			ids = append(ids, record.id)
		}
	}
	return ids
}

// ---- configuration sync ----
//
// Configuration sync carries only global, non-secret values of installed
// plugins that declare a schema. Secrets and workspace overrides never leave
// the device through sync.

type configurationSyncPlugin struct {
	Global map[string]any `json:"global"`
}

type configurationSyncData struct {
	Version int                                `json:"version"`
	Plugins map[string]configurationSyncPlugin `json:"plugins"`
}

func (manager *pluginManager) ConfigurationSyncExport() (configurationSyncData, error) {
	data := configurationSyncData{Version: configurationSyncVersion, Plugins: map[string]configurationSyncPlugin{}}
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	for _, record := range records {
		record.mu.Lock()
		schema := configurationSchemaOf(record.manifest)
		record.mu.Unlock()
		if schema == nil {
			continue
		}
		configurationMu.Lock()
		document, _, err := loadConfigurationDocument(record.id)
		configurationMu.Unlock()
		if err != nil {
			return data, err
		}
		global := validStoredValues(schema, document.Global, configurationScopeGlobal, "", nil)
		if len(global) > 0 {
			data.Plugins[record.id] = configurationSyncPlugin{Global: global}
		}
	}
	return data, nil
}

type configurationSyncImportResult struct {
	Applied []string `json:"applied"`
	Skipped []string `json:"skipped"`
}

// ConfigurationSyncImport replaces the global values of each listed installed
// plugin with the valid, declared, non-secret values in data. Workspace
// overrides stay; unlisted plugins are untouched.
func (manager *pluginManager) ConfigurationSyncImport(data configurationSyncData) (configurationSyncImportResult, error) {
	result := configurationSyncImportResult{Applied: []string{}, Skipped: []string{}}
	if data.Version != configurationSyncVersion {
		return result, fmt.Errorf("unsupported plugin configuration sync version %d", data.Version)
	}
	if len(data.Plugins) > configurationSyncMaxPlugins {
		return result, fmt.Errorf("plugin configuration sync lists more than %d plugins", configurationSyncMaxPlugins)
	}
	ids := make([]string, 0, len(data.Plugins))
	for id := range data.Plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		incoming := data.Plugins[id]
		if _, err := validatePluginID(id); err != nil || id == hostPluginID {
			result.Skipped = append(result.Skipped, id)
			continue
		}
		if _, _, err := manager.configurationRecord(id); err != nil {
			result.Skipped = append(result.Skipped, id)
			continue
		}
		err := manager.mutateConfiguration(id, func(pluginID string, schema *ConfigurationSchema) error {
			document, readErr, err := loadConfigurationDocument(pluginID)
			if err != nil {
				return err
			}
			if readErr != nil {
				document = emptyConfigurationDocument()
			}
			global := map[string]json.RawMessage{}
			for key, value := range incoming.Global {
				encoded, err := json.Marshal(value)
				if err != nil {
					continue
				}
				if _, reason := decodeConfigurationStoredValue(schema, key, encoded, false); reason == "" {
					global[key] = encoded
				}
			}
			document.Global = global
			return saveConfigurationDocument(pluginID, document)
		})
		if err != nil {
			result.Skipped = append(result.Skipped, id)
			continue
		}
		result.Applied = append(result.Applied, id)
	}
	return result, nil
}

// ---- frontend commands ----

func pluginHostConfigurationListHandler(_, _ string) (string, error) {
	return core.EncodeResult(host().ConfigurationPlugins())
}

func pluginHostConfigurationGetHandler(_, argsJSON string) (string, error) {
	var args struct {
		ID          string `json:"id"`
		WorkspaceID string `json:"workspaceId"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	state, err := host().ConfigurationState(strings.TrimSpace(args.ID), args.WorkspaceID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(state)
}

func pluginHostConfigurationSetHandler(_, argsJSON string) (string, error) {
	var args struct {
		ID          string                     `json:"id"`
		Scope       string                     `json:"scope"`
		WorkspaceID string                     `json:"workspaceId"`
		Values      map[string]json.RawMessage `json:"values"`
		ResetAll    bool                       `json:"resetAll"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	err := host().SetConfiguration(strings.TrimSpace(args.ID), configurationUpdate{
		Scope: args.Scope, WorkspaceID: args.WorkspaceID, Values: args.Values, ResetAll: args.ResetAll,
	})
	if err != nil {
		return "", err
	}
	return core.EncodeResult(true)
}

// pluginHostConfigurationSetSecretHandler is write-only: no command returns a
// configuration secret to the frontend.
func pluginHostConfigurationSetSecretHandler(_, argsJSON string) (string, error) {
	var args struct {
		ID    string  `json:"id"`
		Key   string  `json:"key"`
		Value *string `json:"value"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := host().SetConfigurationSecret(strings.TrimSpace(args.ID), args.Key, args.Value); err != nil {
		return "", err
	}
	return core.EncodeResult(true)
}

func pluginHostConfigurationSyncExportHandler(_, _ string) (string, error) {
	data, err := host().ConfigurationSyncExport()
	if err != nil {
		return "", err
	}
	return core.EncodeResult(data)
}

func pluginHostConfigurationSyncImportHandler(_, argsJSON string) (string, error) {
	var args struct {
		Data *configurationSyncData `json:"data"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if args.Data == nil {
		return "", errors.New("plugin configuration sync data is required")
	}
	result, err := host().ConfigurationSyncImport(*args.Data)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}
