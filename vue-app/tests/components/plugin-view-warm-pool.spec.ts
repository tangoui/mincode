import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { computed, defineComponent, h, onBeforeUnmount, onMounted } from "vue";
import { PLUGIN_VIEW_WARM_TTL_MS } from "@/domain/plugin-ui/warm-pool";

// backend events captured from the warm pool store (the runtime reports desktop).
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

// Frame stub: counts mounts/unmounts per tab and lets the test report a status.
const frames = new Map<string, { mounts: number; unmounts: number; emitStatus?: (status: string) => void; props?: Record<string, unknown> }>();
vi.mock("@/components/plugins/PluginViewFrame.vue", () => ({
  default: defineComponent({
    name: "PluginViewFrame",
    props: { pluginId: String, viewId: String, active: Boolean, warm: Boolean, dropTargetId: String },
    emits: ["status"],
    setup(props, { emit }) {
      const record = frames.get(props.dropTargetId!) ?? { mounts: 0, unmounts: 0 };
      frames.set(props.dropTargetId!, record);
      onMounted(() => {
        record.mounts += 1;
        record.emitStatus = (status: string) => emit("status", status);
      });
      onBeforeUnmount(() => { record.unmounts += 1; });
      return () => {
        record.props = { ...props };
        return h("iframe", { class: "frame-stub", "data-tab": props.dropTargetId });
      };
    },
  }),
}));

const { default: PluginWorkspaceView } = await import("@/views/PluginWorkspaceView.vue");
const { useWorkbenchStore } = await import("@/stores/workbench");
const { usePluginViewWarmPoolStore } = await import("@/stores/plugin-view-warm-pool");
const { settingsSnapshotVersion } = await import("@/services/settings-persistence");

function emitEvent(name: string, payload: unknown) {
  for (const handler of listeners.get(name) ?? []) handler(payload);
}

let wrapper: VueWrapper | undefined;

// Mirrors App.vue: one PluginWorkspaceView per workspace that has plugin tabs or
// warm entries, unmounted (destroying its frames) as soon as it has neither. The
// parent re-renders before the host's own watchers run, so closing a workspace's
// last plugin tab only keeps its frame if the pool parked it synchronously.
const AppHost = defineComponent({
  setup() {
    const workbench = useWorkbenchStore();
    const pool = usePluginViewWarmPoolStore();
    const workspaceIds = computed(() => {
      const ids = new Set<string>();
      for (const tab of workbench.tabs) {
        if (tab.view === "plugin-view" && tab.pluginView?.pluginId && tab.pluginView.viewId) ids.add(tab.workspaceId);
      }
      for (const workspaceId of pool.workspaceIds) ids.add(workspaceId);
      return [...ids];
    });
    return () => h("main", workspaceIds.value.map((workspaceId) => h(PluginWorkspaceView, {
      key: workspaceId, workspaceId, visible: workspaceId === workbench.activeWorkspaceId,
    })));
  },
});

async function setup() {
  const workbench = useWorkbenchStore();
  const pool = usePluginViewWarmPoolStore();
  // The pool is off by default; these cases exercise it with three warm frames.
  pool.setLimit(3);
  wrapper = mount(AppHost, { global: { stubs: { PluginViewUnavailable: true } } });
  await flushPromises();
  return { workbench, pool };
}

async function openReady(workbench: ReturnType<typeof useWorkbenchStore>, pluginId: string, viewId = "main") {
  const tab = workbench.openPluginView(pluginId, viewId, pluginId)!;
  await flushPromises();
  frames.get(tab.id)!.emitStatus!("ready");
  await flushPromises();
  return tab.id;
}

function pane(tabId: string) {
  return wrapper!.find(`[data-tab="${tabId}"]`).element.parentElement!;
}

beforeEach(() => {
  frames.clear();
  listeners.clear();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.useRealTimers();
});

