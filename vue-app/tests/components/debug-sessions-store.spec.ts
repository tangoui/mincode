import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { flushPromises } from "@vue/test-utils";
import type { DataBreakpointInfo } from "@/domain/debug/data-breakpoints";
import type { DebugEvent, PreparedDebugLaunch, RunConfiguration } from "@/domain/debug/types";

const debug = vi.hoisted(() => ({
  handler: undefined as ((event: DebugEvent) => void) | undefined,
  prepare: vi.fn(),
  trust: vi.fn(),
  start: vi.fn(),
  request: vi.fn(),
  setBreakpoints: vi.fn(),
  stop: vi.fn(),
  confirm: vi.fn(),
  guard: vi.fn(),
  listDebuggers: vi.fn(),
  ensureJdtls: vi.fn(),
  applySettings: vi.fn(),
  message: { error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => true, invokeBackend: vi.fn(), listenAppEvent: vi.fn() }));
vi.mock("@/services/debug-service", () => ({
  prepareDebugger: debug.prepare,
  trustDebugger: debug.trust,
  startDebugger: debug.start,
  debugRequest: debug.request,
  setSessionBreakpoints: debug.setBreakpoints,
  stopDebugSession: debug.stop,
  applyDebuggerSettings: debug.applySettings,
  listDebuggers: debug.listDebuggers,
  // Test-run prepares (services/plugin-test-runs.ts) pass the host's answer through.
  parsePrepared: (raw: unknown) => raw,
  onDebugEvent: (handler: (event: DebugEvent) => void) => {
    debug.handler = handler;
    return () => undefined;
  },
}));
vi.mock("element-plus", async (original) => ({
  ...(await original<typeof import("element-plus")>()),
  ElMessage: debug.message,
  ElMessageBox: { confirm: async (content: { props?: unknown }) => { if (!(await debug.confirm(content.props))) throw new Error("cancelled"); } },
}));
// The guidance guard (debugger installed, tools ready) has its own spec (debug-guidance.spec.ts).
vi.mock("@/services/debug-guidance", () => ({ guardDebugLaunch: debug.guard }));
vi.mock("@/services/jdtls-bundles", () => ({ ensureJdtlsBundlesReady: debug.ensureJdtls }));
vi.mock("@/services/persistent-store", () => ({ loadPersistedValue: vi.fn().mockResolvedValue(null), savePersistedValue: vi.fn().mockResolvedValue(undefined) }));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));

import { launchDebugSession } from "@/services/debug-launch";
import { isLspServerHeld } from "@/services/lsp-server-holds";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";
import { useVueI18n } from "@/i18n/messages";

const ROOT = "/Users/A/Proj";
const FILE = "/Users/A/Proj/src/app.py";
const fingerprint = "a".repeat(64);

const cfg: RunConfiguration = {
  id: "cfg-1",
  name: "App",
  pluginId: "mincode.python-debug",
  type: "python",
  request: "launch",
  fields: { program: "${file}" },
};

function prepared(trusted: boolean): PreparedDebugLaunch {
  return {
    prepareId: "prep-1",
    type: "python",
    kind: "",
    projectRoot: ROOT,
    request: "launch",
    arguments: { program: FILE },
    launch: {
      pluginId: cfg.pluginId,
      pluginName: "Python Debug",
      pluginVersion: "1.0.0",
      point: "debuggers",
      entryKey: "python",
      title: "debugpy",
      argv: ["python3", "-m", "debugpy.adapter"],
      env: {},
      cwd: ROOT,
      pluginExecutable: false,
      fingerprint,
      trusted,
      previouslyTrusted: false,
    },
  };
}

function emit(event: DebugEvent) {
  debug.handler?.(event);
}

async function startSession(id: string) {
  debug.start.mockResolvedValueOnce({ sessionId: id, capabilities: {}, skippedBreakpoints: [] });
  debug.prepare.mockResolvedValueOnce(prepared(true));
  const store = useDebugSessionsStore();
  expect(await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE })).toBe(id);
  return store;
}

beforeEach(() => {
  setActivePinia(createPinia());
  debug.handler = undefined;
  debug.guard.mockReset().mockResolvedValue({ proceed: true });
  debug.listDebuggers.mockReset().mockResolvedValue([]);
  debug.ensureJdtls.mockReset().mockResolvedValue(true);
  for (const mock of [debug.prepare, debug.trust, debug.start, debug.request, debug.setBreakpoints, debug.stop, debug.confirm]) mock.mockReset();
  debug.applySettings.mockReset().mockResolvedValue(undefined);
  debug.message.error.mockReset();
  debug.message.warning.mockReset();
  debug.request.mockResolvedValue({});
  debug.stop.mockResolvedValue(undefined);
});

describe("debug launch", () => {
  it("launch cancels when confirm is declined", async () => {
    debug.prepare.mockResolvedValue(prepared(false));
    debug.confirm.mockResolvedValue(false);
    await expect(launchDebugSession(ROOT, cfg, { noDebug: false, activeFile: FILE })).resolves.toBeNull();
    expect(debug.prepare).toHaveBeenCalledWith({
      pluginId: cfg.pluginId,
      type: "python",
      projectRoot: ROOT,
      configuration: { program: FILE, request: "launch" },
      noDebug: false,
    });
    expect(debug.confirm).toHaveBeenCalledTimes(1);
    const options = debug.confirm.mock.calls[0]![0] as { changed: boolean; notices: string[] };
    expect(options.changed).toBe(false);
    expect(options.notices).toContain(`program: ${FILE}`);
    expect(debug.trust).not.toHaveBeenCalled();
    expect(debug.start).not.toHaveBeenCalled();
  });

  it.each([false, true])("launch skips confirm when trusted and honors noDebug=%s", async (noDebug) => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.toggleBreakpoint(ROOT, FILE, 4);
    configs.setBreakpoint(ROOT, FILE, { line: 8, enabled: false });
    configs.setBreakpoint(ROOT, FILE, { line: 9, enabled: true, logMessage: "value={value}" });
    configs.setExceptionFilters(ROOT, "python", ["raised"]);
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] });

    const result = await launchDebugSession(ROOT, cfg, { noDebug, activeFile: FILE });
    expect(result?.sessionId).toBe("s1");
    expect(debug.confirm).not.toHaveBeenCalled();
    expect(debug.trust).not.toHaveBeenCalled();
    expect(debug.prepare.mock.calls[0]![0]).toMatchObject({ noDebug });
    expect(debug.start).toHaveBeenCalledWith({
      prepareId: "prep-1",
      fingerprint,
      breakpoints: noDebug ? {} : { [FILE]: [{ line: 4, enabled: true }, { line: 9, enabled: true, logMessage: "value={value}" }] },
      exceptionFilters: noDebug ? [] : ["raised"],
    });
  });

  it("launch trusts the confirmed fingerprint before starting", async () => {
    debug.prepare.mockResolvedValue(prepared(false));
    debug.confirm.mockResolvedValue(true);
    debug.trust.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] });
    await launchDebugSession(ROOT, cfg, { noDebug: false, activeFile: FILE });
    expect(debug.trust).toHaveBeenCalledWith("prep-1", fingerprint);
    expect(debug.trust.mock.invocationCallOrder[0]).toBeLessThan(debug.start.mock.invocationCallOrder[0]!);
  });
});

