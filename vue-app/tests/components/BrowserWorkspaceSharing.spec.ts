import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import BrowserWorkspaceView from "@/views/BrowserWorkspaceView.vue";
import { useBrowserWorkspaceStore } from "@/stores/browser-workspace";
import { useBrowserPageMessagesStore } from "@/stores/browser-page-messages";
import { parseBrowserToolError } from "@/services/browser-page-service";
import { answerAiTabRequest, listSharedTabs, switchAiTarget } from "@/services/browser-ai-tabs";

const browser = vi.hoisted(() => ({
  activateEmbeddedBrowser: vi.fn(),
  closeEmbeddedBrowserTab: vi.fn(),
  completeEmbeddedBrowserOpenRequest: vi.fn(),
  completeEmbeddedBrowserTabRequest: vi.fn(),
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
vi.mock("@/services/browser-page-service", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/browser-page-service")>()),
  ...browser,
}));
vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => true, listenAppEvent: () => () => undefined }));
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

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}

let wrapper: VueWrapper | undefined;

function lastAccess() {
  const calls = browser.setEmbeddedBrowserAiAccess.mock.calls;
  return calls[calls.length - 1] as [string[], string] | undefined;
}

function tabItems() {
  return wrapper!.findAll(".browser-tabbar__tab");
}

function tabButtons() {
  return tabItems().map((item) => item.find(".browser-tabbar__select"));
}

function shareToggle(index: number) {
  return tabItems()[index]!.find(".browser-tabbar__share");
}

