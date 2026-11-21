<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { storeToRefs } from "pinia";
import {
  ArrowLeft,
  ArrowRight,
  BellFilled,
  Close,
  Delete,
  MagicStick,
  Monitor,
  Plus,
  Promotion,
  Refresh,
  Warning,
} from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import BrowserPageMessageBar from "@/components/browser/BrowserPageMessageBar.vue";
import {
  loadBrowserState,
  saveBrowserState,
  type PersistedBrowserState,
} from "@/services/browser-state-persistence";
import { registerUiSessionFlusher } from "@/services/ui-session";
import { completeEmbeddedBrowserOpenRequest, hideEmbeddedBrowser } from "@/services/browser-service";
import {
  activateEmbeddedBrowser,
  closeEmbeddedBrowserTab,
  evaluateEmbeddedBrowser,
  getEmbeddedBrowserUrl,
  mountEmbeddedBrowser,
  navigateEmbeddedBrowser,
  parseBrowserToolError,
  runEmbeddedBrowserCommand,
  setEmbeddedBrowserAiAccess,
  syncEmbeddedBrowserBounds,
  waitEmbeddedBrowserReady,
} from "@/services/browser-page-service";
import {
  EMBEDDED_BROWSER_OVERLAY_BLOCK_EVENT,
  isEmbeddedBrowserOverlayBlocked,
} from "@/services/browser-overlay-events";
import { openSystemBrowser } from "@/services/web-automation-service";
import { useAiAssistantStore } from "@/stores/ai-assistant";
import { useBrowserWorkspaceStore } from "@/stores/browser-workspace";
import { useBrowserPageMessagesStore } from "@/stores/browser-page-messages";

interface BrowserTab {
  id: string;
  url: string;
  title: string;
  loading: boolean;
  mounted: boolean;
  requestId?: string;
  loadError?: string;
  /** The tab whose page opened this one as a popup (window.open). */
  openerTabId?: string;
}

const { t } = useVueI18n();
const assistant = useAiAssistantStore();
const browserWorkspace = useBrowserWorkspaceStore();
const pageMessages = useBrowserPageMessagesStore();
const workspaceStateScope = useWorkspaceStateScope();
const workspaceScopeId = workspaceStateScope.capture().scopeId;
const { open: assistantOpen, fullscreen: assistantFullscreen, width: assistantWidth } = storeToRefs(assistant);
const { sharedTabIds, aiTargetTabId } = storeToRefs(browserWorkspace);
const { pendingDialogs } = storeToRefs(pageMessages);
const viewport = ref<HTMLElement>();
const devtoolsOpening = ref(false);
const systemBrowserOpening = ref(false);
const MAX_PERSISTED_BROWSER_TABS = 50;
let sequence = 0;
const tabs = ref<BrowserTab[]>([newTab()]);
const activeTabId = ref(tabs.value[0]!.id);
const address = ref(tabs.value[0]!.url);
let observer: ResizeObserver | undefined;
// 页面消息栏的实际高度：栏可见时原生视图的上边缘下移这么多（设计 4.2）。
const messageBarHeight = ref(0);
let nativeEventUnsubscribers: Array<() => void> = [];
let overlayObserver: MutationObserver | undefined;
let overlaySyncFrame: number | undefined;
let pollTimer: number | undefined;
let urlSyncInFlight = false;
let browserSessionHydrated = false;
let browserPersistTimer: ReturnType<typeof setTimeout> | undefined;

const activeTab = computed(
  () => tabs.value.find((tab) => tab.id === activeTabId.value) ?? tabs.value[0]!
);
const activeTabMounted = computed(() => activeTab.value.mounted);
// 总开关：任一标签页共享即为开启；关闭时取消全部共享。
const aiShareEnabled = computed(() => sharedTabIds.value.size > 0);

function newTab(initialUrl?: string, requestId?: string): BrowserTab {
  sequence += 1;
  return {
    id: `browser-${Date.now()}-${sequence}`,
    url: normalizeUrl(initialUrl ?? ""),
    title: t("browser.newTab"),
    loading: false,
    mounted: false,
    ...(requestId ? { requestId } : {}),
  };
}

