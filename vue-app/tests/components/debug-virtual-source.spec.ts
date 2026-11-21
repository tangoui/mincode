// A paused frame in a class outside the project (java-debug: `jdt://` with a
// sourceReference) opens a read-only document whose text comes from the DAP
// `source` request, instead of a broken disk path; local frames still open
// their file.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h, ref } from "vue";
import type { DapStackFrame, DebugEvent, PreparedDebugLaunch, RunConfiguration } from "@/domain/debug/types";

const debug = vi.hoisted(() => ({ handler: undefined as ((event: DebugEvent) => void) | undefined, frames: [] as unknown[] }));
vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => true, invokeBackend: vi.fn(), listenAppEvent: vi.fn() }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null), savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("@/services/debug-guidance", () => ({ guardDebugLaunch: vi.fn().mockResolvedValue({ proceed: true }) }));
vi.mock("@/services/jdtls-bundles", () => ({ ensureJdtlsBundlesReady: vi.fn().mockResolvedValue(true) }));
vi.mock("@/services/plugin-launch-confirm", () => ({ confirmPluginLaunch: vi.fn().mockResolvedValue(true) }));
vi.mock("@/services/lsp-service", () => ({
  readJdtClassFileContents: vi.fn().mockResolvedValue("public final class Method {}"),
}));
vi.mock("@/services/debug-service", () => ({
  listDebuggers: vi.fn().mockResolvedValue([]),
  debuggerEntryPoints: vi.fn().mockResolvedValue([]),
  prepareDebugger: vi.fn(),
  trustDebugger: vi.fn(),
  startDebugger: vi.fn(),
  debugRequest: vi.fn(),
  setSessionBreakpoints: vi.fn().mockResolvedValue({ breakpoints: [] }),
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
const LOCAL = "/Users/A/Proj/src/TodoController.java";
const cfg: RunConfiguration = { id: "c", name: "App", pluginId: "p", type: "java", request: "launch", fields: {} };
const JDK_FRAME: DapStackFrame = {
  id: 11, name: "Method.invoke(Object, Object...)", line: 580, column: 1,
  source: { name: "Method.java", path: "jdt://contents/java.base/java.lang.reflect/Method.class?=demo", sourceReference: 7 },
};
const LOCAL_FRAME: DapStackFrame = { id: 12, name: "TodoController.get", line: 35, column: 1, source: { name: "TodoController.java", path: LOCAL } };

let wrapper: VueWrapper | undefined;
let pinia: ReturnType<typeof createPinia>;
beforeEach(() => {
  pinia = createPinia();
  setActivePinia(pinia);
  vi.mocked(service.debugRequest).mockReset().mockImplementation(async (_id: string, command: string) => {
    if (command === "threads") return { threads: [{ id: 1, name: "main" }] };
    if (command === "stackTrace") return { stackFrames: debug.frames };
    if (command === "source") return { content: "public final class Method {}" };
    return {};
  });
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

async function setup() {
  const configs = useDebugConfigsStore();
  await configs.load();
  vi.mocked(service.prepareDebugger).mockResolvedValue({
    prepareId: "x", type: "java", kind: "jdtls", projectRoot: ROOT, request: "launch", arguments: {},
    launch: { fingerprint: "f", trusted: true, previouslyTrusted: false },
  } as unknown as PreparedDebugLaunch);
  vi.mocked(service.startDebugger).mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] } as never);
  const sessions = useDebugSessionsStore();
  expect(await sessions.start(ROOT, cfg, { noDebug: false, activeFile: LOCAL })).toBe("s1");

  const activePath = ref<string>(LOCAL);
  const openAt = vi.fn();
  const openVirtualSource = vi.fn();
  let editor!: ReturnType<typeof useProjectEditorDebug>;
  wrapper = mount(defineComponent({ setup() {
    editor = useProjectEditorDebug({
      activeDocument: () => ({ path: activePath.value, loaded: true }), projectRootForPath: () => ROOT,
      languageIdFor: () => "java", editorReady: () => false, active: () => true, ownsProject: () => true,
      openAt, openVirtualSource,
    });
    return () => h("div");
  } }), { global: { plugins: [pinia] } });
  await flushPromises();
  return { editor, openAt, openVirtualSource, activePath };
}

async function pause(frames: DapStackFrame[]) {
  debug.frames = frames;
  debug.handler?.({ sessionId: "s1", event: "stopped", body: { reason: "step", threadId: 1 } });
  await flushPromises();
}

describe("paused frames outside the project", () => {
  it("open a read-only source from the adapter instead of a disk path", async () => {
    const { editor, openAt, openVirtualSource, activePath } = await setup();
    await pause([JDK_FRAME, LOCAL_FRAME]);

    expect(openAt).not.toHaveBeenCalled();
    expect(openVirtualSource).toHaveBeenCalledTimes(1);
    const [path, line, load] = openVirtualSource.mock.calls[0]!;
    expect(path).toMatch(/^debug-source:\/\/s1\/.+\/Method\.java$/);
    expect(line).toBe(580);
    await expect(load()).resolves.toBe("public final class Method {}");
    expect(readJdtClassFileContents).toHaveBeenCalledWith(ROOT, JDK_FRAME.source?.path);

    // The paused line shows in that document.
    activePath.value = path;
    await flushPromises();
    expect(editor.editorProps.value.pausedLine).toBe(580);

    // Pausing on the same frame again does not reopen it.
    await pause([JDK_FRAME, LOCAL_FRAME]);
    expect(openVirtualSource).toHaveBeenCalledTimes(1);
  });

  it("still open local frames as project files", async () => {
    const { openAt, openVirtualSource } = await setup();
    await pause([LOCAL_FRAME]);
    expect(openVirtualSource).not.toHaveBeenCalled();
    expect(openAt).toHaveBeenCalledWith(LOCAL, 35);
  });
});
