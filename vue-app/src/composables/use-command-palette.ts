import { computed, onBeforeUnmount, ref } from "vue";
import type { Router } from "vue-router";
import { ElMessage } from "element-plus";
import {
  filterCommandPaletteItems,
  type CommandPaletteAction,
  type CommandPaletteItem,
  type CommandPaletteOpenRequest,
} from "@/domain/commands/command-palette";
import type { PluginContextValues } from "@/domain/commands/when";
import {
  restoreHttpCollections,
  type HttpRequestCollection,
  type SavedHttpRequest,
} from "@/domain/http/request-collection";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { requestAiWorkspaceAction } from "@/services/ai-workspace-actions";
import {
  executeRegisteredCommand,
  useRegisteredCommands,
} from "@/services/command-registry";
import { loadHttpState } from "@/services/http-state-persistence";
import { currentKeybindingContext } from "@/services/keybinding-dispatcher";
import { sidebarItemView, sidebarMainItems } from "@/services/sidebar-items";
import { useAiAssistantShellStore } from "@/stores/ai-assistant-shell";
import { useCommandPaletteStore } from "@/stores/command-palette";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useKeybindingsStore } from "@/stores/keybindings";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useWorkbenchStore } from "@/stores/workbench";
import type { WorkspaceView } from "@/types/workspace";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";

interface UseCommandPaletteOptions {
  router: Router;
  openSettings: () => void;
}

interface StoredHttpState {
  collections?: unknown;
}

function collectHttpRequests(
  collections: readonly HttpRequestCollection[],
): Array<{ collection: HttpRequestCollection; request: SavedHttpRequest }> {
  return collections.flatMap((collection) => [
    ...collection.requests.map((request) => ({ collection, request })),
    ...collectHttpRequests(collection.children ?? []),
  ]);
}

