import { loadPersistedValue, savePersistedValue, savePersistedValueWithBrowserSnapshot } from "@/services/persistent-store";
import {
  loadSecurePersistedValue,
  saveSecurePersistedValue,
} from "@/services/secure-persistent-store";
import { isWorkspaceScopePermanentlyDeleted } from "@/services/workspace-deleted-scopes";

const WORKSPACE_SCOPE_MIGRATION_SUFFIX = "__workspace_scope_owner_v1";

export type WorkspaceScopedPersistenceOptions<T> =
  | {
      secure: true;
      migrateLegacy?: boolean;
      browserSnapshot: (value: T) => unknown;
    }
  | {
      secure?: false;
      migrateLegacy?: boolean;
      browserSnapshot?: never;
      writeBrowserSnapshotInDesktop?: boolean;
    };

export function workspaceScopedStorageKey(baseKey: string, scopeId: string) {
  return `${baseKey}__workspace__${encodeURIComponent(scopeId)}`;
}

/**
 * Assigns a legacy global snapshot to the first workspace scope that requests it.
 * The marker makes migration idempotent and prevents every workspace from cloning
 * the same legacy runtime state.
 */
export async function ownsLegacyWorkspaceSnapshot(baseKey: string, scopeId: string) {
  const markerKey = `${baseKey}${WORKSPACE_SCOPE_MIGRATION_SUFFIX}`;
  const owner = await loadPersistedValue<unknown>(markerKey);
  if (typeof owner === "string" && owner.trim()) return owner === scopeId;
  await savePersistedValue(markerKey, scopeId);
  return true;
}

/** Returns whether this scope already owns the global legacy snapshot without claiming it. */
export async function isLegacyWorkspaceSnapshotOwner(baseKey: string, scopeId: string) {
  const owner = await loadPersistedValue<unknown>(`${baseKey}${WORKSPACE_SCOPE_MIGRATION_SUFFIX}`);
  return typeof owner === "string" && owner.trim() === scopeId;
}

export function createWorkspaceScopedPersistence<T>(
  baseKey: string,
  options: WorkspaceScopedPersistenceOptions<T> = {}
) {
  const migrateLegacy = options.migrateLegacy !== false;

  async function load(scopeId: string): Promise<T | null> {
    if (isWorkspaceScopePermanentlyDeleted(scopeId)) return null;
    const scopedKey = workspaceScopedStorageKey(baseKey, scopeId);
    const scoped = options.secure
      ? await loadSecurePersistedValue<T>(scopedKey)
      : await loadPersistedValue<T>(scopedKey);
    if (scoped !== null) return scoped;
    if (!migrateLegacy || !(await ownsLegacyWorkspaceSnapshot(baseKey, scopeId))) return null;
    return options.secure
      ? loadSecurePersistedValue<T>(baseKey)
      : loadPersistedValue<T>(baseKey);
  }

  function save(scopeId: string, value: T): Promise<void> {
    if (isWorkspaceScopePermanentlyDeleted(scopeId)) return Promise.resolve();
    const scopedKey = workspaceScopedStorageKey(baseKey, scopeId);
    if (options.secure) {
      const browserSnapshot = options.browserSnapshot?.(value);
      if (browserSnapshot === undefined) {
        return Promise.reject(
          new Error(`Secure workspace persistence requires browserSnapshot for ${baseKey}`)
        );
      }
      return saveSecurePersistedValue(scopedKey, value, browserSnapshot);
    }
    return savePersistedValueWithBrowserSnapshot(scopedKey, value, value, {
      writeBrowserSnapshotInDesktop: options.writeBrowserSnapshotInDesktop,
    });
  }

  return {
    key: (scopeId: string) => workspaceScopedStorageKey(baseKey, scopeId),
    load,
    save,
  };
}
