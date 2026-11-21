import { computed, ref, watch } from "vue";
import { runtimePerformanceSettingsFromSnapshot } from "@/domain/performance/runtime-limits";
import { readSettingsSnapshot, settingsSnapshotVersion } from "@/services/settings-persistence";
import { acceptHMRUpdate, defineStore } from "pinia";
import { restoreSshProfile } from "@/stores/ssh-connections";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import type {
  LocalShellType,
  LocalTerminalPane,
  LocalTerminalPaneOptions,
  SshTerminalPane,
  SshTerminalPaneOptions,
  TerminalLeafPane,
  TerminalPaneNode,
  TerminalSplitDirection,
} from "@/domain/terminal/types";
import { inheritSplitCwd, isTerminalLeafPane, cloneTerminalPaneTree } from "@/domain/terminal/types";
import {
  MAX_TERMINAL_TABS,
  TERMINAL_TAB_TITLE_MAX,
  activeTerminalTab,
  adjacentTerminalTabId,
  insertTerminalTab,
  removeTerminalTab,
  renameTerminalTab,
  selectTerminalTab,
  terminalTabForPane,
  updateTerminalTab,
  upgradeLegacyLayout,
  type TerminalTab,
  type TerminalTabsState,
} from "@/domain/terminal/terminal-tabs";

const MAX_TERMINAL_PANES = 8;
const MAX_LAYOUT_DEPTH = 16;
const LAYOUT_STORAGE_KEY = "app_terminal_workspace_v1";
const LAYOUT_STORAGE_VERSION = 4;
const PERSISTED_WORKSPACE_VERSION = 5;
/** 旧版单棵分屏树快照 `{root, activePaneId}` 的版本号；读取时升级为单标签。 */
const LEGACY_LAYOUT_VERSIONS: readonly number[] = [1, 2, 3];
/** 旧版外层：v3 / v4 的 current 与 layoutsByTabId 都是旧版快照。 */
const LEGACY_PERSISTED_VERSIONS: readonly number[] = [3, 4];

const PERSIST_DEBOUNCE_MS = 150;
const SHELL_TYPES: ReadonlySet<LocalShellType> = new Set([
  "system",
  "zsh",
  "bash",
  "fish",
  "pwsh",
  "powershell",
  "cmd",
  "wsl",
]);

export interface TerminalWorkspaceSnapshot {
  version: typeof LAYOUT_STORAGE_VERSION;
  tabs: TerminalTab[];
  activeTabId: string;
}

interface PersistedTerminalWorkspace {
  version: typeof PERSISTED_WORKSPACE_VERSION;
  current: TerminalWorkspaceSnapshot;
  activeLayoutTabId?: string;
  layoutsByTabId: Record<string, TerminalWorkspaceSnapshot>;
}

function nextId(prefix: string) {
  const suffix =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `${prefix}-${suffix}`;
}

function createLocalPane(options: LocalTerminalPaneOptions = {}): LocalTerminalPane {
  return {
    id: nextId("terminal"),
    type: "local",
    shellType: options.shellType ?? "system",
    customPath: options.customPath,
    initialCwd: options.initialCwd,
    cwd: options.initialCwd,
    initialCommand: options.initialCommand,
    extraEnv: options.extraEnv,
  };
}

function createSshPane(options: SshTerminalPaneOptions): SshTerminalPane {
  return {
    id: nextId("ssh-terminal"),
    type: "ssh",
    profile: options.profile,
    credentials: options.credentials,
  };
}

function terminalIds(node: TerminalPaneNode): string[] {
  if (isTerminalLeafPane(node)) return [node.id];
  return node.children.flatMap(terminalIds);
}