export function useCommandPalette(options: UseCommandPaletteOptions) {
  const { t } = useVueI18n();
  const workbench = useWorkbenchStore();
  const workspaceStateScope = useWorkspaceStateScope();
  const sshConnections = useSshConnectionsStore();
  const database = useDatabaseWorkspaceStore();
  const assistant = useAiAssistantShellStore();
  const commandPalette = useCommandPaletteStore();
  const keybindings = useKeybindingsStore();
  // Keybinding context captured when the palette opens (before focus moves into
  // it) so `when`-gated commands are listed only where they could run.
  const openContext = ref<PluginContextValues | undefined>();
  const registeredCommands = useRegisteredCommands(() => openContext.value);

  const open = ref(false);
  const query = ref("");
  const resourceLoading = ref(false);
  const loading = resourceLoading;
  const executingId = ref("");
  const httpCollections = ref<HttpRequestCollection[]>([]);
  let loadSequence = 0;

  function withUsage(item: CommandPaletteItem): CommandPaletteItem {
    const usage = commandPalette.usage[item.id];
    return usage
      ? { ...item, usageCount: usage.count, lastUsedAt: usage.lastUsedAt }
      : item;
  }

  const items = computed<CommandPaletteItem[]>(() => {
    const commandItems: CommandPaletteItem[] = registeredCommands.value.map(
      (command, index) => ({
        id: `command:${command.id}`,
        kind: "command",
        title: command.title,
        subtitle:
          command.subtitle ??
          t(`commandPalette.source.${command.source}` as MessageKey),
        keywords: [...(command.keywords ?? []), command.source],
        priority: command.priority ?? 5 + index,
        keybinding: keybindings.label(command.id) || undefined,
        action: { type: "registered-command", commandId: command.id },
      }),
    );

    const viewItems: CommandPaletteItem[] = sidebarMainItems.map(
      (item, index) => ({
        id: `view:${item.id}`,
        kind: "workspace",
        title: t(item.labelKey),
        subtitle: t("commandPalette.workspaceSection"),
        keywords: [item.id],
        priority: 10 + index,
        action: {
          type: "open-view",
          view: sidebarItemView(item),
        },
      }),
    );

    const workspaceItems: CommandPaletteItem[] = workbench.workspaces.map(
      (workspace, index) => ({
        id: `workspace:${workspace.id}`,
        kind: "workspace",
        title: workspace.name,
        subtitle: t("commandPalette.switchWorkspace"),
        priority: 30 + index,
        action: { type: "switch-workspace", workspaceId: workspace.id },
      }),
    );

    const tabItems: CommandPaletteItem[] = workbench.tabs.map((tab, index) => {
      const workspace = workbench.workspaces.find(
        (item) => item.id === tab.workspaceId,
      );
      return {
        id: `tab:${tab.id}`,
        kind: "tab",
        title: tab.title || t(tab.titleKey as MessageKey),
        subtitle: workspace?.name ?? t("commandPalette.tabSection"),
        keywords: [tab.view, workspace?.name ?? ""],
        priority: 20 + index,
        action: { type: "select-tab", tabId: tab.id },
      };
    });

    const sshItems: CommandPaletteItem[] = sshConnections.sortedProfiles.map(
      (profile, index) => ({
        id: `ssh:${profile.id}`,
        kind: "ssh",
        title: profile.name,
        subtitle: t("commandPalette.sshSubtitle", {
          user: profile.username,
          host: profile.host,
          port: profile.port,
        }),
        keywords: [
          profile.host,
          profile.username,
          profile.group ?? "",
          ...profile.tags,
        ],
        priority: 60 + index,
        action: { type: "open-ssh", profileId: profile.id },
      }),
    );

    const databaseItems: CommandPaletteItem[] = database.connections.map(
      (connection, index) => ({
        id: `database:${connection.id}`,
        kind: "database",
        title: connection.name,
        subtitle: t("commandPalette.databaseSubtitle", {
          type: connection.dbType,
        }),
        keywords: [
          connection.dbType,
          connection.host,
          connection.database ?? "",
          connection.group ?? "",
        ],
        priority: 70 + index,
        action: { type: "open-database", connectionId: connection.id },
      }),
    );

    const httpItems: CommandPaletteItem[] = collectHttpRequests(
      httpCollections.value,
    ).map(({ collection, request }, index) => ({
      id: `http:${collection.id}:${request.id}`,
      kind: "http",
      title: request.name,
      subtitle: `${request.method} · ${collection.name}`,
      keywords: [request.method, collection.name],
      priority: 80 + index,
      action: {
        type: "open-http-request",
        collectionId: collection.id,
        requestId: request.id,
      },
    }));

    return [
      ...commandItems,
      ...viewItems,
      ...workspaceItems,
      ...tabItems,
      ...sshItems,
      ...databaseItems,
      ...httpItems,
    ].map(withUsage);
  });

  const results = computed(() => {
    return filterCommandPaletteItems(items.value, query.value);
  });

  async function refreshResources() {
    const sequence = ++loadSequence;
    resourceLoading.value = true;
    try {
      await workspaceStateScope.hydrate();
      const workspaceScopeId = workspaceStateScope.capture().scopeId;
      const [, , httpState] = await Promise.all([
        sshConnections.hydrate(),
        database.initialize(),
        loadHttpState<StoredHttpState>(workspaceScopeId),
      ]);
      if (sequence !== loadSequence) return;
      httpCollections.value = restoreHttpCollections(httpState?.collections);

    } catch {
      ElMessage.error(t("commandPalette.loadFailed"));
    } finally {
      if (sequence === loadSequence) resourceLoading.value = false;
    }
  }

  function openPalette(request: Omit<CommandPaletteOpenRequest, "seq"> = {}) {
    openContext.value = currentKeybindingContext();
    query.value = request.prefix ?? "";
    open.value = true;
    void refreshResources();
  }

  function closePalette() {
    open.value = false;
    query.value = "";
    executingId.value = "";
  }

  async function openView(view: WorkspaceView, query?: import("vue-router").LocationQueryRaw) {
    if (!workbench.openView(view) && view !== "dashboard") return;
    await options.router.push({ name: view, ...(query ? { query } : {}) });
  }

  async function executeAction(action: CommandPaletteAction): Promise<boolean | void> {
    switch (action.type) {
      case "registered-command":
        await executeRegisteredCommand(action.commandId);
        return;
      case "open-view":
        await openView(action.view, action.query);
        return;
      case "select-tab": {
        const tab = workbench.selectTab(action.tabId);
        if (tab) await options.router.push(workspaceTabRoute(tab));
        return;
      }
      case "switch-workspace": {
        const tab = workbench.switchWorkspace(action.workspaceId);
        await options.router.push(workspaceTabRoute(tab));
        return;
      }
      case "open-settings":
        options.openSettings();
        return;
      case "open-ai":
        void assistant.show("agentic");
        return;
      case "open-ssh":
        await openView("ssh");
        await requestAiWorkspaceAction("ssh", {
          action: "open-profile",
          profileId: action.profileId,
        });
        return;
      case "open-database":
        await openView("database");
        await requestAiWorkspaceAction("database", {
          action: "open-connection",
          connectionId: action.connectionId,
        });
        return;
      case "open-http-request":
        await openView("http");
        await requestAiWorkspaceAction("http", {
          action: "open-saved-request",
          collectionId: action.collectionId,
          requestId: action.requestId,
        });
        return;
    }
  }

  async function execute(item: CommandPaletteItem) {
    if (executingId.value) return;
    executingId.value = item.id;
    try {
      if ((await executeAction(item.action)) === false) return;
      commandPalette.recordUsage(item.id);
      closePalette();
    } catch {
      ElMessage.error(t("commandPalette.actionFailed", { name: item.title }));
    } finally {
      executingId.value = "";
    }
  }

  onBeforeUnmount(() => {
  });

  return {
    open,
    query,
    loading,
    executingId,
    results,
    openPalette,
    closePalette,
    execute,
  };
}
