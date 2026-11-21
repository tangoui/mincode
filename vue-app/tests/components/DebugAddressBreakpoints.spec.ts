// Breakpoints in read-only debugger-provided sources (a JDK or dependency
// class the adapter addresses as e.g. `jdt://…`): the gutter works as in a
// project file, they are saved per project under the address, sent with the
// start request and to running sessions, listed in the breakpoints dialog,
// and a source known only by its sourceReference says once that it cannot
// hold breakpoints.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h, ref } from "vue";
import DebugBreakpointsDialog from "@/components/debug/DebugBreakpointsDialog.vue";
import type { DapStackFrame, DebugEvent, PreparedDebugLaunch, RunConfiguration } from "@/domain/debug/types";
import { virtualSourceDocumentPath } from "@/domain/debug/virtual-source";

const debug = vi.hoisted(() => ({
  handler: undefined as ((event: DebugEvent) => void) | undefined,
  frames: [] as unknown[],
  info: vi.fn(),
  warning: vi.fn(),
}));
vi.mock("element-plus", async (importOriginal) => {
  const original = await importOriginal<typeof import("element-plus")>();
  return { ...original, ElMessage: Object.assign(original.ElMessage, { info: debug.info, warning: debug.warning }) };
});
vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => true, invokeBackend: vi.fn(), listenAppEvent: vi.fn() }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("@/services/debug-guidance", () => ({ guardDebugLaunch: vi.fn().mockResolvedValue({ proceed: true }) }));
vi.mock("@/services/jdtls-bundles", () => ({ ensureJdtlsBundlesReady: vi.fn().mockResolvedValue(true) }));
vi.mock("@/services/plugin-launch-confirm", () => ({ confirmPluginLaunch: vi.fn().mockResolvedValue(true) }));
vi.mock("@/services/lsp-service", () => ({ readJdtClassFileContents: vi.fn().mockResolvedValue("public class ArrayList {}") }));
vi.mock("@/services/debug-service", () => ({
  listDebuggers: vi.fn().mockResolvedValue([]),
  debuggerEntryPoints: vi.fn().mockResolvedValue([]),
  applyDebuggerSettings: vi.fn(),
  prepareDebugger: vi.fn(),
  trustDebugger: vi.fn(),
  startDebugger: vi.fn(),
  debugRequest: vi.fn(),
  setSessionBreakpoints: vi.fn().mockResolvedValue({ breakpoints: [] }),
  setSessionAddressBreakpoints: vi.fn(),
  stopDebugSession: vi.fn().mockResolvedValue(undefined),
  onDebugEvent: (handler: (event: DebugEvent) => void) => {
    debug.handler = handler;
    return () => undefined;
  },
}));

import * as service from "@/services/debug-service";
import { readJdtClassFileContents } from "@/services/lsp-service";
import { useProjectEditorDebug } from "@/composables/use-project-editor-debug";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/Users/A/Proj";
const LOCAL = "/Users/A/Proj/src/App.java";
const ADDRESS = "jdt://contents/java.base/java.util/ArrayList.class?=demo/%5C/jrt-fs.jar%60java.base=/%3Cjava.util(ArrayList.class";
const cfg: RunConfiguration = { id: "c", name: "App", pluginId: "p", type: "java", request: "launch", fields: {} };
const SOURCE = { name: "ArrayList.java", path: ADDRESS };
const LIB_FRAME: DapStackFrame = { id: 11, name: "ArrayList.add(Object)", line: 495, column: 1, source: SOURCE };
const LIB_TAB = virtualSourceDocumentPath("s1", SOURCE, "f");
const REF_TAB = virtualSourceDocumentPath("s1", { name: "<eval>", sourceReference: 9 }, "f");
const FILE_URI_TAB = virtualSourceDocumentPath("s1", { name: "App.java", path: "file:///Users/A/Proj/src/App.java" }, "f");

