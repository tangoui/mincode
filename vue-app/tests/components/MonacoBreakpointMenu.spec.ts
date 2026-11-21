import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import type { EditorDebugDecorationsOptions } from "@/composables/use-editor-debug-decorations";
import type { SourceBreakpoint } from "@/domain/debug/types";
import { useVueI18n } from "@/i18n/messages";

// Only the heavyweight Monaco runtime/model boundary is doubled. The SFC's
// menu target capture, menu labels, emitted actions and hover builder are real.
const fixture = vi.hoisted(() => {
  const disposable = { dispose() {} };
  const uri = { toString: () => "file:///p/app.py" };
  const model = { uri, isDisposed: () => false, getValue: () => "value = 1", getLanguageId: () => "python", getLineCount: () => 1,
    getLineContent: () => "value = 1", updateOptions() {}, canUndo: () => false, canRedo: () => false, dispose() {} };
  const editor = { getModel: () => model, getValue: () => "value = 1", updateOptions() {}, addAction: () => disposable,
    onDidChangeModelContent: () => disposable, onDidChangeCursorSelection: () => disposable, onDidScrollChange: () => disposable,
    onDidBlurEditorWidget: () => disposable, onMouseDown: () => disposable, getLayoutInfo: () => ({ width: 0, height: 0 }), dispose() {} };
  return { disposable, uri, model, editor, options: undefined as EditorDebugDecorationsOptions | undefined };
});
vi.mock("@/services/monaco-runtime.js", () => ({
  editor: { create: () => fixture.editor, createModel: () => fixture.model, getModel: () => null,
    MouseTargetType: { GUTTER_GLYPH_MARGIN: 2 }, TrackedRangeStickiness: { NeverGrowsWhenTypingAtEdges: 1 } },
  languages: { registerCompletionItemProvider: () => fixture.disposable },
  Uri: { file: () => fixture.uri, from: () => fixture.uri },
}));
vi.mock("@/services/monaco-formatters", () => ({ formatProjectModelOnSave: vi.fn() }));
vi.mock("@/services/monaco-keymap", () => ({ ensureMonacoKeymap() {}, registerMonacoEditor: () => fixture.disposable }));
vi.mock("@/services/monaco-languages", () => ({ detectMonacoEditorLanguage: () => "python", PLUGIN_LANGUAGES_CHANGED_EVENT: "fixture-languages" }));
vi.mock("@/services/ui-theme", () => ({ readStoredUiTheme: () => "light", resolveMonacoEditorTheme: () => "vs", UI_THEME_CHANGED_EVENT: "fixture-theme" }));
vi.mock("@/composables/use-editor-debug-decorations", async (original) => ({
  ...(await original<typeof import("@/composables/use-editor-debug-decorations")>()),
  useEditorDebugDecorations: (options: EditorDebugDecorationsOptions) => {
    fixture.options = options;
    return { refresh() {}, dispose() {}, captureMoves: () => [], newBreakpointLine: (line: number) => line + 100,
      lineInfo: () => ({ breakpointLine: options.state().breakpoints[0]?.line ?? null, entryLine: null }) };
  },
}));
import MonacoTextEditor from "@/components/projects/MonacoTextEditor.vue";

const ContextMenu = defineComponent({ setup(_, { slots }) { return () => h("div", slots.default?.()); } });
const Item = defineComponent({ emits: ["click"], setup(_, { slots, emit }) { return () => h("button", { onClick: () => emit("click") }, slots.default?.()); } });
let wrapper: VueWrapper | undefined;
afterEach(() => { wrapper?.unmount(); wrapper = undefined; fixture.options = undefined; });

async function editor(breakpoints: SourceBreakpoint[] = []) {
  wrapper = mount(MonacoTextEditor, { props: { modelValue: "value = 1", path: "/p/app.py", projectRoot: "/p", kind: "project", breakpoints,
    preferences: { fontSize: 13, tabSize: 2, wordWrap: "off", minimap: false, lineNumbers: "on" } },
    global: { stubs: { AppContextMenu: ContextMenu, ElDropdownItem: Item } }, attachTo: document.body });
  await flushPromises();
  if (!fixture.options) throw new Error("Monaco debug callbacks not installed");
  return { component: wrapper, callbacks: fixture.options };
}

