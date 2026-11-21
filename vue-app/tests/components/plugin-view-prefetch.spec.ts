import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";

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

const service = vi.hoisted(() => ({
  session: undefined as unknown,
  beginImpl: undefined as undefined | (() => Promise<unknown>),
  invokes: [] as Array<{ args: unknown[]; reply: { resolve: (value: unknown) => void; reject: (error: unknown) => void } }>,
}));
vi.mock("@/services/plugin-service", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/plugin-service")>();
  return {
    ...actual,
    beginPluginUISession: () => (service.beginImpl ? service.beginImpl() : Promise.resolve(service.session)),
    listPlugins: () => Promise.resolve([{ id: "todo" }]),
    listPluginViews: () => Promise.resolve([{ pluginId: "todo", id: "main", pluginName: "Todo", title: "Todo", enabled: true, activeInWorkspace: true, state: "running" }]),
    closePluginUISession: () => Promise.resolve(),
    invokePluginUI: (...args: unknown[]) => new Promise((resolve, reject) => { service.invokes.push({ args, reply: { resolve, reject } }); }),
  };
});

const { default: PluginViewFrame } = await import("@/components/plugins/PluginViewFrame.vue");
const { parsePluginUISession } = await vi.importActual<typeof import("@/services/plugin-service")>("@/services/plugin-service");

const actions = [
  { id: "state", risk: "read", maxPayloadBytes: 64 },
  { id: "save", risk: "write", maxPayloadBytes: 64 },
];
const rawSession = (prefetch?: unknown) => ({
  mode: "asset", sessionId: "s-1", pluginId: "todo", viewId: "main", url: "http://127.0.0.1:4000/plugin-ui/key/index.html#mincodeSession=s-1&nonce=n-1&generation=1",
  nonce: "n-1", generation: 1, requestTimeoutMs: 30_000, actions, binary: false, capabilities: [], ...(prefetch ? { prefetch } : {}),
});

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  listeners.clear();
  service.invokes = [];
  service.beginImpl = undefined;
});

async function openFrame(prefetch?: { action: string; payload: string }) {
  service.session = { ...rawSession(), url: "about:blank#mincodeSession=s-1&nonce=n-1&generation=1", ...(prefetch ? { prefetch } : {}) };
  wrapper = mount(PluginViewFrame, { props: { pluginId: "todo", viewId: "main", active: true, dropTargetId: "tab" }, attachTo: document.body });
  await flushPromises();
  return {
    handshake: async () => {
      const contentWindow = (wrapper!.find("iframe").element as HTMLIFrameElement).contentWindow!;
      let connect: Record<string, unknown> | undefined;
      let port: MessagePort | undefined;
      contentWindow.postMessage = ((message: Record<string, unknown>, _origin: string, transfer?: MessagePort[]) => {
        connect = message;
        port = transfer?.[0];
      }) as typeof contentWindow.postMessage;
      const received: Array<Record<string, unknown>> = [];
      window.dispatchEvent(new MessageEvent("message", {
        data: { type: "mincode.plugin.ready", protocolVersion: 1, sessionId: "s-1", nonce: "n-1", generation: 1 },
        source: contentWindow,
      }));
      port!.onmessage = (event) => received.push(event.data as Record<string, unknown>);
      await flushPromises();
      await new Promise((resolve) => setTimeout(resolve, 0));
      return { connect: connect!, received };
    },
  };
}

const settle = async () => {
  await flushPromises();
  await new Promise((resolve) => setTimeout(resolve, 0));
};

describe("view file-drop session metadata", () => {
  it("retains the host declaration without accepting malformed modes", () => {
    const fileDrop = { mode: "paths", maxEntries: 100 };
    expect(parsePluginUISession({ ...rawSession(), fileDrop }, "todo", "main").fileDrop)
      .toEqual({ mode: "paths", maxEntries: 100, extensions: [], directories: false });
    for (const value of [undefined, { mode: "unknown", maxEntries: 100 }, { mode: "paths", maxEntries: -1 }]) {
      expect(parsePluginUISession({ ...rawSession(), fileDrop: value }, "todo", "main").fileDrop).toBeUndefined();
    }
  });
});

