import { computed } from "vue";
import type { WorkspaceScopedPersistenceOptions } from "@/services/workspace-scoped-persistence";
import { createWorkspaceScopedPersistence } from "@/services/workspace-scoped-persistence";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";

export interface CapturedWorkspaceStateScope {
  scopeId: string;
  workspaceId: string;
}

/**
 * Single source of truth for feature runtime state. New workspace features
 * should consume this composable instead of reading activeWorkspaceId and
 * constructing persistence keys independently.
 */
export function useWorkspaceStateScope() {
  const workbench = useWorkbenchStore();
  const preferences = useWorkspaceStatePreferencesStore();
  const workspaceId = computed(() => workbench.activeWorkspaceId);
  const scopeId = computed(() => preferences.scopeIdFor(workspaceId.value));
  const isolationEnabled = computed(() => preferences.isolationEnabled);

  async function hydrate() {
    await Promise.all([
      workbench.hydrated ? Promise.resolve() : workbench.hydrate(),
      preferences.hydrated ? Promise.resolve() : preferences.hydrate(),
    ]);
    return capture();
  }

  function capture(): CapturedWorkspaceStateScope {
    return { scopeId: scopeId.value, workspaceId: workspaceId.value };
  }

  function isCurrent(captured: CapturedWorkspaceStateScope | string) {
    const capturedScopeId = typeof captured === "string" ? captured : captured.scopeId;
    return scopeId.value === capturedScopeId;
  }

  return {
    workbench,
    preferences,
    workspaceId,
    scopeId,
    isolationEnabled,
    hydrate,
    capture,
    isCurrent,
  };
}

/**
 * Default persistence entry point for new workspace runtime state. The current
 * scope is captured when an operation starts so an in-flight save cannot drift
 * into another workspace after the user switches workspaces.
 */
export function useWorkspaceScopedPersistence<T>(
  baseKey: string,
  options: WorkspaceScopedPersistenceOptions<T> = {}
) {
  const workspaceScope = useWorkspaceStateScope();
  const persistence = createWorkspaceScopedPersistence<T>(baseKey, options);

  return {
    scopeId: workspaceScope.scopeId,
    key: computed(() => persistence.key(workspaceScope.scopeId.value)),
    async load(captured = workspaceScope.capture()) {
      return persistence.load(captured.scopeId);
    },
    save(value: T, captured = workspaceScope.capture()) {
      return persistence.save(captured.scopeId, value);
    },
    capture: workspaceScope.capture,
    isCurrent: workspaceScope.isCurrent,
  };
}
