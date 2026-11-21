import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { ElMessageBox } from "element-plus";

const listeners = vi.hoisted(() => new Map<string, Array<(payload: unknown) => void>>());
vi.mock("@/services/desktop-bridge", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/desktop-bridge")>();
  return {
    ...actual,
    isDesktopRuntime: () => true,
    chooseNativeFile: () => Promise.resolve("/tmp/site.conf"),
    listenAppEvent: (name: string, handler: (payload: unknown) => void) => {
      listeners.set(name, [...(listeners.get(name) ?? []), handler]);
      return () => listeners.set(name, (listeners.get(name) ?? []).filter((item) => item !== handler));
    },
  };
});

const service = vi.hoisted(() => ({
  views: [] as unknown[],
  viewsGate: undefined as undefined | Promise<void>,
  begins: 0,
  mode: "asset",
  closed: [] as string[],
  invokes: [] as Array<[string, string, string, string]>,
  tasks: 0,
}));
vi.mock("@/services/plugin-service", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/plugin-service")>();
  return {
    ...actual,
    beginPluginUISession: () => {
      service.begins += 1;
      const id = `s-${service.begins}`;
      return Promise.resolve({
        mode: service.mode, sessionId: id, pluginId: "todo", viewId: "main", url: `about:blank#mincodeSession=${id}&nonce=n&generation=1`,
        nonce: "n", generation: 1, requestTimeoutMs: 30_000, binary: false, capabilities: [],
        actions: [
          { id: "state", risk: "read", maxPayloadBytes: 1024 },
          { id: "save", risk: "write", maxPayloadBytes: 1024 },
          { id: "wipe", risk: "dangerous", maxPayloadBytes: 1024 },
          { id: "docs", risk: "read", maxPayloadBytes: 1024, resultHandling: "open-url" },
        ],
      });
    },
    listPlugins: () => Promise.resolve([{ id: "todo" }]),
    listPluginViews: async () => { await service.viewsGate; return service.views; },
    closePluginUISession: (id: string) => { service.closed.push(id); return Promise.resolve(); },
    invokePluginUI: (sessionId: string, nonce: string, action: string, payload: string) => {
      service.invokes.push([sessionId, nonce, action, payload]);
      return Promise.resolve({ ok: action });
    },
    startPluginUITask: () => { service.tasks += 1; return Promise.resolve({ taskId: "t1", state: "running", cancelRequested: false }); },
  };
});

vi.mock("@/services/plugin-full-trust", () => ({ authorizeWithoutPrompt: () => Promise.resolve(true) }));

const { default: PluginViewFrame } = await import("@/components/plugins/PluginViewFrame.vue");

const runningView = { pluginId: "todo", id: "main", pluginName: "Todo", title: "Todo", enabled: true, activeInWorkspace: true, state: "running" };
let wrapper: VueWrapper | undefined;

const settle = async () => {
  await flushPromises();
  await new Promise((resolve) => setTimeout(resolve, 0));
  await flushPromises();
};

async function connectedFrame(extraProps: Record<string, unknown> = {}) {
  service.views = [runningView];
  service.viewsGate = undefined;
  service.begins = 0;
  service.closed = [];
  service.invokes = [];
  service.tasks = 0;
  wrapper = mount(PluginViewFrame, { props: { pluginId: "todo", viewId: "main", active: true, dropTargetId: "tab", ...extraProps }, attachTo: document.body });
  await flushPromises();
  const contentWindow = (wrapper.find("iframe").element as HTMLIFrameElement).contentWindow!;
  const posted: Array<Record<string, unknown>> = [];
  const ports: MessagePort[] = [];
  contentWindow.postMessage = ((message: Record<string, unknown>, _origin: string, transfer?: MessagePort[]) => {
    posted.push(message);
    ports.push(...(transfer ?? []));
  }) as typeof contentWindow.postMessage;
  window.dispatchEvent(new MessageEvent("message", {
    data: { type: "mincode.plugin.ready", protocolVersion: 1, sessionId: "s-1", nonce: "n", generation: 1 },
    source: contentWindow,
  }));
  await flushPromises();
  const received: Array<Record<string, unknown>> = [];
  const page = ports[0]!;
  page.onmessage = (event) => received.push(event.data as Record<string, unknown>);
  let sequence = 0;
  // Sends one request as the page and returns its host response.
  const request = async (body: Record<string, unknown>) => {
    const id = `r-${++sequence}`;
    page.postMessage({ ...body, id });
    for (let attempt = 0; attempt < 20; attempt += 1) {
      await settle();
      const response = received.find((message) => message.type === "mincode.host.response" && message.id === id);
      if (response) return response;
    }
    return undefined;
  };
  return { connect: posted[0]!, received, request };
}

