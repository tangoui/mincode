import { nextTick } from "vue";
import { getActivePinia } from "pinia";
import type { Router } from "vue-router";
import { aiCapabilityRegistry } from "@/domain/ai/capability-registry";
import { aiResourceRegistry } from "@/domain/ai/resource-registry";
import { createAiTerminalSessionReuseRegistry } from "@/domain/ai/terminal-session-reuse";
import { aiCloseTargetForPane, isBackgroundTerminalPane, terminalTabForPane } from "@/domain/terminal/terminal-tabs";
import { type LocalTerminalPaneOptions } from "@/domain/terminal/types";
import { normalizeMcpServerForRuntime, type StoredMcpServer } from "@/domain/ai/settings-integrations";
import { useVueI18n } from "@/i18n/messages";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import type { AiCapabilityHostDeps } from "@/services/ai-capability-host";
import { aiResourceSummary } from "@/services/ai-tools/shared";
import type { CreateTerminalTabInput } from "@/services/ai-tools/terminal-tools";
import type { ConfigurableSettingsDependency } from "@/services/ai-tools/settings-tools";
import type { DashboardToolDependencies } from "@/services/ai-tools/dashboard-tools";
import type { UiNavigationDependencies, UiResource } from "@/services/ai-tools/ui-tools";
import { requestAiWorkspaceAction } from "@/services/ai-workspace-actions";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { hydrateSettingsSnapshot, persistSettingsSnapshot, readSettingsSnapshot } from "@/services/settings-persistence";
import { requestOpenSettings } from "@/services/settings-navigation";
import { setAutostart, setMinimizeToTray } from "@/services/settings-service";
import { loadSidebarVisibility, saveSidebarVisibility, sidebarItemIds } from "@/services/sidebar-preferences";
import { connectSftp, disconnectSftp } from "@/services/sftp-service";
import { applyUiTheme, normalizeUiTheme } from "@/services/ui-theme";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useNotesStore } from "@/stores/notes";
import { useNotificationsStore } from "@/stores/notifications";
import { useSftpSessionsStore } from "@/stores/sftp-sessions";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";
import type { WorkspaceTab, WorkspaceView } from "@/types/workspace";

interface McpToolDefinition {
  name: string;
  description?: string;
  inputSchema?: unknown;
}
interface McpConnectResult {
  tools: McpToolDefinition[];
}
interface McpServerStatus {
  id: string;
  state: string;
}

interface AiCapabilitySettings extends Record<string, unknown> {
  activeAiProfileId?: string;
  acpAdapterId?: string;
  acpInjectRules?: boolean;
  mcpServers?: StoredMcpServer[];
  aiSkills?: Array<string | { enabled?: boolean }>;
  aiRules?: Array<{ enabled?: boolean }>;
}

function loadStoredSettings(): AiCapabilitySettings {
  return readSettingsSnapshot<AiCapabilitySettings>();
}

const aiTerminalSessions = createAiTerminalSessionReuseRegistry();

export function clearAiTerminalSessionReuse() {
  aiTerminalSessions.clear();
}

export function stringRecord(value: unknown) {
  if (!value || typeof value !== "object" || Array.isArray(value))
    return {} as Record<string, string>;
  return Object.fromEntries(
    Object.entries(value).filter(
      (entry): entry is [string, string] =>
        typeof entry[0] === "string" && typeof entry[1] === "string"
    )
  );
}

