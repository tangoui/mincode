import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  mergePendingEdit,
  pendingEditsForProjectRoot,
} from "../src/domain/ai/pending-edits.ts";

const edits = [
  {
    id: "workspace-a-shared",
    path: "shared.txt",
    projectRoot: "/projects/a",
    before: "a-before",
    after: "a-after",
    status: "pending",
    createdAt: 1,
  },
  {
    id: "workspace-b-shared",
    path: "shared.txt",
    projectRoot: "/projects/b",
    before: "b-before",
    after: "b-after",
    status: "pending",
    createdAt: 2,
  },
  {
    id: "workspace-a-applied",
    path: "done.txt",
    projectRoot: "/projects/a",
    before: "before",
    after: "after",
    status: "applied",
    createdAt: 3,
  },
];

test("pending AI edits are filtered by project root", () => {
  assert.deepEqual(
    pendingEditsForProjectRoot(edits, "/projects/a").map((edit) => edit.id),
    ["workspace-a-shared"],
  );
  assert.deepEqual(
    pendingEditsForProjectRoot(edits, "/projects/b/").map((edit) => edit.id),
    ["workspace-b-shared"],
  );
  assert.deepEqual(pendingEditsForProjectRoot(edits, ""), []);
});

test("same relative file remains independent across workspaces", () => {
  const merged = mergePendingEdit(
    edits.slice(0, 1),
    {
      path: "shared.txt",
      projectRoot: "/projects/b",
      before: "b-before",
      after: "b-new",
    },
    () => "workspace-b-new",
    () => 4,
  );

  assert.equal(merged.length, 2);
  assert.equal(merged[0]?.after, "a-after");
  assert.equal(merged[1]?.id, "workspace-b-new");

  const updated = mergePendingEdit(
    merged,
    {
      path: "shared.txt",
      projectRoot: "/projects/b/",
      before: "ignored",
      after: "b-latest",
    },
    () => "must-not-create",
    () => 5,
  );
  assert.equal(updated.length, 2);
  assert.equal(updated[1]?.id, "workspace-b-new");
  assert.equal(updated[1]?.after, "b-latest");
});

test("pending edits UI binds bulk actions to the active conversation workspace", async () => {
  const [drawer, panel, store] = await Promise.all([
    readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/ai/AiPendingEdits.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/stores/ai-pending-edits.ts", import.meta.url), "utf8"),
  ]);

  assert.match(drawer, /assistant\.activeConversation\?\.projectRoot/);
  assert.match(drawer, /<AiPendingEdits :project-root="pendingEditsProjectRoot" \/>/);
  assert.match(panel, /pendingEdits\.apply\(props\.projectRoot, ids\)/);
  assert.match(panel, /pendingEdits\.revert\(props\.projectRoot, ids\)/);
  assert.match(store, /pendingEditsForProjectRoot\(actionable\.value, projectRoot\)/);
});
