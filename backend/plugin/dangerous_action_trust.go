package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
)

const pluginDangerousActionTrustVersion = 1

func pluginDangerousActionTrustKey(pluginID string) string {
	return "dangerous-actions/" + pluginID
}

type pluginDangerousActionTrust struct {
	Version       int    `json:"version"`
	PluginVersion string `json:"pluginVersion"`
}

func pluginDangerousActionTrustMatches(raw json.RawMessage, pluginVersion string) bool {
	var trust pluginDangerousActionTrust
	return json.Unmarshal(raw, &trust) == nil &&
		trust.Version == pluginDangerousActionTrustVersion &&
		trust.PluginVersion == pluginVersion
}

func (manager *pluginManager) dangerousActionsTrusted(id string) (bool, error) {
	record, err := manager.record(id)
	if err != nil {
		return false, err
	}
	record.mu.Lock()
	manifest, local := record.manifest, record.source == pluginSourceLocal
	version := ""
	if manifest != nil {
		version = manifest.Version
	}
	record.mu.Unlock()
	if !local || version == "" {
		return false, nil
	}
	raw, found, err := pluginKVGetRaw(hostPluginID, pluginDangerousActionTrustKey(id))
	if err != nil || !found {
		return false, err
	}
	return pluginDangerousActionTrustMatches(raw, version), nil
}

// setDangerousActionsTrusted writes or deletes the legacy record. Nothing in
// the app grants it any more (plugin_full_trust_set only deletes it); tests use
// it to stand in for records written by earlier versions.
func (manager *pluginManager) setDangerousActionsTrusted(id string, trusted bool) error {
	record, err := manager.record(id)
	if err != nil {
		return err
	}
	record.mu.Lock()
	manifest, local := record.manifest, record.source == pluginSourceLocal
	version := ""
	if manifest != nil {
		version = manifest.Version
	}
	record.mu.Unlock()
	if !local || version == "" {
		return errors.New("dangerous action trust is only available for installed local plugins")
	}
	key := pluginDangerousActionTrustKey(id)
	if !trusted {
		return pluginKVDeleteRaw(hostPluginID, key)
	}
	raw, err := json.Marshal(pluginDangerousActionTrust{Version: pluginDangerousActionTrustVersion, PluginVersion: version})
	if err != nil {
		return fmt.Errorf("encode plugin dangerous action trust: %w", err)
	}
	return pluginKVSetRaw(hostPluginID, key, raw)
}
