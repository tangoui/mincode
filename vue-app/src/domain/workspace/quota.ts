export interface QuotaLimits {
  max_devices: number;
  max_active_workspaces: number;
  max_windows_per_workspace: number;
  /** Absent from backends that predate the plugin limit, which means unlimited. */
  max_enabled_plugins?: number;
  /** True when the plan forbids plugins entirely; absent means allowed. */
  plugins_disabled?: boolean;
}

export type QuotaResource = "workspace" | "tab" | "plugin";

export interface QuotaDecision {
  allowed: boolean;
  resource: QuotaResource;
  current: number;
  limit: number;
  /** Set when the plan forbids the resource outright rather than capping it. */
  forbidden?: boolean;
}

/**
 * Decides whether one more workspace, tab or enabled plugin may be added. A limit of 0 means unlimited, and missing
 * limits (no trusted policy was ever obtained) never block. Existing resources over the limit stay usable;
 * only increases are refused.
 */
export function evaluateQuota(limits: QuotaLimits | null | undefined, resource: QuotaResource, current: number): QuotaDecision {
  if (resource === "plugin" && limits?.plugins_disabled === true) {
    return { allowed: false, resource, current, limit: 0, forbidden: true };
  }
  const limit = !limits
    ? 0
    : resource === "workspace"
      ? limits.max_active_workspaces
      : resource === "tab"
        ? limits.max_windows_per_workspace
        : (limits.max_enabled_plugins ?? 0);
  const safeLimit = Number.isSafeInteger(limit) && limit > 0 ? limit : 0;
  return { allowed: safeLimit === 0 || current < safeLimit, resource, current, limit: safeLimit };
}
