/**
 * Plugin file decorations on the project tree (contributes.fileDecorations).
 * The project page reports the directories it loaded, collapsed or
 * refreshed; this composable sends them, through the shared queue of
 * domain/plugins/file-decorations.ts, to the active providers, and answers
 * the node template with one Map lookup. Nothing is requested, and
 * services/plugin-file-decorations.ts is not even loaded, while no provider
 * is active. `plugin:decorations-refresh {pluginId}` (host.decorations.refresh)
 * re-queues the open directories for that plugin only while the page is
 * mounted and visible: a kept-alive page that is deactivated cancels its
 * requests and asks nothing until it is activated again. The cache is
 * dropped when the workspace, the active project or the provider list changes.
 */
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, shallowRef, watch, type Ref } from "vue";
import {
  createDecorationQueue,
  decorationEligible,
  FILE_DECORATION_LIMITS,
  orderByRecency,
  type DecorationDirectory,
  type DecorationQueue,
  type PluginDecorationProvider,
  type TreeDecoration,
} from "@/domain/plugins/file-decorations";
import { useLocalProjectStore } from "@/stores/local-project";
import { usePluginDataProvidersStore } from "@/stores/plugin-data-providers";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import "@/styles/workspaces/project/plugin-file-decorations.css";

interface DecorationNode {
  id: string;
  projectRoot: string;
  path: string;
  isProjectRoot?: boolean;
  worktree?: unknown;
}

type OpenDirectory = DecorationDirectory;

const openKey = (projectRoot: string, directory: string) => `${projectRoot}\u0000${directory}`;

export function usePluginFileDecorations(options: { renamingPath: Ref<string> }) {
  const providersStore = usePluginDataProvidersStore();
  const project = useLocalProjectStore();
  const providers = computed(() => providersStore.decorationProviders.filter((provider) => provider.activeInWorkspace));

  /** Node id → decoration (null: none). Replaced as a whole after every batch; filled lazily by decorationFor. */
  const decorations = shallowRef(new Map<string, TreeDecoration | null>());
  /** Directories open in the tree, most recently loaded last; what a refresh hint re-queues. */
  const open = new Map<string, OpenDirectory>();
  let queue: DecorationQueue | undefined;
  let loading: Promise<DecorationQueue> | undefined;
  let disposed = false;
  /** False while a kept-alive page is hidden: nothing is requested. */
  let visible = true;

  function loadQueue() {
    const pending = (loading ??= import("@/services/plugin-file-decorations").then(({ requestFileDecorations }) => {
      queue = createDecorationQueue({
        ...FILE_DECORATION_LIMITS,
        now: () => Date.now(),
        setTimer: (fn, ms) => setTimeout(fn, ms),
        clearTimer: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
        request: requestFileDecorations,
      });
      queue.onChange(() => {
        decorations.value = new Map();
      });
      return queue;
    }));
    // A failed chunk load is retried on the next request instead of being cached.
    pending.catch(() => {
      if (loading === pending) loading = undefined;
    });
    return pending;
  }

  /** Queues directories for the active providers (or one plugin's), loading the request service on first use. */
  function request(directories: readonly OpenDirectory[], pluginId?: string) {
    const targets = providers.value.filter((provider) => pluginId === undefined || provider.pluginId === pluginId);
    if (!targets.length || !directories.length || disposed || !visible) return;
    const send = (target: DecorationQueue) => {
      if (disposed || !visible) return;
      for (const provider of targets) {
        for (const entry of directories) target.enqueue(provider, entry.projectRoot, entry.directory, entry.names);
      }
    };
    if (queue) send(queue);
    else void loadQueue().then(send, () => undefined);
  }

  function remember(entry: OpenDirectory) {
    const key = openKey(entry.projectRoot, entry.directory);
    open.delete(key);
    open.set(key, entry);
    while (open.size > FILE_DECORATION_LIMITS.cachedDirectoriesMax) open.delete(open.keys().next().value as string);
  }

  function directoryLoaded(projectRoot: string, directory: string, names: string[]) {
    const entry = { projectRoot, directory, names: names.slice(0, FILE_DECORATION_LIMITS.namesMax) };
    remember(entry);
    request([entry]);
  }

  function directoryCollapsed(projectRoot: string, directory: string) {
    for (const [key, entry] of open) {
      if (entry.projectRoot === projectRoot && (directory === "" || entry.directory === directory || entry.directory.startsWith(`${directory}/`))) {
        open.delete(key);
      }
    }
    queue?.cancelDirectory(projectRoot, directory);
  }

  /**
   * After an in-place or manual refresh: the expanded directories replace
   * the open set, ordered by when they were expanded (not tree order), so a
   * capped round keeps the most recently expanded ones.
   */
  function refreshExpanded(expanded: readonly OpenDirectory[]) {
    const ordered = orderByRecency(expanded, [...open.keys()]);
    open.clear();
    for (const entry of ordered) remember({ ...entry, names: entry.names.slice(0, FILE_DECORATION_LIMITS.namesMax) });
    request([...open.values()]);
  }

  function decorationFor(node: DecorationNode): TreeDecoration | undefined {
    const map = decorations.value;
    if (!queue || !decorationEligible(node, options.renamingPath.value)) return undefined;
    let decoration = map.get(node.id);
    if (decoration === undefined) {
      decoration = queue.lookup(node.projectRoot, node.path.replace(/\\/g, "/")) ?? null;
      map.set(node.id, decoration);
    }
    return decoration ?? undefined;
  }

  function reset() {
    queue?.clear();
    decorations.value = new Map();
  }

  const providerKey = (list: readonly PluginDecorationProvider[]) =>
    list.map((provider) => `${provider.pluginId}/${provider.id}/${provider.pluginName}`).join("\n");
  // The tree may already have reported directories of the new state before
  // a watcher runs, so the open directories still listed are re-queued.
  watch(
    () => [providerKey(providers.value), project.activeWorkspaceId, project.projectRoot] as const,
    () => {
      const roots = new Set(project.projects.map((entry) => entry.root));
      for (const [key, entry] of open) if (!roots.has(entry.projectRoot)) open.delete(key);
      reset();
      request([...open.values()]);
    },
  );

  let stopRefresh: (() => void) | undefined;
  onMounted(() => {
    if (!isDesktopRuntime()) return;
    stopRefresh = listenAppEvent<{ pluginId?: unknown }>("plugin:decorations-refresh", (event) => {
      if (typeof event?.pluginId === "string") request([...open.values()], event.pluginId);
    });
  });
  // KeepAlive: the first activation comes with mounting, when the page is already visible.
  onActivated(() => {
    if (visible) return;
    visible = true;
    request([...open.values()]);
  });
  onDeactivated(() => {
    visible = false;
    queue?.cancelAll();
  });
  onBeforeUnmount(() => {
    disposed = true;
    stopRefresh?.();
    reset();
  });

  return { decorationFor, directoryLoaded, directoryCollapsed, refreshExpanded };
}