let wrappers: VueWrapper[] = [];
beforeEach(() => {
  debug.info.mockReset();
  debug.warning.mockReset();
  vi.mocked(service.startDebugger).mockReset().mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] } as never);
  vi.mocked(service.setSessionAddressBreakpoints)
    .mockReset()
    .mockImplementation(async (_id, _address, list) => ({
      breakpoints: list.map((bp) => ({ verified: false, line: bp.line, message: "class not loaded" })),
    }));
  vi.mocked(service.setSessionBreakpoints).mockReset().mockResolvedValue({ breakpoints: [] });
  vi.mocked(service.prepareDebugger).mockResolvedValue({
    prepareId: "x", type: "java", kind: "jdtls", projectRoot: ROOT, request: "launch", arguments: {},
    launch: { fingerprint: "f", trusted: true, previouslyTrusted: false },
  } as unknown as PreparedDebugLaunch);
  vi.mocked(service.debugRequest).mockReset().mockImplementation(async (_id: string, command: string) => {
    if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
    if (command === "stackTrace") return { stackFrames: debug.frames };
    return {};
  });
});
afterEach(() => {
  for (const wrapper of wrappers) wrapper.unmount();
  wrappers = [];
  document.body.innerHTML = "";
});

async function editor(activePath = LIB_TAB, rootForPath: (path: string) => string = () => ROOT) {
  const path = ref(activePath);
  const openVirtualSource = vi.fn();
  const openDocumentPaths = ref<string[]>([]);
  let api!: ReturnType<typeof useProjectEditorDebug>;
  wrappers.push(
    mount(defineComponent({
      setup() {
        api = useProjectEditorDebug({
          // The workspace opens debugger sources as external read-only documents.
          activeDocument: () => ({ path: path.value, loaded: true, external: path.value.startsWith("debug-source://") }),
          projectRootForPath: rootForPath,
          languageIdFor: () => "java",
          editorReady: () => false,
          active: () => true,
          ownsProject: () => true,
          openAt: vi.fn(),
          openVirtualSource,
          openDocumentPaths: () => openDocumentPaths.value,
        });
        return () => h("div");
      },
    })),
  );
  await flushPromises();
  return { api, path, openVirtualSource, openDocumentPaths };
}

async function startSession() {
  const sessions = useDebugSessionsStore();
  expect(await sessions.start(ROOT, cfg, { noDebug: false, activeFile: LOCAL })).toBe("s1");
  await flushPromises();
  return sessions;
}

