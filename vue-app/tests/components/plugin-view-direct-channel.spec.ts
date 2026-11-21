import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";

vi.mock("@/services/desktop-bridge", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/desktop-bridge")>();
  return { ...actual, isDesktopRuntime: () => true, listenAppEvent: () => () => undefined };
});

const service = vi.hoisted(() => ({
  binary: false,
  tokenRequests: [] as Array<[string, string]>,
  tokenResult: { token: "ab".repeat(32), url: `ws://127.0.0.1:4000/__mincode/direct?token=${"ab".repeat(32)}` } as unknown,
}));
vi.mock("@/services/plugin-service", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/plugin-service")>();
  return {
    ...actual,
    beginPluginUISession: () => Promise.resolve({
      mode: "asset", sessionId: "s-1", pluginId: "pet", viewId: "main", url: "about:blank#mincodeSession=s-1&nonce=n-1&generation=1",
      nonce: "n-1", generation: 1, requestTimeoutMs: 30_000, actions: [], binary: service.binary, capabilities: [],
    }),
    listPlugins: () => Promise.resolve([{ id: "pet" }]),
    listPluginViews: () => Promise.resolve([{ pluginId: "pet", id: "main", pluginName: "Pet", title: "Pet", enabled: true, activeInWorkspace: true, state: "running" }]),
    closePluginUISession: () => Promise.resolve(),
  };
});
vi.mock("@/services/plugin-ui-direct", async () => {
  const { parsePluginUIDirectToken } = await import("@/domain/plugin-ui/direct-token");
  return {
    requestPluginUIDirectToken: async (sessionId: string, nonce: string) => {
      service.tokenRequests.push([sessionId, nonce]);
      return parsePluginUIDirectToken(service.tokenResult);
    },
  };
});

const { default: PluginViewFrame } = await import("@/components/plugins/PluginViewFrame.vue");

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  service.tokenRequests = [];
});

async function connect(binary: boolean) {
  service.binary = binary;
  wrapper = mount(PluginViewFrame, { props: { pluginId: "pet", viewId: "main", active: true, dropTargetId: "tab" }, attachTo: document.body });
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
  const request = async (message: Record<string, unknown>) => {
    port!.postMessage(message);
    for (let attempt = 0; attempt < 20 && !received.some((item) => item.id === message.id); attempt++) {
      await flushPromises();
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
    return received.find((item) => item.id === message.id);
  };
  return { features: connectMessage!.features as Record<string, unknown>, request };
}

describe("direct binary channel negotiation", () => {
  it("offers the direct channel only to framed-transport views", async () => {
    const plain = await connect(false);
    expect(plain.features.binary).toBeUndefined();
    expect(plain.features.binaryDirect).toBeUndefined();
    wrapper!.unmount();
    const framed = await connect(true);
    expect(framed.features).toMatchObject({ binary: 1, binaryDirect: 1 });
  });

  it("hands a framed view the socket URL for its own session", async () => {
    const { request } = await connect(true);
    const response = await request({ type: "mincode.plugin.direct.token", id: "d1" });
    expect(service.tokenRequests).toEqual([["s-1", "n-1"]]);
    expect(response).toEqual({ type: "mincode.host.response", id: "d1", ok: true, result: { url: (service.tokenResult as { url: string }).url } });
  });

  it("refuses the token to views without the framed transport", async () => {
    const { request } = await connect(false);
    const response = await request({ type: "mincode.plugin.direct.token", id: "d2" });
    expect(service.tokenRequests).toEqual([]);
    expect(response).toMatchObject({ id: "d2", ok: false, error: { code: "not-allowed" } });
  });
});