function unsyncedIndicator() {
  return wrapper!.find(".browser-toolbar__ai-unsynced");
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

async function setGlobalShare(on: boolean) {
  const toggle = wrapper!.find(".browser-toolbar__ai-share .el-switch");
  const checked = toggle.classes().includes("is-checked");
  if (checked !== on) await toggle.trigger("click");
  await flushPromises();
}

describe("BrowserWorkspaceView per-tab AI sharing", () => {
  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", ResizeObserverStub);
    for (const fn of Object.values(browser)) fn.mockReset().mockResolvedValue(undefined);
    browser.waitEmbeddedBrowserReady.mockImplementation(async (_tabId: string, _timeout: number, url: string) => ({
      readyState: "complete",
      url,
      title: url,
    }));
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
    vi.unstubAllGlobals();
  });

  it("does not share a tab just because the user opened or switched to it", async () => {
    const store = await mountView();
    await openUserTab("https://a.test/");
    await setGlobalShare(true);
    const tabA = store.activeTabId;
    expect(lastAccess()).toEqual([[tabA], tabA]);

    await openUserTab("https://b.test/");
    const tabB = store.activeTabId;
    expect(tabB).not.toBe(tabA);
    expect(lastAccess()![0]).not.toContain(tabB);

    await tabButtons()[0]!.trigger("click");
    await flushPromises();
    await tabButtons()[1]!.trigger("click");
    await flushPromises();
    expect(lastAccess()).toEqual([[tabA], tabA]);
    expect(shareToggle(1).attributes("aria-pressed")).toBe("false");
    expect(shareToggle(1).attributes("aria-label")).toBe("browser.ai.shareTab");
  });

  it("shares a tab when its tab-bar toggle is pressed and unshares it again", async () => {
    const store = await mountView();
    await openUserTab("https://a.test/");
    await setGlobalShare(true);
    const tabA = store.activeTabId;
    await openUserTab("https://b.test/");
    const tabB = store.activeTabId;

    await shareToggle(1).trigger("click");
    await flushPromises();
    expect(lastAccess()![0]).toEqual([tabA, tabB]);
    // Sharing another tab does not steal the AI's current target.
    expect(lastAccess()![1]).toBe(tabA);
    expect(shareToggle(1).attributes("aria-pressed")).toBe("true");
    expect(shareToggle(1).attributes("aria-label")).toBe("browser.ai.unshareTab");

    await shareToggle(0).trigger("click");
    await flushPromises();
    expect(lastAccess()).toEqual([[tabB], tabB]);
  });

  it("unshares every tab when the toolbar switch is turned off", async () => {
    const store = await mountView();
    await openUserTab("https://a.test/");
    await setGlobalShare(true);
    await openUserTab("https://b.test/");
    await shareToggle(1).trigger("click");
    await flushPromises();
    expect(store.sharedTabIds.size).toBe(2);

    await setGlobalShare(false);
    expect(lastAccess()).toEqual([[], ""]);
    expect(store.activeBrowserResource).toBeNull();
  });

  it("shares AI-opened tabs and pushes access before completing the open request", async () => {
    const store = await mountView();
    store.requestAiOpenTab("https://ai.test/", "req-1");
    await flushPromises();
    const tabId = store.requestStates["req-1"]!.tabId!;
    expect(store.aiTargetTabId).toBe(tabId);
    expect(lastAccess()).toEqual([[tabId], tabId]);
    expect(store.activeBrowserResource).toEqual({ tabId, url: "https://ai.test/" });

    const accessOrder = browser.setEmbeddedBrowserAiAccess.mock.invocationCallOrder.at(-1)!;
    const completeOrder = browser.completeEmbeddedBrowserOpenRequest.mock.invocationCallOrder.at(-1)!;
    expect(accessOrder).toBeLessThan(completeOrder);
  });

  it("renders the share and close controls as real buttons beside the tab button", async () => {
    await mountView();
    await openUserTab("https://a.test/");
    const item = tabItems()[0]!;
    const share = item.find(".browser-tabbar__share");
    expect(share.element.tagName).toBe("BUTTON");
    expect(share.attributes("aria-pressed")).toBe("false");
    const select = item.find(".browser-tabbar__select");
    expect(select.element.tagName).toBe("BUTTON");
    expect(select.element.contains(share.element)).toBe(false);
    const close = item.find('button[aria-label="browser.closeTab"]');
    expect(close.exists()).toBe(true);
    expect(select.element.contains(close.element)).toBe(false);

    await share.trigger("click");
    await flushPromises();
    expect(item.find(".browser-tabbar__share").attributes("aria-pressed")).toBe("true");
  });

  it("retries a failed narrowing push and shows a not-synced indicator until it lands", async () => {
    await mountView();
    await openUserTab("https://a.test/");
    await setGlobalShare(true);
    expect(unsyncedIndicator().exists()).toBe(false);
    const before = browser.setEmbeddedBrowserAiAccess.mock.calls.length;

    browser.setEmbeddedBrowserAiAccess.mockRejectedValueOnce(new Error("backend down"));
    await setGlobalShare(false);
    expect(browser.setEmbeddedBrowserAiAccess.mock.calls.length).toBe(before + 1);
    expect(unsyncedIndicator().exists()).toBe(true);

    await new Promise((resolve) => setTimeout(resolve, 400));
    await flushPromises();
    expect(browser.setEmbeddedBrowserAiAccess.mock.calls.length).toBe(before + 2);
    expect(lastAccess()).toEqual([[], ""]);
    expect(unsyncedIndicator().exists()).toBe(false);
  });

  it("does not flag a failed widening push as a permission risk", async () => {
    await mountView();
    await openUserTab("https://a.test/");
    browser.setEmbeddedBrowserAiAccess.mockRejectedValueOnce(new Error("backend down"));
    await setGlobalShare(true);
    const tabA = useBrowserWorkspaceStore().activeTabId;
    expect(unsyncedIndicator().exists()).toBe(false);

    await new Promise((resolve) => setTimeout(resolve, 400));
    await flushPromises();
    expect(lastAccess()).toEqual([[tabA], tabA]);
    expect(unsyncedIndicator().exists()).toBe(false);
  });

  it("stops sharing a tab when it is closed", async () => {
    const store = await mountView();
    await openUserTab("https://a.test/");
    await setGlobalShare(true);
    const tabA = store.activeTabId;
    await tabItems()[0]!.find('button[aria-label="browser.closeTab"]').trigger("click");
    await flushPromises();
    expect(store.sharedTabIds.has(tabA)).toBe(false);
    expect(lastAccess()).toEqual([[], ""]);
  });
});

