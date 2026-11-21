// Program output must not re-render the editor or the workspace: every
// `output` batch replaces the session view in the store, so the editor's
// debug props keep their identity unless breakpoints or verification change.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h } from "vue";
import type { DebugEvent, PreparedDebugLaunch, RunConfiguration } from "@/domain/debug/types";

const debug = vi.hoisted(() => ({ handler: undefined as ((event: DebugEvent) => void) | undefined }));
vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => true, invokeBackend: vi.fn(), listenAppEvent: vi.fn() }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null), savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));
vi.mock("@/services/debug-guidance", () => ({ guardDebugLaunch: vi.fn().mockResolvedValue({ proceed: true }) }));
vi.mock("@/services/jdtls-bundles", () => ({ ensureJdtlsBundlesReady: vi.fn().mockResolvedValue(true) }));
vi.mock("@/services/plugin-launch-confirm", () => ({ confirmPluginLaunch: vi.fn().mockResolvedValue(true) }));
vi.mock("@/services/debug-service", () => ({
  listDebuggers: vi.fn().mockResolvedValue([]),
  debuggerEntryPoints: vi.fn().mockResolvedValue([]),
  prepareDebugger: vi.fn(),
  trustDebugger: vi.fn(),
  startDebugger: vi.fn(),
  debugRequest: vi.fn().mockResolvedValue({}),
  setSessionBreakpoints: vi.fn().mockResolvedValue({ breakpoints: [] }),
  stopDebugSession: vi.fn().mockResolvedValue(undefined),
  onDebugEvent: (handler: (event: DebugEvent) => void) => {
    debug.handler = handler;
    return () => undefined;
  },
}));

import * as service from "@/services/debug-service";
import { useProjectEditorDebug } from "@/composables/use-project-editor-debug";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const ROOT = "/Users/A/Proj";
const FILE = "/Users/A/Proj/app.py";
const cfg: RunConfiguration = { id: "c", name: "App", pluginId: "p", type: "python", request: "launch", fields: {} };

let wrapper: VueWrapper | undefined;
let pinia: ReturnType<typeof createPinia>;
beforeEach(() => {
  pinia = createPinia();
  setActivePinia(pinia);
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

describe("editor debug props under program output", () => {
  it("keep their identity across output events and change on verification", async () => {
    const configs = useDebugConfigsStore();
    await configs.load();
    configs.setBreakpoint(ROOT, FILE, { line: 3, enabled: true });
    const prepared = {
      prepareId: "x", type: "python", kind: "", projectRoot: ROOT, request: "launch", arguments: {},
      launch: { fingerprint: "f", trusted: true, previouslyTrusted: false },
    } as unknown as PreparedDebugLaunch;
    vi.mocked(service.prepareDebugger).mockResolvedValue(prepared);
    vi.mocked(service.startDebugger).mockResolvedValue({ sessionId: "s1", capabilities: {}, skippedBreakpoints: [] } as never);
    const sessions = useDebugSessionsStore();
    expect(await sessions.start(ROOT, cfg, { noDebug: false, activeFile: FILE })).toBe("s1");

    let editor!: ReturnType<typeof useProjectEditorDebug>;
    wrapper = mount(defineComponent({ setup() {
      editor = useProjectEditorDebug({ activeDocument: () => ({ path: FILE, loaded: true }), projectRootForPath: () => ROOT,
        languageIdFor: () => "python", editorReady: () => false, active: () => false, ownsProject: () => true, openAt: () => undefined, openVirtualSource: () => undefined });
      return () => h("div");
    } }), { global: { plugins: [pinia] } });
    await flushPromises();

    const before = editor.editorProps.value;
    for (let i = 0; i < 5; i++) debug.handler?.({ sessionId: "s1", event: "output", body: { category: "stdout", output: `line ${i}\n` } });
    await flushPromises();
    expect(sessions.sessions[0]!.output.length).toBeGreaterThan(0);
    expect(editor.editorProps.value).toBe(before);

    debug.handler?.({ sessionId: "s1", event: "mincode.breakpointsSet", body: { path: FILE, breakpoints: [{ verified: true, line: 3 }] } });
    await flushPromises();
    expect(editor.editorProps.value).not.toBe(before);
    expect(editor.editorProps.value.verifiedLines).toEqual({ 3: true });
    expect(editor.editorProps.value.breakpoints).toBe(before.breakpoints);
  });
});
