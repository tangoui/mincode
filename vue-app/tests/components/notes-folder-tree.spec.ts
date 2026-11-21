import { describe, expect, it } from "vitest";
import { buildNoteTreeNodes, canDropNoteNode, noteDropTarget } from "@/domain/notes/folder-tree";
import { highlightSegments } from "@/domain/notes/search-highlight";
import type { NoteFolder, NoteSummary, NoteTree } from "@/domain/notes/types";

function folder(id: string, parentId: string, position: number): NoteFolder {
  return { id, parentId, name: id, position, createdAt: 1, updatedAt: 1 };
}

function note(id: string, folderId: string, position: number): NoteSummary {
  return { id, folderId, title: id, favorite: false, position, updatedAt: 1, tags: [] };
}

const tree: NoteTree = {
  folders: [folder("a", "", 1), folder("b", "", 2), folder("a1", "a", 1)],
  notes: [note("x", "", 1), note("y", "", 3), note("z", "", 4), note("in-a", "a", 1), note("in-a1", "a1", 1), note("lost", "gone", 1)],
};

describe("buildNoteTreeNodes", () => {
  it("nests folders before notes and counts notes recursively", () => {
    const nodes = buildNoteTreeNodes(tree);
    expect(nodes.map((node) => node.key)).toEqual(["f:a", "f:b", "n:x", "n:lost", "n:y", "n:z"]);
    const a = nodes[0]!;
    expect(a.kind === "folder" && a.count).toBe(2);
    expect(a.kind === "folder" && a.children.map((node) => node.key)).toEqual(["f:a1", "n:in-a"]);
  });
});

describe("canDropNoteNode", () => {
  it("only allows dropping inside folders and between nodes of the same kind", () => {
    expect(canDropNoteNode("note", "folder", "inner")).toBe(true);
    expect(canDropNoteNode("note", "note", "inner")).toBe(false);
    expect(canDropNoteNode("note", "folder", "before")).toBe(false);
    expect(canDropNoteNode("folder", "folder", "after")).toBe(true);
    expect(canDropNoteNode("folder", "note", "after")).toBe(false);
  });
});

describe("noteDropTarget", () => {
  it("uses the midpoint between neighbours", () => {
    expect(noteDropTarget(tree, { kind: "note", id: "z" }, { kind: "note", id: "y" }, "before")).toEqual({ parentId: "", position: 2 });
    expect(noteDropTarget(tree, { kind: "note", id: "x" }, { kind: "note", id: "y" }, "after")).toEqual({ parentId: "", position: 3.5 });
  });

  it("uses first - 1 and last + 1 at the ends", () => {
    expect(noteDropTarget(tree, { kind: "note", id: "y" }, { kind: "note", id: "x" }, "before")).toEqual({ parentId: "", position: 0 });
    expect(noteDropTarget(tree, { kind: "note", id: "x" }, { kind: "note", id: "z" }, "after")).toEqual({ parentId: "", position: 5 });
  });

  it("appends inside a folder", () => {
    expect(noteDropTarget(tree, { kind: "note", id: "x" }, { kind: "folder", id: "a" }, "inner")).toEqual({ parentId: "a", position: 2 });
    expect(noteDropTarget(tree, { kind: "note", id: "x" }, { kind: "folder", id: "b" }, "inner")).toEqual({ parentId: "b", position: 1 });
    expect(noteDropTarget(tree, { kind: "folder", id: "b" }, { kind: "folder", id: "a" }, "inner")).toEqual({ parentId: "a", position: 2 });
  });

  it("moves folders among folders", () => {
    expect(noteDropTarget(tree, { kind: "folder", id: "a1" }, { kind: "folder", id: "a" }, "after")).toEqual({ parentId: "", position: 1.5 });
  });

  it("rejects disallowed drops", () => {
    expect(noteDropTarget(tree, { kind: "note", id: "x" }, { kind: "note", id: "y" }, "inner")).toBeNull();
  });
});

describe("highlightSegments", () => {
  it("splits text around case-insensitive matches", () => {
    expect(highlightSegments("Nginx and nginx", "NGINX")).toEqual([
      { text: "Nginx", match: true },
      { text: " and ", match: false },
      { text: "nginx", match: true },
    ]);
  });

  it("treats the query literally", () => {
    expect(highlightSegments("a.b axb", ".")).toEqual([
      { text: "a", match: false },
      { text: ".", match: true },
      { text: "b axb", match: false },
    ]);
  });

  it("returns the whole text for a blank query", () => {
    expect(highlightSegments("<b>x</b>", "  ")).toEqual([{ text: "<b>x</b>", match: false }]);
  });
});
