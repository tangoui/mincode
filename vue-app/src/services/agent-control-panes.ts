import { nextTick } from "vue";
import type { Router } from "vue-router";
import { quoteShellArgument } from "@/domain/ai/session-tools";
import type { LocalTerminalPane, TerminalSplitDirection } from "@/domain/terminal/types";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import { useVueI18n } from "@/i18n/messages";
import type { AgentControlPaneInfo, AgentControlPaneRequest } from "@/services/agent-control-executor";
import { invokeAgentControl } from "@/services/agent-control-service";
import { waitForAgentControlTerminal } from "@/services/agent-control-terminal-launch";
import { invokeBackend } from "@/services/desktop-bridge";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";

interface ClaudePreparation {
  enhanced: boolean;
  settingsPath?: string;
  extraEnv?: Record<string, string>;
}

interface SessionPaneHost {
  split(request: AgentControlPaneRequest, direction: TerminalSplitDirection, launch: (pane: LocalTerminalPane) => Promise<AgentControlPaneInfo>): Promise<AgentControlPaneInfo | undefined>;
  close(paneId: string): Promise<boolean>;
}

let sessionPaneHost: SessionPaneHost | undefined;
export function registerAgentControlSessionPanes(host: SessionPaneHost): () => void {
  sessionPaneHost = host;
  return () => { if (sessionPaneHost === host) sessionPaneHost = undefined; };
}

export function createAgentControlPaneOperation(router: Router) {
  const terminal = useTerminalWorkspaceStore();
  const workbench = useWorkbenchStore();
  const preferences = useWorkspaceStatePreferencesStore();
  const { t } = useVueI18n();
  type Tab = typeof workbench.tabs[number];
  function target(tab: Tab) {
    return resolveTerminalLayoutTarget(workbench.tabs, tab, preferences.isolationEnabled, preferences.scopeIdFor(tab.workspaceId));
  }
  function activate(tab: Tab, initial?: { initialCwd?: string }) {
    const { layoutId, fallbackLayoutId } = target(tab);
    workbench.selectTab(tab.id);
    terminal.activateTab(tab.id, initial, layoutId, fallbackLayoutId);
  }

  async function operate(request: AgentControlPaneRequest, signal: AbortSignal): Promise<AgentControlPaneInfo | undefined> {
    const missing = () => new Error(t("settings.agentControl.paneMissing"));
    const paneId = request.pane?.trim();
    const cwd = request.cwd?.trim() || undefined;
    const title = request.title?.trim();
    const command = request.command?.trim();
    const tab = paneId
      ? [workbench.activeTab, ...workbench.tabs].find((item) =>
          item?.view === "terminal" && terminal.paneIdsForLayout(target(item).layoutId).includes(paneId))
      : undefined;
    signal.throwIfAborted();
    if (request.op === "close") {
      if (!paneId) throw missing();
      if (!tab) {
        if (await sessionPaneHost?.close(paneId)) return undefined;
        throw missing();
      }
      activate(tab);
      const terminalTab = terminal.tabs.find((item) => terminalTabContains(item.root, paneId));
      if (!terminalTab) throw missing();
      if (!terminal.closePane(paneId) && !terminal.closeTab(terminalTab.id)) {
        workbench.closeTab(tab.id, true);
      }
      await Promise.all([
        invokeBackend("close_local_shell", { payload: { sessionId: paneId } }),
        invokeBackend("terminal_activity_unregister", { payload: { paneId } }).catch(() => undefined),
      ]);
      return undefined;
    }

    async function launch(pane: LocalTerminalPane) {
      const started = waitForAgentControlTerminal(pane.id, command, signal);
      await started;
      const info = await invokeAgentControl<AgentControlPaneInfo>("terminal_control_info", { pane: pane.id }, { signal });
      return {
        ...info,
        title: title || info.title || command || pane.id,
        cwd: info.cwd || pane.initialCwd || "",
        agent: /^claude(?:\s|$)/.test(command ?? "") ? "claude"
          : /^codex(?:\s|$)/.test(command ?? "") ? "codex" : info.agent,
      } satisfies AgentControlPaneInfo;
    }

    let created: LocalTerminalPane | undefined;
    let createdTab: Tab | undefined;
    let createdInnerTabId: string | undefined;
    if (request.op === "split" && paneId) {
      if (request.direction !== "right" && request.direction !== "down") {
        throw new Error(t("settings.agentControl.paneDirection"));
      }
      const direction = request.direction === "right" ? "horizontal" : "vertical";
      if (!tab) {
        const result = await sessionPaneHost?.split(request, direction, launch);
        if (result) return result;
        throw missing();
      }
      activate(tab);
      const split = terminal.splitPane(paneId, direction, { initialCwd: cwd });
      if (split?.type === "local") {
        created = split;
        terminal.selectPane(created.id);
      }
    } else {
      createdTab = workbench.createTab("terminal", title);
      if (createdTab) {
        const initial = { initialCwd: cwd };
        const { layoutId, fallbackLayoutId } = target(createdTab);
        const shared = terminal.hasLayout(layoutId) || Boolean(fallbackLayoutId && terminal.hasLayout(fallbackLayoutId));
        activate(createdTab, shared ? undefined : initial);
        if (shared) createdInnerTabId = terminal.addLocalTab(initial)?.id;
        if (!shared || createdInnerTabId) {
          const pane = terminal.getPane(terminal.activePaneId);
          if (pane?.type === "local") created = pane;
        }
      }
    }
    if (!created) {
      if (createdTab) workbench.closeTab(createdTab.id, true);
      throw new Error(t("settings.agentControl.paneLimit"));
    }
    try {
      // Register the launch before Vue mounts the pane on the next render.
      const info = launch(created);
      const routed = router.push({ name: "terminal" }).then(() => nextTick());
      const [result] = await Promise.all([info, routed]);
      return result;
    } catch (error) {
      if (createdInnerTabId) terminal.closeTab(createdInnerTabId);
      if (createdTab) workbench.closeTab(createdTab.id, true);
      else terminal.closePane(created.id);
      void invokeBackend("close_local_shell", { payload: { sessionId: created.id } }).catch(() => undefined);
      void invokeBackend("terminal_activity_unregister", { payload: { paneId: created.id } }).catch(() => undefined);
      throw error;
    }
  }

  // Workbench layout activation swaps the mounted panes. Finish launching a
  // new pane before activating another layout for a concurrent control call.
  let pending = Promise.resolve();
  return (request: AgentControlPaneRequest, signal: AbortSignal) => {
    const result = pending.then(() => operate(request, signal));
    pending = result.then(() => undefined, () => undefined);
    return result;
  };
}

function terminalTabContains(node: import("@/domain/terminal/types").TerminalPaneNode, paneId: string): boolean {
  return node.type === "split" ? node.children.some((child) => terminalTabContains(child, paneId)) : node.id === paneId;
}

export async function prepareAgentControlTerminalCommand(paneId: string, command: string | undefined, signal: AbortSignal) {
  if (!command || !/^claude(?:\s|$)/.test(command)) return { command };
  const prepared = await invokeBackend<ClaudePreparation>("terminal_activity_prepare_claude", { payload: { paneId } }, { signal })
    .catch(() => undefined);
  signal.throwIfAborted();
  const settings = prepared?.enhanced && prepared.settingsPath;
  return {
    command: settings ? command.replace(/^claude\b/, `claude --settings ${quoteShellArgument(settings)}`) : command,
    extraEnv: prepared?.extraEnv,
  };
}
