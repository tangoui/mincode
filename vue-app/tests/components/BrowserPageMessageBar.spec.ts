import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import BrowserPageMessageBar from "@/components/browser/BrowserPageMessageBar.vue";
import { useBrowserPageMessagesStore } from "@/stores/browser-page-messages";

const browser = vi.hoisted(() => ({
  completeEmbeddedBrowserDialog: vi.fn(),
  revealEmbeddedBrowserDownload: vi.fn(),
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

vi.mock("@/services/browser-page-service", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/browser-page-service")>()),
  ...browser,
}));
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  listenAppEvent: (name: string, handler: (payload: unknown) => void) => {
    if (!bridge.handlers.has(name)) bridge.handlers.set(name, new Set());
    bridge.handlers.get(name)!.add(handler);
    return () => bridge.handlers.get(name)?.delete(handler);
  },
}));
vi.mock("@/i18n/messages", () => ({
  useVueI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
  }),
}));

let observedHeight = 40;
class ResizeObserverStub {
  constructor(private readonly callback: ResizeObserverCallback) {}
  observe(target: Element) {
    const height = observedHeight;
    queueMicrotask(() => this.callback([{
      target,
      contentRect: { height } as DOMRectReadOnly,
      borderBoxSize: [{ blockSize: height, inlineSize: 800 }],
    } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver));
  }
  unobserve() {}
  disconnect() {}
}

let wrapper: VueWrapper | undefined;

function confirmDialog(overrides: Record<string, unknown> = {}) {
  return {
    id: "d1",
    tabId: "tab-a",
    kind: "confirm",
    message: "Delete your account?",
    defaultText: "",
    origin: "https://bank.test",
    openedAt: 1,
    ...overrides,
  };
}

function download(overrides: Record<string, unknown> = {}) {
  return {
    id: "dl-1",
    tabId: "tab-a",
    origin: "https://files.test",
    filename: "report.csv",
    state: "saving",
    bytes: 50,
    totalBytes: 200,
    startedAt: 1,
    ...overrides,
  };
}

async function mountBar(tabId = "tab-a") {
  wrapper = mount(BrowserPageMessageBar, { props: { tabId }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

async function emit(name: string, payload: unknown) {
  bridge.emit(name, payload);
  await flushPromises();
}

function bar() {
  return wrapper!.find(".browser-page-message-bar");
}

describe("BrowserPageMessageBar", () => {
  beforeEach(() => {
    observedHeight = 40;
    vi.stubGlobal("ResizeObserver", ResizeObserverStub);
    for (const fn of Object.values(browser)) fn.mockReset().mockResolvedValue(undefined);
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("renders nothing until the page has something to say", async () => {
    await mountBar();
    expect(bar().exists()).toBe(false);
  });

  it("shows a page confirm with its origin and answers OK as the user", async () => {
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog());
    const dialog = wrapper!.find('[role="alertdialog"]');
    expect(dialog.exists()).toBe(true);
    expect(dialog.text()).toContain('browser.pageMessage.dialogTitle:{"origin":"https://bank.test"}');
    expect(dialog.find(".browser-page-message-bar__message").text()).toBe("Delete your account?");
    const labelledBy = dialog.attributes("aria-labelledby")!;
    const describedBy = dialog.attributes("aria-describedby")!;
    expect(wrapper!.find(`#${labelledBy}`).exists()).toBe(true);
    expect(wrapper!.find(`#${describedBy}`).text()).toBe("Delete your account?");

    const ok = wrapper!.find(".browser-page-message-bar__ok");
    expect(ok.element.tagName).toBe("BUTTON");
    await ok.trigger("click");
    await flushPromises();
    expect(browser.completeEmbeddedBrowserDialog).toHaveBeenCalledWith({
      tabId: "tab-a",
      dialogId: "d1",
      accept: true,
      text: "",
    });
  });

  it("answers Cancel and renders page text as text, not HTML", async () => {
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog({ message: "<img src=x onerror=alert(1)>Sure?" }));
    expect(wrapper!.find(".browser-page-message-bar__message img").exists()).toBe(false);
    expect(wrapper!.find(".browser-page-message-bar__message").text()).toBe("<img src=x onerror=alert(1)>Sure?");
    await wrapper!.find(".browser-page-message-bar__cancel").trigger("click");
    await flushPromises();
    expect(browser.completeEmbeddedBrowserDialog).toHaveBeenCalledWith({
      tabId: "tab-a",
      dialogId: "d1",
      accept: false,
      text: "",
    });
  });

  it("prefills a prompt with its default text and sends what the user typed", async () => {
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog({ id: "p1", kind: "prompt", message: "Your name?", defaultText: "guest" }));
    const input = wrapper!.find<HTMLInputElement>(".browser-page-message-bar__prompt input");
    expect(input.element.value).toBe("guest");
    expect(input.attributes("maxlength")).toBe("2000");
    await input.setValue("Ada");
    await wrapper!.find(".browser-page-message-bar__ok").trigger("click");
    await flushPromises();
    expect(browser.completeEmbeddedBrowserDialog).toHaveBeenCalledWith({
      tabId: "tab-a",
      dialogId: "p1",
      accept: true,
      text: "Ada",
    });
  });

  it("offers only OK for an alert", async () => {
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog({ kind: "alert", message: "Saved" }));
    expect(wrapper!.find(".browser-page-message-bar__ok").exists()).toBe(true);
    expect(wrapper!.find(".browser-page-message-bar__cancel").exists()).toBe(false);
  });

  it("names a web page when the dialog has no origin", async () => {
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog({ origin: "" }));
    expect(wrapper!.find('[role="alertdialog"]').text()).toContain("browser.pageMessage.dialogTitleUnknown");
    expect(wrapper!.text()).not.toContain('"origin":""');
  });

  it("disappears when the dialog is closed elsewhere (AI answered)", async () => {
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog());
    expect(bar().exists()).toBe(true);
    await emit("browser:dialog-closed", { tabId: "tab-a", dialogId: "d1", by: "ai", accepted: true });
    expect(bar().exists()).toBe(false);
  });

  it("drops a dialog the AI already answered when the user's answer loses the race", async () => {
    browser.completeEmbeddedBrowserDialog.mockRejectedValueOnce(new Error(JSON.stringify({
      code: "dialog_already_handled",
      message: "this page dialog was already answered or closed",
    })));
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog());
    await wrapper!.find(".browser-page-message-bar__ok").trigger("click");
    await flushPromises();
    expect(bar().exists()).toBe(false);
  });