function normalizeUrl(value: string) {
  const trimmed = value.trim();
  if (!trimmed) return "https://www.google.com";
  if (/^[a-z][a-z\d+.-]*:\/\//i.test(trimmed)) return trimmed;
  if (/^(localhost|127\.0\.0\.1)(:\d+)?(\/.*)?$/i.test(trimmed)) return `http://${trimmed}`;
  if (/^[\w.-]+\.[a-z]{2,}(\/.*)?$/i.test(trimmed)) return `https://${trimmed}`;
  return `https://www.google.com/search?q=${encodeURIComponent(trimmed)}`;
}

function restoreBrowserSession(value: unknown) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const stored = value as Partial<PersistedBrowserState>;
  if (stored.version !== 1 || !Array.isArray(stored.tabs)) return false;
  const restored: BrowserTab[] = [];
  let restoredActiveId = "";
  for (const item of stored.tabs.slice(0, MAX_PERSISTED_BROWSER_TABS)) {
    if (!item || typeof item !== "object" || Array.isArray(item)) continue;
    const savedId = typeof item.id === "string" ? item.id : "";
    const url = typeof item.url === "string" ? item.url.slice(0, 32_768) : "";
    const tab = newTab(url);
    tab.title =
      typeof item.title === "string" && item.title.trim()
        ? item.title.trim().slice(0, 512)
        : t("browser.newTab");
    restored.push(tab);
    if (savedId && savedId === stored.activeTabId) restoredActiveId = tab.id;
  }
  if (!restored.length) return false;
  tabs.value = restored;
  activeTabId.value = restoredActiveId || restored[0]!.id;
  address.value = activeTab.value.url;
  return true;
}

function browserSessionSnapshot(): PersistedBrowserState {
  return {
    version: 1,
    tabs: tabs.value.slice(0, MAX_PERSISTED_BROWSER_TABS).map((tab) => ({
      id: tab.id,
      url: tab.url,
      title: tab.title,
    })),
    activeTabId: activeTabId.value,
  };
}

async function hydrateBrowserSession() {
  try {
    restoreBrowserSession(await loadBrowserState(workspaceScopeId));
  } catch {
    // Keep a fresh browser tab when the encrypted state is unavailable.
  } finally {
    browserSessionHydrated = true;
  }
}

async function persistBrowserSession() {
  if (browserPersistTimer) clearTimeout(browserPersistTimer);
  browserPersistTimer = undefined;
  if (!browserSessionHydrated) return;
  await saveBrowserState(workspaceScopeId, browserSessionSnapshot());
}

function scheduleBrowserSessionPersist() {
  if (!browserSessionHydrated) return;
  if (browserPersistTimer) clearTimeout(browserPersistTimer);
  browserPersistTimer = setTimeout(() => {
    browserPersistTimer = undefined;
    void persistBrowserSession();
  }, 300);
}

async function flushBrowserSession() {
  await syncCurrentUrl().catch(() => undefined);
  await persistBrowserSession();
}

const unregisterBrowserSessionFlusher = registerUiSessionFlusher(flushBrowserSession);

watch(
  () => ({
    activeTabId: activeTabId.value,
    tabs: tabs.value.map((tab) => ({ id: tab.id, url: tab.url, title: tab.title })),
  }),
  scheduleBrowserSessionPersist,
  { deep: true }
);

function modalOverlayVisible() {
  return Array.from(document.querySelectorAll<HTMLElement>(".el-overlay")).some((overlay) => {
    const style = window.getComputedStyle(overlay);
    return overlay.getClientRects().length > 0 && style.visibility !== "hidden";
  });
}

function currentBounds() {
  const rect = viewport.value?.getBoundingClientRect();
  if (!rect) throw new Error("Browser viewport is not ready");
  const drawerWidth = assistantOpen.value ? assistantWidth.value : 0;
  const visibleRight = Math.max(rect.left, window.innerWidth - drawerWidth);
  // 原生 WKWebView 叠在 HTML 之上，栏必须留在它的矩形之外。
  const barHeight = Math.min(messageBarHeight.value, rect.height);
  return {
    x: rect.x,
    y: rect.y + barHeight,
    width: Math.max(0, Math.min(rect.right, visibleRight) - rect.left),
    height: Math.max(0, rect.height - barHeight),
  };
}

function handleMessageBarHeight(height: number) {
  if (messageBarHeight.value === height) return;
  messageBarHeight.value = height;
  scheduleSync();
}

async function sync() {
  if (!activeTab.value.mounted || !viewport.value) return;
  if (assistantFullscreen.value || modalOverlayVisible() || isEmbeddedBrowserOverlayBlocked()) {
    await hideEmbeddedBrowser();
    return;
  }
  await syncEmbeddedBrowserBounds(activeTab.value.id, currentBounds());
}

function scheduleSync() {
  if (overlaySyncFrame !== undefined) window.cancelAnimationFrame(overlaySyncFrame);
  overlaySyncFrame = window.requestAnimationFrame(() => {
    overlaySyncFrame = undefined;
    void sync();
  });
}

function observeElementPlusOverlays(root: Document | Element) {
  const overlays: Element[] = [];
  if (root instanceof Element && root.matches(".el-overlay")) overlays.push(root);
  overlays.push(...root.querySelectorAll(".el-overlay"));
  for (const overlay of overlays) {
    overlayObserver?.observe(overlay, {
      attributes: true,
      attributeFilter: ["class", "style"],
    });
  }
}

function handleOverlayMutations(records: MutationRecord[]) {
  for (const record of records) {
    for (const node of record.addedNodes) {
      if (node instanceof Element) observeElementPlusOverlays(node);
    }
  }
  scheduleSync();
}

function handleSidebarTooltip() {
  scheduleSync();
}