export function enabledMcpServers(projectRoot: string) {
  const stored = loadStoredSettings();
  return (Array.isArray(stored.mcpServers) ? stored.mcpServers : [])
    .filter(
      (server): server is StoredMcpServer =>
        Boolean(server && server.enabled !== false && server.id?.trim())
    )
    .map((server) => normalizeMcpServerForRuntime(server, projectRoot));
}
async function connectSavedMcpServerForAi(requestedServerId: string, projectRoot: string) {
  const serverId = requestedServerId.trim();
  const server = enabledMcpServers(projectRoot).find((candidate) => candidate.id.trim() === serverId);
  if (!server) throw new Error(`Saved enabled connector not found: ${serverId}`);

  const statuses = await invokeBackend<McpServerStatus[]>("mcp_list_status", {}).catch(() => []);
  const connected = statuses.some((status) => status.id === serverId && status.state === "connected");
  const tools = connected
    ? await invokeBackend<McpToolDefinition[]>("mcp_list_tools", { serverId })
    : (await invokeBackend<McpConnectResult>("mcp_connect", {
        serverId,
        transport: server.transport,
        command: server.command,
        args: server.args?.trim() ? server.args.trim().split(/\s+/) : [],
        env: stringRecord(server.env),
        url: server.url,
        headers: stringRecord(server.headers),
      })).tools;
  const [resources, prompts] = await Promise.all([
    invokeBackend<unknown[]>("mcp_list_resources", { serverId }).catch(() => []),
    invokeBackend<unknown[]>("mcp_list_prompts", { serverId }).catch(() => []),
  ]);

  return {
    serverId,
    connected: true,
    toolCount: tools.length,
    resourceCount: resources.length,
    promptCount: prompts.length,
  };
}

