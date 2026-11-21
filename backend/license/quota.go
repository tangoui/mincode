package license

import "fmt"

// QuotaPolicy describes limits from a verified, product-scoped cloud policy.
// A limit of zero explicitly means unlimited; callers must not construct this
// from a missing response field or treat an unverified response as a policy.
type QuotaPolicy struct {
	MaxDevices             int64 `json:"max_devices"`
	MaxActiveWorkspaces    int64 `json:"max_active_workspaces"`
	MaxWindowsPerWorkspace int64 `json:"max_windows_per_workspace"`
	// MaxEnabledPlugins is only carried by schema 3 snapshots; schema 2 and older free policies mean 0 (unlimited).
	MaxEnabledPlugins int64 `json:"max_enabled_plugins"`
	// PluginsDisabled mirrors the wire field allow_plugins inverted, so the zero value
	// (schema 2, older clouds, cached free policies) keeps plugins allowed.
	PluginsDisabled bool `json:"plugins_disabled"`
}

type QuotaResource string

const (
	QuotaDevice    QuotaResource = "devices"
	QuotaWorkspace QuotaResource = "active_workspaces"
	QuotaWindow    QuotaResource = "windows_per_workspace"
	QuotaPlugin    QuotaResource = "enabled_plugins"
)

type QuotaDecision struct {
	Allowed bool
	Code    string
	Current int64
	Limit   int64
}

// EvaluateQuota checks an attempted increase. Existing resources remain usable
// even when a cloud policy is lowered or a time-limited grant expires.
func EvaluateQuota(policy QuotaPolicy, resource QuotaResource, current, increase int64) (QuotaDecision, error) {
	if policy.MaxDevices < 0 || policy.MaxActiveWorkspaces < 0 || policy.MaxWindowsPerWorkspace < 0 || policy.MaxEnabledPlugins < 0 {
		return QuotaDecision{}, fmt.Errorf("quota policy contains a negative limit")
	}
	if current < 0 || increase <= 0 {
		return QuotaDecision{}, fmt.Errorf("quota usage or increase is invalid")
	}

	var limit int64
	switch resource {
	case QuotaDevice:
		limit = policy.MaxDevices
	case QuotaWorkspace:
		limit = policy.MaxActiveWorkspaces
	case QuotaWindow:
		limit = policy.MaxWindowsPerWorkspace
	case QuotaPlugin:
		if policy.PluginsDisabled {
			return QuotaDecision{Allowed: false, Code: "plugins_not_allowed", Current: current}, nil
		}
		limit = policy.MaxEnabledPlugins
	default:
		return QuotaDecision{}, fmt.Errorf("unknown quota resource %q", resource)
	}

	decision := QuotaDecision{Allowed: true, Current: current, Limit: limit}
	if limit != 0 && (current >= limit || increase > limit-current) {
		decision.Allowed = false
		decision.Code = string(resource) + "_limit_reached"
	}
	return decision, nil
}
