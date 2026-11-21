// Field breakpoints (DAP data breakpoints) while paused, with an adapter that
// declares supportsDataBreakpoints: a field's context menu in the Variables
// pane offers "Break When Value Changes" (and "… Is Read" when the adapter
// lists read access), asked lazily with dataBreakpointInfo when the menu
// opens. Choosing one sends every current field breakpoint of the session
// (setDataBreakpoints). One the adapter declares canPersist is saved for the
// project and debugger type; any other one lives in its session only. The
// breakpoints dialog lists them and removes them. Nothing is offered or
// asked while the program runs.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
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

import DebugBreakpointsDialog from "@/components/debug/DebugBreakpointsDialog.vue";
import DebugVariablesPane from "@/components/debug/DebugVariablesPane.vue";
import { useVueI18n } from "@/i18n/messages";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/p";
const FILE = "/p/DataApp.java";
const LOCAL = 5;
const COUNTER = 7;

// The answers java-debug 0.53.1 gives (TestMachineSmokeJavaIdea2DataBreakpoints),
// plus a field of an adapter that lists no access types and cannot persist.
const COUNT_INFO = { dataId: "Counter#count", description: "Counter.count : int", accessTypes: ["read", "write", "readWrite"], canPersist: true };
const LABEL_INFO = { dataId: "Counter#label", description: "Counter.label : String" };

let wrapper: VueWrapper | undefined;
let dialog: VueWrapper | undefined;
let sessionSeq = 0;
let sessionId = "";
const locals: DapVariable[] = [
  { name: "c", value: "Counter@9", type: "Counter", variablesReference: COUNTER },
  { name: "local", value: "1", type: "int", variablesReference: 0 },
];
const fields: DapVariable[] = [
  { name: "count", value: "0", type: "int", variablesReference: 0 },
  { name: "label", value: '"a"', type: "String", variablesReference: 0 },
];

beforeEach(() => {
  sessionSeq += 1;
  sessionId = `data-${sessionSeq}`;
  for (const mock of Object.values(debug.message)) mock.mockReset();
  debug.request.mockReset();
  debug.request.mockImplementation(async (_id: string, command: string, args: { variablesReference?: number; name?: string; breakpoints?: unknown[] }) => {
    if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
    if (command === "stackTrace") return { stackFrames: [{ id: 11, name: "DataApp.main()", line: 5, column: 1, source: { path: FILE } }] };
    if (command === "scopes") return { scopes: [{ name: "Local", variablesReference: LOCAL, expensive: false }] };
    if (command === "variables") return { variables: (args.variablesReference === LOCAL ? locals : args.variablesReference === COUNTER ? fields : []).map((item) => ({ ...item })) };
    if (command === "dataBreakpointInfo") {
      if (args.variablesReference === COUNTER && args.name === "count") return COUNT_INFO;
      if (args.variablesReference === COUNTER && args.name === "label") return LABEL_INFO;
      // java-debug answers a variable that is no field without a body.
      return null;
    }
    if (command === "setDataBreakpoints") return { breakpoints: (args.breakpoints ?? []).map((_, index) => ({ id: index + 1, verified: true, line: 0 })) };
    return {};
  });
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  dialog?.unmount();
  dialog = undefined;
  document.body.innerHTML = "";
});

function addSession(capabilities: DapCapabilities, paused = true) {
  const store = useDebugSessionsStore();
  const view = newSessionView({ id: sessionId, projectRoot: ROOT, configId: "c1", configName: "App", type: "java", noDebug: false, capabilities, startedAt: 1 });
  store.sessions.push(
    paused
      ? {
          ...view,
          state: "paused",
          threads: [{ id: 1, name: "main" }],
          selectedThreadId: 1,
          stoppedThreadId: 1,
          stoppedReason: "breakpoint",
          frames: [{ id: 11, name: "DataApp.main()", line: 5, column: 1, source: { path: FILE } }],
          selectedFrameId: 11,
        }
      : view,
  );
  store.selectSession(sessionId);
  return store;
}