watch([assistantOpen, assistantFullscreen, assistantWidth], () => {
  void nextTick(scheduleSync);
}, { flush: "post" });

function applyReadyState(tab: BrowserTab, state: { url: string; title: string }) {
  if (state.url) tab.url = state.url;
  tab.title = state.title?.trim() || hostnameTitle(tab.url);
  if (tab.mounted) browserWorkspace.setTabUrl(tab.id, tab.url);
  if (tab.id === activeTabId.value) {
    address.value = tab.url;
    browserWorkspace.setActiveTab(tab.id, tab.url, true);
  }
}

function hostnameTitle(url: string) {
  try {
    return new URL(url).hostname || url;
  } catch {
    return url;
  }
}

type AiAccessState = [sharedTabIds: string[], targetTabId: string];

function aiAccessKey() {
  return JSON.stringify([[...sharedTabIds.value], aiTargetTabId.value]);
}

// 后端最后一次确认过的共享状态；只有调用成功才更新，绝不提前当作已发送。
const confirmedAiAccessKey = ref(JSON.stringify([[], ""]));
const aiAccessSyncFailing = ref(false);
const AI_ACCESS_RETRY_DELAYS_MS = [250, 1000, 3000];
// browser.switch_tab 只给一次快速重试，并且最多等这么久：前端必须在 Go 的
// 5 秒 tab-request 超时之前回答，不能让后端先报超时、前端随后才切换成功。
const SWITCH_SYNC_RETRY_DELAYS_MS = [250];
const SWITCH_SYNC_BUDGET_MS = 3000;
let aiAccessQueue: Promise<void> = Promise.resolve();

/** 期望状态比后端已确认的更窄（有页被取消共享，或目标被清空）。 */
function narrowerThanConfirmed(desiredKey: string) {
  const [desiredShared, desiredTarget] = JSON.parse(desiredKey) as AiAccessState;
  const [confirmedShared, confirmedTarget] = JSON.parse(confirmedAiAccessKey.value) as AiAccessState;
  return confirmedShared.some((tabId) => !desiredShared.includes(tabId))
    || (Boolean(confirmedTarget) && !desiredTarget);
}

// 推送失败且后端仍比界面更宽松时常驻提示，直到同步成功（fail closed：
// 不能让界面显示“已取消共享”而后端仍放行）。
const aiAccessUnsynced = computed(
  () => aiAccessSyncFailing.value && narrowerThanConfirmed(aiAccessKey())
);

/**
 * 把 store 里的共享集合和目标页推给后端。所有调用串行排队，与后端已确认的
 * 状态比较去重；失败时保留期望状态，短暂退避后有限次重试，每次都发送最新的
 * 期望状态。由下方 watch 统一触发；AI 打开请求完成前也会显式 await 一次，
 * 保证后端在 AI 收到 tabId 之前就已放行该页。
 */
function pushAiAccess(retryDelays: readonly number[] = AI_ACCESS_RETRY_DELAYS_MS) {
  aiAccessQueue = aiAccessQueue.then(async () => {
    let failures = 0;
    for (;;) {
      const key = aiAccessKey();
      if (key === confirmedAiAccessKey.value || !isDesktopRuntime()) {
        aiAccessSyncFailing.value = false;
        return;
      }
      const [shared, target] = JSON.parse(key) as AiAccessState;
      try {
        await setEmbeddedBrowserAiAccess(shared, target);
        confirmedAiAccessKey.value = key;
        aiAccessSyncFailing.value = false;
      } catch (error) {
        aiAccessSyncFailing.value = true;
        const retryDelay = retryDelays[failures];
        failures += 1;
        if (retryDelay === undefined) {
          ElMessage.error(parseBrowserToolError(error).message || t("browser.ai.shareFailed"));
          return;
        }
        await new Promise((resolve) => window.setTimeout(resolve, retryDelay));
      }
    }
  });
  return aiAccessQueue;
}

watch(aiAccessKey, () => void pushAiAccess());

function isTabShared(tab: BrowserTab) {
  return sharedTabIds.value.has(tab.id);
}

/** 页面弹框等待处理：标签栏上提示用户该切到哪一页（后台页尤其需要）。 */
function hasPendingDialog(tab: BrowserTab) {
  return Boolean(pendingDialogs.value[tab.id]);
}

/**
 * browser.switch_tab：把已共享的页切到前台，并等后端拿到新的目标页再返回，
 * 这样 AI 的下一次调用（含 ACP 不带 tabId 的调用）就落在这一页上。
 */