describe("debug sessions store", () => {
  it("sets a variable in its container and signals that variables changed", async () => {
    const store = await startSession("s1");
    const version = store.variablesVersion("s1");
    debug.request.mockResolvedValueOnce({ value: "41", type: "int" });
    await expect(store.setVariable("s1", 5, "total", "41")).resolves.toEqual({ value: "41", type: "int", variablesReference: 0 });
    expect(debug.request).toHaveBeenLastCalledWith("s1", "setVariable", { variablesReference: 5, name: "total", value: "41" });
    expect(store.variablesVersion("s1")).toBe(version + 1);
  });

  it("rejects a variable the adapter cannot set with the adapter's message", async () => {
    const store = await startSession("s1");
    const version = store.variablesVersion("s1");
    debug.request.mockRejectedValueOnce(new Error("Failed to set variable. Reason: abc is not a valid int"));
    await expect(store.setVariable("s1", 5, "total", "abc")).rejects.toThrow("Failed to set variable. Reason: abc is not a valid int");
    debug.request.mockResolvedValueOnce({ type: "int" });
    await expect(store.setVariable("s1", 5, "total", "1")).rejects.toThrow("setVariable returned an invalid result");
    expect(store.variablesVersion("s1")).toBe(version);
  });

  it("loads a source the adapter holds by reference", async () => {
    const store = await startSession("s1");
    const source = { name: "Method.java", path: "jdt://contents/java.base/Method.class", sourceReference: 7 };
    debug.request.mockResolvedValueOnce({ content: "class Method {}" });
    await expect(store.loadSource("s1", source)).resolves.toBe("class Method {}");
    expect(debug.request).toHaveBeenLastCalledWith("s1", "source", { source, sourceReference: 7 });
    debug.request.mockResolvedValueOnce({ content: 42 });
    await expect(store.loadSource("s1", source)).rejects.toThrow("source returned no content");
  });

  it("closes a session when its project leaves every workspace", async () => {
    const { useLocalProjectStore } = await import("@/stores/local-project");
    const projects = useLocalProjectStore();
    await projects.hydrate("workspace-a");
    projects.restoreProject(ROOT);
    projects.restoreProject("/Users/A/Other");
    const store = await startSession("s1");
    await Promise.resolve();
    expect(store.sessions).toHaveLength(1);

    // Removing another project leaves the session alone.
    projects.removeProject("/Users/A/Other");
    await vi.waitFor(() => expect(debug.stop).not.toHaveBeenCalled());
    expect(store.sessions).toHaveLength(1);

    projects.removeProject(ROOT);
    await vi.waitFor(() => expect(store.sessions).toHaveLength(0));
    expect(debug.stop).toHaveBeenCalledWith("s1");
  });

  it("closes a session when the workspace listing its project is permanently deleted", async () => {
    const { useLocalProjectStore } = await import("@/stores/local-project");
    const projects = useLocalProjectStore();
    await projects.hydrate("workspace-a");
    projects.restoreProject("/Users/A/Other");
    projects.switchWorkspace("workspace-b");
    projects.restoreProject(ROOT);
    const store = await startSession("s1");
    projects.switchWorkspace("workspace-a");
    await Promise.resolve();
    expect(store.sessions).toHaveLength(1);

    expect(projects.discardWorkspaceState("workspace-b")).toBe(true);
    await vi.waitFor(() => expect(store.sessions).toHaveLength(0));
    expect(debug.stop).toHaveBeenCalledWith("s1");
  });

  it("closes sessions of projects no workspace lists, stopping the live ones", async () => {
    const store = await startSession("s1");
    await startSession("s2");
    emit({ sessionId: "s2", event: "mincode.sessionEnded", body: { reason: "exited" } });
    await vi.waitFor(() => expect(store.sessions.find((s) => s.id === "s2")?.state).toBe("ended"));
    debug.stop.mockClear();

    // Same project, other spelling: nothing is closed.
    expect(store.closeSessionsOutside(["/Users/A/Proj/", "/Users/A/Other"])).toBe(0);
    expect(store.sessions).toHaveLength(2);
    expect(store.hasLiveSession(ROOT)).toBe(true);

    // The project is gone from every workspace: both tabs close, only the live one is stopped.
    expect(store.closeSessionsOutside(["/Users/A/Other"])).toBe(2);
    expect(store.sessions).toHaveLength(0);
    expect(store.selectedSessionId).toBeNull();
    expect(debug.stop).toHaveBeenCalledTimes(1);
    expect(debug.stop).toHaveBeenCalledWith("s1");
    expect(store.hasLiveSession(ROOT)).toBe(false);
  });

  it("commands target the selected session only", async () => {
    const store = await startSession("s1");
    await startSession("s2");
    expect(store.sessions.map((s) => s.id)).toEqual(["s1", "s2"]);
    expect(store.selectedSessionId).toBe("s2");

    debug.request.mockImplementation(async (_id: string, command: string) => {
      if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
      if (command === "stackTrace") return { stackFrames: [{ id: 11, name: "f", line: 3, column: 1 }] };
      return {};
    });
    emit({ sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 1 } });
    emit({ sessionId: "s2", event: "stopped", body: { reason: "breakpoint", threadId: 1 } });
    await vi.waitFor(() => expect(store.selectedSession?.selectedFrameId).toBe(11));
    debug.request.mockClear();

    await store.stepOver(store.selectedSessionId!);
    expect(debug.request).toHaveBeenCalledTimes(1);
    expect(debug.request).toHaveBeenCalledWith("s2", "next", { threadId: 1 });
    expect(store.sessions.find((s) => s.id === "s1")?.state).toBe("paused");
    expect(store.sessions.find((s) => s.id === "s2")?.state).toBe("running");
  });

  it("paused loads threads then stackTrace and selects top frame", async () => {
    const store = await startSession("s1");
    debug.request.mockImplementation(async (_id: string, command: string) => {
      if (command === "threads") return { threads: [{ id: 1, name: "main" }, { id: 2, name: "worker" }] };
      if (command === "stackTrace")
        return {
          stackFrames: [
            { id: 21, name: "inner", line: 9, column: 1, source: { path: FILE } },
            { id: 20, name: "outer", line: 2, column: 1, source: { path: FILE } },
          ],
        };
      return {};
    });
    emit({ sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 2 } });
    await vi.waitFor(() => expect(store.selectedSession?.selectedFrameId).toBe(21));
    const commands = debug.request.mock.calls.map((call) => call[1]);
    expect(commands).toEqual(["threads", "stackTrace"]);
    expect(debug.request.mock.calls[1]![2]).toEqual({ threadId: 2, startFrame: 0, levels: 50 });
    expect(store.selectedSession).toMatchObject({ state: "paused", selectedThreadId: 2 });
    expect(store.selectedSession?.threads).toHaveLength(2);
    expect(store.isPaused).toBe(true);
    expect(store.isActive).toBe(true);
  });

  it("a failed stack fetch is reported as lastError", async () => {
    const store = await startSession("s1");
    debug.request.mockRejectedValue(new Error("unavailable: session closed"));
    emit({ sessionId: "s1", event: "stopped", body: { reason: "pause", threadId: 1 } });
    await vi.waitFor(() => expect(store.selectedSession?.lastError).toBe("unavailable: session closed"));
    expect(store.selectedSession?.state).toBe("paused");
  });

  it("lastError shows the adapter's text, and localized copy for a timeout", async () => {
    const store = await startSession("s1");
    debug.request.mockRejectedValue(new Error("dap threads failed: VM disconnected"));
    emit({ sessionId: "s1", event: "stopped", body: { reason: "pause", threadId: 1 } });
    await vi.waitFor(() => expect(store.selectedSession?.lastError).toBe("VM disconnected"));
    debug.request.mockRejectedValue(new Error("timeout: dap threads did not respond within 15s"));
    emit({ sessionId: "s1", event: "stopped", body: { reason: "pause", threadId: 1 } });
    await vi.waitFor(() =>
      expect(store.selectedSession?.lastError).toMatch(/^(调试器没有及时响应，请稍后重试|The debugger did not respond in time\. Try again)$/),
    );
  });

  it("keeps events that arrive before start resolves", async () => {
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockImplementation(async () => {
      emit({ sessionId: "s1", event: "mincode.breakpointsSet", body: { path: FILE, breakpoints: [{ verified: false }] } });
      return { sessionId: "s1", capabilities: {}, skippedBreakpoints: [] };
    });
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.toggleBreakpoint(ROOT, FILE, 4);
    const store = useDebugSessionsStore();
    await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    expect(store.verifiedLinesFor(ROOT, "/users/a/proj/src/app.py")).toEqual({ 4: false });
  });

  it("does not file a debugger address result as a project file", async () => {
    const store = await startSession("s1");
    emit({ sessionId: "s1", event: "mincode.breakpointsSet", body: { path: FILE, breakpoints: [{ verified: true, line: 4 }] } });
    const before = store.selectedSession?.verified;
    emit({ sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "x-lib://pkg/Type.class", address: true, breakpoints: [{ id: 3, verified: false, line: 4 }] } });
    expect(store.selectedSession?.verified).toBe(before);
    expect(Object.keys(store.selectedSession?.verified ?? {})).toEqual([FILE]);
    expect(store.selectedSession?.breakpointIds).toEqual({});
  });

  it("keeps control events when buffered output overflows before start resolves", async () => {
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockImplementation(async () => {
      emit({ sessionId: "s1", event: "mincode.runInTerminal", body: { terminalSessionId: "t1", title: "app" } });
      for (let i = 0; i < 600; i += 1) emit({ sessionId: "s1", event: "output", body: { category: "stdout", output: `line ${i}\n` } });
      emit({ sessionId: "s1", event: "mincode.sessionEnded", body: { reason: "exited" } });
      return { sessionId: "s1", capabilities: {}, skippedBreakpoints: [] };
    });
    const store = useDebugSessionsStore();
    await store.start(ROOT, cfg, { noDebug: true, activeFile: FILE });
    const view = store.sessions[0]!;
    expect(view.state).toBe("ended");
    expect(view.consoleTerminalId).toBe("t1");
    expect(view.output).toHaveLength(500);
    expect(view.output[0]!.text).toBe("line 100\n");
    expect(view.output.at(-1)!.text).toBe("line 599\n");
    expect(store.isActive).toBe(false);
  });

  it("sends the exception filter switches changed while the start was pending once the session is listed", async () => {
    debug.prepare.mockResolvedValue(prepared(true));
    debug.request.mockResolvedValue({});
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setExceptionFilters(ROOT, "python", ["uncaught"]);
    let release!: () => void;
    debug.start.mockImplementation(async () => {
      // Switched in the breakpoints dialog while the adapter starts: no session is listed yet.
      configs.setExceptionFilters(ROOT, "python", ["raised", "uncaught"]);
      await new Promise<void>((resolve) => (release = resolve));
      return { sessionId: "s1", capabilities: {}, skippedBreakpoints: [] };
    });
    const store = useDebugSessionsStore();
    const started = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() => expect(release).toBeTypeOf("function"));
    expect(debug.start).toHaveBeenCalledWith(expect.objectContaining({ exceptionFilters: ["uncaught"] }));
    release();
    expect(await started).toBe("s1");
    await vi.waitFor(() => expect(debug.request).toHaveBeenCalledWith("s1", "setExceptionBreakpoints", { filters: ["raised", "uncaught"] }));
  });

  it("sends the adapter's default filters once on the first launch of a type without saved switches", async () => {
    debug.request.mockResolvedValue({});
    const configs = useDebugConfigsStore();
    await configs.load();
    debug.prepare.mockResolvedValueOnce(prepared(true));
    debug.start.mockResolvedValueOnce({
      sessionId: "s1",
      capabilities: { exceptionBreakpointFilters: [{ filter: "uncaught", label: "Uncaught", default: true }, { filter: "raised", label: "Raised" }] },
      skippedBreakpoints: [],
    });
    const store = useDebugSessionsStore();
    expect(await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE })).toBe("s1");
    await flushPromises();
    const sent = debug.request.mock.calls.filter(([, command]) => command === "setExceptionBreakpoints");
    expect(sent).toEqual([["s1", "setExceptionBreakpoints", { filters: ["uncaught"] }]]);
  });

  it("does not send the exception filters again when they did not change during the start", async () => {
    debug.request.mockResolvedValue({});
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setExceptionFilters(ROOT, "python", ["uncaught"]);
    await startSession("s1");
    await flushPromises();
    expect(debug.request).not.toHaveBeenCalledWith("s1", "setExceptionBreakpoints", expect.anything());
  });

  it("sends breakpoints changed while the start was pending once the session is listed", async () => {
    const OTHER = "/Users/A/Proj/src/other.py";
    const LATE = "/Users/A/Proj/src/late.py";
    debug.prepare.mockResolvedValue(prepared(true));
    debug.setBreakpoints.mockResolvedValue({ breakpoints: [] });
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.toggleBreakpoint(ROOT, OTHER, 2);
    let release!: () => void;
    debug.start.mockImplementation(async () => {
      // The user edits breakpoints while the adapter starts: the editor's sync finds no listed session.
      configs.toggleBreakpoint(ROOT, FILE, 4);
      configs.toggleBreakpoint(ROOT, LATE, 9);
      configs.toggleBreakpoint(ROOT, LATE, 9);
      await new Promise<void>((resolve) => (release = resolve));
      return { sessionId: "s1", capabilities: {}, skippedBreakpoints: [] };
    });
    const store = useDebugSessionsStore();
    const started = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() => expect(release).toBeTypeOf("function"));
    await store.syncBreakpointsForFile(ROOT, FILE);
    expect(debug.setBreakpoints).not.toHaveBeenCalled();
    release();
    expect(await started).toBe("s1");
    await vi.waitFor(() => expect(debug.setBreakpoints).toHaveBeenCalledTimes(2));
    expect(debug.setBreakpoints).toHaveBeenCalledWith("s1", FILE, [{ line: 4, enabled: true }]);
    // A file whose breakpoint came and went is sent too (empty: the adapter may hold the old list).
    expect(debug.setBreakpoints).toHaveBeenCalledWith("s1", LATE, []);
    // The snapshot already carried OTHER.
    expect(debug.setBreakpoints).not.toHaveBeenCalledWith("s1", OTHER, expect.anything());
  });

  it("ignores a second launch of the project's configuration while one is in flight", async () => {
    debug.prepare.mockResolvedValue(prepared(true));
    let release!: () => void;
    debug.start.mockImplementation(async () => {
      await new Promise<void>((resolve) => (release = resolve));
      return { sessionId: "s1", capabilities: {}, skippedBreakpoints: [] };
    });
    const store = useDebugSessionsStore();
    // E.g. ⌃D and a click on the toolbar's Debug button.
    const first = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    expect(store.launchFor(ROOT)).toEqual(expect.objectContaining({ configId: "cfg-1", noDebug: false }));
    expect(await store.start("/users/a/proj/", cfg, { noDebug: false, activeFile: FILE })).toBeNull();
    await vi.waitFor(() => expect(release).toBeTypeOf("function"));
    release();
    expect(await first).toBe("s1");
    expect(debug.prepare).toHaveBeenCalledTimes(1);
    expect(debug.start).toHaveBeenCalledTimes(1);
    expect(store.launchFor(ROOT)).toBeNull();
    // Once it settled, the configuration starts again.
    debug.start.mockResolvedValue({ sessionId: "s2", capabilities: {}, skippedBreakpoints: [] });
    expect(await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE })).toBe("s2");
  });

  it("clears the in-flight launch when the launch fails", async () => {
    debug.prepare.mockRejectedValue(new Error("internal: boom"));
    const store = useDebugSessionsStore();
    expect(await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE })).toBeNull();
    expect(store.launchFor(ROOT)).toBeNull();
  });

  it("syncBreakpointsForFile skips noDebug sessions", async () => {
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValue({ sessionId: "run", capabilities: {}, skippedBreakpoints: [] });
    const store = useDebugSessionsStore();
    await store.start(ROOT, cfg, { noDebug: true, activeFile: FILE });
    useDebugConfigsStore().toggleBreakpoint(ROOT, FILE, 5);
    await store.syncBreakpointsForFile(ROOT, FILE);
    expect(debug.setBreakpoints).not.toHaveBeenCalled();
  });

  it("restart aborts when stop fails, but proceeds when the session is already gone", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.upsertConfig(ROOT, cfg);
    const store = await startSession("s1");
    debug.stop.mockRejectedValueOnce(new Error("internal: adapter did not answer"));
    expect(await store.restart("s1")).toBeNull();
    expect(debug.start).toHaveBeenCalledTimes(1);
    expect(store.sessions.map((s) => s.id)).toEqual(["s1"]);
    expect(store.selectedSession?.lastError).toBe("internal: adapter did not answer");

    debug.stop.mockRejectedValueOnce(new Error("not_found: debug session s1"));
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValue({ sessionId: "s2", capabilities: {}, skippedBreakpoints: [] });
    expect(await store.restart("s1")).toBe("s2");
    expect(store.sessions.map((s) => s.id)).toEqual(["s2"]);
  });

  it("a Java (jdtls) session holds jdtls from the launch until it ends", async () => {
    const jdtlsDebugger = { pluginId: cfg.pluginId, type: "python", adapterKind: "jdtls" };
    debug.guard.mockResolvedValue({ proceed: true, debugger: jdtlsDebugger });
    let heldDuringLaunch = false;
    debug.ensureJdtls.mockImplementation(async () => {
      heldDuringLaunch = isLspServerHeld(ROOT, "jdtls");
      return true;
    });
    const store = await startSession("s1");
    expect(heldDuringLaunch).toBe(true);
    expect(isLspServerHeld(ROOT, "jdtls")).toBe(true);
    emit({ sessionId: "s1", event: "mincode.sessionEnded", body: { reason: "exited" } });
    expect(isLspServerHeld(ROOT, "jdtls")).toBe(false);

    // A launch that does not start releases at once; so does closing a running session.
    debug.ensureJdtls.mockResolvedValueOnce(false);
    expect(await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE })).toBeNull();
    expect(isLspServerHeld(ROOT, "jdtls")).toBe(false);
    await startSession("s2");
    expect(isLspServerHeld(ROOT, "jdtls")).toBe(true);
    store.close("s2");
    expect(isLspServerHeld(ROOT, "jdtls")).toBe(false);

    // Other debuggers hold nothing.
    debug.guard.mockResolvedValue({ proceed: true });
    await startSession("s3");
    expect(isLspServerHeld(ROOT, "jdtls")).toBe(false);
  });

  it("ended sessions stay listed and inactive", async () => {
    const store = await startSession("s1");
    emit({ sessionId: "s1", event: "mincode.sessionEnded", body: { reason: "exited" } });
    expect(store.sessions).toHaveLength(1);
    expect(store.isActive).toBe(false);
    await store.stepOver("s1");
    expect(debug.request).not.toHaveBeenCalled();
    store.close("s1");
    expect(store.sessions).toHaveLength(0);
    expect(store.selectedSessionId).toBeNull();
  });

  it("syncBreakpointsForFile updates every active session of the project", async () => {
    const store = await startSession("s1");
    await startSession("s2");
    emit({ sessionId: "s2", event: "mincode.sessionEnded", body: { reason: "exited" } });
    const configs = useDebugConfigsStore();
    configs.toggleBreakpoint(ROOT, FILE, 5);
    debug.setBreakpoints.mockResolvedValue({ breakpoints: [{ verified: true, line: 5 }] });
    await store.syncBreakpointsForFile(ROOT, FILE);
    expect(debug.setBreakpoints).toHaveBeenCalledTimes(1);
    expect(debug.setBreakpoints).toHaveBeenCalledWith("s1", FILE, [{ line: 5, enabled: true }]);
    expect(store.verifiedLinesFor(ROOT, FILE)).toEqual({ 5: true });
  });

  it("serializes breakpoint syncs per file so the adapter always ends with the latest list", async () => {
    const store = await startSession("s1");
    debug.setBreakpoints.mockReset();
    const configs = useDebugConfigsStore();
    let inFlight = 0, overlapped = false;
    const releases: Array<() => void> = [];
    debug.setBreakpoints.mockImplementation(async (_id: string, _path: string, list: { line: number }[]) => {
      inFlight += 1;
      if (inFlight > 1) overlapped = true;
      await new Promise<void>((resolve) => releases.push(resolve));
      inFlight -= 1;
      return { breakpoints: list.map((bp) => ({ verified: true, line: bp.line })) };
    });
    // Two quick toggles in one file: each editor action starts a sync right away.
    configs.toggleBreakpoint(ROOT, FILE, 10);
    const first = store.syncBreakpointsForFile(ROOT, FILE);
    configs.toggleBreakpoint(ROOT, FILE, 20);
    const second = store.syncBreakpointsForFile(ROOT, FILE);
    await vi.waitFor(() => expect(releases).toHaveLength(1));
    // The second sync must wait: the adapter replaces the whole list per request.
    expect(debug.setBreakpoints).toHaveBeenCalledTimes(1);
    releases.shift()!();
    await vi.waitFor(() => expect(releases).toHaveLength(1));
    releases.shift()!();
    await Promise.all([first, second]);
    expect(overlapped).toBe(false);
    expect(debug.setBreakpoints).toHaveBeenLastCalledWith("s1", FILE, [{ line: 10, enabled: true }, { line: 20, enabled: true }]);
    expect(store.verifiedLinesFor(ROOT, FILE)).toEqual({ 10: true, 20: true });
  });

  it("verifiedLinesFor reflects late verification", async () => {
    const store = await startSession("s1");
    const configs = useDebugConfigsStore();
    configs.toggleBreakpoint(ROOT, FILE, 5);
    configs.toggleBreakpoint(ROOT, FILE, 8);
    debug.setBreakpoints.mockResolvedValue({
      breakpoints: [
        { id: 11, verified: false, message: "Class not loaded yet" },
        { id: 12, verified: false },
      ],
    });
    await store.syncBreakpointsForFile(ROOT, FILE);
    expect(store.verifiedLinesFor(ROOT, FILE)).toEqual({ 5: false, 8: false });
    expect(store.breakpointDetailsFor(ROOT, FILE)).toEqual({ 5: { message: "Class not loaded yet" } });
    emit({ sessionId: "s1", event: "breakpoint", body: { reason: "changed", breakpoint: { id: 11, verified: true, line: 5 } } });
    expect(store.verifiedLinesFor(ROOT, FILE)).toEqual({ 5: true, 8: false });
    expect(store.breakpointDetailsFor(ROOT, FILE)).toEqual({});
    // The adapter moves line 5 onto line 8: line 5 stays the key, line 8 keeps its own result.
    emit({ sessionId: "s1", event: "breakpoint", body: { reason: "changed", breakpoint: { id: 11, verified: true, line: 8 } } });
    expect(store.verifiedLinesFor(ROOT, FILE)).toEqual({ 5: true, 8: false });
    expect(store.breakpointDetailsFor(ROOT, FILE)).toEqual({ 5: { actualLine: 8 } });
    emit({ sessionId: "s1", event: "breakpoint", body: { reason: "changed", breakpoint: { id: 11, verified: true, line: 5 } } });
    // An id the store never saw, located by a differently cased source path (macOS folds case).
    emit({
      sessionId: "s1",
      event: "breakpoint",
      body: { reason: "changed", breakpoint: { id: 99, verified: true, line: 8, source: { path: "/users/a/proj/src/APP.py" } } },
    });
    expect(store.verifiedLinesFor(ROOT, FILE)).toEqual({ 5: true, 8: true });
    emit({ sessionId: "s1", event: "breakpoint", body: { reason: "removed", breakpoint: { id: 11, verified: false } } });
    expect(store.verifiedLinesFor(ROOT, FILE)).toEqual({ 8: true });
  });

  it("stop omits terminateDebuggee and restart starts the same configuration", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.upsertConfig(ROOT, cfg);
    const store = await startSession("s1");
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValue({ sessionId: "s2", capabilities: {}, skippedBreakpoints: [] });
    await store.restart("s1");
    expect(debug.stop).toHaveBeenCalledWith("s1");
    expect(debug.prepare).toHaveBeenLastCalledWith(expect.objectContaining({ configuration: { program: FILE, request: "launch" }, noDebug: false }));
    expect(store.sessions.map((s) => s.id)).toEqual(["s2"]);
    expect(store.selectedSessionId).toBe("s2");
  });

  it("restart of a deleted configuration only stops", async () => {
    const store = await startSession("s1");
    await store.restart("s1");
    expect(debug.stop).toHaveBeenCalledWith("s1");
    expect(debug.start).toHaveBeenCalledTimes(1);
    expect(debug.message.warning).toHaveBeenCalled();
  });

  it("runToCursor sets a temporary breakpoint and restores on stop", async () => {
    const store = await startSession("s1");
    const configs = useDebugConfigsStore();
    configs.toggleBreakpoint(ROOT, FILE, 2);
    debug.request.mockImplementation(async (_id: string, command: string) => {
      if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
      if (command === "stackTrace") return { stackFrames: [{ id: 11, name: "f", line: 3, column: 1 }] };
      return {};
    });
    debug.setBreakpoints.mockResolvedValue({ breakpoints: [] });
    emit({ sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 1 } });
    await vi.waitFor(() => expect(store.selectedSession?.selectedFrameId).toBe(11));

    await store.runToCursor("s1", FILE, 9);
    expect(debug.setBreakpoints).toHaveBeenLastCalledWith("s1", FILE, [{ line: 2, enabled: true }, { line: 9, enabled: true }]);
    expect(debug.request).toHaveBeenLastCalledWith("s1", "continue", { threadId: 1 });
    emit({ sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 1 } });
    await vi.waitFor(() => expect(debug.setBreakpoints).toHaveBeenLastCalledWith("s1", FILE, [{ line: 2, enabled: true }]));
  });

  it.each([
    { logMessage: "value={value}" },
    { condition: "value < 0", hitCondition: "10", logMessage: "combined={value}" },
    { condition: "value < 0", hitCondition: "10" },
  ])("runToCursor temporarily replaces non-unconditional target %j and restores it", async (fields) => {
    const store = await startSession("s1");
    const configs = useDebugConfigsStore();
    configs.toggleBreakpoint(ROOT, FILE, 2);
    const target = { line: 9, enabled: true, ...fields };
    configs.setBreakpoint(ROOT, FILE, target);
    debug.request.mockImplementation(async (_id: string, command: string) => {
      if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
      if (command === "stackTrace") return { stackFrames: [{ id: 11, name: "f", line: 3, column: 1 }] };
      return {};
    });
    debug.setBreakpoints.mockResolvedValue({ breakpoints: [] });
    emit({ sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 1 } });
    await vi.waitFor(() => expect(store.selectedSession?.selectedFrameId).toBe(11));
    await store.runToCursor("s1", FILE, 9);
    expect(debug.setBreakpoints).toHaveBeenLastCalledWith("s1", FILE, [{ line: 2, enabled: true }, { line: 9, enabled: true }]);
    expect(configs.breakpointsFor(ROOT, FILE)).toEqual([{ line: 2, enabled: true }, target]);
    emit({ sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 1 } });
    await vi.waitFor(() => expect(debug.setBreakpoints).toHaveBeenLastCalledWith("s1", FILE, [{ line: 2, enabled: true }, target]));
  });

  it("muted sessions get empty breakpoint lists until unmuted", async () => {
    const store = await startSession("s1");
    await startSession("s2");
    const configs = useDebugConfigsStore();
    configs.toggleBreakpoint(ROOT, FILE, 5);
    debug.setBreakpoints.mockResolvedValue({ breakpoints: [] });

    await store.setBreakpointsMuted("s1", true);
    expect(store.isMuted("s1")).toBe(true);
    expect(store.isMuted("s2")).toBe(false);
    expect(debug.setBreakpoints).toHaveBeenCalledWith("s1", FILE, []);

    debug.setBreakpoints.mockClear();
    configs.toggleBreakpoint(ROOT, FILE, 7);
    await store.syncBreakpointsForFile(ROOT, FILE);
    expect(debug.setBreakpoints).toHaveBeenCalledWith("s1", FILE, []);
    expect(debug.setBreakpoints).toHaveBeenCalledWith("s2", FILE, [{ line: 5, enabled: true }, { line: 7, enabled: true }]);

    debug.setBreakpoints.mockClear();
    await store.setBreakpointsMuted("s1", false);
    expect(store.isMuted("s1")).toBe(false);
    expect(debug.setBreakpoints).toHaveBeenCalledTimes(1);
    expect(debug.setBreakpoints).toHaveBeenCalledWith("s1", FILE, [{ line: 5, enabled: true }, { line: 7, enabled: true }]);
  });

  it("applyExceptionFilters updates running debug sessions of that type", async () => {
    const store = await startSession("s1");
    await store.applyExceptionFilters(ROOT, "python", ["raised"]);
    expect(debug.request).toHaveBeenCalledWith("s1", "setExceptionBreakpoints", { filters: ["raised"] });
    debug.request.mockClear();
    await store.applyExceptionFilters(ROOT, "java", ["caught"]);
    expect(debug.request).not.toHaveBeenCalled();
  });

  it("applies and saves the adapter's default exception filters when none are saved", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    const capabilities = {
      exceptionBreakpointFilters: [
        { filter: "raised", label: "Raised", default: false },
        { filter: "uncaught", label: "Uncaught", default: true },
      ],
    };
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValueOnce({ sessionId: "s1", capabilities, skippedBreakpoints: [] });
    const store = useDebugSessionsStore();
    await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() => expect(debug.request).toHaveBeenCalledWith("s1", "setExceptionBreakpoints", { filters: ["uncaught"] }));
    expect(configs.stateFor(ROOT).exceptionFilters.python).toEqual(["uncaught"]);

    // Saved filters (even an empty list) are left alone.
    configs.setExceptionFilters(ROOT, "python", []);
    debug.request.mockClear();
    debug.start.mockResolvedValueOnce({ sessionId: "s2", capabilities, skippedBreakpoints: [] });
    await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await Promise.resolve();
    expect(debug.request).not.toHaveBeenCalledWith("s2", "setExceptionBreakpoints", expect.anything());
    expect(configs.stateFor(ROOT).exceptionFilters.python).toEqual([]);
  });

  it("noDebug runs do not apply exception filters", async () => {
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValueOnce({
      sessionId: "run",
      capabilities: { exceptionBreakpointFilters: [{ filter: "uncaught", label: "Uncaught", default: true }] },
      skippedBreakpoints: [],
    });
    const store = useDebugSessionsStore();
    await store.start(ROOT, cfg, { noDebug: true, activeFile: FILE });
    await Promise.resolve();
    expect(debug.request).not.toHaveBeenCalledWith("run", "setExceptionBreakpoints", expect.anything());
    expect(useDebugConfigsStore().stateFor(ROOT).exceptionFilters.python).toBeUndefined();
  });

  it("reports skipped breakpoint files after start", async () => {
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: ["/x/a.py", "/x/b.py"] });
    const store = useDebugSessionsStore();
    await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    expect(debug.message.warning).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining("2") }));
  });

  it("shows the adapter's own text when the start fails inside a host-wrapped request", async () => {
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockRejectedValue(new Error("internal: debug begin: dap launch failed: Failed to resolve classpath: demo.App"));
    const store = useDebugSessionsStore();
    expect(await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE })).toBeNull();
    const shown = String(debug.message.error.mock.calls.at(-1)?.[0]?.message);
    expect(shown).toMatch(/Failed to resolve classpath: demo\.App$/);
    expect(shown).not.toContain("dap launch failed");
    expect(shown).not.toContain("debug begin");
  });

  it("start reports a failure instead of throwing", async () => {
    const store = useDebugSessionsStore();
    expect(await store.start(ROOT, cfg, { noDebug: false })).toBeNull();
    expect(debug.prepare).not.toHaveBeenCalled();
    expect(debug.message.error).toHaveBeenCalled();
  });
});