describe("BrowserWorkspaceView shared tab list and switching", () => {
  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", ResizeObserverStub);
    for (const fn of Object.values(browser)) fn.mockReset().mockResolvedValue(undefined);
    browser.waitEmbeddedBrowserReady.mockImplementation(async (_tabId: string, _timeout: number, url: string) => ({
      readyState: "complete",
      url,
      title: `Title of ${new URL(url).hostname}`,
    }));
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
    vi.unstubAllGlobals();
  });

  /** Three user tabs a, b, c; a and c are shared (a is the AI target), c is in front. */
  async function threeTabsTwoShared() {
    const store = await mountView();
    await openUserTab("https://a.test/?token=secret-a");
    await setGlobalShare(true);
    const tabA = store.activeTabId;
    await openUserTab("https://private-b.test/inbox");
    const tabB = store.activeTabId;
    await openUserTab("https://c.test/");
    const tabC = store.activeTabId;
    await shareToggle(2).trigger("click");
    await flushPromises();
    return { store, tabA, tabB, tabC };
  }

  it("lists only shared tabs and counts the rest without revealing them", async () => {
    const { store, tabA, tabC } = await threeTabsTwoShared();
    const listed = listSharedTabs(store);
    expect(listed.unsharedCount).toBe(1);
    expect(listed.tabs).toEqual([
      { tabId: tabA, title: "Title of a.test", url: "https://a.test/?token=%5BREDACTED%5D", foreground: false, target: true },
      { tabId: tabC, title: "Title of c.test", url: "https://c.test/", foreground: true, target: false },
    ]);
    const text = JSON.stringify(listed);
    expect(text).not.toContain("private-b");
    expect(text).not.toContain("secret-a");
  });

  it("refuses to switch to an unshared tab and changes nothing", async () => {
    const { store, tabA, tabB, tabC } = await threeTabsTwoShared();
    browser.activateEmbeddedBrowser.mockClear();
    const error = await switchAiTarget(store, tabB).then(() => null, (reason: unknown) => reason);
    expect(parseBrowserToolError(error).code).toBe("browser_tab_not_shared");
    expect(String((error as Error).message)).not.toContain("private-b");
    await expect(switchAiTarget(store, "no-such-tab")).rejects.toThrow(/browser_tab_not_shared/);
    expect(store.aiTargetTabId).toBe(tabA);
    expect(store.activeTabId).toBe(tabC);
    expect(browser.activateEmbeddedBrowser).not.toHaveBeenCalled();
  });

  it("brings the switched-to tab to the front and makes it the synced AI target", async () => {
    const { store, tabA, tabC } = await threeTabsTwoShared();
    await tabButtons()[1]!.trigger("click");
    await flushPromises();
    const switched = await switchAiTarget(store, tabC);
    // The backend has the new target by the time the switch resolves.
    expect(lastAccess()).toEqual([[tabA, tabC], tabC]);
    await flushPromises();
    expect(switched).toMatchObject({ tabId: tabC, foreground: true, target: true });
    expect(store.activeTabId).toBe(tabC);
    expect(store.aiTargetTabId).toBe(tabC);
    expect(browser.activateEmbeddedBrowser).toHaveBeenLastCalledWith(tabC);
    expect(lastAccess()).toEqual([[tabA, tabC], tabC]);
    expect(tabItems()[2]!.classes()).toContain("is-active");
    expect(listSharedTabs(store).tabs.find((tab) => tab.tabId === tabC)).toMatchObject({ foreground: true, target: true });
  });

  it("fails the switch and restores the target when native activation fails", async () => {
    const { store, tabA, tabC } = await threeTabsTwoShared();
    await tabButtons()[0]!.trigger("click");
    await flushPromises();
    expect(store.activeTabId).toBe(tabA);
    browser.activateEmbeddedBrowser.mockRejectedValueOnce(new Error("native activate failed"));
    await expect(switchAiTarget(store, tabC)).rejects.toThrow(/front/);
    await flushPromises();
    expect(store.aiTargetTabId).toBe(tabA);
    expect(lastAccess()).toEqual([[tabA, tabC], tabA]);
    // The tab bar does not claim a tab is in front when the native view never switched.
    expect(tabItems()[0]!.classes()).toContain("is-active");
  });

  it("fails the switch and restores the target when the backend never confirms it", async () => {
    const { store, tabA, tabC } = await threeTabsTwoShared();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      browser.setEmbeddedBrowserAiAccess.mockRejectedValue(new Error("backend down"));
      let settled: "pending" | "resolved" | Error = "pending";
      const switching = switchAiTarget(store, tabC).then(
        () => { settled = "resolved"; },
        (error: Error) => { settled = error; },
      );
      // The answer must reach Go well inside its 5 s tab-request timeout.
      await vi.advanceTimersByTimeAsync(3500);
      await switching;
      expect(settled).toBeInstanceOf(Error);
      expect((settled as unknown as Error).message).toMatch(/sync/);
      expect(store.aiTargetTabId).toBe(tabA);
      await vi.advanceTimersByTimeAsync(10_000);
    } finally {
      vi.useRealTimers();
    }
  });

  it("answers backend tab requests for list and switch", async () => {
    const { store, tabB, tabC } = await threeTabsTwoShared();
    await answerAiTabRequest(store, { requestId: "r1", action: "list" });
    expect(browser.completeEmbeddedBrowserTabRequest).toHaveBeenLastCalledWith({
      requestId: "r1",
      result: listSharedTabs(store),
    });
    await answerAiTabRequest(store, { requestId: "r2", action: "switch", tabId: tabB });
    const failed = browser.completeEmbeddedBrowserTabRequest.mock.calls.at(-1)![0] as { requestId: string; error: string };
    expect(failed.requestId).toBe("r2");
    expect(parseBrowserToolError(failed.error).code).toBe("browser_tab_not_shared");
    await answerAiTabRequest(store, { requestId: "r3", action: "switch", tabId: tabC });
    expect(browser.completeEmbeddedBrowserTabRequest).toHaveBeenLastCalledWith({
      requestId: "r3",
      result: expect.objectContaining({ tabId: tabC, target: true }),
    });
    await answerAiTabRequest(store, { requestId: "r4", action: "explode" });
    expect(browser.completeEmbeddedBrowserTabRequest).toHaveBeenLastCalledWith({
      requestId: "r4",
      error: expect.stringContaining("explode"),
    });
  });

  it("marks tabs whose page is waiting on a dialog", async () => {
    const { tabB } = await threeTabsTwoShared();
    expect(wrapper!.findAll(".browser-tabbar__dialog")).toHaveLength(0);
    useBrowserPageMessagesStore().recordDialogOpened({
      id: "d1", tabId: tabB, kind: "confirm", message: "Leave?", defaultText: "", origin: "https://private-b.test", openedAt: 1,
    });
    await flushPromises();
    const marker = tabItems()[1]!.find(".browser-tabbar__dialog");
    expect(marker.exists()).toBe(true);
    expect(marker.attributes("aria-label")).toBe("browser.pageMessage.dialogWaiting");
    expect(marker.attributes("title")).toBe("browser.pageMessage.dialogWaiting");
    expect(wrapper!.findAll(".browser-tabbar__dialog")).toHaveLength(1);
    useBrowserPageMessagesStore().recordDialogClosed({ tabId: tabB, dialogId: "d1" });
    await flushPromises();
    expect(wrapper!.findAll(".browser-tabbar__dialog")).toHaveLength(0);
  });
});

