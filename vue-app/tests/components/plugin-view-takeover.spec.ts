import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

const listeners = new Map<string, Array<(payload: unknown) => void>>();
vi.mock("@/services/desktop-bridge", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/desktop-bridge")>();
  return {
    ...actual,
    isDesktopRuntime: () => true,
    listenAppEvent: (name: string, handler: (payload: unknown) => void) => {
      listeners.set(name, [...(listeners.get(name) ?? []), handler]);
      return () => undefined;
    },
  };
});

vi.mock("@/services/plugin-service", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/plugin-service")>();
  return {
    ...actual,
    beginPluginUISession: () => Promise.resolve({
      mode: "asset", sessionId: "s-1", pluginId: "rdp", viewId: "main", url: "about:blank#mincodeSession=s-1&nonce=n-1&generation=1",
      nonce: "n-1", generation: 1, requestTimeoutMs: 30_000, actions: [], binary: false, capabilities: [],
    }),
    listPlugins: () => Promise.resolve([{ id: "rdp" }]),
    listPluginViews: () => Promise.resolve([{ pluginId: "rdp", id: "main", pluginName: "RDP", title: "RDP", enabled: true, activeInWorkspace: true, state: "running" }]),
    closePluginUISession: () => Promise.resolve(),
  };
});

const { default: PluginViewFrame } = await import("@/components/plugins/PluginViewFrame.vue");
const { setPluginWindowFullscreen, usePluginTakeoverStore } = await import("@/stores/plugin-takeover");

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  listeners.clear();
});

async function connect() {
  const pinia = createPinia();
  setActivePinia(pinia);
  setPluginWindowFullscreen(async () => undefined);
  wrapper = mount(PluginViewFrame, {
    props: { pluginId: "rdp", viewId: "main", active: true, dropTargetId: "tab" },
    attachTo: document.body,
    global: { plugins: [pinia] },
  });
  await flushPromises();
  const contentWindow = (wrapper.find("iframe").element as HTMLIFrameElement).contentWindow!;
  let connectMessage: Record<string, unknown> | undefined;
  let port: MessagePort | undefined;
  contentWindow.postMessage = ((message: Record<string, unknown>, _origin: string, transfer?: MessagePort[]) => {
    connectMessage = message;
    port = transfer?.[0];
  }) as typeof contentWindow.postMessage;
  window.dispatchEvent(new MessageEvent("message", {
    data: { type: "mincode.plugin.ready", protocolVersion: 1, sessionId: "s-1", nonce: "n-1", generation: 1 },
    source: contentWindow,
  }));
  await flushPromises();
  const received: Array<Record<string, unknown>> = [];
  port!.onmessage = (event) => received.push(event.data as Record<string, unknown>);
  const settle = async () => {
    for (let attempt = 0; attempt < 10; attempt++) {
      await flushPromises();
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  };
  const request = async (message: Record<string, unknown>) => {
    port!.postMessage(message);
    await settle();
    return received.find((item) => item.id === message.id);
  };
  return { features: connectMessage!.features as Record<string, unknown>, request, received, settle, store: usePluginTakeoverStore() };
}

describe("plugin takeover bridge", () => {
  it("offers immersive mode and keyboard capture to every view", async () => {
    const { features } = await connect();
    expect(features).toMatchObject({ immersive: 1, keyboardCapture: 1 });
  });

  it("enters and leaves immersive mode and tells the page", async () => {
    const { request, received, store } = await connect();
    expect(await request({ type: "mincode.plugin.immersive.enter", id: "i1" })).toMatchObject({ ok: true });
    expect(store.immersive).toEqual({ sessionId: "s-1" });
    expect(received).toContainEqual({ type: "mincode.host.immersive", active: true });
    expect(await request({ type: "mincode.plugin.immersive.exit", id: "i2" })).toMatchObject({ ok: true });
    expect(store.immersive).toBeNull();
    expect(received).toContainEqual({ type: "mincode.host.immersive", active: false });
  });

  it("captures the keyboard and reports host releases to the page", async () => {
    const { request, received, settle, store } = await connect();
    expect(await request({ type: "mincode.plugin.keyboard.capture", id: "k1" })).toMatchObject({ ok: true });
    expect(store.keyboardCaptured()).toBe(true);
    store.releaseCaptureByHost();
    await settle();
    expect(received).toContainEqual({ type: "mincode.host.keyboard", captured: false });
  });

  it("drops the session's takeover when the frame is revoked or unmounted", async () => {
    const { request, settle, store } = await connect();
    await request({ type: "mincode.plugin.immersive.enter", id: "i1" });
    await request({ type: "mincode.plugin.keyboard.capture", id: "k1" });
    for (const handler of listeners.get("plugin:ui-revoked") ?? []) handler({ pluginId: "rdp", sessionIds: ["s-1"] });
    await settle();
    expect(store.immersive).toBeNull();
    expect(store.capture).toBeNull();

    await request({ type: "mincode.plugin.immersive.enter", id: "i2" });
    wrapper!.unmount();
    wrapper = undefined;
    await settle();
    expect(store.immersive).toBeNull();
  });

  it("lets go of the keyboard when the page loses focus", async () => {
    const { request, settle, store } = await connect();
    await request({ type: "mincode.plugin.keyboard.capture", id: "k1" });
    window.dispatchEvent(new Event("blur"));
    await settle();
    expect(store.capture).toBeNull();
  });
});