async function clickLabel(text: string) {
  const button = wrapper?.findAll("button").find((node) => node.text() === text);
  if (!button) throw new Error(`Menu item not found: ${text}`);
  await button.trigger("click");
}

describe("Monaco breakpoint menu", () => {
  it("keeps a moved breakpoint target when closing its menu before disable/remove/edit", async () => {
    const { component, callbacks } = await editor([{ line: 3, enabled: true, logMessage: "value={value}" }]);
    const { t } = useVueI18n();
    callbacks.onGlyphContextMenu(10, { x: 1, y: 2 }); await flushPromises();
    await clickLabel(t("debug.editor.disableBreakpoint"));
    expect(component.emitted("set-breakpoint-enabled")?.[0]).toEqual([3, false]);
    callbacks.onGlyphContextMenu(10, { x: 1, y: 2 }); await flushPromises();
    await clickLabel(t("debug.editor.removeBreakpoint"));
    expect(component.emitted("remove-breakpoint")?.[0]).toEqual([3]);
    callbacks.onGlyphContextMenu(10, { x: 1, y: 2 }); await flushPromises();
    await clickLabel(t("debug.editor.editLogpoint"));
    expect(component.emitted("edit-breakpoint")?.[0]).toEqual([3, { x: 1, y: 2 }]);
  });

  it("offers a dedicated add-logpoint action on an empty glyph line", async () => {
    const { component, callbacks } = await editor();
    callbacks.onGlyphContextMenu(10, { x: 1, y: 2 }); await flushPromises();
    await clickLabel(useVueI18n().t("debug.editor.addLogpoint"));
    expect(component.emitted("edit-breakpoint")?.[0]).toEqual([110, { x: 1, y: 2 }, "logpoint"]);
  });

  it.each([
    "value=~~hidden~~ &lt;tag&gt; &copy;",
    "&amp;lt;tag&amp;gt; &#169; &#x41;",
    "literal A&B ~single~ ~~double~~",
    "\\~ **bold** [link](command:test) <b>text</b> `code` {value}",
  ])("renders the exact literal log message through Monaco Markdown: %s", async (message) => {
    const bp = { line: 3, enabled: true, logMessage: message };
    const { callbacks } = await editor([bp]);
    const labels = callbacks.labels?.();
    if (!labels) throw new Error("Hover builder not installed");
    // Use the parser shipped with this Monaco version, not a different marked
    // dependency or an assertion on our escaped intermediate string.
    const parserUrl = pathToFileURL(resolve(process.cwd(), "node_modules/monaco-editor/esm/vs/base/common/marked/marked.js")).href;
    const { parse } = await import(/* @vite-ignore */ parserUrl) as { parse: (text: string) => string };
    const rendered = document.createElement("div");
    rendered.innerHTML = parse(labels.breakpoint(bp, true));
    const expected = useVueI18n().t("debug.editor.logMessageHover", { message });
    expect([...rendered.querySelectorAll("p")].map((node) => node.textContent)).toContain(expected);
    expect(rendered.querySelector("del, s, b, strong, em, a, code, img")).toBeNull();
  });

  it("escapes logpoint text in hovers and localizes unsupported adapter feedback", async () => {
    const bp = { line: 3, enabled: true, logMessage: "value={value} <img> **literal**" };
    const { callbacks } = await editor([bp]);
    const labels = callbacks.labels?.();
    if (!labels) throw new Error("Hover builder not installed");
    const { t } = useVueI18n();
    const text = labels.breakpoint(bp, false, { message: "unsupported: logpoints" });
    expect(text).toContain(t("debug.editor.logpointsUnsupported"));
    expect(text).toContain("value=\\{value\\} \\<img\\> \\*\\*literal\\*\\*");
    expect(text).not.toContain("unsupported: logpoints");
  });
});
