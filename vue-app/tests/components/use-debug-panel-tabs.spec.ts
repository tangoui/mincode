import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { effectScope, nextTick } from "vue";
import type { RunConfiguration } from "@/domain/debug/types";

const launcher = vi.hoisted(() => ({ pending: [] as Array<(result: unknown) => void>, progress: true }));

vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => true, invokeBackend: vi.fn(), listenAppEvent: vi.fn(() => () => undefined) }));
vi.mock("@/services/persistent-store", () => ({ loadPersistedValue: vi.fn().mockResolvedValue(null), savePersistedValue: vi.fn().mockResolvedValue(undefined) }));
vi.mock("@/services/debug-service", () => ({ onDebugEvent: () => () => undefined, listDebuggers: vi.fn().mockResolvedValue([]) }));
vi.mock("@/services/debug-launch", () => ({
  syncDebuggerSettings: vi.fn(),
  launchDebugSession: vi.fn(
    (_root: string, _cfg: RunConfiguration, opts: { signal: AbortSignal; onProgress: (progress: { step: string }) => void }) =>
      new Promise((resolve) => {
        if (launcher.progress) opts.onProgress({ step: "resolving" });
        opts.signal.addEventListener("abort", () => resolve(null));
        launcher.pending.push(resolve);
      }),
  ),
}));

import { launchTabSeq, useDebugPanelTabs } from "@/composables/use-debug-panel-tabs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/Users/A/Proj";
const cfg: RunConfiguration = { id: "cfg-1", name: "App", pluginId: "p", type: "python", request: "launch", fields: {} };
const started = { sessionId: "s1", capabilities: {}, skippedBreakpoints: [], breakpointRevision: 0, functionBreakpointNames: [], dataBreakpoints: [], exceptionFilters: [], debuggerListed: true };

beforeEach(() => {
  setActivePinia(createPinia());
  launcher.pending = [];
  launcher.progress = true;
});
afterEach(() => {
  vi.useRealTimers();
});

function panel(owns: (root: string) => boolean = (root) => root === ROOT) {
  const reveal = vi.fn();
  const tabs = effectScope().run(() => useDebugPanelTabs(owns, reveal))!;
  return { ...tabs, reveal };
}

describe("useDebugPanelTabs", () => {
  it("shows a launch as a tab until its session replaces it, opening the panel on each new tab", async () => {
    const store = useDebugSessionsStore();
    const view = panel();
    const launched = store.start(ROOT, cfg, { noDebug: false });
    await nextTick();
    const launchId = view.tabs.value[0]!.id;
    expect(view.tabs.value).toEqual([{ id: launchId, title: "App", closable: false, state: "starting" }]);
    expect(launchTabSeq(launchId)).not.toBeNull();
    expect(view.activeId.value).toBe(launchId);
    expect(view.reveal).toHaveBeenCalledTimes(1);
    expect(view.launchFor(launchId)).toEqual(expect.objectContaining({ configName: "App", step: "resolving" }));

    launcher.pending[0]!(started);
    expect(await launched).toBe("s1");
    await nextTick();
    expect(view.tabs.value).toEqual([{ id: "s1", title: "App", closable: true, state: "running" }]);
    expect(view.activeId.value).toBe("s1");
    expect(view.reveal).toHaveBeenCalledTimes(2);
    expect(view.launchFor(launchId)).toBeUndefined();
  });

  it("drops a cancelled launch's tab and falls back to the selected session", async () => {
    const store = useDebugSessionsStore();
    const view = panel();
    const first = store.start(ROOT, cfg, { noDebug: false });
    launcher.pending[0]!(started);
    await first;
    await nextTick();
    const second = store.start(ROOT, { ...cfg, id: "cfg-2", name: "Other" }, { noDebug: false });
    await nextTick();
    const launchId = view.tabs.value[0]!.id;
    expect(view.activeId.value).toBe(launchId);
    // Selecting the session tab and back keeps both selections apart.
    view.select("s1");
    expect(view.activeId.value).toBe("s1");
    view.select(launchId);
    expect(view.activeId.value).toBe(launchId);
    store.cancelLaunch(view.launchFor(launchId)!.seq);
    expect(await second).toBeNull();
    await nextTick();
    expect(view.tabs.value.map((tab) => tab.id)).toEqual(["s1"]);
    expect(view.activeId.value).toBe("s1");
  });

  it("lists only the launches of projects the view owns", async () => {
    const store = useDebugSessionsStore();
    const view = panel(() => false);
    void store.start(ROOT, cfg, { noDebug: false });
    await nextTick();
    expect(view.tabs.value).toEqual([]);
    expect(view.reveal).not.toHaveBeenCalled();
  });

  it("does not open the panel for a launch refused at once", async () => {
    vi.useFakeTimers();
    launcher.progress = false;
    const store = useDebugSessionsStore();
    const view = panel();
    const launched = store.start(ROOT, cfg, { noDebug: false });
    await nextTick();
    expect(view.tabs.value).toEqual([]);
    vi.advanceTimersByTime(200);
    launcher.pending[0]!(null);
    expect(await launched).toBeNull();
    vi.advanceTimersByTime(1000);
    await nextTick();
    expect(view.tabs.value).toEqual([]);
    expect(view.reveal).not.toHaveBeenCalled();
  });

  it("shows a launch still in its first step after a moment", async () => {
    vi.useFakeTimers();
    launcher.progress = false;
    const store = useDebugSessionsStore();
    const view = panel();
    void store.start(ROOT, cfg, { noDebug: false });
    await nextTick();
    vi.advanceTimersByTime(299);
    await nextTick();
    expect(view.reveal).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    await nextTick();
    expect(view.tabs.value.map((tab) => tab.state)).toEqual(["starting"]);
    expect(view.reveal).toHaveBeenCalledTimes(1);
  });

  it("parses only launch tab ids", () => {
    expect(launchTabSeq("launch:3")).toBe(3);
    expect(launchTabSeq("launch:x")).toBeNull();
    expect(launchTabSeq("s1")).toBeNull();
  });
});
