package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Plugin configuration values live in the host KV namespace (__host__) under
// "<id>.configuration" as {version:1, global:{}, workspaces:{"<ws>":{}}}.
// Secret properties never enter that document: they are sealed in
// plugin_secrets under the reserved "__config." prefix, which host.secrets.*
// cannot reach (secret_storage.go). Stored values are validated on every read;
// an unknown key or a value that no longer matches the schema is reported as
// ignored and kept until the user resets it, so a plugin upgrade that narrows
// a setting never silently destroys data.
const (
	configurationStoreVersion       = 1
	configurationSecretPrefix       = "__config."
	configurationWorkspaceMaxLength = 128
)

func configurationKVKey(pluginID string) string { return pluginID + ".configuration" }

func configurationSecretKey(key string) string { return configurationSecretPrefix + key }

type configurationDocument struct {
	Version    int                                   `json:"version"`
	Global     map[string]json.RawMessage            `json:"global"`
	Workspaces map[string]map[string]json.RawMessage `json:"workspaces"`
}

// ConfigurationIgnoredValue is a stored value the current schema rejects.
type ConfigurationIgnoredValue struct {
	Scope       string `json:"scope"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	Key         string `json:"key"`
	Reason      string `json:"reason"`
}

func emptyConfigurationDocument() configurationDocument {
	return configurationDocument{Version: configurationStoreVersion, Global: map[string]json.RawMessage{}, Workspaces: map[string]map[string]json.RawMessage{}}
}

// parseConfigurationDocument validates the structure (not the values) of a
// stored or imported document.
func parseConfigurationDocument(raw json.RawMessage) (configurationDocument, error) {
	if len(raw) > configurationValuesMax {
		return configurationDocument{}, fmt.Errorf("configuration values exceed %d bytes", configurationValuesMax)
	}
	var document configurationDocument
	if err := decodeStrictJSON(raw, &document, true); err != nil {
		return configurationDocument{}, fmt.Errorf("configuration values are invalid: %w", err)
	}
	if document.Version != configurationStoreVersion {
		return configurationDocument{}, fmt.Errorf("configuration values use unsupported version %d", document.Version)
	}
	if document.Global == nil {
		document.Global = map[string]json.RawMessage{}
	}
	if document.Workspaces == nil {
		document.Workspaces = map[string]map[string]json.RawMessage{}
	}
	for workspaceID, values := range document.Workspaces {
		if err := validateConfigurationWorkspaceID(workspaceID); err != nil {
			return configurationDocument{}, err
		}
		if values == nil {
			document.Workspaces[workspaceID] = map[string]json.RawMessage{}
		}
	}
	return document, nil
}

func validateConfigurationWorkspaceID(workspaceID string) error {
	if workspaceID == "" || len(workspaceID) > configurationWorkspaceMaxLength || strings.TrimSpace(workspaceID) != workspaceID {
		return errors.New("configuration workspace id is invalid")
	}
	for _, r := range workspaceID {
		if r < 0x20 || r == 0x7f {
			return errors.New("configuration workspace id is invalid")
		}
	}
	return nil
}

// loadConfigurationDocument returns the stored document. A missing document is
// empty; an unreadable one is returned as empty with readErr set so reads can
// report it while writes refuse to overwrite it without an explicit reset.
func loadConfigurationDocument(pluginID string) (document configurationDocument, readErr error, err error) {
	raw, found, err := pluginKVGetRaw(hostPluginID, configurationKVKey(pluginID))
	if err != nil {
		return configurationDocument{}, nil, err
	}
	if !found {
		return emptyConfigurationDocument(), nil, nil
	}
	document, readErr = parseConfigurationDocument(raw)
	if readErr != nil {
		return emptyConfigurationDocument(), readErr, nil
	}
	return document, nil, nil
}

func saveConfigurationDocument(pluginID string, document configurationDocument) error {
	for workspaceID, values := range document.Workspaces {
		if len(values) == 0 {
			delete(document.Workspaces, workspaceID)
		}
	}
	if len(document.Global) == 0 && len(document.Workspaces) == 0 {
		return pluginKVDeleteRaw(hostPluginID, configurationKVKey(pluginID))
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return err
	}
	if len(encoded) > configurationValuesMax {
		return fmt.Errorf("configuration values exceed %d bytes", configurationValuesMax)
	}
	return pluginKVSetRaw(hostPluginID, configurationKVKey(pluginID), encoded)
}

func deleteConfigurationDocument(pluginID string) error {
	return pluginKVDeleteRaw(hostPluginID, configurationKVKey(pluginID))
}

// decodeConfigurationStoredValue validates one stored value against schema.
func decodeConfigurationStoredValue(schema *ConfigurationSchema, key string, raw json.RawMessage, workspace bool) (any, string) {
	property := schema.Properties[key]
	if property == nil {
		return nil, "unknown setting"
	}
	if property.Secret {
		return nil, "secret settings are not stored as plain values"
	}
	if workspace && property.Scope != configurationScopeWorkspace {
		return nil, "setting cannot be overridden per workspace"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, "invalid JSON"
	}
	if err := validateConfigurationValue(property, value); err != nil {
		return nil, err.Error()
	}
	return value, ""
}

// validStoredValues returns the valid values of one scope and reports the rest.
func validStoredValues(schema *ConfigurationSchema, values map[string]json.RawMessage, scope, workspaceID string, ignored *[]ConfigurationIgnoredValue) map[string]any {
	result := map[string]any{}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, reason := decodeConfigurationStoredValue(schema, key, values[key], scope == configurationScopeWorkspace)
		if reason != "" {
			if ignored != nil {
				*ignored = append(*ignored, ConfigurationIgnoredValue{Scope: scope, WorkspaceID: workspaceID, Key: key, Reason: reason})
			}
			continue
		}
		result[key] = value
	}
	return result
}

// configurationState is everything the settings page needs for one plugin.
// Secret values never appear here: only whether each is set.
type configurationState struct {
	PluginID          string                      `json:"pluginId"`
	Schema            *ConfigurationSchema        `json:"schema"`
	WorkspaceID       string                      `json:"workspaceId,omitempty"`
	Global            map[string]any              `json:"global"`
	Workspace         map[string]any              `json:"workspace"`
	Effective         map[string]any              `json:"effective"`
	Secrets           map[string]bool             `json:"secrets"`
	OrphanSecrets     []string                    `json:"orphanSecrets"`
	Ignored           []ConfigurationIgnoredValue `json:"ignored"`
	StoreError        string                      `json:"storeError,omitempty"`
	SecretsAvailable  bool                        `json:"secretsAvailable"`
	SecretsReadFailed bool                        `json:"secretsReadFailed,omitempty"`
}

// configurationSecretKeys lists this plugin's stored configuration secrets
// (without the reserved prefix).
func configurationSecretKeys(pluginID string) ([]string, error) {
	keys, err := pluginSecretList(pluginID)
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, key := range keys {
		if strings.HasPrefix(key, configurationSecretPrefix) {
			result = append(result, strings.TrimPrefix(key, configurationSecretPrefix))
		}
	}
	return result, nil
}

// effectiveNonSecretValues resolves default -> global -> workspace override.
func effectiveNonSecretValues(schema *ConfigurationSchema, document configurationDocument, workspaceID string) map[string]any {
	global := validStoredValues(schema, document.Global, configurationScopeGlobal, "", nil)
	var workspace map[string]any
	if workspaceID != "" {
		workspace = validStoredValues(schema, document.Workspaces[workspaceID], configurationScopeWorkspace, workspaceID, nil)
	}
	effective := map[string]any{}
	for key, property := range schema.Properties {
		if property.Secret {
			continue
		}
		if value, ok := workspace[key]; ok {
			effective[key] = value
		} else if value, ok := global[key]; ok {
			effective[key] = value
		} else if property.Default != nil {
			effective[key] = property.Default
		}
	}
	return effective
}

// effectiveConfigurationValues is what host.configuration.get returns: the
// non-secret effective values plus the plain text of set secrets. Unreadable
// secrets are omitted.
func effectiveConfigurationValues(pluginID string, schema *ConfigurationSchema, workspaceID string, includeSecrets bool) (map[string]any, error) {
	document, _, err := loadConfigurationDocument(pluginID)
	if err != nil {
		return nil, err
	}
	effective := effectiveNonSecretValues(schema, document, workspaceID)
	if !includeSecrets {
		return effective, nil
	}
	for _, key := range schema.sortedKeys() {
		if !schema.Properties[key].Secret {
			continue
		}
		value, found, err := pluginSecretGet(pluginID, configurationSecretKey(key))
		if err == nil && found {
			effective[key] = value
		}
	}
	return effective, nil
}

func buildConfigurationState(pluginID string, schema *ConfigurationSchema, workspaceID string, secretsAvailable bool) (configurationState, error) {
	document, readErr, err := loadConfigurationDocument(pluginID)
	if err != nil {
		return configurationState{}, err
	}
	state := configurationState{
		PluginID: pluginID, Schema: schema, WorkspaceID: workspaceID, Secrets: map[string]bool{},
		OrphanSecrets: []string{}, Ignored: []ConfigurationIgnoredValue{}, SecretsAvailable: secretsAvailable,
	}
	if readErr != nil {
		state.StoreError = readErr.Error()
	}
	state.Global = validStoredValues(schema, document.Global, configurationScopeGlobal, "", &state.Ignored)
	state.Workspace = map[string]any{}
	workspaceIDs := make([]string, 0, len(document.Workspaces))
	for id := range document.Workspaces {
		workspaceIDs = append(workspaceIDs, id)
	}
	sort.Strings(workspaceIDs)
	for _, id := range workspaceIDs {
		values := validStoredValues(schema, document.Workspaces[id], configurationScopeWorkspace, id, &state.Ignored)
		if id == workspaceID {
			state.Workspace = values
		}
	}
	state.Effective = effectiveNonSecretValues(schema, document, workspaceID)
	secretKeys, err := configurationSecretKeys(pluginID)
	if err != nil {
		state.SecretsReadFailed = true
	}
	stored := map[string]bool{}
	for _, key := range secretKeys {
		stored[key] = true
	}
	for _, key := range schema.sortedKeys() {
		if schema.Properties[key].Secret {
			state.Secrets[key] = stored[key]
			delete(stored, key)
		}
	}
	for key := range stored {
		state.OrphanSecrets = append(state.OrphanSecrets, key)
	}
	sort.Strings(state.OrphanSecrets)
	return state, nil
}

// configurationUpdate is one validated settings write from the settings page.
// A nil value removes the key from the scope (reset to the inherited value);
// removal is allowed for ignored keys too so the user can clean them up.
type configurationUpdate struct {
	Scope       string
	WorkspaceID string
	Values      map[string]json.RawMessage
	ResetAll    bool
}

func applyConfigurationUpdate(pluginID string, schema *ConfigurationSchema, update configurationUpdate) error {
	if update.ResetAll {
		return deleteConfigurationDocument(pluginID)
	}
	document, readErr, err := loadConfigurationDocument(pluginID)
	if err != nil {
		return err
	}
	if readErr != nil {
		return fmt.Errorf("stored configuration cannot be read (%v); reset it before saving", readErr)
	}
	var target map[string]json.RawMessage
	switch update.Scope {
	case configurationScopeGlobal:
		target = document.Global
	case configurationScopeWorkspace:
		if err := validateConfigurationWorkspaceID(update.WorkspaceID); err != nil {
			return err
		}
		target = document.Workspaces[update.WorkspaceID]
		if target == nil {
			target = map[string]json.RawMessage{}
			document.Workspaces[update.WorkspaceID] = target
		}
	default:
		return errors.New("configuration scope must be global or workspace")
	}
	if len(update.Values) == 0 {
		return errors.New("configuration update has no values")
	}
	for key, raw := range update.Values {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			if _, stored := target[key]; !stored && schema.Properties[key] == nil {
				return fmt.Errorf("unknown configuration key %q", key)
			}
			delete(target, key)
			continue
		}
		value, reason := decodeConfigurationStoredValue(schema, key, trimmed, update.Scope == configurationScopeWorkspace)
		if reason != "" {
			return fmt.Errorf("configuration %q: %s", key, reason)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		target[key] = encoded
	}
	return saveConfigurationDocument(pluginID, document)
}

// setConfigurationSecret stores (or with an empty value deletes) a declared
// secret. Deleting also accepts orphaned keys the schema no longer declares.
func setConfigurationSecret(pluginID string, schema *ConfigurationSchema, key string, value *string) error {
	if value == nil || *value == "" {
		if !configurationKeyPattern.MatchString(key) {
			return fmt.Errorf("unknown configuration key %q", key)
		}
		return pluginSecretDelete(pluginID, configurationSecretKey(key))
	}
	property := schema.Properties[key]
	if property == nil || !property.Secret {
		return fmt.Errorf("configuration %q is not a secret setting", key)
	}
	if err := validateConfigurationValue(property, *value); err != nil {
		return fmt.Errorf("configuration %q: %w", key, err)
	}
	return pluginSecretSet(pluginID, configurationSecretKey(key), *value)
}

// diffConfigurationValues lists keys whose effective value differs.
func diffConfigurationValues(before, after map[string]any) []string {
	changed := []string{}
	seen := map[string]bool{}
	for key, value := range before {
		seen[key] = true
		next, ok := after[key]
		if !ok || !jsonEqual(value, next) {
			changed = append(changed, key)
		}
	}
	for key := range after {
		if !seen[key] {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

func jsonEqual(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}
