// Hot code replace, driven by the debugger's manifest declaration
// (`debuggers[].hotCodeReplace {event, request}`; java-debug's names as test
// data). The event bodies and answers are the ones java-debug 0.53.1 sent in
// TestMachineSmokeJavaIdea2HotCodeReplace. Auto mode sends the declared
// request on BUILD_COMPLETE and reports 「已热替换 N 个类」, reading the frames
// and variables again while paused (with the drop-frame hint when the adapter
// can restart frames and the top frame runs in a replaced type and has a
// caller, i.e. is not the bottom frame of the whole stack). Manual mode
// enables 「应用代码更改」 with a pending notice instead. Warnings, errors and
// refused replaces show the adapter's text and the session goes on. A
// debugger that declares nothing gets no button, and a same-named event is
// ignored.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h, watch } from "vue";
import type { DapCapabilities, DapStackFrame, DebugEvent, DebuggerHotCodeReplace } from "@/domain/debug/types";
import { newSessionView } from "@/domain/debug/session-model";

const debug = vi.hoisted(() => ({
  request: vi.fn(),
  handler: undefined as ((event: DebugEvent) => void) | undefined,
  mode: "auto" as unknown,
  configuration: vi.fn(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  canInvoke: () => true,
  invokeBackend: vi.fn(),
  listenAppEvent: vi.fn(() => () => undefined),
  copyText: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/debug-service", () => ({
  listDebuggers: vi.fn().mockResolvedValue([]),
  debuggerEntryPoints: vi.fn().mockResolvedValue([]),
  debugRequest: debug.request,
  setSessionBreakpoints: vi.fn().mockResolvedValue({ breakpoints: [] }),
  stopDebugSession: vi.fn(),
  attachTerminal: vi.fn(),
  onDebugEvent: (handler: (event: DebugEvent) => void) => {
    debug.handler = handler;
    return () => undefined;
  },
}));
vi.mock("@/services/plugin-configuration-service", () => ({
  getPluginConfiguration: debug.configuration,
}));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
  registerDesktopBrowserSnapshotKeys: vi.fn(),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("@/components/terminal/LocalTerminal.vue", () => ({
  __esModule: true,
  default: defineComponent({ name: "LocalTerminal", setup: () => () => h("div") }),
}));

import DebugToolWindow from "@/components/debug/DebugToolWindow.vue";
import {
  frameInChangedTypes,
  HOT_CODE_REPLACE_MESSAGE_MAX,
  hotCodeReplaceMode,
  parseHotCodeReplaceEvent,
  parseHotCodeReplaceResult,
  shortHotCodeReplaceMessage,
} from "@/domain/debug/hot-code-replace";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/p";
const FILE = "/p/src/HotApp.java";
const HOT: DebuggerHotCodeReplace = { event: "hotcodereplace", request: "redefineClasses", modeSetting: "hotCodeReplace.mode" };
const FRAMES: DapStackFrame[] = [{ id: 1, name: "HotApp.main(String[])", line: 5, column: 1, source: { path: FILE } }];
/** Paused in HotApp.value(), called from main. */
const NESTED_FRAMES: DapStackFrame[] = [
  { id: 2, name: "HotApp.value()", line: 9, column: 1, source: { path: FILE } },
  { id: 1, name: "HotApp.main(String[])", line: 5, column: 1, source: { path: FILE } },
];
// java-debug's bodies, as measured.
const BUILD_COMPLETE = { changeType: "BUILD_COMPLETE", message: "Build completed.", type: "hotcodereplace" };
const STARTING = { changeType: "STARTING", message: "Start hot code replacement procedure...", type: "hotcodereplace" };
const ERROR = { changeType: "ERROR", message: "Add method not implemented", type: "hotcodereplace" };
const END = { changeType: "END", message: "Completed hot code replace", type: "hotcodereplace" };
const REPLACED = { changedClasses: ["HotApp"] };
const REFUSED = { changedClasses: [], errorMessage: "Failed to redefine classes: Add method not implemented" };

let wrapper: VueWrapper | undefined;
/** What the declared request answers; a function runs per request. */
let redefine: () => Promise<unknown>;
/** The frames `stackTrace` answers. */
let stack: DapStackFrame[];

beforeEach(() => {
  redefine = async () => REPLACED;
  stack = FRAMES;
  debug.mode = "auto";
  debug.configuration.mockReset();
  debug.configuration.mockImplementation(async (id: string) => ({ pluginId: id, effective: { "hotCodeReplace.mode": debug.mode } }));
  debug.request.mockReset();
  debug.request.mockImplementation(async (_id: string, command: string) => {
    if (command === "redefineClasses") return redefine();
    if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
    if (command === "stackTrace") return { stackFrames: stack };
    return {};
  });
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  document.body.innerHTML = "";
});

interface SessionOptions {
  capabilities?: DapCapabilities;
  state?: "paused" | "running";
  hotCodeReplace?: DebuggerHotCodeReplace | null;
  noDebug?: boolean;
}

function addSession({ capabilities = {}, state = "paused", hotCodeReplace = HOT, noDebug = false }: SessionOptions = {}) {
  const store = useDebugSessionsStore();
  store.sessions.push({
    ...newSessionView({
      id: "s1",
      projectRoot: ROOT,
      configId: "c1",
      configName: "HotApp",
      type: "java",
      noDebug,
      capabilities,
      startedAt: 1,
      pluginId: "java-debug",
      ...(hotCodeReplace ? { hotCodeReplace } : {}),
    }),
    state,
    threads: [{ id: 1, name: "main" }],
    selectedThreadId: 1,
    stoppedThreadId: state === "paused" ? 1 : undefined,
    frames: state === "paused" ? FRAMES.map((frame) => ({ ...frame })) : [],
    selectedFrameId: state === "paused" ? 1 : undefined,
  });
  store.selectSession("s1");
  return store;
}

async function mountWindow() {
  wrapper = mount(DebugToolWindow, { props: { sessionId: "s1" }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

const settle = async () => {
  await flushPromises();
  await new Promise((resolve) => setTimeout(resolve, 20));
  await flushPromises();
};

function emit(body: unknown, event = "hotcodereplace") {
  debug.handler?.({ sessionId: "s1", event, body });
}

function requests(command: string) {
  return debug.request.mock.calls.filter((call) => call[1] === command);
}

function button() {
  return wrapper!.find('[data-action="apply-code-changes"]');
}

function notices() {
  return wrapper!.findAll(".debug-window__hot-code").map((alert) => alert.text());
}

describe("hot code replace", () => {
  it("applies a finished build at once in auto mode and reads the paused frames again", async () => {
    const store = addSession();
    await mountWindow();
    const version = store.variablesVersion("s1");
    emit(BUILD_COMPLETE);
    await settle();

    expect(debug.configuration).toHaveBeenCalledWith("java-debug");
    expect(requests("redefineClasses")).toEqual([["s1", "redefineClasses"]]);
    expect(notices()).toHaveLength(1);
    expect(notices()[0]).toMatch(/^(已热替换 1 个类|Hot-swapped 1 class)$/);
    expect(store.sessions[0]!.hotCodeReplaceMessage?.kind).toBe("info");
    expect(requests("stackTrace")).toHaveLength(1);
    expect(store.variablesVersion("s1")).toBe(version + 1);
    // Progress events change nothing.
    emit(STARTING);
    emit(END);
    await settle();
    expect(requests("redefineClasses")).toHaveLength(1);
    expect(store.sessions[0]!.state).toBe("paused");
    expect(button().attributes("disabled")).toBeDefined();
  });

  it("keeps the selected frame through the frame reload without passing by the top frame", async () => {
    stack = NESTED_FRAMES;
    const store = addSession();
    const selections: (number | undefined)[] = [];
    const stopWatching = watch(() => store.sessions[0]?.selectedFrameId, (id) => selections.push(id), { flush: "sync" });
    // Paused in main (frame 1), which is the caller in the new stack.
    emit(BUILD_COMPLETE);
    await settle();
    stopWatching();
    expect(requests("stackTrace")).toHaveLength(1);
    expect(store.sessions[0]!.selectedFrameId).toBe(1);
    expect(selections).not.toContain(2);
  });

  it("selects the top frame after the reload when the selected one is gone", async () => {
    stack = [{ id: 30, name: "HotApp.main(String[])", line: 5, column: 1, source: { path: FILE } }];
    const store = addSession();
    emit(BUILD_COMPLETE);
    await settle();
    expect(store.sessions[0]!.selectedFrameId).toBe(30);
  });

  it("does not read frames of a running program", async () => {
    addSession({ state: "running" });
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();

    expect(requests("redefineClasses")).toHaveLength(1);
    expect(requests("stackTrace")).toEqual([]);
    expect(notices()[0]).toMatch(/^(已热替换 1 个类|Hot-swapped 1 class)$/);
  });

  it("hints at dropping the frame when the top frame runs in a replaced type and frames can be restarted", async () => {
    stack = NESTED_FRAMES;
    addSession({ capabilities: { supportsRestartFrame: true } });
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(notices()[0]).toMatch(/已热替换 1 个类。可丢弃帧重新执行|Hot-swapped 1 class\. Drop the frame to run the new code/);

    // A replaced type the top frame does not run in: no hint.
    redefine = async () => ({ changedClasses: ["com.example.Other"] });
    emit(BUILD_COMPLETE);
    await settle();
    expect(notices()[0]).toMatch(/^(已热替换 1 个类|Hot-swapped 1 class)$/);
  });

  // The HotApp checklist case: paused in main, whose frame is the bottom of the stack.
  it("gives no drop-frame hint when the top frame is the bottom frame of the whole stack", async () => {
    const store = addSession({ capabilities: { supportsRestartFrame: true } });
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(requests("stackTrace")).toHaveLength(1);
    expect(store.sessions[0]!.frames.map((frame) => frame.id)).toEqual([1]);
    expect(notices()[0]).toMatch(/^(已热替换 1 个类|Hot-swapped 1 class)$/);
  });

  it("clears the outcome when a frame is dropped", async () => {
    stack = NESTED_FRAMES;
    const store = addSession({ capabilities: { supportsRestartFrame: true } });
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(notices()).toHaveLength(1);
    await store.restartFrame("s1", 2);
    await settle();
    expect(requests("restartFrame")).toEqual([["s1", "restartFrame", { frameId: 2 }]]);
    expect(notices()).toEqual([]);
  });

  it("gives no drop-frame hint without supportsRestartFrame", async () => {
    addSession();
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(notices()[0]).not.toMatch(/丢弃帧|Drop the frame/);
  });

  it("gives no drop-frame hint when the adapter moved the program meanwhile", async () => {
    // java-debug drops a changed frame itself: continued, then stopped (reason step).
    redefine = async () => {
      emit({ threadId: 1, allThreadsContinued: false }, "continued");
      return REPLACED;
    };
    const store = addSession({ capabilities: { supportsRestartFrame: true } });
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(notices()[0]).toMatch(/^(已热替换 1 个类|Hot-swapped 1 class)$/);
    expect(store.sessions[0]!.state).toBe("running");
    expect(requests("stackTrace")).toEqual([]);
  });

  it("waits for Apply Code Changes in manual mode", async () => {
    debug.mode = "manual";
    const store = addSession();
    await mountWindow();
    expect(button().exists()).toBe(true);
    expect(button().attributes("disabled")).toBeDefined();
    expect(button().attributes("aria-label")).toMatch(/应用代码更改|Apply Code Changes/);

    emit(BUILD_COMPLETE);
    await settle();
    expect(requests("redefineClasses")).toEqual([]);
    expect(store.sessions[0]!.pendingCodeChanges).toBe(true);
    expect(button().attributes("disabled")).toBeUndefined();
    expect(notices()[0]).toMatch(/点击「应用代码更改」|Click Apply Code Changes/);

    await button().trigger("click");
    await settle();
    expect(requests("redefineClasses")).toEqual([["s1", "redefineClasses"]]);
    expect(store.sessions[0]!.pendingCodeChanges).toBe(false);
    expect(notices()).toHaveLength(1);
    expect(notices()[0]).toMatch(/^(已热替换 1 个类|Hot-swapped 1 class)$/);
    expect(button().attributes("disabled")).toBeDefined();
  });

  it("ignores builds in never mode", async () => {
    debug.mode = "never";
    const store = addSession();
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(requests("redefineClasses")).toEqual([]);
    expect(store.sessions[0]!.pendingCodeChanges).toBe(false);
    expect(notices()).toEqual([]);
  });

  it("applies in auto mode when the setting cannot be read", async () => {
    debug.configuration.mockRejectedValue(new Error("settings unavailable"));
    addSession();
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(requests("redefineClasses")).toHaveLength(1);
  });

  // Review Focus 1: an unsupported change shows the adapter's text and the session goes on.
  it("shows the adapter's reason for a refused replace and keeps the session", async () => {
    redefine = async () => {
      emit(STARTING);
      emit(ERROR);
      emit(END);
      return REFUSED;
    };
    const store = addSession();
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();

    const view = store.sessions[0]!;
    expect(view.hotCodeReplaceMessage).toEqual({ kind: "error", text: "Failed to redefine classes: Add method not implemented" });
    expect(notices()).toEqual(["Failed to redefine classes: Add method not implemented"]);
    expect(wrapper!.find(".debug-window__hot-code.is-error .el-alert--error").exists()).toBe(true);
    expect(view.state).toBe("paused");
    expect(view.lastError).toBeUndefined();
    expect(requests("stackTrace")).toEqual([]);

    // The bar can be dismissed.
    await wrapper!.find(".debug-window__hot-code .el-alert__close-btn").trigger("click");
    await settle();
    expect(notices()).toEqual([]);
  });

  it("shows an adapter warning or error event as it is", async () => {
    const store = addSession({ state: "running" });
    await mountWindow();
    emit({ changeType: "WARNING", message: "Some frames could not be dropped" });
    await settle();
    expect(store.sessions[0]!.hotCodeReplaceMessage).toEqual({ kind: "warning", text: "Some frames could not be dropped" });
    expect(wrapper!.find(".debug-window__hot-code.is-warning .el-alert--warning").exists()).toBe(true);
    emit(ERROR);
    await settle();
    expect(notices()).toEqual(["Add method not implemented"]);
    expect(store.sessions[0]!.state).toBe("running");
    expect(requests("redefineClasses")).toEqual([]);
  });

  it("keeps the changes pending when the request fails", async () => {
    redefine = async () => {
      throw new Error("unavailable: adapter closed");
    };
    const store = addSession();
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(store.sessions[0]!.pendingCodeChanges).toBe(true);
    expect(notices().join("\n")).toMatch(/应用代码更改失败：unavailable: adapter closed|Could not apply the code changes: unavailable: adapter closed/);
    expect(button().attributes("disabled")).toBeUndefined();
  });

  it("shows the adapter's own text when the request fails", async () => {
    redefine = async () => {
      throw new Error("dap redefineClasses failed: Failed to complete hot code replace: VM disconnected");
    };
    addSession();
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    // The failure (and the still pending changes) show; the host's request prefix does not.
    expect(notices().some((text) => /^(应用代码更改失败：|Could not apply the code changes: )Failed to complete hot code replace: VM disconnected$/.test(text))).toBe(true);
    expect(notices().join("\n")).not.toContain("dap redefineClasses failed");
  });

  it("reports when no loaded type needed replacing", async () => {
    redefine = async () => ({ changedClasses: [] });
    addSession();
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(notices()[0]).toMatch(/没有已加载的类需要替换|No loaded class needed replacing/);
  });

  it("sends once more for a build that came while a request was in flight", async () => {
    let answer: () => void = () => undefined;
    redefine = () => new Promise((resolve) => (answer = () => resolve(REPLACED)));
    addSession({ state: "running" });
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    emit(BUILD_COMPLETE);
    await settle();
    expect(requests("redefineClasses")).toHaveLength(1);
    redefine = async () => REPLACED;
    answer();
    await settle();
    expect(requests("redefineClasses")).toHaveLength(2);
  });

  it("uses auto without reading settings when the debugger declares no modeSetting", async () => {
    debug.mode = "manual";
    addSession({ hotCodeReplace: { event: "hotcodereplace", request: "redefineClasses" } });
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(debug.configuration).not.toHaveBeenCalled();
    expect(requests("redefineClasses")).toHaveLength(1);
  });

  it("reads the declared setting, whatever its key", async () => {
    debug.configuration.mockImplementation(async (id: string) => ({ pluginId: id, effective: { "hotCodeReplace.mode": "auto", "debug.hcr": "manual" } }));
    const store = addSession({ hotCodeReplace: { ...HOT, modeSetting: "debug.hcr" } });
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(requests("redefineClasses")).toEqual([]);
    expect(store.sessions[0]!.pendingCodeChanges).toBe(true);
  });

  it("says the singular and the plural", async () => {
    redefine = async () => ({ changedClasses: ["HotApp", "HotApp$Inner"] });
    addSession({ state: "running" });
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(notices()[0]).toMatch(/^(已热替换 2 个类|Hot-swapped 2 classes)$/);
  });

  it("reports a partial replace with the adapter's reason and still reads the frames again", async () => {
    redefine = async () => ({ changedClasses: ["HotApp"], errorMessage: "Other$Type: Delete method not implemented" });
    const store = addSession();
    await mountWindow();
    const version = store.variablesVersion("s1");
    emit(BUILD_COMPLETE);
    await settle();
    expect(store.sessions[0]!.hotCodeReplaceMessage?.kind).toBe("error");
    expect(notices()[0]).toMatch(
      /^(部分更改未能应用（已热替换 1 个类）：|Some changes could not be applied \(1 hot-swapped\): )Other\$Type: Delete method not implemented$/,
    );
    expect(requests("stackTrace")).toHaveLength(1);
    expect(store.variablesVersion("s1")).toBe(version + 1);
  });

  it("clears the outcome when the program runs on, and keeps errors", async () => {
    const store = addSession();
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(notices()).toHaveLength(1);
    await store.resume("s1");
    await settle();
    expect(notices()).toEqual([]);

    debug.handler?.({ sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 1 } });
    await settle();
    emit(ERROR);
    await settle();
    await store.stepOver("s1");
    await settle();
    expect(notices()).toEqual(["Add method not implemented"]);
  });

  it("keeps the selected frame after reading the frames again", async () => {
    const frames: DapStackFrame[] = [
      { id: 1, name: "HotApp.value()", line: 10, column: 1, source: { path: FILE } },
      { id: 2, name: "HotApp.main(String[])", line: 4, column: 1, source: { path: FILE } },
    ];
    debug.request.mockImplementation(async (_id: string, command: string) => {
      if (command === "redefineClasses") return REPLACED;
      if (command === "stackTrace") return { stackFrames: frames };
      return {};
    });
    const store = addSession();
    store.sessions[0]!.frames = frames.map((frame) => ({ ...frame }));
    store.selectFrame("s1", 2);
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(requests("stackTrace")).toHaveLength(1);
    expect(store.sessions[0]!.selectedFrameId).toBe(2);

    // A frame the adapter no longer lists: the top one.
    frames.splice(1, 1);
    emit(BUILD_COMPLETE);
    await settle();
    expect(store.sessions[0]!.selectedFrameId).toBe(1);
  });

  it("cuts a long adapter message and keeps the full text in the tooltip", async () => {
    const long = "x".repeat(HOT_CODE_REPLACE_MESSAGE_MAX + 10);
    addSession({ state: "running" });
    await mountWindow();
    emit({ changeType: "ERROR", message: long });
    await settle();
    const text = wrapper!.find(".debug-window__hot-code-text");
    expect(text.text()).toBe(`${"x".repeat(HOT_CODE_REPLACE_MESSAGE_MAX)}…`);
    expect(text.attributes("title")).toBe(long);
  });

  it("shows no button and ignores the event for a debugger that declares nothing", async () => {
    const store = addSession({ hotCodeReplace: null });
    await mountWindow();
    expect(button().exists()).toBe(false);
    emit(BUILD_COMPLETE);
    await settle();
    expect(requests("redefineClasses")).toEqual([]);
    expect(debug.configuration).not.toHaveBeenCalled();
    expect(store.sessions[0]!.pendingCodeChanges).toBe(false);
    await expect(store.applyCodeChanges("s1")).resolves.toEqual({ changed: [] });
    expect(requests("redefineClasses")).toEqual([]);
  });

  it("ignores other events of a declaring debugger and runs without debugging", async () => {
    addSession();
    await mountWindow();
    emit(BUILD_COMPLETE, "output");
    await settle();
    expect(requests("redefineClasses")).toEqual([]);
    wrapper!.unmount();
    useDebugSessionsStore().sessions.splice(0);
    addSession({ noDebug: true });
    await mountWindow();
    expect(button().exists()).toBe(false);
    emit(BUILD_COMPLETE);
    await settle();
    expect(requests("redefineClasses")).toEqual([]);
  });

  it("drops the pending changes when the session ends", async () => {
    debug.mode = "manual";
    const store = addSession();
    await mountWindow();
    emit(BUILD_COMPLETE);
    await settle();
    expect(store.sessions[0]!.pendingCodeChanges).toBe(true);
    debug.handler?.({ sessionId: "s1", event: "mincode.sessionEnded", body: { reason: "exited" } });
    await settle();
    expect(store.sessions[0]!.pendingCodeChanges).toBe(false);
    expect(button().attributes("disabled")).toBeDefined();
    await expect(store.applyCodeChanges("s1")).resolves.toEqual({ changed: [] });
    expect(requests("redefineClasses")).toEqual([]);
  });
});

describe("hot code replace protocol", () => {
  it("reads the event body", () => {
    expect(parseHotCodeReplaceEvent(BUILD_COMPLETE)).toEqual({ kind: "buildComplete", message: "Build completed." });
    expect(parseHotCodeReplaceEvent(ERROR)).toEqual({ kind: "error", message: "Add method not implemented" });
    expect(parseHotCodeReplaceEvent({ changeType: "WARNING", message: " " })).toEqual({ kind: "warning" });
    expect(parseHotCodeReplaceEvent(STARTING)?.kind).toBe("other");
    expect(parseHotCodeReplaceEvent({ message: "x" })).toBeUndefined();
    expect(parseHotCodeReplaceEvent(null)).toBeUndefined();
  });

  it("reads the answer", () => {
    expect(parseHotCodeReplaceResult(REPLACED)).toEqual({ changed: ["HotApp"] });
    expect(parseHotCodeReplaceResult(REFUSED)).toEqual({ changed: [], error: REFUSED.errorMessage });
    expect(parseHotCodeReplaceResult({ changedClasses: ["a.B", 3, ""] })).toEqual({ changed: ["a.B"] });
    expect(parseHotCodeReplaceResult(undefined)).toEqual({ changed: [] });
  });

  it("maps the setting to a mode, auto by default", () => {
    expect(["auto", "manual", "never", "AUTO", undefined, 1].map(hotCodeReplaceMode)).toEqual(["auto", "manual", "never", "auto", "auto", "auto"]);
  });

  it("cuts messages at the limit only", () => {
    expect(shortHotCodeReplaceMessage("short")).toBe("short");
    const exact = "界".repeat(HOT_CODE_REPLACE_MESSAGE_MAX);
    expect(shortHotCodeReplaceMessage(exact)).toBe(exact);
    expect(shortHotCodeReplaceMessage(`${exact}x`)).toBe(`${exact}…`);
  });

  it("matches a frame to the replaced types by qualified or last name", () => {
    expect(frameInChangedTypes({ name: "HotApp.main(String[])" }, ["HotApp"])).toBe(true);
    expect(frameInChangedTypes({ name: "Foo.bar(int)" }, ["com.example.Foo"])).toBe(true);
    expect(frameInChangedTypes({ name: "com.example.Foo.bar(int)" }, ["com.example.Foo"])).toBe(true);
    expect(frameInChangedTypes({ name: "Outer$Inner.run()" }, ["com.example.Outer$Inner"])).toBe(true);
    expect(frameInChangedTypes({ name: "FooBar.run()" }, ["com.example.Foo"])).toBe(false);
    expect(frameInChangedTypes({ name: "Outer.run()" }, ["com.example.Outer$Inner"])).toBe(false);
    expect(frameInChangedTypes({ name: "Foo.bar()" }, [])).toBe(false);
  });
});