async function activateTabForAi(tabId: string) {
  const tab = tabs.value.find((item) => item.id === tabId);
  if (!tab?.mounted) throw new Error("the browser tab is no longer open");
  const previous = activeTab.value;
  try {
    await selectBrowserTab(tab, false, true);
  } catch (error) {
    // 原生视图没切过去：标签栏回到原来的前台页，不谎报已切换。
    if (previous !== tab && tabs.value.includes(previous) && activeTabId.value === tab.id) {
      activeTabId.value = previous.id;
      address.value = previous.url;
      if (previous.mounted) browserWorkspace.setActiveTab(previous.id, previous.url, true);
      scheduleSync();
    }
    const failure = error instanceof Error ? error : new Error(String(error));
    failure.message = `could not bring the browser tab to the front: ${failure.message}`;
    throw failure;
  }
  if (!(await confirmAiAccessWithin(SWITCH_SYNC_BUDGET_MS))) {
    throw new Error("could not sync the AI target with the browser backend; try browser.switch_tab again");
  }
}

/** 推送当前共享状态并在 budget 内等后端确认；超时或推送失败都返回 false。 */
async function confirmAiAccessWithin(budgetMs: number) {
  if (!isDesktopRuntime()) return true;
  let timer: number | undefined;
  await Promise.race([
    pushAiAccess(SWITCH_SYNC_RETRY_DELAYS_MS),
    new Promise<void>((resolve) => { timer = window.setTimeout(resolve, budgetMs); }),
  ]);
  window.clearTimeout(timer);
  return confirmedAiAccessKey.value === aiAccessKey();
}

const unregisterTabSource = browserWorkspace.registerTabSource({
  tabs: () => tabs.value.map((tab) => ({
    tabId: tab.id,
    title: tab.title,
    url: tab.url,
    mounted: tab.mounted,
    foreground: tab.id === activeTabId.value,
    ...(tab.openerTabId ? { openerTabId: tab.openerTabId } : {}),
  })),
  activate: activateTabForAi,
});

function toggleTabShared(tab: BrowserTab) {
  if (!tab.mounted) return;
  browserWorkspace.setTabShared(tab.id, !isTabShared(tab));
}

/** 取消共享并忘掉该页的网址和页面消息。 */
function forgetTabState(tabId: string) {
  browserWorkspace.forgetTab(tabId);
  pageMessages.forgetTab(tabId);
}

/** 原生视图被拆除（关闭、加载失败、改用系统浏览器）时不再共享该页。 */
function releaseTab(tab: BrowserTab) {
  forgetTabState(tab.id);
}

async function finishOpenRequest(tab: BrowserTab, error?: unknown) {
  if (!tab.requestId) return;
  const requestId = tab.requestId;
  tab.requestId = undefined;
  if (error) {
    const message = error instanceof Error ? error.message : String(error);
    browserWorkspace.failOpenRequest(requestId, message, tab.id);
    await completeEmbeddedBrowserOpenRequest({ requestId, tabId: tab.id, error: message }).catch(() => undefined);
    return;
  }
  // AI 打开的页自动共享并成为目标页（设计 4.0）。
  browserWorkspace.setTabUrl(tab.id, tab.url);
  browserWorkspace.markTabOpenedByAi(tab.id);
  await pushAiAccess();
  browserWorkspace.completeOpenRequest({ requestId, tabId: tab.id, url: tab.url });
  await completeEmbeddedBrowserOpenRequest({ requestId, tabId: tab.id, url: tab.url }).catch(() => undefined);
}

async function ensureTabMounted(tab: BrowserTab) {
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    throw new Error(t("common.desktopRequired"));
  }
  if (tab.mounted) {
    await activateEmbeddedBrowser(tab.id);
    await sync();
    return;
  }
  await nextTick();
  tab.loading = true;
  tab.loadError = undefined;
  if (tab.requestId) browserWorkspace.updateOpenRequest(tab.requestId, "opening", { tabId: tab.id });
  try {
    await mountEmbeddedBrowser(tab.id, tab.url, currentBounds());
    tab.mounted = true;
    // The bounds were measured before the mount; a resize while it was in flight was
    // skipped by sync() (not mounted yet), so measure again now.
    scheduleSync();
    browserWorkspace.setActiveTab(tab.id, tab.url, true);
    if (tab.requestId) browserWorkspace.updateOpenRequest(tab.requestId, "mounted", { tabId: tab.id });
    try {
      const state = await waitEmbeddedBrowserReady(tab.id, 15_000, tab.url);
      applyReadyState(tab, state);
      if (tab.requestId) browserWorkspace.updateOpenRequest(tab.requestId, "navigation_ready", { tabId: tab.id, url: tab.url });
    } catch (loadError) {
      // 页面没有在超时前就绪（目标失效、网络不可达等）。像普通浏览器一样保留
      // 标签页：收起原生 webview 遮罩，在工作区内渲染加载失败面板，而不是把
      // 底层超时错误抛给调用方弹报错；打开请求的等待方仍会收到失败结果。
      const detail = loadError instanceof Error ? loadError.message : String(loadError || "");
      await closeEmbeddedBrowserTab(tab.id).catch(() => undefined);
      tab.mounted = false;
      tab.loadError = detail;
      tab.title = hostnameTitle(tab.url);
      releaseTab(tab);
      if (tab.id === activeTabId.value) browserWorkspace.setNativeMounted(false);
      await hideEmbeddedBrowser().catch(() => undefined);
      await finishOpenRequest(tab, loadError);
      return;
    }
    await finishOpenRequest(tab);
  } catch (error) {
    await finishOpenRequest(tab, error);
    throw error;
  } finally {
    tab.loading = false;
  }
}

