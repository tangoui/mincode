import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick, shallowRef } from "vue";
// Real Monaco context key semantics decide what the menu shows; Monaco ships no types for its internal modules.
// @ts-expect-error -- untyped Monaco internal ESM module
import { ContextKeyExpr } from "../../node_modules/monaco-editor/esm/vs/platform/contextkey/common/contextkey.js";
import type { PluginServiceEntry } from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";
import { ensureMonacoContributions, type MonacoApi } from "@/services/monaco-contributions";
import { attachMonacoMenuContext } from "@/services/monaco-menus";
import { installMonacoServiceMenus } from "@/services/monaco-service-menus";
import type { PluginServiceRunRequest } from "@/services/plugin-services";

interface Action { id: string; label: string; precondition?: string; contextMenuGroupId?: string; run: (editor: unknown) => unknown }
interface Submenu { id: string; title: string; group: string; order: number; when?: string }
interface SubmenuItem { command: string; title: string; tooltip?: string; when?: string; precondition?: string; order: number }

const registry = {
  actions: new Map<string, Action>(),
  submenus: [] as Submenu[],
  items: [] as Array<SubmenuItem & { submenu: string }>,
};

class Range {
  constructor(
    public startLineNumber: number,
    public startColumn: number,
    public endLineNumber: number,
    public endColumn: number,
  ) {}
}

const fakeMonaco = {
  Range,
  editor: {
    EditorOption: { readOnly: 91 },
    TrackedRangeStickiness: { NeverGrowsWhenTypingAtEdges: 1 },
    addEditorAction(action: Action) {
      registry.actions.set(action.id, action);
      return { dispose: () => registry.actions.delete(action.id) };
    },
  },
  mincodeEditorContextMenu: {
    appendSubmenu(submenu: Submenu) {
      registry.submenus.push(submenu);
      return { dispose: () => registry.submenus.splice(registry.submenus.indexOf(submenu), 1) };
    },
    appendItem(submenu: string, item: SubmenuItem) {
      const stored = { ...item, submenu };
      registry.items.push(stored);
      return { dispose: () => registry.items.splice(registry.items.indexOf(stored), 1) };
    },
  },
} as unknown as MonacoApi;

/** A one-line model holding `text`, fully selected. */
function fakeEditor(text: string, options: { readOnly?: boolean } = {}) {
  const keys = new Map<string, unknown>([["editorHasSelection", text.length > 0]]);
  const selectionListeners: Array<() => void> = [];
  let value = text;
  const edits: Array<{ range: Range; text: string }> = [];
  const selection = Object.assign(new Range(1, 1, 1, text.length + 1), { isEmpty: () => text.length === 0 });
  const model = {
    getValueInRange: () => value,
    getValueLengthInRange: () => value.length,
    getLanguageId: () => "plaintext",
    getEOL: () => "\n",
    getLineMaxColumn: () => value.length + 1,
    isDisposed: () => false,
    deltaDecorations: (_old: string[], added: unknown[]) => added.map(() => "d1"),
    getDecorationRange: () => selection,
    pushStackElement: vi.fn(),
    pushEditOperations: (_before: unknown, operations: Array<{ range: Range; text: string }>) => {
      edits.push(...operations);
      return null;
    },
  };
  const editor = {
    keys,
    edits,
    getModel: () => model,
    getSelection: () => selection,
    getOption: () => Boolean(options.readOnly),
    createContextKey(key: string, initial: unknown) {
      keys.set(key, initial);
      return { set: (next: unknown) => keys.set(key, next), get: () => keys.get(key), reset: () => keys.delete(key) };
    },
    onDidChangeCursorSelection(listener: () => void) {
      selectionListeners.push(listener);
      return { dispose: () => undefined };
    },
    onDidChangeModel(listener: () => void) {
      selectionListeners.push(listener);
      return { dispose: () => undefined };
    },
    select(next: string) {
      value = next;
      keys.set("editorHasSelection", next.length > 0);
      for (const listener of selectionListeners) listener();
    },
  };
  keys.set("mincodeEditorKind", "text");
  return editor;
}

type FakeEditor = ReturnType<typeof fakeEditor>;

function matches(editor: FakeEditor, expression: string | undefined) {
  if (!expression) return true;
  const parsed = ContextKeyExpr.deserialize(expression);
  return parsed ? parsed.evaluate({ getValue: (key: string) => editor.keys.get(key) } as never) : true;
}

/** The submenu items Monaco would show for this editor, with their enabled state. */
function shownItems(editor: FakeEditor) {
  const submenu = registry.submenus.find((item) => matches(editor, item.when));
  if (!submenu) return [];
  return registry.items
    .filter((item) => item.submenu === submenu.id && matches(editor, item.when))
    .map((item) => ({ title: item.title, tooltip: item.tooltip, enabled: matches(editor, item.precondition), command: item.command }));
}

