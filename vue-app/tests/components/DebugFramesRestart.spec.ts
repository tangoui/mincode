// While paused, a frame of the Frames pane can be dropped (DAP restartFrame)
// when the adapter declares supportsRestartFrame: its context menu has "Drop
// Frame", which sends `restartFrame { frameId }`; the adapter's stopped event
// then refreshes the frames. A frame with `canRestart: false`, and the bottom
// frame of a fully loaded stack (it has no caller), have the item disabled; without the capability, or while the program runs, there is no
// item and no request. A failure shows the adapter's message and leaves the
// session as it was.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import type { DapCapabilities, DapStackFrame, DebugEvent } from "@/domain/debug/types";
import { newSessionView } from "@/domain/debug/session-model";

const debug = vi.hoisted(() => ({
  request: vi.fn(),
  handler: undefined as ((event: DebugEvent) => void) | undefined,
  message: { error: vi.fn(), success: vi.fn(), warning: vi.fn(), info: vi.fn() },
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
  onDebugEvent: (handler: (event: DebugEvent) => void) => {
    debug.handler = handler;
    return () => undefined;
  },
}));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("element-plus", async (importOriginal) => ({ ...(await importOriginal<object>()), ElMessage: debug.message }));

import DebugFramesPane from "@/components/debug/DebugFramesPane.vue";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/p";
const FILE = "/p/Main.java";
const REJECTED = "Current stack frame doesn't support restart.";
/** How the host rejects a failure response of the adapter (backend/debug/session.go RequestError). */
const REJECTED_BY_HOST = `dap restartFrame failed: ${REJECTED}`;
const FRAMES: DapStackFrame[] = [
  { id: 1, name: "Main.inner()", line: 12, column: 1, source: { path: FILE } },
  { id: 2, name: "Main.outer()", line: 7, column: 1, source: { path: FILE } },
  { id: 3, name: "Main.main(String[])", line: 3, column: 1, source: { path: FILE }, canRestart: false },
];

let wrapper: VueWrapper | undefined;
let sessionSeq = 0;
let sessionId = "";
/** What `restartFrame` does; a function runs per request. */
let restartFrame: (args: { frameId?: number }) => Promise<unknown>;
/** What `stackTrace` answers after a stop. */
let stackTrace: () => unknown;

beforeEach(() => {
  sessionSeq += 1;
  sessionId = `s${sessionSeq}`;
  restartFrame = async () => ({});
  stackTrace = () => ({ stackFrames: FRAMES.slice(1) });
  debug.message.error.mockReset();
  debug.request.mockReset();
  debug.request.mockImplementation(async (_id: string, command: string, args: { frameId?: number }) => {
    if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
    if (command === "stackTrace") return stackTrace();
    if (command === "restartFrame") return restartFrame(args);
    return {};
  });
});

afterEach(() => {
  // Disarms the guard a keyboard-opened menu leaves for the native contextmenu that follows.
  document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
  wrapper?.unmount();
  wrapper = undefined;
  document.body.innerHTML = "";
});

function addSession(capabilities: DapCapabilities, state: "paused" | "running" = "paused") {
  const store = useDebugSessionsStore();
  store.sessions.push({
    ...newSessionView({ id: sessionId, projectRoot: ROOT, configId: "c1", configName: "App", type: "java", noDebug: false, capabilities, startedAt: 1 }),
    state,
    threads: [{ id: 1, name: "main" }],
    selectedThreadId: 1,
    stoppedThreadId: state === "paused" ? 1 : undefined,
    frames: state === "paused" ? FRAMES.map((frame) => ({ ...frame })) : [],
    selectedFrameId: state === "paused" ? 1 : undefined,
  });
  store.selectSession(sessionId);
  return store;
}

function session() {
  return useDebugSessionsStore().sessions.find((view) => view.id === sessionId)!;
}

async function mountPane() {
  wrapper = mount(
    defineComponent({ setup: () => () => h(DebugFramesPane, { session: session() }) }),
    { attachTo: document.body }
  );
  await flushPromises();
}

function frameButton(index: number) {
  return wrapper!.findAll(".debug-frames__item")[index]!;
}

function menuItem() {
  return document.body.querySelector<HTMLElement>(".debug-frames__menu .el-dropdown-menu__item");
}

function restartRequests() {
  return debug.request.mock.calls.filter((call) => call[1] === "restartFrame");
}

function emit(event: string, body: Record<string, unknown>) {
  debug.handler?.({ sessionId, event, body });
}