async function retryFailedLoad() {
  const tab = activeTab.value;
  if (!tab.loadError) return;
  tab.loadError = undefined;
  try {
    await ensureTabMounted(tab);
  } catch (error) {
    tab.loadError = error instanceof Error ? error.message : String(error);
  }
}

async function navigateEmbedded(value = address.value, tab = activeTab.value) {
  const target = normalizeUrl(value);
  const previousUrl = tab.url;
  tab.url = target;
  if (tab.id === activeTabId.value) address.value = target;
  if (!tab.mounted) {
    await ensureTabMounted(tab);
    return;
  }
  tab.loading = true;
  try {
    await navigateEmbeddedBrowser(tab.id, target);
    const state = await waitEmbeddedBrowserReady(tab.id, 15_000, target, previousUrl);
    applyReadyState(tab, state);
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : t("browser.navigationFailed"));
    throw error;
  } finally {
    tab.loading = false;
  }
}

async function openInSystemBrowser(value = address.value, tab = activeTab.value) {
  if (systemBrowserOpening.value) return;
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  const target = normalizeUrl(value);
  systemBrowserOpening.value = true;
  try {
    if (tab.mounted) {
      await closeEmbeddedBrowserTab(tab.id).catch(() => undefined);
      tab.mounted = false;
      releaseTab(tab);
      if (tab.id === activeTabId.value) browserWorkspace.setNativeMounted(false);
    }
    tab.url = target;
    tab.title = hostnameTitle(target);
    address.value = target;
    await hideEmbeddedBrowser().catch(() => undefined);
    await openSystemBrowser(target);
    ElMessage.success(t("browser.systemOpened"));
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : t("browser.systemOpenFailed"));
  } finally {
    systemBrowserOpening.value = false;
  }
}

async function openEmbeddedPreview() {
  try {
    await navigateEmbedded(address.value, activeTab.value);
    await activateEmbeddedBrowser(activeTab.value.id);
    scheduleSync();
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : t("browser.navigationFailed"));
  }
}

async function syncCurrentUrl() {
  if (urlSyncInFlight || document.visibilityState !== "visible") return;
  const tab = activeTab.value;
  if (!tab.mounted) return;
  urlSyncInFlight = true;
  try {
    const current = await getEmbeddedBrowserUrl(tab.id);
    if (!current || tab.id !== activeTabId.value) return;
    // 原生 webview 在网络连接失败或首帧导航期间会短暂回报 about:blank。
    // 此时仍应保留用户输入的目标地址，等真实页面就绪后再同步 URL。
    if (tab.loading && current === "about:blank") return;
    tab.url = current;
    tab.title = hostnameTitle(current);
    browserWorkspace.setActiveTab(tab.id, current, true);
    if (document.activeElement?.getAttribute("data-browser-address") !== "true") {
      address.value = current;
    }
  } catch {
    // The native overlay can be temporarily unavailable while switching workspaces.
  } finally {
    urlSyncInFlight = false;
  }
}

function stopUrlPolling() {
  if (pollTimer !== undefined) window.clearInterval(pollTimer);
  pollTimer = undefined;
}

function startUrlPolling() {
  stopUrlPolling();
  if (document.visibilityState !== "visible") return;
  pollTimer = window.setInterval(() => void syncCurrentUrl(), 1500);
}

function handleDocumentVisibilityChange() {
  if (document.visibilityState === "visible") {
    startUrlPolling();
    scheduleSync();
    void syncCurrentUrl();
    return;
  }
  stopUrlPolling();
}

async function historyCommand(direction: "back" | "forward") {
  await ensureTabMounted(activeTab.value);
  await evaluateEmbeddedBrowser(
    activeTab.value.id,
    direction === "back" ? "history.back(); true" : "history.forward(); true"
  );
  window.setTimeout(() => void syncCurrentUrl(), 300);
}

async function addBrowserTab(initialUrl?: string, mountImmediately = false, requestId?: string) {
  let tab: BrowserTab;
  const initialTab = tabs.value.length === 1 ? tabs.value[0] : undefined;
  if (mountImmediately && initialTab && !initialTab.mounted && !initialTab.requestId) {
    tab = initialTab;
    tab.url = normalizeUrl(initialUrl ?? "");
    tab.title = t("browser.newTab");
    tab.requestId = requestId;
  } else {
    tabs.value.push(newTab(initialUrl, requestId));
    tab = tabs.value[tabs.value.length - 1]!;
  }
  // 仅 AI 打开的标签会在就绪后（finishOpenRequest）自动共享；普通用户点击
  // 同样立即加载，但默认不共享，也不进入 AI 请求完成通道。
  await selectBrowserTab(tab, mountImmediately);
  return tab;
}

