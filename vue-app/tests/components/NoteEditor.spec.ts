import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { ElMessage, ElSelect } from "element-plus";
import type { Note } from "@/domain/notes/types";

const service = vi.hoisted(() => ({
  getNote: vi.fn(),
  updateNote: vi.fn(),
  listNoteTags: vi.fn(),
  setNoteTags: vi.fn(),
  deleteNote: vi.fn(),
  removeNoteLink: vi.fn(),
}));

// 编辑器替身：记录 apply / insertImage 调用，避免在 happy-dom 中驱动 CodeMirror。
const editor = vi.hoisted(() => ({ apply: vi.fn(), insertImage: vi.fn() }));

vi.mock("@/services/notes-service", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...service,
}));

vi.mock("@/components/notes/NoteMarkdownEditor.vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    default: defineComponent({
      name: "NoteMarkdownEditor",
      props: { modelValue: { type: String, required: true }, documentKey: { type: String, required: true } },
      emits: ["update:modelValue", "upload-error"],
      setup(props, { expose }) {
        expose({ apply: editor.apply, insertImage: editor.insertImage, focus: () => {} });
        return () => h("div", { "data-testid": "note-markdown-editor" }, props.modelValue);
      },
    }),
  };
});

// 关联栏的快照来自各 app store；这里用固定快照，只验证编辑区的显示与操作。
const linkTargets = vi.hoisted(() => ({
  snapshot: {
    ssh: new Map([["ssh-1", "prod web"]]),
    database: new Map(),
    http: new Map(),
    httpCollections: new Map(),
    currentProjects: new Map(),
    otherWorkspaceProjects: new Map([["scope-b", ["/repo/b"]]]),
    currentAi: new Map(),
    currentScopeId: "scope-a",
    liveScopeIds: new Set(["scope-a", "scope-b"]),
    workspaceNames: new Map([["scope-b", "Beta"]]),
    unavailable: new Set(),
  },
  openLinkTarget: vi.fn(),
}));
vi.mock("@/composables/use-note-link-snapshot", async () => {
  const { computed } = await import("vue");
  return {
    useLinkObjectSnapshot: () => computed(() => linkTargets.snapshot),
    openLinkTarget: linkTargets.openLinkTarget,
  };
});

import NoteEditor from "@/components/notes/NoteEditor.vue";
import { useNotesStore } from "@/stores/notes";

function note(id: string, body = `${id} body`): Note {
  return { id, folderId: "", title: `${id} title`, favorite: false, position: 1, updatedAt: 1_700_000_000_000, tags: [], body, createdAt: 1, links: [] };
}