describe("breakpoints in a debugger-provided source", () => {
  it("toggle from the gutter, saved under the address, synced to the running session", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    await startSession();
    const { api } = await editor();

    api.toggleBreakpoint(495);
    await flushPromises();
    expect(configs.stateFor(ROOT).addressBreakpoints).toEqual({ java: { [ADDRESS]: [{ line: 495, enabled: true }] } });
    expect(configs.stateFor(ROOT).breakpoints).toEqual({});
    expect(api.editorProps.value.breakpoints).toEqual([{ line: 495, enabled: true }]);
    expect(service.setSessionAddressBreakpoints).toHaveBeenCalledWith("s1", ADDRESS, [{ line: 495, enabled: true }]);
    expect(service.setSessionBreakpoints).not.toHaveBeenCalled();
    // Unverified as for a file, with the adapter's reason on hover.
    expect(api.editorProps.value.verifiedLines).toEqual({ 495: false });
    expect(api.editorProps.value.breakpointDetails).toEqual({ 495: { message: "class not loaded" } });

    // A later `breakpoint` event for the address verifies it.
    debug.handler?.({ sessionId: "s1", event: "breakpoint", body: { reason: "changed", breakpoint: { verified: true, line: 495, source: { path: ADDRESS } } } });
    await flushPromises();
    expect(api.editorProps.value.verifiedLines).toEqual({ 495: true });
    expect(useDebugSessionsStore().selectedSession?.verified).toEqual({});

    api.toggleBreakpoint(495);
    await flushPromises();
    expect(configs.stateFor(ROOT).addressBreakpoints).toEqual({});
    expect(service.setSessionAddressBreakpoints).toHaveBeenLastCalledWith("s1", ADDRESS, []);
  });

  it("edits conditions, hit counts and logpoints through the same editor as files", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    await startSession();
    const { api } = await editor();
    const anchor = { x: 1, y: 2 };

    api.editBreakpoint(40, anchor, "logpoint");
    expect(api.conditionDraft.value).toMatchObject({ line: 40, at: { address: ADDRESS, type: "java" }, focusLogMessage: true, condition: "" });
    api.saveCondition({ condition: "e == null", hitCondition: "3", logMessage: "added {e}" });
    expect(configs.addressBreakpointsFor(ROOT, "java", ADDRESS)).toEqual([
      { line: 40, enabled: true, condition: "e == null", hitCondition: "3", logMessage: "added {e}" },
    ]);

    api.setBreakpointEnabled(40, false);
    expect(configs.addressBreakpointsFor(ROOT, "java", ADDRESS)[0]?.enabled).toBe(false);
    api.editBreakpoint(40, anchor);
    expect(api.conditionDraft.value).toMatchObject({ condition: "e == null", hitCondition: "3", logMessage: "added {e}" });
    api.cancelCondition();
    api.removeBreakpoint(40);
    expect(configs.stateFor(ROOT).addressBreakpoints).toEqual({});
  });

  it("says it again once the source's tab was closed and opened again", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    const { api, openDocumentPaths } = await editor(REF_TAB);
    openDocumentPaths.value = [REF_TAB];
    await flushPromises();
    api.toggleBreakpoint(3);
    api.toggleBreakpoint(3);
    expect(debug.info).toHaveBeenCalledTimes(1);
    // Closed, then opened again.
    openDocumentPaths.value = [];
    await flushPromises();
    openDocumentPaths.value = [REF_TAB];
    await flushPromises();
    api.toggleBreakpoint(3);
    expect(debug.info).toHaveBeenCalledTimes(2);
  });

  it("a source known only by its sourceReference ignores gutter clicks and says so once", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    const { api, path } = await editor(REF_TAB);
    api.toggleBreakpoint(3);
    api.toggleBreakpoint(4);
    api.editBreakpoint(3, { x: 0, y: 0 });
    expect(api.conditionDraft.value).toBeNull();
    expect(api.editorProps.value.breakpoints).toEqual([]);
    expect(configs.stateFor(ROOT).addressBreakpoints).toEqual({});
    expect(debug.info).toHaveBeenCalledTimes(1);
    expect(String(debug.info.mock.calls[0]?.[0]?.message)).toMatch(/no usable address|没有给出可用的源码地址/);

    // An address the host would refuse (a file URI) is treated the same: not saved, not sent.
    path.value = FILE_URI_TAB;
    await flushPromises();
    api.toggleBreakpoint(3);
    expect(configs.stateFor(ROOT).addressBreakpoints).toEqual({});
    expect(configs.stateFor(ROOT).breakpoints).toEqual({});
    expect(service.setSessionAddressBreakpoints).not.toHaveBeenCalled();
    expect(debug.info).toHaveBeenCalledTimes(2);
  });

  it("the store refuses addresses the host would refuse and more than 256 addresses", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    for (const bad of ["file:///x/A.java", "src/A.java", "C://x/A.java", `ab://${"x".repeat(4093)}`, "ab://x\u0007"]) {
      expect(configs.toggleAddressBreakpoint(ROOT, "java", bad, 1)).toBe(false);
    }
    expect(configs.stateFor(ROOT).addressBreakpoints).toEqual({});
    for (let i = 0; i < 256; i += 1) expect(configs.toggleAddressBreakpoint(ROOT, "java", `ab://c${i}`, 1)).toBe(true);
    // The limit is per debugger type.
    expect(configs.toggleAddressBreakpoint(ROOT, "node", "ab://c0", 1)).toBe(true);
    await startSession();
    const { api } = await editor();
    api.toggleBreakpoint(5);
    expect(configs.addressBreakpointsFor(ROOT, "java", ADDRESS)).toEqual([]);
    expect(debug.warning).toHaveBeenCalledTimes(1);
    // An address that already has breakpoints still takes more.
    expect(configs.toggleAddressBreakpoint(ROOT, "java", "ab://c0", 2)).toBe(true);
  });

  it("are sent with the start request (enabled ones) and their start results show", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.toggleAddressBreakpoint(ROOT, "java", ADDRESS, 495);
    configs.setAddressBreakpoint(ROOT, "java", ADDRESS, { line: 500, enabled: false });
    configs.toggleAddressBreakpoint(ROOT, "java", "x-lib://other/Thing.class", 7);
    configs.removeAddressBreakpoint(ROOT, "java", "x-lib://other/Thing.class", 7);
    vi.mocked(service.startDebugger).mockImplementation(async () => {
      debug.handler?.({ sessionId: "s1", event: "mincode.breakpointsSet", body: { path: ADDRESS, address: true, breakpoints: [{ id: 4, verified: true, line: 495 }] } });
      return { sessionId: "s1", capabilities: {}, skippedBreakpoints: [] } as never;
    });
    await startSession();
    expect(vi.mocked(service.startDebugger).mock.calls[0]?.[0].addressBreakpoints).toEqual({ [ADDRESS]: [{ line: 495, enabled: true }] });
    const { api } = await editor();
    expect(api.editorProps.value.verifiedLines).toEqual({ 495: true });
    expect(useDebugSessionsStore().selectedSession?.addressBreakpointIds).toEqual({ 4: ADDRESS });

    // Muting re-sends the address too (empty).
    await useDebugSessionsStore().setBreakpointsMuted("s1", true);
    expect(service.setSessionAddressBreakpoints).toHaveBeenLastCalledWith("s1", ADDRESS, []);
  });

  it("a start without address breakpoints sends none", async () => {
    await useDebugConfigsStore().load();
    await startSession();
    expect(vi.mocked(service.startDebugger).mock.calls[0]?.[0]).not.toHaveProperty("addressBreakpoints");
  });

  it("the breakpoints dialog lists them by class name and line, opens, switches and removes them", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.toggleBreakpoint(ROOT, LOCAL, 3);
    configs.toggleAddressBreakpoint(ROOT, "java", ADDRESS, 495);
    await startSession();
    // Sent mid-session, answered unverified with the adapter's own text.
    await useDebugSessionsStore().syncBreakpointsForFile(ROOT, { address: ADDRESS, type: "java" });
    const dialog = mount(DebugBreakpointsDialog, { props: { modelValue: true, projectRoot: ROOT }, attachTo: document.body });
    wrappers.push(dialog);
    await flushPromises();

    const locations = [...document.querySelectorAll<HTMLElement>(".debug-breakpoints-dialog__location")];
    expect(locations.map((item) => item.textContent?.trim())).toEqual(["src/App.java:3", "ArrayList:495"]);
    expect(locations[1]?.getAttribute("title")).toBe(ADDRESS);
    const status = document.querySelector(".debug-breakpoints-dialog__location-status");
    expect(status?.textContent?.trim()).toMatch(/class not loaded/);
    expect(document.querySelectorAll(".debug-breakpoints-dialog__location-status")).toHaveLength(1);

    locations[1]!.click();
    await flushPromises();
    expect(dialog.emitted("open-address")).toEqual([[ADDRESS, "java", 495]]);
    expect(dialog.emitted("open-location")).toBeUndefined();

    const rows = document.querySelectorAll(".debug-breakpoints-dialog__table tbody tr");
    (rows[1]!.querySelector(".el-switch") as HTMLElement).click();
    await flushPromises();
    expect(configs.addressBreakpointsFor(ROOT, "java", ADDRESS)).toEqual([{ line: 495, enabled: false }]);
    expect(service.setSessionAddressBreakpoints).toHaveBeenLastCalledWith("s1", ADDRESS, []);

    const remove = rows[1]!.querySelector<HTMLElement>(`[aria-label="${"Remove breakpoint"}"], [aria-label="删除断点"]`);
    remove!.click();
    await flushPromises();
    expect(configs.stateFor(ROOT).addressBreakpoints).toEqual({});
    expect(configs.breakpointsFor(ROOT, LOCAL)).toEqual([{ line: 3, enabled: true }]);
  });

  it("go to opens the address's source through the debugger, reusing an open tab", async () => {
    await useDebugConfigsStore().load();
    await startSession();
    const { api, openVirtualSource, openDocumentPaths } = await editor(LOCAL);

    api.openAddress(ROOT, "s1", ADDRESS, 495);
    const [path, line, load] = openVirtualSource.mock.calls[0]!;
    expect(path).toMatch(/^debug-source:\/\/s1\/.+\/ArrayList\.class$/);
    expect(line).toBe(495);
    await expect(load()).resolves.toBe("public class ArrayList {}");
    expect(readJdtClassFileContents).toHaveBeenCalledWith(ROOT, ADDRESS);

    // Paused in that class, the frame names the tab; an open tab is reused.
    debug.frames = [LIB_FRAME];
    debug.handler?.({ sessionId: "s1", event: "stopped", body: { reason: "breakpoint", threadId: 1 } });
    await flushPromises();
    openDocumentPaths.value = [LIB_TAB];
    api.openAddress(ROOT, "s1", ADDRESS, 12);
    expect(openVirtualSource).toHaveBeenLastCalledWith(LIB_TAB, 12, expect.any(Function));
  });

  it("are kept per debugger type: never sent to or muted for a session of another type", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.toggleAddressBreakpoint(ROOT, "java", ADDRESS, 495);
    expect(configs.stateFor(ROOT).addressBreakpoints).toEqual({ java: { [ADDRESS]: [{ line: 495, enabled: true }] } });
    const node: RunConfiguration = { id: "n", name: "Web", pluginId: "p", type: "node", request: "launch", fields: {} };
    const sessions = useDebugSessionsStore();
    vi.mocked(service.startDebugger).mockResolvedValueOnce({ sessionId: "n1", capabilities: {}, skippedBreakpoints: [] } as never);
    expect(await sessions.start(ROOT, node, { noDebug: false, activeFile: LOCAL })).toBe("n1");
    await flushPromises();
    // Not with the other type's start request.
    expect(vi.mocked(service.startDebugger).mock.calls[0]?.[0]).not.toHaveProperty("addressBreakpoints");
    await startSession();
    expect(vi.mocked(service.startDebugger).mock.calls[1]?.[0].addressBreakpoints).toEqual({ [ADDRESS]: [{ line: 495, enabled: true }] });

    // Mid-session changes go to the Java session only.
    vi.mocked(service.setSessionAddressBreakpoints).mockClear();
    const { api } = await editor();
    api.toggleBreakpoint(500);
    await flushPromises();
    expect(vi.mocked(service.setSessionAddressBreakpoints).mock.calls.map((call) => call[0])).toEqual(["s1"]);
    // The other type's session gave no answer: the Java session's alone counts.
    expect(api.editorProps.value.verifiedLines).toEqual({ 495: false, 500: false });

    // Muting the other type's session re-sends nothing at the address; muting the Java one does.
    vi.mocked(service.setSessionAddressBreakpoints).mockClear();
    await sessions.setBreakpointsMuted("n1", true);
    expect(service.setSessionAddressBreakpoints).not.toHaveBeenCalled();
    await sessions.setBreakpointsMuted("s1", true);
    expect(service.setSessionAddressBreakpoints).toHaveBeenCalledWith("s1", ADDRESS, []);
  });

  it("a library tab keeps the project and type of the session that opened it", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    const sessions = await startSession();
    sessions.close("s1");
    await flushPromises();
    // The tab's path falls in no project of its own: the session's root is used.
    const { api } = await editor(LIB_TAB, () => "/Users/A/Elsewhere");
    api.toggleBreakpoint(495);
    expect(configs.stateFor(ROOT).addressBreakpoints).toEqual({ java: { [ADDRESS]: [{ line: 495, enabled: true }] } });
  });

  it("a library tab of an unknown session refuses breakpoints with one hint", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    const { api } = await editor(virtualSourceDocumentPath("ghost", SOURCE, "f"));
    api.toggleBreakpoint(495);
    api.toggleBreakpoint(496);
    expect(configs.stateFor(ROOT).addressBreakpoints).toEqual({});
    expect(debug.info).toHaveBeenCalledTimes(1);
    expect(String(debug.info.mock.calls[0]?.[0]?.message)).toMatch(/session|会话/);
  });

  it("the breakpoints dialog names the debugger type when the project has addresses of more than one", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.toggleAddressBreakpoint(ROOT, "java", ADDRESS, 495);
    await startSession();
    const mountDialog = async () => {
      const dialog = mount(DebugBreakpointsDialog, { props: { modelValue: true, projectRoot: ROOT }, attachTo: document.body });
      wrappers.push(dialog);
      await flushPromises();
      return [...document.querySelectorAll<HTMLElement>(".debug-breakpoints-dialog__location")].map((item) => item.textContent?.trim());
    };
    expect(await mountDialog()).toEqual(["ArrayList:495"]);
    for (const wrapper of wrappers) wrapper.unmount();
    wrappers = [];
    document.body.innerHTML = "";
    configs.toggleAddressBreakpoint(ROOT, "node", "node-internal://lib/events.js", 7);
    expect(await mountDialog()).toEqual(["ArrayList (java):495", "events (node):7"]);
  });
});
