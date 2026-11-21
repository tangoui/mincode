/**
 * Consumer of the `statusBar` contribution point (process plugins). The Go
 * host validates items and merges dynamic `host.statusBar.update` state into
 * the snapshot's `runtime` field (coalesced to at most one push per 250 ms);
 * this consumer only stores the contributions for AppStatusBar.vue, which
 * re-validates and renders them. Registering data never starts a plugin.
 */
import { STATUS_BAR_CONTRIBUTION_POINT } from "@/domain/plugins/status-bar";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { executePluginCommand } from "@/services/plugin-commands";
import { listPluginContexts } from "@/services/plugin-service";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { useStatusBarStore } from "@/stores/status-bar";

const CONTEXT_REFRESH_DEBOUNCE_MS = 100;

let lastSyncError: string | undefined;

/** The error of the last failed refresh (plugin host unavailable), if any. */
export function statusBarSyncError(): string | undefined {
  return lastSyncError;
}

export function registerStatusBarContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: STATUS_BAR_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const store = useStatusBarStore();
      store.setContributions(contributions);
      lastSyncError = undefined;
      return () => store.setContributions([]);
    },
    onError: (message) => {
      lastSyncError = message;
    },
  });
}

/**
 * Keeps plugin command contexts (host.context.set values) current for status
 * bar `when` clauses. Started by AppStatusBar.vue; returns a stop function.
 */
export function startStatusBarContextSync(): () => void {
  if (!isDesktopRuntime()) return () => undefined;
  const store = useStatusBarStore();
  let stopped = false;
  let sequence = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const refresh = () => {
    const current = ++sequence;
    void listPluginContexts()
      .then((contexts) => {
        if (!stopped && current === sequence) store.setContexts(contexts);
      })
      .catch(() => {
        // Keep the last known contexts on transient failures.
      });
  };
  const schedule = () => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = undefined;
      refresh();
    }, CONTEXT_REFRESH_DEBOUNCE_MS);
  };
  const stops = [
    listenAppEvent("plugin:context-changed", schedule),
    listenAppEvent("plugin:state-changed", schedule),
  ];
  refresh();
  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
    for (const stop of stops) stop();
    store.setContexts({});
  };
}

/** Runs a status bar item's command through the shared plugin command registry. */
export async function runStatusBarCommand(pluginId: string, commandId: string): Promise<void> {
  await executePluginCommand(pluginId, commandId);
}
