import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { Router } from "vue-router";
import { ElMessage } from "element-plus";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import {
  filterCommandPaletteItems,
  parseCommandPaletteQuery,
  type CommandPaletteAction,
  type CommandPaletteItem,
  type CommandPaletteOpenRequest,
  type CommandPaletteScope,
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
  isDesktopRuntime,
} from "@/services/desktop-bridge";
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
  const { t } = useVueI18n();
  const workbench = useWorkbenchStore();
  const workspaceStateScope = useWorkspaceStateScope();
  const localProject = useLocalProjectStore();
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
  const scope = ref<CommandPaletteScope>("all");
  const resourceLoading = ref(false);
  const indexLoading = ref(false);
  const projectSearchLoading = ref(false);
  const projectSearchError = ref<ProjectSearchError>(null);
  const projectSearchTruncated = ref(false);
  const projectSearchEngine = ref("");
  const projectSearchDurationMs = ref(0);
  const loading = computed(() =>
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
        title: tab.title || t(tab.titleKey as MessageKey),
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

    return [
      ...commandItems,
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
    cancelProjectSearch();
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
    openPalette,
    closePalette,
    execute,
  };
}