function emit(event: DebugEvent) {
  debug.handler?.(event);
}

async function mountPane() {
  const store = useDebugSessionsStore();
  wrapper = mount(
    defineComponent({
      setup: () => () => {
        const view = store.sessions.find((item) => item.id === sessionId);
        return view ? h(DebugVariablesPane, { session: view }) : null;
      },
    }),
    { attachTo: document.body },
  );
  await flushPromises();
}

function row(path: string) {
  return wrapper!.find(`.el-tree-node[data-key="${path}"] > .el-tree-node__content`);
}

async function expandCounter() {
  await row("scope:Local/c").trigger("click");
  await flushPromises();
  expect(row("scope:Local/c/count").exists()).toBe(true);
}

async function openMenu(path: string) {
  await row(path).trigger("contextmenu");
  await flushPromises();
}

function menuItems(): string[] {
  return [...document.body.querySelectorAll<HTMLElement>(".debug-variables__menu .el-dropdown-menu__item")].map((item) => item.textContent?.trim() ?? "");
}

function menuItem(text: string): HTMLElement {
  const item = [...document.body.querySelectorAll<HTMLElement>(".debug-variables__menu .el-dropdown-menu__item")].find(
    (element) => element.textContent?.trim() === text,
  );
  expect(item, text).toBeDefined();
  return item!;
}

function requests(command: string) {
  return debug.request.mock.calls.filter(([, name]) => name === command);
}

async function openDialog() {
  dialog = mount(DebugBreakpointsDialog, { props: { modelValue: true, projectRoot: ROOT }, attachTo: document.body });
  await flushPromises();
}

function dataRows(): HTMLElement[] {
  return [...document.body.querySelectorAll<HTMLElement>(".debug-breakpoints-dialog__data-item")];
}

