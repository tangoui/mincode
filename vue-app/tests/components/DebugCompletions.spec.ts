// Completions (DAP completions) in the evaluate dialog, the watch input and
// the debug console input when the adapter declares
// supportsCompletionsRequest: 150 ms after the last keystroke, while paused,
// the input text is sent with the 1-based UTF-16 column of the cursor and the
// selected frame; an answer that comes after a newer keystroke or after the
// program ran on is dropped. The list opens with its first item highlighted
// and Enter accepts the highlighted item (as in IDEA); with no list (none yet,
// empty, or closed with Esc) Enter submits as without completions. Not paused
// or without the capability nothing is requested.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import type { DapCapabilities } from "@/domain/debug/types";
import { newSessionView, type DebugSessionView } from "@/domain/debug/session-model";

const debug = vi.hoisted(() => ({ request: vi.fn() }));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  canInvoke: () => true,
  invokeBackend: vi.fn(),
  listenAppEvent: vi.fn(() => () => undefined),
  copyText: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/debug-service", () => ({
  debugRequest: debug.request,
  setSessionBreakpoints: vi.fn().mockResolvedValue({ breakpoints: [] }),
  stopDebugSession: vi.fn(),
  onDebugEvent: () => () => undefined,
}));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("@/components/terminal/LocalTerminal.vue", () => ({
  __esModule: true,
  default: defineComponent({ name: "LocalTerminal", setup: () => () => h("div") }),
}));

import DebugConsolePane from "@/components/debug/DebugConsolePane.vue";
import DebugEvaluateDialog from "@/components/debug/DebugEvaluateDialog.vue";
import DebugVariablesPane from "@/components/debug/DebugVariablesPane.vue";
import { applyDebugCompletion } from "@/domain/debug/completions";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/p";
const FRAME = 11;
const LIST_ITEMS = [
  { label: "list : ArrayList<java.lang.String>", text: "list", type: "variable", start: 0 },
  { label: "LinkedList - java.util", text: "java.util.LinkedList", type: "class", start: 0 },
];

let wrapper: VueWrapper | undefined;
/** What `completions` answers; a function runs per request. */
let completions: (args: { text: string; column: number }) => Promise<unknown>;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  completions = async () => ({ targets: LIST_ITEMS });
  debug.request.mockReset();
  debug.request.mockImplementation(async (_id: string, command: string, args: { text: string; column: number; expression?: string }) => {
    if (command === "completions") return completions(args);
    if (command === "evaluate") return { result: `= ${args.expression}`, variablesReference: 0 };
    if (command === "scopes") return { scopes: [] };
    return {};
  });
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.useRealTimers();
  document.body.innerHTML = "";
});

function addSession(change: Partial<DebugSessionView> = {}, capabilities: DapCapabilities = { supportsCompletionsRequest: true }) {
  const store = useDebugSessionsStore();
  store.sessions.push({
    ...newSessionView({ id: "s1", projectRoot: ROOT, configId: "c1", configName: "App", type: "java", noDebug: false, capabilities, startedAt: 1 }),
    state: "paused",
    threads: [{ id: 1, name: "main" }],
    selectedThreadId: 1,
    stoppedThreadId: 1,
    frames: [{ id: FRAME, name: "Main.main(String[])", line: 6, column: 1 }],
    selectedFrameId: FRAME,
    ...change,
  });
  store.selectSession("s1");
}

/** The session runs on (a `continued` event). */
function runOn() {
  const store = useDebugSessionsStore();
  store.sessions[0] = { ...store.sessions[0]!, state: "running", frames: [], selectedFrameId: undefined };
}

async function mountDialog() {
  wrapper = mount(DebugEvaluateDialog, { props: { modelValue: true }, attachTo: document.body });
  await flushPromises();
  // The dialog opens (its focus handling settles) before the user types.
  await vi.advanceTimersByTimeAsync(500);
  await flushPromises();
}

