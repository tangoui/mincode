export const WORKSPACE_STATE_PREFERENCES_VERSION = 1;
export const DEFAULT_WORKSPACE_STATE_ISOLATION_ENABLED = true;
const MAX_WORKSPACE_SCOPE_ID_LENGTH = 512;

// 工作区视图 KeepAlive 缓存上限：限制多工作区场景下常驻的视图实例数量，
// 超出时按最近最少使用淘汰；0 表示不限制，适合需要长期保持连接会话的场景。
export const DEFAULT_WORKSPACE_CACHE_LIMIT = 6;
export const MIN_WORKSPACE_CACHE_LIMIT = 0;
export const MAX_WORKSPACE_CACHE_LIMIT = 20;

export function normalizeWorkspaceCacheLimit(value: unknown): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) return DEFAULT_WORKSPACE_CACHE_LIMIT;
  return Math.min(
    MAX_WORKSPACE_CACHE_LIMIT,
    Math.max(MIN_WORKSPACE_CACHE_LIMIT, Math.round(parsed))
  );
}

export interface WorkspaceStatePreferencesSnapshot {
  version: typeof WORKSPACE_STATE_PREFERENCES_VERSION;
  isolationEnabled: boolean;
  sharedWorkspaceId?: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function normalizeWorkspaceScopeId(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const normalized = value.trim();
  if (!normalized || normalized.length > MAX_WORKSPACE_SCOPE_ID_LENGTH) return undefined;
  return normalized;
}

export function restoreWorkspaceStatePreferences(
  value: unknown
): WorkspaceStatePreferencesSnapshot {
  if (!isRecord(value) || value.version !== WORKSPACE_STATE_PREFERENCES_VERSION) {
    return {
      version: WORKSPACE_STATE_PREFERENCES_VERSION,
      isolationEnabled: DEFAULT_WORKSPACE_STATE_ISOLATION_ENABLED,
    };
  }
  return {
    version: WORKSPACE_STATE_PREFERENCES_VERSION,
    isolationEnabled: value.isolationEnabled !== false,
    sharedWorkspaceId: normalizeWorkspaceScopeId(value.sharedWorkspaceId),
  };
}

export function resolveWorkspaceStateScope(
  preferences: WorkspaceStatePreferencesSnapshot,
  workspaceId: string
): string {
  const normalizedWorkspaceId = normalizeWorkspaceScopeId(workspaceId) ?? "workspace:default";
  if (preferences.isolationEnabled) return normalizedWorkspaceId;
  return preferences.sharedWorkspaceId ?? normalizedWorkspaceId;
}

export function updateWorkspaceStateIsolation(
  preferences: WorkspaceStatePreferencesSnapshot,
  enabled: boolean,
  activeWorkspaceId: string
): WorkspaceStatePreferencesSnapshot {
  if (enabled) {
    return { ...preferences, isolationEnabled: true };
  }
  return {
    ...preferences,
    isolationEnabled: false,
    sharedWorkspaceId:
      normalizeWorkspaceScopeId(activeWorkspaceId) ?? preferences.sharedWorkspaceId,
  };
}
