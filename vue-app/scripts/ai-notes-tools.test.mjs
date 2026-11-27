import assert from "node:assert/strict";
import test from "node:test";
import * as notesTools from "../src/services/ai-tools/notes-tools.ts";

const note = { id: "note-1", title: "Runbook", body: "# Restart\n", folderId: "folder-1", tags: ["ops"] };
const tree = { folders: [{ id: "folder-1" }], notes: [{ id: note.id, title: note.title, folderId: "folder-1", position: 4.5, tags: ["ops"], updatedAt: 1 }, { id: "note-2", folderId: "", position: 7 }] };
const link = { objectType: "database", objectId: "db-1", scopeId: "work-1" };
const cases = [
  ["list_tree", "read", [], {}, "getNoteTree", [], tree],
  ["get", "read", ["id"], { id: note.id }, "getNote", [note.id], note],
  ["search", "read", ["query"], { query: "  restart  ", limit: 50 }, "searchNotes", ["restart", 50], [{ note: tree.notes[0], snippet: note.body }]],
  ["create", "write", ["title", "content"], { title: note.title, content: note.body, folderId: "folder-1", tags: ["ops"] }, "createNote", [{ title: note.title, body: note.body, folderId: "folder-1", tags: ["ops"] }], note],
  ["update", "write", ["id"], { id: note.id, content: "\nNext step", append: true }, "updateNote", [{ id: note.id, body: "\nNext step", append: true }], tree.notes[0]],
  ["move", "write", ["id", "folderId"], { id: note.id, folderId: "", position: 1.5 }, "moveNote", [note.id, "", 1.5]],
  ["delete", "dangerous", ["id"], { id: note.id }, "deleteNote", [note.id]],
  ["create_folder", "write", ["name"], { name: "Ops", parentId: "folder-1" }, "createNoteFolder", ["Ops", "folder-1"], { id: "folder-2" }],
  ["rename_folder", "write", ["id", "name"], { id: "folder-1", name: "SRE" }, "renameNoteFolder", ["folder-1", "SRE"]],
  ["delete_folder", "dangerous", ["id"], { id: "folder-1" }, "deleteNoteFolder", ["folder-1"]],
  ["list_tags", "read", [], {}, "listNoteTags", [], [{ name: "ops", count: 1 }]],
  ["set_tags", "write", ["id", "tags"], { id: note.id, tags: [] }, "setNoteTags", [note.id, []], { ...tree.notes[0], tags: [] }],
  ["link_object", "write", ["id", "objectType", "objectId"], { id: note.id, ...link }, "addNoteLink", [note.id, link], link],
  ["unlink_object", "write", ["id", "objectType", "objectId"], { id: note.id, ...link }, "removeNoteLink", [note.id, "database", "db-1", "work-1"]],
  ["list_for_object", "read", ["objectType", "objectId"], link, "listNotesForObject", ["database", "db-1", "work-1"], [tree.notes[0]]],
];

function fixture() {
  const calls = [];
  const service = Object.fromEntries(cases.map(([, , , , method, , result]) => [method, async (...args) => {
    calls.push([method, ...args]);
    return result;
  }]));
  const tools = notesTools.createNotesCapabilities(service);
  return {
    calls,
    capability: (name) => tools.find((item) => item.tool.name === `notes.${name}`),
  };
}

for (const [name, risk, required, input, method, args, result] of cases) {
  test(`notes.${name} advertises its contract and delegates to ${method}`, async () => {
    const { calls, capability } = fixture();
    const tool = capability(name);
    assert.equal(tool.risk, risk);
    assert.deepEqual(tool.tool.inputSchema.required ?? [], required);
    assert.deepEqual(await tool.execute({ input }), result ?? { ok: true });
    assert.deepEqual(calls, [[method, ...args]]);
  });
}

test("notes.move without position puts the note after its destination siblings", async () => {
  const { calls, capability } = fixture();
  await capability("move").execute({ input: { id: note.id, folderId: "" } });
  assert.deepEqual(calls, [["getNoteTree"], ["moveNote", note.id, "", 8]]);
});

test("invalid note updates, search limits and link types are rejected before service calls", async () => {
  const { calls, capability } = fixture();
  await assert.rejects(async () => capability("update").execute({ input: { id: note.id, append: true } }), /content/i);
  await assert.rejects(async () => capability("search").execute({ input: { query: "x", limit: 51 } }), /limit/i);
  await assert.rejects(async () => capability("list_for_object").execute({ input: { objectType: "terminal", objectId: "1" } }), /objectType/i);
  assert.deepEqual(calls, []);
});
