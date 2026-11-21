package plugin

import (
	"encoding/json"
	"fmt"

	"mincode/app/backend/core"
	"mincode/app/backend/license"
)

// enabledPluginQuota reads the desktop's effective cloud quota through the
// registered command, so this package does not depend on cloudcompanion.
// A missing policy or any lookup failure never blocks, like workspace and tab limits.
var enabledPluginQuota = func() (license.QuotaPolicy, string, bool) {
	raw, err := core.Dispatch("cloud_quota_effective", "{}")
	if err != nil {
		return license.QuotaPolicy{}, "", false
	}
	var quota struct {
		Plan   string               `json:"plan"`
		Limits *license.QuotaPolicy `json:"limits"`
	}
	if json.Unmarshal([]byte(raw), &quota) != nil || quota.Limits == nil {
		return license.QuotaPolicy{}, "", false
	}
	return *quota.Limits, quota.Plan, true
}

// admitEnabledPlugin refuses to turn on one more plugin once the plan's cap is
// reached. Plugins that are already enabled stay usable after a downgrade.
// Callers look the quota up first, outside their locks, because it may hit the network.
func (manager *pluginManager) admitEnabledPlugin(candidate *pluginRecord, policy license.QuotaPolicy, plan string) error {
	if policy.MaxEnabledPlugins == 0 && !policy.PluginsDisabled {
		return nil
	}
	// Every installed plugin takes a slot, declarative (data-only) ones included.
	if candidate != nil {
		candidate.mu.Lock()
		already := candidate.enabled
		candidate.mu.Unlock()
		if already {
			return nil
		}
	}
	current := manager.enabledPluginCount(candidate)
	decision, err := license.EvaluateQuota(policy, license.QuotaPlugin, current, 1)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		if plan == "" {
			plan = "free"
		}
		if decision.Code == "plugins_not_allowed" {
			return fmt.Errorf("plugins are not available on the %s plan; upgrade in Settings → Cloud account", plan)
		}
		return fmt.Errorf("plugin limit reached: the %s plan allows %d enabled plugins (%d enabled); disable another plugin or upgrade in Settings → Cloud account",
			plan, decision.Limit, decision.Current)
	}
	return nil
}

func (manager *pluginManager) enabledPluginCount(except *pluginRecord) int64 {
	manager.mu.RLock()
	records := make([]*pluginRecord, 0, len(manager.records))
	for _, record := range manager.records {
		if record != except {
			records = append(records, record)
		}
	}
	manager.mu.RUnlock()
	var count int64
	for _, record := range records {
		record.mu.Lock()
		if record.enabled && record.source != pluginSourceBuiltin {
			count++
		}
		record.mu.Unlock()
	}
	return count
}
