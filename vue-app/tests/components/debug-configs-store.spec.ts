import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { PROJECT_DEBUG_STORAGE_KEY } from "@/domain/debug/configurations";
import type { RunConfiguration } from "@/domain/debug/types";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { flushUiSessionPersistence } from "@/services/ui-session";

const message = vi.hoisted(() => ({ error: vi.fn() }));

vi.mock("@/services/persistent-store", () => ({ loadPersistedValue: vi.fn(), savePersistedValue: vi.fn().mockResolvedValue(undefined) }));
vi.mock("element-plus", async (importOriginal) => ({ ...(await importOriginal<object>()), ElMessage: message }));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));

const ROOT = "/Users/A/Proj";
const FILE = "/Users/A/Proj/src/app.py";

function lastSaved(): Record<string, { breakpoints: Record<string, unknown> } & Record<string, unknown>> {
  return vi.mocked(savePersistedValue).mock.calls.at(-1)?.[1] as never;
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.mocked(loadPersistedValue).mockReset().mockResolvedValue(null);
  vi.mocked(savePersistedValue).mockReset().mockResolvedValue(undefined);
  message.error.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("debug configs store", () => {
  it("the app's quit / close flush writes a pending debounced save", async () => {
    const store = useDebugConfigsStore();
    await store.load();
    store.toggleBreakpoint(ROOT, FILE, 4);
    expect(savePersistedValue).not.toHaveBeenCalled();
    // App.vue flushPersistentUiState → flushUiSessionPersistence.
    await flushUiSessionPersistence();
    expect(savePersistedValue).toHaveBeenCalledTimes(1);
    expect(lastSaved()[ROOT]!.breakpoints).toEqual({ "src/app.py": [{ line: 4, enabled: true }] });
    await vi.advanceTimersByTimeAsync(1000);
    expect(savePersistedValue).toHaveBeenCalledTimes(1);
  });

  it("reports a failed save once until a save succeeds", async () => {
    const store = useDebugConfigsStore();
    await store.load();
    vi.mocked(savePersistedValue).mockRejectedValue(new Error("disk full"));
    store.toggleBreakpoint(ROOT, FILE, 4);
    await store.flush();
    store.toggleBreakpoint(ROOT, FILE, 5);
    await store.flush();
    expect(message.error).toHaveBeenCalledTimes(1);
    expect(message.error.mock.calls[0]![0]).toMatchObject({ message: expect.stringContaining("disk full") });
    vi.mocked(savePersistedValue).mockResolvedValue(undefined);
    store.toggleBreakpoint(ROOT, FILE, 6);
    await store.flush();
    vi.mocked(savePersistedValue).mockRejectedValue(new Error("disk full"));
    store.toggleBreakpoint(ROOT, FILE, 7);
    await store.flush();
    expect(message.error).toHaveBeenCalledTimes(2);
  });

  it("persists breakpoints under the relative key", async () => {
    const store = useDebugConfigsStore();
    await store.load();
    store.toggleBreakpoint(ROOT, FILE, 4);
    store.setBreakpoint(ROOT, "/users/a/proj/src/app.py", { line: 9, condition: "x > 1", enabled: true });
    store.toggleBreakpoint(ROOT, "/Users/A/Elsewhere/app.py", 1);
    expect(store.breakpointsFor(ROOT, FILE)).toEqual([{ line: 4, enabled: true }, { line: 9, condition: "x > 1", enabled: true }]);
    expect(store.breakpointsFor(ROOT, "/Users/A/Elsewhere/app.py")).toEqual([]);

    await vi.advanceTimersByTimeAsync(299);
    expect(savePersistedValue).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(savePersistedValue).toHaveBeenCalledTimes(1);
    expect(vi.mocked(savePersistedValue).mock.calls[0]![0]).toBe(PROJECT_DEBUG_STORAGE_KEY);
    expect(lastSaved()[ROOT]!.breakpoints).toEqual({
      "src/app.py": [{ line: 4, enabled: true }, { line: 9, condition: "x > 1", enabled: true }],
    });

    setActivePinia(createPinia());
    vi.mocked(loadPersistedValue).mockResolvedValue(lastSaved());
    const restored = useDebugConfigsStore();
    await restored.load();
    expect(restored.breakpointsFor(ROOT, FILE).map((bp) => bp.line)).toEqual([4, 9]);
  });

  it("preserves logpoint messages and disabled state through line moves and persisted reloads", async () => {
    const store = useDebugConfigsStore();
    await store.load();
    const bp = { line: 3, enabled: false, condition: "value > 0", hitCondition: "2", logMessage: "  value={value} <tag>  " };
    store.setBreakpoint(ROOT, FILE, bp);
    store.moveBreakpoints(ROOT, FILE, [{ from: 3, to: 8 }]);
    await store.flush();
    vi.mocked(loadPersistedValue).mockResolvedValue(lastSaved());
    setActivePinia(createPinia());
    const restored = useDebugConfigsStore();
    await restored.load();
    expect(restored.breakpointsFor(ROOT, FILE)).toEqual([{ ...bp, line: 8 }]);
  });

  it("toggleBreakpoint twice removes it", async () => {
    const store = useDebugConfigsStore();
    await store.load();
    store.toggleBreakpoint(ROOT, FILE, 7);
    store.toggleBreakpoint(ROOT, FILE, 7);
    expect(store.breakpointsFor(ROOT, FILE)).toEqual([]);
    expect(store.stateFor(ROOT).breakpoints).toEqual({});
    store.setBreakpoint(ROOT, FILE, { line: 2, enabled: false });
    store.removeBreakpoint(ROOT, FILE, 2);
    expect(store.stateFor(ROOT).breakpoints).toEqual({});
  });

  it("moveBreakpoints updates lines and drops collisions", async () => {
    const store = useDebugConfigsStore();
    await store.load();
    for (const line of [3, 5, 8, 12]) store.toggleBreakpoint(ROOT, FILE, line);
    store.setBreakpoint(ROOT, FILE, { line: 8, condition: "keep", enabled: true });
    // Lines 3..8 shift down by two; 12 collapses onto the moved 8 (now 10).
    store.moveBreakpoints(ROOT, FILE, [{ from: 3, to: 5 }, { from: 5, to: 7 }, { from: 8, to: 10 }, { from: 12, to: 10 }]);
    expect(store.breakpointsFor(ROOT, FILE)).toEqual([
      { line: 5, enabled: true },
      { line: 7, enabled: true },
      { line: 10, condition: "keep", enabled: true },
    ]);
    store.moveBreakpoints(ROOT, FILE, [{ from: 5, to: 0 }]);
    expect(store.breakpointsFor(ROOT, FILE).map((bp) => bp.line)).toEqual([7, 10]);
  });

  it("moveBreakpoints keeps a line's own breakpoint when another moves onto it", async () => {
    const store = useDebugConfigsStore();
    await store.load();
    store.setBreakpoint(ROOT, FILE, { line: 3, condition: "moved", enabled: true });
    store.setBreakpoint(ROOT, FILE, { line: 5, condition: "own", enabled: true });
    store.setBreakpoint(ROOT, FILE, { line: 6, condition: "joined", enabled: true });
    // 3 is pushed onto 5; 6 is joined into 5; 5 stays put.
    store.moveBreakpoints(ROOT, FILE, [{ from: 3, to: 5 }, { from: 6, to: 5 }]);
    expect(store.breakpointsFor(ROOT, FILE)).toEqual([{ line: 5, condition: "own", enabled: true }]);
  });

  it("keeps configs, selection, filters, watches and panel height per project", async () => {
    const store = useDebugConfigsStore();
    await store.load();
    const cfg: RunConfiguration = { id: "c1", name: "app", pluginId: "p", type: "python", request: "launch", fields: { program: "a.py" } };
    store.upsertConfig(ROOT, cfg);
    store.upsertConfig(ROOT, { ...cfg, id: "c2", name: "other" });
    store.selectConfig(ROOT, "c1");
    store.upsertConfig(ROOT, { ...cfg, name: "renamed" });
    expect(store.stateFor(ROOT).configs.map((c) => c.name)).toEqual(["renamed", "other"]);
    store.removeConfig(ROOT, "c1");
    expect(store.stateFor(ROOT).selectedConfigId).toBe("c2");
    store.selectConfig(ROOT, "missing");
    expect(store.stateFor(ROOT).selectedConfigId).toBe("c2");
    store.setExceptionFilters(ROOT, "python", ["raised"]);
    store.setWatches(ROOT, ["x", "y.z"]);
    store.setPanelHeight(ROOT, 321.6);
    expect(store.stateFor("/other").configs).toEqual([]);
    await vi.advanceTimersByTimeAsync(300);
    expect(savePersistedValue).toHaveBeenCalledTimes(1);
    expect(lastSaved()[ROOT]).toMatchObject({
      selectedConfigId: "c2",
      exceptionFilters: { python: ["raised"] },
      watches: ["x", "y.z"],
      panelHeight: 322,
    });
    expect(lastSaved()["/other"]).toBeUndefined();
  });

  it("keeps edits made while loading and stored projects alike", async () => {
    let resolve: (value: unknown) => void = () => undefined;
    vi.mocked(loadPersistedValue).mockReturnValue(new Promise((done) => { resolve = done; }));
    const store = useDebugConfigsStore();
    const loading = store.load();
    store.toggleBreakpoint(ROOT, FILE, 1);
    resolve({ "/q": { configs: [], selectedConfigId: null, breakpoints: { "a.py": [{ line: 2, enabled: true }] }, exceptionFilters: {}, watches: [], panelHeight: 200 } });
    await loading;
    await vi.advanceTimersByTimeAsync(300);
    expect(Object.keys(lastSaved()).sort()).toEqual(["/Users/A/Proj", "/q"]);
    expect(store.breakpointsFor(ROOT, FILE).map((bp) => bp.line)).toEqual([1]);
  });

  it("never saves after a failed load and keeps stored projects once a load succeeds", async () => {
    vi.mocked(loadPersistedValue).mockRejectedValue(new Error("native store down"));
    const store = useDebugConfigsStore();
    await store.load();
    expect(store.loaded).toBe(false);
    store.toggleBreakpoint(ROOT, FILE, 3);
    expect(store.breakpointsFor(ROOT, FILE).map((bp) => bp.line)).toEqual([3]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(savePersistedValue).not.toHaveBeenCalled();

    vi.mocked(loadPersistedValue).mockResolvedValue({
      "/q": { configs: [], selectedConfigId: null, breakpoints: { "a.py": [{ line: 2, enabled: true }] }, exceptionFilters: {}, watches: [], panelHeight: 200 },
    });
    await store.load();
    expect(store.loaded).toBe(true);
    await vi.advanceTimersByTimeAsync(300);
    expect(savePersistedValue).toHaveBeenCalledTimes(1);
    expect(Object.keys(lastSaved()).sort()).toEqual(["/Users/A/Proj", "/q"]);
    expect(lastSaved()["/q"]!.breakpoints).toEqual({ "a.py": [{ line: 2, enabled: true }] });
    expect(store.breakpointsFor(ROOT, FILE).map((bp) => bp.line)).toEqual([3]);
  });

  it("replays edits made while loading onto the same root's stored state", async () => {
    let resolve: (value: unknown) => void = () => undefined;
    vi.mocked(loadPersistedValue).mockReturnValue(new Promise((done) => { resolve = done; }));
    const store = useDebugConfigsStore();
    const loading = store.load();
    store.toggleBreakpoint(ROOT, FILE, 1);
    store.setWatches(ROOT, ["w"]);
    resolve({
      [ROOT]: {
        configs: [{ id: "c1", name: "app", pluginId: "p", type: "python", request: "launch", fields: {} }],
        selectedConfigId: "c1",
        breakpoints: { "src/app.py": [{ line: 9, enabled: true }], "lib.py": [{ line: 4, enabled: true }] },
        exceptionFilters: { python: ["raised"] },
        watches: ["old"],
        panelHeight: 400,
      },
    });
    await loading;
    const state = store.stateFor(ROOT);
    expect(state.configs.map((c) => c.id)).toEqual(["c1"]);
    expect(state.selectedConfigId).toBe("c1");
    expect(state.breakpoints).toEqual({
      "src/app.py": [{ line: 1, enabled: true }, { line: 9, enabled: true }],
      "lib.py": [{ line: 4, enabled: true }],
    });
    expect(state.exceptionFilters).toEqual({ python: ["raised"] });
    expect(state.watches).toEqual(["w"]);
    expect(state.panelHeight).toBe(400);
    await vi.advanceTimersByTimeAsync(300);
    expect(lastSaved()[ROOT]!.breakpoints).toEqual(state.breakpoints);
  });

  it("keeps one breakpoint key per file whatever its case on case-insensitive platforms", async () => {
    const store = useDebugConfigsStore();
    await store.load();
    store.toggleBreakpoint(ROOT, "/Users/A/Proj/src/App.py", 1);
    store.toggleBreakpoint(ROOT, "/users/a/proj/SRC/app.py", 2);
    expect(Object.keys(store.stateFor(ROOT).breakpoints)).toEqual(["src/App.py"]);
    expect(store.breakpointsFor(ROOT, "/Users/A/Proj/src/app.py").map((bp) => bp.line)).toEqual([1, 2]);
    store.toggleBreakpoint(ROOT, "/Users/A/Proj/src/app.py", 1);
    expect(store.breakpointsFor(ROOT, "/Users/A/Proj/src/App.py").map((bp) => bp.line)).toEqual([2]);
  });

  it("normalizes the project root key and reuses one that matches ignoring case", async () => {
    vi.mocked(loadPersistedValue).mockResolvedValue({
      [ROOT]: { configs: [], selectedConfigId: null, breakpoints: {}, exceptionFilters: {}, watches: ["stored"], panelHeight: 200 },
    });
    const store = useDebugConfigsStore();
    await store.load();
    expect(store.stateFor("/users/a/proj/").watches).toEqual(["stored"]);
    store.toggleBreakpoint(`${ROOT}/`, FILE, 5);
    store.setPanelHeight("/USERS/A/PROJ", 300);
    store.setWatches("/Users/B/New//", ["n"]);
    await vi.advanceTimersByTimeAsync(300);
    expect(Object.keys(lastSaved()).sort()).toEqual(["/Users/A/Proj", "/Users/B/New"]);
    expect(lastSaved()[ROOT]).toMatchObject({ breakpoints: { "src/app.py": [{ line: 5, enabled: true }] }, panelHeight: 300, watches: ["stored"] });
  });
});
