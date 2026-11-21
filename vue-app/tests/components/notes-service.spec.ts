import { beforeEach, describe, expect, it, vi } from "vitest";
import type { MessageKey } from "@/i18n/messages";

const mocks = vi.hoisted(() => ({
  desktop: true,
  invoke: vi.fn(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => mocks.desktop,
  invokeBackend: mocks.invoke,
  nativeErrorMessage: (error: unknown) => (error instanceof Error && error.message ? error.message : String(error)),
}));

import {
  countNoteLinks,
  getNoteTree,
  listNotesForObject,
  moveNote,
  noteErrorMessage,
  putNoteAsset,
  updateNote,
} from "@/services/notes-service";

const t = (key: MessageKey, params?: Record<string, string | number>) =>
  params ? `${key}:${JSON.stringify(params)}` : key;

describe("notes-service", () => {
  beforeEach(() => {
    mocks.desktop = true;
    mocks.invoke.mockReset();
  });

  it("calls notes_update with exactly the given patch", async () => {
    mocks.invoke.mockResolvedValue({ id: "n1" });
    await updateNote({ id: "n1", body: "x" });
    expect(mocks.invoke).toHaveBeenCalledWith("notes_update", { id: "n1", body: "x" });
  });

  it("rejects with notes_desktop_only outside the desktop runtime", async () => {
    mocks.desktop = false;
    await expect(getNoteTree()).rejects.toThrow("notes_desktop_only");
    expect(mocks.invoke).not.toHaveBeenCalled();
  });

  it("passes folder and position to notes_move", async () => {
    mocks.invoke.mockResolvedValue({});
    await moveNote("n1", "", 2.5);
    expect(mocks.invoke).toHaveBeenCalledWith("notes_move", { id: "n1", folderId: "", position: 2.5 });
  });

  it("omits scopeId from notes_links_for_object when not given", async () => {
    mocks.invoke.mockResolvedValue([]);
    await listNotesForObject("ssh", "c1");
    expect(mocks.invoke).toHaveBeenCalledWith("notes_links_for_object", { objectType: "ssh", objectId: "c1" });
    await listNotesForObject("project", "p1", "");
    expect(mocks.invoke).toHaveBeenLastCalledWith("notes_links_for_object", { objectType: "project", objectId: "p1", scopeId: "" });
  });

  it("returns link counts as a sparse map", async () => {
    mocks.invoke.mockResolvedValue({ a: 2 });
    const counts = await countNoteLinks("database", ["a", "b"]);
    expect(mocks.invoke).toHaveBeenCalledWith("notes_links_counts", { objectType: "database", objectIds: ["a", "b"] });
    expect(counts.a).toBe(2);
    expect(counts.b ?? 0).toBe(0);
  });

  it("uploads an image as plain base64 and returns the ref", async () => {
    mocks.invoke.mockResolvedValue({ ref: "/__notes-assets/abc.png" });
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "shot.png", { type: "image/png" });
    await expect(putNoteAsset(file)).resolves.toBe("/__notes-assets/abc.png");
    expect(mocks.invoke).toHaveBeenCalledWith("notes_asset_put", { fileName: "shot.png", dataBase64: "iVBORw==" });
  });

  it("rejects images over 20 MB before calling the backend", async () => {
    const file = new File(["x"], "big.png", { type: "image/png" });
    Object.defineProperty(file, "size", { value: 20 * 1024 * 1024 + 1 });
    await expect(putNoteAsset(file)).rejects.toThrow("note_asset_too_large");
    expect(mocks.invoke).not.toHaveBeenCalled();
  });

  it("maps backend error codes to localized copy", () => {
    expect(noteErrorMessage(new Error("note_asset_too_large"), t)).toBe(t("notes.error.note_asset_too_large"));
    expect(noteErrorMessage(new Error("notes_desktop_only"), t)).toBe(t("notes.error.notes_desktop_only"));
    expect(noteErrorMessage("note_not_found", t)).toBe(t("notes.error.note_not_found"));
  });

  it("falls back to a generic message carrying the raw error text", () => {
    expect(noteErrorMessage(new Error("notes: update note: disk full"), t))
      .toBe(t("notes.error.unknown", { message: "notes: update note: disk full" }));
    expect(noteErrorMessage(new Error("prefix note_not_found"), t))
      .toBe(t("notes.error.unknown", { message: "prefix note_not_found" }));
  });
});
