import { nextTick, watch } from "vue";
import type { Router } from "vue-router";
import { strongestAttention } from "@/domain/terminal/terminal-attention";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { useTerminalAttentionStore } from "@/stores/terminal-attention";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";
import type { WorkspaceTab } from "@/types/workspace";

/** Pane IDs shown by one terminal tab (its layout may be shared within a workspace). */
export function useTerminalTabPanes() {
  const workbench = useWorkbenchStore();
  const preferences = useWorkspaceStatePreferencesStore();
  const terminalWorkspace = useTerminalWorkspaceStore();
  return function terminalTabPaneIds(tab: WorkspaceTab): readonly string[] {
    if (tab.view !== "terminal") return [];
    const target = resolveTerminalLayoutTarget(
      workbench.tabs,
      tab,
      preferences.isolationEnabled,
      preferences.scopeIdFor(tab.workspaceId),
    );
    return terminalWorkspace.paneIdsForLayout(target.layoutId);
  };
}

/** The most important attention of a workbench tab (its marker), and clearing it on click. */
export function useTabAttention() {
  const attention = useTerminalAttentionStore();
  const terminalTabPaneIds = useTerminalTabPanes();
  function panes(tab: WorkspaceTab): Iterable<string> {
    if (tab.view === "terminal") return terminalTabPaneIds(tab);
    if (tab.view === "sessions") return attention.sessionPanes.keys();
    return [];
  }
  return {
    tabAttention(tab: WorkspaceTab) {
      return attention.count ? strongestAttention(attention.items, panes(tab)) : undefined;
    },
    dismissTabAttention(tab: WorkspaceTab) {
      if (attention.count) attention.dismiss(panes(tab));
    },
  };
}

/**
 * Main-window wiring: listens for terminal attention, tells the store which panes
 * exist and which are on screen, and focuses a pane from a system notification.
 */
export function useTerminalAttention(router: Router) {
  const attention = useTerminalAttentionStore();
  const workbench = useWorkbenchStore();
  const terminalWorkspace = useTerminalWorkspaceStore();
  const terminalTabPaneIds = useTerminalTabPanes();
  const { t } = useVueI18n();

  function terminalTabFor(paneId: string) {
    return workbench.tabs.find((tab) => tab.view === "terminal" && terminalTabPaneIds(tab).includes(paneId));
  }

  function tabTitle(tab: WorkspaceTab) {
    return tab.title || t(tab.titleKey as MessageKey);
  }

  attention.configure({
    // A remembered layout can outlive its tab, so ownership goes through the open tabs.
    owns: (paneId) => attention.sessionPanes.has(paneId) || terminalTabFor(paneId) !== undefined,
    describe(paneId) {
      const sessionLabel = attention.sessionPanes.get(paneId);
      if (sessionLabel !== undefined) return sessionLabel || t("nav.sessions");
      const tab = terminalTabFor(paneId);
      return tab ? tabTitle(tab) : "";
    },
  });
  attention.ensureListening();

  // The terminal workspace shows the active terminal tab of the active layout while a
  // terminal tab is active; panes of background terminal tabs still raise attention.
  watch(
    () => (workbench.activeTab?.view === "terminal" ? terminalWorkspace.paneIds : []),
    (paneIds) => attention.setVisiblePanes("terminal", paneIds),
    { immediate: true },
  );
  // Closing workbench tabs, terminal tabs or panes (in any terminal tab) drops their attention.
  watch(
    () => [workbench.tabs.length, terminalWorkspace.tabs] as const,
    () => attention.prune(),
  );

  async function openTab(tab: WorkspaceTab) {
    workbench.selectTab(tab.id);
    await router.push(workspaceTabRoute(tab)).catch(() => undefined);
  }

  /** Opens the tab holding a pane (system notification click). */
  async function focusPane(paneId: string) {
    if (attention.sessionPanes.has(paneId)) {
      const tab = workbench.tabs.find((candidate) => candidate.view === "sessions" && candidate.workspaceId === workbench.activeWorkspaceId)
        ?? workbench.tabs.find((candidate) => candidate.view === "sessions");
      if (tab) await openTab(tab);
      attention.requestFocus(paneId);
      return;
    }
    const tab = terminalTabFor(paneId);
    if (!tab) return;
    await openTab(tab);
    await nextTick();
    terminalWorkspace.selectPane(paneId);
  }

  return { focusPane };
}
