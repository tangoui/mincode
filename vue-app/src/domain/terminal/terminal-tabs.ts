import type { TerminalLeafPane, TerminalPaneNode } from "@/domain/terminal/types";

/**
 * 终端工作区内的终端标签模型：每个标签拥有独立的分屏树。
 * 纯函数、不可变更新，不依赖 Vue / DOM / IPC。
 */

export const MAX_TERMINAL_TABS = 12;
export const TERMINAL_TAB_TITLE_MAX = 60;

export interface TerminalTab {
  id: string;
  /** 用户自定义名称；缺省时显示当前分屏名。 */
  title?: string;
  root: TerminalPaneNode;
  activePaneId: string;
}

export interface TerminalTabsState {
  tabs: TerminalTab[];
  activeTabId: string;
}

/** 叶子 pane 的 id，深度优先（与 store 中 terminalIds 同语义）。 */
function leafPanes(node: TerminalPaneNode): TerminalLeafPane[] {
  if (node.type !== "split") return [node];
  return node.children.flatMap(leafPanes);
}

function leafPaneIds(node: TerminalPaneNode): string[] {
  return leafPanes(node).map((pane) => pane.id);
}

/** 标签内全部叶子 pane id（深度优先）。 */
export function terminalTabPaneIds(tab: TerminalTab): string[] {
  return leafPaneIds(tab.root);
}

/** activeTabId 无效时返回 tabs[0]。 */
export function activeTerminalTab(state: TerminalTabsState): TerminalTab {
  return state.tabs.find((tab) => tab.id === state.activeTabId) ?? state.tabs[0]!;
}

function activeTabIndex(state: TerminalTabsState): number {
  const index = state.tabs.findIndex((tab) => tab.id === state.activeTabId);
  return index >= 0 ? index : 0;
}

/** 插在当前标签右侧并选中；已满 MAX_TERMINAL_TABS 个返回 null。 */
export function insertTerminalTab(
  state: TerminalTabsState,
  tab: TerminalTab
): TerminalTabsState | null {
  if (state.tabs.length >= MAX_TERMINAL_TABS) return null;
  const tabs = [...state.tabs];
  tabs.splice(activeTabIndex(state) + 1, 0, tab);
  return { tabs, activeTabId: tab.id };
}

/**
 * 关闭标签并返回其所有叶子 pane id（供调用方结束会话）。
 * 只剩一个或找不到返回 null；关当前标签时选右侧，没有则左侧。
 */
export function removeTerminalTab(
  state: TerminalTabsState,
  tabId: string
): { state: TerminalTabsState; paneIds: string[] } | null {
  if (state.tabs.length <= 1) return null;
  const index = state.tabs.findIndex((tab) => tab.id === tabId);
  if (index < 0) return null;
  const removed = state.tabs[index]!;
  const tabs = state.tabs.filter((_, i) => i !== index);
  let activeTabId = state.activeTabId;
  if (activeTerminalTab(state).id === tabId) {
    activeTabId = (tabs[index] ?? tabs[index - 1])!.id;
  }
  return { state: { tabs, activeTabId }, paneIds: leafPaneIds(removed.root) };
}

/** 不存在则原样返回。 */
export function selectTerminalTab(state: TerminalTabsState, tabId: string): TerminalTabsState {
  if (!state.tabs.some((tab) => tab.id === tabId)) return state;
  return { ...state, activeTabId: tabId };
}

/** 相邻标签 id，首尾循环。 */
export function adjacentTerminalTabId(state: TerminalTabsState, step: 1 | -1): string {
  const count = state.tabs.length;
  const index = (activeTabIndex(state) + step + count) % count;
  return state.tabs[index]!.id;
}

/** trim 后截到 TERMINAL_TAB_TITLE_MAX 个字符；为空则删除 title。 */
export function renameTerminalTab(
  state: TerminalTabsState,
  tabId: string,
  title: string
): TerminalTabsState {
  const normalized = title.trim().slice(0, TERMINAL_TAB_TITLE_MAX);
  return updateTerminalTab(state, tabId, (tab) => {
    if (normalized) return { ...tab, title: normalized };
    const { title: _removed, ...rest } = tab;
    return rest;
  });
}

export function updateTerminalTab(
  state: TerminalTabsState,
  tabId: string,
  update: (tab: TerminalTab) => TerminalTab
): TerminalTabsState {
  return {
    ...state,
    tabs: state.tabs.map((tab) => (tab.id === tabId ? update(tab) : tab)),
  };
}

export function terminalTabForPane(
  state: TerminalTabsState,
  paneId: string
): TerminalTab | undefined {
  return state.tabs.find((tab) => leafPaneIds(tab.root).includes(paneId));
}

/** 分屏属于当前工作区的后台（非当前）终端标签。 */
export function isBackgroundTerminalPane(state: TerminalTabsState, paneId: string): boolean {
  const tab = terminalTabForPane(state, paneId);
  return tab !== undefined && tab.id !== state.activeTabId;
}

/**
 * AI 关闭某分屏时实际该关什么：所在标签有多个分屏 → "pane"；
 * 只有这一个分屏且工作区有多个标签 → "tab"；工作区最后一个分屏或找不到 → null。
 */
export function aiCloseTargetForPane(
  state: TerminalTabsState,
  paneId: string
): "pane" | "tab" | null {
  const tab = terminalTabForPane(state, paneId);
  if (!tab) return null;
  if (leafPaneIds(tab.root).length > 1) return "pane";
  return state.tabs.length > 1 ? "tab" : null;
}

/** title ?? 当前分屏名；无 title 且多分屏时追加 " +N"（N = 分屏数 - 1）。 */
export function terminalTabLabel(
  tab: TerminalTab,
  paneLabel: (pane: TerminalLeafPane) => string
): string {
  if (tab.title) return tab.title;
  const panes = leafPanes(tab.root);
  const active = panes.find((pane) => pane.id === tab.activePaneId) ?? panes[0]!;
  const label = paneLabel(active);
  return panes.length > 1 ? `${label} +${panes.length - 1}` : label;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * 旧版单棵分屏树布局 `{root, activePaneId}` 升级为单标签 `{tabs, activeTabId}`；
 * 其他字段（如 version）原样保留，由调用方决定新版本号。其他值原样返回。
 */
export function upgradeLegacyLayout(raw: unknown, createTabId: () => string): unknown {
  if (!isRecord(raw) || "tabs" in raw || !("root" in raw)) return raw;
  const { root, activePaneId, ...rest } = raw;
  const id = createTabId();
  return { ...rest, tabs: [{ id, root, activePaneId }], activeTabId: id };
}
