package plugin

import (
	"encoding/json"
	"errors"
)

// ErrPaidHostRequired is returned when a marketplace plugin that the cloud marks
// as "requires a paid MinCode plan" is enabled or started on a free plan.
var ErrPaidHostRequired = errors.New("this plugin requires a paid MinCode plan (annual or perpetual); purchase or activate one in Settings → Cloud account")

func paidHostRequirementKey(id string) string { return id + ".requiresPaidHost" }

// RecordMarketPaidHostRequirement stores the cloud's requires_paid_host flag for
// a plugin installed from the marketplace. The flag stays after uninstall so a
// reinstall from a sideloaded copy of the same ID keeps the requirement.
func RecordMarketPaidHostRequirement(id string, required bool) error {
	if !pluginIDPattern.MatchString(id) {
		return errors.New("invalid plugin ID")
	}
	if !required {
		return pluginKVDeleteRaw(hostPluginID, paidHostRequirementKey(id))
	}
	value, _ := json.Marshal(true)
	return pluginKVSetRaw(hostPluginID, paidHostRequirementKey(id), value)
}

func pluginRequiresPaidHost(id string) bool {
	raw, found, err := pluginKVGetRaw(hostPluginID, paidHostRequirementKey(id))
	if err != nil || !found {
		return false
	}
	var required bool
	return json.Unmarshal(raw, &required) == nil && required
}

func paidHostPlan(plan string) bool { return plan == "annual" || plan == "perpetual" }

// HostPlanPaid reports whether the effective cloud quota is a paid host plan.
// A failed lookup or an unknown plan counts as unpaid.
var HostPlanPaid = func() bool {
	_, plan, ok := enabledPluginQuota()
	return ok && paidHostPlan(plan)
}

// admitPaidHostPlugin is called outside manager locks because the plan lookup may hit the network.
func admitPaidHostPlugin(id string) error {
	if !pluginRequiresPaidHost(id) || HostPlanPaid() {
		return nil
	}
	return ErrPaidHostRequired
}
