import assert from "node:assert/strict";
import test from "node:test";
import { createConversationTree } from "../src/domain/ai/conversation-tree.ts";

test("nested conversations follow their parents and filtered orphans stay reachable", () => {
  const row = (id, parentId) => ({ id, delegation: parentId ? { parentId } : undefined });
  const items = [
    row("child-b", "parent"), row("grandchild", "child-a"), row("orphan", "missing"),
    row("parent"), row("child-a", "parent"),
  ];
  const tree = createConversationTree(items);
  assert.equal(tree.anchor("grandchild")?.id, "parent");
  assert.equal(tree.anchor("orphan")?.id, "orphan");
  assert.deepEqual(tree.rows().map(({ item, depth, children }) => [item.id, depth, children]), [
    ["orphan", 0, 0],
    ["parent", 0, 2],
    ["child-b", 1, 0],
    ["child-a", 1, 1],
    ["grandchild", 2, 0],
  ]);
  assert.deepEqual(tree.rows(new Set(["parent"])).map(({ item }) => item.id), ["orphan", "parent"]);
});

test("persisted cyclic links remain reachable without recursive cycles", () => {
  const tree = createConversationTree([
    { id: "one", delegation: { parentId: "two" } },
    { id: "two", delegation: { parentId: "one" } },
  ]);
  assert.deepEqual(tree.rows().map(({ item }) => item.id).sort(), ["one", "two"]);
});
