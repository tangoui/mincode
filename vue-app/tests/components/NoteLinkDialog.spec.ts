import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { computed } from "vue";
import type { LinkObjectSnapshot } from "@/domain/notes/link-targets";
import type { Note, NoteLinkInput, NoteLinkType } from "@/domain/notes/types";

const service = vi.hoisted(() => ({
  getNote: vi.fn(),
  addNoteLink: vi.fn(),
  removeNoteLink: vi.fn(),
}));

vi.mock("@/services/notes-service", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...service,
}));

// The dialog lists objects from the shared snapshot; the real composable reads app stores.
const snapshot = vi.hoisted(() => ({ value: null as LinkObjectSnapshot | null }));
vi.mock("@/composables/use-note-link-snapshot", () => ({
  useLinkObjectSnapshot: () => computed(() => snapshot.value!),
  openLinkTarget: vi.fn(),
}));

import NoteLinkDialog from "@/components/notes/NoteLinkDialog.vue";
import { useNotesStore } from "@/stores/notes";

function note(): Note {
  return { id: "n1", folderId: "", title: "n1", favorite: false, position: 1, updatedAt: 1, tags: [], body: "", createdAt: 1, links: [] };
}

function baseSnapshot(): LinkObjectSnapshot {
  return {
    ssh: new Map([["ssh-1", "prod web"], ["ssh-2", "staging"]]),
    database: new Map([["db-1", "orders"]]),
    http: new Map([["req-1", "List users"]]),
    httpCollections: new Map([["req-1", "col-1"]]),
    currentProjects: new Map([["/repo/a", "a"]]),
    otherWorkspaceProjects: new Map(),
    currentAi: new Map([["conv-1", "Plan"]]),
    currentScopeId: "scope-a",
    liveScopeIds: new Set(["scope-a"]),
    workspaceNames: new Map([["scope-a", "Alpha"]]),
    unavailable: new Set<NoteLinkType>(),
  };
}

async function mountDialog() {
  const store = useNotesStore();
  await store.openNote("n1");
  const wrapper = mount(NoteLinkDialog, { props: { noteId: "n1", modelValue: true }, attachTo: document.body });
  await flushPromises();
  return { wrapper, store };
}

function tabByLabel(label: string) {
  return [...document.querySelectorAll<HTMLElement>(".el-tabs__item")].find((item) => item.textContent?.trim() === label);
}

function itemByName(name: string) {
  return [...document.querySelectorAll<HTMLElement>(".note-link-dialog__item")].find((item) => item.textContent?.includes(name));
}

beforeEach(() => {
  for (const fn of Object.values(service)) fn.mockReset();
  document.body.innerHTML = "";
  snapshot.value = baseSnapshot();
  service.getNote.mockResolvedValue(note());
  service.addNoteLink.mockImplementation(async (_id: string, input: NoteLinkInput) => ({
    parentId: "",
    scopeId: "",
    nameSnapshot: "",
    scopeNameSnapshot: "",
    ...input,
    createdAt: 5,
  }));
  service.removeNoteLink.mockResolvedValue(undefined);
});

describe("NoteLinkDialog", () => {
  it("links an SSH connection on click and unlinks it on a second click", async () => {
    const { wrapper, store } = await mountDialog();
    const addLink = vi.spyOn(store, "addLink");
    const removeLink = vi.spyOn(store, "removeLink");

    tabByLabel("SSH")!.click();
    await flushPromises();
    itemByName("prod web")!.click();
    await flushPromises();

    expect(addLink).toHaveBeenCalledWith("n1", expect.objectContaining({ objectType: "ssh", objectId: "ssh-1", nameSnapshot: "prod web" }));
    expect(itemByName("prod web")!.classList.contains("is-linked")).toBe(true);

    itemByName("prod web")!.click();
    await flushPromises();
    expect(removeLink).toHaveBeenCalledWith("n1", "ssh", "ssh-1", "");
    expect(itemByName("prod web")!.classList.contains("is-linked")).toBe(false);
    wrapper.unmount();
  });

  it("records the scope for AI conversations and the collection for HTTP requests", async () => {
    const { wrapper, store } = await mountDialog();
    const addLink = vi.spyOn(store, "addLink");

    tabByLabel("AI")!.click();
    await flushPromises();
    itemByName("Plan")!.click();
    await flushPromises();
    expect(addLink).toHaveBeenLastCalledWith("n1", expect.objectContaining({
      objectType: "ai",
      objectId: "conv-1",
      scopeId: "scope-a",
      scopeNameSnapshot: "Alpha",
      nameSnapshot: "Plan",
    }));

    tabByLabel("HTTP")!.click();
    await flushPromises();
    itemByName("List users")!.click();
    await flushPromises();
    expect(addLink).toHaveBeenLastCalledWith("n1", expect.objectContaining({ objectType: "http", objectId: "req-1", parentId: "col-1" }));
    wrapper.unmount();
  });

  it("filters each tab with its own search and shows empty states", async () => {
    snapshot.value = { ...baseSnapshot(), database: new Map() };
    const { wrapper } = await mountDialog();
    tabByLabel("SSH")!.click();
    await flushPromises();
    const search = document.querySelector<HTMLInputElement>('.el-tab-pane:not([style*="display: none"]) input')!;
    search.value = "stag";
    search.dispatchEvent(new Event("input"));
    await flushPromises();
    expect(itemByName("staging")).toBeTruthy();
    expect(itemByName("prod web")).toBeUndefined();

    tabByLabel("数据库")!.click();
    await flushPromises();
    expect(document.body.textContent).toContain("还没有数据库连接");
    wrapper.unmount();
  });
});
