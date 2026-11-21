import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia, type Pinia } from "pinia";
import { mount } from "@vue/test-utils";
import { computed, defineComponent, h, nextTick } from "vue";

const mocks = vi.hoisted(() => ({
  invoke: vi.fn(),
  start: vi.fn(),
  listeners: new Map<string, Set<(payload: unknown) => void>>(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  canInvoke: () => true,
  invokeBackend: mocks.invoke,
  listenAppEvent: vi.fn((name: string, handler: (payload: unknown) => void) => {
    const set = mocks.listeners.get(name) ?? new Set();
    set.add(handler);
    mocks.listeners.set(name, set);
    return () => set.delete(handler);
  }),
}));
vi.mock("@/stores/debug-sessions", () => ({ useDebugSessionsStore: () => ({ start: mocks.start }) }));

import { resetTestRunEvents, startTestRun, stopTestRun, TEST_RUN_EVENT, testRunConfiguration } from "@/services/plugin-test-runs";
import { parseTestRunEventPayload } from "@/domain/testing/test-runs";
import { useTestRuns } from "@/stores/test-runs";

function emitPayload(runs: ReturnType<typeof useTestRuns>, raw: unknown) {
  const payload = parseTestRunEventPayload(raw);
  if (!payload) throw new Error("invalid payload");
  runs.applyPayload(payload);
}

const ROOT = "/Users/A/Proj";
const RUN = "run-00aa11bb";
const PREPARED = {
  prepareId: "p1",
  type: "java",
  kind: "jdtls",
  projectRoot: ROOT,
  request: "launch",
  arguments: { mainClass: "Runner", noDebug: true },
  launch: { fingerprint: "f".repeat(64), cwd: ROOT, argv: ["jdtls"], env: {}, trusted: true },
  runId: RUN,
};

function emit(payload: unknown) {
  for (const handler of mocks.listeners.get(TEST_RUN_EVENT) ?? []) handler(payload);
}

const request = {
  pluginId: "java-debug",
  providerId: "junit",
  projectRoot: ROOT,
  testIds: ["pkg.CalcTest"],
  debug: false,
  debuggerType: "java",
  name: "CalcTest",
  discovered: [{ id: "pkg.CalcTest", label: "CalcTest", children: [{ id: "pkg.CalcTest#adds", label: "adds()" }, { id: "pkg.CalcTest#subs", label: "subs()" }] }],
};

describe("plugin test runs", () => {
  let pinia: Pinia;
  beforeEach(() => {
    pinia = createPinia();
    setActivePinia(pinia);
    resetTestRunEvents();
    mocks.listeners.clear();
    mocks.invoke.mockReset();
    mocks.start.mockReset();
  });

  it("starts through the debug launch path with only the prepare step replaced, and merges events", async () => {
    mocks.invoke.mockImplementation(async (command: string) => {
      if (command === "plugin_host_test_run_prepare") return PREPARED;
      throw new Error(command);
    });
    mocks.start.mockImplementation(async (_root: string, _cfg: unknown, opts: { prepare: () => Promise<unknown> }) => {
      // An event can arrive before prepare returns the run id: it is buffered.
      emit({ runId: RUN, pluginId: "java-debug", events: [{ type: "started", id: "pkg.CalcTest#adds" }] });
      await opts.prepare();
      return "session-1";
    });
    const result = await startTestRun(request);
    expect(result).toEqual({ status: "started", runId: RUN, sessionId: "session-1" });
    expect(mocks.invoke).toHaveBeenCalledWith("plugin_host_test_run_prepare", {
      payload: { pluginId: "java-debug", providerId: "junit", projectRoot: ROOT, testIds: ["pkg.CalcTest"], debug: false },
    });
    const [root, cfg, opts] = mocks.start.mock.calls[0]!;
    expect(root).toBe(ROOT);
    // A transient configuration, never saved: the debug launch path takes it as is.
    expect(cfg).toEqual(testRunConfiguration(request));
    expect(opts.noDebug).toBe(true);

    const runs = useTestRuns();
    const run = runs.runFor(RUN)!;
    expect(run.sessionId).toBe("session-1");
    expect(run.phase).toBe("running");
    expect(run.nodes["pkg.CalcTest#adds"]!.status).toBe("running");
    expect(run.nodes["pkg.CalcTest#subs"]!.status).toBe("pending");

    emit({
      runId: RUN,
      pluginId: "java-debug",
      events: [
        { type: "failed", id: "pkg.CalcTest#adds", message: "expected 2", expected: "2", actual: "3" },
        { type: "tree", id: "pkg.CalcTest#param[1]", parentId: "pkg.CalcTest", label: "[1]", kind: "test" },
        { type: "passed", id: "pkg.CalcTest#param[1]" },
      ],
    });
    // A payload naming another plugin is ignored.
    emit({ runId: RUN, pluginId: "other", events: [{ type: "passed", id: "pkg.CalcTest#adds" }] });
    expect(runs.runFor(RUN)!.nodes["pkg.CalcTest#adds"]!.status).toBe("failed");
    expect(runs.failedTestIds(RUN)).toEqual(["pkg.CalcTest#adds"]);
    expect(runs.summary(RUN)).toMatchObject({ total: 3, failed: 1, passed: 1, pending: 1 });
    expect(runs.latestRun("java-debug", "junit", ROOT)?.runId).toBe(RUN);

    // The session ended without finished: what never got a result is terminated.
    emit({ runId: RUN, pluginId: "java-debug", events: [{ type: "ended", finished: false, reason: "sessionEnded" }] });
    expect(runs.runFor(RUN)!.nodes["pkg.CalcTest#subs"]!.status).toBe("terminated");
    expect(runs.statuses(RUN).get("pkg.CalcTest")).toBe("failed");
  });

  it("ends and stops a prepared run whose launch was declined", async () => {
    mocks.invoke.mockImplementation(async (command: string) => {
      if (command === "plugin_host_test_run_prepare") return PREPARED;
      if (command === "plugin_host_test_run_stop") return { stopped: true };
      throw new Error(command);
    });
    mocks.start.mockImplementation(async (_root: string, _cfg: unknown, opts: { prepare: () => Promise<unknown> }) => {
      await opts.prepare();
      return null;
    });
    expect(await startTestRun(request)).toEqual({ status: "notStarted", runId: RUN });
    expect(mocks.invoke).toHaveBeenCalledWith("plugin_host_test_run_stop", { payload: { runId: RUN } });
    const run = useTestRuns().runFor(RUN)!;
    expect(run.phase).toBe("ended");
    expect(run.endReason).toBe("cancelled");
    expect(run.nodes["pkg.CalcTest#adds"]!.status).toBe("terminated");
  });

  it("answers busy while a run of the same provider and project is launching", async () => {
    let release!: (value: string) => void;
    mocks.invoke.mockResolvedValue(PREPARED);
    mocks.start.mockImplementation(() => new Promise<string>((resolve) => (release = resolve)));
    const first = startTestRun(request);
    expect(await startTestRun(request)).toEqual({ status: "busy" });
    // Another project is independent.
    mocks.start.mockResolvedValueOnce(null);
    expect((await startTestRun({ ...request, projectRoot: "/other" })).status).toBe("notStarted");
    release("session-1");
    expect((await first).status).toBe("notStarted");
    expect(mocks.start).toHaveBeenCalledTimes(2);
  });

  it("rejects an invalid prepare result and reports stop results", async () => {
    mocks.invoke.mockResolvedValueOnce({ ...PREPARED, runId: "x" });
    mocks.start.mockImplementation(async (_root: string, _cfg: unknown, opts: { prepare: () => Promise<unknown> }) => {
      await expect(opts.prepare()).rejects.toThrow(/invalid run id/);
      return null;
    });
    expect(await startTestRun(request)).toEqual({ status: "notStarted" });
    mocks.invoke.mockResolvedValueOnce({ stopped: false });
    expect(await stopTestRun(RUN)).toBe(false);
  });

  it("merges 10000 dynamic nodes through the store quickly, results first and in publish-sized batches", () => {
    const runs = useTestRuns();
    runs.createRun({ runId: RUN, pluginId: "java-debug", providerId: "junit", projectRoot: ROOT, debug: false, testIds: [], startedAt: 0 });
    const started = performance.now();
    const batch = (events: unknown[]) => {
      for (let index = 0; index < events.length; index += 500) {
        emitPayload(runs, { runId: RUN, pluginId: "java-debug", events: events.slice(index, index + 500) });
      }
    };
    // Out of order on purpose: every result arrives before its tree node.
    batch(Array.from({ length: 9999 }, (_, index) => ({ type: "passed", id: `p[${index}]`, durationMs: 1 })));
    batch([
      { type: "tree", id: "p", label: "param", kind: "suite" },
      ...Array.from({ length: 9999 }, (_, index) => ({ type: "tree", id: `p[${index}]`, parentId: "p", label: `[${index}]`, kind: "test" })),
    ]);
    const run = runs.runFor(RUN)!;
    const summary = runs.summary(RUN)!;
    const elapsed = performance.now() - started;
    expect(run.nodeCount).toBe(10000);
    expect(run.roots).toEqual(["p"]);
    expect(run.nodes.p!.children.length).toBe(9999);
    expect(summary.passed).toBe(9999);
    // Measured ~30 ms on the development machine; 500 ms leaves a wide margin.
    expect(elapsed).toBeLessThan(500);
  });

  it("shows the newest run per provider and project; late events of a superseded run stay in it", () => {
    const runs = useTestRuns();
    const init = { pluginId: "java-debug", providerId: "junit", projectRoot: ROOT, debug: false, testIds: [], startedAt: 0 };
    runs.createRun({ ...init, runId: "run-0a" });
    runs.createRun({ ...init, runId: "run-0b" });
    runs.createRun({ ...init, projectRoot: "/other", runId: "run-0c" });
    emitPayload(runs, { runId: "run-0a", pluginId: "java-debug", events: [{ type: "failed", id: "late" }] });
    const current = runs.currentRun("java-debug", "junit", ROOT)!;
    expect(current.runId).toBe("run-0b");
    expect(current.nodes.late).toBeUndefined();
    expect(runs.runFor("run-0a")!.nodes.late!.status).toBe("failed");
    expect(runs.currentRun("java-debug", "junit", "/other")!.runId).toBe("run-0c");
  });

  it("marks a stopped run stopping until the host ends it", async () => {
    const runs = useTestRuns();
    runs.createRun({ runId: RUN, pluginId: "java-debug", providerId: "junit", projectRoot: ROOT, debug: false, testIds: [], startedAt: 0 });
    runs.bindSession(RUN, "session-1");
    const revision = runs.revisions[RUN];
    mocks.invoke.mockResolvedValueOnce({ stopped: true });
    expect(await stopTestRun(RUN)).toBe(true);
    expect(runs.runFor(RUN)!.stopping).toBe(true);
    expect(runs.revisions[RUN]).toBeGreaterThan(revision!);
    emitPayload(runs, { runId: RUN, pluginId: "java-debug", events: [{ type: "ended", finished: false, reason: "stopped" }] });
    expect(runs.runFor(RUN)!.stopping).toBe(false);
    expect(runs.runFor(RUN)!.phase).toBe("ended");
  });

  it("re-renders a component whose computed reads currentRun after every merged payload", async () => {
    const runs = useTestRuns();
    const Probe = defineComponent({
      setup() {
        const store = useTestRuns();
        const run = computed(() => store.currentRun("java-debug", "junit", ROOT));
        return () => h("p", run.value ? `${run.value.phase}|${run.value.nodeCount}|${run.value.stopping}|${run.value.nodes.a?.status ?? "-"}` : "none");
      },
    });
    const wrapper = mount(Probe, { global: { plugins: [pinia] } });
    expect(wrapper.text()).toBe("none");
    runs.createRun({ runId: RUN, pluginId: "java-debug", providerId: "junit", projectRoot: ROOT, debug: false, testIds: [], startedAt: 0 });
    await nextTick();
    expect(wrapper.text()).toBe("preparing|0|false|-");
    runs.bindSession(RUN, "session-1");
    emitPayload(runs, { runId: RUN, pluginId: "java-debug", events: [{ type: "started", id: "a" }, { type: "tree", id: "b", label: "b", kind: "test" }] });
    await nextTick();
    expect(wrapper.text()).toBe("running|2|false|running");
    emitPayload(runs, { runId: RUN, pluginId: "java-debug", events: [{ type: "passed", id: "a" }] });
    await nextTick();
    expect(wrapper.text()).toBe("running|2|false|passed");
    runs.markStopping(RUN);
    await nextTick();
    expect(wrapper.text()).toBe("running|2|true|passed");
    emitPayload(runs, { runId: RUN, pluginId: "java-debug", events: [{ type: "ended", finished: false, reason: "stopped" }] });
    await nextTick();
    expect(wrapper.text()).toBe("ended|2|false|passed");
    // The snapshot is read-only and stable within one revision.
    expect(Object.isFrozen(runs.runFor(RUN))).toBe(true);
    expect(runs.runFor(RUN)).toBe(runs.runFor(RUN));
    wrapper.unmount();
  });

  it("clears stopping when the stop request fails", async () => {
    const runs = useTestRuns();
    runs.createRun({ runId: RUN, pluginId: "java-debug", providerId: "junit", projectRoot: ROOT, debug: false, testIds: [], startedAt: 0 });
    mocks.invoke.mockRejectedValueOnce(new Error("unavailable: host"));
    await expect(stopTestRun(RUN)).rejects.toThrow(/unavailable/);
    expect(runs.runFor(RUN)!.stopping).toBe(false);
  });

  it("evicts old ended runs and drops invalid payloads", () => {
    const runs = useTestRuns();
    for (let index = 0; index < 20; index += 1) {
      const runId = `run-${index.toString(16).padStart(4, "0")}`;
      runs.createRun({ runId, pluginId: "p", providerId: "junit", projectRoot: ROOT, debug: false, testIds: [], startedAt: index });
      if (index < 18) runs.endRun(runId, "finished");
    }
    expect(runs.order.length).toBe(16);
    expect(runs.runFor("run-0013")).toBeDefined();
    expect(runs.runFor("run-0000")).toBeUndefined();
  });

  it("drops an evicted run's snapshot (a reused run id never shows the old run)", () => {
    const runs = useTestRuns();
    runs.createRun({ runId: "run-00ff", pluginId: "p", providerId: "old", projectRoot: ROOT, debug: false, testIds: [], startedAt: 0 });
    expect(runs.runFor("run-00ff")!.providerId).toBe("old");
    for (let index = 0; index < 16; index += 1) {
      runs.createRun({ runId: `run-1${index.toString(16)}`, pluginId: "p", providerId: "junit", projectRoot: ROOT, debug: false, testIds: [], startedAt: index + 1 });
    }
    expect(runs.runFor("run-00ff")).toBeUndefined();
    runs.createRun({ runId: "run-00ff", pluginId: "p", providerId: "new", projectRoot: ROOT, debug: false, testIds: [], startedAt: 99 });
    expect(runs.runFor("run-00ff")!.providerId).toBe("new");
  });

  it("finds the run of a session and the last result of tests across runs", () => {
    const runs = useTestRuns();
    runs.createRun({ runId: "run-0a", pluginId: "p", providerId: "u", projectRoot: ROOT, debug: true, testIds: ["t1"], startedAt: 0 }, [{ id: "t1", label: "t1" }, { id: "t2", label: "t2" }]);
    runs.bindSession("run-0a", "s1");
    emitPayload(runs, { runId: "run-0a", pluginId: "p", events: [{ type: "failed", id: "t1" }, { type: "ended", finished: false, reason: "sessionEnded" }] });
    expect(runs.runForSession("s1")).toMatchObject({ runId: "run-0a", testIds: ["t1"], debug: true });
    expect(runs.runForSession("s9")).toBeUndefined();
    runs.createRun({ runId: "run-0b", pluginId: "p", providerId: "u", projectRoot: ROOT, debug: false, testIds: ["t2"], startedAt: 1 }, [{ id: "t2", label: "t2" }]);
    expect(Object.fromEntries(runs.lastStatuses("p", "u", ROOT, ["t1", "t2", "t3"]))).toEqual({ t1: "failed", t2: "pending" });
    expect(runs.lastStatuses("p", "other", ROOT, ["t1"]).size).toBe(0);
  });
});