function normalizeSizes(sizes: readonly number[] | undefined, childCount: number): number[] {
  if (childCount <= 0) return [];
  if (
    !sizes ||
    sizes.length !== childCount ||
    sizes.some((size) => !Number.isFinite(size) || size <= 0)
  ) {
    return Array.from({ length: childCount }, () => 1 / childCount);
  }
  const total = sizes.reduce((sum, size) => sum + size, 0);
  if (!Number.isFinite(total) || total <= 0) {
    return Array.from({ length: childCount }, () => 1 / childCount);
  }
  return sizes.map((size) => size / total);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function optionalString(value: unknown) {
  return typeof value === "string" && value.length <= 32_768 ? value : undefined;
}

function restorePaneNode(
  value: unknown,
  seenIds: Set<string>,
  leafCount: { value: number },
  depth = 0
): TerminalPaneNode | null {
  if (!isRecord(value) || depth > MAX_LAYOUT_DEPTH) return null;
  const id = value.id;
  if (typeof id !== "string" || !id || seenIds.has(id)) return null;
  seenIds.add(id);

  if (value.type === "local") {
    if (value.profile || value.task) return null;
    if (typeof value.shellType !== "string" || !SHELL_TYPES.has(value.shellType as LocalShellType)) {
      return null;
    }
    leafCount.value += 1;
    if (leafCount.value > MAX_TERMINAL_PANES) return null;
    return {
      id,
      type: "local",
      shellType: value.shellType as LocalShellType,
      customPath: optionalString(value.customPath),
      initialCwd: optionalString(value.initialCwd),
      cwd: optionalString(value.cwd),
      // 不恢复命令和环境变量，避免重放副作用或把敏感值写入未加密快照。
    };
  }

  if (value.type === "ssh") {
    const profile = restoreSshProfile(value.profile);
    if (!profile) return null;
    leafCount.value += 1;
    if (leafCount.value > MAX_TERMINAL_PANES) return null;
    return {
      id,
      type: "ssh",
      profile,
      // 密码和私钥口令只驻留内存。恢复时会优先附加仍存活的 后端会话；
      // 后端进程已重启时由界面提示用户重新输入凭据。
    };
  }

  if (
    value.type !== "split" ||
    (value.direction !== "horizontal" && value.direction !== "vertical") ||
    !Array.isArray(value.children) ||
    value.children.length < 2 ||
    value.children.length > MAX_TERMINAL_PANES
  ) {
    return null;
  }

  const children: TerminalPaneNode[] = [];
  for (const child of value.children) {
    const restoredChild = restorePaneNode(child, seenIds, leafCount, depth + 1);
    if (!restoredChild) return null;
    children.push(restoredChild);
  }
  return {
    id,
    type: "split",
    direction: value.direction,
    children,
    sizes: normalizeSizes(Array.isArray(value.sizes) ? value.sizes : undefined, children.length),
  };
}

function restoreTerminalTab(value: unknown, seenIds: Set<string>): TerminalTab | null {
  if (!isRecord(value) || typeof value.id !== "string" || !value.id || value.id.length > 512) {
    return null;
  }
  // 每个标签用独立的 seen 集合恢复，坏标签不会占用后续标签的 pane id；成功后再合并。
  const tabSeenIds = new Set(seenIds);
  const root = restorePaneNode(value.root, tabSeenIds, { value: 0 });
  if (!root) return null;
  for (const id of tabSeenIds) seenIds.add(id);
  const ids = terminalIds(root);
  const activePaneId =
    typeof value.activePaneId === "string" && ids.includes(value.activePaneId)
      ? value.activePaneId
      : ids[0];
  const title =
    typeof value.title === "string" ? value.title.trim().slice(0, TERMINAL_TAB_TITLE_MAX) : "";
  return title ? { id: value.id, title, root, activePaneId } : { id: value.id, root, activePaneId };
}

/**
 * 校验并恢复一份工作区布局快照；旧版单棵分屏树先升级为单标签。
 * 坏标签丢弃；没有可用标签时返回 null（调用方保持现有布局或默认终端）。
 */
function restoreWorkspaceSnapshot(snapshot: unknown): TerminalWorkspaceSnapshot | null {
  if (!isRecord(snapshot)) return null;
  const version = snapshot.version as number;
  if (!LEGACY_LAYOUT_VERSIONS.includes(version) && version !== LAYOUT_STORAGE_VERSION) return null;
  const upgraded = upgradeLegacyLayout(snapshot, () => nextId("terminal-tab"));
  if (!isRecord(upgraded) || !Array.isArray(upgraded.tabs)) return null;

  const tabs: TerminalTab[] = [];
  const seenPaneIds = new Set<string>();
  const seenTabIds = new Set<string>();
  for (const candidate of upgraded.tabs) {
    if (isRecord(candidate) && typeof candidate.id === "string" && seenTabIds.has(candidate.id)) continue;
    const tab = restoreTerminalTab(candidate, seenPaneIds);
    if (!tab) continue;
    seenTabIds.add(tab.id);
    tabs.push(tab);
    if (tabs.length >= MAX_TERMINAL_TABS) break;
  }
  if (tabs.length === 0) return null;
  const activeTabId =
    typeof upgraded.activeTabId === "string" && seenTabIds.has(upgraded.activeTabId)
      ? upgraded.activeTabId
      : tabs[0].id;
  return { version: LAYOUT_STORAGE_VERSION, tabs, activeTabId };
}

function restorePersistedWorkspace(value: unknown): PersistedTerminalWorkspace | null {
  // Told apart by shape, not version: a layout snapshot v4 has `tabs`, the outer v4 has `current`.
  const legacy = restoreWorkspaceSnapshot(value);
  if (legacy) {
    return {
      version: PERSISTED_WORKSPACE_VERSION,
      current: legacy,
      layoutsByTabId: {},
    };
  }
  if (
    !isRecord(value) ||
    (!LEGACY_PERSISTED_VERSIONS.includes(value.version as number) &&
      value.version !== PERSISTED_WORKSPACE_VERSION)
  ) {
    return null;
  }
  const current = restoreWorkspaceSnapshot(value.current);
  if (!current) return null;
  const layoutsByTabId: Record<string, TerminalWorkspaceSnapshot> = {};
  if (isRecord(value.layoutsByTabId)) {
    for (const [tabId, candidate] of Object.entries(value.layoutsByTabId)) {
      if (!tabId || tabId.length > 512) continue;
      const restored = restoreWorkspaceSnapshot(candidate);
      if (restored) layoutsByTabId[tabId] = restored;
      if (Object.keys(layoutsByTabId).length >= 100) break;
    }
  }
  const activeLayoutTabId =
    typeof value.activeLayoutTabId === "string" && layoutsByTabId[value.activeLayoutTabId]
      ? value.activeLayoutTabId
      : undefined;
  return {
    version: PERSISTED_WORKSPACE_VERSION,
    current,
    activeLayoutTabId,
    layoutsByTabId,
  };
}

function readPersistedWorkspace(): PersistedTerminalWorkspace | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(LAYOUT_STORAGE_KEY);
    if (!raw) return null;
    const restored = restorePersistedWorkspace(JSON.parse(raw));
    if (restored) return restored;
    window.localStorage.removeItem(LAYOUT_STORAGE_KEY);
  } catch {
    try {
      window.localStorage.removeItem(LAYOUT_STORAGE_KEY);
    } catch {
      // WebView 存储不可用时退回到当前进程内状态。
    }
  }
  return null;
}

