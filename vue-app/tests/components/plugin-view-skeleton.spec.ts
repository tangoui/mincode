import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import PluginViewSkeleton from "@/components/plugins/PluginViewSkeleton.vue";
import { setPluginViewTitles } from "@/services/plugin-titles";

vi.mock("@/services/desktop-bridge", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/desktop-bridge")>();
  return { ...actual, isDesktopRuntime: () => true, listenAppEvent: () => () => undefined };
});

interface Deferred<T> { promise: Promise<T>; resolve: (value: T) => void; reject: (error: unknown) => void }
function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

const service = vi.hoisted(() => ({
  begin: undefined as undefined | Deferred<unknown>,
  views: [] as unknown[],
  plugins: [] as unknown[],
  invoke: vi.fn(),
}));
vi.mock("@/services/plugin-service", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/plugin-service")>();
  return {
    ...actual,
    beginPluginUISession: () => service.begin!.promise,
    listPlugins: () => Promise.resolve(service.plugins),
    listPluginViews: () => Promise.resolve(service.views),
    closePluginUISession: () => Promise.resolve(),
    invokePluginUI: service.invoke,
  };
});

const { default: PluginViewFrame } = await import("@/components/plugins/PluginViewFrame.vue");

const view = { pluginId: "todo", id: "main", pluginName: "Todo", title: "Todo board", icon: "tickets", enabled: true, activeInWorkspace: true, state: "running" };
const session = {
  mode: "asset", sessionId: "s-1", pluginId: "todo", viewId: "main", url: "about:blank#mincodeSession=s-1&nonce=n-1&generation=1",
  nonce: "n-1", generation: 1, requestTimeoutMs: 30_000, actions: [], binary: false, capabilities: [],
};

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  setPluginViewTitles([]);
});

describe("plugin view skeleton", () => {
  it("renders the cached plugin name, view title and icon as a busy status", () => {
    setPluginViewTitles([view]);
    wrapper = mount(PluginViewSkeleton, { props: { pluginId: "todo", viewId: "main" } });
    const root = wrapper.find(".plugin-view-skeleton");
    expect(root.attributes("role")).toBe("status");
    expect(root.attributes("aria-busy")).toBe("true");
    expect(root.attributes("aria-label")).toContain("Todo board");
    expect(wrapper.find(".plugin-view-skeleton__title").text()).toBe("Todo board");
    expect(wrapper.find(".plugin-view-skeleton__plugin").text()).toBe("Todo");
    expect(wrapper.find(".plugin-view-skeleton__icon").exists()).toBe(true);
    expect(wrapper.findAll(".plugin-view-skeleton__toolbar .el-skeleton__item").length).toBeGreaterThan(0);
    // No screenshot or plugin content: only host placeholders.
    expect(wrapper.find("img").exists()).toBe(false);
    expect(wrapper.find("iframe").exists()).toBe(false);
  });

  it("falls back to the view ID and drops the header in compact layout", () => {
    wrapper = mount(PluginViewSkeleton, { props: { pluginId: "todo", viewId: "main", layout: "compact" } });
    expect(wrapper.find(".plugin-view-skeleton__header").exists()).toBe(false);
    expect(wrapper.find(".plugin-view-skeleton__toolbar").exists()).toBe(false);
    expect(wrapper.find(".plugin-view-skeleton").attributes("aria-label")).toContain("main");
  });

  it("shows immediately on open and gives way to the existing error state on failure", async () => {
    setPluginViewTitles([view]);
    service.begin = deferred();
    service.plugins = [];
    service.views = [];
    wrapper = mount(PluginViewFrame, { props: { pluginId: "todo", viewId: "main", active: true, dropTargetId: "tab" } });
    // Synchronously after mount: the skeleton is up and the frame is busy.
    expect(wrapper.find(".plugin-view-skeleton").exists()).toBe(true);
    expect(wrapper.find(".plugin-view-frame").attributes("aria-busy")).toBe("true");
    service.begin.reject(new Error("plugin view \"main\" not found"));
    await flushPromises();
    expect(wrapper.find(".plugin-view-skeleton").exists()).toBe(false);
    expect(wrapper.find(".plugin-view-unavailable").exists()).toBe(true);
  });

  it("keeps the skeleton over the hidden iframe until the handshake, then reveals the page", async () => {
    setPluginViewTitles([view]);
    service.begin = deferred();
    service.plugins = [{ id: "todo" }];
    service.views = [view];
    wrapper = mount(PluginViewFrame, { props: { pluginId: "todo", viewId: "main", active: true, dropTargetId: "tab" }, attachTo: document.body });
    service.begin.resolve(session);
    await flushPromises();
    const iframe = wrapper.find("iframe");
    expect(iframe.exists()).toBe(true);
    expect(iframe.classes()).not.toContain("is-ready");
    expect(wrapper.find(".plugin-view-skeleton").exists()).toBe(true);

    const contentWindow = (iframe.element as HTMLIFrameElement).contentWindow!;
    const posted: unknown[] = [];
    contentWindow.postMessage = ((message: unknown) => { posted.push(message); }) as typeof contentWindow.postMessage;
    window.dispatchEvent(new MessageEvent("message", {
      data: { type: "mincode.plugin.ready", protocolVersion: 1, sessionId: "s-1", nonce: "n-1", generation: 1 },
      source: contentWindow,
    }));
    await flushPromises();
    expect(posted).toHaveLength(1);
    expect(wrapper.find(".plugin-view-skeleton").exists()).toBe(false);
    expect(wrapper.find("iframe").classes()).toContain("is-ready");
    expect(wrapper.find(".plugin-view-frame").attributes("aria-busy")).toBe("false");
  });
});