async function mountEditor(props: { noteId: string; compact?: boolean } = { noteId: "n1" }) {
  const wrapper = mount(NoteEditor, { props, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

function buttonByLabel(wrapper: Awaited<ReturnType<typeof mountEditor>>, label: string) {
  return wrapper.find(`button[aria-label="${label}"]`);
}

beforeEach(() => {
  for (const fn of [...Object.values(service), ...Object.values(editor), linkTargets.openLinkTarget]) fn.mockReset();
  service.getNote.mockImplementation(async (id: string) => note(id));
  service.updateNote.mockImplementation(async (input: { id: string; favorite?: boolean }) => ({ ...note(input.id), favorite: input.favorite ?? false, updatedAt: 2 }));
  service.listNoteTags.mockResolvedValue([{ name: "go", count: 2 }]);
  service.setNoteTags.mockImplementation(async (id: string, tags: string[]) => ({ ...note(id), tags, updatedAt: 3 }));
});

describe("NoteEditor", () => {
  it("offers only source and preview in compact mode and remembers it separately from the notes page", async () => {
    localStorage.setItem("notes.editorMode", "split");
    const compact = await mountEditor({ noteId: "n1", compact: true });
    const values = compact.findAll('input[type="radio"]').map((input) => input.attributes("value"));
    expect(values).toEqual(["source", "preview"]);
    expect(compact.find('[data-testid="note-markdown-editor"]').exists()).toBe(true);
    expect(compact.find(".note-preview").exists()).toBe(false);

    await compact.find('input[type="radio"][value="preview"]').setValue(true);
    expect(localStorage.getItem("notes.drawerEditorMode")).toBe("preview");
    expect(localStorage.getItem("notes.editorMode")).toBe("split");
    compact.unmount();
  });

  it("renders only the preview in preview mode and remembers the mode after remount", async () => {
    const wrapper = await mountEditor();
    expect(wrapper.find('[data-testid="note-markdown-editor"]').exists()).toBe(true);

    await wrapper.find('input[type="radio"][value="preview"]').setValue(true);

    expect(wrapper.find('[data-testid="note-markdown-editor"]').exists()).toBe(false);
    expect(wrapper.find(".note-preview").exists()).toBe(true);
    expect(localStorage.getItem("notes.editorMode")).toBe("preview");
    wrapper.unmount();

    const again = await mountEditor();
    expect(again.find('[data-testid="note-markdown-editor"]').exists()).toBe(false);
    expect(again.find(".note-preview").exists()).toBe(true);
    again.unmount();
  });

  it("falls back to split mode for an invalid stored mode", async () => {
    localStorage.setItem("notes.editorMode", "bogus");
    const wrapper = await mountEditor();

    expect(wrapper.find('[data-testid="note-markdown-editor"]').exists()).toBe(true);
    expect(wrapper.find(".note-preview").exists()).toBe(true);
    wrapper.unmount();
  });

  it("applies bold through the editor when clicking B", async () => {
    const wrapper = await mountEditor();

    await buttonByLabel(wrapper, "加粗").trigger("click");

    expect(editor.apply).toHaveBeenCalledWith("bold");
    wrapper.unmount();
  });

  it("disables format buttons in preview mode", async () => {
    localStorage.setItem("notes.editorMode", "preview");
    const wrapper = await mountEditor();

    expect(buttonByLabel(wrapper, "加粗").attributes("disabled")).toBeDefined();
    expect(buttonByLabel(wrapper, "图片").attributes("disabled")).toBeDefined();
    wrapper.unmount();
  });

  it("edits the title through the store", async () => {
    const store = useNotesStore();
    const editNote = vi.spyOn(store, "editNote");
    const wrapper = await mountEditor();

    await wrapper.find('input[aria-label="笔记标题"]').setValue("New title");

    expect(editNote).toHaveBeenCalledWith("n1", { title: "New title" });
    expect(wrapper.find('input[aria-label="笔记标题"]').attributes("maxlength")).toBe("500");
    wrapper.unmount();
  });

  it("edits the body through the store", async () => {
    const store = useNotesStore();
    const editNote = vi.spyOn(store, "editNote");
    const wrapper = await mountEditor();

    wrapper.findComponent({ name: "NoteMarkdownEditor" }).vm.$emit("update:modelValue", "typed");

    expect(editNote).toHaveBeenCalledWith("n1", { body: "typed" });
    wrapper.unmount();
  });

  it("flushes the previous note before opening the next one", async () => {
    const store = useNotesStore();
    const calls: string[] = [];
    vi.spyOn(store, "flushNote").mockImplementation(async (id) => void calls.push(`flush:${id}`));
    const openNote = store.openNote;
    vi.spyOn(store, "openNote").mockImplementation((id) => {
      calls.push(`open:${id}`);
      return openNote(id);
    });
    const wrapper = await mountEditor();
    calls.length = 0;

    await wrapper.setProps({ noteId: "n2" });
    await flushPromises();

    expect(calls.slice(0, 2)).toEqual(["flush:n1", "open:n2"]);
    expect(wrapper.find('input[aria-label="笔记标题"]').element).toHaveProperty("value", "n2 title");
    wrapper.unmount();
  });

  it("flushes the note on unmount", async () => {
    const store = useNotesStore();
    const flushNote = vi.spyOn(store, "flushNote");
    const wrapper = await mountEditor();

    wrapper.unmount();

    expect(flushNote).toHaveBeenCalledWith("n1");
  });

  it("shows a retry button on save errors that flushes the note", async () => {
    const store = useNotesStore();
    const wrapper = await mountEditor();
    const flushNote = vi.spyOn(store, "flushNote");
    expect(wrapper.text()).not.toContain("重试");

    store.saveErrors = { n1: "磁盘已满" };
    await flushPromises();

    expect(wrapper.text()).toContain("磁盘已满");
    const retry = wrapper.findAll("button").find((button) => button.text() === "重试");
    expect(retry).toBeDefined();
    await retry!.trigger("click");
    expect(flushNote).toHaveBeenCalledWith("n1");
    wrapper.unmount();
  });

  it("keeps the save error and retry outside the scrolling toolbar in compact mode", async () => {
    const store = useNotesStore();
    const wrapper = await mountEditor({ noteId: "n1", compact: true });
    const flushNote = vi.spyOn(store, "flushNote");

    store.saveErrors = { n1: "磁盘已满" };
    await flushPromises();

    const status = wrapper.find(".note-editor__status");
    expect(status.exists()).toBe(true);
    expect(status.text()).toContain("磁盘已满");
    expect(wrapper.find(".note-editor--compact .note-editor__tools").exists()).toBe(true);
    expect(wrapper.find(".note-editor__tools .note-editor__status").exists()).toBe(false);
    const retry = status.findAll("button").find((button) => button.text() === "重试");
    expect(retry).toBeDefined();
    await retry!.trigger("click");
    expect(flushNote).toHaveBeenCalledWith("n1");
    wrapper.unmount();
  });

  it("shows saving state while a save is in flight", async () => {
    const store = useNotesStore();
    const wrapper = await mountEditor();

    store.saving = { n1: true };
    await flushPromises();

    expect(wrapper.text()).toContain("保存中…");
    wrapper.unmount();
  });

  it("toggles favorite through the store", async () => {
    const store = useNotesStore();
    const setFavorite = vi.spyOn(store, "setFavorite");
    const wrapper = await mountEditor();

    await buttonByLabel(wrapper, "收藏").trigger("click");
    await flushPromises();

    expect(setFavorite).toHaveBeenCalledWith("n1", true);
    expect(buttonByLabel(wrapper, "取消收藏").exists()).toBe(true);
    wrapper.unmount();
  });

  it("emits toggle-sidebar and hides that button in compact mode", async () => {
    const wrapper = await mountEditor();
    await buttonByLabel(wrapper, "折叠左栏").trigger("click");
    expect(wrapper.emitted("toggle-sidebar")).toHaveLength(1);
    wrapper.unmount();

    const compact = await mountEditor({ noteId: "n1", compact: true });
    expect(buttonByLabel(compact, "折叠左栏").exists()).toBe(false);
    expect(compact.find(".note-editor--compact").exists()).toBe(true);
    compact.unmount();
  });

  it("inserts each chosen image through the editor and reports upload errors", async () => {
    const error = vi.spyOn(ElMessage, "error").mockImplementation(() => ({ close: () => {} }) as never);
    const wrapper = await mountEditor();
    const input = wrapper.find<HTMLInputElement>('input[type="file"]');
    const files = [new File(["a"], "a.png", { type: "image/png" }), new File(["b"], "b.webp", { type: "image/webp" })];
    Object.defineProperty(input.element, "files", { value: files, configurable: true });

    await input.trigger("change");
    await flushPromises();

    expect(editor.insertImage.mock.calls.map(([file]) => (file as File).name)).toEqual(["a.png", "b.webp"]);
    wrapper.findComponent({ name: "NoteMarkdownEditor" }).vm.$emit("upload-error", "图片过大");
    expect(error).toHaveBeenCalledWith("图片过大");
    wrapper.unmount();
  });

  it("saves tag changes through the store", async () => {
    const store = useNotesStore();
    const setTags = vi.spyOn(store, "setTags");
    const wrapper = await mountEditor();

    wrapper.findComponent(ElSelect).vm.$emit("update:modelValue", ["go", "new"]);
    await flushPromises();

    expect(setTags).toHaveBeenCalledWith("n1", ["go", "new"]);
    expect(store.loadedNotes.n1!.tags).toEqual(["go", "new"]);
    wrapper.unmount();
  });

  it("shows the not-found state when the open note is deleted", async () => {
    const store = useNotesStore();
    const wrapper = await mountEditor();
    expect(wrapper.find('input[aria-label="笔记标题"]').element).toHaveProperty("value", "n1 title");

    service.getNote.mockRejectedValue(new Error("note_not_found"));
    await store.deleteNote("n1");
    await flushPromises();

    expect(wrapper.text()).toContain("无法打开笔记：笔记不存在，可能已被删除");
    expect(wrapper.text()).not.toContain("正在打开笔记…");
    wrapper.unmount();
  });

  it("shows linked objects with live names, other-workspace and deleted states", async () => {
    const link = (objectType: "ssh" | "project", objectId: string, nameSnapshot: string) => ({
      objectType, objectId, parentId: "", scopeId: "", nameSnapshot, scopeNameSnapshot: "", createdAt: 1,
    });
    service.getNote.mockResolvedValue({
      ...note("n1"),
      links: [link("ssh", "ssh-1", "old name"), link("project", "/repo/b", "b"), link("ssh", "ssh-gone", "gone box")],
    });
    service.removeNoteLink.mockResolvedValue(undefined);
    const wrapper = await mountEditor();

    const tags = wrapper.findAll(".note-editor__link");
    expect(tags.map((tag) => tag.text())).toEqual([
      "SSH · prod web",
      "项目 · b（其他工作区：Beta）",
      "SSH · gone box已删除",
    ]);
    expect(tags[2]!.classes()).toContain("is-deleted");

    await tags[0]!.find(".note-editor__link-body").trigger("click");
    expect(linkTargets.openLinkTarget).toHaveBeenCalledTimes(1);
    await tags[2]!.find(".note-editor__link-body").trigger("click");
    expect(linkTargets.openLinkTarget).toHaveBeenCalledTimes(1);

    // 已删除的关联可以移除。
    await tags[2]!.find(".el-tag__close").trigger("click");
    await flushPromises();
    expect(service.removeNoteLink).toHaveBeenCalledWith("n1", "ssh", "ssh-gone", "");
    expect(wrapper.findAll(".note-editor__link")).toHaveLength(2);
    wrapper.unmount();
  });

  it("shows links whose source failed to load as unknown: snapshot name, hint, not clickable, not struck", async () => {
    linkTargets.snapshot.unavailable = new Set(["database"]);
    try {
      service.getNote.mockResolvedValue({
        ...note("n1"),
        links: [{ objectType: "database", objectId: "db-1", parentId: "", scopeId: "", nameSnapshot: "orders", scopeNameSnapshot: "", createdAt: 1 }],
      });
      const wrapper = await mountEditor();
      const tag = wrapper.find(".note-editor__link");
      expect(tag.classes()).toContain("is-unknown");
      expect(tag.classes()).not.toContain("is-deleted");
      // 状态不只靠颜色：显示「状态未知」文字。
      expect(tag.text()).toBe("数据库 · orders状态未知");
      const body = tag.find(".note-editor__link-body");
      expect(body.attributes("title")).toContain("暂时无法确认对象状态");
      expect(body.attributes("role")).toBeUndefined();
      await body.trigger("click");
      expect(linkTargets.openLinkTarget).not.toHaveBeenCalled();
      wrapper.unmount();
    } finally {
      linkTargets.snapshot.unavailable = new Set();
    }
  });
});
