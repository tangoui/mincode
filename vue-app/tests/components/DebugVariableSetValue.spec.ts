// While paused, a variable of the Variables pane can be given a new value
// (DAP setVariable) when the adapter supports it: double-click the value or
// "Set Value" in its context menu, Enter sets it, Esc cancels. A value the
// adapter rejects keeps the input open and shows the adapter's message; after
// a change the pane is read again (watches, other rows) keeping what is
// expanded, and the editor's inline values are read again.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h, ref } from "vue";
import type { DapCapabilities, DapVariable, DebugEvent } from "@/domain/debug/types";
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

import DebugVariablesPane from "@/components/debug/DebugVariablesPane.vue";
import DebugVariableTree from "@/components/debug/DebugVariableTree.vue";
import { useProjectEditorDebug } from "@/composables/use-project-editor-debug";
import { variableNode } from "@/domain/debug/variable-tree";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/p";
const FILE = "/p/Main.java";
const LOCAL = 5;
const INVALID = "Failed to set variable. Reason: abc is not a valid int";

let wrapper: VueWrapper | undefined;
let sessionSeq = 0;
let sessionId = "";
/** The Local scope's variables; setVariable changes them like the adapter would. */
let locals: DapVariable[] = [];
/** While set, `scopes` requests wait for it (a refresh that has not landed yet). */
let scopesGate: Promise<void> | null = null;
const children: Record<number, DapVariable[]> = {
  8: [{ name: "0", value: '"old"', variablesReference: 0 }],
  9: [{ name: "0", value: '"new"', variablesReference: 0 }],
};