  it("only shows dialogs of the tab it belongs to", async () => {
    await mountBar("tab-b");
    await emit("browser:dialog-opened", confirmDialog());
    expect(wrapper!.find('[role="alertdialog"]').exists()).toBe(false);
  });

  it("shows only the dialog while a download is also in progress", async () => {
    await mountBar();
    await emit("browser:download-updated", download());
    await emit("browser:file-chooser", { tabId: "tab-a", open: true, selectedCount: 0 });
    await emit("browser:dialog-opened", confirmDialog());
    expect(wrapper!.find('[role="alertdialog"]').exists()).toBe(true);
    expect(wrapper!.find(".browser-page-message-bar__download").exists()).toBe(false);
    expect(wrapper!.find(".browser-page-message-bar__file-chooser").exists()).toBe(false);
  });

  it("shows the file-chooser wait notice ahead of downloads, as a status", async () => {
    await mountBar();
    await emit("browser:download-updated", download());
    await emit("browser:file-chooser", { tabId: "tab-a", open: true, selectedCount: 0 });
    const notice = wrapper!.find(".browser-page-message-bar__file-chooser");
    expect(notice.attributes("role")).toBe("status");
    expect(notice.text()).toContain("browser.pageMessage.fileChooserWaiting");
    expect(wrapper!.find(".browser-page-message-bar__download").exists()).toBe(false);

    await emit("browser:file-chooser", { tabId: "tab-a", open: false, selectedCount: 1 });
    expect(wrapper!.find(".browser-page-message-bar__file-chooser").exists()).toBe(false);
    expect(wrapper!.find(".browser-page-message-bar__download").exists()).toBe(true);
  });