describe("browser workspace tab list without a mounted workspace", () => {
  it("reports no tabs and refuses switches", async () => {
    const store = useBrowserWorkspaceStore();
    expect(listSharedTabs(store)).toEqual({ tabs: [], unsharedCount: 0 });
    await expect(switchAiTarget(store, "t1")).rejects.toThrow(/browser_tab_not_shared/);
  });

  it("names a popup's opener only when the opener is shared too", () => {
    const store = useBrowserWorkspaceStore();
    const summary = (tabId: string, openerTabId?: string) => ({
      tabId, title: tabId, url: `https://${tabId}.test/`, mounted: true, foreground: false, ...(openerTabId ? { openerTabId } : {}),
    });
    const release = store.registerTabSource({
      tabs: () => [summary("opener"), summary("private"), summary("popup", "opener"), summary("popup-2", "private")],
      activate: async () => undefined,
    });
    store.setTabShared("opener", true);
    store.setTabShared("popup", true);
    store.setTabShared("popup-2", true);
    const tabs = listSharedTabs(store).tabs;
    expect(tabs.find((tab) => tab.tabId === "popup")?.openerTabId).toBe("opener");
    expect(tabs.find((tab) => tab.tabId === "popup-2")).not.toHaveProperty("openerTabId");
    expect(JSON.stringify(tabs)).not.toContain("private");
    release();
    expect(listSharedTabs(store)).toEqual({ tabs: [], unsharedCount: 0 });
  });
});

