import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { ElMessage } from "element-plus";
import type { Note, NoteSummary } from "@/domain/notes/types";
import { canPopOutTab } from "@/services/popup-tab";

const service = vi.hoisted(() => ({
  createNote: vi.fn(),
  addNoteLink: vi.fn(),
  listNotesForObject: vi.fn(),
  countNoteLinks: vi.fn(),
  getNote: vi.fn(),
  removeNoteLink: vi.fn(),
  deleteNote: vi.fn(),
}));

vi.mock("@/services/notes-service", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...service,
}));

// 编辑区替身：只显示正在编辑的笔记 id，避免在 happy-dom 中加载 CodeMirror。
vi.mock("@/components/notes/NoteEditor.vue", () => ({
  default: defineComponent({
    name: "NoteEditor",
    props: { noteId: { type: String, required: true }, compact: Boolean },
    setup: (props) => () => h("div", { "data-testid": "note-editor", "data-compact": String(props.compact) }, props.noteId),
  }),
}));

import LinkedNotesDrawer from "@/components/notes/LinkedNotesDrawer.vue";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";
import { useNotesStore } from "@/stores/notes";

function note(id: string): Note {
  return { id, folderId: "", title: "", favorite: false, position: 1, updatedAt: 1, tags: [], body: "", createdAt: 1, links: [] };
}

function summary(id: string, title: string): NoteSummary {
  return { id, folderId: "", title, favorite: false, position: 1, updatedAt: 1, tags: [] };
}

async function mountDrawer() {
  const empty = defineComponent({ render: () => null });
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "dashboard", component: empty },
      { path: "/notes", name: "notes", component: empty },
    ],
  });
  await router.push("/");
  const wrapper = mount(LinkedNotesDrawer, { global: { plugins: [router] }, attachTo: document.body });
  return { wrapper, router };
}

const target = { objectType: "ssh", objectId: "p1", nameSnapshot: "prod" } as const;

beforeEach(() => {
  for (const fn of Object.values(service)) fn.mockReset();
  service.createNote.mockResolvedValue(note("n-new"));
  service.addNoteLink.mockImplementation(async (_id: string, link: object) => ({ parentId: "", scopeId: "", nameSnapshot: "", scopeNameSnapshot: "", ...link, createdAt: 1 }));
  service.listNotesForObject.mockResolvedValue([summary("n-new", ""), summary("n-old", "旧笔记")]);
  service.countNoteLinks.mockResolvedValue({});
  service.getNote.mockImplementation(async (id: string) => ({ ...note(id), title: id === "n-old" ? "旧笔记" : "" }));
  service.removeNoteLink.mockResolvedValue(undefined);
});

