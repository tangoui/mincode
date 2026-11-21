import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import type { Router } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  filterCommandPaletteItems,
  pluginServicePaletteItems,
  withoutStalePluginFileResults,
  type CommandPaletteAction,
  type CommandPaletteItem,
  type CommandPaletteOpenRequest,
} from "@/domain/commands/command-palette";
import type { PluginContextValues } from "@/domain/commands/when";
import {
  SEARCH_PROVIDER_LIMITS,
  normalizeSearchResultURL,
  parseProviderQuery,
  providerQueryReady,
  searchProviderTitle,
  searchResultURLHost,
  type PluginSearchProvider,
  type SearchFileScope,
  type SearchProviderRuntime,
  type SearchResultItem,
} from "@/domain/plugins/search-providers";
import {
  paletteServiceInput,
  serviceAccepts,
  servicesForPalette,
  utf8ByteLength,
} from "@/domain/plugins/services";
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
import { pluginTabTitle } from "@/services/plugin-titles";
import { sidebarItemView, sidebarMainItems } from "@/services/sidebar-items";
import {
  chooseNativeFile,
  chooseNativeFiles,
  nativeErrorMessage,
} from "@/services/desktop-bridge";
import { pluginServices, runPluginService } from "@/services/plugin-services";
import {
  executePluginCommandWithArgs,
  refreshPluginCommands,
} from "@/services/plugin-commands";
import { searchWithPlugin } from "@/services/plugin-search-service";
import {
  currentPluginWorkspaceScope,
  onPluginWorkspaceScopeChange,
} from "@/services/plugin-workspace-scope";
import { openPluginViewWithRequest } from "@/services/plugin-view-open-requests";
import { openSystemBrowser } from "@/services/web-automation-service";
import { usePluginSearchProvidersStore } from "@/stores/plugin-search-providers";
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

/** Status of a `<prefix>:<query>` plugin search (contributes.searchProviders). */
export interface PluginSearchStatus {
  state: "too-short" | "searching" | "done" | "empty" | "failed" | "timeout" | "unavailable";
  /** Localized provider title (plugin content, rendered as text). */
  title: string;
  minLength?: number;
  detail?: string;
  dropped?: number;
  truncated?: boolean;
  /** File results cleared because the workspace scope changed after they rendered. */
  staleFiles?: number;
}

type PluginOpenFileAction = Extract<CommandPaletteAction, { type: "plugin-open-file" }>;

/** The host-filled context a file-enabled response carries for its file rows. */
interface PluginFileResultContext {
  fileScope: SearchFileScope;
  providerRuntime: SearchProviderRuntime;
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
  const { t, localeChain } = useVueI18n();
  const workbench = useWorkbenchStore();
  const workspaceStateScope = useWorkspaceStateScope();
  const sshConnections = useSshConnectionsStore();
  const database = useDatabaseWorkspaceStore();
  const assistant = useAiAssistantShellStore();
  const commandPalette = useCommandPaletteStore();
  const keybindings = useKeybindingsStore();
  const pluginSearchProviders = usePluginSearchProvidersStore();
  // Keybinding context captured when the palette opens (before focus moves into
  // it) so `when`-gated commands are listed only where they could run.
  const openContext = ref<PluginContextValues | undefined>();
  const registeredCommands = useRegisteredCommands(() => openContext.value);

  const open = ref(false);
  const query = ref("");
  const resourceLoading = ref(false);
  // Plugin search providers: `<prefix>:<query>`.
  const pluginSearch = ref<PluginSearchStatus | null>(null);
  const pluginResultItems = ref<CommandPaletteItem[]>([]);
  const pluginQuery = computed(() =>
    parseProviderQuery(query.value, pluginSearchProviders.providers),
  );
  const searchPrefixes = computed(() =>
    pluginSearchProviders.providers.map((provider) => ({
      prefix: provider.prefix,
      title: searchProviderTitle(provider, localeChain.value),
    })),
  );
  const loading = computed(() => pluginSearch.value?.state === "searching" || resourceLoading.value);
  const executingId = ref("");
  const httpCollections = ref<HttpRequestCollection[]>([]);
  let loadSequence = 0;
  let pluginSearchTimer: ReturnType<typeof setTimeout> | undefined;
  let pluginSearchController: AbortController | undefined;
  let pluginSearchSequence = 0;
  let pluginFileOpenController: AbortController | undefined;

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
        title: pluginTabTitle(tab) || t(tab.titleKey as MessageKey),
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