/** rethrow：AI 切换需要知道原生激活失败，而不是只给用户弹提示。 */
async function selectBrowserTab(tab: BrowserTab, mountForAutomation = false, rethrow = false) {
  activeTabId.value = tab.id;
  address.value = tab.url;
  try {
    if (mountForAutomation) await ensureTabMounted(tab);
    if (tab.mounted) {
      await activateEmbeddedBrowser(tab.id);
      browserWorkspace.setActiveTab(tab.id, tab.url, true);
    } else {
      await hideEmbeddedBrowser().catch(() => undefined);
      browserWorkspace.setNativeMounted(false);
    }
    // 切换标签页不改变共享状态，也不改变 AI 的目标页（设计 4.0）。
    scheduleSync();
  } catch (error) {
    if (rethrow) throw error;
    if (!tab.requestId) {
      ElMessage.error(error instanceof Error ? error.message : t("browser.navigationFailed"));
    }
  }
}

async function consumeRequestedTabs() {
  const requests = browserWorkspace.consumeOpenRequests();
  for (const request of requests) {
    await addBrowserTab(request.url, true, request.origin === "ai" ? request.id : undefined);
  }
}

watch(
  () => browserWorkspace.pendingOpenRequests.length,
  () => void consumeRequestedTabs(),
  { flush: "post" }
);

interface NativePopupOpened {
  tabId: string;
  openerTabId: string;
  url: string;
}

/**
 * 接管原生已创建（并隐藏）的弹出窗口：作为已挂载的标签页加在打开者后面并激活，
 * 不再调用 mount 创建第二个原生视图；打开者已共享时它也共享并成为 AI 目标页（设计 4.1）。
 */
async function adoptNativePopup(event: NativePopupOpened) {
  const popupTabId = typeof event?.tabId === "string" ? event.tabId.trim() : "";
  if (!popupTabId || tabs.value.some((tab) => tab.id === popupTabId)) return;
  const url = typeof event.url === "string" ? event.url : "";
  const openerIndex = tabs.value.findIndex((tab) => tab.id === event.openerTabId);
  tabs.value.splice(openerIndex >= 0 ? openerIndex + 1 : tabs.value.length, 0, {
    id: popupTabId,
    url,
    title: (url && hostnameTitle(url)) || t("browser.newTab"),
    loading: false,
    mounted: true,
    ...(typeof event.openerTabId === "string" && event.openerTabId ? { openerTabId: event.openerTabId } : {}),
  });
  const tab = tabs.value.find((item) => item.id === popupTabId)!;
  browserWorkspace.adoptPopupTab({ tabId: popupTabId, openerTabId: event.openerTabId, url });
  await selectBrowserTab(tab);
  try {
    const state = await waitEmbeddedBrowserReady(popupTabId, 15_000);
    if (tabs.value.includes(tab) && tab.mounted) applyReadyState(tab, state);
  } catch {
    // The popup may still be loading or already closed; URL polling catches up.
  }
}

/** 网页自己关闭了标签页（window.close）：原生视图已不在，只需移除并忘掉它。 */
async function handleNativeTabClosed(event: { tabId: string }) {
  const closedTabId = typeof event?.tabId === "string" ? event.tabId : "";
  if (!closedTabId) return;
  const tab = tabs.value.find((item) => item.id === closedTabId);
  if (!tab) {
    forgetTabState(closedTabId);
    return;
  }
  tab.mounted = false;
  await closeBrowserTab(closedTabId);
}

function subscribeNativeTabEvents() {
  if (!isDesktopRuntime()) return;
  nativeEventUnsubscribers = [
    listenAppEvent<NativePopupOpened>("browser:popup-opened", (event) => void adoptNativePopup(event)),
    listenAppEvent<{ tabId: string }>("browser:tab-closed", (event) => void handleNativeTabClosed(event)),
  ];
}

async function closeBrowserTab(tabId: string) {
  const index = tabs.value.findIndex((tab) => tab.id === tabId);
  if (index < 0) return;
  const tab = tabs.value[index]!;
  if (tab.mounted) await closeEmbeddedBrowserTab(tab.id).catch(() => undefined);
  if (tab.requestId) await finishOpenRequest(tab, new Error("Browser tab was closed before it became ready"));
  releaseTab(tab);
  tabs.value.splice(index, 1);
  if (tabs.value.length === 0) tabs.value.push(newTab());
  if (activeTabId.value === tabId) {
    await selectBrowserTab(tabs.value[Math.max(0, index - 1)]!);
  }
}

async function reloadActiveTab() {
  await ensureTabMounted(activeTab.value);
  activeTab.value.loading = true;
  try {
    await runEmbeddedBrowserCommand("embedded_browser_reload", activeTab.value.id);
    applyReadyState(activeTab.value, await waitEmbeddedBrowserReady(activeTab.value.id));
  } finally {
    activeTab.value.loading = false;
  }
}

