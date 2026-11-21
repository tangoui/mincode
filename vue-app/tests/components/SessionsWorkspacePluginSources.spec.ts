import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import type { PointContribution } from "@/domain/plugins/contributions";

// The sessions page with its real plugin source chain: the data provider
// consumer (entry module + lazy runtime + store), PluginSessionSources and
// the transcript. Native calls, routing, workspace persistence and the
// contribution sync are stubbed.
const bridge = vi.hoisted(() => ({ calls: [] as Array<[string, Record<string, unknown>]> }));
vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/desktop-bridge")>()),
  isDesktopRuntime: () => true,
  listenAppEvent: () => () => undefined,
  chooseNativeDirectory: () => Promise.resolve(""),
  invokeBackend: (command: string, args: Record<string, unknown>) => {
    bridge.calls.push([command, args]);
    if (command === "plugin_host_session_list") {
      return Promise.resolve({ sessions: [{ id: "a-1", title: "Alpha session" }], dropped: 0, truncated: false });
    }
    if (command === "plugin_host_session_messages") {
      return Promise.resolve({ messages: [{ role: "assistant", text: "alpha transcript" }], dropped: 0, truncated: false });
    }
    if (command === "plugin_host_contribution_cancel") return Promise.resolve({ cancelled: false });
    return Promise.resolve(command.startsWith("list_") ? [] : {});
  },
}));
vi.mock("vue-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("vue-router")>()),
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: vi.fn() }),
}));
vi.mock("@/composables/use-active-view-commands", () => ({ useToggleSidebarCommand: () => undefined }));
vi.mock("@/stores/ai-assistant", () => ({ useAiAssistantStore: () => ({ setDraft: () => undefined, show: () => undefined }) }));
vi.mock("@/composables/use-workspace-state-scope", async () => {
  const { ref } = await import("vue");
  return {
    useWorkspaceStateScope: () => ({ hydrate: () => Promise.resolve() }),
    useWorkspaceScopedPersistence: () => ({
      scopeId: ref("test"),
      capture: () => ({}),
      isCurrent: () => true,
      load: () => Promise.resolve(undefined),
      save: () => Promise.resolve(),
    }),
  };
});
vi.mock("@/services/session-tool-preferences", async () => {
  const { createDefaultSessionToolPrefs } = await import("@/domain/ai/session-tools");
  return {
    SESSION_TOOLS_EVENT: "test:session-tools",
    applySessionToolRoots: () => Promise.resolve({}),
    loadSessionToolPrefs: () => Promise.resolve(createDefaultSessionToolPrefs()),
  };
});
// The contribution sync: consumers are captured and driven like applyConsumer
// (dispose the previous apply, then apply the next snapshot in the same tick).
const sync = vi.hoisted(() => ({
  consumers: new Map<string, { apply: (contributions: PointContribution[]) => (() => void) | void; dispose?: () => void }>(),
}));
vi.mock("@/services/plugin-contributions", () => ({
  registerContributionConsumer: (consumer: { point: string; apply: (contributions: PointContribution[]) => (() => void) | void }) => {
    sync.consumers.set(consumer.point, { apply: consumer.apply });
    return () => sync.consumers.delete(consumer.point);
  },
}));

const { registerDataProviderContributionConsumers } = await import("@/services/contributions/data-providers");
const { default: SessionsWorkspaceView } = await import("@/views/SessionsWorkspaceView.vue");

function contribution(pluginId: string, entries: Array<Record<string, unknown>>): PointContribution {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: JSON.stringify(entries), entries, issues: [] },
  } as PointContribution;
}

function applySnapshot(contributions: PointContribution[]) {
  const state = sync.consumers.get("sessionSources");
  if (!state) throw new Error("sessionSources consumer not registered");
  state.dispose?.();
  state.dispose = state.apply(contributions) ?? undefined;
}

const settle = async () => {
  for (let round = 0; round < 4; round += 1) {
    await flushPromises();
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
};

let wrapper: VueWrapper | undefined;
let stopConsumers: (() => void) | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  stopConsumers?.();
  stopConsumers = undefined;
  sync.consumers.clear();
  bridge.calls = [];
});

const alpha = contribution("acme.alpha", [{ id: "chat", title: "Alpha", action: "sessions.read" }]);
const beta = (title: string) => contribution("acme.beta", [{ id: "chat", title, action: "sessions.read" }]);

describe("SessionsWorkspaceView plugin sources", () => {
  it("another plugin's change keeps an expanded source and its open transcript", async () => {
    stopConsumers = registerDataProviderContributionConsumers();
    applySnapshot([alpha, beta("Beta")]);
    await settle();
    wrapper = mount(SessionsWorkspaceView, { attachTo: document.body, global: { stubs: { teleport: true } } });
    await settle();

    const toggles = () => wrapper!.findAll('[data-testid="plugin-session-source-toggle"]');
    await vi.waitFor(() => expect(toggles()).toHaveLength(2));
    await toggles()[0].trigger("click");
    await vi.waitFor(() => expect(wrapper!.find(".app-plugin-session-sources .sessions-session-row button").exists()).toBe(true));
    await wrapper.get(".app-plugin-session-sources .sessions-session-row button").trigger("click");
    await vi.waitFor(() => expect(wrapper!.text()).toContain("alpha transcript"));
    const listCalls = bridge.calls.filter(([command]) => command === "plugin_host_session_list").length;
    const messageCalls = bridge.calls.filter(([command]) => command === "plugin_host_session_messages").length;

    // Plugin B's source changes: the host disposes the old apply and applies the new snapshot.
    applySnapshot([alpha, beta("Beta renamed")]);
    await wrapper.vm.$nextTick();
    expect(toggles()).toHaveLength(2);
    await settle();

    expect(toggles()).toHaveLength(2);
    expect(toggles()[0].attributes("aria-expanded")).toBe("true");
    expect(toggles()[1].text()).toContain("Beta renamed");
    expect(wrapper.text()).toContain("alpha transcript");
    expect(wrapper.find(".app-plugin-session-sources .sessions-session-row.is-active").exists()).toBe(true);
    expect(bridge.calls.filter(([command]) => command === "plugin_host_session_list")).toHaveLength(listCalls);
    expect(bridge.calls.filter(([command]) => command === "plugin_host_session_messages")).toHaveLength(messageCalls);
  });

  it("removing the plugin closes its transcript", async () => {
    stopConsumers = registerDataProviderContributionConsumers();
    applySnapshot([alpha]);
    await settle();
    wrapper = mount(SessionsWorkspaceView, { attachTo: document.body, global: { stubs: { teleport: true } } });
    await vi.waitFor(() => expect(wrapper!.find('[data-testid="plugin-session-source-toggle"]').exists()).toBe(true));
    await wrapper.get('[data-testid="plugin-session-source-toggle"]').trigger("click");
    await vi.waitFor(() => expect(wrapper!.find(".app-plugin-session-sources .sessions-session-row button").exists()).toBe(true));
    await wrapper.get(".app-plugin-session-sources .sessions-session-row button").trigger("click");
    await vi.waitFor(() => expect(wrapper!.text()).toContain("alpha transcript"));

    applySnapshot([]);
    await settle();
    expect(wrapper.find('[data-testid="plugin-session-source-toggle"]').exists()).toBe(false);
    expect(wrapper.text()).not.toContain("alpha transcript");
  });
});