function persistablePaneNode(node: TerminalPaneNode): TerminalPaneNode {
  if (node.type === "local") {
    return {
      id: node.id,
      type: "local",
      shellType: node.shellType,
      customPath: node.customPath,
      initialCwd: node.initialCwd,
      cwd: node.cwd,
    };
  }
  if (node.type === "ssh") {
    return {
      id: node.id,
      type: "ssh",
      profile: node.profile,
      // credentials 故意不持久化。
    };
  }
  return {
    ...node,
    children: node.children.map(persistablePaneNode),
    sizes: normalizeSizes(node.sizes, node.children.length),
  };
}

function collectLocalPaneIds(node: TerminalPaneNode, ids: Set<string>) {
  if (node.type === "local") {
    ids.add(node.id);
    return;
  }
  if (node.type === "split") {
    for (const child of node.children) collectLocalPaneIds(child, ids);
  }
}

function updateLocalPaneCwdInTree(
  node: TerminalPaneNode,
  paneId: string,
  cwd: string
): TerminalPaneNode {
  if (node.type === "local") {
    return node.id === paneId && node.cwd !== cwd ? { ...node, cwd } : node;
  }
  if (node.type === "ssh") return node;
  const children = node.children.map((child) => updateLocalPaneCwdInTree(child, paneId, cwd));
  return children.some((child, index) => child !== node.children[index])
    ? { ...node, children }
    : node;
}