  it("shows download progress, collapses, and reveals a completed file in Finder", async () => {
    await mountBar();
    await emit("browser:download-updated", download());
    const section = wrapper!.find(".browser-page-message-bar__downloads");
    expect(section.attributes("role")).toBe("status");
    expect(section.text()).toContain("report.csv");
    expect(section.text()).toContain("25%");
    expect(wrapper!.find(".browser-page-message-bar__reveal").exists()).toBe(false);

    await emit("browser:download-updated", download({ state: "completed", bytes: 200, finishedAt: 2 }));
    const reveal = wrapper!.find(".browser-page-message-bar__reveal");
    expect(reveal.text()).toContain("browser.pageMessage.revealInFinder");
    await reveal.trigger("click");
    await flushPromises();
    expect(browser.revealEmbeddedBrowserDownload).toHaveBeenCalledWith("dl-1");

    const toggle = wrapper!.find(".browser-page-message-bar__collapse");
    expect(toggle.attributes("aria-expanded")).toBe("true");
    await toggle.trigger("click");
    expect(wrapper!.find(".browser-page-message-bar__collapse").attributes("aria-expanded")).toBe("false");
    expect(wrapper!.find(".browser-page-message-bar__download").exists()).toBe(false);
    expect(wrapper!.find(".browser-page-message-bar__downloads").text()).toContain("report.csv");

    await wrapper!.find(".browser-page-message-bar__dismiss").trigger("click");
    expect(bar().exists()).toBe(false);
  });

  it("does not show downloads the page started without a user action", async () => {
    await mountBar();
    await emit("browser:download-updated", download({ state: "cancelled", error: "no_user_action" }));
    expect(bar().exists()).toBe(false);
  });

  it("explains failed downloads without alarming wording for known codes", async () => {
    await mountBar();
    await emit("browser:download-updated", download({ state: "failed", error: "destination_exists" }));
    expect(wrapper!.find(".browser-page-message-bar__download").text()).toContain("browser.pageMessage.downloadError.destination_exists");
  });

  it("collapses a long page message behind an expand button", async () => {
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog({ message: "x".repeat(600) }));
    const message = wrapper!.find(".browser-page-message-bar__message");
    expect(message.classes()).toContain("is-collapsed");
    const expand = wrapper!.find(".browser-page-message-bar__expand");
    expect(expand.attributes("aria-expanded")).toBe("false");
    await expand.trigger("click");
    expect(wrapper!.find(".browser-page-message-bar__message").classes()).not.toContain("is-collapsed");
    expect(wrapper!.find(".browser-page-message-bar__expand").attributes("aria-expanded")).toBe("true");
  });

  it("moves focus to the primary action when a dialog appears", async () => {
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog());
    await flushPromises();
    expect(document.activeElement).toBe(wrapper!.find(".browser-page-message-bar__ok").element);
  });

  it("focuses the prompt's text box", async () => {
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog({ kind: "prompt", defaultText: "guest" }));
    await flushPromises();
    expect(document.activeElement).toBe(wrapper!.find(".browser-page-message-bar__prompt input").element);
  });

  it("does not steal focus from a text box the user is typing in", async () => {
    const chat = document.createElement("textarea");
    document.body.appendChild(chat);
    chat.focus();
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog());
    await flushPromises();
    expect(document.activeElement).toBe(chat);
  });

  it("reports its measured height and zero once it disappears", async () => {
    await mountBar();
    await emit("browser:dialog-opened", confirmDialog());
    await flushPromises();
    expect(wrapper!.emitted("height")?.at(-1)).toEqual([40]);
    await emit("browser:dialog-closed", { tabId: "tab-a", dialogId: "d1", by: "user", accepted: true });
    await flushPromises();
    expect(wrapper!.emitted("height")?.at(-1)).toEqual([0]);
  });

  it("stops listening to page events after unmount", async () => {
    await mountBar();
    expect(bridge.handlers.get("browser:dialog-opened")?.size).toBe(1);
    wrapper!.unmount();
    wrapper = undefined;
    expect(bridge.handlers.get("browser:dialog-opened")?.size ?? 0).toBe(0);
    expect(useBrowserPageMessagesStore().pendingDialogs).toEqual({});
  });
});