describe("launch progress", () => {
  function deferred<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>((done) => (resolve = done));
    return { promise, resolve };
  }

  it("lists the launch with its current step until it starts", async () => {
    const guard = deferred<{ proceed: boolean }>();
    const prepare = deferred<PreparedDebugLaunch>();
    const start = deferred<{ sessionId: string; capabilities: object; skippedBreakpoints: string[] }>();
    debug.guard.mockReturnValue(guard.promise);
    debug.prepare.mockReturnValue(prepare.promise);
    debug.start.mockReturnValue(start.promise);
    const store = useDebugSessionsStore();
    const started = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    expect(store.launches).toEqual([expect.objectContaining({ root: ROOT, configId: "cfg-1", configName: "App", step: "checking", languageService: false, status: "" })]);
    guard.resolve({ proceed: true });
    await vi.waitFor(() => expect(store.launches[0]?.step).toBe("resolving"));
    prepare.resolve(prepared(true));
    await vi.waitFor(() => expect(store.launches[0]?.step).toBe("launching"));
    start.resolve({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] });
    expect(await started).toBe("s1");
    expect(store.launches).toEqual([]);
  });

  it("shows the language service step and its status text for debuggers it hosts", async () => {
    debug.guard.mockResolvedValue({ proceed: true, debugger: { pluginId: cfg.pluginId, type: "python", adapterKind: "jdtls" } });
    const ready = deferred<boolean>();
    debug.ensureJdtls.mockImplementation(async (_root: string, _plugin: string, _signal: AbortSignal, options: { onStatus?: (text: string) => void }) => {
      options.onStatus?.("Importing 40%");
      return ready.promise;
    });
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] });
    const store = useDebugSessionsStore();
    const started = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() => expect(store.launches[0]).toEqual(expect.objectContaining({ step: "languageService", languageService: true, status: "Importing 40%" })));
    const before = store.launches[0]!.progressAt;
    ready.resolve(true);
    expect(await started).toBe("s1");
    expect(store.launches).toEqual([]);
    expect(before).toBeGreaterThan(0);
  });

  it("cancelling ends the launch before the start request, without an error", async () => {
    const prepare = deferred<PreparedDebugLaunch>();
    debug.prepare.mockReturnValue(prepare.promise);
    const store = useDebugSessionsStore();
    const started = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() => expect(store.launches[0]?.step).toBe("resolving"));
    store.cancelLaunch(store.launches[0]!.seq);
    prepare.resolve(prepared(true));
    expect(await started).toBeNull();
    expect(debug.start).not.toHaveBeenCalled();
    expect(debug.message.error).not.toHaveBeenCalled();
    expect(store.launches).toEqual([]);
  });

  it("cancelling reaches the language service wait through the launch's signal", async () => {
    debug.guard.mockResolvedValue({ proceed: true, debugger: { pluginId: cfg.pluginId, type: "python", adapterKind: "jdtls" } });
    debug.ensureJdtls.mockImplementation(
      (_root: string, _plugin: string, signal: AbortSignal) => new Promise<boolean>((resolve) => signal.addEventListener("abort", () => resolve(false))),
    );
    const store = useDebugSessionsStore();
    const started = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() => expect(store.launches[0]?.step).toBe("languageService"));
    store.cancelLaunch(store.launches[0]!.seq);
    expect(await started).toBeNull();
    expect(debug.prepare).not.toHaveBeenCalled();
    expect(store.launches).toEqual([]);
  });

  it("marks a cancelled launch as cancelling until it ends (prepare cannot be interrupted)", async () => {
    const prepare = deferred<PreparedDebugLaunch>();
    debug.prepare.mockReturnValue(prepare.promise);
    const store = useDebugSessionsStore();
    const started = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() => expect(store.launches[0]?.step).toBe("resolving"));
    expect(store.launches[0]?.cancelling).toBe(false);
    store.cancelLaunch(store.launches[0]!.seq);
    expect(store.launches[0]?.cancelling).toBe(true);
    prepare.resolve(prepared(true));
    expect(await started).toBeNull();
    expect(store.launches).toEqual([]);
  });

  it("knows the language service step as soon as the guard found the debugger", async () => {
    const guard = deferred<{ proceed: boolean }>();
    const jdtls = { pluginId: cfg.pluginId, type: "python", adapterKind: "jdtls" };
    debug.guard.mockImplementation(async (_root: string, _cfg: RunConfiguration, _file: string, onDebugger?: (found: typeof jdtls) => void) => {
      onDebugger?.(jdtls);
      return guard.promise;
    });
    const store = useDebugSessionsStore();
    const started = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() => expect(store.launches[0]?.languageService).toBe(true));
    expect(store.launches[0]?.step).toBe("checking");
    guard.resolve({ proceed: false });
    expect(await started).toBeNull();
  });

  it("tells the language service wait whether the panel shows the launch's Cancel", async () => {
    debug.guard.mockResolvedValue({ proceed: true, debugger: { pluginId: cfg.pluginId, type: "python", adapterKind: "jdtls" } });
    let cancelInPanel: (() => boolean) | undefined;
    const ready = deferred<boolean>();
    debug.ensureJdtls.mockImplementation(async (_root: string, _plugin: string, _signal: AbortSignal, options: { cancelInPanel?: () => boolean }) => {
      cancelInPanel = options.cancelInPanel;
      return ready.promise;
    });
    const store = useDebugSessionsStore();
    const started = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() => expect(cancelInPanel).toBeDefined());
    expect(cancelInPanel!()).toBe(false);
    const seq = store.launches[0]!.seq;
    store.setLaunchShown(seq, true);
    expect(cancelInPanel!()).toBe(true);
    store.setLaunchShown(seq, false);
    expect(cancelInPanel!()).toBe(false);
    ready.resolve(false);
    expect(await started).toBeNull();
  });

  it("passes the last status text on when the language service wait ends within the interval", async () => {
    debug.guard.mockResolvedValue({ proceed: true, debugger: { pluginId: cfg.pluginId, type: "python", adapterKind: "jdtls" } });
    debug.ensureJdtls.mockImplementation(async (_root: string, _plugin: string, _signal: AbortSignal, options: { onStatus?: (text: string) => void }) => {
      options.onStatus?.("Importing 10%");
      options.onStatus?.("Importing 90%");
      return false;
    });
    const statuses: string[] = [];
    await launchDebugSession(ROOT, cfg, { noDebug: false, activeFile: FILE, onProgress: (progress) => progress.status !== undefined && statuses.push(progress.status) });
    expect(statuses).toEqual(["Importing 10%", "Importing 90%"]);
  });

  it("shows the language service's status texts at most every 100 ms, always the last one", async () => {
    vi.useFakeTimers();
    try {
      debug.guard.mockResolvedValue({ proceed: true, debugger: { pluginId: cfg.pluginId, type: "python", adapterKind: "jdtls" } });
      let onStatus: ((text: string) => void) | undefined;
      const ready = deferred<boolean>();
      debug.ensureJdtls.mockImplementation(async (_root: string, _plugin: string, _signal: AbortSignal, options: { onStatus?: (text: string) => void }) => {
        onStatus = options.onStatus;
        return ready.promise;
      });
      const store = useDebugSessionsStore();
      const started = store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
      await vi.advanceTimersByTimeAsync(0);
      expect(onStatus).toBeDefined();
      onStatus!("1%");
      expect(store.launches[0]?.status).toBe("1%");
      onStatus!("2%");
      onStatus!("3%");
      expect(store.launches[0]?.status).toBe("1%");
      await vi.advanceTimersByTimeAsync(99);
      expect(store.launches[0]?.status).toBe("1%");
      await vi.advanceTimersByTimeAsync(1);
      expect(store.launches[0]?.status).toBe("3%");
      ready.resolve(false);
      expect(await started).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("a failed launch clears its progress", async () => {
    debug.prepare.mockRejectedValue(new Error("internal: boom"));
    const store = useDebugSessionsStore();
    expect(await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE })).toBeNull();
    expect(store.launches).toEqual([]);
  });

  it("orders the steps: language service ready → prepare → confirmation → trust → settings → start", async () => {
    const order: string[] = [];
    const withSettings = { pluginId: cfg.pluginId, type: "python", hasSettings: true, adapterKind: "jdtls", exceptionTypes: {} };
    debug.guard.mockResolvedValue({ proceed: true, debugger: withSettings });
    debug.ensureJdtls.mockImplementation(async () => {
      await Promise.resolve();
      order.push("languageService");
      return true;
    });
    debug.prepare.mockImplementation(async () => {
      order.push("prepare");
      return prepared(false);
    });
    debug.confirm.mockImplementation(async () => {
      order.push("confirm");
      return true;
    });
    debug.trust.mockImplementation(async () => {
      order.push("trust");
      return prepared(true);
    });
    debug.applySettings.mockImplementation(async () => {
      await Promise.resolve();
      order.push("settings");
    });
    debug.start.mockImplementation(async () => {
      order.push("start");
      return { sessionId: "s1", capabilities: {}, skippedBreakpoints: [] };
    });
    const steps: string[] = [];
    await launchDebugSession(ROOT, cfg, { noDebug: false, activeFile: FILE, onProgress: (progress) => progress.step && steps.push(progress.step) });
    expect(order).toEqual(["languageService", "prepare", "confirm", "trust", "settings", "start"]);
    expect(steps).toEqual(["checking", "languageService", "resolving", "launching"]);
  });
});

