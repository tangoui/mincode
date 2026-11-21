import type { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

// Startup-safe entry points for the database workspace store. App shell code
// (App.vue, AppTabBar.vue) must not import the full store statically, otherwise
// the database workspace chunk is preloaded on every launch.

export type DatabaseWorkspaceStore = ReturnType<typeof useDatabaseWorkspaceStore>;

let storeFactory: (() => DatabaseWorkspaceStore) | undefined;
let pendingScopeId: string | undefined;

/** Called by the full store while it is created. */
export function registerDatabaseWorkspaceStore(factory: () => DatabaseWorkspaceStore) {
  storeFactory = factory;
}

/** Workspace scope requested before the full store existed; consumed once by the store. */
export function takePendingDatabaseWorkspaceScope(): string | undefined {
  const scopeId = pendingScopeId;
  pendingScopeId = undefined;
  return scopeId;
}

export function switchDatabaseWorkspaceScope(scopeId: string) {
  const normalized = scopeId.trim();
  if (!normalized) return;
  const store = storeFactory?.();
  if (store) store.switchWorkspaceScope(normalized);
  else pendingScopeId = normalized;
}

/** A store that was never created owns no scope runtimes or sessions to discard. */
export async function discardDatabaseWorkspaceScope(scopeId: string): Promise<boolean> {
  const store = storeFactory?.();
  return store ? store.discardWorkspaceScope(scopeId) : false;
}
