import { afterEach, describe, expect, it, vi } from "vitest";

const bridge = vi.hoisted(() => ({
  native: true,
  invoke: vi.fn(),
  listen: vi.fn(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => bridge.native,
  invokeBackend: bridge.invoke,
  listenAppEvent: bridge.listen,
}));

import {
  applyDebuggerSettings,
  attachTerminal,
  checkDebugger,
  debugRequest,
  listDebuggers,
  onDebugEvent,
  parseBreakpointListEvent,
  parseBreakpointsSetEvent,
  prepareDebugger,
  setSessionAddressBreakpoints,
  setSessionBreakpoints,
  startDebugger,
  stopDebugSession,
} from "@/services/debug-service";
import { DEBUG_BREAKPOINTS_SET_EVENT, type DebugEvent, type PreparedDebugLaunch } from "@/domain/debug/types";

const fingerprint = "a".repeat(64);

const prepared: PreparedDebugLaunch = {
  prepareId: "p1",
  launch: {
    pluginId: "python-debug",
    pluginName: "Python Debugger",
    pluginVersion: "0.1.0",
    point: "debuggers",
    entryKey: "python@abc",
    title: "Python (debugpy)",
    argv: ["/usr/bin/python3", "-m", "debugpy.adapter"],
    env: {},
    cwd: "/work/app",
    pluginExecutable: false,
    fingerprint,
    trusted: false,
    previouslyTrusted: false,
  },
  type: "python",
  kind: "stdio",
  projectRoot: "/work/app",
  request: "launch",
  arguments: { program: "/work/app/main.py" },
};

afterEach(() => {
  bridge.native = true;
  bridge.invoke.mockReset();
  bridge.listen.mockReset();
});

describe("debug service", () => {
  it("prepareDebugger sends the backend payload verbatim", async () => {
    bridge.invoke.mockResolvedValue(prepared);
    const request = {
      pluginId: "python-debug",
      type: "python",
      projectRoot: "/work/app",
      configuration: { request: "launch", program: "main.py" },
      noDebug: false,
    };
    await expect(prepareDebugger(request)).resolves.toEqual(prepared);
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_host_debugger_prepare", { payload: request });
  });

  it("prepareDebugger accepts a jdtls adapter launch (no process argv)", async () => {
    const jdtls: PreparedDebugLaunch = {
      ...prepared,
      launch: {
        ...prepared.launch,
        argv: ["jdtls", "workspace/executeCommand", "vscode.java.startDebugSession"],
        pluginFiles: [{ relative: "bundles/java-debug.jar", sha256: "b".repeat(64) }],
      },
      type: "java",
      kind: "jdtls",
      arguments: { mainClass: "com.example.Main" },
    };
    bridge.invoke.mockResolvedValue(jdtls);
    await expect(
      prepareDebugger({ pluginId: "example-debug", type: "example", projectRoot: "/work/app", configuration: {}, noDebug: false }),
    ).resolves.toEqual(jdtls);
  });

  it("startDebugger strips enabled and disabled breakpoints", async () => {
    bridge.invoke.mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] });
    await startDebugger({
      prepareId: "p1",
      fingerprint,
      breakpoints: {
        "/work/app/main.py": [
          { line: 3, enabled: true },
          { line: 7, condition: "x > 1", enabled: false },
          { line: 9, hitCondition: "3" },
        ],
        "/work/app/off.py": [{ line: 1, enabled: false }],
      },
      exceptionFilters: ["raised"],
    });
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_host_debugger_start", {
      payload: {
        prepareId: "p1",
        fingerprint,
        breakpoints: { "/work/app/main.py": [{ line: 3 }, { line: 9, hitCondition: "3" }] },
        exceptionFilters: ["raised"],
      },
    });
  });

  it("startDebugger sends enabled function and data breakpoints in DAP shape", async () => {
    bridge.invoke.mockResolvedValue({ sessionId: "s1", capabilities: { supportsFunctionBreakpoints: true }, skippedBreakpoints: [] });
    await startDebugger({
      prepareId: "p1",
      fingerprint,
      breakpoints: {},
      exceptionFilters: [],
      functionBreakpoints: [
        { name: "pkg.Type.run", enabled: true },
        { name: "pkg.Type.off", enabled: false },
        { name: "pkg.Type.stop", condition: "x > 1", hitCondition: "2" },
        { name: "pkg.Type.blank", condition: "", hitCondition: "" },
      ],
      dataBreakpoints: [
        { dataId: "field-1", accessType: "write" },
        { dataId: "field-2", accessType: "readWrite", condition: "v == 3", hitCondition: "4", enabled: true },
        { dataId: "field-3", enabled: false },
        { dataId: "field-4" },
      ],
    });
    expect(bridge.invoke).toHaveBeenLastCalledWith("plugin_host_debugger_start", {
      payload: {
        prepareId: "p1",
        fingerprint,
        breakpoints: {},
        exceptionFilters: [],
        functionBreakpoints: [{ name: "pkg.Type.run" }, { name: "pkg.Type.stop", condition: "x > 1", hitCondition: "2" }, { name: "pkg.Type.blank" }],
        dataBreakpoints: [
          { dataId: "field-1", accessType: "write" },
          { dataId: "field-2", accessType: "readWrite", condition: "v == 3", hitCondition: "4" },
          { dataId: "field-4" },
        ],
      },
    });
    // Nothing enabled: the lists are left out, like before.
    await startDebugger({ prepareId: "p1", fingerprint, breakpoints: {}, exceptionFilters: [], functionBreakpoints: [{ name: "a", enabled: false }], dataBreakpoints: [] });
    expect(bridge.invoke).toHaveBeenLastCalledWith("plugin_host_debugger_start", { payload: { prepareId: "p1", fingerprint, breakpoints: {}, exceptionFilters: [] } });
  });

  it("parseBreakpointListEvent reads the function and data breakpoint results of a start", () => {
    const event = (body: unknown): DebugEvent => ({ sessionId: "s1", event: DEBUG_BREAKPOINTS_SET_EVENT, body });
    expect(
      parseBreakpointListEvent(
        event({ path: "", functionBreakpoints: [{ verified: true, id: 4 }, { verified: false, message: "class not loaded", id: 5 }, "junk"] }),
        3,
      ),
    ).toEqual({
      kind: "functionBreakpoints",
      results: [{ verified: true, id: 4 }, { verified: false, id: 5, message: "class not loaded" }, { verified: false }],
    });
    expect(parseBreakpointListEvent(event({ path: "", dataBreakpoints: [{ verified: true, message: "ignored when verified" }] }), 1)).toEqual({
      kind: "dataBreakpoints",
      results: [{ verified: true }],
    });
    // A failed request: every sent breakpoint is unverified with the adapter's message.
    expect(parseBreakpointListEvent(event({ path: "", functionBreakpoints: null, error: "bad name" }), 2)).toEqual({
      kind: "functionBreakpoints",
      error: "bad name",
      results: [
        { verified: false, message: "bad name" },
        { verified: false, message: "bad name" },
      ],
    });
    // A short answer: the breakpoints it leaves out are unverified.
    expect(parseBreakpointListEvent(event({ path: "", functionBreakpoints: [{ verified: true, id: 1 }] }), 3)).toEqual({
      kind: "functionBreakpoints",
      results: [{ verified: true, id: 1 }, { verified: false }, { verified: false }],
    });
    // Not one of these lists.
    expect(parseBreakpointListEvent(event({ path: "/w/a.py", breakpoints: [] }), 1)).toBeUndefined();
    expect(parseBreakpointListEvent(event({ path: "", exceptionFilters: null }), 1)).toBeUndefined();
    expect(parseBreakpointListEvent({ sessionId: "s1", event: "breakpoint", body: { functionBreakpoints: [] } }, 1)).toBeUndefined();
    expect(parseBreakpointListEvent(event("nope"), 1)).toBeUndefined();
  });

  it("startDebugger sends enabled address breakpoints apart from path breakpoints", async () => {
    const address = "x-lib://pkg/Type.class?=a";
    bridge.invoke.mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] });
    await startDebugger({
      prepareId: "p1",
      fingerprint,
      breakpoints: { "/work/app/main.py": [{ line: 1 }] },
      addressBreakpoints: {
        [address]: [{ line: 4, condition: "n > 1", enabled: true }, { line: 6, enabled: false }, { line: 8, logMessage: "v={v}" }],
        "x-lib://off": [{ line: 1, enabled: false }],
      },
      exceptionFilters: [],
    });
    expect(bridge.invoke).toHaveBeenLastCalledWith("plugin_host_debugger_start", {
      payload: {
        prepareId: "p1",
        fingerprint,
        breakpoints: { "/work/app/main.py": [{ line: 1 }] },
        addressBreakpoints: { [address]: [{ line: 4, condition: "n > 1" }, { line: 8, logMessage: "v={v}" }] },
        exceptionFilters: [],
      },
    });
    // Nothing enabled: the key is left out.
    await startDebugger({ prepareId: "p1", fingerprint, breakpoints: {}, addressBreakpoints: { "x-lib://off": [{ line: 1, enabled: false }] }, exceptionFilters: [] });
    expect(bridge.invoke).toHaveBeenLastCalledWith("plugin_host_debugger_start", { payload: { prepareId: "p1", fingerprint, breakpoints: {}, exceptionFilters: [] } });
  });

  it("setSessionAddressBreakpoints sends the address, never a path", async () => {
    bridge.invoke.mockResolvedValue({ breakpoints: [{ verified: false, line: 4 }] });
    const result = await setSessionAddressBreakpoints("s1", "x-lib://pkg/Type.class", [
      { line: 4, hitCondition: "2", enabled: true },
      { line: 9, enabled: false },
    ]);
    expect(result).toEqual({ breakpoints: [{ verified: false, line: 4 }] });
    expect(bridge.invoke).toHaveBeenLastCalledWith("debug_set_breakpoints", {
      sessionId: "s1",
      address: "x-lib://pkg/Type.class",
      breakpoints: [{ line: 4, hitCondition: "2" }],
    });
    await setSessionAddressBreakpoints("s1", "x-lib://pkg/Type.class", []);
    expect(bridge.invoke).toHaveBeenLastCalledWith("debug_set_breakpoints", { sessionId: "s1", address: "x-lib://pkg/Type.class", breakpoints: [] });
  });

  it("parseBreakpointsSetEvent tells address results from path results", () => {
    const event = (body: unknown): DebugEvent => ({ sessionId: "s1", event: DEBUG_BREAKPOINTS_SET_EVENT, body });
    expect(parseBreakpointsSetEvent(event({ path: "/w/a.py", breakpoints: [{ verified: true }] }))).toEqual({
      path: "/w/a.py",
      address: false,
      breakpoints: [{ verified: true }],
    });
    expect(parseBreakpointsSetEvent(event({ path: "x-lib://A", address: true, breakpoints: [{ verified: false, message: "m" }] }))).toEqual({
      path: "x-lib://A",
      address: true,
      breakpoints: [{ verified: false, message: "m" }],
    });
    expect(parseBreakpointsSetEvent(event({ path: "x-lib://A", address: true, breakpoints: null, error: "no class" }))).toEqual({
      path: "x-lib://A",
      address: true,
      breakpoints: null,
      error: "no class",
    });
    // Only a literal true marks an address.
    expect(parseBreakpointsSetEvent(event({ path: "/w/a.py", address: "true", breakpoints: [] }))?.address).toBe(false);
    // Path-less lists, other events and malformed bodies are not source results.
    expect(parseBreakpointsSetEvent(event({ path: "", functionBreakpoints: [] }))).toBeUndefined();
    expect(parseBreakpointsSetEvent(event({ path: "", exceptionFilters: null }))).toBeUndefined();
    expect(parseBreakpointsSetEvent(event({ path: 3, breakpoints: [] }))).toBeUndefined();
    expect(parseBreakpointsSetEvent(event({ path: "/w/a.py" }))).toBeUndefined();
    expect(parseBreakpointsSetEvent({ sessionId: "s1", event: "breakpoint", body: { path: "/w/a.py", breakpoints: [] } })).toBeUndefined();
    expect(parseBreakpointsSetEvent(event("nope"))).toBeUndefined();
    // An address result is not a path-less list either.
    expect(parseBreakpointListEvent(event({ path: "x-lib://A", address: true, breakpoints: [] }), 1)).toBeUndefined();
  });

  it("preserves logpoints in startup and live requests without forwarding disabled or blank messages", async () => {
    const message = "  value={value} <tag>  ";
    const breakpoints = [
      { line: 3, condition: "value > 0", hitCondition: "2", logMessage: message, enabled: true },
      { line: 7, logMessage: "disabled", enabled: false },
      { line: 9, logMessage: " \t ", enabled: true },
    ];
    bridge.invoke.mockResolvedValue({ sessionId: "s1", capabilities: { supportsLogPoints: true }, skippedBreakpoints: [] });
    const started = await startDebugger({ prepareId: "p1", fingerprint, breakpoints: { "/work/app/main.py": breakpoints }, exceptionFilters: [] });
    expect(started.capabilities).toMatchObject({ supportsLogPoints: true });
    const expected = [{ line: 3, condition: "value > 0", hitCondition: "2", logMessage: message }, { line: 9 }];
    expect(bridge.invoke).toHaveBeenLastCalledWith("plugin_host_debugger_start", {
      payload: { prepareId: "p1", fingerprint, breakpoints: { "/work/app/main.py": expected }, exceptionFilters: [] },
    });
    bridge.invoke.mockResolvedValue({ breakpoints: [] });
    await setSessionBreakpoints("s1", "/work/app/main.py", breakpoints);
    expect(bridge.invoke).toHaveBeenLastCalledWith("debug_set_breakpoints", { sessionId: "s1", path: "/work/app/main.py", breakpoints: expected });
  });

  it("setSessionBreakpoints sends only enabled breakpoints and may clear a file", async () => {
    bridge.invoke.mockResolvedValue({ breakpoints: [] });
    await setSessionBreakpoints("s1", "/work/app/main.py", [
      { line: 2, condition: "a", enabled: true },
      { line: 4, enabled: false },
    ]);
    expect(bridge.invoke).toHaveBeenLastCalledWith("debug_set_breakpoints", {
      sessionId: "s1",
      path: "/work/app/main.py",
      breakpoints: [{ line: 2, condition: "a" }],
    });
    await setSessionBreakpoints("s1", "/work/app/main.py", []);
    expect(bridge.invoke).toHaveBeenLastCalledWith("debug_set_breakpoints", {
      sessionId: "s1",
      path: "/work/app/main.py",
      breakpoints: [],
    });
  });

  it("debug session commands use the backend argument names", async () => {
    bridge.invoke.mockResolvedValue({ threads: [] });
    await debugRequest("s1", "threads");
    expect(bridge.invoke).toHaveBeenLastCalledWith("debug_request", { sessionId: "s1", command: "threads", arguments: undefined, timeoutMs: 0 });
    await debugRequest("s1", "evaluate", { expression: "x" }, 5000);
    expect(bridge.invoke).toHaveBeenLastCalledWith("debug_request", { sessionId: "s1", command: "evaluate", arguments: { expression: "x" }, timeoutMs: 5000 });

    bridge.invoke.mockResolvedValue(null);
    await stopDebugSession("s1");
    expect(bridge.invoke).toHaveBeenLastCalledWith("debug_stop", { sessionId: "s1" });
    await stopDebugSession("s1", false);
    expect(bridge.invoke).toHaveBeenLastCalledWith("debug_stop", { sessionId: "s1", terminateDebuggee: false });

    bridge.invoke.mockResolvedValue({ ok: true, output: "hi", offset: 2, ended: false });
    await expect(attachTerminal("debug-term-1")).resolves.toEqual({ ok: true, output: "hi", offset: 2, ended: false });
    expect(bridge.invoke).toHaveBeenLastCalledWith("attach_local_shell", { payload: { sessionId: "debug-term-1" } });

    bridge.invoke.mockResolvedValue({ ready: true, missing: [] });
    await checkDebugger({ pluginId: "python-debug", type: "python", projectRoot: "/work/app" });
    expect(bridge.invoke).toHaveBeenLastCalledWith("plugin_host_debugger_check", {
      payload: { pluginId: "python-debug", type: "python", projectRoot: "/work/app" },
    });
  });

  it("applyDebuggerSettings sends the list (also when empty) and accepts only {}", async () => {
    bridge.invoke.mockResolvedValue({});
    const request = { pluginId: "example-debug", type: "example", projectRoot: "/work/app", exceptionTypes: ["pkg.Failure"] };
    await expect(applyDebuggerSettings(request)).resolves.toBeUndefined();
    expect(bridge.invoke).toHaveBeenLastCalledWith("plugin_host_debugger_apply_settings", { payload: request });
    await applyDebuggerSettings({ ...request, exceptionTypes: [] });
    expect(bridge.invoke).toHaveBeenLastCalledWith("plugin_host_debugger_apply_settings", { payload: { ...request, exceptionTypes: [] } });
    for (const bad of [null, undefined, [], "ok", { applied: true }]) {
      bridge.invoke.mockResolvedValue(bad);
      await expect(applyDebuggerSettings(request)).rejects.toThrow(/^unavailable: plugin_host_debugger_apply_settings returned an invalid settings result/);
    }
    // The host returns settings errors unwrapped, starting with their code
    // (debuggers_launch.go applyDebuggerSettings, TestApplyDebuggerSettings).
    bridge.invoke.mockRejectedValue(new Error("timeout: plugin \"example-debug\" did not answer in time"));
    await expect(applyDebuggerSettings(request)).rejects.toThrow(/^timeout:/);
  });

  it("listDebuggers keeps the declared exception type list and settings support", async () => {
    const exceptionTypes = { placeholder: "pkg.Failure", descriptionLocalized: { zh: "只在这些类型处暂停" } };
    bridge.invoke.mockResolvedValue([
      { pluginId: "p", type: "t", title: "T", languages: ["x"], configurationSnippets: [], hasEntryPoints: false, hasCheck: false, hasSettings: true, exceptionTypes },
    ]);
    const [debuggerInfo] = await listDebuggers();
    expect(debuggerInfo.hasSettings).toBe(true);
    expect(debuggerInfo.exceptionTypes).toEqual(exceptionTypes);
    expect(debuggerInfo.hotCodeReplace).toBeUndefined();
    expect(debuggerInfo.functionBreakpoints).toBeUndefined();
  });

  it("listDebuggers keeps hotCodeReplace and functionBreakpoints texts, dropping a malformed hotCodeReplace", async () => {
    const functionBreakpoints = { placeholder: "pkg.Type.method", descriptionLocalized: { zh: "方法被调用时暂停" } };
    const base = { pluginId: "p", title: "T", languages: ["x"], configurationSnippets: [], hasEntryPoints: false, hasCheck: false, hasSettings: false };
    bridge.invoke.mockResolvedValue([
      { ...base, type: "a", hotCodeReplace: { event: "codeReloaded", request: "applyCode" }, functionBreakpoints },
      { ...base, type: "b", hotCodeReplace: { event: "codeReloaded" } },
      { ...base, type: "c", hotCodeReplace: "applyCode" },
      { ...base, type: "d", hotCodeReplace: { event: "codeReloaded", request: "applyCode", modeSetting: "hot.mode" } },
      { ...base, type: "e", hotCodeReplace: { event: "codeReloaded", request: "applyCode", modeSetting: 3 } },
    ]);
    const [a, b, c, d, e] = await listDebuggers();
    expect(a.hotCodeReplace).toEqual({ event: "codeReloaded", request: "applyCode" });
    expect(d.hotCodeReplace).toEqual({ event: "codeReloaded", request: "applyCode", modeSetting: "hot.mode" });
    expect(e.hotCodeReplace).toEqual({ event: "codeReloaded", request: "applyCode" });
    expect(a.functionBreakpoints).toEqual(functionBreakpoints);
    expect(b.hotCodeReplace).toBeUndefined();
    expect(c.hotCodeReplace).toBeUndefined();
    expect(c.type).toBe("c");
  });

  it("throws unavailable outside desktop", async () => {
    bridge.native = false;
    await expect(listDebuggers()).rejects.toThrow(/^unavailable:/);
    await expect(applyDebuggerSettings({ pluginId: "p", type: "t", projectRoot: "/w", exceptionTypes: [] })).rejects.toThrow(/^unavailable:/);
    await expect(debugRequest("s1", "threads")).rejects.toThrow(/^unavailable:/);
    expect(() => onDebugEvent(() => undefined)).toThrow(/^unavailable:/);
    expect(bridge.invoke).not.toHaveBeenCalled();
    expect(bridge.listen).not.toHaveBeenCalled();
  });

  it("onDebugEvent unsubscribes", () => {
    const off = vi.fn();
    let deliver: ((payload: unknown) => void) | undefined;
    bridge.listen.mockImplementation((name: string, handler: (payload: unknown) => void) => {
      expect(name).toBe("debug:event");
      deliver = handler;
      return off;
    });
    const received: DebugEvent[] = [];
    const unsubscribe = onDebugEvent((event) => received.push(event));
    deliver?.({ sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 1 } });
    deliver?.("not an event");
    deliver?.({ sessionId: 3, event: "stopped" });
    expect(received).toEqual([{ sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 1 } }]);
    unsubscribe();
    expect(off).toHaveBeenCalledTimes(1);
  });
});