describe("exception types (debugger settings)", () => {
  const withSettings = { pluginId: cfg.pluginId, type: "python", hasSettings: true, exceptionTypes: {} };

  it("sends the saved list to the debugger's settings right before start", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setExceptionTypes(ROOT, "python", ["app.Failure", "app.Other"]);
    debug.guard.mockResolvedValue({ proceed: true, debugger: withSettings });
    await startSession("s1");
    expect(debug.applySettings).toHaveBeenCalledExactlyOnceWith({
      pluginId: cfg.pluginId,
      type: "python",
      projectRoot: ROOT,
      exceptionTypes: ["app.Failure", "app.Other"],
    });
    // After prepare (and any confirmation), before the session starts and sets its exception breakpoints.
    expect(debug.prepare.mock.invocationCallOrder[0]).toBeLessThan(debug.applySettings.mock.invocationCallOrder[0]!);
    expect(debug.applySettings.mock.invocationCallOrder[0]).toBeLessThan(debug.start.mock.invocationCallOrder[0]!);
  });

  it("sends the list as it is after the launch confirmation", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setExceptionTypes(ROOT, "python", ["app.Before"]);
    debug.guard.mockResolvedValue({ proceed: true, debugger: withSettings });
    debug.prepare.mockResolvedValue(prepared(false));
    debug.confirm.mockImplementation(async () => {
      // The user edits the list while the confirmation is open.
      configs.setExceptionTypes(ROOT, "python", ["app.During"]);
      return true;
    });
    debug.trust.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] });
    await launchDebugSession(ROOT, cfg, { noDebug: false, activeFile: FILE });
    expect(debug.applySettings).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ exceptionTypes: ["app.During"] }));
    expect(debug.trust.mock.invocationCallOrder[0]).toBeLessThan(debug.applySettings.mock.invocationCallOrder[0]!);
    expect(debug.applySettings.mock.invocationCallOrder[0]).toBeLessThan(debug.start.mock.invocationCallOrder[0]!);
  });

  it("does not sync the settings when the launch is declined", async () => {
    debug.guard.mockResolvedValue({ proceed: true, debugger: withSettings });
    debug.prepare.mockResolvedValue(prepared(false));
    debug.confirm.mockResolvedValue(false);
    expect(await launchDebugSession(ROOT, cfg, { noDebug: false, activeFile: FILE })).toBeNull();
    expect(debug.applySettings).not.toHaveBeenCalled();
    expect(debug.start).not.toHaveBeenCalled();
  });

  it("sends an explicit empty list when nothing is saved", async () => {
    debug.guard.mockResolvedValue({ proceed: true, debugger: withSettings });
    await startSession("s1");
    expect(debug.applySettings).toHaveBeenCalledWith(expect.objectContaining({ exceptionTypes: [] }));
  });

  it("warns and still starts when the settings cannot be applied", async () => {
    debug.guard.mockResolvedValue({ proceed: true, debugger: withSettings });
    debug.applySettings.mockRejectedValue(new Error('timeout: plugin "mincode.python-debug" did not answer in time'));
    await startSession("s1");
    const { t } = useVueI18n();
    expect(debug.message.warning).toHaveBeenCalledWith(
      expect.objectContaining({ message: t("debug.settings.syncFailed", { error: 'timeout: plugin "mincode.python-debug" did not answer in time' }) }),
    );
    expect(t("debug.settings.syncFailed", { error: "x" })).toBe("异常类型过滤可能未生效：x");
    expect(debug.start).toHaveBeenCalledTimes(1);
    expect(debug.message.error).not.toHaveBeenCalled();
  });

  it("does not call debuggers without settings, nor for runs without debugging", async () => {
    debug.guard.mockResolvedValue({ proceed: true, debugger: { ...withSettings, hasSettings: false } });
    await startSession("s1");
    debug.guard.mockResolvedValue({ proceed: true });
    await startSession("s2");
    debug.guard.mockResolvedValue({ proceed: true, debugger: withSettings });
    debug.prepare.mockResolvedValueOnce(prepared(true));
    debug.start.mockResolvedValueOnce({ sessionId: "run", capabilities: {}, skippedBreakpoints: [] });
    await useDebugSessionsStore().start(ROOT, cfg, { noDebug: true, activeFile: FILE });
    expect(debug.applySettings).not.toHaveBeenCalled();
  });

  it("re-syncs running sessions of the type in the project after the list changed", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setExceptionFilters(ROOT, "python", ["raised"]);
    const store = await startSession("s1");
    await startSession("s2");
    debug.prepare.mockResolvedValueOnce(prepared(true));
    debug.start.mockResolvedValueOnce({ sessionId: "run", capabilities: {}, skippedBreakpoints: [] });
    await store.start(ROOT, cfg, { noDebug: true, activeFile: FILE });
    emit({ sessionId: "s2", event: "mincode.sessionEnded", body: { reason: "exited" } });
    debug.request.mockClear();

    configs.setExceptionTypes(ROOT, "python", ["app.Failure"]);
    await store.syncExceptionTypes(ROOT, withSettings);
    // One call per project: the settings belong to the debugger's process for the project.
    expect(debug.applySettings).toHaveBeenCalledExactlyOnceWith({ pluginId: cfg.pluginId, type: "python", projectRoot: ROOT, exceptionTypes: ["app.Failure"] });
    // The running session re-reads them when its exception breakpoints are set again.
    expect(debug.request.mock.calls).toEqual([["s1", "setExceptionBreakpoints", { filters: ["raised"] }]]);
    expect(debug.applySettings.mock.invocationCallOrder[0]).toBeLessThan(debug.request.mock.invocationCallOrder[0]!);
  });

  it("does nothing without a running session of the type, or for a debugger without settings", async () => {
    const store = useDebugSessionsStore();
    await store.syncExceptionTypes(ROOT, withSettings);
    await startSession("s1");
    await store.syncExceptionTypes(ROOT, { ...withSettings, hasSettings: false });
    await store.syncExceptionTypes(ROOT, { ...withSettings, type: "java" });
    await store.syncExceptionTypes("/Users/A/Other", withSettings);
    expect(debug.applySettings).not.toHaveBeenCalled();
  });

  it("warns when a running session cannot be re-synced; the list stays saved", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    const store = await startSession("s1");
    debug.request.mockClear();
    configs.setExceptionTypes(ROOT, "python", ["app.Failure"]);
    debug.applySettings.mockRejectedValue(new Error("unavailable: Java 语言服务未就绪"));
    await store.syncExceptionTypes(ROOT, withSettings);
    const { t } = useVueI18n();
    expect(debug.message.warning).toHaveBeenCalledWith(
      expect.objectContaining({ message: t("debug.settings.syncFailed", { error: "unavailable: Java 语言服务未就绪" }) }),
    );
    expect(debug.request).not.toHaveBeenCalled();
    expect(configs.stateFor(ROOT).exceptionTypes.python).toEqual(["app.Failure"]);
  });
});

