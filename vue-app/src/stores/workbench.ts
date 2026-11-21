import { computed, onBeforeUnmount, ref, watch } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { useQuotaStore } from "@/stores/quota";
import type {
  WorkspaceDefinition,
  WorkspaceTab,
  WorkspaceView,
} from "@/types/workspace";
import {
  DEFAULT_WORKSPACE_ID,
  WORKBENCH_SESSION_VERSION,
  defaultWorkspace,
  isPluginViewTab,
  isTransientTab,
  normalizeWorkbenchSession,
  normalizeWorkspaces,
  pluginViewTabId,
  prependDeletedWorkspace,
  serviceResultTabId,
  viewTitleKeys,
  type DeletedWorkspaceRecord,
  type PersistedWorkbenchSession,
} from "@/domain/workspace/workbench-session";

export { DEFAULT_WORKSPACE_ID };
export type { DeletedWorkspaceRecord };

const WORKSPACES_KEY = "app_workspaces_v1";
const ACTIVE_WORKSPACE_KEY = "app_active_workspace_v1";
const WORKBENCH_SESSION_KEY = "app_workbench_session_v1";

function now() {
  return Date.now();
}

function createId(prefix: string) {
  const uuid = globalThis.crypto?.randomUUID?.();
  return `${prefix}:${uuid ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`;
}

function loadWorkspaces(): WorkspaceDefinition[] {
  if (typeof window === "undefined") return [defaultWorkspace()];
  try {
    return normalizeWorkspaces(JSON.parse(localStorage.getItem(WORKSPACES_KEY) || "[]") as unknown);
  } catch {
    return [defaultWorkspace()];
  }
}

function loadActiveWorkspaceId(workspaces: WorkspaceDefinition[]) {
  if (typeof window === "undefined") return DEFAULT_WORKSPACE_ID;
  const saved = localStorage.getItem(ACTIVE_WORKSPACE_KEY) || DEFAULT_WORKSPACE_ID;
  return workspaces.some((item) => item.id === saved) ? saved : DEFAULT_WORKSPACE_ID;
}

