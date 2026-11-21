/**
 * Workbench tab and view commands, registered at startup by AppTabBar (the
 * owner of tab navigation) in the main window:
 * `workbench:close-tab`, `close-all`, `reopen-closed`, `next-tab`, `prev-tab`,
 * `tab-1`…`tab-9`, `new-terminal` and `show-view.{source-control,terminal,plugins}`.
 *
 * Tab commands act on the nested document tabs of the active editor host
 * (services/document-tab-host.ts) while the focus is inside its editor area,
 * otherwise on the workbench tabs of the active workspace.
 */
import { onBeforeUnmount } from "vue";
import { ElMessage } from "element-plus";
import { useRouter } from "vue-router";
import {
  adjacentTabId,
  closeTargetKind,
  tabIdAtIndex,
  type EditorAreaFocus,
} from "@/domain/workbench/tab-navigation";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { recordClosedTabs, takeClosedEntry } from "@/services/closed-tab-history";
import { registerCommand, type RegisteredCommand } from "@/services/command-registry";
import { activeDocumentTabHost, type DocumentTabHost } from "@/services/document-tab-host";
import { sidebarItemRoute, sidebarItemView, sidebarMainItems } from "@/services/sidebar-items";
import { useWorkbenchStore } from "@/stores/workbench";
import type { WorkspaceTab } from "@/types/workspace";

export interface WorkbenchTabCommandOptions {
  /** Selects a workbench tab and routes to it. */
  selectTab(id: string): void;
  /** Closes a workbench tab exactly like its close button. */
  closeTab(id: string): Promise<void>;
  /** Routes to `tab` (the dashboard when undefined). */
  navigateTo(tab: WorkspaceTab | undefined): void;
  /** Opens a new local terminal tab (optionally with a title). */
  newTerminal(title?: string): void;
}

function editorAreaFocus(host: DocumentTabHost): EditorAreaFocus {
  const active = typeof document === "undefined" ? null : document.activeElement;
  if (!active || active === document.body || active === document.documentElement) return "none";
  return host.contains(active) ? "editor-area" : "elsewhere";
}

/** The document host when tab commands should act on its tabs. */
function documentTarget(): DocumentTabHost | undefined {
  const host = activeDocumentTabHost();
  if (!host) return undefined;
  const kind = closeTargetKind({
    hasDocumentHost: true,
    focus: editorAreaFocus(host),
    documentTabCount: host.tabIds().length,
  });
  return kind === "document" ? host : undefined;
}

const SHOW_VIEW_COMMANDS = [
  { id: "workbench:show-view.terminal", item: "terminal" },
  { id: "workbench:show-view.plugins", item: "plugins" },
] as const;

export function useWorkbenchTabCommands(options: WorkbenchTabCommandOptions): void {
  const { t } = useVueI18n();
  const router = useRouter();
  const workbench = useWorkbenchStore();

  const workbenchTabIds = () => workbench.workspaceTabs.map((tab) => tab.id);

  function selectRelative(delta: number) {
    const host = documentTarget();
    if (host) {
      const id = adjacentTabId(host.tabIds(), host.activeTabId(), delta);
      if (id) host.selectTab(id);
      return;
    }
    const id = adjacentTabId(workbenchTabIds(), workbench.activeTabId, delta);
    if (id) options.selectTab(id);
  }

  function selectIndex(n: number) {
    const host = documentTarget();
    if (host) {
      const id = tabIdAtIndex(host.tabIds(), n);
      if (id) host.selectTab(id);
      return;
    }
    const id = tabIdAtIndex(workbenchTabIds(), n);
    if (id) options.selectTab(id);
  }

  async function closeActive() {
    const host = documentTarget();
    if (host) {
      const id = host.activeTabId();
      if (id) await host.closeTab(id);
      return;
    }
    const id = workbench.activeTabId;
    if (id) await options.closeTab(id);
  }

  async function closeAll() {
    const host = documentTarget();
    if (host) {
      await host.closeAll();
      return;
    }
    const closing = workbench.workspaceTabs.filter(
      (tab) => !tab.pinned && !workbench.poppedOutTabLabels[tab.id],
    );
    if (!closing.length) return;
    recordClosedTabs(closing);
    for (const tab of closing) workbench.closeTab(tab.id);
    options.navigateTo(workbench.activeTab);
  }

  async function reopenClosed() {
    const entry = takeClosedEntry(workbench.activeWorkspaceId);
    if (!entry) {
      ElMessage.info(t("keybindings.workbench.nothingToReopen"));
      return;
    }
    switch (entry.kind) {
      case "view": {
        const tab = workbench.openView(entry.view);
        if (tab) options.navigateTo(tab);
        return;
      }
      case "terminal":
        options.newTerminal(entry.title);
        return;
      case "plugin-view": {
        const tab = workbench.openPluginView(entry.pluginId, entry.viewId, entry.title);
        if (tab) options.navigateTo(tab);
        return;
      }
    }
  }

  function showView(itemId: (typeof SHOW_VIEW_COMMANDS)[number]["item"]) {
    const item = sidebarMainItems.find((candidate) => candidate.id === itemId);
    if (!item) return;
    if (!workbench.openView(sidebarItemView(item))) return;
    void router.push(sidebarItemRoute(item));
  }

  const command = (
    id: string,
    execute: RegisteredCommand["execute"],
    extra: Partial<RegisteredCommand> = {},
  ): (() => void) =>
    registerCommand({
      id,
      source: "builtin",
      title: () => t(`keybindings.command.${id}` as MessageKey),
      subtitle: () => t("commandPalette.source.builtin"),
      execute,
      ...extra,
    });

  const disposers = [
    command("workbench:close-tab", closeActive, { keywords: ["close", "关闭"] }),
    command("workbench:close-all", closeAll, { keywords: ["close", "关闭"] }),
    command("workbench:reopen-closed", reopenClosed, { keywords: ["reopen", "undo close", "重新打开"] }),
    command("workbench:next-tab", () => selectRelative(1), { showInPalette: false }),
    command("workbench:prev-tab", () => selectRelative(-1), { showInPalette: false }),
    ...Array.from({ length: 9 }, (_, index) =>
      command(`workbench:tab-${index + 1}`, () => selectIndex(index + 1), { showInPalette: false }),
    ),
    command("workbench:new-terminal", () => options.newTerminal(), { keywords: ["terminal", "shell", "终端"] }),
    // The palette already lists these views as navigation entries.
    ...SHOW_VIEW_COMMANDS.map(({ id, item }) => command(id, () => showView(item), { showInPalette: false })),
  ];

  onBeforeUnmount(() => {
    for (const dispose of disposers) dispose();
  });
}