describe("hot code replace declaration", () => {
  it("the session keeps the debugger's plugin and its hot code replace declaration", async () => {
    debug.guard.mockResolvedValue({
      proceed: true,
      debugger: { pluginId: cfg.pluginId, type: "python", title: "debugpy", languages: [], hotCodeReplace: { event: "codeRebuilt", request: "applyCode" } },
    });
    const store = await startSession("s1");
    expect(store.sessions[0]!.pluginId).toBe(cfg.pluginId);
    expect(store.sessions[0]!.hotCodeReplace).toEqual({ event: "codeRebuilt", request: "applyCode" });
    expect(store.sessions[0]!.pendingCodeChanges).toBe(false);
  });

  it("a debugger without the declaration gives the session none", async () => {
    debug.guard.mockResolvedValue({ proceed: true, debugger: { pluginId: cfg.pluginId, type: "python", title: "debugpy", languages: [] } });
    const store = await startSession("s1");
    expect(store.sessions[0]!.hotCodeReplace).toBeUndefined();
    expect(debug.listDebuggers).not.toHaveBeenCalled();
  });

  it("reads the listing again when it failed at launch and handles the events that came meanwhile", async () => {
    // The guard proceeds without a debugger when the listing failed.
    let answer: (list: unknown[]) => void = () => undefined;
    debug.listDebuggers.mockReturnValueOnce(new Promise((resolve) => (answer = resolve)));
    const store = await startSession("s1");
    expect(debug.listDebuggers).toHaveBeenCalledTimes(1);
    expect(store.sessions[0]!.hotCodeReplace).toBeUndefined();
    emit({ sessionId: "s1", event: "codeRebuilt", body: { changeType: "ERROR", message: "Add method not implemented" } });
    emit({ sessionId: "s1", event: "output", body: { output: "x" } });
    answer([
      { pluginId: "other", type: "python", hotCodeReplace: { event: "wrong", request: "wrong" } },
      { pluginId: cfg.pluginId, type: "python", hotCodeReplace: { event: "codeRebuilt", request: "applyCode" } },
    ]);
    await flushPromises();
    expect(store.sessions[0]!.hotCodeReplace).toEqual({ event: "codeRebuilt", request: "applyCode" });
    expect(store.sessions[0]!.hotCodeReplaceMessage).toEqual({ kind: "error", text: "Add method not implemented" });
    // Later events go straight through.
    emit({ sessionId: "s1", event: "codeRebuilt", body: { changeType: "WARNING", message: "later" } });
    await flushPromises();
    expect(store.sessions[0]!.hotCodeReplaceMessage).toEqual({ kind: "warning", text: "later" });
  });

  it("stays without hot code replace when the listing fails again", async () => {
    debug.listDebuggers.mockRejectedValueOnce(new Error("unavailable"));
    const store = await startSession("s1");
    await flushPromises();
    emit({ sessionId: "s1", event: "codeRebuilt", body: { changeType: "ERROR", message: "x" } });
    await flushPromises();
    expect(store.sessions[0]!.hotCodeReplace).toBeUndefined();
    expect(store.sessions[0]!.hotCodeReplaceMessage).toBeUndefined();
  });
});