export const useWorkbenchStore = defineStore("workbench", () => {
  const workspaces = ref<WorkspaceDefinition[]>(loadWorkspaces());
  const activeWorkspaceId = ref(loadActiveWorkspaceId(workspaces.value));
  const tabs = ref<WorkspaceTab[]>([]);
  const activeTabId = ref("");
  const lastActiveTabByWorkspace = ref<Record<string, string>>({});
  const deletedWorkspaces = ref<DeletedWorkspaceRecord[]>([]);
  // 独立原生窗口的 label 只属于当前进程的运行态，不能写入会话存储。
  const poppedOutTabLabels = ref<Record<string, string>>({});
  const hydrated = ref(false);

  const workspaceTabs = computed(() =>
    tabs.value.filter((tab) => tab.workspaceId === activeWorkspaceId.value)
  );
  const activeWorkspace = computed(
    () =>
      workspaces.value.find((item) => item.id === activeWorkspaceId.value) ?? workspaces.value[0]
  );
  const activeTab = computed(() => tabs.value.find((tab) => tab.id === activeTabId.value));
  const activeTabPopupLabel = computed(() => poppedOutTabLabels.value[activeTabId.value]);

  function tabId(view: WorkspaceView) {
    return `${activeWorkspaceId.value}:${view}`;
  }

  function rememberActiveTab() {
    if (activeTabId.value)
      lastActiveTabByWorkspace.value[activeWorkspaceId.value] = activeTabId.value;
  }

  const quota = useQuotaStore();
  // Every path that adds an active workspace or a workspace tab must pass through these two checks.
  function admitTab(workspaceId: string) {
    return quota.admit("tab", tabs.value.filter((tab) => tab.workspaceId === workspaceId).length);
  }
  function admitWorkspace() {
    return quota.admit("workspace", workspaces.value.length);
  }

  function createTab(view: WorkspaceView, title?: string) {
    if (view === "dashboard") {
      activeTabId.value = "";
      return undefined;
    }
    if (!admitTab(activeWorkspaceId.value)) return undefined;

    const tab: WorkspaceTab = {
      id: createId(`${activeWorkspaceId.value}:${view}`),
      workspaceId: activeWorkspaceId.value,
      view,
      titleKey: viewTitleKeys[view],
      title,
      closable: true,
    };
    tabs.value.push(tab);
    activeTabId.value = tab.id;
    rememberActiveTab();
    return tab;
  }

  function openView(view: WorkspaceView) {
    if (view === "plugin-view" || view === "service-result") return undefined;
    if (view === "dashboard") {
      activeTabId.value = "";
      return undefined;
    }

    const id = tabId(view);
    const existing = tabs.value.find((tab) => tab.id === id);
    if (existing) {
      activeTabId.value = id;
      rememberActiveTab();
      return existing;
    }
    if (!admitTab(activeWorkspaceId.value)) return undefined;

    const tab: WorkspaceTab = {
      id,
      workspaceId: activeWorkspaceId.value,
      view,
      titleKey: viewTitleKeys[view],
      closable: true,
    };
    tabs.value.push(tab);
    activeTabId.value = tab.id;
    rememberActiveTab();
    return tab;
  }

  function openPluginView(pluginId: string, viewId: string, title: string) {
    const normalizedPluginId = pluginId.trim();
    const normalizedViewId = viewId.trim();
    if (!normalizedPluginId || !normalizedViewId) return undefined;
    const id = pluginViewTabId(activeWorkspaceId.value, normalizedPluginId, normalizedViewId);
    const existing = tabs.value.find((tab) =>
      isPluginViewTab(tab, activeWorkspaceId.value, normalizedPluginId, normalizedViewId)
    );
    if (existing) {
      existing.title = title.trim() || existing.title;
      activeTabId.value = existing.id;
      rememberActiveTab();
      return existing;
    }
    if (!admitTab(activeWorkspaceId.value)) return undefined;
    const tab: WorkspaceTab = {
      id,
      workspaceId: activeWorkspaceId.value,
      view: "plugin-view",
      titleKey: viewTitleKeys["plugin-view"],
      title: title.trim() || undefined,
      closable: true,
      pluginView: { pluginId: normalizedPluginId, viewId: normalizedViewId },
    };
    tabs.value.push(tab);
    activeTabId.value = tab.id;
    rememberActiveTab();
    return tab;
  }

  /** Opens (or selects) the read-only tab of a plugin service result held in memory under resultId. */
  function openServiceResult(resultId: string, title: string) {
    const normalizedResultId = resultId.trim();
    if (!normalizedResultId) return undefined;
    const existing = tabs.value.find(
      (tab) =>
        tab.workspaceId === activeWorkspaceId.value &&
        tab.view === "service-result" &&
        tab.serviceResult?.resultId === normalizedResultId
    );
    if (existing) {
      activeTabId.value = existing.id;
      rememberActiveTab();
      return existing;
    }
    if (!admitTab(activeWorkspaceId.value)) return undefined;
    const tab: WorkspaceTab = {
      id: serviceResultTabId(activeWorkspaceId.value, normalizedResultId),
      workspaceId: activeWorkspaceId.value,
      view: "service-result",
      titleKey: viewTitleKeys["service-result"],
      title: title.trim() || undefined,
      closable: true,
      serviceResult: { resultId: normalizedResultId },
    };
    tabs.value.push(tab);
    activeTabId.value = tab.id;
    rememberActiveTab();
    return tab;
  }

  function selectTab(id: string) {
    const tab = tabs.value.find((item) => item.id === id);
    if (!tab) return undefined;
    if (activeWorkspaceId.value === tab.workspaceId && activeTabId.value === id) return tab;
    activeWorkspaceId.value = tab.workspaceId;
    activeTabId.value = id;
    rememberActiveTab();
    return tab;
  }

  function renameTab(id: string, title: string) {
    const tab = tabs.value.find((item) => item.id === id);
    if (!tab) return;
    tab.title = title.trim() || undefined;
  }

  function duplicateTab(id: string, copySuffix = "copy") {
    const source = tabs.value.find((item) => item.id === id);
    if (!source) return undefined;
    // One plugin view per workspace; a service result tab shows content held once in memory.
    if (source.view === "plugin-view" || source.view === "service-result") {
      activeWorkspaceId.value = source.workspaceId;
      activeTabId.value = source.id;
      rememberActiveTab();
      return source;
    }
    if (!admitTab(source.workspaceId)) return undefined;
    const index = tabs.value.indexOf(source);
    const copy: WorkspaceTab = {
      ...source,
      id: createId(`${source.workspaceId}:${source.view}`),
      title: source.title ? `${source.title} ${copySuffix}` : undefined,
      pinned: false,
    };
    tabs.value.splice(index + 1, 0, copy);
    activeTabId.value = copy.id;
    rememberActiveTab();
    return copy;
  }

  function togglePinTab(id: string) {
    const tab = tabs.value.find((item) => item.id === id);
    if (!tab) return;
    tab.pinned = !tab.pinned;
    const scoped = workspaceTabs.value;
    const pinned = scoped.filter((item) => item.pinned);
    const unpinned = scoped.filter((item) => !item.pinned);
    const scopedIds = new Set(scoped.map((item) => item.id));
    const ordered = [...pinned, ...unpinned];
    let cursor = 0;
    tabs.value = tabs.value.map((item) => (scopedIds.has(item.id) ? ordered[cursor++]! : item));
  }

  function closeTab(id: string, force = false) {
    const tab = tabs.value.find((item) => item.id === id);
    if (!tab || (!force && (tab.pinned || poppedOutTabLabels.value[id]))) return;

    const scopedBefore = tabs.value.filter((item) => item.workspaceId === tab.workspaceId);
    const tabIndex = scopedBefore.findIndex((item) => item.id === id);
    tabs.value = tabs.value.filter((item) => item.id !== id);
    delete poppedOutTabLabels.value[id];
    if (activeTabId.value === id) {
      const remaining = tabs.value.filter((item) => item.workspaceId === tab.workspaceId);
      activeTabId.value = remaining[Math.max(0, tabIndex - 1)]?.id ?? "";
      rememberActiveTab();
    }
  }

  function closeTabsWhere(
    tabIdValue: string,
    predicate: (index: number, targetIndex: number) => boolean
  ) {
    const target = tabs.value.find((item) => item.id === tabIdValue);
    if (!target) return;
    const scoped = tabs.value.filter((item) => item.workspaceId === target.workspaceId);
    const targetIndex = scoped.findIndex((item) => item.id === tabIdValue);
    const closing = new Set(
      scoped
        .filter(
          (item, index) =>
            !item.pinned &&
            !poppedOutTabLabels.value[item.id] &&
            predicate(index, targetIndex)
        )
        .map((item) => item.id)
    );
    tabs.value = tabs.value.filter((item) => !closing.has(item.id));
    for (const id of closing) delete poppedOutTabLabels.value[id];
    if (closing.has(activeTabId.value)) activeTabId.value = tabIdValue;
    rememberActiveTab();
  }

  function closeTabsToLeft(id: string) {
    closeTabsWhere(id, (index, targetIndex) => index < targetIndex);
  }
  function closeTabsToRight(id: string) {
    closeTabsWhere(id, (index, targetIndex) => index > targetIndex);
  }
  function closeOtherTabs(id: string) {
    closeTabsWhere(id, (index, targetIndex) => index !== targetIndex);
  }

  function moveTabToWorkspace(tabIdValue: string, workspaceId: string) {
    const tab = tabs.value.find((item) => item.id === tabIdValue);
    if (
      !tab ||
      poppedOutTabLabels.value[tabIdValue] ||
      !workspaces.value.some((item) => item.id === workspaceId)
    )
      return;
    const sourceWorkspaceId = tab.workspaceId;
    if (sourceWorkspaceId === workspaceId) return tab;

    if (tab.view === "plugin-view" && tab.pluginView) {
      const { pluginId, viewId } = tab.pluginView;
      const existing = tabs.value.find(
        (item) => item !== tab && isPluginViewTab(item, workspaceId, pluginId, viewId)
      );
      if (existing) {
        tabs.value = tabs.value.filter((item) => item !== tab);
        if (lastActiveTabByWorkspace.value[sourceWorkspaceId] === tabIdValue) {
          const replacement = tabs.value.find((item) => item.workspaceId === sourceWorkspaceId);
          if (replacement) lastActiveTabByWorkspace.value[sourceWorkspaceId] = replacement.id;
          else delete lastActiveTabByWorkspace.value[sourceWorkspaceId];
        }
        activeWorkspaceId.value = workspaceId;
        activeTabId.value = existing.id;
        rememberActiveTab();
        return existing;
      }
      if (!admitTab(workspaceId)) return undefined;
      tab.workspaceId = workspaceId;
      tab.id = pluginViewTabId(workspaceId, pluginId, viewId);
    } else {
      if (!admitTab(workspaceId)) return undefined;
      tab.workspaceId = workspaceId;
      tab.id = createId(`${workspaceId}:${tab.view}`);
    }
    if (lastActiveTabByWorkspace.value[sourceWorkspaceId] === tabIdValue) {
      const replacement = tabs.value.find((item) => item.workspaceId === sourceWorkspaceId);
      if (replacement) lastActiveTabByWorkspace.value[sourceWorkspaceId] = replacement.id;
      else delete lastActiveTabByWorkspace.value[sourceWorkspaceId];
    }
    activeWorkspaceId.value = workspaceId;
    activeTabId.value = tab.id;
    rememberActiveTab();
    return tab;
  }

  function registerPopupTab(tab: WorkspaceTab) {
    const existing = tabs.value.find((item) => item.id === tab.id);
    if (existing) return existing;
    tabs.value.push({ ...tab });
    activeWorkspaceId.value = tab.workspaceId;
    activeTabId.value = tab.id;
    return tab;
  }

  function markTabPoppedOut(id: string, label: string) {
    if (!tabs.value.some((tab) => tab.id === id) || !label) return;
    poppedOutTabLabels.value[id] = label;
  }

  function reclaimPoppedOutTab(id: string) {
    delete poppedOutTabLabels.value[id];
  }

  function reclaimPoppedOutTabByLabel(label: string) {
    for (const [tabId, windowLabel] of Object.entries(poppedOutTabLabels.value)) {
      if (windowLabel === label) {
        delete poppedOutTabLabels.value[tabId];
        return tabId;
      }
    }
    return undefined;
  }

  function switchWorkspace(id: string) {
    if (!workspaces.value.some((item) => item.id === id)) return undefined;
    activeWorkspaceId.value = id;
    const scoped = tabs.value.filter((tab) => tab.workspaceId === id);
    const preferred = lastActiveTabByWorkspace.value[id];
    const next = scoped.find((tab) => tab.id === preferred) ?? scoped[0];
    activeTabId.value = next?.id ?? "";
    return next;
  }

  function createWorkspace(name: string) {
    const trimmed = name.trim();
    if (!trimmed) return undefined;
    if (!admitWorkspace()) return undefined;
    const timestamp = now();
    const workspace: WorkspaceDefinition = {
      id: createId("workspace"),
      name: trimmed,
      createdAt: timestamp,
      updatedAt: timestamp,
    };
    workspaces.value.push(workspace);
    activeWorkspaceId.value = workspace.id;
    activeTabId.value = "";
    return workspace;
  }

  function renameWorkspace(id: string, name: string) {
    const workspace = workspaces.value.find((item) => item.id === id);
    const trimmed = name.trim();
    if (!workspace || !trimmed) return;
    workspace.name = trimmed;
    workspace.updatedAt = now();
  }

  function deleteWorkspace(id: string, stateScopeId?: string) {
    if (id === DEFAULT_WORKSPACE_ID) return false;
    const index = workspaces.value.findIndex((item) => item.id === id);
    if (index < 0) return false;
    const workspace = workspaces.value[index]!;
    const archivedTabs = tabs.value
      .filter((tab) => tab.workspaceId === id && !isTransientTab(tab))
      .map((tab) => ({ ...tab }));
    const lastActiveTabId = lastActiveTabByWorkspace.value[id];
    deletedWorkspaces.value = prependDeletedWorkspace(deletedWorkspaces.value, {
      workspace: { ...workspace },
      tabs: archivedTabs,
      lastActiveTabId: archivedTabs.some((tab) => tab.id === lastActiveTabId)
        ? lastActiveTabId!
        : archivedTabs[0]?.id ?? "",
      deletedAt: now(),
      stateScopeId: stateScopeId?.trim() || undefined,
    });

    workspaces.value.splice(index, 1);
    const removedTabIds = new Set(
      tabs.value.filter((tab) => tab.workspaceId === id).map((tab) => tab.id)
    );
    tabs.value = tabs.value.filter((tab) => tab.workspaceId !== id);
    for (const tabId of removedTabIds) delete poppedOutTabLabels.value[tabId];
    delete lastActiveTabByWorkspace.value[id];
    if (activeWorkspaceId.value === id) switchWorkspace(DEFAULT_WORKSPACE_ID);
    return true;
  }

  function permanentlyDeleteWorkspace(id: string) {
    const index = deletedWorkspaces.value.findIndex((item) => item.workspace.id === id);
    if (index < 0) return false;
    deletedWorkspaces.value.splice(index, 1);
    return true;
  }

  function restoreDeletedWorkspace(id: string) {
    if (workspaces.value.some((item) => item.id === id)) return undefined;
    const index = deletedWorkspaces.value.findIndex((item) => item.workspace.id === id);
    if (index < 0) return undefined;
    if (!admitWorkspace()) return undefined;
    const restored = deletedWorkspaces.value[index]!;
    const existingTabIds = new Set(tabs.value.map((tab) => tab.id));
    const restoredTabs = restored.tabs.filter((tab) => !existingTabIds.has(tab.id));
    workspaces.value.push({ ...restored.workspace, updatedAt: now() });
    tabs.value.push(...restoredTabs.map((tab) => ({ ...tab })));
    deletedWorkspaces.value.splice(index, 1);
    activeWorkspaceId.value = id;
    const nextTabId = restoredTabs.some((tab) => tab.id === restored.lastActiveTabId)
      ? restored.lastActiveTabId
      : restoredTabs[0]?.id ?? "";
    activeTabId.value = nextTabId;
    if (nextTabId) lastActiveTabByWorkspace.value[id] = nextTabId;
    return restored.workspace;
  }

  function sessionSnapshot(): PersistedWorkbenchSession {
    // Service result tabs hold in-memory content only: they never reach the session store.
    const persistedTabs = tabs.value.filter((tab) => !isTransientTab(tab));
    const persistedTabIds = new Set(persistedTabs.map((tab) => tab.id));
    return {
      version: WORKBENCH_SESSION_VERSION,
      workspaces: workspaces.value.map((workspace) => ({ ...workspace })),
      activeWorkspaceId: activeWorkspaceId.value,
      tabs: persistedTabs.map((tab) => ({ ...tab })),
      activeTabId: persistedTabIds.has(activeTabId.value) ? activeTabId.value : "",
      lastActiveTabByWorkspace: Object.fromEntries(
        Object.entries(lastActiveTabByWorkspace.value).filter(([, tabId]) => persistedTabIds.has(tabId))
      ),
      deletedWorkspaces: deletedWorkspaces.value.map((record) => ({
        ...record,
        workspace: { ...record.workspace },
        tabs: record.tabs.map((tab) => ({ ...tab })),
      })),
    };
  }

  async function hydrate() {
    const [savedSession, savedWorkspaces, savedActiveWorkspaceId] = await Promise.all([
      loadPersistedValue<unknown>(WORKBENCH_SESSION_KEY),
      loadPersistedValue<unknown>(WORKSPACES_KEY),
      loadPersistedValue<unknown>(ACTIVE_WORKSPACE_KEY),
    ]);
    const restoredSession = normalizeWorkbenchSession(savedSession);
    if (restoredSession) {
      workspaces.value = restoredSession.workspaces;
      activeWorkspaceId.value = restoredSession.activeWorkspaceId;
      tabs.value = restoredSession.tabs;
      activeTabId.value = restoredSession.activeTabId;
      lastActiveTabByWorkspace.value = restoredSession.lastActiveTabByWorkspace;
      deletedWorkspaces.value = restoredSession.deletedWorkspaces;
    } else {
      const restoredWorkspaces = normalizeWorkspaces(savedWorkspaces);
      workspaces.value = restoredWorkspaces;
      activeWorkspaceId.value =
        typeof savedActiveWorkspaceId === "string" &&
        restoredWorkspaces.some((item) => item.id === savedActiveWorkspaceId)
          ? savedActiveWorkspaceId
          : DEFAULT_WORKSPACE_ID;
      tabs.value = [];
      activeTabId.value = "";
      lastActiveTabByWorkspace.value = {};
      deletedWorkspaces.value = [];
    }
    hydrated.value = true;

    // Migrate legacy workspace-only data and keep the native store canonical. An unchanged
    // store is not rewritten, and the write never delays the first workspace render.
    if (
      JSON.stringify(savedSession) !== JSON.stringify(sessionSnapshot()) ||
      JSON.stringify(savedWorkspaces) !== JSON.stringify(workspaces.value) ||
      savedActiveWorkspaceId !== activeWorkspaceId.value
    ) {
      void flushPersistence();
    }
  }

  let persistTimer: ReturnType<typeof setTimeout> | undefined;

  async function flushPersistence() {
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = undefined;
    if (!hydrated.value) return;
    await Promise.all([
      savePersistedValue(WORKBENCH_SESSION_KEY, sessionSnapshot()),
      savePersistedValue(WORKSPACES_KEY, workspaces.value),
      savePersistedValue(ACTIVE_WORKSPACE_KEY, activeWorkspaceId.value),
    ]);
  }

  function schedulePersist() {
    if (!hydrated.value) return;
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(() => void flushPersistence(), 300);
  }

  watch(
    [workspaces, tabs, activeWorkspaceId, activeTabId, lastActiveTabByWorkspace, deletedWorkspaces],
    schedulePersist,
    { deep: true }
  );
  onBeforeUnmount(() => void flushPersistence());

  return {
    workspaces,
    deletedWorkspaces,
    activeWorkspaceId,
    activeWorkspace,
    tabs,
    workspaceTabs,
    activeTabId,
    activeTab,
    hydrated,
    hydrate,
    flushPersistence,
    activeTabPopupLabel,
    poppedOutTabLabels,
    openView,
    openPluginView,
    openServiceResult,
    createTab,
    selectTab,
    renameTab,
    duplicateTab,
    togglePinTab,
    closeTab,
    closeTabsToLeft,
    closeTabsToRight,
    closeOtherTabs,
    moveTabToWorkspace,
    switchWorkspace,
    createWorkspace,
    renameWorkspace,
    deleteWorkspace,
    permanentlyDeleteWorkspace,
    restoreDeletedWorkspace,
    registerPopupTab,
    markTabPoppedOut,
    reclaimPoppedOutTab,
    reclaimPoppedOutTabByLabel,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useWorkbenchStore, import.meta.hot));
}
