export const PROJECT_WORKSPACE_COLLECTION_VERSION = 1 as const;
const MAX_PERSISTED_WORKSPACES = 256;
const MAX_WORKSPACE_ID_LENGTH = 512;

export interface PersistedProjectWorkspaceCollection<T> {
  version: typeof PROJECT_WORKSPACE_COLLECTION_VERSION;
  workspaces: Record<string, T>;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function normalizeProjectWorkspaceId(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const workspaceId = value.trim();
  return workspaceId && workspaceId.length <= MAX_WORKSPACE_ID_LENGTH ? workspaceId : null;
}

export function restoreProjectWorkspaceCollection<T>(
  value: unknown,
  restoreWorkspace: (value: unknown) => T | null
): PersistedProjectWorkspaceCollection<T> | null {
  if (!isRecord(value) || value.version !== PROJECT_WORKSPACE_COLLECTION_VERSION) return null;
  if (!isRecord(value.workspaces)) return null;

  const workspaces: Record<string, T> = {};
  for (const [rawWorkspaceId, candidate] of Object.entries(value.workspaces)) {
    const workspaceId = normalizeProjectWorkspaceId(rawWorkspaceId);
    const workspace = restoreWorkspace(candidate);
    if (!workspaceId || !workspace) continue;
    workspaces[workspaceId] = workspace;
    if (Object.keys(workspaces).length >= MAX_PERSISTED_WORKSPACES) break;
  }

  return {
    version: PROJECT_WORKSPACE_COLLECTION_VERSION,
    workspaces,
  };
}

export function migrateLegacyProjectWorkspace<T>(
  collection: PersistedProjectWorkspaceCollection<T> | null,
  activeWorkspaceId: string,
  legacyWorkspace: T | null
): PersistedProjectWorkspaceCollection<T> {
  if (collection) return collection;
  const workspaceId = normalizeProjectWorkspaceId(activeWorkspaceId);
  return {
    version: PROJECT_WORKSPACE_COLLECTION_VERSION,
    workspaces: workspaceId && legacyWorkspace ? { [workspaceId]: legacyWorkspace } : {},
  };
}
