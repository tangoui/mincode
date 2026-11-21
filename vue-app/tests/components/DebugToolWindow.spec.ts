import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { ElSelect } from "element-plus";
import { defineComponent, h } from "vue";
import { newSessionView, type DebugSessionView } from "@/domain/debug/session-model";

const debug = vi.hoisted(() => ({
  request: vi.fn(),
  setBreakpoints: vi.fn(),
  stop: vi.fn(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  canInvoke: () => true,
  invokeBackend: vi.fn(),
  listenAppEvent: vi.fn(() => () => undefined),
  copyText: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/debug-service", () => ({
  debugRequest: debug.request,
  setSessionBreakpoints: debug.setBreakpoints,
  stopDebugSession: debug.stop,
  onDebugEvent: () => () => undefined,
  attachTerminal: vi.fn(),
}));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("@/components/terminal/LocalTerminal.vue", () => ({
  __esModule: true,
  default: defineComponent({
    name: "LocalTerminal",
    props: { sessionId: String, attachOnlySessionId: String, active: Boolean, canClose: Boolean, canSplit: Boolean },
    setup: (props) => () => h("div", { class: "local-terminal-stub", "data-attach": props.attachOnlySessionId }),
  }),
}));

import DebugToolWindow from "@/components/debug/DebugToolWindow.vue";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/Users/A/Proj";
const FILE = "/Users/A/Proj/src/app.py";

let wrapper: ReturnType<typeof mount> | undefined;

beforeEach(() => {
  debug.request.mockReset();
  debug.request.mockResolvedValue({});
  debug.setBreakpoints.mockReset();
  debug.setBreakpoints.mockResolvedValue({ breakpoints: [] });
  debug.stop.mockReset();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

function addSession(change: Partial<DebugSessionView> = {}): DebugSessionView {
  const store = useDebugSessionsStore();
  const view = {
    ...newSessionView({
      id: "s1",
      projectRoot: ROOT,
      configId: "cfg-1",
      configName: "App",
      type: "python",
      noDebug: false,
      capabilities: {},
      startedAt: 1,
    }),
    ...change,
  };
  store.sessions.push(view);
  store.selectSession(view.id);
  return store.sessions[store.sessions.length - 1]!;
}

const frames = [
  { id: 11, name: "main", line: 3, column: 1, source: { name: "app.py", path: FILE } },
  { id: 12, name: "helper", line: 9, column: 1, source: { name: "app.py", path: FILE } },
];

async function mountWindow() {
  wrapper = mount(DebugToolWindow, { props: { sessionId: "s1" }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

function action(name: string) {
  return wrapper!.find(`[data-action="${name}"]`);
}

describe("DebugToolWindow", () => {
  it("disables step buttons while running", async () => {
    addSession();
    await mountWindow();
    for (const name of ["step-over", "step-into", "step-out", "run-to-cursor"]) {
      expect(action(name).attributes("disabled"), name).toBeDefined();
    }
    expect(action("pause").attributes("disabled")).toBeUndefined();
    expect(action("resume").exists()).toBe(false);
    expect(action("stop").attributes("disabled")).toBeUndefined();
  });

  it("enables step buttons while paused and steps the session", async () => {
    addSession({ state: "paused", frames, selectedFrameId: 11, threads: [{ id: 1, name: "main" }], selectedThreadId: 1 });
    await mountWindow();
    expect(action("step-over").attributes("disabled")).toBeUndefined();
    expect(action("resume").exists()).toBe(true);
    await action("step-over").trigger("click");
    expect(debug.request).toHaveBeenCalledWith("s1", "next", { threadId: 1 });
  });

  it("noDebug session shows console only", async () => {
    addSession({ noDebug: true });
    await mountWindow();
    expect(wrapper!.find(".debug-frames").exists()).toBe(false);
    expect(wrapper!.find(".debug-variables").exists()).toBe(false);
    expect(wrapper!.find(".debug-console").exists()).toBe(true);
    expect(action("step-over").exists()).toBe(false);
    expect(action("stop").exists()).toBe(true);
  });

  it("clicking a frame selects it", async () => {
    addSession({ state: "paused", frames, selectedFrameId: 11, threads: [{ id: 1, name: "main" }], selectedThreadId: 1 });
    await mountWindow();
    const items = wrapper!.findAll(".debug-frames__item");
    expect(items).toHaveLength(2);
    await items[1]!.trigger("click");
    expect(useDebugSessionsStore().selectedSession?.selectedFrameId).toBe(12);
    expect(wrapper!.emitted("open-location")).toEqual([[FILE, 9]]);
  });

  it("clicking a frame in a JDK or dependency class does not open it as a local file", async () => {
    const jdkFrames = [
      { id: 21, name: "Class.forName", line: 536, column: 1, source: { name: "Class.class", path: "jdt://contents/java.base/java.lang/Class.class", sourceReference: 7 } },
      { id: 22, name: "PolicyFile.init", line: 847, column: 1, source: { name: "PolicyFile.java", path: "jdt://contents/java.base/sun.security.provider/PolicyFile.class", sourceReference: 8 } },
      { id: 23, name: "<unknown>", line: 3, column: 1, source: { name: "<eval>", sourceReference: 9 } },
    ];
    addSession({ state: "paused", frames: jdkFrames, selectedFrameId: 21, threads: [{ id: 1, name: "main" }], selectedThreadId: 1 });
    await mountWindow();
    const items = wrapper!.findAll(".debug-frames__item");
    await items[1]!.trigger("click");
    expect(useDebugSessionsStore().selectedSession?.selectedFrameId).toBe(22);
    await items[2]!.trigger("click");
    expect(useDebugSessionsStore().selectedSession?.selectedFrameId).toBe(23);
    // The selected frame drives the read-only virtual source document instead.
    expect(wrapper!.emitted("open-location")).toBeUndefined();
  });

  it("watch expressions are evaluated on pause", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setWatches(ROOT, ["x + 1"]);
    debug.request.mockImplementation(async (_id: string, command: string) => {
      if (command === "evaluate") return { result: "42", variablesReference: 0 };
      if (command === "scopes") return { scopes: [{ name: "Locals", variablesReference: 5, expensive: false }] };
      if (command === "variables") return { variables: [{ name: "x", value: "41", variablesReference: 0 }] };
      return {};
    });
    const view = addSession();
    await mountWindow();
    expect(debug.request).not.toHaveBeenCalledWith("s1", "evaluate", expect.anything());

    const store = useDebugSessionsStore();
    store.sessions[0] = { ...view, state: "paused", frames, selectedFrameId: 11 };
    await flushPromises();
    expect(debug.request).toHaveBeenCalledWith("s1", "evaluate", { expression: "x + 1", context: "watch", frameId: 11 });
    await vi.waitFor(() => expect(wrapper!.text()).toContain("42"));

    debug.request.mockClear();
    store.selectFrame("s1", 12);
    await flushPromises();
    expect(debug.request).toHaveBeenCalledWith("s1", "evaluate", { expression: "x + 1", context: "watch", frameId: 12 });
  });

  it("a failed watch shows the adapter's text without the host's request prefix", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setWatches(ROOT, ["nope"]);
    debug.request.mockImplementation(async (_id: string, command: string) => {
      if (command === "evaluate") throw new Error("dap evaluate failed: name 'nope' is not defined");
      return {};
    });
    addSession({ state: "paused", frames, selectedFrameId: 11 });
    await mountWindow();
    await vi.waitFor(() => expect(wrapper!.text()).toContain("name 'nope' is not defined"));
    expect(wrapper!.text()).not.toContain("dap evaluate failed");
  });

  it("adds watches through the debug configs store", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    addSession();
    await mountWindow();
    const input = wrapper!.find(".debug-variables__watch-input input");
    await input.setValue("len(items)");
    await input.trigger("keydown", { key: "Enter" });
    expect(configs.stateFor(ROOT).watches).toEqual(["len(items)"]);
  });

  it("console uses attached terminal when runInTerminal happened", async () => {
    addSession({ consoleTerminalId: "debug-s1-1" });
    await mountWindow();
    await vi.waitFor(() => expect(wrapper!.find(".local-terminal-stub").exists()).toBe(true));
    expect(wrapper!.find(".local-terminal-stub").attributes("data-attach")).toBe("debug-s1-1");
  });

  it("console keeps every runInTerminal terminal: the latest shown, earlier ones switchable", async () => {
    const view = addSession({ consoleTerminalId: "debug-s1-1", consoleTerminalIds: ["debug-s1-1"] });
    await mountWindow();
    await vi.waitFor(() => expect(wrapper!.find(".local-terminal-stub").exists()).toBe(true));
    expect(wrapper!.find(".debug-console__terminals").exists()).toBe(false);
    const store = useDebugSessionsStore();
    store.sessions[0] = { ...view, consoleTerminalId: "debug-s1-2", consoleTerminalIds: ["debug-s1-1", "debug-s1-2"] };
    await vi.waitFor(() => expect(wrapper!.find(".local-terminal-stub").attributes("data-attach")).toBe("debug-s1-2"));
    await vi.waitFor(() => expect(wrapper!.find(".debug-console__terminals").exists()).toBe(true));
    const select = wrapper!.find(".debug-console__terminals").findComponent(ElSelect);
    select.vm.$emit("update:modelValue", "debug-s1-1");
    await vi.waitFor(() => expect(wrapper!.find(".local-terminal-stub").attributes("data-attach")).toBe("debug-s1-1"));
    // A third terminal takes the screen again.
    store.sessions[0] = { ...store.sessions[0]!, consoleTerminalId: "debug-s1-3", consoleTerminalIds: ["debug-s1-1", "debug-s1-2", "debug-s1-3"] };
    await vi.waitFor(() => expect(wrapper!.find(".local-terminal-stub").attributes("data-attach")).toBe("debug-s1-3"));
  });

  it("console says the program runs in an external terminal", async () => {
    addSession({ consoleNotice: { kind: "external", terminal: "Terminal" } });
    await mountWindow();
    const notice = wrapper!.find(".debug-console__notice");
    expect(notice.exists()).toBe(true);
    expect(notice.text()).toContain("程序在外部终端（Terminal）中运行");
    expect(wrapper!.find(".local-terminal-stub").exists()).toBe(false);
    // The notice replaces the empty-console placeholder.
    expect(wrapper!.text()).not.toContain("暂无输出");
  });

  it("console says why the integrated terminal was used instead", async () => {
    addSession({
      consoleTerminalId: "debug-s1-1",
      consoleTerminalIds: ["debug-s1-1"],
      consoleNotice: { kind: "externalFallback", code: "unavailable", message: "no graphical session (neither DISPLAY nor WAYLAND_DISPLAY is set)" },
    });
    await mountWindow();
    await vi.waitFor(() => expect(wrapper!.find(".local-terminal-stub").exists()).toBe(true));
    const notice = wrapper!.find(".debug-console__notice");
    // The reason is translated by its code; the backend's explanation is the detail.
    expect(notice.find(".el-alert__title").text()).toBe("无法在外部终端中运行，已改用集成终端：没有可用的外部终端");
    expect(notice.find(".el-alert__description").text()).toContain("DISPLAY");
  });

  it("console notices are translated", async () => {
    const { applySavedLocale } = await import("@/i18n/messages");
    await applySavedLocale("en");
    try {
      addSession({ consoleNotice: { kind: "external", terminal: "gnome-terminal" } });
      await mountWindow();
      expect(wrapper!.find(".debug-console__notice").text()).toContain("The program runs in an external terminal (gnome-terminal)");
      const store = useDebugSessionsStore();
      store.sessions[0] = {
        ...store.sessions[0]!,
        consoleTerminalId: "debug-s1-1",
        consoleTerminalIds: ["debug-s1-1"],
        consoleNotice: { kind: "externalFallback", code: "failed", message: "the program did not start within 10s" },
      };
      await vi.waitFor(() =>
        expect(wrapper!.find(".debug-console__notice .el-alert__title").text()).toBe(
          "Could not run in an external terminal; using the integrated terminal instead: the external terminal did not run the program"
        )
      );
      store.sessions[0] = {
        ...store.sessions[0]!,
        consoleNotice: { kind: "externalFallback", code: "unsupported", message: "argument 2 contains `%`, which cmd.exe would interpret" },
      };
      await vi.waitFor(() =>
        expect(wrapper!.find(".debug-console__notice .el-alert__title").text()).toContain(
          "an argument or environment variable cannot be passed to it safely"
        )
      );
    } finally {
      await applySavedLocale("zh");
    }
  });

  it("console rows keep their elements when the oldest output is dropped", async () => {
    const output = Array.from({ length: 3 }, (_, index) => ({ seq: index + 1, category: "stdout", text: `row ${index + 1}\n` }));
    const view = addSession({ noDebug: true, output });
    await mountWindow();
    const before = wrapper!.findAll(".debug-console__line").map((row) => row.element);
    expect(before).toHaveLength(3);
    const store = useDebugSessionsStore();
    store.sessions[0] = { ...view, output: [...output.slice(1), { seq: 4, category: "stdout", text: "row 4\n" }] };
    await flushPromises();
    const after = wrapper!.findAll(".debug-console__line").map((row) => row.element);
    expect(after.map((row) => row.textContent)).toEqual(["row 2\n", "row 3\n", "row 4\n"]);
    expect(after[0]).toBe(before[1]);
    expect(after[1]).toBe(before[2]);
  });

  it("console shows adapter output without a terminal", async () => {
    addSession({ output: [{ seq: 1, category: "stdout", text: "hello from app\n" }] });
    await mountWindow();
    expect(wrapper!.find(".local-terminal-stub").exists()).toBe(false);
    expect(wrapper!.find(".debug-console").text()).toContain("hello from app");
  });

  it("breakpoints dialog lists breakpoints and toggles exception filters", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.toggleBreakpoint(ROOT, FILE, 4);
    addSession({
      capabilities: {
        exceptionBreakpointFilters: [
          { filter: "raised", label: "Raised Exceptions", default: false },
          { filter: "uncaught", label: "Uncaught Exceptions", default: true },
        ],
      },
    });
    await mountWindow();
    await action("breakpoints").trigger("click");
    await vi.waitFor(() => expect(document.body.textContent).toContain("src/app.py:4"));
    const raised = [...document.body.querySelectorAll<HTMLElement>(".debug-breakpoints-dialog .el-checkbox")].find((item) =>
      item.textContent?.includes("Raised Exceptions")
    )!;
    raised.querySelector<HTMLInputElement>("input")!.click();
    await flushPromises();
    expect(configs.stateFor(ROOT).exceptionFilters.python).toEqual(["uncaught", "raised"]);
    expect(debug.request).toHaveBeenCalledWith("s1", "setExceptionBreakpoints", { filters: ["uncaught", "raised"] });

    const switchInput = document.body.querySelector<HTMLElement>(".debug-breakpoints-dialog .el-switch")!;
    switchInput.click();
    await flushPromises();
    expect(configs.breakpointsFor(ROOT, FILE)).toEqual([{ line: 4, enabled: false }]);
    expect(debug.setBreakpoints).toHaveBeenCalledWith("s1", FILE, []);
  });

  it("mute sends empty breakpoint lists for the session", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.toggleBreakpoint(ROOT, FILE, 4);
    addSession();
    await mountWindow();
    await action("mute").trigger("click");
    await flushPromises();
    expect(useDebugSessionsStore().isMuted("s1")).toBe(true);
    expect(debug.setBreakpoints).toHaveBeenCalledWith("s1", FILE, []);
  });
});
