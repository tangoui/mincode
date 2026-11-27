import type { Pinia } from "pinia";
import { dashboardQuickActions, orderDashboardTabs } from "@/domain/dashboard/overview";
import { flattenCollectionRequests } from "@/domain/http/request-collection";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useHttpResourcesStore } from "@/stores/http-resources";
import { useSftpSessionsStore } from "@/stores/sftp-sessions";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useWorkbenchStore } from "@/stores/workbench";
import type { DashboardToolDependencies } from "./ai-tools/dashboard-tools";
import { sanitizeAiResult } from "./ai-tools/shared";
import { loadRecentDashboardActivity } from "./dashboard-overview-service";
import { getNoteTree } from "./notes-service";

export function createDashboardToolRuntime(pinia: Pinia | undefined): DashboardToolDependencies {
  const { t } = useVueI18n();
  const workbench = useWorkbenchStore(pinia);
  const database = useDatabaseWorkspaceStore(pinia);
  const ssh = useSshConnectionsStore(pinia);
  const sftp = useSftpSessionsStore(pinia);
  const http = useHttpResourcesStore(pinia);

  async function connectionsData() {
    await Promise.all([database.initialize(), ssh.hydrate()]);
    return {
      ssh: { count: ssh.profiles.length, favorites: ssh.sortedProfiles.filter((item) => item.favorite)
        .map(({ id, name }) => ({ id, name })) },
      database: { count: database.connections.length, connected: database.connectedConnections.length,
        favorites: database.connections.filter((item) => item.favorite).map(({ id, name }) => ({ id, name })) },
    };
  }

  function quickActions() {
    return dashboardQuickActions.map(({ view, titleKey, descriptionKey }) => ({
      view, title: t(titleKey), description: t(descriptionKey),
    }));
  }

  function activeSessions() {
    return orderDashboardTabs(workbench.workspaceTabs).slice(0, 6).map(({ id, view, title, titleKey, pinned }) => ({
      id, view, title: title?.trim() || t(titleKey as MessageKey), pinned: !!pinned,
    }));
  }

  const nativeWidgets = {
    quick_actions: { titleKey: "dashboard.quick.title", data: quickActions },
    active_sessions: { titleKey: "dashboard.active.title", data: activeSessions },
    recent_sessions: { titleKey: "dashboard.sessions.title", data: loadRecentDashboardActivity },
    connections: { titleKey: "dashboard.connections.title", data: connectionsData },
  } satisfies Record<string, { titleKey: MessageKey; data(): unknown }>;

  return {
    async overview() {
      const [connections, recentSessions, notes] = await Promise.all([
        connectionsData(), loadRecentDashboardActivity(), getNoteTree(), http.hydrate(),
      ]);
      return sanitizeAiResult({
        workspace: { id: workbench.activeWorkspaceId, name: workbench.activeWorkspace?.name },
        activeSessions: activeSessions(),
        recentSessions,
        connections,
        quickActions: quickActions(),
        counts: {
          ssh: connections.ssh.count,
          database: connections.database.count,
          connectedDatabases: connections.database.connected,
          openTabs: workbench.workspaceTabs.length,
          terminals: workbench.workspaceTabs.filter((tab) => tab.view === "terminal").length,
          sftpSessions: sftp.sessions.length,
          notes: notes.notes.length,
          httpRequests: http.collections.reduce((count, collection) => count + flattenCollectionRequests(collection).length, 0),
        },
      });
    },
    async widgets() {
      return Object.entries(nativeWidgets).map(([id, widget]) => ({
        id, title: t(widget.titleKey), kind: "data", visible: true,
      }));
    },
    async widgetData(id, signal) {
      signal.throwIfAborted();
      if (Object.prototype.hasOwnProperty.call(nativeWidgets, id)) {
        return sanitizeAiResult(await nativeWidgets[id as keyof typeof nativeWidgets].data());
      }
      throw new Error(t("dashboard.tools.widgetMissing", { id }));
    },
  };
}