describe("field breakpoints from the variables pane", () => {
  it("offers both access types the adapter lists on a field, asking dataBreakpointInfo only when the menu opens", async () => {
    const { t } = useVueI18n();
    expect(t("debug.variables.watchWrite")).toBe("字段被修改时暂停");
    expect(t("debug.variables.watchRead")).toBe("字段被访问时暂停");
    addSession({ supportsDataBreakpoints: true });
    await mountPane();
    await expandCounter();
    expect(requests("dataBreakpointInfo")).toEqual([]);
    await openMenu("scope:Local/c/count");
    expect(requests("dataBreakpointInfo")).toEqual([[sessionId, "dataBreakpointInfo", { variablesReference: COUNTER, name: "count" }]]);
    expect(menuItems()).toEqual(["字段被修改时暂停", "字段被访问时暂停"]);
  });

  it("opens the field breakpoint menu from the keyboard", async () => {
    addSession({ supportsDataBreakpoints: true });
    await mountPane();
    await expandCounter();
    const item = wrapper!.find('.el-tree-node[data-key="scope:Local/c/count"]');
    await item.trigger("keydown", { key: "ContextMenu" });
    await flushPromises();
    expect(requests("dataBreakpointInfo")).toEqual([[sessionId, "dataBreakpointInfo", { variablesReference: COUNTER, name: "count" }]]);
    expect(menuItems()).toEqual(["字段被修改时暂停", "字段被访问时暂停"]);
    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
  });

  it("offers only the write access when the adapter lists no access types", async () => {
    addSession({ supportsDataBreakpoints: true });
    await mountPane();
    await expandCounter();
    await openMenu("scope:Local/c/label");
    expect(menuItems()).toEqual(["字段被修改时暂停"]);
  });

  it("offers nothing for a variable without a field id; Set Value stays", async () => {
    addSession({ supportsDataBreakpoints: true });
    await mountPane();
    await openMenu("scope:Local/local");
    expect(requests("dataBreakpointInfo")).toHaveLength(1);
    expect(menuItems()).toEqual([]);
    wrapper!.unmount();
    wrapper = undefined;
    document.body.innerHTML = "";
    sessionId = `${sessionId}-edit`;
    addSession({ supportsDataBreakpoints: true, supportsSetVariable: true });
    await mountPane();
    await openMenu("scope:Local/local");
    expect(menuItems()).toEqual(["修改值"]);
    await expandCounter();
    await openMenu("scope:Local/c/count");
    expect(menuItems()).toEqual(["修改值", "字段被修改时暂停", "字段被访问时暂停"]);
  });

  it("offers and asks nothing without the adapter's capability", async () => {
    addSession({});
    await mountPane();
    await expandCounter();
    await openMenu("scope:Local/c/count");
    expect(requests("dataBreakpointInfo")).toEqual([]);
    expect(menuItems()).toEqual([]);
  });

  it("offers and asks nothing while the program runs, and drops an answer that comes after it ran on", async () => {
    const store = addSession({ supportsDataBreakpoints: true });
    await mountPane();
    await expandCounter();
    let answer!: (body: unknown) => void;
    debug.request.mockImplementationOnce(() => new Promise((resolve) => (answer = resolve)));
    await openMenu("scope:Local/c/count");
    emit({ sessionId, event: "continued", body: { threadId: 1 } });
    answer(COUNT_INFO);
    await flushPromises();
    expect(menuItems()).toEqual([]);
    expect(store.sessions.find((view) => view.id === sessionId)!.state).toBe("running");
    debug.request.mockClear();
    await openMenu("scope:Local/c/count");
    expect(requests("dataBreakpointInfo")).toEqual([]);
    expect(menuItems()).toEqual([]);
    expect(requests("setDataBreakpoints")).toEqual([]);
  });

  it("drops a slow answer once the user pressed elsewhere, so the menu does not pop up later", async () => {
    addSession({ supportsDataBreakpoints: true });
    await mountPane();
    await expandCounter();
    let answer!: (body: unknown) => void;
    debug.request.mockImplementationOnce(() => new Promise((resolve) => (answer = resolve)));
    await openMenu("scope:Local/c/count");
    document.body.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    answer(COUNT_INFO);
    await flushPromises();
    expect(menuItems()).toEqual([]);
    // A new right-click asks again and opens.
    await openMenu("scope:Local/c/count");
    expect(menuItems()).toEqual(["字段被修改时暂停", "字段被访问时暂停"]);
  });

  it("names a set item as on for screen readers", async () => {
    addSession({ supportsDataBreakpoints: true });
    await mountPane();
    await expandCounter();
    await openMenu("scope:Local/c/count");
    expect(menuItem("字段被修改时暂停").getAttribute("aria-label")).toBeNull();
    menuItem("字段被修改时暂停").click();
    await flushPromises();
    await openMenu("scope:Local/c/count");
    expect(menuItem("字段被修改时暂停").getAttribute("aria-label")).toBe("字段被修改时暂停（已开启）");
    expect(menuItem("字段被访问时暂停").getAttribute("aria-label")).toBeNull();
  });

  it("a persistable field breakpoint is saved for the project and sent with every current one", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    const store = addSession({ supportsDataBreakpoints: true });
    await mountPane();
    await expandCounter();
    await openMenu("scope:Local/c/count");
    menuItem("字段被修改时暂停").click();
    await flushPromises();
    expect(requests("setDataBreakpoints")).toEqual([[sessionId, "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#count", accessType: "write" }] }]]);
    expect(configs.stateFor(ROOT).dataBreakpoints.java).toEqual([{ dataId: "Counter#count", description: "Counter.count : int", accessType: "write" }]);

    // The menu shows what is set; the read one adds to it.
    await openMenu("scope:Local/c/count");
    expect(menuItem("字段被修改时暂停").classList).toContain("is-checked");
    expect(menuItem("字段被访问时暂停").classList).not.toContain("is-checked");
    menuItem("字段被访问时暂停").click();
    await flushPromises();
    expect(requests("setDataBreakpoints").at(-1)).toEqual([
      sessionId,
      "setDataBreakpoints",
      { breakpoints: [{ dataId: "Counter#count", accessType: "write" }, { dataId: "Counter#count", accessType: "read" }] },
    ]);
    expect(store.sessions.find((view) => view.id === sessionId)!.dataBreakpoints?.map((bp) => bp.verified)).toEqual([true, true]);

    // Choosing a set one again removes it.
    await openMenu("scope:Local/c/count");
    menuItem("字段被修改时暂停").click();
    await flushPromises();
    expect(configs.stateFor(ROOT).dataBreakpoints.java).toEqual([{ dataId: "Counter#count", description: "Counter.count : int", accessType: "read" }]);
  });

  it("one that cannot persist is listed for its session only and gone from the dialog when the session ends", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    const store = addSession({ supportsDataBreakpoints: true });
    await mountPane();
    await expandCounter();
    await openMenu("scope:Local/c/label");
    menuItem("字段被修改时暂停").click();
    await flushPromises();
    expect(configs.stateFor(ROOT).dataBreakpoints).toEqual({});
    expect(store.sessions.find((view) => view.id === sessionId)!.sessionDataBreakpoints).toEqual([
      { dataId: "Counter#label", description: "Counter.label : String", accessType: "write" },
    ]);
    expect(requests("setDataBreakpoints")).toEqual([[sessionId, "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#label", accessType: "write" }] }]]);

    await openDialog();
    expect(dataRows()).toHaveLength(1);
    expect(dataRows()[0]!.textContent).toContain("Counter.label : String");
    expect(dataRows()[0]!.textContent).toContain("仅本次会话");

    emit({ sessionId, event: "mincode.sessionEnded", body: { reason: "exited" } });
    await flushPromises();
    expect(document.body.querySelector(".debug-breakpoints-dialog__data")).toBeNull();
  });
});