async function mountPane(pane: "variables" | "console") {
  const store = useDebugSessionsStore();
  wrapper = mount(
    defineComponent({
      setup: () => () => {
        const session = store.sessions.find((view) => view.id === "s1")!;
        return pane === "variables" ? h(DebugVariablesPane, { session }) : h(DebugConsolePane, { session, active: true });
      },
    }),
    { attachTo: document.body }
  );
  await flushPromises();
}

const INPUTS = {
  dialog: ".debug-evaluate__expression textarea",
  watch: ".debug-variables__watch-input input",
  console: ".debug-console__repl input",
} as const;

function input(selector: string): HTMLInputElement | HTMLTextAreaElement {
  const element = document.body.querySelector<HTMLInputElement | HTMLTextAreaElement>(selector);
  if (!element) throw new Error(`${selector} not found`);
  return element;
}

/** Types `text` as the input's whole value (an input event, the cursor at its end). */
async function type(selector: string, text: string) {
  const element = input(selector);
  element.focus();
  element.value = text;
  element.setSelectionRange(text.length, text.length);
  element.dispatchEvent(new Event("input", { bubbles: true }));
  await flushPromises();
}

async function key(selector: string, name: string, init: KeyboardEventInit = {}) {
  input(selector).dispatchEvent(new KeyboardEvent("keydown", { key: name, bubbles: true, cancelable: true, ...init }));
  await flushPromises();
}

async function wait(ms: number) {
  await vi.advanceTimersByTimeAsync(ms);
  await flushPromises();
  // The suggestion popper opens on a timer of its own.
  await vi.advanceTimersByTimeAsync(0);
  await flushPromises();
}

function completionRequests() {
  return debug.request.mock.calls.filter((call) => call[1] === "completions");
}

function evaluateRequests() {
  return debug.request.mock.calls.filter((call) => call[1] === "evaluate");
}

/** The label of the open list's highlighted row (null when none). */
function highlightedLabel(): string | null {
  const popper = [...document.body.querySelectorAll<HTMLElement>(".el-autocomplete__popper")].find((element) => element.style.display !== "none");
  return popper?.querySelector("li.highlighted .debug-completions__label")?.textContent?.trim() ?? null;
}

/** The labels of the open suggestion list (none when it is closed). */
function shownLabels(): string[] {
  const popper = [...document.body.querySelectorAll<HTMLElement>(".el-autocomplete__popper")].find((element) => element.style.display !== "none");
  if (!popper) return [];
  return [...popper.querySelectorAll(".debug-completions__label")].map((element) => element.textContent?.trim() ?? "");
}