    const serviceItems = pluginServicePaletteItems(
      servicesForPalette(pluginServices.value, localeChain.value),
      t("plugins.services.palettePrefix"),
    );

    return [
      ...commandItems,
      ...serviceItems,
      ...viewItems,
      ...workspaceItems,
      ...tabItems,
      ...sshItems,
      ...databaseItems,
      ...httpItems,
    ].map(withUsage);
  });

  const results = computed(() => {
    if (pluginQuery.value) return pluginResultItems.value;
    return filterCommandPaletteItems(items.value, query.value);
  });

  function pluginResultItem(
    provider: PluginSearchProvider,
    title: string,
    item: SearchResultItem,
    index: number,
    fileContext: PluginFileResultContext | undefined,
  ): CommandPaletteItem[] {
    const target = item.target;
    const browserHint =
      target.type === "url"
        ? t("pluginContrib.searchProviders.opensInBrowser", {
            host: searchResultURLHost(target.url),
          })
        : "";
    // A file row needs the host-filled scope and runtime of its response.
    if (target.type === "file" && !fileContext) return [];
    const location =
      target.type === "file"
        ? [target.path, target.line, target.column]
            .filter((part) => part !== undefined)
            .join(":")
        : "";
    const action: CommandPaletteAction | undefined =
      target.type === "file" && fileContext
        ? {
            type: "plugin-open-file",
            pluginId: provider.pluginId,
            providerId: provider.id,
            fileScope: fileContext.fileScope,
            providerRuntime: fileContext.providerRuntime,
            target,
          }
        : target.type === "view"
        ? {
            type: "plugin-open-view",
            pluginId: provider.pluginId,
            view: target.view,
            providerId: provider.id,
            ...(target.args ? { args: target.args } : {}),
          }
        : target.type === "command"
          ? {
              type: "plugin-run-command",
              pluginId: provider.pluginId,
              command: target.command,
              ...(target.args ? { args: target.args } : {}),
            }
          : target.type === "url"
            ? { type: "open-url", url: target.url }
            : undefined;
    if (!action) return [];
    return [{
      id: `plugin-result:${provider.pluginId}:${provider.id}:${item.id}`,
      kind: "plugin-result",
      title: item.title,
      subtitle:
        [location, item.detail ?? "", browserHint].filter(Boolean).join(" · ") ||
        undefined,
      group: t("pluginContrib.searchProviders.group", {
        title,
        plugin: provider.pluginName,
      }),
      priority: index,
      action,
    }];
  }

  /**
   * Removes file rows that no longer belong to the published scope (or all of
   * them, when the host said the scope or runtime changed); view, command and
   * URL rows stay. The status counts what was cleared so the user searches again.
   */
  function clearStalePluginFileResults(all = false) {
    const before = pluginResultItems.value;
    const kept = all
      ? before.filter((item) => item.action.type !== "plugin-open-file")
      : withoutStalePluginFileResults(before, currentPluginWorkspaceScope());
    const removed = before.length - kept.length;
    if (!removed) return;
    pluginResultItems.value = kept;
    if (pluginSearch.value) {
      pluginSearch.value = {
        ...pluginSearch.value,
        staleFiles: (pluginSearch.value.staleFiles ?? 0) + removed,
      };
    }
  }

  function cancelPluginSearch() {
    if (pluginSearchTimer) {
      clearTimeout(pluginSearchTimer);
      pluginSearchTimer = undefined;
    }
    pluginSearchController?.abort();
    pluginSearchController = undefined;
    pluginSearchSequence += 1;
    pluginFileOpenController?.abort();
    pluginFileOpenController = undefined;
  }

  /** Debounced provider query; stale and aborted responses are dropped. */
  function runPluginSearch() {
    cancelPluginSearch();
    pluginResultItems.value = [];
    const parsed = open.value ? pluginQuery.value : undefined;
    if (!parsed) {
      pluginSearch.value = null;
      return;
    }
    const provider = parsed.provider;
    const title = searchProviderTitle(provider, localeChain.value);
    if (!providerQueryReady(parsed)) {
      pluginSearch.value = {
        state: "too-short",
        title,
        minLength: Math.max(1, provider.minQueryLength),
      };
      return;
    }
    pluginSearch.value = { state: "searching", title };
    const sequence = pluginSearchSequence;
    pluginSearchTimer = setTimeout(async () => {
      pluginSearchTimer = undefined;
      const controller = new AbortController();
      pluginSearchController = controller;
      const outcome = await searchWithPlugin(
        provider,
        parsed.query,
        localeChain.value[0] ?? "en",
        controller.signal,
      );
      if (sequence !== pluginSearchSequence || controller.signal.aborted) return;
      pluginSearchController = undefined;
      if (!outcome.ok) {
        const code = outcome.error.code;
        pluginSearch.value =
          code === "timeout"
            ? { state: "timeout", title }
            : code === "unavailable" || code === "not_found"
              ? { state: "unavailable", title }
              : { state: "failed", title, detail: outcome.error.detail.slice(0, 200) };
        return;
      }
      const { items: found, dropped, truncated, fileScope, providerRuntime } =
        outcome.response;
      const fileContext =
        fileScope && providerRuntime ? { fileScope, providerRuntime } : undefined;
      const rendered = found.flatMap((item, index) =>
        pluginResultItem(provider, title, item, index, fileContext),
      );
      // A response that raced a scope switch never shows its file rows.
      const current = withoutStalePluginFileResults(
        rendered,
        currentPluginWorkspaceScope(),
      );
      pluginResultItems.value = current;
      const staleFiles = rendered.length - current.length;
      pluginSearch.value = {
        state: found.length ? "done" : "empty",
        title,
        dropped: dropped + (found.length - rendered.length),
        truncated,
        ...(staleFiles ? { staleFiles } : {}),
      };
    }, SEARCH_PROVIDER_LIMITS.debounceMs);
  }

  watch(
    [open, query, () => pluginSearchProviders.providers, localeChain],
    runPluginSearch,
  );

  // Workspace scope switches (services/plugin-workspace-scope.ts): a query
  // still in flight is cancelled and sent again under the new scope; rendered
  // results lose only their file rows, since an older revision never matches.
  const stopPluginScopeWatch = onPluginWorkspaceScopeChange(() => {
    if (!open.value || !pluginQuery.value) return;
    if (pluginSearch.value?.state === "searching") {
      runPluginSearch();
      return;
    }
    // A click still validating rechecks the scope itself and reports "stale".
    clearStalePluginFileResults();
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

      // Plugin commands are registered at startup (services/plugin-commands.ts);
      // opening the palette only refreshes them.
      await refreshPluginCommands();
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
    cancelPluginSearch();
    open.value = false;
    query.value = "";
    executingId.value = "";
  }

  async function openView(view: WorkspaceView, query?: import("vue-router").LocationQueryRaw) {
    if (!workbench.openView(view) && view !== "dashboard") return;
    await options.router.push({ name: view, ...(query ? { query } : {}) });
  }

  function baseName(path: string) {
    return path.slice(Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\")) + 1);
  }

  /**
   * Runs a palette service (design §11): `none` runs directly, `text` asks in
   * a text box, `file`/`files` in the native file picker filtered by the
   * service's accepts. runPluginService then collects params, picks the
   * provider and shows progress and results.
   */
  async function runPaletteService(service: string) {
    const group = servicesForPalette(pluginServices.value, localeChain.value).find(
      (item) => item.service === service,
    );
    if (!group) return;
    const input = paletteServiceInput(group);
    // Let the palette close first so the prompt takes focus.
    await nextTick();
    if (input.kind === "none") {
      await runPluginService({ service, surface: "palette" });
      return;
    }
    if (input.kind === "text") {
      let text: string;
      try {
        ({ value: text } = await ElMessageBox.prompt(
          t("plugins.services.inputPrompt", { name: group.title }),
          group.title,
          {
            inputType: "textarea",
            inputValue: "",
            inputPlaceholder: t("plugins.services.inputLabel"),
            confirmButtonText: t("plugins.services.confirmRun"),
            cancelButtonText: t("plugins.services.cancel"),
            inputValidator: (value: string) =>
              utf8ByteLength(value) <= input.maxBytes ||
              t("plugins.services.tooLarge"),
          },
        ));
      } catch {
        return; // cancelled
      }
      await runPluginService({ service, surface: "palette", input: text });
      return;
    }
    const title = t("plugins.services.chooseFiles", { name: group.title });
    let paths: string[];
    try {
      if (input.kind === "file") {
        const filters = input.extensions.length
          ? [{ DisplayName: group.title, Pattern: input.extensions.map((extension) => `*${extension}`).join(";") }]
          : [];
        const path = await chooseNativeFile(title, filters);
        paths = path ? [path] : [];
      } else {
        paths = await chooseNativeFiles(title, input.extensions);
      }
    } catch (error) {
      ElMessage.error({
        message: t("plugins.services.chooseFilesFailed", { detail: nativeErrorMessage(error) }),
        grouping: true,
        showClose: true,
      });
      return;
    }
    if (!paths.length) return;
    // Dialog filters are advisory on some platforms; recheck type and count.
    if (
      paths.length > input.maxFiles ||
      !paths.every((path) => serviceAccepts(input.accepts, baseName(path)))
    ) {
      ElMessage.warning({
        message: t("plugins.services.filesRejected", { name: group.title, max: input.maxFiles }),
        grouping: true,
        showClose: true,
      });
      return;
    }
    await runPluginService({ service, surface: "palette", paths });
  }

  /**
   * Opens a plugin file row through the lazily loaded click chain (local scope
   * check, Go re-validation, local check again, open-file with the row's own
   * root). Returns whether it opened; otherwise the palette stays open.
   */
  async function openPluginFileResult(action: PluginOpenFileAction): Promise<boolean> {
    pluginFileOpenController?.abort();
    const controller = new AbortController();
    pluginFileOpenController = controller;
    try {
      const { openPluginSearchFileTarget } = await import(
        "@/services/plugin-search-file-targets"
      );
      const outcome = await openPluginSearchFileTarget(
        {
          pluginId: action.pluginId,
          providerId: action.providerId,
          fileScope: action.fileScope,
          providerRuntime: action.providerRuntime,
          target: action.target,
        },
        async (request) => {
          await executeAction({ type: "open-file", ...request });
        },
        controller.signal,
      );
      switch (outcome.status) {
        case "opened":
          return true;
        case "stale":
          clearStalePluginFileResults(true);
          ElMessage.warning({
            message: t("pluginContrib.searchProviders.fileStale"),
            grouping: true,
          });
          return false;
        case "desktop-required":
          ElMessage.warning({
            message: t("pluginContrib.searchProviders.fileDesktopOnly"),
            grouping: true,
          });
          return false;
        case "failed":
          ElMessage.error({
            message: t("pluginContrib.searchProviders.fileOpenFailed", {
              detail: (outcome.error.detail || outcome.error.code).slice(0, 200),
            }),
            grouping: true,
            showClose: true,
          });
          return false;
        default:
          // "cancelled": a newer click, a scope switch or closing the palette.
          return false;
      }
    } finally {
      if (pluginFileOpenController === controller) pluginFileOpenController = undefined;
    }
  }

  /** Runs a palette action; `false` means it did not complete and the palette stays open. */
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
      case "plugin-open-view":
        await openPluginViewWithRequest(action.pluginId, action.view, {
          source: "search",
          entry: action.providerId,
          ...(action.args ? { params: action.args } : {}),
        });
        return;
      case "plugin-run-command":
        await executePluginCommandWithArgs(action.pluginId, action.command, {
          ...(action.args ? { args: action.args } : {}),
        });
        return;
      case "plugin-service":
        // Prompts follow the palette closing; the run reports its own failures.
        closePalette();
        void runPaletteService(action.service).catch(() => {
          ElMessage.error(t("commandPalette.actionFailed", { name: action.service }));
        });
        return;
      case "open-url": {
        // Only reached from an explicit click or Enter on the result row.
        const url = normalizeSearchResultURL(action.url);
        if (!url) throw new Error("Unsupported URL");
        await openSystemBrowser(url);
        return;
      }
      case "plugin-open-file":
        return openPluginFileResult(action);
    }
  }

  async function execute(item: CommandPaletteItem) {
    if (executingId.value) return;
    executingId.value = item.id;
    try {
      if ((await executeAction(item.action)) === false) return;
      // Plugin search results are transient and never enter the usage history.
      if (item.kind !== "plugin-result") commandPalette.recordUsage(item.id);
      closePalette();
    } catch {
      ElMessage.error(t("commandPalette.actionFailed", { name: item.title }));
    } finally {
      executingId.value = "";
    }
  }

  onBeforeUnmount(() => {
    stopPluginScopeWatch();
    cancelPluginSearch();
  });

  return {
    open,
    query,
    loading,
    executingId,
    results,
    pluginSearch,
    searchPrefixes,
    openPalette,
    closePalette,
    execute,
  };
}