describe("field breakpoints in the breakpoints dialog", () => {
  it("lists the saved ones with their access type and removes one, re-sending the session's list", async () => {
    const { t } = useVueI18n();
    expect(t("debug.breakpoints.dataTitle")).toBe("字段断点");
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setDataBreakpoints(ROOT, "java", [
      { dataId: "Counter#count", description: "Counter.count : int", accessType: "write" },
      { dataId: "Counter#count", description: "Counter.count : int", accessType: "read" },
    ]);
    const store = addSession({ supportsDataBreakpoints: true }, false);
    const index = store.sessions.findIndex((view) => view.id === sessionId);
    store.sessions[index] = {
      ...store.sessions[index]!,
      dataBreakpoints: [
        { dataId: "Counter#count", description: "Counter.count : int", accessType: "write", verified: true, id: 1 },
        { dataId: "Counter#count", description: "Counter.count : int", accessType: "read", verified: false, id: 2 },
      ],
    };
    await openDialog();
    const section = document.body.querySelector(".debug-breakpoints-dialog__data");
    expect(section?.querySelector("h4")?.textContent).toBe("字段断点");
    expect(dataRows().map((item) => item.querySelector(".debug-breakpoints-dialog__data-access")?.textContent?.trim())).toEqual([
      "字段被修改时暂停",
      "字段被访问时暂停",
    ]);
    expect(dataRows()[0]!.querySelector(".debug-breakpoints-dialog__function-status")).toBeNull();
    expect(dataRows()[1]!.querySelector(".debug-breakpoints-dialog__function-status")?.textContent?.trim()).toBe("未验证");
    expect(dataRows()[0]!.textContent).not.toContain("仅本次会话");

    dataRows()[0]!.querySelector<HTMLButtonElement>(`button[aria-label='${t("debug.breakpoints.remove")}']`)!.click();
    await flushPromises();
    expect(configs.stateFor(ROOT).dataBreakpoints.java).toEqual([{ dataId: "Counter#count", description: "Counter.count : int", accessType: "read" }]);
    expect(requests("setDataBreakpoints")).toEqual([[sessionId, "setDataBreakpoints", { breakpoints: [{ dataId: "Counter#count", accessType: "read" }] }]]);
    expect(dataRows()).toHaveLength(1);
  });

  it("has no field breakpoint section without any", async () => {
    await openDialog();
    expect(document.body.querySelector(".debug-breakpoints-dialog__data")).toBeNull();
  });
});
