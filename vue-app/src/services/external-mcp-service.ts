import { reactive, readonly, watch } from "vue";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { invokeBackend, isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";

export interface ExternalMcpStatus {
  enabled: boolean;
  projectCount: number;
  rendezvousFile: string;
}

type Project = { name: string; root: string };
const state = reactive({ status: null as ExternalMcpStatus | null, busy: false, error: "" });
export const externalMcpState = readonly(state);
let queue: Promise<unknown> = Promise.resolve();
let stopWatching: (() => void) | undefined;

function parseStatus(value: unknown): ExternalMcpStatus {
  if (!value || typeof value !== "object") throw new Error("Invalid external MCP status");
  const result = value as Partial<ExternalMcpStatus>;
  if (typeof result.enabled !== "boolean" || !Number.isInteger(result.projectCount) ||
      (result.projectCount ?? -1) < 0 || typeof result.rendezvousFile !== "string") {
    throw new Error("Invalid external MCP status");
  }
  return result as ExternalMcpStatus;
}

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("External MCP requires the desktop app");
}

function currentScope(): { key: string; projects: Project[] } {
  const workbench = useWorkbenchStore();
  const preferences = useWorkspaceStatePreferencesStore();
  if (!workbench.hydrated || !preferences.hydrated) {
    throw new Error("Workspace projects are not ready");
  }
  const scopeId = useWorkspaceStateScope().scopeId.value;
  const projects: Project[] = [];
  return { key: JSON.stringify([scopeId, projects]), projects };
}

async function status(): Promise<ExternalMcpStatus> {
  return parseStatus(await invokeBackend<unknown>("external_mcp_status"));
}

export interface ExternalMcpClientExecutable {
  executable: string;
  /** Launched from a mounted disk image or App Translocation; the path stops working later. */
  transient: boolean;
}

export async function getExternalMcpClientExecutable(): Promise<ExternalMcpClientExecutable> {
  requireDesktop();
  const result: unknown = await invokeBackend<unknown>("external_mcp_client_executable");
  if (!result || typeof result !== "object" || !("executable" in result)) {
    throw new Error("Invalid external MCP executable response");
  }
  const executable = result.executable;
  const hasControlCharacter = typeof executable === "string" && [...executable].some((character) => character.charCodeAt(0) < 0x20);
  if (typeof executable !== "string" || executable.length > 4096 ||
      !(executable.startsWith("/") || /^[A-Za-z]:[\\/]/.test(executable) || /^\\\\[^\\]+\\[^\\]+\\/.test(executable)) ||
      hasControlCharacter) {
    throw new Error("Invalid external MCP executable path");
  }
  return { executable, transient: "transient" in result && result.transient === true };
}

async function disableNative() {
  await invokeBackend("external_mcp_disable");
  state.status = { enabled: false, projectCount: 0, rendezvousFile: "" };
}

async function failClosed(error: unknown): Promise<never> {
  try {
    await disableNative();
  } catch (disableError) {
    state.status = null;
    state.error = `${nativeErrorMessage(error)}; disable failed: ${nativeErrorMessage(disableError)}`;
    throw disableError;
  }
  state.error = nativeErrorMessage(error);
  throw error;
}

function serial<T>(operation: () => Promise<T>): Promise<T> {
  const run = queue.then(async () => {
    state.busy = true;
    try { return await operation(); }
    finally { state.busy = false; }
  });
  queue = run.catch(() => undefined);
  return run;
}

export function refreshExternalMcp(): Promise<ExternalMcpStatus> {
  return serial(async () => {
    requireDesktop();
    state.status = await status();
    state.error = "";
    return state.status;
  });
}

export function enableExternalMcp(): Promise<void> {
  return serial(async () => {
    requireDesktop();
    // Never send a previous workspace's roots while a workspace switch is in progress.
    const scope = currentScope();
    try {
      state.status = parseStatus(await invokeBackend<unknown>("external_mcp_enable", { projects: scope.projects }));
      if (currentScope().key !== scope.key) throw new Error("Workspace changed during external MCP enable");
      state.error = "";
    } catch (error) { await failClosed(error); }
  });
}

export function disableExternalMcp(): Promise<void> {
  return serial(async () => {
    requireDesktop();
    try { await disableNative(); state.error = ""; }
    catch (error) { state.status = null; state.error = nativeErrorMessage(error); throw error; }
  });
}

export function startExternalMcpScopeSync(): () => void {
  if (stopWatching || !isDesktopRuntime()) return stopWatching ?? (() => undefined);
  const workbench = useWorkbenchStore();
  const preferences = useWorkspaceStatePreferencesStore();
  const workspaceScope = useWorkspaceStateScope();
  let previous = "";
  let revision = 0;
  stopWatching = watch(
    () => [state.status?.enabled, workbench.hydrated, preferences.hydrated,
      workspaceScope.scopeId.value, workbench.activeWorkspaceId].join("\u0000"),
    () => {
      const changed = ++revision;
      void serial(async () => {
        if (!state.status?.enabled || changed !== revision) return;
        try {
          const scope = currentScope();
          if (scope.key === previous) return;
          state.status = parseStatus(await invokeBackend<unknown>("external_mcp_update", { projects: scope.projects }));
          previous = scope.key;
          // A switch during the native call must not leave the old scope exposed.
          if (currentScope().key !== scope.key) throw new Error("Workspace changed during external MCP update");
          state.error = "";
        } catch (error) { await failClosed(error); }
      }).catch(() => undefined);
    },
    { flush: "post" },
  );
  return () => { stopWatching?.(); stopWatching = undefined; };
}
