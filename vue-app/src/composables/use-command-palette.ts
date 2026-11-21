import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import type { Router } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import {
  filterCommandPaletteItems,
  parseCommandPaletteQuery,
  pluginServicePaletteItems,
  withoutStalePluginFileResults,
  type CommandPaletteAction,
  type CommandPaletteItem,
  type CommandPaletteOpenRequest,
  type CommandPaletteScope,
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
import {
  refreshProjectFileIndex,
  searchProjectFileIndex,
  type ProjectIndexFileEntry,
} from "@/services/project-index";
import {
  renderProjectCodeSearchResult,
  searchProjectCode,
} from "@/services/project-search";
import { sidebarItemView, sidebarMainItems } from "@/services/sidebar-items";
import {
  chooseNativeFile,
  chooseNativeFiles,
  isDesktopRuntime,
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
import { useLocalProjectStore } from "@/stores/local-project";
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

type ProjectSearchError = "no-project" | "query-too-short" | "search-failed" | null;

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

const CONTENT_RESULT_LIMIT = 50;

function isAbortError(error: unknown) {
  return error instanceof DOMException && error.name === "AbortError";
}

function collectHttpRequests(
  collections: readonly HttpRequestCollection[],
): Array<{ collection: HttpRequestCollection; request: SavedHttpRequest }> {
  return collections.flatMap((collection) => [
    ...collection.requests.map((request) => ({ collection, request })),
    ...collectHttpRequests(collection.children ?? []),
  ]);
}

function relativeProjectPath(root: string, path: string) {
  const normalizedRoot = root.replace(/\\/g, "/").replace(/\/+$/, "");
  const normalizedPath = path.replace(/\\/g, "/");
  return normalizedPath.startsWith(`${normalizedRoot}/`)
    ? normalizedPath.slice(normalizedRoot.length + 1)
    : normalizedPath;
}

export function useCommandPalette(options: UseCommandPaletteOptions) {
  const { t, localeChain } = useVueI18n();
  const workbench = useWorkbenchStore();
  const workspaceStateScope = useWorkspaceStateScope();
  const localProject = useLocalProjectStore();
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
  const scope = ref<CommandPaletteScope>("all");
  const resourceLoading = ref(false);
  const indexLoading = ref(false);
  const projectSearchLoading = ref(false);
  const projectSearchError = ref<ProjectSearchError>(null);
  const projectSearchTruncated = ref(false);
  const projectSearchEngine = ref("");
  const projectSearchDurationMs = ref(0);
  // Plugin search providers: `<prefix>:<query>` in the "all" scope.
  const pluginSearch = ref<PluginSearchStatus | null>(null);
  const pluginResultItems = ref<CommandPaletteItem[]>([]);
  const pluginQuery = computed(() =>
    scope.value === "all"
      ? parseProviderQuery(query.value, pluginSearchProviders.providers)
      : undefined,
  );
  const searchPrefixes = computed(() =>
    pluginSearchProviders.providers.map((provider) => ({
      prefix: provider.prefix,
      title: searchProviderTitle(provider, localeChain.value),
    })),
  );
  const loading = computed(() =>
    pluginSearch.value?.state === "searching" ||
    (scope.value === "all" && resourceLoading.value) ||
    ((scope.value === "all" || scope.value === "files") && indexLoading.value) ||
    (scope.value === "content" && projectSearchLoading.value),
  );
  const executingId = ref("");
  const httpCollections = ref<HttpRequestCollection[]>([]);
  const indexedFiles = ref<ProjectIndexFileEntry[]>([]);
  const projectFileItems = ref<CommandPaletteItem[]>([]);
  const projectContentItems = ref<CommandPaletteItem[]>([]);
  const projectRoots = computed(() =>
    localProject.projects.map((project) => project.root).slice(0, 12),
  );
  const activeProjectRoot = computed(() => localProject.projectRoot.trim());
  function projectDisplayName(root: string) {
    const namedProject = localProject.projects.find(
      (project) => project.root === root,
    )?.name;
    if (namedProject) return namedProject;
    const segments = root.split(/[\\/]/).filter(Boolean);
    return segments[segments.length - 1] ?? "";
  }
  const activeProjectName = computed(() =>
    projectDisplayName(activeProjectRoot.value),
  );
  /**
   * Which workspace projects the file and content scopes search. An empty target
   * means every project in the workspace, which is the default so a multi-root
   * workspace is searchable without first activating the project you want.
   */
  const projectTarget = ref("");
  const searchRoots = computed(() =>
    projectTarget.value && projectRoots.value.includes(projectTarget.value)
      ? [projectTarget.value]
      : projectRoots.value,
  );
  const projectOptions = computed(() =>
    projectRoots.value.map((root) => ({
      root,
      name: projectDisplayName(root),
    })),
  );
  const projectScopeAvailable = computed(() => projectRoots.value.length > 0);
  /** Labels results by project so grouped sections stay attributable. */
  const multiRootSearch = computed(() => searchRoots.value.length > 1);
  const searchTarget = () =>
    JSON.stringify([
      open.value,
      scope.value,
      searchRoots.value,
      query.value,
    ]);
  const projectSearchGate = createLatestRequestGate(searchTarget);
  let loadSequence = 0;
  let indexSequence = 0;
  let indexTimer: ReturnType<typeof setTimeout> | undefined;
  let contentTimer: ReturnType<typeof setTimeout> | undefined;
  let contentAbortController: AbortController | undefined;
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
          ...("query" in item ? { query: item.query } : {}),
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

    const projectItems: CommandPaletteItem[] = localProject.projects.map(
      (project, index) => ({
        id: `project:${project.root}`,
        kind: "project",
        title: project.name,
        subtitle: project.root,
        priority: 40 + index,
        action: { type: "select-project", root: project.root },
      }),
    );

    const indexedFileItems: CommandPaletteItem[] = indexedFiles.value.map(
      (file, index) => ({
        id: `file:${file.root}:${file.path}`,
        kind: "file",
        title: file.name,
        subtitle: `${file.path} · ${file.root}`,
        priority: 50 + index,
        action: { type: "open-file", root: file.root, path: file.path },
      }),
    );
    const indexedFileIds = new Set(indexedFileItems.map((item) => item.id));
    const openFileItems: CommandPaletteItem[] = localProject.projectRoot
      ? localProject.documents.flatMap((document, index) => {
          const relativePath = relativeProjectPath(
            localProject.projectRoot,
            document.path,
          );
          const id = `file:${localProject.projectRoot}:${relativePath}`;
          if (indexedFileIds.has(id)) return [];
          return [
            {
              id,
              kind: "file" as const,
              title: document.name,
              subtitle: relativePath,
              priority: 55 + index,
              action: {
                type: "open-file" as const,
                root: localProject.projectRoot,
                path: relativePath,
              },
            },
          ];
        })
      : [];

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
      ...projectItems,
      ...indexedFileItems,
      ...openFileItems,
      ...sshItems,
      ...databaseItems,
      ...httpItems,
    ].map(withUsage);
  });

  const results = computed(() => {
    if (pluginQuery.value) return pluginResultItems.value;
    if (scope.value === "files") return projectFileItems.value;
    if (scope.value === "content") return projectContentItems.value;
    return filterCommandPaletteItems(items.value, query.value);
  });

  function fileIndexItem(file: ProjectIndexFileEntry, index: number): CommandPaletteItem {
    return withUsage({
      id: `file:${file.root}:${file.path}`,
      kind: "file",
      title: file.name,
      subtitle: file.path,
      group: multiRootSearch.value ? projectDisplayName(file.root) : undefined,
      search: { scope: { path: file.path } },
      priority: 50 + index,
      action: { type: "open-file", root: file.root, path: file.path },
    });
  }

  function cancelProjectSearch() {
    if (contentTimer) {
      clearTimeout(contentTimer);
      contentTimer = undefined;
    }
    contentAbortController?.abort();
    contentAbortController = undefined;
    projectSearchGate.invalidate();
    projectSearchLoading.value = false;
  }

  async function runIndexSearch(delay = 150) {
    if (indexTimer) clearTimeout(indexTimer);
    const sequence = ++indexSequence;
    const roots = scope.value === "files" ? searchRoots.value : projectRoots.value;
    if (!open.value || !isDesktopRuntime() || roots.length === 0) {
      if (scope.value === "files") {
        projectFileItems.value = [];
        projectSearchDurationMs.value = 0;
      } else {
        indexedFiles.value = [];
      }
      indexLoading.value = false;
      return;
    }
    indexTimer = setTimeout(async () => {
      indexTimer = undefined;
      indexLoading.value = true;
      // Measured around the native call only, so the debounce window is not
      // billed to the search itself.
      const startedAt = performance.now();
      try {
        const searchQuery =
          scope.value === "files"
            ? query.value.trim()
            : parseCommandPaletteQuery(query.value).query;
        const files = await searchProjectFileIndex(roots, searchQuery, 80);
        if (sequence !== indexSequence) return;
        if (scope.value === "files") {
          projectFileItems.value = files.map(fileIndexItem);
          projectSearchError.value = null;
          projectSearchEngine.value = "";
          projectSearchDurationMs.value = Math.max(
            1,
            Math.round(performance.now() - startedAt),
          );
        } else {
          indexedFiles.value = files;
        }
      } catch {
        if (sequence !== indexSequence) return;
        if (scope.value === "files") {
          projectFileItems.value = [];
          projectSearchError.value = "search-failed";
          projectSearchDurationMs.value = 0;
        } else {
          indexedFiles.value = [];
        }
      } finally {
        if (sequence === indexSequence) indexLoading.value = false;
      }
    }, delay);
  }

  function runContentSearch() {
    cancelProjectSearch();
    projectSearchError.value = null;
    projectSearchTruncated.value = false;
    projectSearchEngine.value = "";
    projectSearchDurationMs.value = 0;
    if (!open.value || scope.value !== "content") return;
    const roots = searchRoots.value;
    if (roots.length === 0) {
      projectContentItems.value = [];
      projectSearchError.value = "no-project";
      return;
    }
    const searchQuery = query.value.trim();
    if (searchQuery.length < 2) {
      projectContentItems.value = [];
      projectSearchError.value = "query-too-short";
      return;
    }

    const token = projectSearchGate.begin();
    projectSearchLoading.value = true;
    contentTimer = setTimeout(async () => {
      contentTimer = undefined;
      const controller = new AbortController();
      contentAbortController = controller;
      const labelled = roots.length > 1;
      try {
        // The native command is scoped to one root, so a workspace-wide search
        // fans out per project on a shared signal and merges the ranked results.
        const settled = await Promise.allSettled(
          roots.map((root) =>
            searchProjectCode({
              projectRoot: root,
              query: searchQuery,
              mode: "text",
              limit: CONTENT_RESULT_LIMIT,
              signal: controller.signal,
            }).then((response) => ({ root, response })),
          ),
        );
        if (!projectSearchGate.isCurrent(token) || controller.signal.aborted) return;

        const fulfilled = settled.flatMap((outcome) =>
          outcome.status === "fulfilled" ? [outcome.value] : [],
        );
        if (fulfilled.length === 0) {
          projectContentItems.value = [];
          projectSearchError.value = "search-failed";
          return;
        }

        const merged = fulfilled
          .flatMap(({ root, response }) =>
            response.results.map((result) => ({ root, result })),
          )
          .sort(
            (left, right) =>
              right.result.score - left.result.score ||
              left.result.path.localeCompare(right.result.path) ||
              (left.result.line ?? 0) - (right.result.line ?? 0),
          );

        projectContentItems.value = merged
          .slice(0, CONTENT_RESULT_LIMIT)
          .map(({ root, result }, index) => ({
            id: `content:${root}:${result.path}:${result.line ?? 0}:${result.column ?? 0}:${index}`,
            priority: index,
            ...renderProjectCodeSearchResult(root, result),
            group: labelled ? projectDisplayName(root) : undefined,
          }));
        projectSearchTruncated.value =
          merged.length > CONTENT_RESULT_LIMIT ||
          fulfilled.some(({ response }) => response.truncated);
        projectSearchEngine.value =
          fulfilled.find(({ response }) => response.engine)?.response.engine ?? "";
        // Clamped to 1 so a sub-millisecond search still reads as "ran"; 0 is
        // reserved for "nothing has been searched yet".
        projectSearchDurationMs.value = fulfilled.reduce(
          (longest, { response }) => Math.max(longest, response.durationMs),
          1,
        );
      } catch (error) {
        if (projectSearchGate.isCurrent(token) && !isAbortError(error)) {
          projectContentItems.value = [];
          projectSearchError.value = "search-failed";
        }
      } finally {
        if (contentAbortController === controller) contentAbortController = undefined;
        if (projectSearchGate.isCurrent(token)) projectSearchLoading.value = false;
      }
    }, 120);
  }

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
    [open, query, scope, () => pluginSearchProviders.providers, localeChain],
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

  watch(
    [open, query, scope, projectTarget, projectRoots],
    () => {
      if (scope.value === "content") {
        if (indexTimer) clearTimeout(indexTimer);
        indexSequence += 1;
        indexLoading.value = false;
        runContentSearch();
        return;
      }
      cancelProjectSearch();
      projectSearchError.value =
        scope.value === "files" && searchRoots.value.length === 0
          ? "no-project"
          : null;
      void runIndexSearch(scope.value === "files" ? 20 : 150);
    },
    { flush: "sync" },
  );

  async function refreshResources() {
    const sequence = ++loadSequence;
    resourceLoading.value = true;
    try {
      await workspaceStateScope.hydrate();
      const workspaceScopeId = workspaceStateScope.capture().scopeId;
      const [, , , httpState] = await Promise.all([
        localProject.hydrated
          ? Promise.resolve()
          : localProject.hydrate(workspaceScopeId),
        sshConnections.hydrate(),
        database.initialize(),
        loadHttpState<StoredHttpState>(workspaceScopeId),
      ]);
      if (localProject.hydrated) localProject.switchWorkspace(workspaceScopeId);
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

  /**
   * Refreshes the native file index in the background, then re-queries only when
   * the palette is still showing exactly the roots that were warmed.
   */
  function warmProjectIndexes(roots: readonly string[]) {
    if (!roots.length || !isDesktopRuntime()) return;
    const expectedRoots = [...roots];
    void refreshProjectFileIndex(expectedRoots)
      .then(() => {
        if (!open.value || scope.value === "content") return;
        const currentRoots =
          scope.value === "files" ? searchRoots.value : projectRoots.value;
        if (
          expectedRoots.length === currentRoots.length &&
          expectedRoots.every((root, index) => currentRoots[index] === root)
        ) {
          void runIndexSearch(0);
        }
      })
      .catch(() => undefined);
  }

  function openPalette(request: Omit<CommandPaletteOpenRequest, "seq"> = {}) {
    openContext.value = currentKeybindingContext();
    query.value = request.prefix ?? "";
    scope.value =
      request.scope && (request.scope === "all" || projectScopeAvailable.value) ? request.scope : "all";
    projectSearchError.value = null;
    projectSearchEngine.value = "";
    projectSearchDurationMs.value = 0;
    open.value = true;
    warmProjectIndexes(projectRoots.value);
    void refreshResources();
  }

  function closePalette() {
    cancelProjectSearch();
    cancelPluginSearch();
    if (indexTimer) {
      clearTimeout(indexTimer);
      indexTimer = undefined;
    }
    indexSequence += 1;
    indexLoading.value = false;
    open.value = false;
    query.value = "";
    executingId.value = "";
  }

  watch(
    activeProjectRoot,
    (root) => {
      if (root) warmProjectIndexes([root]);
    },
    { immediate: true },
  );

  // A project target that leaves the workspace falls back to searching everything.
  watch(projectRoots, (roots) => {
    if (projectTarget.value && !roots.includes(projectTarget.value)) {
      projectTarget.value = "";
    }
  });

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
      case "select-project":
        await openView("projects");
        await requestAiWorkspaceAction("projects", {
          action: "select",
          root: action.root,
        });
        return;
      case "open-file":
        await openView("projects");
        await requestAiWorkspaceAction("projects", {
          action: "open-file",
          projectRoot: action.root,
          path: action.path,
          line: action.line,
          column: action.column,
        });
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
    cancelProjectSearch();
    cancelPluginSearch();
    if (indexTimer) clearTimeout(indexTimer);
    indexSequence += 1;
  });

  return {
    open,
    query,
    scope,
    loading,
    executingId,
    results,
    activeProjectName,
    activeProjectRoot,
    projectTarget,
    projectOptions,
    projectScopeAvailable,
    projectSearchError,
    projectSearchTruncated,
    projectSearchEngine,
    projectSearchDurationMs,
    pluginSearch,
    searchPrefixes,
    openPalette,
    closePalette,
    execute,
  };
}