function deferred() {
  let resolve!: (value: unknown) => void;
  const promise = new Promise<unknown>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("applyDebugCompletion", () => {
  it("replaces the identifier before the cursor when the item has no length", () => {
    expect(applyDebugCompletion("li", { label: "list : ArrayList", text: "list" })).toBe("list");
    expect(applyDebugCompletion("list.si", { label: "size() : int", text: "size()", start: 0 })).toBe("list.size()");
    expect(applyDebugCompletion("a + li", { label: "list" })).toBe("a + list");
    expect(applyDebugCompletion("list.", { label: "size() : int", text: "size()" })).toBe("list.size()");
  });

  it("replaces `length` code units before the cursor when the item has a length (start is not used)", () => {
    expect(applyDebugCompletion("x.getNa", { label: "getName()", length: 5 })).toBe("x.getName()");
    expect(applyDebugCompletion("x.getNa", { label: "getName()", length: 0 })).toBe("x.getNagetName()");
    // start is ignored, whatever base the adapter meant.
    expect(applyDebugCompletion("x.getNa", { label: "getName()", start: 0, length: 2 })).toBe("x.getgetName()");
    expect(applyDebugCompletion("x.getNa", { label: "getName()", start: 3, length: 5 })).toBe("x.getName()");
    // A length beyond the cursor replaces only what is before it.
    expect(applyDebugCompletion("ab", { label: "abc", length: 9 })).toBe("abc");
  });

  it("works on a later line of a multi-line expression", () => {
    expect(applyDebugCompletion("a\nli", { label: "list", length: 2 })).toBe("a\nlist");
    expect(applyDebugCompletion("a\nli", { label: "list" })).toBe("a\nlist");
    expect(applyDebugCompletion("li\nx", { label: "list", length: 2 }, 2)).toBe("list\nx");
  });

  it("keeps the text after the cursor", () => {
    expect(applyDebugCompletion("li + 1", { label: "list" }, 2)).toBe("list + 1");
  });
});

describe("debug completions", () => {
  it("evaluate dialog: requests 150 ms after typing and Enter accepts the first item", async () => {
    addSession();
    await mountDialog();
    await type(INPUTS.dialog, "li");
    await wait(149);
    expect(completionRequests()).toHaveLength(0);
    await wait(1);
    expect(debug.request).toHaveBeenCalledWith("s1", "completions", { text: "li", column: 3, frameId: FRAME });
    expect(shownLabels()).toEqual(["list : ArrayList<java.lang.String>", "LinkedList - java.util"]);
    expect(highlightedLabel()).toBe("list : ArrayList<java.lang.String>");

    await key(INPUTS.dialog, "Enter");
    expect(input(INPUTS.dialog).value).toBe("list");
    expect(evaluateRequests()).toHaveLength(0);
    expect(shownLabels()).toEqual([]);

    // The list is closed: Enter evaluates.
    await key(INPUTS.dialog, "Enter");
    expect(debug.request).toHaveBeenCalledWith("s1", "evaluate", { expression: "list", context: "repl", frameId: FRAME });
    await wait(200);
    expect(shownLabels()).toEqual([]);
  });

  it("evaluate dialog: Shift+Enter still adds a line, an IME Enter does nothing", async () => {
    addSession();
    await mountDialog();
    await type(INPUTS.dialog, "li");
    const shift = new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true, cancelable: true });
    input(INPUTS.dialog).dispatchEvent(shift);
    // The browser inserts the newline (not prevented); nothing is evaluated.
    expect(shift.defaultPrevented).toBe(false);
    await key(INPUTS.dialog, "Enter", { isComposing: true });
    expect(evaluateRequests()).toHaveLength(0);
  });

  it("sends only the last of quick keystrokes", async () => {
    addSession();
    await mountDialog();
    await type(INPUTS.dialog, "l");
    await wait(100);
    await type(INPUTS.dialog, "li");
    await wait(150);
    expect(completionRequests().map((call) => call[2])).toEqual([{ text: "li", column: 3, frameId: FRAME }]);
  });

  it("drops an answer that comes after a newer keystroke", async () => {
    addSession();
    const answers: Array<ReturnType<typeof deferred>> = [];
    completions = () => {
      const answer = deferred();
      answers.push(answer);
      return answer.promise;
    };
    await mountDialog();
    await type(INPUTS.dialog, "l");
    await wait(150);
    await type(INPUTS.dialog, "li");
    await wait(150);
    expect(answers).toHaveLength(2);
    answers[1]!.resolve({ targets: [LIST_ITEMS[0]] });
    await flushPromises();
    expect(shownLabels()).toEqual(["list : ArrayList<java.lang.String>"]);
    // The first request answers late: neither shown nor replacing the newer list.
    answers[0]!.resolve({ targets: [{ label: "lambda", type: "variable" }] });
    await flushPromises();
    expect(shownLabels()).toEqual(["list : ArrayList<java.lang.String>"]);

    // An answer that comes after typing on (before the next request) is not shown either.
    completions = () => {
      const answer = deferred();
      answers.push(answer);
      return answer.promise;
    };
    await type(INPUTS.dialog, "lis");
    await wait(150);
    await type(INPUTS.dialog, "list");
    answers[2]!.resolve({ targets: [LIST_ITEMS[0]] });
    await flushPromises();
    expect(shownLabels()).toEqual([]);
    expect(input(INPUTS.dialog).value).toBe("list");
  });

  it("drops an answer that comes after the program ran on", async () => {
    addSession();
    const answer = deferred();
    completions = () => answer.promise;
    await mountDialog();
    await type(INPUTS.dialog, "li");
    await wait(150);
    expect(completionRequests()).toHaveLength(1);
    runOn();
    await flushPromises();
    answer.resolve({ targets: LIST_ITEMS });
    await flushPromises();
    expect(shownLabels()).toEqual([]);
  });

  it("store: completions of a stop that is over are dropped, failures yield []", async () => {
    addSession();
    const store = useDebugSessionsStore();
    const answer = deferred();
    completions = () => answer.promise;
    const late = store.completions("s1", "li", 3);
    // The program runs on and stops again, even with the same frame id: the answer is of the old stop.
    await store.resume("s1");
    store.sessions[0] = { ...store.sessions[0]!, state: "paused", frames: [{ id: FRAME, name: "m", line: 1, column: 1 }], selectedFrameId: FRAME };
    answer.resolve({ targets: LIST_ITEMS });
    expect(await late).toEqual([]);

    completions = async () => {
      throw new Error("boom");
    };
    expect(await store.completions("s1", "li", 3)).toEqual([]);
    completions = async () => ({ targets: [{ label: "list", text: "list", type: "variable", start: 0 }, { text: "no label" }, "junk"] });
    expect(await store.completions("s1", "li", 3)).toEqual([{ label: "list", text: "list", type: "variable", start: 0 }]);
  });

  it("does not request while the program runs or without the capability", async () => {
    addSession({ state: "running", frames: [], selectedFrameId: undefined });
    await mountDialog();
    await type(INPUTS.dialog, "li");
    await wait(300);
    expect(completionRequests()).toHaveLength(0);
    wrapper?.unmount();
    wrapper = undefined;
    useDebugSessionsStore().sessions.splice(0);

    addSession({}, {});
    await mountDialog();
    await type(INPUTS.dialog, "li");
    await wait(300);
    expect(completionRequests()).toHaveLength(0);
    // Enter evaluates as before.
    await key(INPUTS.dialog, "Enter");
    expect(debug.request).toHaveBeenCalledWith("s1", "evaluate", { expression: "li", context: "repl", frameId: FRAME });
  });

  it("watch input: ↓ + Enter accepts the second item, Enter without a list adds the watch", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    addSession();
    await mountPane("variables");
    // The input keeps its label, placeholder and clear button.
    expect(input(INPUTS.watch).getAttribute("aria-label")).toBe(input(INPUTS.watch).getAttribute("placeholder"));
    expect(input(INPUTS.watch).getAttribute("aria-label")).toBeTruthy();
    expect(document.body.querySelector(".debug-variables__watch-input .el-input--small")).not.toBeNull();
    await type(INPUTS.watch, "li");
    await wait(150);
    expect(debug.request).toHaveBeenCalledWith("s1", "completions", { text: "li", column: 3, frameId: FRAME });
    await key(INPUTS.watch, "ArrowDown");
    expect(highlightedLabel()).toBe("LinkedList - java.util");
    await key(INPUTS.watch, "Enter");
    expect(input(INPUTS.watch).value).toBe("java.util.LinkedList");
    expect(configs.stateFor(ROOT).watches).toEqual([]);

    await key(INPUTS.watch, "Enter");
    expect(configs.stateFor(ROOT).watches).toEqual(["java.util.LinkedList"]);
    expect(input(INPUTS.watch).value).toBe("");
    // The submitted text is not completed afterwards.
    debug.request.mockClear();
    await wait(300);
    expect(completionRequests()).toHaveLength(0);
    expect(shownLabels()).toEqual([]);
  });

  it("watch input without the capability adds on Enter", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    addSession({}, {});
    await mountPane("variables");
    await type(INPUTS.watch, "li");
    await wait(300);
    expect(completionRequests()).toHaveLength(0);
    await key(INPUTS.watch, "Enter");
    expect(configs.stateFor(ROOT).watches).toEqual(["li"]);
  });

  it("console input: Enter accepts the first item, Enter without a list runs the command", async () => {
    addSession();
    await mountPane("console");
    await type(INPUTS.console, "li");
    await wait(150);
    expect(shownLabels()).toHaveLength(2);
    await key(INPUTS.console, "Enter");
    expect(input(INPUTS.console).value).toBe("list");
    expect(evaluateRequests()).toHaveLength(0);
    await key(INPUTS.console, "Enter");
    expect(debug.request).toHaveBeenCalledWith("s1", "evaluate", { expression: "list", context: "repl", frameId: FRAME });
    expect(input(INPUTS.console).value).toBe("");
    expect(shownLabels()).toEqual([]);

    // Esc closes the list: Enter then runs what was typed.
    await type(INPUTS.console, "li");
    await wait(150);
    expect(shownLabels()).toHaveLength(2);
    await key(INPUTS.console, "Escape");
    expect(shownLabels()).toEqual([]);
    await key(INPUTS.console, "Enter");
    expect(debug.request).toHaveBeenCalledWith("s1", "evaluate", { expression: "li", context: "repl", frameId: FRAME });

    // An empty answer opens no list: Enter runs the command.
    completions = async () => ({ targets: [] });
    await type(INPUTS.console, "zz");
    await wait(150);
    await key(INPUTS.console, "Enter");
    expect(debug.request).toHaveBeenCalledWith("s1", "evaluate", { expression: "zz", context: "repl", frameId: FRAME });
  });

  it("with no list, modified list keys keep their native default and never reach Element Plus", async () => {
    addSession();
    await mountDialog();
    await type(INPUTS.dialog, "li");
    // Esc: no list (and the pending request is dropped).
    await key(INPUTS.dialog, "Escape");
    await wait(300);
    debug.request.mockClear();
    const modified: Array<[string, KeyboardEventInit]> = [
      ["ArrowUp", { metaKey: true }],
      ["ArrowDown", { metaKey: true }],
      ["ArrowUp", { altKey: true }],
      ["ArrowUp", { metaKey: true, shiftKey: true }],
      ["Home", { ctrlKey: true }],
      ["End", { ctrlKey: true, shiftKey: true }],
      ["PageDown", { altKey: true }],
    ];
    for (const [name, init] of modified) {
      const event = new KeyboardEvent("keydown", { key: name, code: name, bubbles: true, cancelable: true, ...init });
      input(INPUTS.dialog).dispatchEvent(event);
      // Element Plus would prevent it (cursor movement / selection lost).
      expect(event.defaultPrevented, `${name} ${JSON.stringify(init)}`).toBe(false);
    }
    await wait(300);
    expect(completionRequests()).toHaveLength(0);
    expect(evaluateRequests()).toHaveLength(0);

    // Ctrl/⌘+Enter evaluates as before completions; Element Plus's Enter
    // handling (which asks for completions again and opens the list) never runs.
    for (const init of [{ ctrlKey: true }, { metaKey: true }] as KeyboardEventInit[]) {
      debug.request.mockClear();
      await key(INPUTS.dialog, "Enter", { code: "Enter", ...init });
      await wait(300);
      expect(evaluateRequests().map((call) => call[2])).toEqual([{ expression: "li", context: "repl", frameId: FRAME }]);
      expect(completionRequests()).toHaveLength(0);
      expect(shownLabels()).toEqual([]);
    }
  });

  it("console input is disabled and requests nothing once the program runs", async () => {
    addSession();
    await mountPane("console");
    expect(input(INPUTS.console).disabled).toBe(false);
    const pausedPlaceholder = input(INPUTS.console).placeholder;
    runOn();
    await flushPromises();
    expect(input(INPUTS.console).disabled).toBe(true);
    expect(input(INPUTS.console).placeholder).not.toBe(pausedPlaceholder);
    await type(INPUTS.console, "li");
    await wait(300);
    expect(completionRequests()).toHaveLength(0);
  });

  it("console shows a failed command's adapter text without the host's request prefix", async () => {
    debug.request.mockImplementation(async (_id: string, command: string) => {
      if (command === "evaluate") throw new Error("dap evaluate failed: cannot find symbol: nope");
      return {};
    });
    addSession({}, {});
    await mountPane("console");
    await type(INPUTS.console, "nope");
    await key(INPUTS.console, "Enter");
    const errors = [...document.body.querySelectorAll(".debug-console__line.is-repl-error")].map((line) => line.textContent);
    expect(errors).toEqual(["cannot find symbol: nope"]);
  });
});
