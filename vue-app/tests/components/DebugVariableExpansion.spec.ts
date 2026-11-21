// The Variables pane keeps what the user expanded across steps: paths are
// remembered per session and method, restored on every stop (bounded), and a
// collapse forgets the path.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, effectScope, h } from "vue";
import type { DapVariable, DebugEvent } from "@/domain/debug/types";
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

import DebugVariablesPane from "@/components/debug/DebugVariablesPane.vue";
import { useDebugVariableExpansion } from "@/composables/use-debug-variable-expansion";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const METHOD = "Main.run(String[])";

/** One stop of the program: the Local scope's reference and every reference's children. */
interface Stop {
  local: number;
  children: Record<number, DapVariable[]>;
}

let wrapper: VueWrapper | undefined;
let sessionSeq = 0;
let sessionId = "";
let stop: Stop;
let frameId = 11;

function variable(name: string, variablesReference = 0, value = name): DapVariable {
  return { name, value, variablesReference };
}

beforeEach(() => {
  sessionSeq += 1;
  sessionId = `expansion-${sessionSeq}`;
  frameId = 11;
  debug.request.mockReset();
  debug.request.mockImplementation(async (_id: string, command: string, args: { variablesReference?: number }) => {
    if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
    if (command === "stackTrace") return { stackFrames: [{ id: frameId, name: METHOD, line: 3, column: 1 }] };
    if (command === "scopes") return { scopes: [{ name: "Local", variablesReference: stop.local, expensive: false }] };
    if (command === "variables") return { variables: stop.children[args.variablesReference ?? 0] ?? [] };
    return {};
  });
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

function addPausedSession() {
  const store = useDebugSessionsStore();
  store.sessions.push({
    ...newSessionView({ id: sessionId, projectRoot: "/p", configId: "c1", configName: "App", type: "java", noDebug: false, capabilities: {}, startedAt: 1 }),
    state: "paused",
    threads: [{ id: 1, name: "main" }],
    selectedThreadId: 1,
    stoppedThreadId: 1,
    frames: [{ id: frameId, name: METHOD, line: 3, column: 1 }],
    selectedFrameId: frameId,
  });
  store.selectSession(sessionId);
}

async function mountPane(first: Stop) {
  stop = first;
  const store = useDebugSessionsStore();
  wrapper = mount(
    defineComponent({
      setup: () => () => h(DebugVariablesPane, { session: store.sessions.find((view) => view.id === sessionId)! }),
    }),
    { attachTo: document.body }
  );
  await flushPromises();
}

/** The program steps: it runs, then stops again in the same method with new references. */
async function step(next: Stop) {
  stop = next;
  frameId += 1;
  debug.handler?.({ sessionId, event: "continued", body: { threadId: 1 } });
  debug.handler?.({ sessionId, event: "stopped", body: { reason: "step", threadId: 1 } });
  await flushPromises();
  await flushPromises();
}

function treeNode(path: string) {
  return wrapper!.find(`.el-tree-node[data-key="${path}"]`);
}

function isExpanded(path: string): boolean {
  return treeNode(path).classes().includes("is-expanded");
}

async function toggle(path: string) {
  await treeNode(path).find(".el-tree-node__content").trigger("click");
  await flushPromises();
}

function variablesRequests(reference?: number) {
  return debug.request.mock.calls.filter(
    ([, command, args]) => command === "variables" && (reference === undefined || (args as { variablesReference: number }).variablesReference === reference)
  );
}

describe("Variables pane expansion across steps", () => {
  it("keeps an expanded variable expanded after a step, showing its new children", async () => {
    addPausedSession();
    await mountPane({ local: 5, children: { 5: [variable("list", 7, "ArrayList size=1")], 7: [variable("0", 0, "a")] } });
    expect(isExpanded("scope:Local")).toBe(true);
    await toggle("scope:Local/list");
    expect(treeNode("scope:Local/list/0").text()).toContain("a");

    await step({ local: 15, children: { 15: [variable("list", 9, "ArrayList size=1")], 9: [variable("0", 0, "b")] } });
    expect(variablesRequests(9)).toHaveLength(1);
    expect(isExpanded("scope:Local/list")).toBe(true);
    expect(treeNode("scope:Local/list/0").exists()).toBe(true);
    expect(treeNode("scope:Local/list/0").text()).toContain("b");
  });

  it("expands two variables of the same object independently", async () => {
    addPausedSession();
    await mountPane({ local: 5, children: { 5: [variable("a", 7), variable("b", 7)], 7: [variable("0")] } });
    await toggle("scope:Local/a");
    expect(isExpanded("scope:Local/a")).toBe(true);
    expect(isExpanded("scope:Local/b")).toBe(false);

    await step({ local: 15, children: { 15: [variable("a", 9), variable("b", 9)], 9: [variable("0")] } });
    expect(isExpanded("scope:Local/a")).toBe(true);
    expect(isExpanded("scope:Local/b")).toBe(false);
    expect(variablesRequests(9)).toHaveLength(1);
  });

  it("survives a variable leaving scope and restores it when it is back", async () => {
    addPausedSession();
    await mountPane({ local: 5, children: { 5: [variable("list", 7)], 7: [variable("0")] } });
    await toggle("scope:Local/list");

    await step({ local: 15, children: { 15: [variable("y")] } });
    expect(treeNode("scope:Local/y").exists()).toBe(true);
    expect(treeNode("scope:Local/list").exists()).toBe(false);
    expect(useDebugSessionsStore().sessions[0]!.lastError).toBeUndefined();

    await step({ local: 25, children: { 25: [variable("list", 11)], 11: [variable("0")] } });
    expect(variablesRequests(11)).toHaveLength(1);
    expect(isExpanded("scope:Local/list")).toBe(true);
    expect(treeNode("scope:Local/list/0").exists()).toBe(true);
  });

  it("records an expand on the old tree, while another frame's variables load, for the old frame's method", async () => {
    const CALLER = "Main.caller()";
    addPausedSession();
    const store = useDebugSessionsStore();
    const view = store.sessions.find((item) => item.id === sessionId)!;
    view.frames = [{ id: frameId, name: METHOD, line: 3, column: 1 }, { id: 99, name: CALLER, line: 9, column: 1 }];
    await mountPane({ local: 5, children: { 5: [variable("list", 7)], 7: [variable("0")] } });
    let answer: (value: unknown) => void = () => undefined;
    const base = debug.request.getMockImplementation()!;
    debug.request.mockImplementation(async (id: string, command: string, args: { frameId?: number; variablesReference?: number }) => {
      if (command === "scopes" && args.frameId === 99) return new Promise((resolve) => (answer = resolve));
      if (command === "variables" && args.variablesReference === 15) return { variables: [variable("list", 17)] };
      return base(id, command, args);
    });
    store.selectFrame(sessionId, 99);
    await flushPromises();
    // The old tree is still shown: expanding there is about METHOD.
    await toggle("scope:Local/list");
    answer({ scopes: [{ name: "Local", variablesReference: 15, expensive: false }] });
    await flushPromises();
    await flushPromises();
    expect(treeNode("scope:Local/list").exists()).toBe(true);
    expect(isExpanded("scope:Local/list")).toBe(false);
    // Back in METHOD the expand is there.
    store.selectFrame(sessionId, frameId);
    await flushPromises();
    await flushPromises();
    expect(isExpanded("scope:Local/list")).toBe(true);
  });

  it("forgets a session's expanded paths when it ends, with no pane mounted", async () => {
    addPausedSession();
    const store = useDebugSessionsStore();
    const scope = effectScope();
    scope.run(() => {
      const expansion = useDebugVariableExpansion(() => sessionId, () => METHOD);
      expansion.markExpanded("scope:Local/list");
    });
    scope.stop();
    expect(store.variableExpansionFor(sessionId).size).toBe(1);
    expect(store.hasVariableExpansion(sessionId)).toBe(true);
    debug.handler?.({ sessionId, event: "mincode.sessionEnded", body: {} });
    await flushPromises();
    expect(store.sessions.find((view) => view.id === sessionId)?.state).toBe("ended");
    expect(store.hasVariableExpansion(sessionId)).toBe(false);
    expect(store.variableExpansionFor(sessionId).size).toBe(0);
  });

  it("restores at most 64 nodes per refresh", async () => {
    addPausedSession();
    const many = Array.from({ length: 100 }, (_item, index) => variable(`v${index}`, 1000 + index));
    const scope = effectScope();
    scope.run(() => {
      const expansion = useDebugVariableExpansion(() => sessionId, () => METHOD);
      expansion.markExpanded("scope:Local");
      for (const item of many) expansion.markExpanded(`scope:Local/${item.name}`);
    });
    scope.stop();
    await mountPane({ local: 5, children: { 5: many } });
    expect(variablesRequests()).toHaveLength(64);
  });

  it("restores at most 8 levels deep", async () => {
    addPausedSession();
    const children: Record<number, DapVariable[]> = { 5: [variable("d1", 101)] };
    const paths = ["scope:Local"];
    let parent = "scope:Local";
    for (let depth = 1; depth <= 12; depth += 1) {
      children[100 + depth] = [variable(`d${depth + 1}`, 101 + depth)];
      parent = `${parent}/d${depth}`;
      paths.push(parent);
    }
    const scope = effectScope();
    scope.run(() => {
      const expansion = useDebugVariableExpansion(() => sessionId, () => METHOD);
      for (const path of paths) expansion.markExpanded(path);
    });
    scope.stop();
    await mountPane({ local: 5, children });
    // Levels 1 (the scope) to 8 (d7) are expanded; d8 stays collapsed.
    expect(variablesRequests()).toHaveLength(8);
    expect(isExpanded("scope:Local/d1/d2/d3/d4/d5/d6/d7")).toBe(true);
    expect(isExpanded("scope:Local/d1/d2/d3/d4/d5/d6/d7/d8")).toBe(false);
  });

  it("does not expand a collapsed variable again", async () => {
    addPausedSession();
    await mountPane({ local: 5, children: { 5: [variable("list", 7)], 7: [variable("0")] } });
    await toggle("scope:Local/list");
    expect(isExpanded("scope:Local/list")).toBe(true);
    await toggle("scope:Local/list");
    expect(isExpanded("scope:Local/list")).toBe(false);

    await step({ local: 15, children: { 15: [variable("list", 9)], 9: [variable("0")] } });
    expect(isExpanded("scope:Local")).toBe(true);
    expect(isExpanded("scope:Local/list")).toBe(false);
    expect(variablesRequests(9)).toHaveLength(0);
  });

  it("starts with the first cheap scope expanded after a first stop whose scopes failed to load", async () => {
    const scope = effectScope();
    let expansion: ReturnType<typeof useDebugVariableExpansion> | undefined;
    scope.run(() => {
      expansion = useDebugVariableExpansion(() => sessionId, () => METHOD);
    });
    // The first stop's scopes request failed: nothing to seed from yet.
    expect(expansion!.pathsToRestore([], [])).toEqual([]);
    // The next stop lists the scopes: the method still gets its default.
    expect(expansion!.pathsToRestore(["scope:Local"], ["scope:Local"])).toEqual(["scope:Local"]);
    scope.stop();
  });
});

function scrollWrap(): HTMLElement {
  return wrapper!.find(".debug-variables__tree .el-scrollbar__wrap").element as HTMLElement;
}

/** The user scrolls the Variables pane. */
function scrollTo(top: number) {
  const wrap = scrollWrap();
  wrap.scrollTop = top;
  wrap.dispatchEvent(new Event("scroll"));
}

describe("Variables pane scroll position across steps", () => {
  it("keeps the tree (greyed) while the program runs", async () => {
    addPausedSession();
    await mountPane({ local: 5, children: { 5: [variable("list", 7)], 7: [variable("0")] } });
    const scrollbar = wrapper!.find(".debug-variables__tree").element;
    expect(wrapper!.find(".debug-variables__tree .el-tree").classes()).not.toContain("is-stale");

    debug.handler?.({ sessionId, event: "continued", body: { threadId: 1 } });
    await flushPromises();
    expect(wrapper!.find(".debug-variables__tree").element).toBe(scrollbar);
    expect(treeNode("scope:Local/list").exists()).toBe(true);
    expect(wrapper!.find(".debug-variables__tree .el-tree").classes()).toContain("is-stale");
    expect(wrapper!.find(".debug-pane-empty").exists()).toBe(false);
    // Its nodes no longer expand or collapse: their references are gone.
    await toggle("scope:Local");
    expect(isExpanded("scope:Local")).toBe(true);

    stop = { local: 15, children: { 15: [variable("list", 9)], 9: [variable("0")] } };
    frameId += 1;
    debug.handler?.({ sessionId, event: "stopped", body: { reason: "step", threadId: 1 } });
    await flushPromises();
    await flushPromises();
    expect(wrapper!.find(".debug-variables__tree").element).toBe(scrollbar);
    expect(wrapper!.find(".debug-variables__tree .el-tree").classes()).not.toContain("is-stale");
  });

  it("scrolls back to where the user was once the expanded nodes are restored after a step", async () => {
    addPausedSession();
    await mountPane({ local: 5, children: { 5: [variable("list", 7)], 7: [variable("0")] } });
    await toggle("scope:Local/list");
    scrollTo(120);

    const request = debug.request.getMockImplementation()!;
    debug.request.mockImplementation(async (id: string, command: string, args: { variablesReference?: number }) => {
      // The new tree is shorter until its nodes are expanded again: the browser clamps.
      if (command === "variables" && args.variablesReference === 9) scrollTo(0);
      return request(id, command, args);
    });
    await step({ local: 15, children: { 15: [variable("list", 9)], 9: [variable("0")] } });
    expect(isExpanded("scope:Local/list")).toBe(true);
    expect(scrollWrap().scrollTop).toBe(120);
  });

  it("restores the position after a value is set", async () => {
    addPausedSession();
    await mountPane({ local: 5, children: { 5: [variable("list", 7)], 7: [variable("0")] } });
    await toggle("scope:Local/list");
    scrollTo(80);
    const request = debug.request.getMockImplementation()!;
    debug.request.mockImplementation(async (id: string, command: string, args: { variablesReference?: number }) => {
      if (command === "variables" && args.variablesReference === 7) scrollTo(0);
      return request(id, command, args);
    });
    useDebugSessionsStore().bumpVariables(sessionId);
    await flushPromises();
    await flushPromises();
    expect(variablesRequests(7).length).toBeGreaterThan(1);
    expect(scrollWrap().scrollTop).toBe(80);
  });

  it("opens a method shown for the first time at the top", async () => {
    addPausedSession();
    await mountPane({ local: 5, children: { 5: [variable("list", 7)], 7: [variable("0")] } });
    scrollTo(60);
    stop = { local: 15, children: { 15: [variable("x")] } };
    frameId += 1;
    debug.request.mockImplementation(async (_id: string, command: string, args: { variablesReference?: number }) => {
      if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
      if (command === "stackTrace") return { stackFrames: [{ id: frameId, name: "Main.other()", line: 9, column: 1 }] };
      if (command === "scopes") return { scopes: [{ name: "Local", variablesReference: stop.local, expensive: false }] };
      if (command === "variables") return { variables: stop.children[args.variablesReference ?? 0] ?? [] };
      return {};
    });
    debug.handler?.({ sessionId, event: "continued", body: { threadId: 1 } });
    debug.handler?.({ sessionId, event: "stopped", body: { reason: "step", threadId: 1 } });
    await flushPromises();
    await flushPromises();
    expect(treeNode("scope:Local/x").exists()).toBe(true);
    expect(scrollWrap().scrollTop).toBe(0);
  });
});
