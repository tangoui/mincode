import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";

export type BrowserOpenRequestStatus =
  | "queued"
  | "opening"
  | "mounted"
  | "navigation_ready"
  | "ready"
  | "failed";

export interface BrowserOpenRequest {
  id: string;
  origin: "user" | "ai";
  url?: string;
  status: BrowserOpenRequestStatus;
  tabId?: string;
  error?: string;
}

export interface BrowserOpenResult {
  requestId: string;
  tabId: string;
  url: string;
}

/** One tab as the browser workspace view knows it; the view stays the source of truth. */
export interface BrowserTabSummary {
  tabId: string;
  title: string;
  url: string;
  mounted: boolean;
  foreground: boolean;
  openerTabId?: string;
}

/** What the mounted browser workspace view lends the store for tab tools. */
export interface BrowserTabSource {
  tabs(): BrowserTabSummary[];
  /** Brings the tab to the front and resolves once the backend has the new AI access. */
  activate(tabId: string): Promise<void>;
}

/** `browser:ai-tab-request`: the backend asks for the tab list or a switch (ACP MCP tools). */
export interface BrowserAiTabRequest {
  requestId: string;
  action: "list" | "switch" | string;
  tabId?: string;
}

function createId() {
  const uuid = globalThis.crypto?.randomUUID?.();
  return uuid ?? `browser-open-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

/**
 * Coordinates the lazy browser workspace with AI/native callers. Requests stay
 * in Pinia until the view consumes them; completion promises live outside the
 * reactive graph so native events can safely arrive before the view mounts.
 */
export const useBrowserWorkspaceStore = defineStore("browser-workspace", () => {
  const pendingOpenRequests = ref<BrowserOpenRequest[]>([]);
  const requestStates = ref<Record<string, BrowserOpenRequest>>({});
  const nativeMounted = ref(false);
  const activeTabId = ref("");
  const activeTabUrl = ref("");
  // 按标签页共享（设计 4.0）：只有集合里的标签页对 AI 可见；用户打开或切换
  // 过去的页默认不共享。aiTargetTabId 是 AI 未指定 tabId 时操作的页，始终
  // 属于共享集合（或为空）。Set 保留插入顺序，目标被取消时回退到最近共享的页。
  const sharedTabIds = ref<Set<string>>(new Set());
  const aiTargetTabId = ref("");
  // 已挂载标签页的最新网址，供 activeBrowserResource 给出目标页（而不是前台页）的网址。
  const tabUrls = ref<Record<string, string>>({});

  const activeBrowserResource = computed(() =>
    aiTargetTabId.value && sharedTabIds.value.has(aiTargetTabId.value)
      ? { tabId: aiTargetTabId.value, url: tabUrls.value[aiTargetTabId.value] ?? "" }
      : null
  );

  // 标签页列表归 BrowserWorkspaceView 所有；视图挂载时把读取和激活能力借给
  // store，避免在 store 里再存一份标题和网址。未挂载时没有任何标签页。
  let tabSource: BrowserTabSource | null = null;

  function registerTabSource(source: BrowserTabSource) {
    tabSource = source;
    return () => {
      if (tabSource === source) tabSource = null;
    };
  }

  /** 当前借给 store 的标签页来源（AI 标签页工具在 services/browser-ai-tabs.ts 里用）。 */
  function currentTabSource() {
    return tabSource;
  }

  const waiters = new Map<string, {
    resolve: (result: BrowserOpenResult) => void;
    reject: (error: Error) => void;
    timer: number;
  }>();

  function queueOpenTab(
    origin: BrowserOpenRequest["origin"],
    url?: string,
    requestId: string = createId()
  ) {
    const existing = requestStates.value[requestId];
    if (existing) return existing;
    const request: BrowserOpenRequest = {
      id: requestId,
      origin,
      status: "queued",
      ...(url?.trim() ? { url: url.trim() } : {}),
    };
    requestStates.value[requestId] = request;
    pendingOpenRequests.value.push(request);
    return request;
  }

  // 用户点击的普通网页不需要等待原生 AI 调用完成，避免加载失败时回流为
  // AI 工具异常；AI 工具请求仍保留 requestId 完成协议。
  function requestUserOpenTab(url?: string) {
    return queueOpenTab("user", url);
  }

  function requestAiOpenTab(url?: string, requestId?: string) {
    return queueOpenTab("ai", url, requestId);
  }

  function consumeOpenRequests() {
    const requests = pendingOpenRequests.value;
    pendingOpenRequests.value = [];
    return requests;
  }

  function updateOpenRequest(
    requestId: string,
    status: BrowserOpenRequestStatus,
    detail: Partial<Pick<BrowserOpenRequest, "tabId" | "url" | "error">> = {}
  ) {
    const current = requestStates.value[requestId];
    if (!current) return;
    requestStates.value[requestId] = { ...current, ...detail, status };
  }

  function waitForOpen(requestId: string, timeoutMs = 25_000) {
    const current = requestStates.value[requestId];
    if (current?.status === "ready" && current.tabId) {
      return Promise.resolve({
        requestId,
        tabId: current.tabId,
        url: current.url ?? "",
      });
    }
    if (current?.status === "failed") {
      return Promise.reject(new Error(current.error || "Failed to open browser tab"));
    }
    return new Promise<BrowserOpenResult>((resolve, reject) => {
      const previous = waiters.get(requestId);
      if (previous) window.clearTimeout(previous.timer);
      const timer = window.setTimeout(() => {
        waiters.delete(requestId);
        reject(new Error("Timed out waiting for the browser tab to become ready"));
      }, timeoutMs);
      waiters.set(requestId, { resolve, reject, timer });
    });
  }

  function completeOpenRequest(result: BrowserOpenResult) {
    updateOpenRequest(result.requestId, "ready", { tabId: result.tabId, url: result.url });
    const waiter = waiters.get(result.requestId);
    if (!waiter) return;
    window.clearTimeout(waiter.timer);
    waiters.delete(result.requestId);
    waiter.resolve(result);
  }

  function failOpenRequest(requestId: string, error: unknown, tabId?: string) {
    const message = error instanceof Error ? error.message : String(error || "Failed to open browser tab");
    updateOpenRequest(requestId, "failed", { error: message, ...(tabId ? { tabId } : {}) });
    const waiter = waiters.get(requestId);
    if (!waiter) return;
    window.clearTimeout(waiter.timer);
    waiters.delete(requestId);
    waiter.reject(new Error(message));
  }

  function setActiveTab(tabId: string, url: string, mounted = nativeMounted.value) {
    activeTabId.value = tabId;
    activeTabUrl.value = url;
    nativeMounted.value = mounted;
    if (mounted && tabId) setTabUrl(tabId, url);
  }

  function setTabUrl(tabId: string, url: string) {
    if (!tabId) return;
    tabUrls.value[tabId] = url;
  }

  function setTabShared(tabId: string, shared: boolean) {
    if (!tabId) return;
    if (shared) {
      sharedTabIds.value.add(tabId);
      if (!aiTargetTabId.value) aiTargetTabId.value = tabId;
      return;
    }
    sharedTabIds.value.delete(tabId);
    if (aiTargetTabId.value === tabId) {
      const remaining = [...sharedTabIds.value];
      aiTargetTabId.value = remaining[remaining.length - 1] ?? "";
    }
  }

  function setAiTarget(tabId: string) {
    if (sharedTabIds.value.has(tabId)) aiTargetTabId.value = tabId;
  }

  function unshareAll() {
    sharedTabIds.value.clear();
    aiTargetTabId.value = "";
  }

  function markTabOpenedByAi(tabId: string) {
    setTabShared(tabId, true);
    setAiTarget(tabId);
  }

  function adoptPopupTab(tab: { tabId: string; openerTabId: string; url: string }) {
    setTabUrl(tab.tabId, tab.url);
    if (!sharedTabIds.value.has(tab.openerTabId)) return;
    markTabOpenedByAi(tab.tabId);
  }

  /** 标签页关闭或原生视图被拆除：取消共享并忘掉它的网址（页面消息在 browser-page-messages store）。 */
  function forgetTab(tabId: string) {
    setTabShared(tabId, false);
    delete tabUrls.value[tabId];
  }

  function setNativeMounted(value: boolean) {
    nativeMounted.value = value;
    if (!value) {
      activeTabId.value = "";
      activeTabUrl.value = "";
    }
  }

  return {
    pendingOpenRequests,
    requestStates,
    nativeMounted,
    activeTabId,
    activeTabUrl,
    sharedTabIds,
    aiTargetTabId,
    tabUrls,
    activeBrowserResource,
    requestUserOpenTab,
    requestAiOpenTab,
    consumeOpenRequests,
    updateOpenRequest,
    waitForOpen,
    completeOpenRequest,
    failOpenRequest,
    setActiveTab,
    setNativeMounted,
    setTabUrl,
    setTabShared,
    setAiTarget,
    unshareAll,
    markTabOpenedByAi,
    adoptPopupTab,
    forgetTab,
    registerTabSource,
    currentTabSource,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useBrowserWorkspaceStore, import.meta.hot));
}