beforeEach(() => {
  sessionSeq += 1;
  sessionId = `set-value-${sessionSeq}`;
  scopesGate = null;
  locals = [
    { name: "total", value: "40", type: "int", variablesReference: 0 },
    { name: "name", value: '"Ann"', type: "String", variablesReference: 0 },
  ];
  debug.message.error.mockReset();
  debug.request.mockReset();
  debug.request.mockImplementation(async (_id: string, command: string, args: { variablesReference?: number; name?: string; value?: string }) => {
    if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
    if (command === "stackTrace") return { stackFrames: [{ id: 11, name: "Main.run()", line: 3, column: 1, source: { path: FILE } }] };
    if (command === "scopes") {
      if (scopesGate) await scopesGate;
      return { scopes: [{ name: "Local", variablesReference: LOCAL, expensive: false }] };
    }
    if (command === "variables") {
      if (args.variablesReference === LOCAL) return { variables: locals.map((item) => ({ ...item })) };
      return { variables: children[args.variablesReference ?? 0] ?? [] };
    }
    if (command === "evaluate") {
      const found = locals.find((item) => item.name === (args as { expression?: string }).expression);
      return { result: found?.value ?? "?", type: found?.type, variablesReference: found?.variablesReference ?? 0 };
    }
    if (command === "setVariable") {
      if (args.value === "abc") throw new Error(`dap setVariable failed: ${INVALID}`);
      const reference = args.name === "list" ? 9 : 0;
      locals = locals.map((item) => (item.name === args.name ? { ...item, value: args.value ?? "", variablesReference: reference } : item));
      return { value: args.value, type: args.name === "list" ? "ArrayList" : "int", variablesReference: reference };
    }
    return {};
  });
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

function addSession(capabilities: DapCapabilities) {
  const store = useDebugSessionsStore();
  store.sessions.push({
    ...newSessionView({ id: sessionId, projectRoot: ROOT, configId: "c1", configName: "App", type: "java", noDebug: false, capabilities, startedAt: 1 }),
    state: "paused",
    threads: [{ id: 1, name: "main" }],
    selectedThreadId: 1,
    stoppedThreadId: 1,
    frames: [{ id: 11, name: "Main.run()", line: 3, column: 1, source: { path: FILE } }],
    selectedFrameId: 11,
  });
  store.selectSession(sessionId);
  return store;
}

async function mountPane() {
  const store = useDebugSessionsStore();
  wrapper = mount(
    defineComponent({
      setup: () => () => h(DebugVariablesPane, { session: store.sessions.find((view) => view.id === sessionId)! }),
    }),
    { attachTo: document.body }
  );
  await flushPromises();
}

function row(path: string) {
  return wrapper!.find(`.el-tree-node[data-key="${path}"] > .el-tree-node__content`);
}

function editor() {
  return wrapper!.find(".debug-variables__edit input");
}

function isExpanded(path: string): boolean {
  return wrapper!.find(`.el-tree-node[data-key="${path}"]`).classes().includes("is-expanded");
}

function scopesRequests() {
  return debug.request.mock.calls.filter(([, command]) => command === "scopes").length;
}

function setVariableRequests() {
  return debug.request.mock.calls.filter(([, command]) => command === "setVariable");
}

async function startEdit(path: string) {
  await row(path).find(".debug-variables__value").trigger("dblclick");
  await flushPromises();
}

describe("setting a variable's value", () => {
  it("sets the value from a double-click on Enter, shows it and refreshes inline values", async () => {
    const store = addSession({ supportsSetVariable: true });
    await mountPane();
    const version = store.variablesVersion(sessionId);
    await startEdit("scope:Local/total");
    expect(editor().exists()).toBe(true);
    expect((editor().element as HTMLInputElement).value).toBe("40");

    await editor().setValue("41");
    await editor().trigger("keydown", { key: "Enter" });
    await flushPromises();

    expect(setVariableRequests()).toEqual([[sessionId, "setVariable", { variablesReference: LOCAL, name: "total", value: "41" }]]);
    expect(editor().exists()).toBe(false);
    expect(row("scope:Local/total").find(".debug-variables__value").text()).toBe("41");
    expect(store.variablesVersion(sessionId)).toBe(version + 1);
  });

  it("shows the new value at once, before the pane's refresh lands", async () => {
    addSession({ supportsSetVariable: true });
    await mountPane();
    let release!: () => void;
    scopesGate = new Promise((resolve) => (release = resolve));
    const scopesBefore = scopesRequests();
    await startEdit("scope:Local/total");
    await editor().setValue("41");
    await editor().trigger("keydown", { key: "Enter" });
    await flushPromises();

    expect(scopesRequests()).toBe(scopesBefore + 1);
    expect(editor().exists()).toBe(false);
    expect(row("scope:Local/total").find(".debug-variables__value").text()).toBe("41");
    release();
    await flushPromises();
    expect(row("scope:Local/total").find(".debug-variables__value").text()).toBe("41");
  });

  it("reads watches and other rows again after a change, keeping what is expanded", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setWatches(ROOT, ["total"]);
    locals.push({ name: "list", value: "ArrayList size=1", type: "ArrayList", variablesReference: 8 });
    addSession({ supportsSetVariable: true });
    await mountPane();
    expect(row("watch:0:total").find(".debug-variables__value").text()).toBe("40");
    await row("scope:Local/list").trigger("click");
    await flushPromises();
    expect(row("scope:Local/list/0").text()).toContain('"old"');

    await startEdit("scope:Local/total");
    await editor().setValue("41");
    await editor().trigger("keydown", { key: "Enter" });
    await flushPromises();
    await flushPromises();

    expect(row("watch:0:total").find(".debug-variables__value").text()).toBe("41");
    expect(row("scope:Local/total").find(".debug-variables__value").text()).toBe("41");
    expect(isExpanded("scope:Local")).toBe(true);
    expect(isExpanded("scope:Local/list")).toBe(true);
    expect(row("scope:Local/list/0").text()).toContain('"old"');
  });

  it("reads an expanded object's children again under its new reference", async () => {
    locals.push({ name: "list", value: "ArrayList size=1", type: "ArrayList", variablesReference: 8 });
    addSession({ supportsSetVariable: true });
    await mountPane();
    await row("scope:Local/list").trigger("click");
    await flushPromises();
    expect(row("scope:Local/list/0").text()).toContain('"old"');

    await startEdit("scope:Local/list");
    await editor().setValue("other");
    await editor().trigger("keydown", { key: "Enter" });
    await flushPromises();
    await flushPromises();

    expect(setVariableRequests()).toEqual([[sessionId, "setVariable", { variablesReference: LOCAL, name: "list", value: "other" }]]);
    expect(row("scope:Local/list").find(".debug-variables__value").text()).toBe("other");
    expect(wrapper!.find('.el-tree-node[data-key="scope:Local/list"]').classes()).toContain("is-expanded");
    expect(row("scope:Local/list/0").text()).toContain('"new"');
  });

  it("opens the editor from the context menu", async () => {
    addSession({ supportsSetVariable: true });
    await mountPane();
    await row("scope:Local/name").trigger("contextmenu");
    await flushPromises();
    const item = document.body.querySelector<HTMLElement>(".debug-variables__menu .el-dropdown-menu__item");
    expect(item?.textContent).toMatch(/修改值|Set Value/);
    item!.click();
    await flushPromises();
    expect((editor().element as HTMLInputElement).value).toBe('"Ann"');
  });

  it("opens the context menu from the keyboard (Shift+F10 / ContextMenu key)", async () => {
    addSession({ supportsSetVariable: true });
    await mountPane();
    const item = wrapper!.find('.el-tree-node[data-key="scope:Local/name"]');
    (item.element as HTMLElement).focus();
    await item.trigger("keydown", { key: "F10", shiftKey: true });
    await flushPromises();
    const menuItem = document.body.querySelector<HTMLElement>(".debug-variables__menu .el-dropdown-menu__item");
    expect(menuItem?.textContent).toMatch(/修改值|Set Value/);
    menuItem!.click();
    await flushPromises();
    expect((editor().element as HTMLInputElement).value).toBe('"Ann"');
    // Disarms the guard a keyboard-opened menu leaves for the native contextmenu that follows.
    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));

    await editor().trigger("keydown", { key: "Escape" });
    await flushPromises();
    const scope = wrapper!.find('.el-tree-node[data-key="scope:Local"]');
    const plain = new KeyboardEvent("keydown", { key: "F10", bubbles: true, cancelable: true });
    scope.element.dispatchEvent(plain);
    await scope.trigger("keydown", { key: "ContextMenu" });
    await flushPromises();
    // A scope offers no menu, and a plain F10 is not claimed.
    expect(document.body.querySelector(".debug-variables__menu")).toBeNull();
    expect(plain.defaultPrevented).toBe(false);
  });

  it("cancels on Esc without a request", async () => {
    addSession({ supportsSetVariable: true });
    await mountPane();
    await startEdit("scope:Local/total");
    await editor().setValue("99");
    await editor().trigger("keydown", { key: "Escape" });
    await flushPromises();

    expect(editor().exists()).toBe(false);
    expect(setVariableRequests()).toHaveLength(0);
    expect(row("scope:Local/total").find(".debug-variables__value").text()).toBe("40");
  });

  it("keeps the input with the typed text and shows the adapter's message when the value is rejected", async () => {
    const store = addSession({ supportsSetVariable: true });
    await mountPane();
    const version = store.variablesVersion(sessionId);
    await startEdit("scope:Local/total");
    await editor().setValue("abc");
    await editor().trigger("keydown", { key: "Enter" });
    await flushPromises();

    expect(setVariableRequests()).toHaveLength(1);
    expect(debug.message.error).toHaveBeenCalledTimes(1);
    expect(String(debug.message.error.mock.calls[0]![0].message)).toContain(INVALID);
    expect(String(debug.message.error.mock.calls[0]![0].message)).not.toContain("dap setVariable failed");
    expect((editor().element as HTMLInputElement).value).toBe("abc");
    expect((editor().element as HTMLInputElement).disabled).toBe(false);
    expect(store.variablesVersion(sessionId)).toBe(version);

    await editor().trigger("keydown", { key: "Escape" });
    await flushPromises();
    expect(editor().exists()).toBe(false);
    expect(row("scope:Local/total").find(".debug-variables__value").text()).toBe("40");
    expect(setVariableRequests()).toHaveLength(1);
  });

  it("does not edit without the adapter's support", async () => {
    addSession({});
    await mountPane();
    await startEdit("scope:Local/total");
    expect(editor().exists()).toBe(false);
    await row("scope:Local/total").trigger("contextmenu");
    await flushPromises();
    expect(document.body.querySelector(".debug-variables__menu .el-dropdown-menu__item")).toBeNull();
  });

  it("does not edit scopes or watches", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setWatches(ROOT, ["total"]);
    addSession({ supportsSetVariable: true });
    await mountPane();
    await startEdit("watch:0:total");
    expect(editor().exists()).toBe(false);
    await row("scope:Local").trigger("dblclick");
    await flushPromises();
    expect(editor().exists()).toBe(false);
  });

  it("does not edit once the program runs again", async () => {
    addSession({ supportsSetVariable: true });
    await mountPane();
    await startEdit("scope:Local/total");
    expect(editor().exists()).toBe(true);

    debug.handler?.({ sessionId, event: "continued", body: { threadId: 1, allThreadsContinued: true } });
    await flushPromises();
    expect(useDebugSessionsStore().sessions.find((view) => view.id === sessionId)?.state).toBe("running");
    expect(editor().exists()).toBe(false);
    // The last stop's tree stays, greyed and read-only.
    expect(wrapper!.find(".debug-variable-tree").classes()).toContain("is-stale");
    await startEdit("scope:Local/total");
    expect(editor().exists()).toBe(false);
    expect(setVariableRequests()).toHaveLength(0);
  });

  it("does not edit in a read-only tree (the Evaluate dialog)", async () => {
    addSession({ supportsSetVariable: true });
    const node = variableNode({ name: "total", value: "40", variablesReference: 0 }, "result", 0, 9);
    wrapper = mount(DebugVariableTree, { props: { sessionId, nodes: [node], live: true, editable: false }, attachTo: document.body });
    await flushPromises();
    await startEdit("result/total");
    expect(editor().exists()).toBe(false);
  });
});

describe("inline values after a change", () => {
  it("are read again when the session's variables change", async () => {
    const store = addSession({ supportsSetVariable: true });
    let editorDebug!: ReturnType<typeof useProjectEditorDebug>;
    const activePath = ref(FILE);
    wrapper = mount(
      defineComponent({
        setup() {
          editorDebug = useProjectEditorDebug({
            activeDocument: () => ({ path: activePath.value, loaded: true }),
            projectRootForPath: () => ROOT,
            languageIdFor: () => "java",
            editorReady: () => false,
            active: () => true,
            ownsProject: () => true,
            openAt: vi.fn(),
            openVirtualSource: vi.fn(),
          });
          return () => h("div");
        },
      })
    );
    await flushPromises();
    expect(editorDebug.editorProps.value.inlineVariables).toEqual([
      { name: "total", value: "40" },
      { name: "name", value: '"Ann"' },
    ]);

    await store.setVariable(sessionId, LOCAL, "total", "41");
    await flushPromises();
    expect(editorDebug.editorProps.value.inlineVariables?.[0]).toEqual({ name: "total", value: "41" });
  });
});
