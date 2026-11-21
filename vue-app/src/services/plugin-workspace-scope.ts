import { watch } from "vue";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { useLocalProjectStore } from "@/stores/local-project";
import { beginPluginWorkspaceScope, commitPluginWorkspaceScope, restorePluginFloatingViews, type PluginWorkspaceScope } from "@/services/plugin-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

export type { PluginWorkspaceScope } from "@/services/plugin-service";

function parsePluginWorkspaceScope(raw: unknown): PluginWorkspaceScope {
  if (!raw || typeof raw !== "object") throw new Error("Invalid plugin workspace scope");
  const value = raw as Record<string, unknown>;
  if (typeof value.workspaceId !== "string" || typeof value.root !== "string"
    || typeof value.revision !== "string" || !/^\d+$/.test(value.revision)
    || typeof value.ready !== "boolean" || value.ready !== Boolean(value.root)) {
    throw new Error("Invalid plugin workspace scope");
  }
  return Object.freeze({ workspaceId: value.workspaceId, root: value.root, revision: value.revision, ready: value.ready });
}


let current: PluginWorkspaceScope | undefined;
const listeners = new Set<(scope: PluginWorkspaceScope | undefined) => void>();
let owner: symbol | undefined;
// Module-wide, so disposal/reinstallation cannot overlap an older native begin.
let transactions = Promise.resolve();

function publish(scope: PluginWorkspaceScope | undefined) {
  current = scope;
  for (const fn of listeners) fn(scope);
}
export function currentPluginWorkspaceScope(): PluginWorkspaceScope | undefined { return current; }
export function onPluginWorkspaceScopeChange(fn: (scope: PluginWorkspaceScope | undefined) => void): () => void {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
}

/** Main-window only. All writes use workspace-scoped project state, never the
 * still-mounted previous workspace's projectRoot. A backend get is deliberately
 * not adopted: each installation must begin a new host revision first. */
export function installPluginWorkspaceScopeBridge(): () => void {
  if (!isDesktopRuntime() || owner) return () => undefined;
  const token = Symbol("plugin workspace scope");
  owner = token;
  publish(undefined);
  const state = useWorkspaceStateScope();
  const project = useLocalProjectStore();
  let epoch = 0;
  let running = false;
  let pending = false;
  let disposed = false;

  const capture = () => {
    const captured = state.capture();
    const hydrated = state.workbench.hydrated && state.preferences.hydrated && project.hydrated;
    const root = hydrated ? project.projectRootForWorkspace(captured.scopeId).trim()
      || state.workbench.workspaces.find((workspace) => workspace.id === captured.workspaceId)?.worktree?.worktreePath?.trim() || "" : "";
    return { ...captured, root, hydrated };
  };
  type Desired = ReturnType<typeof capture>;
  const isCurrent = (request: Desired, requestEpoch: number) => {
    const now = capture();
    return !disposed && epoch === requestEpoch && now.workspaceId === request.workspaceId
      && now.scopeId === request.scopeId && now.root === request.root && now.hydrated === request.hydrated;
  };
  const drain = async () => {
    try {
      while (pending && !disposed) {
        pending = false;
        const request = capture();
        const requestEpoch = epoch;
        if (!state.workbench.hydrated || !state.preferences.hydrated) continue;
        try {
          const ticket = parsePluginWorkspaceScope(await beginPluginWorkspaceScope(request.workspaceId));
          if (ticket.workspaceId !== request.workspaceId || ticket.ready || ticket.root || ticket.revision === "0") throw new Error("Invalid plugin workspace ticket");
          if (!isCurrent(request, requestEpoch) || !request.hydrated) continue;
          const scope = parsePluginWorkspaceScope(await commitPluginWorkspaceScope(request.workspaceId, ticket.revision, request.root));
          if (scope.workspaceId !== request.workspaceId || scope.revision !== ticket.revision) throw new Error("Stale plugin workspace scope");
          if (!isCurrent(request, requestEpoch)) continue;
          publish(scope);
          // Outside the scope transaction: like the old bridge, restore is
          // best effort, never clears a committed scope and never delays the
          // next queued switch. Restore needs no running process: reopened
          // pages activate through BeginUI's lazy ensureRunning path.
          void restorePluginFloatingViews().catch(() => undefined);
        } catch (error) {
          // Fail closed; no adopting get/old ready state or fake browser success.
          if (isCurrent(request, requestEpoch)) {
            publish(undefined);
            // No UI owner exists here; log only the error category, never roots/payloads.
            // eslint-disable-next-line no-console -- fail-closed background bridge diagnostic
            console.warn("Plugin workspace scope synchronization failed", error instanceof Error ? error.name : "error");
          }
        }
      }
    } finally { running = false; }
  };
  const stop = watch(
    () => {
      const request = capture();
      return [request.workspaceId, request.scopeId, request.root, request.hydrated,
        state.workbench.hydrated, state.preferences.hydrated] as const;
    },
    () => {
      epoch++;
      publish(undefined);
      pending = true;
      if (running) return;
      running = true;
      transactions = transactions.then(drain);
    },
    { immediate: true, flush: "sync" }
  );
  return () => {
    if (disposed) return;
    disposed = true;
    epoch++;
    pending = false;
    stop();
    if (owner === token) { owner = undefined; publish(undefined); }
  };
}
