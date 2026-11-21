// Smart Step Into (DAP stepInTargets) when the adapter declares
// supportsStepInTargetsRequest: the toolbar button next to Step Into (and the
// `project:debug.smartStepInto` command, Shift+F7) asks for the step-in
// targets of the selected frame. No target steps in as usual, one steps into
// it, several open a menu below the button (keyboard selectable, Esc cancels
// without a request). Without the capability there is no button and no
// command; while the program runs the button is disabled and nothing is sent.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import type { DapCapabilities, DapStackFrame, DebugEvent } from "@/domain/debug/types";
import { newSessionView } from "@/domain/debug/session-model";

const debug = vi.hoisted(() => ({
  request: vi.fn(),
  handler: undefined as ((event: DebugEvent) => void) | undefined,
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
import { registerDebugCommands } from "@/composables/use-debug-commands";
import { buildKeybindingContext } from "@/domain/keybindings/context";
import { KEYBINDING_COMMANDS } from "@/domain/keybindings/catalog";
import { MINCODE_PRESET } from "@/domain/keybindings/presets/mincode";
import { executeRegisteredCommand, isRegisteredCommandEnabled } from "@/services/command-registry";
import { setDebugLineAnchorProvider } from "@/services/debug-line-anchor";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/p";
const FILE = "/p/Main.java";
const FRAMES: DapStackFrame[] = [
  { id: 1, name: "Main.main(String[])", line: 5, column: 1, source: { path: FILE } },
];
const TARGETS = [
  { id: 101, label: "twice(int)", line: 5, column: 21 },
  { id: 102, label: "twice(int)", line: 5, column: 31 },
  { id: 103, label: "add(int, int)", line: 5, column: 17 },
];

let wrapper: VueWrapper | undefined;
let disposers: Array<() => void> = [];
/** What `stepInTargets` answers; a function runs per request. */
let stepInTargets: () => Promise<unknown>;

beforeEach(() => {
  stepInTargets = async () => ({ targets: TARGETS });
  debug.request.mockReset();
  debug.request.mockImplementation(async (_id: string, command: string) => {
    if (command === "stepInTargets") return stepInTargets();
    if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
    if (command === "stackTrace") return { stackFrames: FRAMES };
    return {};
  });
});

afterEach(() => {
  for (const dispose of disposers) dispose();
  disposers = [];
  wrapper?.unmount();
  wrapper = undefined;
  document.body.innerHTML = "";
});

function addSession(capabilities: DapCapabilities, state: "paused" | "running" = "paused", frames: DapStackFrame[] = FRAMES) {
  const store = useDebugSessionsStore();
  store.sessions.push({
    ...newSessionView({ id: "s1", projectRoot: ROOT, configId: "c1", configName: "App", type: "java", noDebug: false, capabilities, startedAt: 1 }),
    state,
    threads: [{ id: 1, name: "main" }],
    selectedThreadId: 1,
    stoppedThreadId: state === "paused" ? 1 : undefined,
    frames: state === "paused" ? frames.map((frame) => ({ ...frame })) : [],
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

function button() {
  return wrapper!.find('[data-action="smart-step-into"]');
}

const settle = async () => {
  await flushPromises();
  await new Promise((resolve) => setTimeout(resolve, 20));
};

function menuItems() {
  return [...document.body.querySelectorAll<HTMLElement>(".debug-step-targets__menu .el-dropdown-menu__item")];
}

function requests(command: string) {
  return debug.request.mock.calls.filter((call) => call[1] === command);
}

function keydown(target: Element, key: string) {
  target.dispatchEvent(new KeyboardEvent("keydown", { key, code: key, bubbles: true, cancelable: true }));
}

describe("smart step into", () => {
  it("steps in as usual when the frame has no target", async () => {
    stepInTargets = async () => ({ targets: [] });
    addSession({ supportsStepInTargetsRequest: true });
    await mountWindow();
    await button().trigger("click");
    await settle();

    expect(requests("stepInTargets")).toEqual([["s1", "stepInTargets", { frameId: 1 }]]);
    expect(requests("stepIn")).toEqual([["s1", "stepIn", { threadId: 1 }]]);
    expect(menuItems()).toHaveLength(0);
  });

  it("steps straight into the only target", async () => {
    stepInTargets = async () => ({ targets: [TARGETS[2]] });
    addSession({ supportsStepInTargetsRequest: true });
    await mountWindow();
    await button().trigger("click");
    await settle();

    expect(requests("stepIn")).toEqual([["s1", "stepIn", { threadId: 1, targetId: 103 }]]);
    expect(menuItems()).toHaveLength(0);
  });

  it("lists several targets in a menu and steps into the chosen one", async () => {
    addSession({ supportsStepInTargetsRequest: true });
    await mountWindow();
    await button().trigger("click");
    await settle();

    const popper = document.body.querySelector(".debug-step-targets__menu");
    expect(popper?.textContent).toMatch(/选择要进入的方法|Choose a method to step into/);
    // In source order (the adapter's answer is not).
    expect(menuItems().map((item) => item.textContent?.trim())).toEqual(["add(int, int)", "twice(int)", "twice(int)"]);
    expect(requests("stepIn")).toEqual([]);

    menuItems()[0]!.click();
    await settle();
    expect(requests("stepIn")).toEqual([["s1", "stepIn", { threadId: 1, targetId: 103 }]]);
    expect(menuItems()).toHaveLength(0);
  });

  it("selects a target with the arrow keys and Enter", async () => {
    addSession({ supportsStepInTargetsRequest: true });
    await mountWindow();
    await button().trigger("click");
    await settle();

    // Element Plus focuses the opened menu (after its transition, which the
    // test renderer stubs), and the menu passes the focus to its first item.
    document.body.querySelector<HTMLElement>(".debug-step-targets__menu .el-dropdown-menu")!.focus();
    expect(document.activeElement).toBe(menuItems()[0]);
    keydown(menuItems()[0]!, "ArrowDown");
    await flushPromises();
    expect(document.activeElement).toBe(menuItems()[1]);
    keydown(menuItems()[1]!, "Enter");
    await settle();
    expect(requests("stepIn")).toEqual([["s1", "stepIn", { threadId: 1, targetId: 101 }]]);
  });

  it("cancels with Escape without a request", async () => {
    addSession({ supportsStepInTargetsRequest: true });
    await mountWindow();
    await button().trigger("click");
    await settle();
    expect(menuItems()).toHaveLength(3);

    keydown(document.activeElement ?? document.body, "Escape");
    await settle();
    expect(menuItems()).toHaveLength(0);
    expect(requests("stepIn")).toEqual([]);
    expect(useDebugSessionsStore().stepTargetsPickFor("s1")).toBeNull();
  });

  it("closes the menu when the program runs on", async () => {
    addSession({ supportsStepInTargetsRequest: true });
    await mountWindow();
    await button().trigger("click");
    await settle();
    expect(menuItems()).toHaveLength(3);

    debug.handler?.({ sessionId: "s1", event: "continued", body: { threadId: 1, allThreadsContinued: true } });
    await settle();
    expect(menuItems()).toHaveLength(0);
    expect(useDebugSessionsStore().stepTargetsPickFor("s1")).toBeNull();
  });

  it("drops an answer that comes after the program ran on", async () => {
    let answer: () => void = () => undefined;
    stepInTargets = () => new Promise((resolve) => (answer = () => resolve({ targets: TARGETS })));
    addSession({ supportsStepInTargetsRequest: true });
    await mountWindow();
    await button().trigger("click");
    await flushPromises();
    debug.handler?.({ sessionId: "s1", event: "continued", body: { threadId: 1, allThreadsContinued: true } });
    answer();
    await settle();

    expect(menuItems()).toHaveLength(0);
    expect(requests("stepIn")).toEqual([]);
  });

  it("disables the button of an ended session", async () => {
    const store = addSession({ supportsStepInTargetsRequest: true });
    store.sessions[0]!.state = "ended";
    await mountWindow();
    expect(button().attributes("disabled")).toBeDefined();
    await store.smartStepInto("s1");
    expect(requests("stepInTargets")).toEqual([]);
  });

  it("falls back to a plain step in when the targets cannot be read", async () => {
    stepInTargets = async () => {
      throw new Error("no targets");
    };
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    try {
      const store = addSession({ supportsStepInTargetsRequest: true });
      await expect(store.stepInTargets("s1")).resolves.toEqual([]);
      await store.smartStepInto("s1");
      expect(requests("stepIn")).toEqual([["s1", "stepIn", { threadId: 1 }]]);
      expect(store.sessions[0]!.lastError).toBeUndefined();
      // No message for the user, but the reason is in the developer log.
      expect(warn).toHaveBeenCalledWith(expect.stringContaining("stepInTargets"), expect.objectContaining({ message: "no targets" }));
    } finally {
      warn.mockRestore();
    }
  });

  it("drops targets that are not valid DAP step-in targets", async () => {
    stepInTargets = async () => ({ targets: [{ id: 7, label: "ok()" }, { id: "x", label: "bad" }, { id: 8 }, null] });
    const store = addSession({ supportsStepInTargetsRequest: true });
    await expect(store.stepInTargets("s1")).resolves.toEqual([{ id: 7, label: "ok()" }]);
  });

  it("has no button and sends nothing without the capability", async () => {
    const store = addSession({});
    await mountWindow();
    expect(button().exists()).toBe(false);
    expect(wrapper!.find('[data-action="step-into"]').exists()).toBe(true);

    await store.smartStepInto("s1");
    await expect(store.stepInTargets("s1")).resolves.toEqual([]);
    expect(await store.stepIntoTarget("s1", 103)).toBe(false);
    expect(requests("stepInTargets")).toEqual([]);
    expect(requests("stepIn")).toEqual([]);
  });

  it("disables the button and sends nothing while the program runs", async () => {
    const store = addSession({ supportsStepInTargetsRequest: true }, "running");
    await mountWindow();
    expect(button().exists()).toBe(true);
    expect(button().attributes("disabled")).toBeDefined();

    await store.smartStepInto("s1");
    expect(await store.stepIntoTarget("s1", 103)).toBe(false);
    expect(requests("stepInTargets")).toEqual([]);
    expect(requests("stepIn")).toEqual([]);
  });
});

describe("smart step into targets and frame", () => {
  it("orders the targets by line and column, unpositioned ones last in their order", async () => {
    stepInTargets = async () => ({
      targets: [
        { id: 2, label: "twice(2)", line: 3, column: 31 },
        { id: 7, label: "first()" },
        { id: 3, label: "add(twice(1),twice(2))", line: 3, column: 17 },
        { id: 4, label: "twice(1)", line: 3, column: 21 },
        { id: 8, label: "second()" },
        { id: 6, label: "above()", line: 2 },
      ],
    });
    const store = addSession({ supportsStepInTargetsRequest: true });
    expect((await store.stepInTargets("s1")).map((target) => target.id)).toEqual([6, 3, 4, 2, 7, 8]);
  });

  it("asks about the top frame and keeps the choice when another frame is selected", async () => {
    const frames: DapStackFrame[] = [...FRAMES, { id: 2, name: "Main.run()", line: 9, column: 1, source: { path: FILE } }];
    const store = addSession({ supportsStepInTargetsRequest: true }, "paused", frames);
    store.selectFrame("s1", 2);
    await mountWindow();
    await button().trigger("click");
    await settle();
    expect(requests("stepInTargets")).toEqual([["s1", "stepInTargets", { frameId: 1 }]]);

    store.selectFrame("s1", 1);
    store.selectFrame("s1", 2);
    await settle();
    expect(menuItems()).toHaveLength(3);
    menuItems()[0]!.click();
    await settle();
    expect(requests("stepIn")).toEqual([["s1", "stepIn", { threadId: 1, targetId: 103 }]]);
  });
});

describe("smart step into menu position", () => {
  function anchorStyle() {
    return wrapper!.find(".app-context-menu__anchor").attributes("style");
  }

  it("opens below the execution line when the editor shows it", async () => {
    const provider = vi.fn(() => ({ x: 300, y: 120 }));
    disposers.push(setDebugLineAnchorProvider(provider));
    addSession({ supportsStepInTargetsRequest: true });
    await mountWindow();
    await button().trigger("click");
    await settle();

    expect(provider).toHaveBeenCalledWith(FILE, 5, 1);
    expect(anchorStyle()).toContain("left: 300px");
    expect(anchorStyle()).toContain("top: 120px");
  });

  it("opens below the toolbar button when no editor shows the line", async () => {
    disposers.push(setDebugLineAnchorProvider(() => null));
    addSession({ supportsStepInTargetsRequest: true });
    await mountWindow();
    const element = button().element as HTMLElement;
    element.getBoundingClientRect = () => ({ left: 40, top: 34, right: 66, bottom: 60, width: 26, height: 26, x: 40, y: 34, toJSON: () => ({}) });
    await button().trigger("click");
    await settle();

    expect(anchorStyle()).toContain("left: 40px");
    expect(anchorStyle()).toContain("top: 62px");
  });

  it("opens in the upper middle of the window when the panel is collapsed", async () => {
    addSession({ supportsStepInTargetsRequest: true });
    await mountWindow();
    await button().trigger("click");
    await settle();

    expect(anchorStyle()).toContain(`left: ${Math.max(8, window.innerWidth / 2 - 120)}px`);
    expect(anchorStyle()).toContain(`top: ${Math.round(window.innerHeight / 3)}px`);
  });

  // The same project open in two workspaces: the choice is the store's, but
  // only the tool window of the active workspace view shows it (also when its
  // panel is collapsed, i.e. `active` is false).
  it("opens one menu, in the tool window of the active workspace view", async () => {
    const store = addSession({ supportsStepInTargetsRequest: true });
    const cached = mount(DebugToolWindow, { props: { sessionId: "s1", active: false, viewActive: false }, attachTo: document.body });
    try {
      wrapper = mount(DebugToolWindow, { props: { sessionId: "s1", active: false, viewActive: true }, attachTo: document.body });
      await flushPromises();
      await store.smartStepInto("s1");
      await settle();
      expect(document.body.querySelectorAll(".debug-step-targets__menu")).toHaveLength(1);
      expect(menuItems()).toHaveLength(3);

      // Switching workspaces moves the menu instead of adding one.
      await wrapper.setProps({ viewActive: false });
      await cached.setProps({ viewActive: true });
      await settle();
      expect(document.body.querySelectorAll(".debug-step-targets__menu")).toHaveLength(1);
      menuItems()[0]!.click();
      await settle();
      expect(requests("stepIn")).toHaveLength(1);
      expect(document.body.querySelectorAll(".debug-step-targets__menu")).toHaveLength(0);
    } finally {
      cached.unmount();
    }
  });
});

describe("smart step into command", () => {
  const options = () => ({
    projectRoot: () => ROOT,
    activeFile: () => FILE,
    ownsProject: (root: string) => root === ROOT,
    cursor: () => ({ path: FILE, line: 5 }),
    toggleBreakpoint: vi.fn(),
    openEvaluate: vi.fn(),
    newConfiguration: vi.fn(),
  });
  const context = () => buildKeybindingContext({ target: null, platform: "mac" });

  it("is in the keybinding catalog and bound to Shift+F7 during a session", () => {
    const entry = KEYBINDING_COMMANDS.find((command) => command.id === "project:debug.smartStepInto");
    expect(entry).toMatchObject({ scope: "project", showInPalette: true, available: true });
    expect(MINCODE_PRESET.rules).toContainEqual({ key: "shift+f7", command: "project:debug.smartStepInto", when: "debug.active" });
  });

  it("runs smart step into on this workspace's session", async () => {
    stepInTargets = async () => ({ targets: [TARGETS[0]] });
    addSession({ supportsStepInTargetsRequest: true });
    disposers = registerDebugCommands(options());
    expect(context()["debug.stepInTargets"]).toBe(true);
    expect(isRegisteredCommandEnabled("project:debug.smartStepInto", context())).toBe(true);

    await executeRegisteredCommand("project:debug.smartStepInto");
    await settle();
    expect(requests("stepIn")).toEqual([["s1", "stepIn", { threadId: 1, targetId: 101 }]]);
  });

  it("is unavailable without the capability", () => {
    addSession({});
    disposers = registerDebugCommands(options());
    expect(context()["debug.stepInTargets"]).toBe(false);
    expect(isRegisteredCommandEnabled("project:debug.smartStepInto", context())).toBe(false);
  });

  it("opens the menu of the session's tool window from the command", async () => {
    addSession({ supportsStepInTargetsRequest: true });
    disposers = registerDebugCommands(options());
    await mountWindow();
    await executeRegisteredCommand("project:debug.smartStepInto");
    await settle();
    expect(menuItems()).toHaveLength(3);
  });
});