describe("DebugFramesPane drop frame", () => {
  it("drops the frame from its context menu", async () => {
    addSession({ supportsRestartFrame: true });
    await mountPane();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();

    const item = menuItem();
    expect(item?.textContent).toMatch(/丢弃到此帧|Drop Frame/);
    expect(item?.getAttribute("aria-disabled")).not.toBe("true");
    item!.click();
    await flushPromises();

    expect(restartRequests()).toEqual([[sessionId, "restartFrame", { frameId: 2 }]]);
    expect(menuItem()).toBeNull();
    expect(debug.message.error).not.toHaveBeenCalled();
  });

  it("refreshes the frames on the adapter's stopped event", async () => {
    addSession({ supportsRestartFrame: true });
    await mountPane();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    menuItem()!.click();
    await flushPromises();

    emit("continued", { threadId: 1 });
    emit("stopped", { reason: "restartframe", threadId: 1 });
    await flushPromises();

    expect(session().frames.map((frame) => frame.id)).toEqual([2, 3]);
    expect(wrapper!.findAll(".debug-frames__item")[0]!.text()).toContain("Main.outer()");
  });

  it("opens the menu from the keyboard", async () => {
    addSession({ supportsRestartFrame: true });
    await mountPane();
    await frameButton(0).trigger("keydown", { key: "F10", shiftKey: true });
    await flushPromises();

    menuItem()!.click();
    await flushPromises();
    expect(restartRequests()).toEqual([[sessionId, "restartFrame", { frameId: 1 }]]);
  });

  it("disables the item for a frame that cannot restart", async () => {
    addSession({ supportsRestartFrame: true });
    await mountPane();
    await frameButton(2).trigger("contextmenu");
    await flushPromises();

    const item = menuItem();
    expect(item?.getAttribute("aria-disabled")).toBe("true");
    item!.click();
    await flushPromises();
    expect(restartRequests()).toEqual([]);
  });

  // Generic DAP: the bottom frame of a stack has no caller to return to.
  it("disables the item for the bottom frame once the whole stack is loaded", async () => {
    const stack: DapStackFrame[] = [
      { id: 21, name: "Main.inner()", line: 12, column: 1, source: { path: FILE } },
      { id: 22, name: "Main.main(String[])", line: 3, column: 1, source: { path: FILE } },
    ];
    stackTrace = () => ({ stackFrames: stack });
    const store = addSession({ supportsRestartFrame: true });
    await mountPane();
    emit("stopped", { reason: "breakpoint", threadId: 1 });
    await flushPromises();
    expect(session().frames.map((frame) => frame.id)).toEqual([21, 22]);

    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()?.getAttribute("aria-disabled")).toBe("true");
    menuItem()!.click();
    await flushPromises();
    await store.restartFrame(sessionId, 22);
    expect(restartRequests()).toEqual([]);

    await frameButton(0).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()?.getAttribute("aria-disabled")).not.toBe("true");
  });

  it("keeps the item for the last shown frame while more of the stack may follow", async () => {
    const deep = (count: number): DapStackFrame[] =>
      Array.from({ length: count }, (_, index) => ({ id: 100 + index, name: `Main.level${index}()`, line: 1, column: 1, source: { path: FILE } }));
    // As many frames as requested and no totalFrames: the stack may go on.
    stackTrace = () => ({ stackFrames: deep(50) });
    addSession({ supportsRestartFrame: true });
    await mountPane();
    emit("stopped", { reason: "breakpoint", threadId: 1 });
    await flushPromises();
    await frameButton(49).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()?.getAttribute("aria-disabled")).not.toBe("true");
    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    await flushPromises();

    // totalFrames says these are all of them.
    stackTrace = () => ({ stackFrames: deep(50), totalFrames: 50 });
    emit("continued", { threadId: 1 });
    emit("stopped", { reason: "breakpoint", threadId: 1 });
    await flushPromises();
    await frameButton(49).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()?.getAttribute("aria-disabled")).toBe("true");
  });

  it("has no menu without the capability", async () => {
    addSession({});
    await mountPane();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();

    expect(menuItem()).toBeNull();
    await useDebugSessionsStore().restartFrame(sessionId, 2);
    expect(restartRequests()).toEqual([]);
  });

  it("has no menu and sends nothing while the program runs", async () => {
    const store = addSession({ supportsRestartFrame: true }, "running");
    await mountPane();
    expect(wrapper!.findAll(".debug-frames__item")).toHaveLength(0);
    await store.restartFrame(sessionId, 2);
    expect(restartRequests()).toEqual([]);
  });

  it("closes an open menu when the program runs on", async () => {
    addSession({ supportsRestartFrame: true });
    await mountPane();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()).not.toBeNull();

    emit("continued", { threadId: 1, allThreadsContinued: true });
    await flushPromises();

    expect(menuItem()).toBeNull();
    expect(restartRequests()).toEqual([]);
  });

  it("sends one request at a time", async () => {
    let answer: () => void = () => undefined;
    restartFrame = () => new Promise((resolve) => (answer = () => resolve({})));
    addSession({ supportsRestartFrame: true });
    await mountPane();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    menuItem()!.click();
    await flushPromises();
    await frameButton(0).trigger("contextmenu");
    await flushPromises();

    expect(menuItem()?.getAttribute("aria-disabled")).toBe("true");
    menuItem()!.click();
    await flushPromises();
    expect(restartRequests()).toHaveLength(1);
    answer();
    await flushPromises();
  });

  it("keeps Drop Frame disabled after the answer until the adapter's stop refreshes the frames", async () => {
    addSession({ supportsRestartFrame: true });
    await mountPane();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    menuItem()!.click();
    await flushPromises();
    // Answered, but the old frames are still shown: not again on them.
    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()?.getAttribute("aria-disabled")).toBe("true");
    menuItem()!.click();
    await flushPromises();
    expect(restartRequests()).toHaveLength(1);
    // Program output changes neither the state nor the frames.
    emit("output", { category: "stdout", output: "tick\n" });
    await flushPromises();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()?.getAttribute("aria-disabled")).toBe("true");
    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    await flushPromises();

    emit("continued", { threadId: 1 });
    emit("stopped", { reason: "restartframe", threadId: 1 });
    await flushPromises();
    await frameButton(0).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()?.getAttribute("aria-disabled")).not.toBe("true");
  });

  it("keeps Drop Frame disabled while the request is in flight even when the stop comes first", async () => {
    let answer: () => void = () => undefined;
    restartFrame = () => new Promise((resolve) => (answer = () => resolve({})));
    addSession({ supportsRestartFrame: true });
    await mountPane();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    menuItem()!.click();
    await flushPromises();
    // java-debug may send its stop before the answer.
    emit("continued", { threadId: 1 });
    emit("stopped", { reason: "restartframe", threadId: 1 });
    await flushPromises();
    await frameButton(0).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()?.getAttribute("aria-disabled")).toBe("true");
    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    await flushPromises();
    // Answered after the change: usable again.
    answer();
    await flushPromises();
    await frameButton(0).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()?.getAttribute("aria-disabled")).not.toBe("true");
  });

  it("allows Drop Frame again at once after a failure", async () => {
    restartFrame = async () => {
      throw new Error(REJECTED_BY_HOST);
    };
    addSession({ supportsRestartFrame: true });
    await mountPane();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    menuItem()!.click();
    await flushPromises();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    expect(menuItem()?.getAttribute("aria-disabled")).not.toBe("true");
  });

  it("shows the adapter's message and keeps the session as it was on a failure", async () => {
    restartFrame = async () => {
      throw new Error(REJECTED_BY_HOST);
    };
    addSession({ supportsRestartFrame: true });
    await mountPane();
    const before = session();
    await frameButton(1).trigger("contextmenu");
    await flushPromises();
    menuItem()!.click();
    await flushPromises();

    expect(debug.message.error).toHaveBeenCalledTimes(1);
    expect(String(debug.message.error.mock.calls[0]![0].message)).toMatch(/丢弃帧失败|Could not drop the frame/);
    expect(String(debug.message.error.mock.calls[0]![0].message)).toContain(REJECTED);
    expect(String(debug.message.error.mock.calls[0]![0].message)).not.toContain("dap restartFrame failed");
    expect(session().state).toBe("paused");
    expect(session().frames).toEqual(before.frames);
    expect(session().selectedFrameId).toBe(1);
    expect(session().lastError).toBeUndefined();
  });

  it("rejects with the adapter's message from the store", async () => {
    restartFrame = async () => {
      throw new Error(REJECTED_BY_HOST);
    };
    const store = addSession({ supportsRestartFrame: true });
    await expect(store.restartFrame(sessionId, 2)).rejects.toThrow(REJECTED);
  });
});
