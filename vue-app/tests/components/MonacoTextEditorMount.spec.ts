import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";

// Creating the editor and then updating its options lays out a large file twice.
const fixture = vi.hoisted(() => {
  const disposable = { dispose() {} };
  const uri = { toString: () => "file:///p/big.ts" };
  const model = { uri, isDisposed: () => false, getValue: () => "", getLanguageId: () => "typescript", getLineCount: () => 1,
    getLineContent: () => "", updateOptions: vi.fn(), onDidChangeContent: () => ({ dispose() {} }), onDidChangeDecorations: () => ({ dispose() {} }), getDecorationsInRange: () => [], deltaDecorations: () => [], canUndo: () => false, canRedo: () => false, dispose() {} };
  const editor = { getModel: () => model, getValue: () => "", updateOptions: vi.fn(), addAction: () => disposable,
    onDidChangeModelContent: () => disposable, onDidChangeCursorSelection: () => disposable, onDidScrollChange: () => disposable,
    onDidBlurEditorWidget: () => disposable, onMouseDown: () => disposable, onContextMenu: () => disposable, deltaDecorations: () => [], createDecorationsCollection: () => ({ set() {}, clear() {} }), getLayoutInfo: () => ({ width: 0, height: 0 }), dispose() {} };
  return { disposable, uri, model, editor, create: vi.fn((_host: unknown, _options: unknown) => editor) };
});
vi.mock("@/services/monaco-runtime.js", () => ({
  editor: { create: fixture.create, createModel: () => fixture.model, getModel: () => null,
    MouseTargetType: { GUTTER_GLYPH_MARGIN: 2 }, TrackedRangeStickiness: { NeverGrowsWhenTypingAtEdges: 1 } },
  languages: { registerCompletionItemProvider: () => fixture.disposable },
  Uri: { file: () => fixture.uri, from: () => fixture.uri },
}));
vi.mock("@/services/monaco-formatters", () => ({ formatProjectModelOnSave: vi.fn() }));
vi.mock("@/services/monaco-keymap", () => ({ ensureMonacoKeymap() {}, registerMonacoEditor: () => fixture.disposable }));
vi.mock("@/services/monaco-languages", () => ({ detectMonacoEditorLanguage: () => "typescript", PLUGIN_LANGUAGES_CHANGED_EVENT: "fixture-languages" }));
vi.mock("@/services/ui-theme", () => ({ readStoredUiTheme: () => "light", resolveMonacoEditorTheme: () => "vs", UI_THEME_CHANGED_EVENT: "fixture-theme" }));
import MonacoTextEditor from "@/components/projects/MonacoTextEditor.vue";

const preferences = { fontSize: 15, tabSize: 4, wordWrap: "on" as const, minimap: false, lineNumbers: "relative" as const, stickyScroll: false };
let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  fixture.create.mockClear();
  fixture.editor.updateOptions.mockClear();
  fixture.model.updateOptions.mockClear();
});

describe("MonacoTextEditor mount", () => {
  it("creates the editor with the presentation preferences instead of updating it afterwards", async () => {
    wrapper = mount(MonacoTextEditor, { props: { modelValue: "", path: "/p/big.ts", projectRoot: "/p", kind: "project", preferences }, attachTo: document.body });
    await flushPromises();
    expect(fixture.create).toHaveBeenCalledTimes(1);
    expect(fixture.create.mock.calls[0]?.[1]).toMatchObject({ fontSize: 15, tabSize: 4, wordWrap: "on", minimap: { enabled: false }, lineNumbers: "relative", stickyScroll: { enabled: false } });
    expect(fixture.editor.updateOptions).not.toHaveBeenCalled();
    expect(fixture.model.updateOptions).toHaveBeenCalledWith({ tabSize: 4, insertSpaces: true });
  });

  it("still applies later preference changes", async () => {
    wrapper = mount(MonacoTextEditor, { props: { modelValue: "", path: "/p/big.ts", projectRoot: "/p", kind: "project", preferences }, attachTo: document.body });
    await flushPromises();
    await wrapper.setProps({ preferences: { ...preferences, fontSize: 17 } });
    expect(fixture.editor.updateOptions).toHaveBeenCalledWith(expect.objectContaining({ fontSize: 17 }));
  });
});
