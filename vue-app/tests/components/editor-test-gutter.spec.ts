import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick, ref } from "vue";
import { mount } from "@vue/test-utils";

const mocks = vi.hoisted(() => ({
  listDebuggers: vi.fn(),
  debuggerEntryPoints: vi.fn(),
  discoverTests: vi.fn(),
  launchTestRun: vi.fn(),
  ensureTestRunRestart: vi.fn(),
  message: { info: vi.fn(), warning: vi.fn(), error: vi.fn(), success: vi.fn() },
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  canInvoke: () => true,
  invokeBackend: vi.fn(),
  listenAppEvent: vi.fn(() => () => undefined),
}));
vi.mock("@/services/debug-service", () => ({
  listDebuggers: mocks.listDebuggers,
  debuggerEntryPoints: mocks.debuggerEntryPoints,
  debugRequest: vi.fn().mockResolvedValue({}),
  setSessionBreakpoints: vi.fn(),
  stopDebugSession: vi.fn(),
  onDebugEvent: () => () => undefined,
}));
vi.mock("@/services/plugin-test-provider-service", () => ({ discoverTests: mocks.discoverTests, runTests: vi.fn() }));
vi.mock("@/services/test-run-launcher", () => ({ launchTestRun: mocks.launchTestRun, ensureTestRunRestart: mocks.ensureTestRunRestart }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("element-plus", async (importOriginal) => ({ ...(await importOriginal<object>()), ElMessage: mocks.message }));

import { useProjectEditorDebug } from "@/composables/use-project-editor-debug";
import { buildDebugDecorations } from "@/composables/use-editor-debug-decorations";
import { parseTestRunEventPayload } from "@/domain/testing/test-runs";
import { usePluginTestProvidersStore } from "@/stores/plugin-test-providers";
import { useTestRuns } from "@/stores/test-runs";
import { TEST_DISCOVERY_RETRY_BASE_MS, TEST_DISCOVERY_RETRY_LIMIT } from "@/stores/test-discovery";

const ROOT = "/Users/A/Proj";
const FILE = `${ROOT}/src/calc.spec.ts`;
const OTHER = `${ROOT}/src/other.ts`;
const PROVIDER = { pluginId: "acme-tests", pluginName: "Acme", id: "unit", title: "Unit", runRisk: "read" as const, debugRun: { debuggerType: "acme" } };
const TESTS = [
  {
    id: "calc",
    label: "calc",
    path: "src/calc.spec.ts",
    line: 1,
    children: [
      { id: "calc#adds", label: "adds", path: "src/calc.spec.ts", line: 3 },
      { id: "calc#subs", label: "subs", path: "src/calc.spec.ts", line: 7 },
    ],
  },
];

function discovered(tests = TESTS) {
  return { ok: true, response: { tests, dropped: 0, truncated: false } };
}

const flush = async () => {
  for (let i = 0; i < 6; i += 1) await Promise.resolve();
};

function harness(path = ref(FILE)) {
  let api!: ReturnType<typeof useProjectEditorDebug>;
  const Harness = defineComponent({
    setup() {
      api = useProjectEditorDebug({
        activeDocument: () => ({ path: path.value, loaded: true }),
        projectRootForPath: () => ROOT,
        languageIdFor: () => "typescript",
        editorReady: () => true,
        active: () => true,
        ownsProject: () => true,
        openAt: vi.fn(),
        openVirtualSource: () => undefined,
      });
      return () => h("div");
    },
  });
  const wrapper = mount(Harness);
  return { wrapper, api: () => api, path };
}

beforeEach(() => {
  vi.useFakeTimers();
  for (const mock of [mocks.listDebuggers, mocks.debuggerEntryPoints, mocks.discoverTests, mocks.launchTestRun, mocks.ensureTestRunRestart]) mock.mockReset();
  mocks.listDebuggers.mockResolvedValue([]);
  mocks.debuggerEntryPoints.mockResolvedValue([]);
  mocks.launchTestRun.mockResolvedValue({ status: "started", runId: "run-01", sessionId: "s1" });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("editor gutter test buttons", () => {
  it("shows run buttons on the discovered tests' lines and starts the clicked test", async () => {
    usePluginTestProvidersStore().setContributions([PROVIDER], []);
    mocks.discoverTests.mockResolvedValue(discovered());
    const { wrapper, api } = harness();
    await flush();
    // Restart of test-run sessions is registered with the workspace's debug feature, before any launch.
    expect(mocks.ensureTestRunRestart).toHaveBeenCalledTimes(1);
    expect(mocks.discoverTests).toHaveBeenCalledTimes(1);
    expect(mocks.discoverTests).toHaveBeenCalledWith(PROVIDER, { projectRoot: ROOT, locale: expect.any(String) });
    await vi.advanceTimersByTimeAsync(600);
    expect(api().editorProps.value.entryPoints).toEqual([
      { line: 1, label: "calc" },
      { line: 3, label: "adds" },
      { line: 7, label: "subs" },
    ]);
    expect(api().editorProps.value.entryPointStates).toEqual({});

    api().openEntryMenu(3, { x: 1, y: 2 });
    expect(api().entryMenu.value?.items.map((item) => item.label)).toEqual(["adds"]);
    const key = api().entryMenu.value!.items[0]!.key;
    await api().runEntry(key, false);
    expect(mocks.launchTestRun).toHaveBeenCalledWith({
      pluginId: "acme-tests",
      providerId: "unit",
      debuggerType: "acme",
      projectRoot: ROOT,
      testIds: ["calc#adds"],
      debug: true,
      name: "adds",
      discovered: [TESTS[0]!.children[0]],
    });
    expect(api().entryMenu.value).toBeNull();
    wrapper.unmount();
  });

  it("shows each test's last result and keeps the markers when only results change", async () => {
    usePluginTestProvidersStore().setContributions([PROVIDER], []);
    mocks.discoverTests.mockResolvedValue(discovered());
    const { wrapper, api } = harness();
    await flush();
    const markers = api().editorProps.value.entryPoints;
    const runs = useTestRuns();
    runs.createRun({ runId: "run-0a", pluginId: "acme-tests", providerId: "unit", projectRoot: ROOT, debug: false, testIds: [], startedAt: 1 }, TESTS);
    runs.bindSession("run-0a", "s1");
    const apply = (events: unknown[]) => runs.applyPayload(parseTestRunEventPayload({ runId: "run-0a", pluginId: "acme-tests", events })!);
    apply([{ type: "started", id: "calc#adds" }]);
    await nextTick();
    // Throttled like the Tests tab: nothing changes until the interval passed.
    expect(api().editorProps.value.entryPointStates).toEqual({});
    const lastStatuses = vi.spyOn(runs, "lastStatuses");
    await vi.advanceTimersByTimeAsync(100);
    expect(api().editorProps.value.entryPointStates).toEqual({ 1: "running", 3: "running" });
    lastStatuses.mockClear();
    for (let index = 0; index < 20; index += 1) apply([{ type: "output", id: "calc#adds", text: `line ${index}\n` }]);
    apply([{ type: "passed", id: "calc#adds" }, { type: "failed", id: "calc#subs", message: "boom" }]);
    await vi.advanceTimersByTimeAsync(100);
    // 21 payloads, one recomputation.
    expect(lastStatuses).toHaveBeenCalledTimes(1);
    expect(api().editorProps.value.entryPointStates).toEqual({ 1: "failed", 3: "passed", 7: "failed" });
    expect(api().editorProps.value.entryPoints).toBe(markers);

    // A newer run of one test keeps the older results of the others.
    runs.createRun({ runId: "run-0b", pluginId: "acme-tests", providerId: "unit", projectRoot: ROOT, debug: false, testIds: ["calc#subs"], startedAt: 2 }, [TESTS[0]!.children[1]!]);
    runs.applyPayload(parseTestRunEventPayload({ runId: "run-0b", pluginId: "acme-tests", events: [{ type: "passed", id: "calc#subs" }, { type: "ended", finished: true, reason: "finished" }] })!);
    await vi.advanceTimersByTimeAsync(100);
    expect(api().editorProps.value.entryPointStates).toEqual({ 1: "failed", 3: "passed", 7: "passed" });
    wrapper.unmount();
  });

  it("discovers again after a save of a file that can hold tests, debounced and not per keystroke", async () => {
    usePluginTestProvidersStore().setContributions([PROVIDER], []);
    mocks.discoverTests.mockResolvedValue(discovered());
    const path = ref(FILE);
    const { wrapper, api } = harness(path);
    await flush();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(1);
    // Switching documents in the same project reuses the discovery.
    path.value = OTHER;
    await flush();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(1);
    expect(api().editorProps.value.entryPoints).toEqual([]);
    path.value = FILE;
    await flush();

    const moved = [{ ...TESTS[0]!, children: [{ id: "calc#adds", label: "adds", path: "src/calc.spec.ts", line: 4 }] }];
    mocks.discoverTests.mockResolvedValue(discovered(moved));
    api().refreshEntryPoints();
    api().refreshEntryPoints();
    await vi.advanceTimersByTimeAsync(1000);
    await flush();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(2);
    expect(api().editorProps.value.entryPoints).toEqual([
      { line: 1, label: "calc" },
      { line: 4, label: "adds" },
    ]);

    // A save of a file type that holds no tests does not discover again.
    path.value = `${ROOT}/README.md`;
    await flush();
    api().refreshEntryPoints();
    await vi.advanceTimersByTimeAsync(1000);
    expect(mocks.discoverTests).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("discovers again while discovery fails or finds nothing (language service still starting), with backoff", async () => {
    usePluginTestProvidersStore().setContributions([PROVIDER], []);
    mocks.discoverTests
      .mockResolvedValueOnce({ ok: false, error: { code: "unavailable", detail: "language service is not running" } })
      .mockResolvedValueOnce(discovered([]))
      .mockResolvedValue(discovered());
    const path = ref(FILE);
    const { wrapper, api } = harness(path);
    await flush();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(1);
    // Activating another file right away does not hammer the provider.
    path.value = OTHER;
    await flush();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(1);
    // After the backoff, activating a file of the same project discovers again (it finds nothing yet).
    await vi.advanceTimersByTimeAsync(TEST_DISCOVERY_RETRY_BASE_MS - 1);
    expect(mocks.discoverTests).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    await flush();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(2);
    path.value = FILE;
    await flush();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(2);
    // The file stays open: the next attempt runs by itself after a longer wait and finds the tests.
    await vi.advanceTimersByTimeAsync(TEST_DISCOVERY_RETRY_BASE_MS * 2);
    await flush();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(600);
    expect(api().editorProps.value.entryPoints).toEqual([
      { line: 1, label: "calc" },
      { line: 3, label: "adds" },
      { line: 7, label: "subs" },
    ]);
    // Found: no more retries.
    await vi.advanceTimersByTimeAsync(10 * 60_000);
    path.value = OTHER;
    await flush();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(3);
    wrapper.unmount();
  });

  it("gives up retrying an empty discovery after a bounded number of attempts", async () => {
    usePluginTestProvidersStore().setContributions([PROVIDER], []);
    mocks.discoverTests.mockResolvedValue(discovered([]));
    const path = ref(FILE);
    const { wrapper } = harness(path);
    await flush();
    for (let index = 0; index < 40; index += 1) {
      await vi.advanceTimersByTimeAsync(30_000);
      await flush();
    }
    expect(mocks.discoverTests).toHaveBeenCalledTimes(1 + TEST_DISCOVERY_RETRY_LIMIT);
    // Switching files after giving up does not discover again either.
    path.value = OTHER;
    await flush();
    expect(mocks.discoverTests).toHaveBeenCalledTimes(1 + TEST_DISCOVERY_RETRY_LIMIT);
    wrapper.unmount();
  });

  it("providers without debugRun get no gutter buttons", async () => {
    usePluginTestProvidersStore().setContributions([{ ...PROVIDER, debugRun: undefined }], []);
    const { wrapper, api } = harness();
    await flush();
    expect(mocks.discoverTests).not.toHaveBeenCalled();
    expect(api().editorProps.value.entryPoints).toEqual([]);
    wrapper.unmount();
  });

  it("renders the last result on the glyph and in its hover", () => {
    const decorations = buildDebugDecorations(
      { breakpoints: [], entryPoints: [{ line: 3, label: "adds" }, { line: 7, label: "subs" }], entryPointStates: { 3: "failed" } },
      1,
      { labels: { breakpoint: () => "", paused: () => "", entryPoint: (label, state) => `${label}:${state ?? ""}` } },
    );
    expect(decorations.map((item) => item.options.glyphMarginClassName)).toEqual(["debug-entry debug-entry--failed", "debug-entry"]);
    expect(decorations.map((item) => item.options.glyphMarginHoverMessage?.value)).toEqual(["adds:failed", "subs:"]);
  });
});
