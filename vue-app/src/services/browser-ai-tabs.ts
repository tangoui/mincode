import { completeEmbeddedBrowserTabRequest } from "@/services/browser-service";
import { sanitizeAiResult, sanitizeUrlForAi } from "@/services/ai-tools/shared";
import type {
  BrowserAiTabRequest,
  BrowserTabSummary,
  useBrowserWorkspaceStore,
} from "@/stores/browser-workspace";

// AI 的标签页工具（browser.list_tabs / browser.switch_tab）。不放进
// browser-workspace store：App.vue 急加载那个 store，而这里要用 AI 的脱敏
// 工具；App.vue 收到 `browser:ai-tab-request` 时才按需加载本模块。

type BrowserWorkspaceStore = ReturnType<typeof useBrowserWorkspaceStore>;

/** A shared tab as AI sees it: sanitized URL, never anything about unshared tabs. */
export interface SharedBrowserTab {
  tabId: string;
  title: string;
  url: string;
  foreground: boolean;
  target: boolean;
  openerTabId?: string;
}

export interface SharedBrowserTabList {
  tabs: SharedBrowserTab[];
  unsharedCount: number;
}

/** Same JSON shape as the backend's structured tool errors, so AI sees the code. */
function tabNotSharedError(tabId: string) {
  return new Error(JSON.stringify({
    code: "browser_tab_not_shared",
    message: "the requested browser tab is not shared with AI; call browser.list_tabs to see the shared tabs",
    details: { tabId },
  }));
}

function sharedTabView(store: BrowserWorkspaceStore, tab: BrowserTabSummary): SharedBrowserTab {
  const opener = tab.openerTabId && store.sharedTabIds.has(tab.openerTabId) ? tab.openerTabId : "";
  return {
    tabId: tab.tabId,
    title: String(sanitizeAiResult(tab.title ?? "")),
    url: sanitizeUrlForAi(tab.url ?? ""),
    foreground: tab.foreground,
    target: store.aiTargetTabId === tab.tabId,
    // 打开者未共享时不提它，免得透露未共享页的 tabId。
    ...(opener ? { openerTabId: opener } : {}),
  };
}

/** 只列出已共享且已挂载的标签页（设计 4.0）；其余只计数，标题和网址不出现。 */
export function listSharedTabs(store: BrowserWorkspaceStore): SharedBrowserTabList {
  const all = store.currentTabSource()?.tabs() ?? [];
  const shared = all.filter((tab) => tab.mounted && store.sharedTabIds.has(tab.tabId));
  return {
    tabs: shared.map((tab) => sharedTabView(store, tab)),
    unsharedCount: all.length - shared.length,
  };
}

/** 把已共享的标签页设为 AI 目标并切到前台；未共享（或不存在）时抛 browser_tab_not_shared。 */
export async function switchAiTarget(store: BrowserWorkspaceStore, rawTabId: string): Promise<SharedBrowserTab> {
  const tabId = typeof rawTabId === "string" ? rawTabId.trim() : "";
  const source = store.currentTabSource();
  const tab = source?.tabs().find((item) => item.tabId === tabId);
  if (!source || !tab?.mounted || !store.sharedTabIds.has(tabId)) throw tabNotSharedError(tabId);
  const previousTarget = store.aiTargetTabId;
  store.setAiTarget(tabId);
  try {
    await source.activate(tabId);
  } catch (error) {
    // 不留半截状态：切换失败时 AI 目标回到原来的页（它若已被取消共享则保持现状）。
    if (store.aiTargetTabId === tabId && previousTarget && store.sharedTabIds.has(previousTarget)) {
      store.setAiTarget(previousTarget);
    }
    throw error;
  }
  const current = source.tabs().find((item) => item.tabId === tabId);
  if (!current || !store.sharedTabIds.has(tabId)) throw tabNotSharedError(tabId);
  return sharedTabView(store, current);
}

/** Answers the backend's `browser:ai-tab-request`; every request gets exactly one completion. */
export async function answerAiTabRequest(store: BrowserWorkspaceStore, request: BrowserAiTabRequest) {
  const requestId = typeof request?.requestId === "string" ? request.requestId.trim() : "";
  if (!requestId) return;
  try {
    let result: unknown;
    if (request.action === "list") result = listSharedTabs(store);
    else if (request.action === "switch") result = await switchAiTarget(store, request.tabId ?? "");
    else throw new Error(`unknown browser tab request action: ${String(request.action)}`);
    await completeEmbeddedBrowserTabRequest({ requestId, result });
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    await completeEmbeddedBrowserTabRequest({ requestId, error: message }).catch(() => undefined);
  }
}

/** The AI tool dependency (`AiToolDependencies.browserTabs`) bound to a store. */
export function browserTabsDependency(store: BrowserWorkspaceStore) {
  return {
    listSharedTabs: () => listSharedTabs(store),
    switchAiTarget: (tabId: string) => switchAiTarget(store, tabId),
  };
}