/** Uses the application's stores without loading the assistant surface or its history. */
export function createAppAiCapabilityDeps(router: Router): AiCapabilityHostDeps {
  const pinia = getActivePinia();
  const { setLocale, t } = useVueI18n();
  const databaseWorkspace = useDatabaseWorkspaceStore();
  const sftpSessions = useSftpSessionsStore();
  const sshConnections = useSshConnectionsStore();
  const terminalWorkspace = useTerminalWorkspaceStore();
  const workbench = useWorkbenchStore();
  const notes = useNotesStore();
  const notifications = useNotificationsStore();
  const workspacePreferences = useWorkspaceStatePreferencesStore();
  let configurableSettingsRuntime: Promise<ConfigurableSettingsDependency> | undefined;
  const loadConfigurableSettings = () => configurableSettingsRuntime ??=
    import("@/services/settings-tool-runtime").then(({ createSettingsToolRuntime }) => createSettingsToolRuntime(pinia));
  let dashboardToolRuntime: Promise<DashboardToolDependencies> | undefined;
  const loadDashboardTools = () => dashboardToolRuntime ??=
    import("@/services/dashboard-tool-runtime").then(({ createDashboardToolRuntime }) => createDashboardToolRuntime(pinia));

  const workspaceRoot = () => "";

  function nativeResourceList(capability: string) {
    return aiResourceRegistry.list(capability).map(aiResourceSummary);
  }
  async function openWorkspaceViewForAi(input: { view: WorkspaceView }) {
    if (!isDesktopRuntime()) throw new Error("workspace.open_view requires the desktop runtime");
    const tab = workbench.openView(input.view);
    if (!tab && input.view !== "dashboard") return { view: input.view };
    await router.push({ name: input.view });
    await nextTick();
    return {
      view: input.view,
      tabId: tab?.id,
    };
  }

  async function openNavigationView(view: WorkspaceView) {
    const opened = await openWorkspaceViewForAi({ view });
    if (view !== "dashboard" && !opened.tabId) {
      throw new Error(t("ai.navigation.viewUnavailable", { view }));
    }
    return opened;
  }

  function layoutTarget(tab: WorkspaceTab) {
    return resolveTerminalLayoutTarget(workbench.tabs, tab, workspacePreferences.isolationEnabled,
      workspacePreferences.scopeIdFor(tab.workspaceId));
  }

  async function openResource(resource: UiResource, signal: AbortSignal) {
    signal.throwIfAborted();
    if (resource.kind === "terminal") {
      const tab = workbench.workspaceTabs.find((item) => {
        if (item.view !== "terminal") return false;
        const { layoutId, fallbackLayoutId } = layoutTarget(item);
        const paneIds = terminalWorkspace.paneIdsForLayout(terminalWorkspace.hasLayout(layoutId) ? layoutId : fallbackLayoutId ?? layoutId);
        return item.id === resource.id || paneIds.includes(resource.id);
      });
      if (!tab) throw new Error(t("ai.navigation.resourceMissing", { id: resource.id }));
      workbench.selectTab(tab.id);
      const { layoutId, fallbackLayoutId } = layoutTarget(tab);
      terminalWorkspace.activateTab(tab.id, undefined, layoutId, fallbackLayoutId);
      if (tab.id !== resource.id) terminalWorkspace.selectPane(resource.id);
      await router.push(workspaceTabRoute(tab));
      return { view: "terminal", tabId: tab.id, paneId: terminalWorkspace.activePaneId };
    }
    if (resource.kind === "note") {
      await notes.loadTree();
      if (!notes.tree?.notes.some((note) => note.id === resource.id)) {
        throw new Error(t("ai.navigation.resourceMissing", { id: resource.id }));
      }
      signal.throwIfAborted();
      const opened = await openNavigationView("notes");
      notes.selectNote(resource.id);
      return { ...opened, id: resource.id };
    }
    const view = resource.kind === "http_request" ? "http" : resource.kind;
    const opened = await openNavigationView(view);
    const action = { action: view === "ssh" ? "open-profile" : "open-resource", id: resource.id, profileId: resource.id };
    const result = await requestAiWorkspaceAction(view, action, signal);
    return { ...opened, resource: result };
  }

  const activeView = () => workbench.activeTab?.view ?? "dashboard";
  const uiNavigation: UiNavigationDependencies = {
    listTabs: () => ({
      activeView: activeView(),
      tabs: workbench.workspaceTabs.map(({ id, view, title, titleKey }) => ({ id, view, title, titleKey })),
    }),
    getActive: async () => {
      const view = activeView();
      const activeTab = workbench.activeTab;
      const tab = activeTab ? { id: activeTab.id, view, title: activeTab.title } : null;
      let resource: UiResource | null = null;
      if (view === "terminal") {
        resource = { kind: "terminal", id: terminalWorkspace.activePaneId };
      } else if (view === "notes" && notes.selectedNoteId) {
        resource = { kind: "note", id: notes.selectedNoteId };
      } else if (view === "database" || view === "http" || view === "ssh" || view === "sftp") {
        const id = await requestAiWorkspaceAction(view, { action: "active-resource" });
        if (typeof id === "string" && id) resource = { kind: view === "http" ? "http_request" : view, id };
      }
      return { view, tab, resource };
    },
    openView: openNavigationView,
    openSettings: async () => {
      requestOpenSettings();
      await nextTick();
    },
    openResource,
    closeTab: async (id) => {
      if (!(await requestAiWorkspaceAction("workbench", { action: "close-tab", id }))) {
        throw new Error(t("ai.navigation.tabUnavailable", { id }));
      }
    },
    notify: (input) => notifications.push({ ...input, source: "ai" }).id,
  };

  async function createTerminalTabForAi(input: CreateTerminalTabInput) {
    if (!isDesktopRuntime()) throw new Error("terminal.create_session requires the desktop runtime");
    const reusable = !input.forceNew && input.conversationId
      ? aiTerminalSessions.resolve(input.conversationId, ({ tabId, paneId }) =>
          Boolean(
            workbench.tabs.some((tab) => tab.id === tabId && tab.view === "terminal")
            && terminalWorkspace.localPaneIds().includes(paneId)
          )
        )
      : undefined;
    if (reusable) {
      workbench.selectTab(reusable.tabId);
      terminalWorkspace.activateTab(reusable.tabId);
      terminalWorkspace.selectPane(reusable.paneId);
      await router.push({ name: "terminal" });
      return { ...reusable, reused: true };
    }

    const tab = workbench.createTab("terminal", input.title);
    if (!tab) throw new Error("Failed to create terminal Tab");
    const options: LocalTerminalPaneOptions = {
      shellType: input.shellType,
      initialCwd: input.initialCwd,
    };
    terminalWorkspace.activateTab(tab.id, options);
    const paneId = terminalWorkspace.activePaneId;
    await router.push({ name: "terminal" });
    if (input.conversationId) {
      aiTerminalSessions.remember(input.conversationId, { tabId: tab.id, paneId });
    }
    return { tabId: tab.id, paneId, reused: false };
  }
  /**
   * Closes the pane's split, or its whole terminal tab when it is that tab's only
   * pane; the last pane of the workspace stays. Ends each closed pane's shell once
   * here — tab cleanup may close it again, which is harmless but not relied on.
   */
  async function closeTerminalPaneForAi(sessionId: string) {
    if (!isDesktopRuntime()) return false;
    const tabsState = { tabs: terminalWorkspace.tabs, activeTabId: terminalWorkspace.activeTabId };
    const target = aiCloseTargetForPane(tabsState, sessionId);
    let closed = false;
    if (target === "pane") {
      await invokeBackend<void>("close_local_shell", { payload: { sessionId } });
      closed = terminalWorkspace.closePane(sessionId);
    } else if (target === "tab") {
      const tab = terminalTabForPane(tabsState, sessionId);
      const paneIds = tab ? terminalWorkspace.closeTab(tab.id) : null;
      if (paneIds) {
        await Promise.all(paneIds.map((paneId) =>
          invokeBackend<void>("close_local_shell", { payload: { sessionId: paneId } }).catch(() => undefined)
        ));
        closed = true;
      }
    }
    return closed;
  }
  /** AI 输入或调整后台终端标签里的分屏前，先切换到该标签（与会话复用一致）。 */
  function revealTerminalPaneForAi(sessionId: string) {
    const tabsState = { tabs: terminalWorkspace.tabs, activeTabId: terminalWorkspace.activeTabId };
    if (isBackgroundTerminalPane(tabsState, sessionId)) terminalWorkspace.selectPane(sessionId);
  }
  async function connectSftpForAi(profileId: string) {
    if (!isDesktopRuntime()) throw new Error("sftp.connect requires the desktop runtime");
    const profile = sshConnections.findProfile(profileId);
    if (!profile) throw new Error("SFTP profile not found");
    const sessionId = `sftp-${profileId}`;
    const home = await connectSftp(sessionId, profile, sshConnections.credentialsFor(profileId));
    sftpSessions.connected({ id: sessionId, name: `${profile.name.trim()} SFTP`, profileId });
    return { sessionId, home };
  }
  async function disconnectSftpForAi(sessionId: string) {
    if (!isDesktopRuntime()) throw new Error("sftp.disconnect requires the desktop runtime");
    await disconnectSftp(sessionId);
    sftpSessions.disconnected(sessionId);
  }
  return {
    nativeRuntime: isDesktopRuntime(),
    capabilities: aiCapabilityRegistry,
    registry: aiResourceRegistry,
    invoke: invokeBackend,
    uiNavigation: isDesktopRuntime() ? uiNavigation : undefined,
    dashboard: isDesktopRuntime() ? {
      overview: async () => (await loadDashboardTools()).overview(),
      widgets: async () => (await loadDashboardTools()).widgets(),
      widgetData: async (id, signal) => (await loadDashboardTools()).widgetData(id, signal),
    } : undefined,
    listResources: nativeResourceList,
    createTerminalTab: isDesktopRuntime() ? createTerminalTabForAi : undefined,
    closeTerminalPane: isDesktopRuntime() ? closeTerminalPaneForAi : undefined,
    revealTerminalPane: revealTerminalPaneForAi,
    connectSftp: isDesktopRuntime() ? connectSftpForAi : undefined,
    disconnectSftp: isDesktopRuntime() ? disconnectSftpForAi : undefined,
    workspaceRoot,
    openWorkspaceView: isDesktopRuntime() ? openWorkspaceViewForAi : undefined,
    httpAction: isDesktopRuntime() ? async (input) => {
      await openWorkspaceViewForAi({ view: "http" });
      return requestAiWorkspaceAction("http", input);
    } : undefined,
    databaseAction: isDesktopRuntime() ? async (input, signal) => {
      signal?.throwIfAborted();
      await openWorkspaceViewForAi({ view: "database" });
      return requestAiWorkspaceAction("database", input, signal);
    } : undefined,
    redisAction: isDesktopRuntime() ? async (input) => {
      await openWorkspaceViewForAi({ view: "database" });
      return requestAiWorkspaceAction("database", {
        ...input,
        action: input.action === "format-json" ? "format-redis-json" : "open-redis-key",
      });
    } : undefined,
    databaseSnapshot: () => ({
      connections: databaseWorkspace.connections,
      sessions: databaseWorkspace.sessionIds,
      schemas: databaseWorkspace.schemas,
      activeConnectionId: databaseWorkspace.activeConnectionId,
    }),
    connectDatabase: (connectionId) => databaseWorkspace.connect(connectionId),
    disconnectDatabase: (connectionId) => databaseWorkspace.disconnect(connectionId),
    stageWorkspaceEdit: async (edit) => {
      const { useAiPendingEditsStore } = await import("@/stores/ai-pending-edits");
      const pendingEdits = useAiPendingEditsStore(pinia);
      await pendingEdits.hydrate();
      return pendingEdits.stage(edit);
    },
    sshProfile: (profileId) => sshConnections.findProfile(profileId),
    sshCredentials: (profileId) => sshConnections.credentialsFor(profileId),
    settingsSummary: () => {
      const stored = loadStoredSettings();
      return {
        activeAiProfileId: stored.activeAiProfileId,
        acpAdapterId: stored.acpAdapterId,
        acpInjectRules: stored.acpInjectRules,
        enabledMcpServers: stored.mcpServers?.filter((server) => server.enabled).length ?? 0,
        enabledAiSkills: stored.aiSkills?.filter((skill) =>
          typeof skill === "string" || skill.enabled !== false
        ).length ?? 0,
        enabledAiRules: stored.aiRules?.filter((rule) => rule.enabled !== false).length ?? 0,
      };
    },
    configurableSettings: isDesktopRuntime() ? {
      read: async (key) => (await loadConfigurableSettings()).read(key),
      write: async (key, value) => (await loadConfigurableSettings()).write(key, value),
    } : undefined,
    listSavedMcpServers: async () => {
      const statuses = await invokeBackend<McpServerStatus[]>("mcp_list_status", {}).catch(() => []);
      const connected = new Set(
        statuses.filter((status) => status.state === "connected").map((status) => status.id)
      );
      return enabledMcpServers(workspaceRoot()).map((server) => ({
        id: server.id.trim(),
        connected: connected.has(server.id.trim()),
      }));
    },
    connectSavedMcpServer: isDesktopRuntime() ? (serverId) => connectSavedMcpServerForAi(serverId, workspaceRoot()) : undefined,
    updateSafeSettings: isDesktopRuntime() ? async (update) => {
      const stored = await hydrateSettingsSnapshot();
      const next = { ...(stored ?? {}) };
      if (update.theme !== undefined) {
        const theme = normalizeUiTheme(update.theme);
        next.uiTheme = theme;
        applyUiTheme(theme);
      }
      if (update.locale !== undefined) {
        next.locale = update.locale;
        setLocale(update.locale);
      }
      if (update.minimizeToTray !== undefined) {
        next.minimizeToTray = update.minimizeToTray;
        await setMinimizeToTray(update.minimizeToTray);
      }
      if (update.autostart !== undefined) {
        next.autostart = update.autostart;
        await setAutostart(update.autostart);
      }
      await persistSettingsSnapshot(next);
      return update;
    } : undefined,
    sidebarVisibility: loadSidebarVisibility,
    setSidebarItemVisible: async (id, visible) => {
      if (!sidebarItemIds.includes(id)) throw new Error(`Unknown sidebar item: ${id}`);
      const current = await loadSidebarVisibility();
      await saveSidebarVisibility({ ...current, [id]: visible });
      return { itemId: id, visible };
    },
  };
}