async function toggleDevtools() {
  if (devtoolsOpening.value) return;
  devtoolsOpening.value = true;
  try {
    await ensureTabMounted(activeTab.value);
    await runEmbeddedBrowserCommand("embedded_browser_toggle_devtools", activeTab.value.id);
  } catch {
    ElMessage.error(t("browser.devtoolsFailed"));
  } finally {
    devtoolsOpening.value = false;
  }
}

async function clearBrowsingData() {
  await runEmbeddedBrowserCommand("embedded_browser_clear_browsing_data");
  ElMessage.success(t("browser.clearDataDone"));
}

// 总开关开启：共享当前前台页并设为 AI 目标（与按页共享之前的行为一致）；
// 关闭：取消全部标签页的共享。
function toggleAiShare(value: string | number | boolean) {
  if (!value) {
    browserWorkspace.unshareAll();
    return;
  }
  if (!activeTab.value.mounted) return;
  browserWorkspace.setTabShared(activeTab.value.id, true);
  browserWorkspace.setAiTarget(activeTab.value.id);
}

onMounted(async () => {
  window.addEventListener("resize", scheduleSync);
  document.addEventListener("visibilitychange", handleDocumentVisibilityChange);
  window.addEventListener(EMBEDDED_BROWSER_OVERLAY_BLOCK_EVENT, handleSidebarTooltip);
  observer = new ResizeObserver(scheduleSync);
  if (viewport.value) observer.observe(viewport.value);
  overlayObserver = new MutationObserver(handleOverlayMutations);
  overlayObserver.observe(document.body, { childList: true });
  observeElementPlusOverlays(document);
  subscribeNativeTabEvents();
  startUrlPolling();
  await hydrateBrowserSession();
  await nextTick();
  const requests = browserWorkspace.consumeOpenRequests();
  if (requests.length) {
    for (const request of requests) {
      await addBrowserTab(request.url, true, request.origin === "ai" ? request.id : undefined);
    }
  }
});

onBeforeUnmount(() => {
  unregisterBrowserSessionFlusher();
  unregisterTabSource();
  for (const unsubscribe of nativeEventUnsubscribers) unsubscribe();
  nativeEventUnsubscribers = [];
  void persistBrowserSession();
  observer?.disconnect();
  overlayObserver?.disconnect();
  window.removeEventListener("resize", scheduleSync);
  document.removeEventListener("visibilitychange", handleDocumentVisibilityChange);
  window.removeEventListener(EMBEDDED_BROWSER_OVERLAY_BLOCK_EVENT, handleSidebarTooltip);
  if (overlaySyncFrame !== undefined) window.cancelAnimationFrame(overlaySyncFrame);
  stopUrlPolling();
  browserWorkspace.unshareAll();
  for (const tab of tabs.value) forgetTabState(tab.id);
  browserWorkspace.setNativeMounted(false);
  void pushAiAccess();
  if (isDesktopRuntime()) {
    void hideEmbeddedBrowser();
    for (const tab of tabs.value) {
      if (tab.requestId) {
        void finishOpenRequest(tab, new Error("Browser workspace closed before the tab became ready"));
      }
      if (tab.mounted) void closeEmbeddedBrowserTab(tab.id);
    }
  }
});

</script>