describe("LinkedNotesDrawer", () => {
  it("creates a note, links it to the object and shows it", async () => {
    const { wrapper } = await mountDrawer();
    const drawer = useLinkedNotesDrawer();

    await drawer.open(target, { createNew: true });
    await flushPromises();

    expect(service.createNote).toHaveBeenCalledTimes(1);
    expect(service.addNoteLink).toHaveBeenCalledWith("n-new", {
      objectType: "ssh",
      objectId: "p1",
      parentId: "",
      scopeId: "",
      nameSnapshot: "prod",
      scopeNameSnapshot: "",
    });
    expect(service.createNote.mock.invocationCallOrder[0]!).toBeLessThan(service.addNoteLink.mock.invocationCallOrder[0]!);
    expect(service.listNotesForObject).toHaveBeenLastCalledWith("ssh", "p1", undefined);
    expect(document.body.textContent).toContain("prod 的笔记");
    expect(document.body.textContent).toContain("旧笔记");
    const editor = document.querySelector('[data-testid="note-editor"]');
    expect(editor?.textContent).toBe("n-new");
    expect(editor?.getAttribute("data-compact")).toBe("true");
    wrapper.unmount();
  });

  it("selects the first linked note when opened without createNew and switches on click", async () => {
    const { wrapper } = await mountDrawer();
    const drawer = useLinkedNotesDrawer();

    await drawer.open({ objectType: "ai", objectId: "c1", scopeId: "scope-a", nameSnapshot: "对话" });
    await flushPromises();

    expect(service.createNote).not.toHaveBeenCalled();
    // 对话可能被移动过工作区：按对话 id 列出，不限 scope。
    expect(service.listNotesForObject).toHaveBeenCalledWith("ai", "c1", undefined);
    expect(document.querySelector('[data-testid="note-editor"]')?.textContent).toBe("n-new");

    // 多篇笔记时用一行下拉切换；每个选项带更新时间，同名笔记也能区分。
    expect(document.querySelector(".linked-notes-drawer__picker")).not.toBeNull();
    const options = [...document.querySelectorAll<HTMLElement>(".linked-notes-drawer__picker .el-select-dropdown__item")];
    expect(options.length).toBe(drawer.notes.length);
    expect(options.every((option) => option.querySelector(".linked-notes-drawer__item-time")?.textContent?.trim())).toBe(true);
    const old = options.find((option) => option.textContent?.includes("旧笔记"));
    old!.click();
    await flushPromises();
    expect(document.querySelector('[data-testid="note-editor"]')?.textContent).toBe("n-old");
    wrapper.unmount();
  });

  it("hides the note picker when the object has only one note", async () => {
    service.listNotesForObject.mockResolvedValueOnce([summary("n-only", "唯一")]);
    const { wrapper } = await mountDrawer();
    const drawer = useLinkedNotesDrawer();

    await drawer.open(target);
    await flushPromises();

    expect(drawer.notes.length).toBe(1);
    expect(document.querySelector(".linked-notes-drawer__picker")).toBeNull();
    expect(document.querySelector('[data-testid="note-editor"]')?.textContent).toBe("n-only");
    wrapper.unmount();
  });

  it("moves AI links recorded under another workspace to the current one", async () => {
    const stale = { objectType: "ai", objectId: "c1", parentId: "", scopeId: "scope-old", nameSnapshot: "对话", scopeNameSnapshot: "旧", createdAt: 1 } as const;
    service.getNote.mockImplementation(async (id: string) => ({ ...note(id), links: id === "n-old" ? [stale] : [] }));
    const { wrapper } = await mountDrawer();
    const drawer = useLinkedNotesDrawer();
    const current = { objectType: "ai", objectId: "c1", scopeId: "scope-a", nameSnapshot: "对话", scopeNameSnapshot: "甲" } as const;

    await drawer.open(current);
    await flushPromises();

    expect(service.addNoteLink).toHaveBeenCalledTimes(1);
    expect(service.addNoteLink).toHaveBeenCalledWith("n-old", { ...current, parentId: "" });
    expect(service.removeNoteLink).toHaveBeenCalledWith("n-old", "ai", "c1", "scope-old");
    wrapper.unmount();
  });

  it("flushes pending edits before hiding on close", async () => {
    const { wrapper } = await mountDrawer();
    const drawer = useLinkedNotesDrawer();
    const notes = useNotesStore();
    await drawer.open(target);
    await flushPromises();

    let release!: () => void;
    const flushAll = vi.spyOn(notes, "flushAll").mockImplementation(() => new Promise<void>((resolve) => { release = resolve; }));
    const closing = drawer.close();
    await flushPromises();
    expect(flushAll).toHaveBeenCalledTimes(1);
    expect(drawer.visible).toBe(true);

    release();
    await closing;
    expect(drawer.visible).toBe(false);
    wrapper.unmount();
  });

  it("never grows wider than the viewport", async () => {
    const { wrapper } = await mountDrawer();
    await useLinkedNotesDrawer().open(target);
    await flushPromises();
    expect(wrapper.findComponent({ name: "ElDrawer" }).props("size")).toBe("min(520px, 100vw)");
    wrapper.unmount();
  });

  it("deletes the new note again when linking it fails, then shows the error", async () => {
    service.addNoteLink.mockRejectedValue(new Error("note_link_type"));
    service.deleteNote.mockResolvedValue(undefined);
    const error = vi.spyOn(ElMessage, "error").mockImplementation(() => ({ close: () => {} }) as never);
    try {
      const { wrapper } = await mountDrawer();
      const drawer = useLinkedNotesDrawer();
      await drawer.open(target, { createNew: true });
      await flushPromises();
      expect(service.deleteNote).toHaveBeenCalledWith("n-new");
      expect(error).toHaveBeenCalledWith("不支持关联这种对象");
      expect(useNotesStore().loadedNotes["n-new"]).toBeUndefined();
      wrapper.unmount();
    } finally {
      error.mockRestore();
    }
  });

  it("stays open and shows the error when a note in the drawer failed to save", async () => {
    const { wrapper } = await mountDrawer();
    const drawer = useLinkedNotesDrawer();
    const notes = useNotesStore();
    await drawer.open(target);
    await flushPromises();
    const error = vi.spyOn(ElMessage, "error").mockImplementation(() => ({ close: () => {} }) as never);
    try {
      const flushAll = vi.spyOn(notes, "flushAll").mockImplementation(async () => {
        notes.saveErrors = { ...notes.saveErrors, "n-new": "磁盘已满" };
      });
      await drawer.close();
      expect(drawer.visible).toBe(true);
      expect(error).toHaveBeenCalledWith("保存失败：磁盘已满");

      // Esc / 点遮罩走 before-close，同样不关闭。
      const before = wrapper.findComponent({ name: "ElDrawer" }).props("beforeClose") as (done: () => void) => void;
      const done = vi.fn();
      before(done);
      await flushPromises();
      expect(done).not.toHaveBeenCalled();
      expect(drawer.visible).toBe(true);

      flushAll.mockRestore();
      notes.saveErrors = {};
      await drawer.close();
      expect(drawer.visible).toBe(false);
    } finally {
      error.mockRestore();
    }
  });

  it("opens the current note in the notes page and closes", async () => {
    const { wrapper, router } = await mountDrawer();
    const drawer = useLinkedNotesDrawer();
    const notes = useNotesStore();
    const selectNote = vi.spyOn(notes, "selectNote");
    const flushAll = vi.spyOn(notes, "flushAll");
    await drawer.open(target, { createNew: true });
    await flushPromises();

    const button = document.querySelector<HTMLButtonElement>('button[aria-label="在笔记页中打开"]');
    button!.click();
    await vi.waitFor(() => expect(drawer.visible).toBe(false));

    expect(selectNote).toHaveBeenCalledWith("n-new");
    expect(router.currentRoute.value.name).toBe("notes");
    expect(flushAll).toHaveBeenCalled();
    wrapper.unmount();
  });

  describe("in a popped-out window", () => {
    it("tells the user to use the main window and does not create or open notes", async () => {
      const original = window.location.href;
      window.history.replaceState(null, "", "/?mode=popup-tab&label=popup-x");
      const info = vi.spyOn(ElMessage, "info").mockImplementation(() => ({ close: () => {} }) as never);
      try {
        const drawer = useLinkedNotesDrawer();
        await drawer.open(target, { createNew: true });
        await drawer.open(target);
        await flushPromises();

        expect(info).toHaveBeenCalledWith("请在主窗口中查看笔记");
        expect(service.createNote).not.toHaveBeenCalled();
        expect(service.listNotesForObject).not.toHaveBeenCalled();
        expect(drawer.visible).toBe(false);
        expect(drawer.rendered).toBe(false);
      } finally {
        info.mockRestore();
        window.history.replaceState(null, "", original);
      }
    });
  });

  it("does not allow the notes page to pop out", () => {
    expect(canPopOutTab({ view: "notes" })).toBe(false);
  });
});