function splitPaneInTree(
  node: TerminalPaneNode,
  paneId: string,
  direction: TerminalSplitDirection,
  newPane: TerminalLeafPane
): TerminalPaneNode {
  if (isTerminalLeafPane(node)) {
    if (node.id !== paneId) return node;
    return {
      id: nextId("terminal-split"),
      type: "split",
      direction,
      children: [node, newPane],
      sizes: [0.5, 0.5],
    };
  }

  const directIndex = node.children.findIndex(
    (child) => isTerminalLeafPane(child) && child.id === paneId
  );
  if (node.direction === direction && directIndex >= 0) {
    const children = [...node.children];
    children.splice(directIndex + 1, 0, newPane);
    const sizes = normalizeSizes(node.sizes, node.children.length);
    const targetSize = sizes[directIndex];
    sizes.splice(directIndex, 1, targetSize / 2, targetSize / 2);
    return { ...node, children, sizes: normalizeSizes(sizes, children.length) };
  }

  const children = node.children.map((child) => splitPaneInTree(child, paneId, direction, newPane));
  if (children.every((child, index) => child === node.children[index])) return node;
  return { ...node, children };
}

function removePaneFromTree(node: TerminalPaneNode, paneId: string): TerminalPaneNode | null {
  if (isTerminalLeafPane(node)) return node.id === paneId ? null : node;

  const oldSizes = normalizeSizes(node.sizes, node.children.length);
  const childResults = node.children.map((child) => removePaneFromTree(child, paneId));
  const children = childResults.filter((child): child is TerminalPaneNode => child !== null);
  if (children.length === 0) return null;
  if (children.length === 1) return children[0];
  if (childResults.every((child, index) => child === node.children[index])) return node;
  const sizes = oldSizes.filter((_, index) => childResults[index] !== null);
  return { ...node, children, sizes: normalizeSizes(sizes, children.length) };
}

function reloadPaneInTree(node: TerminalPaneNode, paneId: string): TerminalPaneNode {
  if (isTerminalLeafPane(node)) {
    return node.id === paneId ? { ...node, reloadVersion: (node.reloadVersion ?? 0) + 1 } : node;
  }
  const children = node.children.map((child) => reloadPaneInTree(child, paneId));
  if (children.every((child, index) => child === node.children[index])) return node;
  return { ...node, children };
}

function updateSshPaneInTree(
  node: TerminalPaneNode,
  paneId: string,
  options: SshTerminalPaneOptions
): TerminalPaneNode {
  if (isTerminalLeafPane(node)) {
    if (node.id !== paneId || node.type !== "ssh") return node;
    return {
      ...node,
      profile: options.profile,
      credentials: options.credentials,
      reloadVersion: (node.reloadVersion ?? 0) + 1,
    };
  }
  const children = node.children.map((child) => updateSshPaneInTree(child, paneId, options));
  if (children.every((child, index) => child === node.children[index])) return node;
  return { ...node, children };
}

function findPaneInTree(node: TerminalPaneNode, paneId: string): TerminalLeafPane | undefined {
  if (isTerminalLeafPane(node)) return node.id === paneId ? node : undefined;
  for (const child of node.children) {
    const found = findPaneInTree(child, paneId);
    if (found) return found;
  }
  return undefined;
}

function resizeSplitInTree(
  node: TerminalPaneNode,
  splitId: string,
  sizes: readonly number[]
): TerminalPaneNode {
  if (isTerminalLeafPane(node)) return node;
  if (node.id === splitId) {
    return { ...node, sizes: normalizeSizes(sizes, node.children.length) };
  }
  const children = node.children.map((child) => resizeSplitInTree(child, splitId, sizes));
  if (children.every((child, index) => child === node.children[index])) return node;
  return { ...node, children };
}

function singleTabState(pane: TerminalLeafPane): TerminalTabsState {
  const id = nextId("terminal-tab");
  return { tabs: [{ id, root: pane, activePaneId: pane.id }], activeTabId: id };
}

function persistableTab(tab: TerminalTab): TerminalTab {
  const root = persistablePaneNode(tab.root);
  return tab.title
    ? { id: tab.id, title: tab.title, root, activePaneId: tab.activePaneId }
    : { id: tab.id, root, activePaneId: tab.activePaneId };
}

function tabsPaneIds(tabs: readonly TerminalTab[]): string[] {
  return tabs.flatMap((tab) => terminalIds(tab.root));
}

