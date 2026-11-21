import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
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

import DebugEvaluateDialog from "@/components/debug/DebugEvaluateDialog.vue";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

let wrapper: ReturnType<typeof mount> | undefined;

beforeEach(() => {
  debug.request.mockReset();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  document.body.innerHTML = "";
});

function addPausedSession(change: Partial<DebugSessionView> = {}) {
  const store = useDebugSessionsStore();
  store.sessions.push({
    ...newSessionView({
      id: "s1",
      projectRoot: "/p",
      configId: "c1",
      configName: "App",
      type: "python",
      noDebug: false,
      capabilities: {},
      startedAt: 1,
    }),
    state: "paused",
    frames: [{ id: 11, name: "main", line: 3, column: 1, source: { name: "app.py", path: "/p/app.py" } }],
    selectedFrameId: 11,
    ...change,
  });
  store.selectSession("s1");
}

async function mountDialog(prefill?: string) {
  wrapper = mount(DebugEvaluateDialog, { props: { modelValue: true, prefill }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

function expressionInput(): HTMLTextAreaElement | HTMLInputElement {
  const input = document.body.querySelector<HTMLTextAreaElement | HTMLInputElement>(".debug-evaluate__expression textarea, .debug-evaluate__expression input");
  if (!input) throw new Error("expression input not found");
  return input;
}

async function pressEnter() {
  expressionInput().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  await flushPromises();
}

describe("DebugEvaluateDialog", () => {
  it("Evaluate dialog prefills selection and shows result tree", async () => {
    addPausedSession();
    debug.request.mockImplementation(async (_id: string, command: string, args: { variablesReference?: number }) => {
      if (command === "evaluate") return { result: "Order(id=7)", type: "Order", variablesReference: 9 };
      if (command === "variables" && args.variablesReference === 9) {
        return { variables: [{ name: "id", value: "7", type: "int", variablesReference: 0 }] };
      }
      return {};
    });
    await mountDialog("order");
    expect(expressionInput().value).toBe("order");

    await pressEnter();
    expect(debug.request).toHaveBeenCalledWith("s1", "evaluate", { expression: "order", context: "repl", frameId: 11 });
    const text = document.body.querySelector(".debug-evaluate__result")?.textContent ?? "";
    expect(text).toContain("order");
    expect(text).toContain("Order(id=7)");
    // The result node is expanded: its children are loaded as a variable tree.
    expect(debug.request).toHaveBeenCalledWith("s1", "variables", { variablesReference: 9 });
    expect(document.body.querySelector(".debug-evaluate__result")?.textContent).toContain("id");
    // The expression is remembered for this app session.
    expect(document.body.querySelector(".debug-evaluate__history")).not.toBeNull();
  });

  it("shows adapter error message", async () => {
    addPausedSession();
    debug.request.mockImplementation(async (_id: string, command: string) => {
      if (command === "evaluate") throw new Error("dap evaluate failed: NameError: name 'missing' is not defined");
      return {};
    });
    await mountDialog("missing");
    await pressEnter();
    const error = document.body.querySelector(".debug-evaluate__error");
    expect(error?.textContent).toContain("NameError: name 'missing' is not defined");
    expect(error?.textContent).not.toContain("dap evaluate failed");
    expect(document.body.querySelector(".debug-evaluate__result .el-tree")).toBeNull();
  });

  it("does not evaluate while the program runs", async () => {
    addPausedSession({ state: "running", frames: [], selectedFrameId: undefined });
    await mountDialog("x");
    await pressEnter();
    expect(debug.request).not.toHaveBeenCalled();
    expect(document.body.querySelector(".debug-evaluate__hint")).not.toBeNull();
  });
});
