import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { Note, NoteSummary, NoteTree } from "@/domain/notes/types";

const service = vi.hoisted(() => ({
  getNoteTree: vi.fn(),
  getNote: vi.fn(),
  updateNote: vi.fn(),
  deleteNote: vi.fn(),
  deleteNoteFolder: vi.fn(),
  addNoteLink: vi.fn(),
  removeNoteLink: vi.fn(),
}));

vi.mock("@/services/notes-service", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  ...service,
}));

import { NOTE_BODY_MAX_BYTES, useNotesStore } from "@/stores/notes";

function summary(id: string, folderId = "", title = id): NoteSummary {
  return { id, folderId, title, favorite: false, position: 1, updatedAt: 1, tags: [] };
}

function note(id: string, body = ""): Note {
  return { ...summary(id), body, createdAt: 1, links: [] };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function savedBodies() {
  return service.updateNote.mock.calls.map(([input]) => (input as { id: string; body?: string }));
}

beforeEach(() => {
  vi.useFakeTimers();
  setActivePinia(createPinia());
  localStorage.clear();
  for (const fn of Object.values(service)) fn.mockReset();
  service.getNote.mockImplementation(async (id: string) => note(id, `${id} body`));
  service.updateNote.mockImplementation(async (input: { id: string }) => ({ ...summary(input.id), updatedAt: 2 }));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("notes store", () => {
  it("debounces saves per note", async () => {
    const store = useNotesStore();
    await store.openNote("n1");
    await store.openNote("n2");
    store.editNote("n1", { body: "a" });
    store.editNote("n1", { body: "ab" });
    store.editNote("n1", { body: "abc" });
    await vi.advanceTimersByTimeAsync(400);
    store.editNote("n2", { body: "x" });
    await vi.advanceTimersByTimeAsync(399);
    expect(service.updateNote).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(service.updateNote).toHaveBeenCalledTimes(1);
    expect(savedBodies()[0]).toMatchObject({ id: "n1", body: "abc" });
    await vi.advanceTimersByTimeAsync(400);
    expect(service.updateNote).toHaveBeenCalledTimes(2);
    expect(savedBodies()[1]).toMatchObject({ id: "n2", body: "x" });
  });

  it("flushes pending edit to the original note when switching", async () => {
    const store = useNotesStore();
    await store.openNote("n1");
    await store.openNote("n2");
    store.editNote("n1", { body: "draft one" });
    await store.flushNote("n1");
    expect(service.updateNote).toHaveBeenCalledTimes(1);
    expect(savedBodies()[0]).toMatchObject({ id: "n1", body: "draft one" });
    store.editNote("n2", { body: "draft two" });
    expect(store.loadedNotes.n1!.body).toBe("draft one");
    await vi.advanceTimersByTimeAsync(800);
    expect(service.updateNote).toHaveBeenCalledTimes(2);
    expect(savedBodies()[1]).toMatchObject({ id: "n2", body: "draft two" });
  });

  it("shares one draft between two editors of the same note", async () => {
    const store = useNotesStore();
    const [page, drawer] = await Promise.all([store.openNote("n1"), store.openNote("n1")]);
    const again = await store.openNote("n1");
    expect(service.getNote).toHaveBeenCalledTimes(1);
    expect(drawer).toBe(page);
    expect(again).toBe(page);
    store.editNote("n1", { body: "shared" });
    expect(page.body).toBe("shared");
    expect(drawer.body).toBe("shared");
  });

  it("keeps local text when save fails", async () => {
    const store = useNotesStore();
    await store.openNote("n1");
    service.updateNote.mockRejectedValueOnce(new Error("note_request_too_large"));
    store.editNote("n1", { body: "keep me" });
    await store.flushNote("n1");
    expect(store.loadedNotes.n1!.body).toBe("keep me");
    expect(store.saveErrors.n1).toBeTruthy();
    expect(store.saving.n1).toBeFalsy();
    await vi.advanceTimersByTimeAsync(5000);
    expect(service.updateNote).toHaveBeenCalledTimes(1);
    await store.flushNote("n1");
    expect(service.updateNote).toHaveBeenCalledTimes(2);
    expect(savedBodies()[1]).toMatchObject({ id: "n1", body: "keep me" });
    expect(store.saveErrors.n1).toBeUndefined();
  });

  it("does not lose an edit typed while a save is in flight", async () => {
    const store = useNotesStore();
    await store.openNote("n1");
    const first = deferred<NoteSummary>();
    service.updateNote.mockReturnValueOnce(first.promise);
    store.editNote("n1", { body: "one" });
    await vi.advanceTimersByTimeAsync(800);
    expect(service.updateNote).toHaveBeenCalledTimes(1);
    expect(store.saving.n1).toBe(true);
    store.editNote("n1", { body: "one two" });
    first.resolve({ ...summary("n1"), updatedAt: 5 });
    await vi.advanceTimersByTimeAsync(0);
    expect(store.loadedNotes.n1!.body).toBe("one two");
    await vi.advanceTimersByTimeAsync(800);
    expect(service.updateNote).toHaveBeenCalledTimes(2);
    expect(savedBodies()[1]).toMatchObject({ id: "n1", body: "one two" });
    await store.flushNote("n1");
    expect(service.updateNote).toHaveBeenCalledTimes(2);
  });

  it("saves a newer edit after an in-flight save even without another debounce", async () => {
    const store = useNotesStore();
    await store.openNote("n1");
    const first = deferred<NoteSummary>();
    service.updateNote.mockReturnValueOnce(first.promise);
    store.editNote("n1", { body: "one" });
    const flushing = store.flushNote("n1");
    store.editNote("n1", { body: "one two" });
    const again = store.flushNote("n1");
    first.resolve(summary("n1"));
    await Promise.all([flushing, again]);
    expect(service.updateNote).toHaveBeenCalledTimes(2);
    expect(savedBodies()[1]).toMatchObject({ id: "n1", body: "one two" });
    await vi.advanceTimersByTimeAsync(800);
    expect(service.updateNote).toHaveBeenCalledTimes(2);
  });

  it("does not send the same revision twice from concurrent flushes", async () => {
    const store = useNotesStore();
    await store.openNote("n1");
    const first = deferred<NoteSummary>();
    service.updateNote.mockReturnValueOnce(first.promise);
    store.editNote("n1", { body: "once" });
    const flushes = [store.flushNote("n1"), store.flushNote("n1"), store.flushAll()];
    first.resolve(summary("n1"));
    await Promise.all(flushes);
    expect(service.updateNote).toHaveBeenCalledTimes(1);
  });

  it("updates the tree title immediately and flushAll saves every dirty note", async () => {
    const tree: NoteTree = { folders: [], notes: [summary("n1"), summary("n2")] };
    service.getNoteTree.mockResolvedValue(tree);
    const store = useNotesStore();
    await store.loadTree();
    await store.openNote("n1");
    await store.openNote("n2");
    store.editNote("n1", { title: "Renamed" });
    store.editNote("n2", { body: "b" });
    expect(store.tree!.notes[0]!.title).toBe("Renamed");
    await store.flushAll();
    expect(service.updateNote).toHaveBeenCalledTimes(2);
    expect(savedBodies().map((input) => input.id).sort()).toEqual(["n1", "n2"]);
    expect(store.tree!.notes[0]!.updatedAt).toBe(2);
  });

  it("persists the selected note and clears it when the note is deleted", async () => {
    localStorage.setItem("notes.lastNoteId", "n1");
    service.getNoteTree.mockResolvedValue({ folders: [], notes: [summary("n1")] });
    service.deleteNote.mockResolvedValue(undefined);
    const store = useNotesStore();
    expect(store.selectedNoteId).toBe("n1");
    store.selectNote("n2");
    expect(localStorage.getItem("notes.lastNoteId")).toBe("n2");
    store.selectNote("n1");
    await store.loadTree();
    await store.openNote("n1");
    store.editNote("n1", { body: "gone" });
    await store.deleteNote("n1");
    expect(store.selectedNoteId).toBe("");
    expect(store.loadedNotes.n1).toBeUndefined();
    expect(store.tree!.notes).toEqual([]);
    expect(store.linksVersion).toBe(1);
    await vi.advanceTimersByTimeAsync(800);
    expect(service.updateNote).not.toHaveBeenCalled();
  });

  it("keeps state when a delete fails", async () => {
    service.getNoteTree.mockResolvedValue({ folders: [], notes: [summary("n1")] });
    service.deleteNote.mockRejectedValue(new Error("note_not_found"));
    const store = useNotesStore();
    await store.loadTree();
    await store.openNote("n1");
    await expect(store.deleteNote("n1")).rejects.toThrow("note_not_found");
    expect(store.tree!.notes).toHaveLength(1);
    expect(store.loadedNotes.n1).toBeDefined();
    expect(store.linksVersion).toBe(0);
  });

  it("drops notes inside a deleted folder and its subfolders", async () => {
    service.getNoteTree.mockResolvedValue({
      folders: [
        { id: "f1", parentId: "", name: "A", position: 1, createdAt: 1, updatedAt: 1 },
        { id: "f2", parentId: "f1", name: "B", position: 1, createdAt: 1, updatedAt: 1 },
        { id: "f3", parentId: "", name: "C", position: 2, createdAt: 1, updatedAt: 1 },
      ],
      notes: [summary("n1", "f1"), summary("n2", "f2"), summary("n3", "f3")],
    });
    service.deleteNoteFolder.mockResolvedValue(undefined);
    const store = useNotesStore();
    await store.loadTree();
    await store.openNote("n2");
    store.selectNote("n2");
    await store.deleteFolder("f1");
    expect(store.tree!.folders.map((folder) => folder.id)).toEqual(["f3"]);
    expect(store.tree!.notes.map((item) => item.id)).toEqual(["n3"]);
    expect(store.loadedNotes.n2).toBeUndefined();
    expect(store.selectedNoteId).toBe("");
    expect(store.linksVersion).toBe(1);
  });

  it("bumps linksVersion and updates links after adding one", async () => {
    service.addNoteLink.mockResolvedValue({
      objectType: "ssh", objectId: "s1", parentId: "", scopeId: "", nameSnapshot: "prod", scopeNameSnapshot: "", createdAt: 3,
    });
    const store = useNotesStore();
    await store.openNote("n1");
    await store.addLink("n1", { objectType: "ssh", objectId: "s1", nameSnapshot: "prod" });
    expect(store.loadedNotes.n1!.links).toHaveLength(1);
    expect(store.linksVersion).toBe(1);
  });

  it("moves an AI link to the new scope instead of keeping a stale copy", async () => {
    const stale = { objectType: "ai", objectId: "c1", parentId: "", scopeId: "scope-old", nameSnapshot: "chat", scopeNameSnapshot: "Old", createdAt: 1 } as const;
    service.getNote.mockResolvedValue({ ...note("n1"), links: [stale, { ...stale, objectId: "c2" }] });
    service.addNoteLink.mockImplementation(async (_id: string, link: object) => ({ ...stale, ...link, createdAt: 2 }));
    service.removeNoteLink.mockResolvedValue(undefined);
    const store = useNotesStore();
    await store.openNote("n1");
    await store.addLink("n1", { objectType: "ai", objectId: "c1", scopeId: "scope-new", nameSnapshot: "chat", scopeNameSnapshot: "New" });

    expect(service.removeNoteLink).toHaveBeenCalledTimes(1);
    expect(service.removeNoteLink).toHaveBeenCalledWith("n1", "ai", "c1", "scope-old");
    expect(store.loadedNotes.n1!.links.map((link) => `${link.objectId}@${link.scopeId}`)).toEqual(["c2@scope-old", "c1@scope-new"]);
  });

  it("replaces control characters in titles so the save is accepted", async () => {
    const store = useNotesStore();
    await store.openNote("n1");
    store.editNote("n1", { title: "a\tb\u0007c\u202Ed  e" });
    expect(store.loadedNotes.n1!.title).toBe("a b c d  e");
    store.editNote("n1", { title: "长".repeat(600) });
    expect([...store.loadedNotes.n1!.title]).toHaveLength(500);
  });

  it("does not send a body over the size limit and keeps the text", async () => {
    const store = useNotesStore();
    await store.openNote("n1");
    // 每个「汉」占 3 个 UTF-8 字节：字符数未超但字节数超过 4 MiB。
    const big = "汉".repeat(Math.floor(NOTE_BODY_MAX_BYTES / 3) + 1);
    store.editNote("n1", { body: big });
    await store.flushNote("n1");
    expect(service.updateNote).not.toHaveBeenCalled();
    expect(store.saveErrors.n1).toBe("笔记正文超过 4 MB，无法保存，请拆分为多篇");
    expect(store.loadedNotes.n1!.body).toBe(big);

    store.editNote("n1", { body: "short" });
    await store.flushNote("n1");
    expect(service.updateNote).toHaveBeenCalledTimes(1);
    expect(store.saveErrors.n1).toBeUndefined();
  });
});
