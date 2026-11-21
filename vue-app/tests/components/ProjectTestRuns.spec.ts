import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { nextTick } from "vue";

const mocks = vi.hoisted(() => ({
  invoke: vi.fn(),
  start: vi.fn(),
  registerRestartHandler: vi.fn(),
  discoverTests: vi.fn(),
  runTests: vi.fn(),
  searchIndex: vi.fn(),
  refreshIndex: vi.fn(),
  listeners: new Map<string, Set<(payload: unknown) => void>>(),
  message: Object.assign(vi.fn(), { info: vi.fn(), warning: vi.fn(), error: vi.fn(), success: vi.fn() }),
  /** Overrides the keyboard platform (paths fold case except on linux). */
  platform: undefined as string | undefined,
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
vi.mock("@/stores/debug-sessions", () => ({
  useDebugSessionsStore: () => ({ start: mocks.start, registerRestartHandler: mocks.registerRestartHandler }),
}));
vi.mock("@/services/plugin-test-provider-service", () => ({ discoverTests: mocks.discoverTests, runTests: mocks.runTests }));
vi.mock("@/services/project-index", () => ({ searchProjectFileIndex: mocks.searchIndex, refreshProjectFileIndex: mocks.refreshIndex }));
vi.mock("element-plus", async (importOriginal) => ({ ...(await importOriginal<object>()), ElMessage: mocks.message }));
vi.mock("@/services/keyboard-platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/keyboard-platform")>();
  return { ...actual, currentKeyboardPlatform: () => mocks.platform ?? actual.currentKeyboardPlatform() };
});

import ProjectPluginTestsPanel from "@/components/projects/ProjectPluginTestsPanel.vue";
import ProjectTestRunDetails from "@/components/projects/ProjectTestRunDetails.vue";
import ProjectTestRunResults from "@/components/projects/ProjectTestRunResults.vue";
import { useTestDiscovery } from "@/stores/test-discovery";
import { createTestRunState, applyTestRunEvents, seedTestRunNodes } from "@/domain/testing/test-runs";
import { resetTestRunEvents, TEST_RUN_EVENT } from "@/services/plugin-test-runs";
import { usePluginTestProvidersStore } from "@/stores/plugin-test-providers";
import { useVueI18n } from "@/i18n/messages";

const { t } = useVueI18n();

const ROOT = "/Users/A/Proj";
const PROVIDER = { pluginId: "acme-tests", pluginName: "Acme", id: "unit", title: "Unit", runRisk: "read" as const, debugRun: { debuggerType: "acme" } };
const TESTS = [
  {
    id: "calc",
    label: "CalcSpec",
    path: "src/test/acme/CalcSpec.kt",
    line: 1,
    children: [
      { id: "calc#adds", label: "adds", path: "src/test/acme/CalcSpec.kt", line: 3 },
      { id: "calc#subs", label: "subs", path: "src/test/acme/CalcSpec.kt", line: 7 },
    ],
  },
];
const PREPARED = {
  prepareId: "p1",
  type: "acme",
  kind: "",
  projectRoot: ROOT,
  request: "launch",
  arguments: {},
  launch: { fingerprint: "f".repeat(64), cwd: ROOT, argv: ["acme"], env: {}, trusted: true },
};

let runCounter = 0;
let sessionCounter = 0;

function emit(runId: string, events: unknown[]) {
  for (const handler of mocks.listeners.get(TEST_RUN_EVENT) ?? []) handler({ runId, pluginId: "acme-tests", events });
}

async function mountPanel(provider: typeof PROVIDER | Omit<typeof PROVIDER, "debugRun"> = PROVIDER) {
  usePluginTestProvidersStore().setContributions([provider], []);
  const wrapper = mount(ProjectPluginTestsPanel, { props: { projectRoot: ROOT }, attachTo: document.body });
  await settle();
  return wrapper;
}

async function settle() {
  await flushPromises();
  await vi.dynamicImportSettled();
  await flushPromises();
}

function button(wrapper: VueWrapper, action: string) {
  return wrapper.get(`[data-action="${action}"]`);
}

function disabled(wrapper: VueWrapper, action: string) {
  return button(wrapper, action).attributes("disabled") !== undefined;
}

/** Renders the throttled tree update. */
async function render() {
  await vi.advanceTimersByTimeAsync(150);
  await nextTick();
}

beforeEach(() => {
  resetTestRunEvents();
  mocks.listeners.clear();
  runCounter = 0;
  sessionCounter = 0;
  for (const mock of [mocks.invoke, mocks.start, mocks.registerRestartHandler, mocks.discoverTests, mocks.runTests, mocks.searchIndex, mocks.refreshIndex]) mock.mockReset();
  for (const mock of [mocks.message, mocks.message.info, mocks.message.warning]) mock.mockReset();
  mocks.discoverTests.mockResolvedValue({ ok: true, response: { tests: TESTS, dropped: 0, truncated: false } });
  mocks.searchIndex.mockResolvedValue([]);
  mocks.refreshIndex.mockResolvedValue({ available: true, roots: [] });
  mocks.invoke.mockImplementation(async (command: string) => {
    if (command === "plugin_host_test_run_prepare") return { ...PREPARED, runId: `run-0${(runCounter += 1)}` };
    if (command === "plugin_host_test_run_stop") return { stopped: true };
    throw new Error(command);
  });
  mocks.start.mockImplementation(async (_root: string, _cfg: unknown, opts: { prepare: () => Promise<unknown> }) => {
    await opts.prepare();
    return `s${(sessionCounter += 1)}`;
  });
});

describe("Tests tab streaming runs", () => {
  it("runs all, shows live statuses, durations and the summary, and ends terminated without results", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const wrapper = await mountPanel();
    expect(wrapper.text()).toContain("CalcSpec");
    expect(disabled(wrapper, "run-all")).toBe(false);
    expect(disabled(wrapper, "stop")).toBe(true);
    expect(disabled(wrapper, "rerun-failed")).toBe(true);

    await button(wrapper, "run-all").trigger("click");
    await settle();
    expect(mocks.start).toHaveBeenCalledTimes(1);
    expect(mocks.start.mock.calls[0]![2]).toMatchObject({ noDebug: true });
    expect(mocks.invoke).toHaveBeenCalledWith("plugin_host_test_run_prepare", {
      payload: { pluginId: "acme-tests", providerId: "unit", projectRoot: ROOT, testIds: [], debug: false },
    });
    // While it runs: Run / Debug / Rerun are disabled, Stop is enabled.
    expect(disabled(wrapper, "run-all")).toBe(true);
    expect(disabled(wrapper, "debug-all")).toBe(true);
    expect(disabled(wrapper, "stop")).toBe(false);

    emit("run-01", [
      { type: "started", id: "calc#adds" },
      { type: "passed", id: "calc#adds", durationMs: 12 },
      { type: "started", id: "calc#subs" },
    ]);
    await render();
    expect(wrapper.get('[data-id="calc#adds"]').text()).toContain(t("pluginContrib.testProviders.durationMs", { ms: 12 }));
    expect(wrapper.find('[data-id="calc#subs"] .is-running').exists()).toBe(true);
    expect(wrapper.get('[data-test="summary"]').text()).toContain(t("pluginContrib.testProviders.summary.passed", { count: 1 }));
    expect(wrapper.get('[data-test="phase"]').text()).toContain(t("pluginContrib.testProviders.phase.running"));

    emit("run-01", [{ type: "ended", finished: false, reason: "sessionEnded" }]);
    await nextTick();
    await nextTick();
    expect(wrapper.get('[data-test="phase"]').text()).toContain(t("pluginContrib.testProviders.phase.terminated"));
    expect(wrapper.find('[data-id="calc#subs"] .is-terminated').exists()).toBe(true);
    expect(wrapper.get('[data-test="summary"]').text()).toContain(t("pluginContrib.testProviders.summary.terminated", { count: 1 }));
    expect(wrapper.find('[data-test="total-time"]').exists()).toBe(true);
    expect(disabled(wrapper, "run-all")).toBe(false);
    expect(disabled(wrapper, "stop")).toBe(true);
    wrapper.unmount();
    vi.useRealTimers();
  });

  it("debugs all, reruns only the failed tests, and stops a running run", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const wrapper = await mountPanel();
    await button(wrapper, "debug-all").trigger("click");
    await settle();
    expect(mocks.start.mock.calls[0]![2]).toMatchObject({ noDebug: false });
    emit("run-01", [
      { type: "passed", id: "calc#adds" },
      { type: "failed", id: "calc#subs", message: "expected 1" },
      { type: "finished", durationMs: 1500 },
      { type: "ended", finished: true, reason: "finished" },
    ]);
    await render();
    expect(wrapper.get('[data-test="phase"]').text()).toContain(t("pluginContrib.testProviders.phase.finished"));
    expect(wrapper.get('[data-test="total-time"]').text()).toContain(t("pluginContrib.testProviders.durationS", { s: "1.50" }));
    expect(disabled(wrapper, "rerun-failed")).toBe(false);

    await button(wrapper, "rerun-failed").trigger("click");
    await settle();
    // Rerun failed keeps the last run's mode (that one was a debug run).
    expect(mocks.invoke).toHaveBeenLastCalledWith("plugin_host_test_run_prepare", {
      payload: { pluginId: "acme-tests", providerId: "unit", projectRoot: ROOT, testIds: ["calc#subs"], debug: true },
    });
    expect(mocks.start.mock.calls[1]![1]).toMatchObject({ id: "test-run:acme-tests/unit", name: "subs" });
    // The previous run had ended: no "stopped" hint.
    expect(mocks.message.info).not.toHaveBeenCalled();
    await render();
    // Only the current run's tree is shown.
    expect(wrapper.find('[data-id="calc#adds"]').exists()).toBe(false);
    expect(wrapper.find('[data-id="calc#subs"]').exists()).toBe(true);

    await button(wrapper, "stop").trigger("click");
    await settle();
    expect(mocks.invoke).toHaveBeenLastCalledWith("plugin_host_test_run_stop", { payload: { runId: "run-02" } });
    await render();
    expect(wrapper.get('[data-test="phase"]').text()).toContain(t("pluginContrib.testProviders.phase.stopping"));
    expect(disabled(wrapper, "stop")).toBe(true);
    wrapper.unmount();
    vi.useRealTimers();
  });

  it("a new run while one runs stops the old one with a hint and shows only the new run", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const wrapper = await mountPanel();
    await button(wrapper, "run-all").trigger("click");
    await settle();
    // A second run of the same provider (e.g. from the editor gutter) supersedes it.
    const { launchTestRun } = await import("@/services/test-run-launcher");
    const result = await launchTestRun({ pluginId: "acme-tests", providerId: "unit", debuggerType: "acme", projectRoot: ROOT, testIds: ["calc#adds"], debug: false, name: "adds", discovered: [TESTS[0]!.children[0]!] });
    expect(result).toMatchObject({ status: "started", runId: "run-02" });
    expect(mocks.message.info).toHaveBeenCalledWith(expect.objectContaining({ message: t("pluginContrib.testProviders.superseded") }));
    // Late events of the superseded run do not change what is shown.
    emit("run-01", [{ type: "failed", id: "calc#subs" }]);
    emit("run-02", [{ type: "passed", id: "calc#adds" }]);
    await render();
    expect(wrapper.find('[data-id="calc#subs"]').exists()).toBe(false);
    expect(wrapper.find('[data-id="calc#adds"] .is-passed').exists()).toBe(true);

    // While a launch is still in flight a further start is refused with a hint.
    let release!: () => void;
    mocks.start.mockImplementationOnce(async (_root: string, _cfg: unknown, opts: { prepare: () => Promise<unknown> }) => {
      await opts.prepare();
      await new Promise<void>((resolve) => (release = resolve));
      return "s9";
    });
    const pending = launchTestRun({ pluginId: "acme-tests", providerId: "unit", debuggerType: "acme", projectRoot: ROOT, testIds: [], debug: false, name: "all" });
    await settle();
    expect(await launchTestRun({ pluginId: "acme-tests", providerId: "unit", debuggerType: "acme", projectRoot: ROOT, testIds: [], debug: false, name: "all" })).toEqual({ status: "busy" });
    expect(mocks.message.info).toHaveBeenLastCalledWith(expect.objectContaining({ message: t("pluginContrib.testProviders.busy") }));
    release();
    await pending;
    wrapper.unmount();
    vi.useRealTimers();
  });

  it("shows a truncation notice", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const wrapper = await mountPanel();
    await button(wrapper, "run-all").trigger("click");
    await settle();
    emit("run-01", [{ type: "truncated" }]);
    await render();
    expect(wrapper.get('[data-test="truncated"]').text()).toContain(t("pluginContrib.testProviders.runTruncated"));
    wrapper.unmount();
    vi.useRealTimers();
  });

  it("the node context menu runs or debugs that node", async () => {
    const wrapper = await mountPanel();
    await wrapper.get('[data-id="calc#adds"]').trigger("contextmenu");
    await settle();
    const debugItem = document.body.querySelector<HTMLElement>('[data-action="menu-debug"]');
    expect(debugItem?.textContent).toContain(t("pluginContrib.testProviders.debugOne", { name: "adds" }));
    debugItem!.click();
    await settle();
    expect(mocks.invoke).toHaveBeenCalledWith("plugin_host_test_run_prepare", {
      payload: { pluginId: "acme-tests", providerId: "unit", projectRoot: ROOT, testIds: ["calc#adds"], debug: true },
    });
    expect(mocks.start.mock.calls[0]![1]).toMatchObject({ name: "adds" });
    wrapper.unmount();
  });

  it("opening the tab after a run (e.g. from the gutter) shows its results until Refresh", async () => {
    const { useTestRuns } = await import("@/stores/test-runs");
    const runs = useTestRuns();
    runs.createRun({ runId: "run-0e", pluginId: "acme-tests", providerId: "unit", projectRoot: ROOT, debug: false, testIds: ["calc#adds"], startedAt: 1 }, [TESTS[0]!.children[0]!]);
    runs.endRun("run-0e", "sessionEnded");
    const wrapper = await mountPanel();
    expect(wrapper.find('[data-test="summary"]').exists()).toBe(true);
    expect(wrapper.find('[data-id="calc#subs"]').exists()).toBe(false);
    await wrapper.get(`button[aria-label="${t("pluginContrib.testProviders.refresh")}"]`).trigger("click");
    await settle();
    expect(wrapper.find('[data-test="summary"]').exists()).toBe(false);
    expect(wrapper.find('[data-id="calc#subs"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it("the tree is keyboard navigable and collapsible", async () => {
    const wrapper = await mountPanel();
    const item = (id: string) => wrapper.get(`[data-id="${id}"]`).element.closest<HTMLElement>('[role="treeitem"]')!;
    expect(item("calc").getAttribute("aria-expanded")).toBe("true");
    item("calc").dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    await nextTick();
    expect(item("calc#adds").getAttribute("aria-selected")).toBe("true");
    item("calc#adds").dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(wrapper.emitted("open-file")).toEqual([["src/test/acme/CalcSpec.kt", 3]]);
    item("calc#adds").dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    await nextTick();
    item("calc").dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    await nextTick();
    expect(item("calc").getAttribute("aria-expanded")).toBe("false");
    expect(wrapper.find('[data-id="calc#adds"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("reuses a fresh discovery when the tab opens, and keeps its notice in sync", async () => {
    const first = await mountPanel();
    first.unmount();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(1);
    // Reopened within 30 s without a save: the kept discovery is shown.
    const second = await mountPanel();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(1);
    expect(second.text()).toContain("CalcSpec");
    // The editor discovers again after a save: the panel's notice follows.
    useTestDiscovery().set(PROVIDER, ROOT, { tests: TESTS, dropped: 0, truncated: true }, "zh");
    await nextTick();
    expect(second.text()).toContain(t("pluginContrib.testProviders.truncated"));
    second.unmount();
    // A save since makes it stale: opening the tab discovers again.
    useTestDiscovery().markStale(ROOT);
    const third = await mountPanel();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(2);
    third.unmount();
  });

  it("a discovery started before a save is not shared by the one after it, and stays stale", async () => {
    const releases: ((value: unknown) => void)[] = [];
    mocks.discoverTests.mockImplementation(() => new Promise((resolve) => releases.push(resolve)));
    const discovery = useTestDiscovery();
    const ok = (label: string) => ({ ok: true, response: { tests: [{ id: "t", label }], dropped: 0, truncated: false } });
    const before = discovery.discover(PROVIDER, ROOT, "en");
    discovery.markStale(ROOT);
    const after = discovery.discover(PROVIDER, ROOT, "en");
    expect(mocks.discoverTests).toHaveBeenCalledTimes(2);
    // The same generation still shares.
    void discovery.discover(PROVIDER, ROOT, "en");
    expect(mocks.discoverTests).toHaveBeenCalledTimes(2);
    releases[1]!(ok("after save"));
    await after;
    releases[0]!(ok("before save"));
    await before;
    expect(discovery.entry(PROVIDER, ROOT)?.tests[0]?.label).toBe("after save");
    expect(discovery.isFresh(PROVIDER, ROOT, "en")).toBe(true);

    // Only a pre-save result arrives: it is kept but stale.
    discovery.markStale(ROOT);
    const old = discovery.discover(PROVIDER, ROOT, "en");
    discovery.markStale(ROOT);
    releases[2]!(ok("old"));
    await old;
    expect(discovery.entry(PROVIDER, ROOT)?.tests[0]?.label).toBe("old");
    expect(discovery.isFresh(PROVIDER, ROOT, "en")).toBe(false);
  });

  it("providers without debugRun keep the synchronous run", async () => {
    const { debugRun: _unused, ...syncProvider } = PROVIDER;
    mocks.runTests.mockResolvedValue({ ok: true, response: { results: [{ id: "calc#adds", status: "passed" }], outputTruncated: false, dropped: 0, truncated: false } });
    const wrapper = await mountPanel(syncProvider);
    expect(wrapper.find('[data-action="debug-all"]').exists()).toBe(false);
    await wrapper.get(`button[aria-label="${t("pluginContrib.testProviders.runAll")}"]`).trigger("click");
    await settle();
    expect(mocks.runTests).toHaveBeenCalledTimes(1);
    expect(mocks.start).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});

describe("results tree accessibility", () => {
  const MANY = [{ id: "suite", label: "Suite", children: Array.from({ length: 100 }, (_, index) => ({ id: `t${index}`, label: `test ${index}` })) }];

  function items(wrapper: VueWrapper) {
    return wrapper.findAll('[role="treeitem"]').map((item) => item.element as HTMLElement);
  }

  it("rows carry level, position and set size; Home/End/Page keys move and keep focus on far jumps", async () => {
    const wrapper = mount(ProjectTestRunResults, { props: { tests: MANY, projectRoot: ROOT, busy: false }, attachTo: document.body });
    await nextTick();
    const row = (id: string) => wrapper.get(`[data-id="${id}"]`).element.closest<HTMLElement>('[role="treeitem"]')!;
    expect(row("t3").getAttribute("aria-level")).toBe("2");
    expect(row("t3").getAttribute("aria-posinset")).toBe("4");
    expect(row("t3").getAttribute("aria-setsize")).toBe("100");
    // Only one tab stop.
    expect(items(wrapper).filter((item) => item.tabIndex === 0)).toHaveLength(1);

    row("suite").dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    await nextTick();
    await nextTick();
    expect(document.activeElement?.querySelector('[data-id]')?.getAttribute("data-id")).toBe("t99");
    // A far jump (ArrowLeft to the parent at the top) renders and focuses the target.
    (document.activeElement as HTMLElement).dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    await nextTick();
    await nextTick();
    expect(document.activeElement?.querySelector('[data-id]')?.getAttribute("data-id")).toBe("suite");
    (document.activeElement as HTMLElement).dispatchEvent(new KeyboardEvent("keydown", { key: "PageDown", bubbles: true }));
    await nextTick();
    await nextTick();
    const paged = document.activeElement?.querySelector('[data-id]')?.getAttribute("data-id");
    expect(paged).toMatch(/^t\d+$/);
    expect(Number(paged!.slice(1))).toBeGreaterThan(5);
    (document.activeElement as HTMLElement).dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    await nextTick();
    await nextTick();
    expect(document.activeElement?.querySelector('[data-id]')?.getAttribute("data-id")).toBe("suite");
    wrapper.unmount();
  });

  it("keeps a tab stop when the selected row scrolls out of view", async () => {
    const wrapper = mount(ProjectTestRunResults, { props: { tests: MANY, projectRoot: ROOT, busy: false }, attachTo: document.body });
    await nextTick();
    const first = wrapper.get('[data-id="suite"]').element.closest<HTMLElement>('[role="treeitem"]')!;
    first.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    await nextTick();
    await nextTick();
    // Scroll back to the top: t99 is no longer rendered, the first rendered row is the tab stop.
    const wrap = wrapper.get(".el-scrollbar__wrap").element as HTMLElement;
    wrap.scrollTop = 0;
    wrap.dispatchEvent(new Event("scroll"));
    await nextTick();
    expect(wrapper.find('[data-id="t99"]').exists()).toBe(false);
    const stops = items(wrapper).filter((item) => item.tabIndex === 0);
    expect(stops).toHaveLength(1);
    expect(stops[0]!.querySelector("[data-id]")!.getAttribute("data-id")).toBe("suite");
    wrapper.unmount();
  });
});

describe("test details", () => {
  function runWithFailure(trace: string) {
    const run = createTestRunState({ runId: "run-0f", pluginId: "acme-tests", providerId: "unit", projectRoot: ROOT, debug: false, testIds: [], startedAt: 1 });
    seedTestRunNodes(run, TESTS);
    applyTestRunEvents(run, [
      { type: "failed", id: "calc#subs", durationMs: 4, message: "<b>expected</b> 1 but was 2", expected: "1", actual: "2", trace },
      { type: "output", id: "calc#subs", text: "log line\n" },
    ]);
    return Object.freeze({ ...run });
  }

  it("shows message, expected/actual, output and stack trace links; never renders HTML", async () => {
    mocks.searchIndex.mockResolvedValue([{ root: ROOT, path: "src/main/acme/Calc.kt", name: "Calc.kt", size: 1, modifiedAt: 1 }]);
    const trace = [
      "AssertionFailed: expected 1",
      "\tat acme.CalcSpec.subs(CalcSpec.kt:9)",
      "\tat acme.Calc.sub(Calc.kt:4)",
      "\tat lib.Helper.go(Helper.kt:2)",
      "\tat lib (acme://lib.jar/lib/Helper.class)",
    ].join("\n");
    const wrapper = mount(ProjectTestRunDetails, { props: { run: runWithFailure(trace), nodeId: "calc#subs", tests: TESTS, projectRoot: ROOT } });
    await flushPromises();
    await flushPromises();
    expect(wrapper.get('[data-test="message"]').text()).toBe("<b>expected</b> 1 but was 2");
    expect(wrapper.find('[data-test="message"] b').exists()).toBe(false);
    expect(wrapper.get('[data-test="expected"]').text()).toBe("1");
    expect(wrapper.get('[data-test="actual"]').text()).toBe("2");
    expect(wrapper.get('[data-test="output"]').text()).toBe("log line");

    const links = wrapper.findAll('[data-link="file"]');
    // The test file resolves from the run's own paths, Calc.kt through the project index; Helper.kt does not resolve.
    expect(links.map((link) => link.text())).toEqual(["CalcSpec.kt:9", "Calc.kt:4"]);
    expect(wrapper.get('[data-test="trace"]').text()).toContain("Helper.kt:2");
    expect(mocks.searchIndex).toHaveBeenCalledWith([ROOT], "Calc.kt", expect.any(Number));
    await links[0]!.trigger("click");
    await links[1]!.trigger("click");
    expect(wrapper.emitted("open-file")).toEqual([
      ["src/test/acme/CalcSpec.kt", 9],
      ["src/main/acme/Calc.kt", 4],
    ]);
    await wrapper.get('[data-link="address"]').trigger("click");
    expect(wrapper.emitted("open-address")).toEqual([["acme://lib.jar/lib/Helper.class"]]);

    await wrapper.get('[data-action="go-to-source"]').trigger("click");
    expect(wrapper.emitted("open-file")!.at(-1)).toEqual(["src/test/acme/CalcSpec.kt", 7]);

    // A live update of the run with the same trace: no new lookups, links stay.
    const searches = mocks.searchIndex.mock.calls.length;
    await wrapper.setProps({ run: runWithFailure(trace) });
    await flushPromises();
    expect(mocks.searchIndex.mock.calls.length).toBe(searches);
    expect(wrapper.findAll('[data-link="file"]').map((link) => link.text())).toEqual(["CalcSpec.kt:9", "Calc.kt:4"]);
  });

  it("a full index page links when its same-name hits end before the page does, else it is ambiguous", async () => {
    const trace = "\tat acme.Calc.sub(Calc.kt:4)";
    const fuzzy = (index: number) => ({ root: ROOT, path: `src/x${index}/CalcHelper.kt`, name: "CalcHelper.kt", size: 1, modifiedAt: 1 });
    // One exact hit first (exact names sort first), the rest fuzzy: every same-name file is on the page.
    mocks.searchIndex.mockResolvedValue([{ root: ROOT, path: "src/main/acme/Calc.kt", name: "Calc.kt", size: 1, modifiedAt: 1 }, ...Array.from({ length: 49 }, (_, index) => fuzzy(index))]);
    const linked = mount(ProjectTestRunDetails, { props: { run: runWithFailure(trace), nodeId: "calc#subs", tests: TESTS, projectRoot: ROOT } });
    await flushPromises();
    await flushPromises();
    expect(linked.findAll('[data-link="file"]').map((link) => link.text())).toEqual(["Calc.kt:4"]);
    linked.unmount();

    // A page of only same-name hits may hide more: not linked.
    mocks.searchIndex.mockResolvedValue(Array.from({ length: 50 }, (_, index) => ({ root: ROOT, path: index ? `x${index}/Calc.kt` : "src/main/acme/Calc.kt", name: "Calc.kt", size: 1, modifiedAt: 1 })));
    const ambiguous = mount(ProjectTestRunDetails, { props: { run: runWithFailure(trace), nodeId: "calc#subs", tests: TESTS, projectRoot: ROOT } });
    await flushPromises();
    await flushPromises();
    expect(ambiguous.findAll('[data-link="file"]')).toHaveLength(0);
    expect(ambiguous.get('[data-test="trace"]').text()).toContain("Calc.kt:4");
    ambiguous.unmount();

    // Case-sensitive paths: the index ranks case variants as exact names too, so a full page
    // ending in "calc.kt" may hide another "Calc.kt": not linked either.
    mocks.platform = "linux";
    try {
      mocks.searchIndex.mockResolvedValue([
        { root: ROOT, path: "src/main/acme/Calc.kt", name: "Calc.kt", size: 1, modifiedAt: 1 },
        ...Array.from({ length: 49 }, (_, index) => ({ root: ROOT, path: `x${index}/calc.kt`, name: "calc.kt", size: 1, modifiedAt: 1 })),
      ]);
      const variants = mount(ProjectTestRunDetails, { props: { run: runWithFailure(trace), nodeId: "calc#subs", tests: TESTS, projectRoot: ROOT } });
      await flushPromises();
      await flushPromises();
      expect(variants.findAll('[data-link="file"]')).toHaveLength(0);
      variants.unmount();
    } finally {
      mocks.platform = undefined;
    }
  });
});
