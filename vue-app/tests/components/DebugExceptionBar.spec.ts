// When a session pauses on an exception (stopped reason "exception") and the
// adapter supports `exceptionInfo`, the tool window shows a bar under the
// toolbar with the exception type and description; the stack trace, when
// the adapter gives one, can be expanded. The bar goes away once the program
// runs again. A failed request shows nothing and is not a session error.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import type { DapCapabilities, DebugEvent } from "@/domain/debug/types";
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
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("@/components/terminal/LocalTerminal.vue", () => ({
  __esModule: true,
  default: defineComponent({
    name: "LocalTerminal",
    props: { sessionId: String, attachOnlySessionId: String, active: Boolean, canClose: Boolean, canSplit: Boolean },
    setup: () => () => h("div", { class: "local-terminal-stub" }),
  }),
}));

import DebugToolWindow from "@/components/debug/DebugToolWindow.vue";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/p";
const FILE = "/p/Main.java";
const NPE = {
  exceptionId: "NullPointerException",
  description: "Cannot invoke \"String.length()\" because \"s\" is null",
  breakMode: "always",
  details: { message: "s is null", typeName: "NullPointerException", stackTrace: "at Main.run(Main.java:3)\nat Main.main(Main.java:9)" },
};

let wrapper: VueWrapper | undefined;
let sessionSeq = 0;
let sessionId = "";
/** What `exceptionInfo` answers; a function runs per request. */
let exceptionInfo: (args: { threadId?: number }) => Promise<unknown>;