describe("method breakpoints", () => {
  const capable = { supportsFunctionBreakpoints: true };

  async function startCapable(id: string, capabilities: Record<string, unknown> = capable, noDebug = false) {
    debug.start.mockResolvedValueOnce({ sessionId: id, capabilities, skippedBreakpoints: [] });
    debug.prepare.mockResolvedValueOnce(prepared(true));
    const store = useDebugSessionsStore();
    expect(await store.start(ROOT, cfg, { noDebug, activeFile: FILE })).toBe(id);
    return store;
  }

  async function savedList() {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setFunctionBreakpoints(ROOT, "python", [
      { name: "app.run", enabled: true },
      { name: "app.off", enabled: false },
      { name: "app.nope", enabled: true },
    ]);
    configs.setFunctionBreakpoints(ROOT, "other", [{ name: "other.fn", enabled: true }]);
    return configs;
  }

  it.each([false, true])("launch sends the type's enabled method breakpoints in the start request (noDebug=%s)", async (noDebug) => {
    await savedList();
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValue({ sessionId: "s1", capabilities: capable, skippedBreakpoints: [] });
    const result = await launchDebugSession(ROOT, cfg, { noDebug, activeFile: FILE });
    const request = debug.start.mock.calls[0]![0] as Record<string, unknown>;
    if (noDebug) {
      expect(request.functionBreakpoints).toBeUndefined();
      expect(result?.functionBreakpointNames).toEqual([]);
    } else {
      expect(request.functionBreakpoints).toEqual([{ name: "app.run" }, { name: "app.nope" }]);
      expect(result?.functionBreakpointNames).toEqual(["app.run", "app.nope"]);
    }
  });

  it("pairs the start-time results with the sent names; a short reply leaves the rest unverified", async () => {
    await savedList();
    const store = await startCapable("s1");
    expect(store.sessions[0]!.functionBreakpoints).toEqual([
      { name: "app.run", verified: false },
      { name: "app.nope", verified: false },
    ]);
    emit({ sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "", functionBreakpoints: [{ id: 1, verified: false, line: 0 }] } });
    expect(store.sessions[0]!.functionBreakpoints).toEqual([
      { name: "app.run", verified: false, id: 1 },
      { name: "app.nope", verified: false },
    ]);
    // The class was loaded.
    emit({ sessionId: "s1", event: "breakpoint", body: { reason: "changed", breakpoint: { id: 1, verified: true, line: 0 } } });
    expect(store.functionBreakpointStatusFor(ROOT, "python")).toEqual({ "app.run": { verified: true }, "app.nope": { verified: false } });
  });

  it("keeps no method breakpoint results for an adapter without the capability", async () => {
    await savedList();
    const store = await startCapable("s1", {});
    expect(store.sessions[0]!.functionBreakpoints).toBeUndefined();
    expect(store.functionBreakpointStatusFor(ROOT, "python")).toEqual({});
  });

  it("buffers start-time results that arrive before the session is listed", async () => {
    await savedList();
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockImplementation(async () => {
      emit({ sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "", functionBreakpoints: [{ id: 1, verified: true }, { id: 2, verified: false, message: "no class" }] } });
      return { sessionId: "s1", capabilities: capable, skippedBreakpoints: [] };
    });
    const store = useDebugSessionsStore();
    await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    expect(store.sessions[0]!.functionBreakpoints).toEqual([
      { name: "app.run", verified: true, id: 1 },
      { name: "app.nope", verified: false, id: 2, message: "no class" },
    ]);
  });

  it("syncs only enabled names to the project's running sessions of the type that support them", async () => {
    const configs = await savedList();
    const store = await startCapable("s1");
    await startCapable("s2", {});
    await startCapable("run", capable, true);
    await startCapable("s3");
    emit({ sessionId: "s3", event: "mincode.sessionEnded", body: { reason: "exited" } });
    debug.request.mockReset().mockResolvedValue({ breakpoints: [{ id: 5, verified: true }] });
    configs.setFunctionBreakpoints(ROOT, "python", [
      { name: "app.run", enabled: false },
      { name: "app.more", enabled: true },
      { name: "app.nope", enabled: true },
    ]);
    await store.syncFunctionBreakpoints(ROOT, "python");
    expect(debug.request.mock.calls).toEqual([["s1", "setFunctionBreakpoints", { breakpoints: [{ name: "app.more" }, { name: "app.nope" }] }]]);
    // The adapter answered one result for two names: the missing one is unverified.
    expect(store.sessions.find((view) => view.id === "s1")!.functionBreakpoints).toEqual([
      { name: "app.more", verified: true, id: 5 },
      { name: "app.nope", verified: false },
    ]);
    debug.request.mockClear();
    await store.syncFunctionBreakpoints(ROOT, "other");
    await store.syncFunctionBreakpoints("/Users/A/Other", "python");
    expect(debug.request).not.toHaveBeenCalled();
  });

  it("a failed request leaves the names unverified with the failure and no session error", async () => {
    await savedList();
    const store = await startCapable("s1");
    debug.request.mockReset().mockRejectedValue(new Error("dap setFunctionBreakpoints failed: bad request"));
    await expect(store.syncFunctionBreakpoints(ROOT, "python")).resolves.toBeUndefined();
    expect(store.sessions[0]!.functionBreakpoints).toEqual([
      { name: "app.run", verified: false, message: "bad request" },
      { name: "app.nope", verified: false, message: "bad request" },
    ]);
    expect(store.functionBreakpointStatusFor(ROOT, "python")["app.run"]).toEqual({ verified: false, message: "bad request" });
    expect(store.sessions[0]!.lastError).toBeUndefined();
  });

  it("sends a list changed during a request once more, after that request, and keeps the newest answer", async () => {
    const configs = await savedList();
    const store = await startCapable("s1");
    let answerFirst!: (body: unknown) => void;
    debug.request.mockReset().mockImplementationOnce(() => new Promise((resolve) => (answerFirst = resolve)));
    const first = store.syncFunctionBreakpoints(ROOT, "python");
    configs.setFunctionBreakpoints(ROOT, "python", [{ name: "app.only", enabled: true }]);
    debug.request.mockResolvedValueOnce({ breakpoints: [{ id: 9, verified: true }] });
    const second = store.syncFunctionBreakpoints(ROOT, "python");
    await vi.waitFor(() => expect(answerFirst).toBeTypeOf("function"));
    // The adapter replaces the whole list per request: the change waits for the request in flight.
    expect(debug.request).toHaveBeenCalledTimes(1);
    answerFirst({ breakpoints: [{ id: 1, verified: true }, { id: 2, verified: true }] });
    await Promise.all([first, second]);
    expect(debug.request).toHaveBeenCalledTimes(2);
    expect(debug.request).toHaveBeenLastCalledWith("s1", "setFunctionBreakpoints", { breakpoints: [{ name: "app.only" }] });
    expect(store.sessions[0]!.functionBreakpoints).toEqual([{ name: "app.only", verified: true, id: 9 }]);
  });

  it("sends a list changed while the start was pending once the session is listed", async () => {
    const configs = await savedList();
    debug.prepare.mockResolvedValue(prepared(true));
    debug.request.mockResolvedValue({ breakpoints: [] });
    debug.start.mockImplementation(async () => {
      configs.setFunctionBreakpoints(ROOT, "python", [{ name: "app.late", enabled: true }]);
      return { sessionId: "s1", capabilities: capable, skippedBreakpoints: [] };
    });
    const store = useDebugSessionsStore();
    await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() => expect(debug.request).toHaveBeenCalledWith("s1", "setFunctionBreakpoints", { breakpoints: [{ name: "app.late" }] }));
  });

  it("muting sends an empty list and unmuting the enabled names again", async () => {
    await savedList();
    const store = await startCapable("s1");
    debug.request.mockReset().mockResolvedValue({ breakpoints: [] });
    await store.setBreakpointsMuted("s1", true);
    expect(debug.request).toHaveBeenCalledWith("s1", "setFunctionBreakpoints", { breakpoints: [] });
    debug.request.mockClear();
    await store.syncFunctionBreakpoints(ROOT, "python");
    expect(debug.request).toHaveBeenCalledWith("s1", "setFunctionBreakpoints", { breakpoints: [] });
    debug.request.mockClear();
    await store.setBreakpointsMuted("s1", false);
    expect(debug.request).toHaveBeenCalledWith("s1", "setFunctionBreakpoints", { breakpoints: [{ name: "app.run" }, { name: "app.nope" }] });
  });

  it("a name is verified when any running session verified it; otherwise the first message is shown", async () => {
    await savedList();
    const store = await startCapable("s1");
    await startCapable("s2");
    emit({ sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "", functionBreakpoints: [{ verified: false, message: "first" }, { verified: false }] } });
    emit({ sessionId: "s2", event: "mincode.breakpointsSet", body: { path: "", functionBreakpoints: [{ verified: true }, { verified: false, message: "second" }] } });
    expect(store.functionBreakpointStatusFor(ROOT, "python")).toEqual({
      "app.run": { verified: true },
      "app.nope": { verified: false, message: "second" },
    });
    emit({ sessionId: "s2", event: "mincode.sessionEnded", body: { reason: "exited" } });
    expect(store.functionBreakpointStatusFor(ROOT, "python")).toEqual({
      "app.run": { verified: false, message: "first" },
      "app.nope": { verified: false },
    });
  });
});

