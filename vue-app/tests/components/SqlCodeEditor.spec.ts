import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h, nextTick } from "vue";
import { EditorView, runScopeHandlers } from "@codemirror/view";
import { currentCompletions, startCompletion } from "@codemirror/autocomplete";
import { undo } from "@codemirror/commands";
import SqlCodeEditor from "@/components/database/query/SqlCodeEditor.vue";
import { buildKeybindingContext, keybindingFocusClass } from "@/domain/keybindings/context";
import type { SqlCompletionItem } from "@/domain/database/sql-completion";
import type { ProjectEditorViewState } from "@/domain/projects/types";
const clipboard = vi.hoisted(() => ({ text: "", copy: vi.fn(), read: vi.fn() }));
vi.mock("@/services/desktop-bridge", async (original) => ({
  ...(await original<typeof import("@/services/desktop-bridge")>()),
  copyText: async (text: string) => { clipboard.copy(text); clipboard.text = text; },
  readText: async () => { clipboard.read(); return clipboard.text; },
}));

// Element Plus menus need a live popper: these stubs render every item and forward clicks.
const DropdownItem = defineComponent({
  inheritAttrs: false,
  props: { disabled: Boolean },
  setup(props, { attrs, slots }) {
    return () => h("li", { role: "menuitem", "aria-disabled": String(props.disabled), ...attrs }, slots.default?.());
  },
});
const ContextMenu = defineComponent({ setup(_, { slots }) { return () => h("ul", { class: "context-menu-stub" }, slots.default?.()); } });
const menuStubs = { AppContextMenu: ContextMenu, ElDropdownItem: DropdownItem, "el-dropdown-item": DropdownItem };

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.useRealTimers();
});

const completions: SqlCompletionItem[] = [
  { label: "SELECT", insertText: "SELECT", kind: "keyword", sortText: "1-00" },
  { label: "users", insertText: "users", kind: "table", detail: "table", sortText: "2-users" },
  { label: "user_id", insertText: "user_id", kind: "column", detail: "users.int", sortText: "2-user_id" },
  { label: "SELECT … FROM", insertText: "SELECT ${1:*}\nFROM ${2:table}", kind: "snippet", sortText: "0-select" },
];

function mountEditor(props: Partial<InstanceType<typeof SqlCodeEditor>["$props"]> = {}) {
  wrapper = mount(SqlCodeEditor, {
    props: { modelValue: "SELECT 1;", path: "tab-1.sql", fontSize: 13, dbType: "mysql", sqlCompletions: completions, ...props },
    attachTo: document.body,
    global: { stubs: menuStubs },
  });
  const view = EditorView.findFromDOM(wrapper.find(".db-sql-code-editor").element as HTMLElement);
  if (!view) throw new Error("no editor view");
  return view;
}

function type(view: EditorView, text: string) {
  const at = view.state.selection.main.head;
  view.dispatch({ changes: { from: at, insert: text }, selection: { anchor: at + text.length }, userEvent: "input.type" });
}

describe("SqlCodeEditor", () => {
  it("shows the query and reports edits, but not values pushed down by the parent", async () => {
    const view = mountEditor();
    expect(view.state.doc.toString()).toBe("SELECT 1;");
    view.dispatch({ selection: { anchor: view.state.doc.length } });
    type(view, " -- x");
    expect(wrapper!.emitted("update:modelValue")?.at(-1)).toEqual(["SELECT 1; -- x"]);
    const before = wrapper!.emitted("update:modelValue")!.length;
    await wrapper!.setProps({ modelValue: "SELECT 2;" });
    expect(view.state.doc.toString()).toBe("SELECT 2;");
    expect(wrapper!.emitted("update:modelValue")!.length).toBe(before);
  });

  it("keeps a value from the parent out of the undo history", async () => {
    const view = mountEditor();
    type(view, "\n");
    await wrapper!.setProps({ modelValue: "SELECT 2;" });
    undo(view);
    // Undo reverts the user's edit, not the parent's replacement of the whole text.
    expect(view.state.doc.toString()).not.toBe("SELECT 1;");
  });

  it("runs with Mod-Enter and reports the selection first, for select-and-run", async () => {
    const view = mountEditor({ modelValue: "SELECT 1;\nSELECT 2;" });
    view.dispatch({ selection: { anchor: 10, head: 19 } });
    // The editor's own keymap (Mod is ⌘ on macOS, Ctrl elsewhere).
    const mod = /Mac/.test(navigator.platform) ? { metaKey: true } : { ctrlKey: true };
    expect(runScopeHandlers(view, new KeyboardEvent("keydown", { key: "Enter", ...mod }), "editor")).toBe(true);
    await nextTick();
    expect(wrapper!.emitted("run")).toHaveLength(1);
    const [path, state] = wrapper!.emitted("view-state")!.at(-1) as [string, ProjectEditorViewState];
    expect(path).toBe("tab-1.sql");
    expect([state.selectionStartLineNumber, state.selectionStartColumn, state.positionLineNumber, state.positionColumn]).toEqual([2, 1, 2, 10]);
  });

  it("restores the selection of a saved view state", () => {
    const viewState: ProjectEditorViewState = {
      lineNumber: 1, column: 7, selectionStartLineNumber: 1, selectionStartColumn: 1,
      positionLineNumber: 1, positionColumn: 7, scrollTop: 0, scrollLeft: 0,
    };
    const view = mountEditor({ viewState });
    expect(view.state.selection.main.anchor).toBe(0);
    expect(view.state.selection.main.head).toBe(6);
  });

  it("completes tables and columns from the catalog for the word before the cursor", async () => {
    const view = mountEditor({ modelValue: "SELECT * FROM us" });
    view.dispatch({ selection: { anchor: view.state.doc.length } });
    startCompletion(view);
    await flushPromises();
    await new Promise((resolve) => setTimeout(resolve, 80));
    const labels = currentCompletions(view.state).map((option) => option.label);
    expect(labels).toEqual(expect.arrayContaining(["users", "user_id"]));
  });

  it("a new query tab is a new document with its own undo history", async () => {
    const view = mountEditor();
    type(view, " -- edited");
    await wrapper!.setProps({ path: "tab-2.sql", modelValue: "SELECT 9;" });
    expect(view.state.doc.toString()).toBe("SELECT 9;");
    undo(view);
    expect(view.state.doc.toString()).toBe("SELECT 9;");
  });

  it("follows the font size", async () => {
    const view = mountEditor();
    // EditorView.theme writes a scoped rule into the document: the size shows up there.
    const sizeRules = () => [...document.querySelectorAll("style")].map((style) => style.textContent ?? "").join("\n");
    expect(sizeRules()).toContain("font-size: 13px");
    await wrapper!.setProps({ fontSize: 18 });
    await nextTick();
    expect(sizeRules()).toContain("font-size: 18px");
    expect(view.dom.isConnected).toBe(true);
  });

  it("counts as SQL editor focus for the app keymap", () => {
    const view = mountEditor();
    const target = view.contentDOM;
    expect(keybindingFocusClass(target)).toBe("monaco");
    const context = buildKeybindingContext({ target, platform: "mac" });
    expect(context["editor.focus"]).toBe(true);
    expect(context["editor.kind"]).toBe("sql");
  });

  it("stops providing the editor kind once unmounted", () => {
    const view = mountEditor();
    const target = view.contentDOM;
    wrapper!.unmount();
    wrapper = undefined;
    expect(buildKeybindingContext({ target, platform: "mac" })["editor.kind"]).toBeUndefined();
  });
});