function emit(name: string, payload: unknown) {
  for (const handler of listeners.get(name) ?? []) handler(payload);
}

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  service.mode = "asset";
  vi.restoreAllMocks();
});

describe("warm frame reuse", () => {
  it("advertises visibility, tells a parked page it is hidden and reveals it after revalidation", async () => {
    const { connect, received } = await connectedFrame();
    expect(connect).toMatchObject({ features: expect.objectContaining({ visibility: 1 }), visible: true });
    await wrapper!.setProps({ active: false, warm: true });
    await settle();
    expect(received).toContainEqual({ type: "mincode.host.visibility", visible: false });

    await wrapper!.setProps({ active: true, warm: false });
    await settle();
    const visibility = received.filter((message) => message.type === "mincode.host.visibility");
    expect(visibility).toEqual([{ type: "mincode.host.visibility", visible: false }, { type: "mincode.host.visibility", visible: true }]);
    expect(wrapper!.find("iframe").classes()).toContain("is-ready");
    // Same session, no new begin.
    expect(service.begins).toBe(1);
    expect(service.closed).toEqual([]);
  });

  it("keeps the page shown but inert (no skeleton) while revalidating a reuse", async () => {
    await connectedFrame();
    await wrapper!.setProps({ active: false, warm: true });
    await settle();
    let release!: () => void;
    service.viewsGate = new Promise<void>((resolve) => { release = resolve; });
    await wrapper!.setProps({ active: true, warm: false });
    await flushPromises();
    const iframe = wrapper!.find("iframe");
    expect(iframe.classes()).toContain("is-ready");
    expect(iframe.attributes("inert")).toBeDefined();
    expect(wrapper!.find(".plugin-view-skeleton").exists()).toBe(false);
    expect(wrapper!.find(".plugin-view-frame__loading").exists()).toBe(false);
    release();
    await settle();
    expect(wrapper!.find("iframe").attributes("inert")).toBeUndefined();
  });

  it("fails closed instead of reusing a parked page whose plugin was disabled meanwhile", async () => {
    await connectedFrame();
    await wrapper!.setProps({ active: false, warm: true });
    await flushPromises();
    service.views = [{ ...runningView, enabled: false, state: "disabled" }];
    await wrapper!.setProps({ active: true, warm: false });
    await flushPromises();
    expect(wrapper!.find("iframe").exists()).toBe(false);
    expect(wrapper!.find(".plugin-view-unavailable").exists()).toBe(true);
    expect(service.closed).toEqual(["s-1"]);
  });

  it("fails closed when the view disappeared", async () => {
    await connectedFrame();
    await wrapper!.setProps({ active: false, warm: true });
    service.views = [];
    await wrapper!.setProps({ active: true, warm: false });
    await flushPromises();
    expect(wrapper!.find(".plugin-view-unavailable").exists()).toBe(true);
    expect(service.closed).toEqual(["s-1"]);
  });

  it("refuses host UI and side effects while parked, allows reads, and restores everything on reuse", async () => {
    const confirm = vi.spyOn(ElMessageBox, "confirm").mockResolvedValue("confirm" as never);
    const { request } = await connectedFrame();
    await wrapper!.setProps({ active: false, warm: true });
    await settle();
    const blocked = [
      { type: "mincode.plugin.confirm", intent: "dangerous", subject: "x" },
      { type: "mincode.plugin.clipboard.write-text", text: "x", sensitive: false },
      { type: "mincode.plugin.configuration.open" },
      { type: "mincode.plugin.floating.show", viewId: "pet" },
      { type: "mincode.plugin.floating.activate", token: "t", target: { kind: "app" } },
      { type: "mincode.plugin.pick-path", kind: "file" },
      { type: "mincode.plugin.invoke", action: "save", payload: {} },
      { type: "mincode.plugin.invoke", action: "wipe", payload: {} },
      { type: "mincode.plugin.invoke", action: "docs", payload: {} },
      { type: "mincode.plugin.task.start", action: "state", payload: {}, timeoutMs: 1000 },
    ];
    for (const body of blocked) {
      const response = await request(body);
      expect(response, body.type).toMatchObject({ ok: false, error: { code: "not-allowed" } });
      expect(String((response!.error as { message: string }).message), body.type).toMatch(/plugins\.actionNotAllowedWhileClosed|保温|warm/);
    }
    expect(confirm).not.toHaveBeenCalled();
    expect(service.invokes).toEqual([]);
    expect(service.tasks).toBe(0);
    // Reads keep working while parked; task lookups are not refused as "closed".
    expect(await request({ type: "mincode.plugin.invoke", action: "state", payload: {} })).toMatchObject({ ok: true, result: { ok: "state" } });
    const lookup = await request({ type: "mincode.plugin.task.get", taskId: "t9" });
    expect(String((lookup?.error as { message?: string } | undefined)?.message ?? "")).not.toMatch(/WhileClosed|保温/);

    // Reused and visible again: side effects are allowed.
    await wrapper!.setProps({ active: true, warm: false });
    await settle();
    expect(await request({ type: "mincode.plugin.invoke", action: "save", payload: {} })).toMatchObject({ ok: true });
    expect(await request({ type: "mincode.plugin.task.start", action: "state", payload: {}, timeoutMs: 1000 })).toMatchObject({ ok: true });
    expect(service.tasks).toBe(1);
  });

  it("opens the file dialog at once after a click and confirms first otherwise", async () => {
    const confirm = vi.spyOn(ElMessageBox, "confirm").mockResolvedValue("confirm" as never);
    const activation = { isActive: true };
    Object.defineProperty(navigator, "userActivation", { configurable: true, get: () => activation });
    try {
      const { request } = await connectedFrame();
      expect(await request({ type: "mincode.plugin.pick-path", kind: "file" })).toMatchObject({ ok: true, result: "/tmp/site.conf" });
      expect(confirm).not.toHaveBeenCalled();
      activation.isActive = false;
      expect(await request({ type: "mincode.plugin.pick-path", kind: "file" })).toMatchObject({ ok: true, result: "/tmp/site.conf" });
      expect(confirm).toHaveBeenCalledTimes(1);
    } finally {
      delete (navigator as { userActivation?: unknown }).userActivation;
      confirm.mockRestore();
    }
  });

  it("fails closed when plugin:ui-revoked lists its session, and ignores events about other sessions", async () => {
    await connectedFrame();
    emit("plugin:ui-revoked", { pluginId: "todo", sessionIds: ["s-old"] });
    emit("plugin:ui-revoked", { pluginId: "other", sessionIds: ["s-1"] });
    await settle();
    expect(wrapper!.find("iframe").exists()).toBe(true);
    emit("plugin:ui-revoked", { pluginId: "todo", sessionIds: ["s-1"] });
    await settle();
    expect(wrapper!.find("iframe").exists()).toBe(false);
    expect(wrapper!.find(".plugin-view-unavailable").exists()).toBe(true);
    expect(service.closed).toEqual(["s-1"]);
  });

  it("leaves development sessions to the development renewal path", async () => {
    service.mode = "development";
    await connectedFrame();
    emit("plugin:ui-revoked", { pluginId: "todo", sessionIds: ["s-1"] });
    await settle();
    expect(wrapper!.find("iframe").exists()).toBe(true);
  });
});

describe("settings variant", () => {
  it("connects with surface settings and without viewSave", async () => {
    const { connect } = await connectedFrame({ variant: "settings", dropTargetId: "" });
    const features = connect.features as Record<string, unknown>;
    expect(features.viewSave).toBeUndefined();
    expect(features.tasks).toBe(1);
    expect(connect.context).toMatchObject({ surface: "settings" });
    wrapper!.unmount();
    wrapper = undefined;
    const page = await connectedFrame();
    expect(page.connect.features).toMatchObject({ viewSave: 1 });
  });

  it("leaves the failure message to the settings section", async () => {
    await connectedFrame({ variant: "settings", dropTargetId: "" });
    emit("plugin:ui-revoked", { pluginId: "todo", sessionIds: ["s-1"] });
    await settle();
    const statuses = wrapper!.emitted("status") ?? [];
    const failure = statuses.find(([status]) => status === "error" || status === "unavailable");
    expect(failure).toBeDefined();
    expect(typeof failure![1]).toBe("string");
    expect(wrapper!.find(".plugin-view-unavailable").exists()).toBe(false);
  });
});