<template>
  <section class="browser-workspace browser-workspace--original">
    <el-scrollbar class="browser-tabbar">
      <div class="browser-tabbar__content">
      <div
        v-for="tab in tabs"
        :key="tab.id"
        role="presentation"
        class="browser-tabbar__tab"
        :class="{ 'is-active': activeTabId === tab.id }"
      >
        <button
          type="button"
          class="browser-tabbar__select"
          :aria-current="activeTabId === tab.id ? 'page' : undefined"
          @click="selectBrowserTab(tab)"
        >
          <span class="browser-tabbar__favicon">◉</span>
          <span class="browser-tabbar__title">{{ tab.title }}</span>
          <el-icon
            v-if="hasPendingDialog(tab)"
            class="browser-tabbar__dialog"
            role="img"
            :aria-label="t('browser.pageMessage.dialogWaiting')"
            :title="t('browser.pageMessage.dialogWaiting')"
          >
            <BellFilled />
          </el-icon>
          <i v-if="tab.loading" class="browser-tabbar__loading" />
        </button>
        <el-button
          v-if="tab.mounted"
          text
          size="small"
          class="browser-tabbar__share"
          :class="{ 'is-shared': isTabShared(tab), 'is-target': aiTargetTabId === tab.id }"
          :icon="MagicStick"
          :aria-pressed="isTabShared(tab) ? 'true' : 'false'"
          :aria-label="t(isTabShared(tab) ? 'browser.ai.unshareTab' : 'browser.ai.shareTab')"
          :title="t(isTabShared(tab) ? 'browser.ai.unshareTab' : 'browser.ai.shareTab')"
          @click="toggleTabShared(tab)"
        />
        <el-button
          text
          size="small"
          class="browser-tabbar__close"
          :icon="Close"
          :aria-label="t('browser.closeTab')"
          :title="t('browser.closeTab')"
          @click="closeBrowserTab(tab.id)"
        />
      </div>
      <el-button
        text
        class="browser-tabbar__add"
        :icon="Plus"
        :aria-label="t('browser.newTab')"
        @click="addBrowserTab()"
      />
      </div>
    </el-scrollbar>

    <header class="browser-toolbar browser-toolbar--original">
      <el-button
        text
        :icon="ArrowLeft"
        :aria-label="t('browser.back')"
        :disabled="!activeTabMounted"
        @click="historyCommand('back')"
      />
      <el-button
        text
        :icon="ArrowRight"
        :aria-label="t('browser.forward')"
        :disabled="!activeTabMounted"
        @click="historyCommand('forward')"
      />
      <el-button
        text
        :icon="Refresh"
        :loading="activeTab.loading"
        :disabled="!activeTabMounted"
        :aria-label="t('browser.reload')"
        @click="reloadActiveTab"
      />
      <el-input
        v-model="address"
        data-browser-address="true"
        :placeholder="t('browser.address')"
        @keyup.enter="openInSystemBrowser()"
      />
      <el-button
        class="browser-toolbar__go"
        type="primary"
        size="small"
        :icon="Promotion"
        :loading="systemBrowserOpening"
        @click="openInSystemBrowser()"
      >
        {{ t("browser.openSystem") }}
      </el-button>
      <el-button
        class="browser-toolbar__preview"
        size="small"
        :icon="Monitor"
        :disabled="systemBrowserOpening"
        @click="openEmbeddedPreview"
      >
        {{ t("browser.openEmbedded") }}
      </el-button>
      <div class="browser-toolbar__ai-share">
        <el-switch
          :model-value="aiShareEnabled"
          size="small"
          :disabled="!activeTabMounted && !aiShareEnabled"
          :aria-label="t('browser.aiShare')"
          @change="toggleAiShare"
        />
        <el-tooltip
          :content="t('browser.aiShareHint')"
          placement="top"
          :popper-options="{ modifiers: [{ name: 'flip', enabled: false }] }"
          popper-class="browser-ai-share-tooltip"
          :show-after="400"
          :teleported="false"
        >
          <span :class="{ 'is-shared': aiShareEnabled }">
            {{ t(aiShareEnabled ? "browser.aiShared" : "browser.aiNotShared") }}
          </span>
        </el-tooltip>
        <el-button
          v-if="aiAccessUnsynced"
          text
          type="warning"
          size="small"
          class="browser-toolbar__ai-unsynced"
          :icon="Warning"
          :title="t('browser.ai.notSyncedHint')"
          @click="pushAiAccess()"
        >
          {{ t("browser.ai.notSynced") }}
        </el-button>
      </div>
      <el-button
        text
        :icon="Monitor"
        :loading="devtoolsOpening"
        :disabled="!activeTabMounted || devtoolsOpening"
        :aria-label="t('browser.devtools')"
        @click="toggleDevtools"
      >
        {{ t("browser.devtools") }}
      </el-button>
      <el-button
        text
        :icon="Delete"
        :title="t('browser.clearData')"
        :aria-label="t('browser.clearData')"
        @click="clearBrowsingData"
      />
    </header>

    <div ref="viewport" class="browser-viewport">
      <BrowserPageMessageBar
        :tab-id="activeTabMounted ? activeTab.id : ''"
        @height="handleMessageBarHeight"
      />
      <div v-if="!activeTabMounted" class="browser-placeholder">
        <template v-if="activeTab.loadError">
          <el-icon :size="38" class="browser-placeholder__error-icon"><Monitor /></el-icon>
          <strong>{{ t("browser.loadFailedTitle") }}</strong>
          <p class="browser-placeholder__url">{{ activeTab.url }}</p>
          <p>{{ t("browser.loadFailedHint") }}</p>
          <div class="browser-placeholder__actions">
            <el-button type="primary" :icon="Refresh" @click="retryFailedLoad">
              {{ t("browser.loadFailedRetry") }}
            </el-button>
            <el-button :icon="Promotion" :loading="systemBrowserOpening" @click="openInSystemBrowser()">
              {{ t("browser.openSystem") }}
            </el-button>
          </div>
        </template>
        <template v-else>
          <el-icon :size="38"><Monitor /></el-icon>
          <strong>{{ t("browser.systemDefault") }}</strong>
          <p>{{ t("browser.systemDefaultHint") }}</p>
          <div class="browser-placeholder__actions">
            <el-button type="primary" :icon="Promotion" :loading="systemBrowserOpening" @click="openInSystemBrowser()">
              {{ t("browser.openSystem") }}
            </el-button>
            <el-button :icon="Monitor" @click="openEmbeddedPreview">
              {{ t("browser.openEmbedded") }}
            </el-button>
          </div>
        </template>
      </div>
    </div>
  </section>
</template>
