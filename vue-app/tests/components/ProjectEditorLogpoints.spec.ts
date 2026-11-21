import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import ProjectEditorDebugPopups from "@/components/projects/ProjectEditorDebugPopups.vue";
import DebugBreakpointsDialog from "@/components/debug/DebugBreakpointsDialog.vue";
import { useProjectEditorDebug } from "@/composables/use-project-editor-debug";
import { useDebugConfigsStore } from "@/stores/debug-configs";
import { useVueI18n } from "@/i18n/messages";

const session = vi.hoisted(() => ({ sync: vi.fn(), reason: "" }));
vi.mock("@/stores/debug-sessions", () => ({ useDebugSessionsStore: () => ({
  selectedSession: null, sessions: [], syncBreakpointsForFile: session.sync,
  verifiedLinesFor: () => ({}), breakpointDetailsFor: () => ({ 5: { message: session.reason } }),
  dataBreakpointEntries: () => [], registerRestartHandler: () => undefined,
}) }));
vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => false }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null), savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/debug-service", () => ({ listDebuggers: vi.fn().mockResolvedValue([]), debuggerEntryPoints: vi.fn().mockResolvedValue([]) }));

const ROOT = "/fixture";
const FILE = "/fixture/app.py";
let wrapper: VueWrapper | undefined;
let controller: ReturnType<typeof useProjectEditorDebug> | undefined;

afterEach(() => {
  wrapper?.unmount(); wrapper = undefined; controller = undefined;
  session.sync.mockClear(); session.reason = "";
});

async function host() {
  const configs = useDebugConfigsStore();
  await configs.load();
  wrapper = mount(defineComponent({ setup() {
    controller = useProjectEditorDebug({ activeDocument: () => ({ path: FILE, loaded: false }), projectRootForPath: () => ROOT,
      languageIdFor: () => "python", editorReady: () => false, active: () => false, ownsProject: () => true, openAt: () => undefined, openVirtualSource: () => undefined });
    return () => h(ProjectEditorDebugPopups, { condition: controller?.conditionDraft.value ?? null, entryMenu: null,
      onSaveCondition: (values: Parameters<ReturnType<typeof useProjectEditorDebug>["saveCondition"]>[0]) => controller?.saveCondition(values), onCancelCondition: () => controller?.cancelCondition() });
  } }), { attachTo: document.body });
  await flushPromises();
  if (!controller) throw new Error("Editor debug controller not mounted");
  return { configs, editor: controller };
}

function messageInput() {
  const input = document.querySelector<HTMLInputElement>(".project-debug-popups__condition [data-field='logMessage'] input");
  if (!input) throw new Error("Log message input not found");
  return input;
}

async function setMessage(text: string) {
  const input = messageInput(); input.value = text;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  await flushPromises();
}

async function save() {
  const button = document.querySelector<HTMLButtonElement>(".project-debug-popups__actions .el-button--primary");
  if (!button) throw new Error("Save button not found");
  button.click(); await flushPromises();
}

describe("project editor logpoints", () => {
  it("prefills and focuses logging text, preserving conditions and disabled state on save", async () => {
    const { configs, editor } = await host();
    configs.setBreakpoint(ROOT, FILE, { line: 5, enabled: false, condition: "value > 0", hitCondition: "2", logMessage: "old={value}" });
    editor.editBreakpoint(5, { x: 20, y: 30 });
    await flushPromises();
    expect(messageInput().value).toBe("old={value}");
    expect(document.activeElement).toBe(messageInput());
    await setMessage("  new={value} <tag>  ");
    await save();
    expect(configs.breakpointsFor(ROOT, FILE)).toEqual([{ line: 5, enabled: false, condition: "value > 0", hitCondition: "2", logMessage: "  new={value} <tag>  " }]);
  });

  it("creates a logpoint from its menu intent and clears logging text back to a normal breakpoint", async () => {
    const { configs, editor } = await host();
    editor.editBreakpoint(8, { x: 20, y: 30 }, "logpoint");
    await flushPromises();
    expect(document.activeElement).toBe(messageInput());
    await setMessage("value={value}");
    messageInput().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await flushPromises();
    expect(configs.breakpointsFor(ROOT, FILE)).toEqual([{ line: 8, enabled: true, logMessage: "value={value}" }]);
    editor.editBreakpoint(8, { x: 20, y: 30 });
    await flushPromises();
    await setMessage(" \t "); await save();
    expect(configs.breakpointsFor(ROOT, FILE)).toEqual([{ line: 8, enabled: true }]);
  });

  it("cancels edits with Escape instead of storing an unfinished logpoint", async () => {
    const { configs, editor } = await host();
    editor.editBreakpoint(8, { x: 20, y: 30 }, "logpoint"); await flushPromises();
    await setMessage("unfinished");
    messageInput().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await flushPromises();
    expect(editor.conditionDraft.value).toBeNull();
    expect(configs.breakpointsFor(ROOT, FILE)).toEqual([]);
  });

  it("shows log messages as plain text and localizes unsupported-adapter feedback in the breakpoint list", async () => {
    const configs = useDebugConfigsStore(); await configs.load();
    configs.setBreakpoint(ROOT, FILE, { line: 5, enabled: true, logMessage: "value={value} <b>text</b>" });
    session.reason = "unsupported: logpoints";
    wrapper = mount(DebugBreakpointsDialog, { props: { modelValue: true, projectRoot: ROOT }, attachTo: document.body });
    await flushPromises();
    const table = document.querySelector(".debug-breakpoints-dialog");
    const { t } = useVueI18n();
    expect(table?.textContent).toContain("value={value} <b>text</b>");
    expect(table?.querySelector("b")).toBeNull();
    expect(table?.textContent).toContain(t("debug.editor.logpointsUnsupported"));
  });
});