beforeEach(() => {
  sessionSeq += 1;
  sessionId = `exception-${sessionSeq}`;
  exceptionInfo = async () => NPE;
  debug.request.mockReset();
  debug.request.mockImplementation(async (_id: string, command: string, args: { threadId?: number }) => {
    if (command === "threads") return { threads: [{ id: 1, name: "main" }, { id: 2, name: "worker" }] };
    if (command === "stackTrace") return { stackFrames: [{ id: 11, name: "Main.run()", line: 3, column: 1, source: { path: FILE } }] };
    if (command === "exceptionInfo") return exceptionInfo(args);
    if (command === "scopes") return { scopes: [] };
    return {};
  });
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

async function start(capabilities: DapCapabilities) {
  const store = useDebugSessionsStore();
  store.sessions.push(
    newSessionView({ id: sessionId, projectRoot: ROOT, configId: "c1", configName: "App", type: "java", noDebug: false, capabilities, startedAt: 1 })
  );
  store.selectSession(sessionId);
  wrapper = mount(DebugToolWindow, { props: { sessionId }, attachTo: document.body });
  await flushPromises();
  return store;
}

function emit(event: string, body: Record<string, unknown>) {
  debug.handler?.({ sessionId, event, body });
}

function exceptionRequests() {
  return debug.request.mock.calls.filter((call) => call[1] === "exceptionInfo");
}

function bar() {
  return wrapper!.find(".debug-window__exception");
}

function session() {
  return useDebugSessionsStore().sessions.find((view) => view.id === sessionId);
}

describe("DebugExceptionBar", () => {
  it("shows the exception type and description when paused on an exception", async () => {
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();

    expect(exceptionRequests()).toEqual([[sessionId, "exceptionInfo", { threadId: 1 }]]);
    expect(bar().exists()).toBe(true);
    expect(bar().text()).toContain("NullPointerException");
    expect(bar().text()).toContain(NPE.description);
    expect(wrapper!.find(".debug-window__header").element.nextElementSibling).toBe(bar().element);
  });

  it("shows the new exception when the same thread stops on another one without running on in between", async () => {
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();
    expect(bar().text()).toContain("NullPointerException");
    exceptionInfo = async () => ({ exceptionId: "IllegalStateException", description: "second" });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();
    expect(exceptionRequests()).toHaveLength(2);
    expect(bar().text()).toContain("IllegalStateException");
    expect(bar().text()).not.toContain("NullPointerException");
  });

  it("expands the stack trace on request", async () => {
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();

    expect(wrapper!.find(".debug-window__exception-stack").exists()).toBe(false);
    const toggle = bar().find(".debug-window__exception-toggle");
    expect(toggle.exists()).toBe(true);
    await toggle.trigger("click");
    expect(wrapper!.find(".debug-window__exception-stack").text()).toContain("at Main.main(Main.java:9)");
  });

  it("labels the toggle by what it does next", async () => {
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();

    const toggle = bar().find(".debug-window__exception-toggle");
    expect(toggle.text()).toBe("查看堆栈");
    expect(toggle.attributes("aria-expanded")).toBe("false");
    await toggle.trigger("click");
    expect(toggle.text()).toBe("收起堆栈");
    expect(toggle.attributes("aria-expanded")).toBe("true");
    await toggle.trigger("click");
    expect(toggle.text()).toBe("查看堆栈");
    expect(wrapper!.find(".debug-window__exception-stack").exists()).toBe(false);
  });

  it("does not repeat the type when the description starts with it", async () => {
    exceptionInfo = async () => ({ exceptionId: "pkg.BadThing", description: "pkg.BadThing: it broke" });
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();

    expect(bar().find(".debug-window__exception-description").text()).toBe("it broke");
  });

  it("unwraps a quoted description before dropping the repeated type", async () => {
    exceptionInfo = async () => ({ exceptionId: "pkg.BadThing", description: "\"pkg.BadThing: Unknown context name : [a[b]]\"" });
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();

    expect(bar().find(".debug-window__exception-description").text()).toBe("Unknown context name : [a[b]]");
  });

  it("keeps quotes that are part of the message", async () => {
    exceptionInfo = async () => ({ exceptionId: "pkg.BadThing", description: "pkg.BadThing: \"x\" is null" });
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();

    expect(bar().find(".debug-window__exception-description").text()).toBe("\"x\" is null");
  });

  it("shows no description when it is only the type and its prefix", async () => {
    exceptionInfo = async () => ({ exceptionId: "pkg.BadThing", description: "pkg.BadThing: " });
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();

    expect(bar().text()).toContain("pkg.BadThing");
    expect(bar().find(".debug-window__exception-description").exists()).toBe(false);
  });

  it("offers no stack trace when the adapter gives none", async () => {
    exceptionInfo = async () => ({ exceptionId: "ValueError", description: "bad value" });
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();

    expect(bar().text()).toContain("ValueError");
    expect(bar().find(".debug-window__exception-toggle").exists()).toBe(false);
  });

  it("hides the bar once the program continues", async () => {
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();
    expect(bar().exists()).toBe(true);

    emit("continued", { threadId: 1, allThreadsContinued: true });
    await flushPromises();
    expect(bar().exists()).toBe(false);
  });

  it("does not ask when the adapter does not support exceptionInfo", async () => {
    await start({});
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();
    expect(exceptionRequests()).toHaveLength(0);
    expect(bar().exists()).toBe(false);
  });

  it("does not ask when the stop is not an exception", async () => {
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "breakpoint", threadId: 1 });
    await flushPromises();
    expect(exceptionRequests()).toHaveLength(0);
    expect(bar().exists()).toBe(false);
  });

  it("shows nothing and sets no session error when the request fails", async () => {
    exceptionInfo = async () => {
      throw new Error("exceptionInfo failed");
    };
    const store = await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();

    expect(exceptionRequests()).toHaveLength(1);
    expect(bar().exists()).toBe(false);
    expect(session()?.lastError).toBeUndefined();
    expect(wrapper!.find(".debug-window__error").exists()).toBe(false);
    await expect(store.loadExceptionInfo(sessionId, 1)).resolves.toBeNull();
    expect(session()?.lastError).toBeUndefined();
  });

  it("drops an answer that arrives after the program continued", async () => {
    let answer: (value: unknown) => void = () => undefined;
    exceptionInfo = () => new Promise((resolve) => (answer = resolve));
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();
    expect(exceptionRequests()).toHaveLength(1);

    emit("continued", { threadId: 1, allThreadsContinued: true });
    await flushPromises();
    answer(NPE);
    await flushPromises();
    expect(bar().exists()).toBe(false);
  });

  it("shows only the latest exception when it stops again before the answer", async () => {
    const answers: Array<(value: unknown) => void> = [];
    exceptionInfo = () => new Promise((resolve) => answers.push(resolve));
    await start({ supportsExceptionInfoRequest: true });
    emit("stopped", { reason: "exception", threadId: 1 });
    await flushPromises();
    emit("stopped", { reason: "exception", threadId: 2 });
    await flushPromises();
    expect(exceptionRequests().map((call) => call[2])).toEqual([{ threadId: 1 }, { threadId: 2 }]);

    answers[1]!({ exceptionId: "IllegalStateException", description: "second" });
    await flushPromises();
    answers[0]!(NPE);
    await flushPromises();
    expect(bar().text()).toContain("IllegalStateException");
    expect(bar().text()).not.toContain("NullPointerException");
  });
});