function entry(overrides: Partial<PluginServiceEntry> = {}): PluginServiceEntry {
  return {
    pluginId: "demo.provider",
    pluginName: "Demo Provider",
    entryId: "reverse",
    service: "demo.text.reverse",
    title: "Reverse text",
    description: "",
    input: { kind: "text", maxBytes: 8 },
    output: { kind: "text" },
    surfaces: ["selection", "palette"],
    risk: "read",
    timeoutMs: 30000,
    isDefault: false,
    ...overrides,
  };
}

const { t } = useVueI18n();
const services = shallowRef<PluginServiceEntry[]>([]);
const run = vi.fn<(request: PluginServiceRunRequest) => Promise<void>>();
let uninstall: (() => void) | undefined;
const detachers: Array<() => void> = [];

function attach(editor: FakeEditor, kind = "text") {
  editor.keys.set("mincodeEditorKind", kind);
  detachers.push(attachMonacoMenuContext(editor as never, { kind: () => kind }));
  return editor;
}

beforeEach(() => {
  run.mockReset();
  run.mockResolvedValue(undefined);
  services.value = [entry(), entry({ service: "demo.text.upper", entryId: "upper", title: "Upper case", surfaces: ["palette"] })];
  ensureMonacoContributions(fakeMonaco);
  uninstall = installMonacoServiceMenus({ services, run });
});

afterEach(() => {
  uninstall?.();
  for (const detach of detachers.splice(0)) detach();
});

describe("Monaco plugin service menu", () => {
  it("shows a 'Process with plugin' submenu with selection services only while text is selected", () => {
    const editor = attach(fakeEditor(""));
    expect(shownItems(editor)).toEqual([]);
    editor.select("abc");
    expect(registry.submenus.map((submenu) => submenu.title)).toEqual([t("plugins.services.menu")]);
    expect(shownItems(editor)).toEqual([expect.objectContaining({ title: "Reverse text", enabled: true })]);
  });

  it("disables a service whose providers cannot take the selection and says why", () => {
    const editor = attach(fakeEditor("123456789"));
    expect(shownItems(editor)).toEqual([
      expect.objectContaining({ title: t("plugins.services.tooLargeItem", { title: "Reverse text" }), tooltip: t("plugins.services.tooLarge"), enabled: false }),
    ]);
    // Multi-byte text counts in UTF-8 bytes: 3 × 3 bytes > 8.
    editor.select("一二三");
    expect(shownItems(editor)[0]).toMatchObject({ enabled: false });
    editor.select("一二");
    expect(shownItems(editor)[0]).toMatchObject({ title: "Reverse text", enabled: true });
  });

  it("tracks the selection of editors opened before the services module loaded", () => {
    uninstall?.();
    const editor = attach(fakeEditor("abc"));
    expect(editor.keys.has("mincodeSelectionBytes")).toBe(false);
    uninstall = installMonacoServiceMenus({ services, run });
    expect(shownItems(editor)).toEqual([expect.objectContaining({ title: "Reverse text", enabled: true })]);
    uninstall();
    uninstall = undefined;
    expect(editor.keys.has("mincodeSelectionBytes")).toBe(false);
  });

  it("is not offered in SQL editors", () => {
    const editor = attach(fakeEditor("abc"), "sql");
    expect(shownItems(editor)).toEqual([]);
  });

  it("runs the service with the selected text and replaces the selection through an undoable edit", async () => {
    const editor = attach(fakeEditor("abc"));
    const [item] = shownItems(editor);
    const action = registry.actions.get(item.command);
    expect(action).toBeDefined();
    expect(matches(editor, action?.precondition)).toBe(true);
    await action?.run(editor);
    expect(run).toHaveBeenCalledTimes(1);
    const request = run.mock.calls[0][0];
    expect(request).toMatchObject({ service: "demo.text.reverse", surface: "selection", input: "abc" });
    request.selection?.replace("cba");
    expect(editor.edits).toEqual([{ range: expect.objectContaining({ startColumn: 1, endColumn: 4 }), text: "cba" }]);
    request.selection?.insertBelow("more");
    expect(editor.edits.at(-1)).toEqual({ range: expect.objectContaining({ startLineNumber: 1, startColumn: 4 }), text: "\nmore" });
  });

  it("offers no replace target for a read-only editor", async () => {
    const editor = attach(fakeEditor("abc", { readOnly: true }));
    const [item] = shownItems(editor);
    await registry.actions.get(item.command)?.run(editor);
    expect(run.mock.calls[0][0].selection).toBeUndefined();
  });

  it("re-registers when the service list changes", async () => {
    const editor = attach(fakeEditor("abc"));
    services.value = [];
    await nextTick();
    expect(shownItems(editor)).toEqual([]);
    expect(registry.submenus).toEqual([]);
  });
});
