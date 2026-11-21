import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import BrowserWorkspaceView from "@/views/BrowserWorkspaceView.vue";
import { useBrowserWorkspaceStore } from "@/stores/browser-workspace";

const browser = vi.hoisted(() => ({
  activateEmbeddedBrowser: vi.fn(),
  completeEmbeddedBrowserDialog: vi.fn(),
  revealEmbeddedBrowserDownload: vi.fn(),
  closeEmbeddedBrowserTab: vi.fn(),
  completeEmbeddedBrowserOpenRequest: vi.fn(),
  evaluateEmbeddedBrowser: vi.fn(),
  getEmbeddedBrowserUrl: vi.fn(),
  hideEmbeddedBrowser: vi.fn(),
  mountEmbeddedBrowser: vi.fn(),
  navigateEmbeddedBrowser: vi.fn(),
  runEmbeddedBrowserCommand: vi.fn(),
  setEmbeddedBrowserAiAccess: vi.fn(),
  syncEmbeddedBrowserBounds: vi.fn(),
  waitEmbeddedBrowserReady: vi.fn(),
}));

vi.mock("@/services/browser-service", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/browser-service")>()),
  ...browser,
}));
// The page message bar's backend calls live in their own module.
vi.mock("@/services/browser-page-service", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/browser-page-service")>()),
  ...browser,
  completeEmbeddedBrowserDialog: browser.completeEmbeddedBrowserDialog,
  revealEmbeddedBrowserDownload: browser.revealEmbeddedBrowserDownload,
}));
const bridge = vi.hoisted(() => {
  const handlers = new Map<string, Set<(payload: unknown) => void>>();
  return {
    handlers,
    emit(name: string, payload: unknown) {
      for (const handler of handlers.get(name) ?? []) handler(payload);
    },
  };
});
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  listenAppEvent: (name: string, handler: (payload: unknown) => void) => {
    if (!bridge.handlers.has(name)) bridge.handlers.set(name, new Set());
    bridge.handlers.get(name)!.add(handler);
    return () => bridge.handlers.get(name)?.delete(handler);
  },
}));
vi.mock("@/i18n/messages", () => ({ useVueI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/composables/use-workspace-state-scope", () => ({
  useWorkspaceStateScope: () => ({ capture: () => ({ scopeId: "scope", workspaceId: "workspace" }) }),
}));
vi.mock("@/services/browser-state-persistence", () => ({
  loadBrowserState: vi.fn().mockResolvedValue(null),
  saveBrowserState: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/ui-session", () => ({ registerUiSessionFlusher: () => () => undefined }));
vi.mock("@/services/web-automation-service", () => ({ openSystemBrowser: vi.fn() }));
vi.mock("@/stores/ai-assistant", async () => {
  const { defineStore } = await import("pinia");
  const { ref } = await import("vue");
  return {
    useAiAssistantStore: defineStore("ai-assistant-test", () => ({
      open: ref(false),
      fullscreen: ref(false),
      width: ref(0),
    })),
  };
});

const BAR_HEIGHT = 40;
const VIEWPORT = { x: 10, y: 82, width: 800, height: 600 };

// The message bar measures itself through ResizeObserver; report 40px for it.
class ResizeObserverStub {
  constructor(private readonly callback: ResizeObserverCallback) {}
  observe(target: Element) {
    if (!target.classList.contains("browser-page-message-bar")) return;
    queueMicrotask(() => this.callback([{
      target,
      contentRect: { height: BAR_HEIGHT } as DOMRectReadOnly,
      borderBoxSize: [{ blockSize: BAR_HEIGHT, inlineSize: VIEWPORT.width }],
    } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver));
  }
  unobserve() {}
  disconnect() {}
}

let wrapper: VueWrapper | undefined;

function tabItems() {
  return wrapper!.findAll(".browser-tabbar__tab");
}

function lastBounds() {
  const calls = browser.syncEmbeddedBrowserBounds.mock.calls;
  return calls[calls.length - 1] as [string, { x: number; y: number; width: number; height: number }] | undefined;
}

async function mountView() {
  wrapper = mount(BrowserWorkspaceView, { attachTo: document.body });
  await flushPromises();
  return useBrowserWorkspaceStore();
}

async function openUserTab(url: string) {
  useBrowserWorkspaceStore().requestUserOpenTab(url);
  await flushPromises();
}

async function emit(name: string, payload: unknown) {
  bridge.emit(name, payload);
  await flushPromises();
  await flushPromises();
}

describe("BrowserWorkspaceView popups and page message bar", () => {
  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", ResizeObserverStub);
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      if (!this.classList.contains("browser-viewport")) return new DOMRect(0, 0, 0, 0);
      return new DOMRect(VIEWPORT.x, VIEWPORT.y, VIEWPORT.width, VIEWPORT.height);
    });
    for (const fn of Object.values(browser)) fn.mockReset().mockResolvedValue(undefined);
    browser.waitEmbeddedBrowserReady.mockImplementation(async (_tabId: string, _timeout: number, url: string) => ({
      readyState: "complete",
      url: url || "https://login.test/",
      title: url || "Login",
    }));
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
    vi.unstubAllGlobals();
  });

  it("adopts a native popup as a new, active tab without creating it again", async () => {
    const store = await mountView();
    await openUserTab("https://app.test/");
    const opener = store.activeTabId;
    store.setTabShared(opener, true);
    await flushPromises();
    expect(tabItems()).toHaveLength(1);
    browser.mountEmbeddedBrowser.mockClear();

    await emit("browser:popup-opened", { tabId: "popup-1", openerTabId: opener, url: "https://login.test/" });
    expect(tabItems()).toHaveLength(2);
    expect(tabItems()[1]!.classes()).toContain("is-active");
    expect(store.activeTabId).toBe("popup-1");
    expect(browser.activateEmbeddedBrowser).toHaveBeenCalledWith("popup-1");
    expect(browser.mountEmbeddedBrowser).not.toHaveBeenCalled();
    expect(lastBounds()?.[0]).toBe("popup-1");
    // Popups of a shared tab are shared and become the AI target (design 4.1).
    expect(store.sharedTabIds.has("popup-1")).toBe(true);
    expect(store.aiTargetTabId).toBe("popup-1");
    expect(tabItems()[1]!.find(".browser-tabbar__share").attributes("aria-pressed")).toBe("true");

    // A duplicate event does not add a second tab.
    await emit("browser:popup-opened", { tabId: "popup-1", openerTabId: opener, url: "https://login.test/" });
    expect(tabItems()).toHaveLength(2);
  });

  it("keeps a popup of an unshared tab private", async () => {
    const store = await mountView();
    await openUserTab("https://app.test/");
    const opener = store.activeTabId;
    await emit("browser:popup-opened", { tabId: "popup-2", openerTabId: opener, url: "https://ads.test/" });
    expect(tabItems()).toHaveLength(2);
    expect(store.sharedTabIds.has("popup-2")).toBe(false);
  });

  it("removes a tab the page closed itself and forgets it", async () => {
    const store = await mountView();
    await openUserTab("https://app.test/");
    const opener = store.activeTabId;
    store.setTabShared(opener, true);
    await emit("browser:popup-opened", { tabId: "popup-1", openerTabId: opener, url: "https://login.test/" });
    expect(store.sharedTabIds.has("popup-1")).toBe(true);

    await emit("browser:tab-closed", { tabId: "popup-1", reason: "window.close" });
    expect(tabItems()).toHaveLength(1);
    expect(store.sharedTabIds.has("popup-1")).toBe(false);
    expect(store.tabUrls["popup-1"]).toBeUndefined();
    expect(browser.closeEmbeddedBrowserTab).not.toHaveBeenCalledWith("popup-1");
    // Focus returns to the opener.
    expect(store.activeTabId).toBe(opener);
    expect(browser.activateEmbeddedBrowser).toHaveBeenLastCalledWith(opener);
  });

  it("shifts the native view below the message bar while it is visible", async () => {
    const store = await mountView();
    await openUserTab("https://app.test/");
    const tabId = store.activeTabId;
    expect(lastBounds()).toEqual([tabId, VIEWPORT]);

    await emit("browser:dialog-opened", {
      id: "d1", tabId, kind: "confirm", message: "Leave?", defaultText: "", origin: "https://app.test", openedAt: 1,
    });
    expect(wrapper!.find(".browser-page-message-bar").exists()).toBe(true);
    expect(lastBounds()).toEqual([tabId, {
      ...VIEWPORT,
      y: VIEWPORT.y + BAR_HEIGHT,
      height: VIEWPORT.height - BAR_HEIGHT,
    }]);
    // The bar is not a modal overlay: the page stays visible.
    expect(browser.hideEmbeddedBrowser).not.toHaveBeenCalled();

    await emit("browser:dialog-closed", { tabId, dialogId: "d1", by: "ai", accepted: true });
    expect(wrapper!.find(".browser-page-message-bar").exists()).toBe(false);
    expect(lastBounds()).toEqual([tabId, VIEWPORT]);
  });

  it("answers the page dialog from the bar", async () => {
    const store = await mountView();
    await openUserTab("https://app.test/");
    const tabId = store.activeTabId;
    await emit("browser:dialog-opened", {
      id: "d1", tabId, kind: "confirm", message: "Leave?", defaultText: "", origin: "https://app.test", openedAt: 1,
    });
    await wrapper!.find(".browser-page-message-bar__ok").trigger("click");
    await flushPromises();
    expect(browser.completeEmbeddedBrowserDialog).toHaveBeenCalledWith({ tabId, dialogId: "d1", accept: true, text: "" });
  });
});