/** 逐个标签套用树变换；没有任何标签变化时返回原数组。 */
function mapTabRoots(
  tabs: TerminalTab[],
  update: (root: TerminalPaneNode) => TerminalPaneNode
): TerminalTab[] {
  let changed = false;
  const next = tabs.map((tab) => {
    const root = update(tab.root);
    if (root === tab.root) return tab;
    changed = true;
    return { ...tab, root };
  });
  return changed ? next : tabs;
}

/** 复制整份工作区布局：每个终端标签和分屏都拿到新 ID，不与源工作区共享会话。 */
function cloneWorkspaceSnapshot(source: TerminalWorkspaceSnapshot): TerminalWorkspaceSnapshot {
  let activeTabId = "";
  const tabs = source.tabs.map((tab) => {
    const cloned = cloneTerminalPaneTree(tab.root, tab.activePaneId, nextId);
    const id = nextId("terminal-tab");
    if (tab.id === source.activeTabId) activeTabId = id;
    return tab.title
      ? { id, title: tab.title, root: cloned.root, activePaneId: cloned.activePaneId }
      : { id, root: cloned.root, activePaneId: cloned.activePaneId };
  });
  return { version: LAYOUT_STORAGE_VERSION, tabs, activeTabId: activeTabId || tabs[0].id };
}