describe("SqlCodeEditor context menu", () => {
  beforeEach(() => {
    // Browsers queue selectionchange as a task; happy-dom dispatches it during
    // setBaseAndExtent(), reentering CodeMirror before its update is finished.
    const dispatch = document.dispatchEvent.bind(document);
    vi.spyOn(document, "dispatchEvent").mockImplementation((event) => {
      if (event.type !== "selectionchange") return dispatch(event);
      setTimeout(() => dispatch(event), 0);
      return true;
    });
    clipboard.copy.mockReset();
    clipboard.read.mockReset();
    clipboard.text = "";
  });
  afterEach(() => {
    vi.mocked(document.dispatchEvent).mockRestore();
  });

  async function openMenu(view: EditorView) {
    // happy-dom has no layout: make the click land on the cursor (inside the selection), as a
    // right click on the selected text does in the WebView.
    const head = view.state.selection.main.head;
    vi.spyOn(view, "posAtCoords").mockReturnValue(head);
    const event = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 });
    view.contentDOM.dispatchEvent(event);
    await flushPromises();
    expect(event.defaultPrevented).toBe(true);
    return wrapper!.find(".context-menu-stub");
  }
  const item = (label: string) => wrapper!.findAll("[role=menuitem]").find((entry) => entry.text().includes(label));

  it("disables cut and copy without a selection", async () => {
    const view = mountEditor();
    view.dispatch({ selection: { anchor: 0 } });
    await openMenu(view);
    expect(item("剪切")!.attributes("aria-disabled")).toBe("true");
    expect(item("复制")!.attributes("aria-disabled")).toBe("true");
  });

  it("cuts, copies, pastes and selects all through the clipboard", async () => {
    const view = mountEditor({ modelValue: "SELECT 1; SELECT 2;" });
    view.dispatch({ selection: { anchor: 0, head: 9 } });
    await openMenu(view);
    await item("复制")!.trigger("click");
    await flushPromises();
    expect(clipboard.copy).toHaveBeenLastCalledWith("SELECT 1;");

    view.dispatch({ selection: { anchor: 0, head: 9 } });
    await openMenu(view);
    await item("剪切")!.trigger("click");
    await flushPromises();
    expect(view.state.doc.toString()).toBe(" SELECT 2;");

    view.dispatch({ selection: { anchor: 0 } });
    await openMenu(view);
    await item("粘贴")!.trigger("click");
    await flushPromises();
    expect(view.state.doc.toString()).toBe("SELECT 1; SELECT 2;");

    await openMenu(view);
    await item("全选")!.trigger("click");
    await flushPromises();
    expect([view.state.selection.main.from, view.state.selection.main.to]).toEqual([0, view.state.doc.length]);
  });

  it("a right click outside the selection moves the cursor there first", async () => {
    const view = mountEditor({ modelValue: "SELECT 1; SELECT 2;" });
    view.dispatch({ selection: { anchor: 0, head: 6 } });
    vi.spyOn(view, "posAtCoords").mockReturnValue(15);
    view.contentDOM.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 50, clientY: 5 }));
    await flushPromises();
    expect([view.state.selection.main.from, view.state.selection.main.to]).toEqual([15, 15]);
    expect(item("Explain with plugin")).toBeUndefined();
  });

  it("runs the current statement, runs all, and formats from the menu", async () => {
    const view = mountEditor();
    await openMenu(view);
    await item("执行选中或当前语句")!.trigger("click");
    await openMenu(view);
    await item("执行全部 SQL")!.trigger("click");
    await openMenu(view);
    await item("格式化 SQL")!.trigger("click");
    await flushPromises();
    expect(wrapper!.emitted("run")).toEqual([[], ["all"]]);
    expect(wrapper!.emitted("format")).toHaveLength(1);
  });
});