describe("view prefetch", () => {
  it("revalidates the declared prefetch against the session's actions", () => {
    expect(parsePluginUISession(rawSession({ action: "state", payload: "{\"x\":1}" }), "todo", "main").prefetch)
      .toEqual({ action: "state", payload: "{\"x\":1}" });
    expect(parsePluginUISession(rawSession({ action: "state" }), "todo", "main").prefetch).toEqual({ action: "state", payload: "null" });
    for (const prefetch of [
      { action: "save" },
      { action: "missing" },
      { action: "state", payload: JSON.stringify({ q: "x".repeat(100) }) },
      { action: "state", payload: "{not json" },
    ]) {
      expect(parsePluginUISession(rawSession(prefetch), "todo", "main").prefetch).toBeUndefined();
    }
  });

  it("invokes through the new session before the handshake and pushes the result after it", async () => {
    const frame = await openFrame({ action: "state", payload: "{\"x\":1}" });
    // In flight while the iframe is still loading.
    expect(service.invokes.map((item) => item.args)).toEqual([["s-1", "n-1", "state", "{\"x\":1}"]]);
    service.invokes[0]!.reply.resolve({ tasks: ["a"] });
    await settle();
    const { connect, received } = await frame.handshake();
    expect(connect).toMatchObject({ features: expect.objectContaining({ prefetch: 1 }), prefetch: { action: "state" } });
    expect(received.filter((message) => message.type === "mincode.host.prefetch"))
      .toEqual([{ type: "mincode.host.prefetch", action: "state", ok: true, result: { tasks: ["a"] } }]);
  });

  it("pushes a result that arrives after the handshake, and failures as ok:false", async () => {
    let frame = await openFrame({ action: "state", payload: "null" });
    let { received } = await frame.handshake();
    expect(received.some((message) => message.type === "mincode.host.prefetch")).toBe(false);
    service.invokes[0]!.reply.resolve([1, 2]);
    await settle();
    expect(received.filter((message) => message.type === "mincode.host.prefetch")).toEqual([{ type: "mincode.host.prefetch", action: "state", ok: true, result: [1, 2] }]);
    wrapper!.unmount();
    service.invokes = [];

    frame = await openFrame({ action: "state", payload: "null" });
    ({ received } = await frame.handshake());
    service.invokes[0]!.reply.reject(new Error("boom"));
    await settle();
    expect(received.find((message) => message.type === "mincode.host.prefetch")).toMatchObject({ action: "state", ok: false, error: { code: "invoke-failed" } });
  });

  it("drops a prefetch whose session was revoked before delivery", async () => {
    const frame = await openFrame({ action: "state", payload: "null" });
    const { received } = await frame.handshake();
    for (const handler of listeners.get("plugin:state-changed") ?? []) handler({ id: "todo", state: "stopped", enabled: true, generation: 1 });
    await settle();
    service.invokes[0]!.reply.resolve({ secret: "late" });
    await settle();
    expect(received.some((message) => message.type === "mincode.host.prefetch")).toBe(false);
    expect(wrapper!.find(".plugin-view-unavailable").exists()).toBe(true);
  });

  it("never prefetches for a frame unmounted before its session arrived", async () => {
    let resolveBegin!: (value: unknown) => void;
    service.beginImpl = () => new Promise((resolve) => { resolveBegin = resolve; });
    service.session = { ...rawSession(), url: "about:blank#mincodeSession=s-1&nonce=n-1&generation=1", prefetch: { action: "state", payload: "null" } };
    wrapper = mount(PluginViewFrame, { props: { pluginId: "todo", viewId: "main", active: true, dropTargetId: "tab" } });
    await flushPromises();
    wrapper.unmount();
    wrapper = undefined;
    resolveBegin(service.session);
    await settle();
    expect(service.invokes).toEqual([]);
  });

  it("lets only the latest attempt prefetch", async () => {
    const pending: Array<(value: unknown) => void> = [];
    service.beginImpl = () => new Promise((resolve) => { pending.push(resolve); });
    const session = (id: string, viewId: string) => ({
      ...rawSession(), sessionId: id, viewId, url: `about:blank#mincodeSession=${id}&nonce=n-1&generation=1`,
      prefetch: { action: "state", payload: JSON.stringify(id) },
    });
    wrapper = mount(PluginViewFrame, { props: { pluginId: "todo", viewId: "main", active: true, dropTargetId: "tab" } });
    await flushPromises();
    // A second attempt (view change) starts before the first session arrives.
    await wrapper.setProps({ viewId: "other" });
    await flushPromises();
    expect(pending).toHaveLength(2);
    pending[1]!(session("s-new", "other"));
    await settle();
    pending[0]!(session("s-old", "main"));
    await settle();
    expect(service.invokes.map((item) => item.args[0])).toEqual(["s-new"]);
  });

  it("does nothing for views without a prefetch", async () => {
    const frame = await openFrame();
    const { connect } = await frame.handshake();
    expect(service.invokes).toEqual([]);
    expect(connect).toMatchObject({ features: expect.objectContaining({ prefetch: 1 }) });
    expect(connect).not.toHaveProperty("prefetch");
  });
});