describe("field breakpoints", () => {
  const capable = { supportsDataBreakpoints: true };
  // As java-debug 0.53.1 answers dataBreakpointInfo for an object's field.
  const countInfo: DataBreakpointInfo = {
    dataId: "Counter#count",
    description: "Counter.count : int",
    accessTypes: ["read", "write", "readWrite"],
    canPersist: true,
  };
  const countWrite = { dataId: "Counter#count", description: "Counter.count : int", accessType: "write" as const };
  const countRead = { ...countWrite, accessType: "read" as const };
  const localInfo: DataBreakpointInfo = { dataId: "frame#local", description: "local", accessTypes: ["write"], canPersist: false };
  const localWrite = { dataId: "frame#local", description: "local", accessType: "write" as const };

  async function startCapable(id: string, capabilities: Record<string, unknown> = capable, noDebug = false) {
    debug.start.mockResolvedValueOnce({ sessionId: id, capabilities, skippedBreakpoints: [] });
    debug.prepare.mockResolvedValueOnce(prepared(true));
    const store = useDebugSessionsStore();
    expect(await store.start(ROOT, cfg, { noDebug, activeFile: FILE })).toBe(id);
    return store;
  }

  function pause(id: string) {
    emit({ sessionId: id, event: "stopped", body: { reason: "breakpoint", threadId: 1 } });
  }

  function dataRequests() {
    return debug.request.mock.calls.filter((call) => call[1] === "setDataBreakpoints");
  }

  /** Field breakpoint requests (a stop also reads threads and frames). */
  function fieldRequests() {
    return debug.request.mock.calls.filter((call) => call[1] === "setDataBreakpoints" || call[1] === "dataBreakpointInfo");
  }

  async function loadedConfigs() {
    const configs = useDebugConfigsStore();
    await configs.load();
    return configs;
  }

  it.each([false, true])("launch sends the type's saved field breakpoints in the start request (noDebug=%s)", async (noDebug) => {
    const configs = await loadedConfigs();
    configs.setDataBreakpoints(ROOT, "python", [countWrite, countRead]);
    configs.setDataBreakpoints(ROOT, "other", [{ dataId: "O#x", description: "O.x", accessType: "write" }]);
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockResolvedValue({ sessionId: "s1", capabilities: capable, skippedBreakpoints: [] });
    const result = await launchDebugSession(ROOT, cfg, { noDebug, activeFile: FILE });
    const request = debug.start.mock.calls[0]![0] as Record<string, unknown>;
    if (noDebug) {
      expect(request.dataBreakpoints).toBeUndefined();
      expect(result?.dataBreakpoints).toEqual([]);
    } else {
      expect(request.dataBreakpoints).toEqual([
        { dataId: "Counter#count", accessType: "write" },
        { dataId: "Counter#count", accessType: "read" },
      ]);
      expect(result?.dataBreakpoints).toEqual([countWrite, countRead]);
    }
  });

  it("pairs buffered start-time results with the sent breakpoints and verifies them when the class loads", async () => {
    const configs = await loadedConfigs();
    configs.setDataBreakpoints(ROOT, "python", [countWrite]);
    debug.prepare.mockResolvedValue(prepared(true));
    debug.start.mockImplementation(async () => {
      emit({ sessionId: "s1", event: "mincode.breakpointsSet", body: { path: "", dataBreakpoints: [{ id: 1, verified: false, line: 0 }] } });
      return { sessionId: "s1", capabilities: capable, skippedBreakpoints: [] };
    });
    const store = useDebugSessionsStore();
    await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    expect(store.sessions[0]!.dataBreakpoints).toEqual([{ ...countWrite, verified: false, id: 1 }]);
    expect(store.dataBreakpointEntries(ROOT)).toEqual([{ ...countWrite, type: "python", sessionOnly: false, status: { verified: false } }]);
    emit({ sessionId: "s1", event: "breakpoint", body: { reason: "new", breakpoint: { id: 1, verified: true, line: 0 } } });
    expect(store.dataBreakpointEntries(ROOT)[0]!.status).toEqual({ verified: true });
  });

  it("keeps no field breakpoint results for an adapter without the capability", async () => {
    const configs = await loadedConfigs();
    configs.setDataBreakpoints(ROOT, "python", [countWrite]);
    const store = await startCapable("s1", {});
    expect(store.sessions[0]!.dataBreakpoints).toBeUndefined();
    expect(store.dataBreakpointEntries(ROOT)).toEqual([{ ...countWrite, type: "python", sessionOnly: false }]);
  });

  it("asks dataBreakpointInfo only while paused with the capability, and reads no answer as no breakpoint", async () => {
    const store = await startCapable("s1");
    await startCapable("s2", {});
    debug.request.mockReset().mockResolvedValue(countInfo);
    // Running: no request (the references belong to a stop).
    expect(await store.dataBreakpointInfo("s1", 3, "count")).toEqual({ dataId: null, description: "" });
    expect(fieldRequests()).toEqual([]);
    pause("s1");
    pause("s2");
    expect(await store.dataBreakpointInfo("s2", 3, "count")).toEqual({ dataId: null, description: "" });
    expect(await store.dataBreakpointInfo("s1", 0, "count")).toEqual({ dataId: null, description: "" });
    expect(fieldRequests()).toEqual([]);
    expect(await store.dataBreakpointInfo("s1", 3, "count")).toEqual(countInfo);
    expect(fieldRequests()).toEqual([["s1", "dataBreakpointInfo", { variablesReference: 3, name: "count" }]]);
    // java-debug answers a variable that is no field without a body.
    debug.request.mockResolvedValueOnce(null);
    expect((await store.dataBreakpointInfo("s1", 2, "local")).dataId).toBeNull();
    debug.request.mockRejectedValueOnce(new Error("adapter: no"));
    expect((await store.dataBreakpointInfo("s1", 3, "count")).dataId).toBeNull();
    expect(store.sessions[0]!.lastError).toBeUndefined();
    // An answer that comes after the program ran on is dropped.
    let answer!: (body: unknown) => void;
    debug.request.mockImplementationOnce(() => new Promise((resolve) => (answer = resolve)));
    const late = store.dataBreakpointInfo("s1", 3, "count");
    emit({ sessionId: "s1", event: "continued", body: { threadId: 1 } });
    answer(countInfo);
    expect((await late).dataId).toBeNull();
  });

  it("a persistable field breakpoint is saved and sent, with all current ones, to the project's sessions of the type", async () => {
    const configs = await loadedConfigs();
    const store = await startCapable("s1");
    await startCapable("s2");
    await startCapable("s3", {});
    await startCapable("run", capable, true);
    pause("s1");
    debug.request.mockReset().mockResolvedValue({ breakpoints: [{ id: 2, verified: true, line: 0 }] });
    await store.toggleDataBreakpoint("s1", countInfo, "write");
    expect(configs.stateFor(ROOT).dataBreakpoints.python).toEqual([countWrite]);
    expect(dataRequests()).toEqual([
      ["s1", "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#count", accessType: "write" }] }],
      ["s2", "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#count", accessType: "write" }] }],
    ]);
    expect(store.sessions.find((view) => view.id === "s1")!.dataBreakpoints).toEqual([{ ...countWrite, verified: true, id: 2 }]);
    expect(store.hasDataBreakpoint("s1", "Counter#count", "write")).toBe(true);
    expect(store.hasDataBreakpoint("s1", "Counter#count", "read")).toBe(false);

    debug.request.mockClear();
    debug.request.mockResolvedValue({ breakpoints: [{ id: 2, verified: true }, { id: 3, verified: true }] });
    await store.toggleDataBreakpoint("s1", countInfo, "read");
    expect(configs.stateFor(ROOT).dataBreakpoints.python).toEqual([countWrite, countRead]);
    expect(dataRequests()[0]).toEqual([
      "s1",
      "setDataBreakpoints",
      { breakpoints: [{ dataId: "Counter#count", accessType: "write" }, { dataId: "Counter#count", accessType: "read" }] },
    ]);

    // Choosing a set one again removes it.
    debug.request.mockClear();
    await store.toggleDataBreakpoint("s1", countInfo, "write");
    expect(configs.stateFor(ROOT).dataBreakpoints.python).toEqual([countRead]);
    expect(dataRequests()[0]).toEqual(["s1", "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#count", accessType: "read" }] }]);
  });

  it("a field breakpoint that cannot persist stays in its session and disappears when the session ends", async () => {
    const configs = await loadedConfigs();
    configs.setDataBreakpoints(ROOT, "python", [countWrite]);
    const store = await startCapable("s1");
    await startCapable("s2");
    pause("s1");
    debug.request.mockReset().mockResolvedValue({ breakpoints: [] });
    await store.toggleDataBreakpoint("s1", localInfo, "write");
    expect(configs.stateFor(ROOT).dataBreakpoints.python).toEqual([countWrite]);
    expect(store.sessions[0]!.sessionDataBreakpoints).toEqual([localWrite]);
    // The session gets every current one: the saved ones, then its own.
    expect(dataRequests()).toEqual([
      ["s1", "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#count", accessType: "write" }, { dataId: "frame#local", accessType: "write" }] }],
    ]);
    expect(store.dataBreakpointEntries(ROOT).map((entry) => [entry.dataId, entry.sessionOnly])).toEqual([
      ["Counter#count", false],
      ["frame#local", true],
    ]);

    emit({ sessionId: "s1", event: "mincode.sessionEnded", body: { reason: "exited" } });
    expect(store.dataBreakpointEntries(ROOT).map((entry) => entry.dataId)).toEqual(["Counter#count"]);

    // The next session is sent only the saved one.
    debug.start.mockClear();
    await startCapable("s4");
    expect((debug.start.mock.calls[0]![0] as Record<string, unknown>).dataBreakpoints).toEqual([{ dataId: "Counter#count", accessType: "write" }]);
  });

  it("does nothing unless paused with the capability and a field id", async () => {
    const configs = await loadedConfigs();
    const store = await startCapable("s1");
    await startCapable("s2", {});
    debug.request.mockReset().mockResolvedValue({ breakpoints: [] });
    await store.toggleDataBreakpoint("s1", countInfo, "write");
    pause("s2");
    await store.toggleDataBreakpoint("s2", countInfo, "write");
    pause("s1");
    await store.toggleDataBreakpoint("s1", { dataId: null, description: "" }, "write");
    expect(fieldRequests()).toEqual([]);
    expect(configs.stateFor(ROOT).dataBreakpoints).toEqual({});
  });

  it("refuses more than the host's limit, saying so", async () => {
    const configs = await loadedConfigs();
    configs.setDataBreakpoints(ROOT, "python", Array.from({ length: 256 }, (_, i) => ({ dataId: `C#f${i}`, description: `C.f${i}`, accessType: "write" as const })));
    const store = await startCapable("s1");
    pause("s1");
    debug.request.mockReset().mockResolvedValue({ breakpoints: [] });
    await store.toggleDataBreakpoint("s1", localInfo, "write");
    expect(store.sessions[0]!.sessionDataBreakpoints).toBeUndefined();
    expect(fieldRequests()).toEqual([]);
    expect(debug.message.warning).toHaveBeenCalledOnce();
  });

  it("removing from the dialog drops it from the saved list and from sessions, and re-sends", async () => {
    const configs = await loadedConfigs();
    configs.setDataBreakpoints(ROOT, "python", [countWrite, countRead]);
    const store = await startCapable("s1");
    pause("s1");
    debug.request.mockReset().mockResolvedValue({ breakpoints: [] });
    await store.toggleDataBreakpoint("s1", localInfo, "write");
    debug.request.mockClear();
    await store.removeDataBreakpoint(ROOT, "python", countWrite);
    expect(configs.stateFor(ROOT).dataBreakpoints.python).toEqual([countRead]);
    expect(dataRequests()).toEqual([
      ["s1", "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#count", accessType: "read" }, { dataId: "frame#local", accessType: "write" }] }],
    ]);
    debug.request.mockClear();
    await store.removeDataBreakpoint(ROOT, "python", localWrite);
    expect(store.sessions[0]!.sessionDataBreakpoints).toEqual([]);
    expect(dataRequests()).toEqual([["s1", "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#count", accessType: "read" }] }]]);
  });

  it("a failed request leaves them unverified with the failure and no session error; a change during a request is sent after it", async () => {
    const configs = await loadedConfigs();
    configs.setDataBreakpoints(ROOT, "python", [countWrite]);
    const store = await startCapable("s1");
    debug.request.mockReset().mockRejectedValue(new Error("dap setDataBreakpoints failed: bad request"));
    await store.syncDataBreakpoints(ROOT, "python");
    expect(store.sessions[0]!.dataBreakpoints).toEqual([{ ...countWrite, verified: false, message: "bad request" }]);
    expect(store.sessions[0]!.lastError).toBeUndefined();

    let answerFirst!: (body: unknown) => void;
    debug.request.mockReset().mockImplementationOnce(() => new Promise((resolve) => (answerFirst = resolve)));
    const first = store.syncDataBreakpoints(ROOT, "python");
    configs.setDataBreakpoints(ROOT, "python", [countRead]);
    debug.request.mockResolvedValueOnce({ breakpoints: [{ id: 9, verified: true }] });
    const second = store.syncDataBreakpoints(ROOT, "python");
    await vi.waitFor(() => expect(answerFirst).toBeTypeOf("function"));
    expect(debug.request).toHaveBeenCalledTimes(1);
    answerFirst({ breakpoints: [{ id: 1, verified: false }] });
    await Promise.all([first, second]);
    expect(debug.request).toHaveBeenCalledTimes(2);
    expect(store.sessions[0]!.dataBreakpoints).toEqual([{ ...countRead, verified: true, id: 9 }]);
  });

  it("sends a list changed while the start was pending once the session is listed", async () => {
    const configs = await loadedConfigs();
    debug.prepare.mockResolvedValue(prepared(true));
    debug.request.mockResolvedValue({ breakpoints: [] });
    debug.start.mockImplementation(async () => {
      configs.setDataBreakpoints(ROOT, "python", [countWrite]);
      return { sessionId: "s1", capabilities: capable, skippedBreakpoints: [] };
    });
    const store = useDebugSessionsStore();
    await store.start(ROOT, cfg, { noDebug: false, activeFile: FILE });
    await vi.waitFor(() =>
      expect(debug.request).toHaveBeenCalledWith("s1", "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#count", accessType: "write" }] }),
    );
  });

  it("muting sends an empty list and unmuting every current one again", async () => {
    const configs = await loadedConfigs();
    configs.setDataBreakpoints(ROOT, "python", [countWrite]);
    const store = await startCapable("s1");
    debug.request.mockReset().mockResolvedValue({ breakpoints: [] });
    await store.setBreakpointsMuted("s1", true);
    expect(debug.request).toHaveBeenCalledWith("s1", "setDataBreakpoints", { breakpoints: [] });
    debug.request.mockClear();
    await store.setBreakpointsMuted("s1", false);
    expect(debug.request).toHaveBeenCalledWith("s1", "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#count", accessType: "write" }] });
  });
});