describe("plugin view warm pool", () => {
  it("parks the workspace's only plugin tab under an App-level host and reuses it on reopen", async () => {
    const { workbench, pool } = await setup();
    const tabId = await openReady(workbench, "todo");
    expect(workbench.tabs.filter((tab) => tab.view === "plugin-view")).toHaveLength(1);
    // Closing the only plugin tab: the host must stay mounted because the frame is parked synchronously.
    workbench.closeTab(tabId);
    await flushPromises();
    expect(pool.entries).toHaveLength(1);
    expect(pool.entries[0]!.key).toBe(tabId);
    expect(frames.get(tabId)).toMatchObject({ mounts: 1, unmounts: 0 });
    expect(wrapper!.findComponent(PluginWorkspaceView).exists()).toBe(true);

    const reopened = workbench.openPluginView("todo", "main", "todo")!;
    await flushPromises();
    expect(reopened.id).toBe(tabId);
    expect(pool.entries).toEqual([]);
    expect(frames.get(tabId)).toMatchObject({ mounts: 1, unmounts: 0 });
    expect(frames.get(tabId)!.props).toMatchObject({ warm: false, active: true });

    // Closing it again parks it again.
    workbench.closeTab(tabId);
    await flushPromises();
    expect(pool.entries.map((entry) => entry.key)).toEqual([tabId]);
    expect(frames.get(tabId)!.unmounts).toBe(0);
  });

  it("recreates (never reuses) a frame whose entry expired before its timer fired, without moving other frames", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    const { workbench, pool } = await setup();
    const other = await openReady(workbench, "calendar");
    const tabId = await openReady(workbench, "todo");
    const otherFrame = wrapper!.find(`[data-tab="${other}"]`).element;
    workbench.closeTab(tabId);
    await flushPromises();
    expect(pool.entries).toHaveLength(1);
    vi.setSystemTime(Date.now() + PLUGIN_VIEW_WARM_TTL_MS + 1_000);
    workbench.openPluginView("todo", "main", "todo");
    await flushPromises();
    // The old instance (and its session) is gone and a fresh one was mounted.
    expect(frames.get(tabId)).toMatchObject({ mounts: 2, unmounts: 1 });
    expect(frames.get(tabId)!.props).toMatchObject({ warm: false, active: true });
    expect(pool.entries).toEqual([]);
    // The other iframe is the same, still connected node: nothing was moved or reloaded.
    expect(wrapper!.find(`[data-tab="${other}"]`).element).toBe(otherFrame);
    expect(frames.get(other)).toMatchObject({ mounts: 1, unmounts: 0 });
    expect(frames.get(other)).toMatchObject({ mounts: 1, unmounts: 0 });
  });

  it("keeps a closed ready view hidden and inert, then reuses the same frame on reopen", async () => {
    const { workbench, pool } = await setup();
    const tabId = await openReady(workbench, "todo");
    workbench.openView("terminal");
    workbench.closeTab(tabId);
    await flushPromises();

    expect(pool.entries.map((entry) => entry.key)).toEqual([tabId]);
    expect(frames.get(tabId)).toMatchObject({ mounts: 1, unmounts: 0 });
    expect(frames.get(tabId)!.props).toMatchObject({ warm: true, active: false });
    const parked = pane(tabId);
    expect(parked.hasAttribute("inert")).toBe(true);
    expect(parked.getAttribute("aria-hidden")).toBe("true");
    expect(parked.classList.contains("is-warm")).toBe(true);
    expect(parked.classList.contains("is-active")).toBe(false);

    const reopened = workbench.openPluginView("todo", "main", "todo")!;
    await flushPromises();
    expect(reopened.id).toBe(tabId);
    expect(frames.get(tabId)).toMatchObject({ mounts: 1, unmounts: 0 });
    expect(frames.get(tabId)!.props).toMatchObject({ warm: false, active: true });
    expect(pool.entries).toEqual([]);
    expect(pane(tabId).hasAttribute("inert")).toBe(false);
  });

  it("does not park views that never became ready, popped-out tabs, or anything when disabled", async () => {
    const { workbench, pool } = await setup();
    const loading = workbench.openPluginView("todo", "main", "todo")!;
    await flushPromises();
    workbench.closeTab(loading.id);
    await flushPromises();
    expect(pool.entries).toEqual([]);
    expect(frames.get(loading.id)!.unmounts).toBe(1);

    const popped = await openReady(workbench, "calendar");
    workbench.markTabPoppedOut(popped, "popup-1");
    await flushPromises();
    expect(pool.entries).toEqual([]);
    expect(frames.get(popped)!.unmounts).toBe(1);

    pool.setLimit(0);
    const off = await openReady(workbench, "notes");
    workbench.closeTab(off);
    await flushPromises();
    expect(pool.entries).toEqual([]);
    expect(frames.get(off)!.unmounts).toBe(1);
  });

  it("follows the settings limit even before the first park", async () => {
    const { workbench, pool } = await setup();
    localStorage.setItem("app_settings_v2", JSON.stringify({ pluginViewWarmFrames: 0 }));
    settingsSnapshotVersion.value += 1;
    await flushPromises();
    expect(pool.limit).toBe(0);
    const tabId = await openReady(workbench, "todo");
    workbench.closeTab(tabId);
    await flushPromises();
    expect(pool.entries).toEqual([]);
    expect(frames.get(tabId)!.unmounts).toBe(1);
  });

  it("evicts the least recently closed frame beyond the limit", async () => {
    const { workbench, pool } = await setup();
    pool.setLimit(2);
    const ids: string[] = [];
    for (const plugin of ["a-plugin", "b-plugin", "c-plugin"]) ids.push(await openReady(workbench, plugin));
    for (const id of ids) {
      workbench.closeTab(id);
      await flushPromises();
    }
    expect(pool.entries.map((entry) => entry.key)).toEqual([ids[1], ids[2]]);
    expect(frames.get(ids[0])!.unmounts).toBe(1);
    expect(frames.get(ids[1])!.unmounts).toBe(0);
    pool.setLimit(1);
    await flushPromises();
    expect(pool.entries.map((entry) => entry.key)).toEqual([ids[2]]);
    expect(frames.get(ids[1])!.unmounts).toBe(1);
  });

  it("destroys parked frames after the TTL", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    const { workbench, pool } = await setup();
    const tabId = await openReady(workbench, "todo");
    workbench.closeTab(tabId);
    await flushPromises();
    expect(pool.entries).toHaveLength(1);
    vi.advanceTimersByTime(PLUGIN_VIEW_WARM_TTL_MS + 10);
    await flushPromises();
    expect(pool.entries).toEqual([]);
    expect(frames.get(tabId)!.unmounts).toBe(1);
  });

  it("destroys parked frames on revocation, state, development and workspace events", async () => {
    const { workbench, pool } = await setup();
    const cases: Array<[string, (pluginId: string) => unknown]> = [
      ["plugin:ui-revoked", (pluginId) => ({ pluginId })],
      ["plugin:state-changed", (pluginId) => ({ id: pluginId, state: "stopped", enabled: false })],
      ["plugin:development-changed", (pluginId) => ({ pluginId, status: "disconnected" })],
    ];
    for (const [event, payload] of cases) {
      const pluginId = `p-${event.split(":")[1]}`;
      const tabId = await openReady(workbench, pluginId);
      const other = await openReady(workbench, `${pluginId}-other`);
      workbench.closeTab(tabId);
      workbench.closeTab(other);
      await flushPromises();
      emitEvent(event, payload(pluginId));
      await flushPromises();
      expect(pool.has(tabId), event).toBe(false);
      expect(frames.get(tabId)!.unmounts, event).toBe(1);
      // Other plugins' parked frames are unaffected.
      expect(pool.has(other), event).toBe(true);
      pool.clear();
      await flushPromises();
    }
    const tabId = await openReady(workbench, "todo");
    workbench.closeTab(tabId);
    await flushPromises();
    workbench.createWorkspace("Second");
    await flushPromises();
    expect(pool.entries).toEqual([]);
    expect(frames.get(tabId)!.unmounts).toBe(1);
  });

  it("destroys a parked frame whose session fails (generation change, crash) and never reuses it", async () => {
    const { workbench, pool } = await setup();
    const tabId = await openReady(workbench, "todo");
    workbench.closeTab(tabId);
    await flushPromises();
    frames.get(tabId)!.emitStatus!("unavailable");
    await flushPromises();
    expect(pool.entries).toEqual([]);
    expect(frames.get(tabId)!.unmounts).toBe(1);
    workbench.openPluginView("todo", "main", "todo");
    await flushPromises();
    // A fresh frame instance is created instead.
    expect(frames.get(tabId)!.mounts).toBe(2);
    expect(frames.get(tabId)!.props).toMatchObject({ warm: false, active: true });
  });

  it("drops the parked frames of its workspace when the host unmounts", async () => {
    const { workbench, pool } = await setup();
    const tabId = await openReady(workbench, "todo");
    workbench.closeTab(tabId);
    await flushPromises();
    expect(pool.entries).toHaveLength(1);
    wrapper!.unmount();
    wrapper = undefined;
    expect(pool.entries).toEqual([]);
  });
});
