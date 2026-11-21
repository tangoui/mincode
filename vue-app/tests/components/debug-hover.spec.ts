import { describe, expect, it, vi } from "vitest";
import { newSessionView, type DebugSessionView } from "@/domain/debug/session-model";

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => false,
  canInvoke: () => false,
  invokeBackend: vi.fn(),
  listenAppEvent: vi.fn(() => () => undefined),
}));

import { DEBUG_INSPECT_COMMAND, provideDebugHover, type DebugHoverDeps } from "@/composables/use-debug-hover";

function session(change: Partial<DebugSessionView> = {}): DebugSessionView {
  return {
    ...newSessionView({ id: "s1", projectRoot: "/p", configId: "c", configName: "App", type: "python", noDebug: false, capabilities: {}, startedAt: 1 }),
    state: "paused",
    selectedFrameId: 11,
    ...change,
  };
}

function deps(view: DebugSessionView | null, change: Partial<DebugHoverDeps> = {}): DebugHoverDeps {
  return {
    session: () => view,
    evaluate: vi.fn().mockResolvedValue({ result: "3", type: "int", variablesReference: 0 }),
    loadVariables: vi.fn().mockResolvedValue([]),
    caseInsensitive: () => false,
    inspectLabel: () => "Inspect",
    moreLabel: (count) => `${count} more`,
    ...change,
  };
}

const line = "total = order.items + count";
const model = (path = "/p/src/app.py", scheme = "file") => ({ uri: { scheme, path }, getLineContent: () => line });
const at = (word: string) => ({ lineNumber: 4, column: line.indexOf(word) + 2 });

describe("debug hover", () => {
  it("returns null unless the selected session is paused and the file is in its project", async () => {
    expect(await provideDebugHover(deps(null), model(), at("count"))).toBeNull();
    expect(await provideDebugHover(deps(session({ state: "running" })), model(), at("count"))).toBeNull();
    expect(await provideDebugHover(deps(session()), model("/other/app.py"), at("count"))).toBeNull();
    expect(await provideDebugHover(deps(session()), model("/p/app.py", "app"), at("count"))).toBeNull();
    const failing = deps(session(), { evaluate: vi.fn().mockRejectedValue(new Error("NameError")) });
    expect(await provideDebugHover(failing, model(), at("count"))).toBeNull();
  });

  it("evaluates the member chain with the hover context", async () => {
    const hoverDeps = deps(session());
    const hover = await provideDebugHover(hoverDeps, model(), at("items"));
    expect(hoverDeps.evaluate).toHaveBeenCalledWith("s1", "order.items", "hover");
    expect(hover?.range).toEqual({ startLineNumber: 4, startColumn: line.indexOf("order") + 1, endLineNumber: 4, endColumn: line.indexOf("items") + 6 });
    expect(hover?.contents[0]!.value).toContain("order.items = {int} 3");
    expect(hover?.contents).toHaveLength(1);
  });

  it("lists a structured value's children and links to Evaluate", async () => {
    const children = Array.from({ length: 12 }, (_, index) => ({ name: `[${index}]`, value: String(index), variablesReference: 0 }));
    const hoverDeps = deps(session(), {
      evaluate: vi.fn().mockResolvedValue({ result: "[...]", variablesReference: 5 }),
      loadVariables: vi.fn().mockResolvedValue(children),
    });
    const hover = await provideDebugHover(hoverDeps, model(), at("order"));
    expect(hoverDeps.loadVariables).toHaveBeenCalledWith("s1", 5);
    expect(hover?.contents[0]!.value).toContain("  [9] = 9");
    expect(hover?.contents[0]!.value).not.toContain("[10] = 10");
    expect(hover?.contents[0]!.value).toContain("2 more");
    expect(hover?.contents[1]!.value).toContain(`command:${DEBUG_INSPECT_COMMAND}?${encodeURIComponent(JSON.stringify(["order"]))}`);
    expect(hover?.contents[1]!.isTrusted).toEqual({ enabledCommands: [DEBUG_INSPECT_COMMAND] });
  });

  it("drops the result when the program moved on meanwhile", async () => {
    let view: DebugSessionView | null = session();
    const hoverDeps = deps(null, {
      session: () => view,
      evaluate: vi.fn().mockImplementation(async () => {
        view = session({ state: "running", selectedFrameId: undefined });
        return { result: "3", variablesReference: 0 };
      }),
    });
    expect(await provideDebugHover(hoverDeps, model(), at("count"))).toBeNull();
  });
});