describe("browser workspace sharing store", () => {
  it("markTabOpenedByAi shares the tab and makes it the AI target", () => {
    const store = useBrowserWorkspaceStore();
    store.setTabShared("t1", true);
    store.markTabOpenedByAi("t2");
    expect([...store.sharedTabIds]).toEqual(["t1", "t2"]);
    expect(store.aiTargetTabId).toBe("t2");
  });

  it("setAiTarget only accepts shared tabs", () => {
    const store = useBrowserWorkspaceStore();
    store.setTabShared("t1", true);
    store.setAiTarget("t9");
    expect(store.aiTargetTabId).toBe("t1");
    store.setTabShared("t2", true);
    store.setAiTarget("t2");
    expect(store.aiTargetTabId).toBe("t2");
  });

  it("adoptPopupTab shares the popup only when its opener is shared", () => {
    const store = useBrowserWorkspaceStore();
    store.setTabShared("opener", true);
    store.adoptPopupTab({ tabId: "popup", openerTabId: "opener", url: "https://login.test/" });
    expect(store.sharedTabIds.has("popup")).toBe(true);
    expect(store.aiTargetTabId).toBe("popup");
    expect(store.activeBrowserResource).toEqual({ tabId: "popup", url: "https://login.test/" });

    store.adoptPopupTab({ tabId: "popup-2", openerTabId: "private", url: "https://x.test/" });
    expect(store.sharedTabIds.has("popup-2")).toBe(false);
    expect(store.aiTargetTabId).toBe("popup");
  });

  it("unsharing the target falls back to another shared tab", () => {
    const store = useBrowserWorkspaceStore();
    store.setTabShared("t1", true);
    store.markTabOpenedByAi("t2");
    store.setTabShared("t2", false);
    expect(store.aiTargetTabId).toBe("t1");
    store.unshareAll();
    expect(store.sharedTabIds.size).toBe(0);
    expect(store.aiTargetTabId).toBe("");
  });
});

describe("parseBrowserToolError", () => {
  it("reads structured tool errors", () => {
    const error = new Error(JSON.stringify({
      code: "browser_tab_not_shared",
      message: "tab is not shared",
      details: { tabId: "t1" },
    }));
    expect(parseBrowserToolError(error)).toEqual({
      code: "browser_tab_not_shared",
      message: "tab is not shared",
      details: { tabId: "t1" },
    });
  });

  it("keeps plain errors as messages", () => {
    expect(parseBrowserToolError(new Error("boom"))).toEqual({ message: "boom" });
    expect(parseBrowserToolError("{not json")).toEqual({ message: "{not json" });
    expect(parseBrowserToolError('{"foo":1}')).toEqual({ message: '{"foo":1}' });
  });
});
