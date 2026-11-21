import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import type { NoteSummary } from "@/domain/notes/types";

const service = vi.hoisted(() => ({ getNoteTree: vi.fn(), deleteNote: vi.fn() }));
const bridge = vi.hoisted(() => ({ desktop: true, invokeBackend: vi.fn() }));

vi.mock("@/services/notes-service", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...service,
}));
vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  isDesktopRuntime: () => bridge.desktop,
  invokeBackend: bridge.invokeBackend,
}));
vi.mock("@/components/notes/NoteEditor.vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    default: defineComponent({
      name: "NoteEditor",
      props: { noteId: { type: String, required: true }, sidebarCollapsed: Boolean },
      setup: (props) => () => h("div", { "data-testid": "note-editor" }, props.noteId),
    }),
  };
});

import NotesWorkspaceView from "@/views/NotesWorkspaceView.vue";
import { NOTES_LAST_NOTE_STORAGE_KEY, useNotesStore } from "@/stores/notes";

function summary(id: string): NoteSummary {
  return { id, folderId: "", title: `${id} title`, favorite: false, position: 1, updatedAt: 1, tags: [] };
}

let wrapper: VueWrapper | undefined;

beforeEach(() => {
  bridge.desktop = true;
  for (const fn of [...Object.values(service), bridge.invokeBackend]) fn.mockReset();
  service.getNoteTree.mockResolvedValue({ folders: [], notes: [summary("n1")] });
  service.deleteNote.mockResolvedValue(undefined);
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

async function mountView() {
  wrapper = mount(NotesWorkspaceView, { attachTo: document.body });
  await flushPromises();
  return wrapper;
}

describe("NotesWorkspaceView", () => {
  it("shows a desktop-only state in the browser preview without calling commands", async () => {
    bridge.desktop = false;
    await mountView();

    expect(wrapper!.text()).toContain("笔记仅在桌面端可用");
    expect(service.getNoteTree).not.toHaveBeenCalled();
    expect(bridge.invokeBackend).not.toHaveBeenCalled();
  });

  it("opens the selected note and returns to the empty state when it is deleted", async () => {
    localStorage.setItem(NOTES_LAST_NOTE_STORAGE_KEY, "n1");
    await mountView();

    expect(wrapper!.find('[data-testid="note-editor"]').text()).toBe("n1");

    await useNotesStore().deleteNote("n1");
    await flushPromises();

    expect(wrapper!.find('[data-testid="note-editor"]').exists()).toBe(false);
    expect(wrapper!.text()).toContain("还没有笔记，新建一篇或从 Markdown 导入");
  });

  it("follows selection made outside the page", async () => {
    await mountView();
    expect(wrapper!.find('[data-testid="note-editor"]').exists()).toBe(false);
    expect(wrapper!.text()).toContain("在左侧选择一篇笔记，或新建一篇");

    useNotesStore().selectNote("n1");
    await flushPromises();

    expect(wrapper!.find('[data-testid="note-editor"]').text()).toBe("n1");
  });

  it("drops a remembered note that no longer exists", async () => {
    localStorage.setItem(NOTES_LAST_NOTE_STORAGE_KEY, "gone");
    await mountView();

    expect(useNotesStore().selectedNoteId).toBe("");
    expect(wrapper!.find('[data-testid="note-editor"]').exists()).toBe(false);
  });
});