export const useTerminalWorkspaceStore = defineStore("terminal-workspace", () => {
  const restoredWorkspace = readPersistedWorkspace();
  const initialState: TerminalTabsState =
    restoredWorkspace?.current ?? singleTabState(createLocalPane());
  const tabs = ref<TerminalTab[]>(initialState.tabs);
  const activeTabId = ref(initialState.activeTabId);
  const hydrated = ref(false);
  const layoutsByTabId = new Map<string, TerminalWorkspaceSnapshot>(
    Object.entries(restoredWorkspace?.layoutsByTabId ?? {})
  );
  let activeLayoutTabId = restoredWorkspace?.activeLayoutTabId;

  function tabsState(): TerminalTabsState {
    return { tabs: tabs.value, activeTabId: activeTabId.value };
  }

  function setTabsState(state: TerminalTabsState) {
    tabs.value = state.tabs;
    activeTabId.value = state.activeTabId;
  }

  function updateTab(tabId: string, update: (tab: TerminalTab) => TerminalTab) {
    tabs.value = updateTerminalTab(tabsState(), tabId, update).tabs;
  }

  function tabForPane(paneId: string) {
    return terminalTabForPane(tabsState(), paneId);
  }

  function terminalLayoutCacheLimit() {
    return runtimePerformanceSettingsFromSnapshot(readSettingsSnapshot()).terminalLayoutCacheLimit;
  }

  function pruneStoredLayouts() {
    const limit = terminalLayoutCacheLimit();
    // 0 表示不限制：保留所有标签页的布局快照。
    if (limit <= 0) return;
    while (layoutsByTabId.size > limit) {
      const oldestInactiveId = [...layoutsByTabId.keys()].find(
        (tabId) => tabId !== activeLayoutTabId
      );
      if (!oldestInactiveId) break;
      layoutsByTabId.delete(oldestInactiveId);
    }
  }

  function rememberLayout(tabId: string, snapshot: TerminalWorkspaceSnapshot) {
    layoutsByTabId.delete(tabId);
    layoutsByTabId.set(tabId, snapshot);
    pruneStoredLayouts();
  }

  function readLayout(tabId: string | undefined) {
    if (!tabId) return undefined;
    const snapshot = layoutsByTabId.get(tabId);
    if (!snapshot) return undefined;
    // Map 插入顺序作为 LRU；命中后移动到末尾。
    layoutsByTabId.delete(tabId);
    layoutsByTabId.set(tabId, snapshot);
    return snapshot;
  }

  pruneStoredLayouts();
  watch(settingsSnapshotVersion, () => {
    pruneStoredLayouts();
    schedulePersistLayout();
  });

  let persistTimer: ReturnType<typeof setTimeout> | null = null;
  let hydratePromise: Promise<void> | null = null;

  /** 当前终端标签；root / activePaneId 都取自它（只读）。 */
  const activeTab = computed(() => activeTerminalTab(tabsState()));
  const root = computed(() => activeTab.value.root);
  const activePaneId = computed(() => activeTab.value.activePaneId);
  /** 当前终端标签的分屏（可见分屏）。 */
  const paneIds = computed(() => terminalIds(root.value));
  /** 当前工作区全部终端标签的分屏。 */
  const allPaneIds = computed(() => tabsPaneIds(tabs.value));
  const paneCount = computed(() => paneIds.value.length);
  const canSplit = computed(() => paneCount.value < MAX_TERMINAL_PANES);
  const canClose = computed(() => paneCount.value > 1);
  const canAddTab = computed(() => tabs.value.length < MAX_TERMINAL_TABS);

  function activateTab(
    tabId: string,
    initialPane?: LocalTerminalPaneOptions,
    layoutId = tabId,
    fallbackLayoutId?: string
  ) {
    if (!tabId) return false;
    const targetLayoutId = layoutId.trim() || tabId;

    if (activeLayoutTabId && activeLayoutTabId !== targetLayoutId) {
      rememberLayout(activeLayoutTabId, layoutSnapshot());
    }

    activeLayoutTabId = targetLayoutId;
    if (initialPane) {
      setTabsState(singleTabState(createLocalPane(initialPane)));
      layoutsByTabId.delete(targetLayoutId);
      schedulePersistLayout();
      return true;
    }

    const saved = readLayout(targetLayoutId) ?? readLayout(fallbackLayoutId);
    if (saved) {
      setTabsState(saved);
      if (!layoutsByTabId.has(targetLayoutId)) rememberLayout(targetLayoutId, saved);
    }
    schedulePersistLayout();
    return true;
  }

  function hasLayout(tabId: string) {
    return layoutsByTabId.has(tabId) || activeLayoutTabId === tabId;
  }

  function duplicateTab(sourceTabId: string, targetTabId: string) {
    if (!sourceTabId || !targetTabId || sourceTabId === targetTabId) return false;
    const source =
      activeLayoutTabId === sourceTabId ? layoutSnapshot() : layoutsByTabId.get(sourceTabId);
    if (!source) return false;

    rememberLayout(targetTabId, cloneWorkspaceSnapshot(source));
    schedulePersistLayout();
    return true;
  }

  function discardTab(tabId: string) {
    discardTabs([tabId]);
  }

  function discardTabs(tabIds: readonly string[]) {
    for (const tabId of tabIds) {
      layoutsByTabId.delete(tabId);
      if (activeLayoutTabId === tabId) activeLayoutTabId = undefined;
    }
    schedulePersistLayout();
  }

  /** 在全部终端标签中查找；分屏在后台标签时先切换到该标签。 */
  function selectPane(paneId: string) {
    const tab = tabForPane(paneId);
    if (!tab) return;
    setTabsState({
      tabs: updateTerminalTab(tabsState(), tab.id, (current) => ({ ...current, activePaneId: paneId })).tabs,
      activeTabId: tab.id,
    });
  }

  function addTab(pane: TerminalLeafPane): TerminalTab | null {
    if (!canAddTab.value) return null;
    const tab: TerminalTab = { id: nextId("terminal-tab"), root: pane, activePaneId: pane.id };
    const next = insertTerminalTab(tabsState(), tab);
    if (!next) return null;
    setTabsState(next);
    return tab;
  }

  function addLocalTab(options: LocalTerminalPaneOptions = {}) {
    if (!canAddTab.value) return null;
    return addTab(createLocalPane(options));
  }

  function addSshTab(options: SshTerminalPaneOptions) {
    if (!canAddTab.value) return null;
    return addTab(createSshPane(options));
  }

  /** 关闭终端标签，返回需要结束会话的 pane id；最后一个标签或不存在时返回 null。 */
  function closeTab(tabId: string): string[] | null {
    const result = removeTerminalTab(tabsState(), tabId);
    if (!result) return null;
    setTabsState(result.state);
    return result.paneIds;
  }

  function selectTab(tabId: string) {
    setTabsState(selectTerminalTab(tabsState(), tabId));
  }

  function selectAdjacentTab(step: 1 | -1) {
    selectTab(adjacentTerminalTabId(tabsState(), step));
  }

  function renameTab(tabId: string, title: string) {
    tabs.value = renameTerminalTab(tabsState(), tabId, title).tabs;
  }

  function insertPane(paneId: string, direction: TerminalSplitDirection, pane: TerminalLeafPane) {
    const tab = tabForPane(paneId);
    if (!tab || terminalIds(tab.root).length >= MAX_TERMINAL_PANES) return null;
    updateTab(tab.id, (current) => ({
      ...current,
      root: splitPaneInTree(current.root, paneId, direction, pane),
      activePaneId: pane.id,
    }));
    return pane;
  }

  function splitPane(
    paneId: string,
    direction: TerminalSplitDirection,
    options: LocalTerminalPaneOptions = {}
  ) {
    // 未显式指定目录时继承源 Pane 最近记录的工作目录，避免分屏后反复 cd。
    return insertPane(paneId, direction, createLocalPane(inheritSplitCwd(options, getPane(paneId))));
  }

  function splitSshPane(
    paneId: string,
    direction: TerminalSplitDirection,
    options: SshTerminalPaneOptions
  ) {
    return insertPane(paneId, direction, createSshPane(options));
  }

  /** 关闭所在终端标签里的分屏；标签内最后一个分屏不在这里关闭（见 closeTab）。 */
  function closePane(paneId: string) {
    const tab = tabForPane(paneId);
    if (!tab) return false;
    const oldIds = terminalIds(tab.root);
    if (oldIds.length <= 1) return false;
    const oldIndex = oldIds.indexOf(paneId);
    const nextRoot = removePaneFromTree(tab.root, paneId);
    if (!nextRoot) return false;
    updateTab(tab.id, (current) => {
      if (current.activePaneId !== paneId) return { ...current, root: nextRoot };
      const nextIds = terminalIds(nextRoot);
      return {
        ...current,
        root: nextRoot,
        activePaneId: nextIds[Math.min(oldIndex, nextIds.length - 1)] ?? nextIds[0],
      };
    });
    return true;
  }

  function reloadPane(paneId: string) {
    const tab = tabForPane(paneId);
    if (!tab) return;
    updateTab(tab.id, (current) => ({ ...current, root: reloadPaneInTree(current.root, paneId) }));
  }

  function updateSshPane(paneId: string, options: SshTerminalPaneOptions) {
    const tab = tabForPane(paneId);
    const pane = tab ? findPaneInTree(tab.root, paneId) : undefined;
    if (!tab || !pane || pane.type !== "ssh") return false;
    updateTab(tab.id, (current) => ({
      ...current,
      root: updateSshPaneInTree(current.root, paneId, options),
    }));
    return true;
  }

  /** 在全部终端标签中查找。 */
  function getPane(paneId: string) {
    for (const tab of tabs.value) {
      const pane = findPaneInTree(tab.root, paneId);
      if (pane) return pane;
    }
    return undefined;
  }

  /** Pane IDs of every terminal tab in one layout, active or remembered; terminal attention maps panes to tabs with it. */
  function paneIdsForLayout(layoutId: string): readonly string[] {
    if (layoutId === activeLayoutTabId) return allPaneIds.value;
    const snapshot = layoutsByTabId.get(layoutId);
    return snapshot ? tabsPaneIds(snapshot.tabs) : [];
  }

  function localPaneIds() {
    const ids = new Set<string>();
    for (const tab of tabs.value) collectLocalPaneIds(tab.root, ids);
    for (const snapshot of layoutsByTabId.values()) {
      for (const tab of snapshot.tabs) collectLocalPaneIds(tab.root, ids);
    }
    return [...ids];
  }

  function updateLocalPaneCwd(paneId: string, value: string) {
    const cwd = value.trim();
    if (!paneId || !cwd || cwd.length > 32_768) return false;

    const updateRoot = (node: TerminalPaneNode) => updateLocalPaneCwdInTree(node, paneId, cwd);
    let updated = false;
    const nextTabs = mapTabRoots(tabs.value, updateRoot);
    if (nextTabs !== tabs.value) {
      tabs.value = nextTabs;
      updated = true;
    }
    for (const [tabId, snapshot] of layoutsByTabId) {
      const nextSnapshotTabs = mapTabRoots(snapshot.tabs, updateRoot);
      if (nextSnapshotTabs === snapshot.tabs) continue;
      layoutsByTabId.set(tabId, { ...snapshot, tabs: nextSnapshotTabs });
      updated = true;
    }
    if (updated) schedulePersistLayout();
    return updated;
  }

  function updateLocalPaneCwds(cwds: Readonly<Record<string, string>>) {
    let updated = false;
    for (const [paneId, cwd] of Object.entries(cwds)) {
      updated = updateLocalPaneCwd(paneId, cwd) || updated;
    }
    return updated;
  }

  function resizeSplit(splitId: string, sizes: readonly number[]) {
    const nextTabs = mapTabRoots(tabs.value, (node) => resizeSplitInTree(node, splitId, sizes));
    if (nextTabs !== tabs.value) tabs.value = nextTabs;
  }

  function layoutSnapshot(): TerminalWorkspaceSnapshot {
    return {
      version: LAYOUT_STORAGE_VERSION,
      tabs: tabs.value.map(persistableTab),
      activeTabId: activeTab.value.id,
    };
  }

  /** 接受弹窗发送的纯数据快照（含旧版单棵分屏树）；校验失败时保持当前布局不变。 */
  function applyLayoutSnapshot(snapshot: unknown) {
    const restored = restoreWorkspaceSnapshot(snapshot);
    if (!restored) return false;
    setTabsState(restored);
    return true;
  }

  function persistedWorkspaceSnapshot(): PersistedTerminalWorkspace {
    const current = layoutSnapshot();
    if (activeLayoutTabId) rememberLayout(activeLayoutTabId, current);
    return {
      version: PERSISTED_WORKSPACE_VERSION,
      current,
      activeLayoutTabId,
      layoutsByTabId: Object.fromEntries(layoutsByTabId),
    };
  }

  function applyPersistedWorkspace(restored: PersistedTerminalWorkspace) {
    setTabsState(restored.current);
    layoutsByTabId.clear();
    for (const [tabId, snapshot] of Object.entries(restored.layoutsByTabId)) {
      rememberLayout(tabId, snapshot);
    }
    activeLayoutTabId = restored.activeLayoutTabId;
  }

  function hydrate() {
    if (hydrated.value) return Promise.resolve();
    if (hydratePromise) return hydratePromise;
    hydratePromise = (async () => {
      const stored = await loadPersistedValue<unknown>(LAYOUT_STORAGE_KEY);
      const restored = restorePersistedWorkspace(stored);
      if (restored) applyPersistedWorkspace(restored);
      hydrated.value = true;
      const snapshot = persistedWorkspaceSnapshot();
      if (JSON.stringify(stored) !== JSON.stringify(snapshot)) await savePersistedValue(LAYOUT_STORAGE_KEY, snapshot);
    })().finally(() => {
      hydratePromise = null;
    });
    return hydratePromise;
  }

  async function persistLayout() {
    if (persistTimer) {
      clearTimeout(persistTimer);
      persistTimer = null;
    }
    if (!hydrated.value) return;
    await savePersistedValue(LAYOUT_STORAGE_KEY, persistedWorkspaceSnapshot());
  }

  function schedulePersistLayout() {
    if (!hydrated.value) return;
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(() => void persistLayout(), PERSIST_DEBOUNCE_MS);
  }

  watch([tabs, activeTabId], schedulePersistLayout, { deep: true });

  return {
    tabs,
    activeTabId,
    activeTab,
    root,
    activePaneId,
    hydrated,
    hydrate,
    paneIds,
    allPaneIds,
    paneCount,
    canSplit,
    canClose,
    canAddTab,
    activateTab,
    hasLayout,
    duplicateTab,
    discardTab,
    discardTabs,
    selectPane,
    addLocalTab,
    addSshTab,
    closeTab,
    selectTab,
    selectAdjacentTab,
    renameTab,
    splitPane,
    splitSshPane,
    closePane,
    reloadPane,
    updateSshPane,
    getPane,
    localPaneIds,
    paneIdsForLayout,
    updateLocalPaneCwd,
    updateLocalPaneCwds,
    resizeSplit,
    layoutSnapshot,
    applyLayoutSnapshot,
    persistLayout,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useTerminalWorkspaceStore, import.meta.hot));
}
