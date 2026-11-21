import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { EditorView } from "@codemirror/view";
import { undo } from "@codemirror/commands";

const service = vi.hoisted(() => ({ putNoteAsset: vi.fn() }));

vi.mock("@/services/notes-service", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  putNoteAsset: service.putNoteAsset,
}));

import NoteMarkdownEditor from "@/components/notes/NoteMarkdownEditor.vue";

const REF = `/__notes-assets/${"b".repeat(64)}.png`;

function mountEditor(modelValue: string, documentKey = "note-1") {
  return mount(NoteMarkdownEditor, { props: { modelValue, documentKey }, attachTo: document.body });
}

function viewOf(wrapper: ReturnType<typeof mountEditor>) {
  const view = EditorView.findFromDOM(wrapper.element.querySelector(".cm-editor") as HTMLElement);
  if (!view) throw new Error("editor view not mounted");
  return view;
}

function lastValue(wrapper: ReturnType<typeof mountEditor>) {
  const events = wrapper.emitted("update:modelValue") ?? [];
  return events.at(-1)?.[0] as string | undefined;
}

function paste(view: EditorView, files: File[], text = "") {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: { files, types: text ? ["text/plain", "Files"] : ["Files"], items: [], getData: (type: string) => (/^text/i.test(type) ? text : "") },
  });
  view.contentDOM.dispatchEvent(event);
  return event;
}

const png = () => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "shot.png", { type: "image/png" });

describe("NoteMarkdownEditor", () => {
  it("wraps the selection in ** when applying bold", () => {
    const wrapper = mountEditor("hello world");
    const view = viewOf(wrapper);
    view.dispatch({ selection: { anchor: 0, head: 5 } });

    (wrapper.vm as unknown as { apply(kind: string): void }).apply("bold");

    expect(lastValue(wrapper)).toBe("**hello** world");
    wrapper.unmount();
  });

  it("uploads a pasted image and inserts its reference at the cursor", async () => {
    service.putNoteAsset.mockResolvedValue(REF);
    const wrapper = mountEditor("start ");
    const view = viewOf(wrapper);
    view.dispatch({ selection: { anchor: 6 } });

    const event = paste(view, [png()]);
    await flushPromises();

    expect(event.defaultPrevented).toBe(true);
    expect(service.putNoteAsset).toHaveBeenCalledTimes(1);
    // 文件名（去掉扩展名）作为替代文字。
    expect(lastValue(wrapper)).toBe(`start ![shot](${REF})`);
    wrapper.unmount();
  });

  it("pastes text instead of uploading when the clipboard also has text", async () => {
    const wrapper = mountEditor("");
    const view = viewOf(wrapper);

    paste(view, [png()], "hello from office");
    await flushPromises();

    expect(service.putNoteAsset).not.toHaveBeenCalled();
    expect(view.state.doc.toString()).toBe("hello from office");
    wrapper.unmount();
  });

  it("refuses to insert images while disabled", async () => {
    service.putNoteAsset.mockResolvedValue(REF);
    const wrapper = mountEditor("text");
    await wrapper.setProps({ disabled: true });

    await (wrapper.vm as unknown as { insertImage(file: File): Promise<void> }).insertImage(png());

    expect(service.putNoteAsset).not.toHaveBeenCalled();
    expect(viewOf(wrapper).state.doc.toString()).toBe("text");
    wrapper.unmount();
  });

  it("drops an upload that finishes after the editor became disabled", async () => {
    let finish!: (ref: string) => void;
    service.putNoteAsset.mockReturnValue(new Promise<string>((resolve) => { finish = resolve; }));
    const wrapper = mountEditor("text");

    paste(viewOf(wrapper), [png()]);
    await wrapper.setProps({ disabled: true });
    finish(REF);
    await flushPromises();

    expect(viewOf(wrapper).state.doc.toString()).toBe("text");
    expect(wrapper.emitted("update:modelValue")).toBeUndefined();
    expect(wrapper.emitted("upload-error")).toBeUndefined();
    wrapper.unmount();
  });

  it("emits upload-error when storing the image fails", async () => {
    service.putNoteAsset.mockRejectedValue(new Error("note_asset_type"));
    const wrapper = mountEditor("text");

    paste(viewOf(wrapper), [png()]);
    await flushPromises();

    const errors = wrapper.emitted("upload-error") ?? [];
    expect(errors).toHaveLength(1);
    expect(errors[0][0]).toMatch(/PNG/);
    expect(wrapper.emitted("update:modelValue")).toBeUndefined();
    wrapper.unmount();
  });

  it("does not insert an upload into a different document", async () => {
    let finish!: (ref: string) => void;
    service.putNoteAsset.mockReturnValue(new Promise<string>((resolve) => { finish = resolve; }));
    const wrapper = mountEditor("first", "a");

    paste(viewOf(wrapper), [png()]);
    await wrapper.setProps({ documentKey: "b", modelValue: "second" });
    finish(REF);
    await flushPromises();

    expect(viewOf(wrapper).state.doc.toString()).toBe("second");
    expect(wrapper.emitted("update:modelValue")).toBeUndefined();
    wrapper.unmount();
  });

  it("resets undo history when the document key changes", async () => {
    const wrapper = mountEditor("first", "a");
    const view = viewOf(wrapper);
    view.dispatch({ changes: { from: 5, insert: " edit" }, userEvent: "input.type" });
    expect(lastValue(wrapper)).toBe("first edit");

    await wrapper.setProps({ documentKey: "b", modelValue: "second" });
    const current = viewOf(wrapper);
    undo(current);

    expect(current.state.doc.toString()).toBe("second");
    wrapper.unmount();
  });

  it("applies parent value changes without echoing them back", async () => {
    const wrapper = mountEditor("one");
    await wrapper.setProps({ modelValue: "two" });
    expect(viewOf(wrapper).state.doc.toString()).toBe("two");
    expect(wrapper.emitted("update:modelValue")).toBeUndefined();
    wrapper.unmount();
  });

  it("uses a sanitized file name without extension as the image alt text", async () => {
    service.putNoteAsset.mockResolvedValue(REF);
    const wrapper = mountEditor("");
    const file = new File([new Uint8Array([1])], "a [b] (c)\nd.final.png", { type: "image/png" });
    await (wrapper.vm as unknown as { insertImage(file: File): Promise<void> }).insertImage(file);
    expect(lastValue(wrapper)).toBe(`![a b c d.final](${REF})`);
    wrapper.unmount();
  });

  it("inserts a link with Mod-Shift-k and leaves Mod-k to the command palette", () => {
    const wrapper = mountEditor("site");
    const view = viewOf(wrapper);
    view.dispatch({ selection: { anchor: 0, head: 4 } });
    const mac = /Mac|iPhone|iPad/.test(navigator.platform);
    const press = (shiftKey: boolean) => {
      const event = new KeyboardEvent("keydown", {
        key: shiftKey ? "K" : "k", keyCode: 75, shiftKey, metaKey: mac, ctrlKey: !mac, bubbles: true, cancelable: true,
      });
      view.contentDOM.dispatchEvent(event);
      return event;
    };

    expect(press(false).defaultPrevented).toBe(false);
    expect(lastValue(wrapper)).toBeUndefined();
    expect(press(true).defaultPrevented).toBe(true);
    expect(lastValue(wrapper)).toContain("[site](");
    wrapper.unmount();
  });
});
