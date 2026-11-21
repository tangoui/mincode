import { watch } from "vue";
import { releasedTerminalPanes } from "@/domain/terminal/tab-cleanup";
import { useTerminalTabPanes } from "@/composables/use-terminal-attention";
import { deleteTerminalSnapshot } from "@/services/terminal-session-cache";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";

/**
 * Closing a terminal tab ends its shells. Panes keep their PTY while their tab is
 * merely hidden (LocalTerminal keepAliveOnUnmount), so without this a closed tab's
 * shells kept running and its cached screen stayed in memory.
 */
export function useTerminalTabCleanup() {
  const workbench = useWorkbenchStore();
  const terminalWorkspace = useTerminalWorkspaceStore();
  const terminalTabPaneIds = useTerminalTabPanes();

  // Panes shown by any open terminal tab (in shared workspace mode, by its layout slot).
  function openTabPanes() {
    return workbench.tabs.filter((tab) => tab.view === "terminal").flatMap((tab) => [...terminalTabPaneIds(tab)]);
  }

  let previous = openTabPanes();
  // Post-flush: runs after a closing tab's terminals unmounted (and saved their screen).
  // `tabs` (not the active tab's root) so closing a background terminal tab is seen too.
  watch(
    () => [workbench.tabs.map((tab) => tab.id).join("\n"), terminalWorkspace.tabs] as const,
    () => {
      const current = openTabPanes();
      for (const paneId of releasedTerminalPanes(previous, current)) {
        deleteTerminalSnapshot(paneId);
        if (isDesktopRuntime()) void invokeBackend<void>("close_local_shell", { payload: { sessionId: paneId } }).catch(() => undefined);
      }
      previous = current;
    },
    { flush: "post" },
  );
}
