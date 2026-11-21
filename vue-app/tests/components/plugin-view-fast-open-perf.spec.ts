// Opt-in timing of the real host path (App-level host -> PluginWorkspaceView ->
// PluginViewFrame -> warm pool) for "close the tab, then reopen it":
//   MINCODE_PERF=1 npx vitest run tests/components/plugin-view-fast-open-perf.spec.ts
// Services are mocked (immediate IPC, the page handshakes as soon as its iframe
// exists), so this measures host-side work only; iframe load and paint are
// measured separately in a real browser (docs/verification/plugin-view-fast-open.md).
import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { computed, defineComponent, h } from "vue";

vi.mock("@/services/desktop-bridge", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/desktop-bridge")>();
  return { ...actual, isDesktopRuntime: () => true, listenAppEvent: () => () => undefined };
});

let sessions = 0;
vi.mock("@/services/plugin-service", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/plugin-service")>();
  const view = { pluginId: "todo", id: "main", pluginName: "Todo", title: "Todo", enabled: true, activeInWorkspace: true, state: "running" };
  return {
    ...actual,
    beginPluginUISession: () => {
      sessions += 1;
      return Promise.resolve({
        mode: "asset", sessionId: `s-${sessions}`, pluginId: "todo", viewId: "main", url: `about:blank#mincodeSession=s-${sessions}&nonce=n&generation=1`,
        nonce: "n", generation: 1, requestTimeoutMs: 30_000, actions: [], binary: false, capabilities: [],
      });
    },
    listPlugins: () => Promise.resolve([{ id: "todo" }]),
    listPluginViews: () => Promise.resolve([view]),
    closePluginUISession: () => Promise.resolve(),
  };
});

const { default: PluginWorkspaceView } = await import("@/views/PluginWorkspaceView.vue");
const { useWorkbenchStore } = await import("@/stores/workbench");
const { usePluginViewWarmPoolStore } = await import("@/stores/plugin-view-warm-pool");

const AppHost = defineComponent({
  setup() {
    const workbench = useWorkbenchStore();
    const pool = usePluginViewWarmPoolStore();
    const workspaceIds = computed(() => {
      const ids = new Set<string>();
      for (const tab of workbench.tabs) if (tab.view === "plugin-view" && tab.pluginView) ids.add(tab.workspaceId);
      for (const workspaceId of pool.workspaceIds) ids.add(workspaceId);
      return [...ids];
    });
    return () => h("main", workspaceIds.value.map((workspaceId) => h(PluginWorkspaceView, { key: workspaceId, workspaceId, visible: true })));
  },
});

// Answers the frame's handshake as soon as its iframe exists.
async function handshake(root: Element) {
  for (let attempt = 0; attempt < 200; attempt += 1) {
    const iframe = root.querySelector("iframe:not(.is-ready)") as HTMLIFrameElement | null;
    const session = iframe?.getAttribute("src")?.match(/mincodeSession=(s-\d+)/)?.[1];
    if (iframe?.contentWindow && session) {
      iframe.contentWindow.postMessage = (() => undefined) as typeof iframe.contentWindow.postMessage;
      window.dispatchEvent(new MessageEvent("message", {
        data: { type: "mincode.plugin.ready", protocolVersion: 1, sessionId: session, nonce: "n", generation: 1 },
        source: iframe.contentWindow,
      }));
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  throw new Error("no frame to handshake");
}

async function shownAndInteractive(root: Element) {
  for (let attempt = 0; attempt < 500; attempt += 1) {
    const iframe = root.querySelector(".plugin-view-workspace__pane.is-active iframe.is-ready");
    if (iframe && !iframe.hasAttribute("inert")) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  throw new Error("frame never became ready");
}

const median = (values: number[]) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)]!;

describe.skipIf(!process.env.MINCODE_PERF)("close then reopen timing (real host path, mocked IPC)", () => {
  it("measures reopen with the warm pool off and on", async () => {
    const wrapper = mount(AppHost, { attachTo: document.body, global: { stubs: { PluginViewUnavailable: true } } });
    const workbench = useWorkbenchStore();
    const pool = usePluginViewWarmPoolStore();
    const root = wrapper.element;
    const run = async (limit: number) => {
      pool.setLimit(limit);
      const times: number[] = [];
      // First open.
      const first = workbench.openPluginView("todo", "main", "todo")!;
      await flushPromises();
      await handshake(root);
      await shownAndInteractive(root);
      for (let round = 0; round < 30; round += 1) {
        workbench.closeTab(first.id);
        await flushPromises();
        const t0 = performance.now();
        workbench.openPluginView("todo", "main", "todo");
        if (!pool.has(first.id)) {
          await flushPromises();
          await handshake(root);
        }
        await shownAndInteractive(root);
        times.push(performance.now() - t0);
      }
      workbench.closeTab(first.id);
      pool.clear();
      await flushPromises();
      return median(times);
    };
    const off = await run(0);
    const sessionsBefore = sessions;
    const on = await run(3);
    // eslint-disable-next-line no-console -- opt-in benchmark output (MINCODE_PERF=1 only)
    console.log(JSON.stringify({ reopenMedianMs: { warmPoolOff: Math.round(off * 100) / 100, warmPoolOn: Math.round(on * 100) / 100 }, newSessionsWithPoolOn: sessions - sessionsBefore }));
    // With the pool on, only the first open creates a session; every reopen reuses it.
    expect(sessions - sessionsBefore).toBe(1);
    wrapper.unmount();
  });
});
