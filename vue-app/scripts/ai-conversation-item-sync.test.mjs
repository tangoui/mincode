import assert from "node:assert/strict";
import test from "node:test";

import {
  AI_ITEM_PATCH_CONFLICT,
  conversationMetadata,
  createConversationItemSaver,
  diffConversationItems,
  serializeConversationItems,
} from "../src/domain/ai/conversation-item-sync.ts";

function conversation(id = "c1") {
  return {
    id,
    title: "t",
    messages: [{ id: "m1", content: "hello" }, { id: "m2", content: "wor" }],
    agentHistory: [{ role: "assistant", content: "h" }],
    agentEvents: [{ id: "e1", status: "running" }],
    messageQueue: [{ id: "q1" }],
  };
}

function fakeStore({ fail } = {}) {
  const calls = [];
  let revision = 0;
  const request = async (args) => {
    calls.push(args);
    const error = fail?.(args, calls.length);
    if (error) throw new Error(error);
    revision += 1;
    return { itemRevision: revision };
  };
  return { calls, request };
}

test("items serialize exactly as a full save sends them", () => {
  const value = conversation();
  const serialized = serializeConversationItems({ ...value, messageQueue: "not an array" });
  assert.deepEqual(serialized.messages, value.messages.map((item) => JSON.stringify(item)));
  assert.deepEqual(serialized.messageQueue, []);
  assert.deepEqual(Object.keys(conversationMetadata(value)), ["id", "title"]);
});

test("the diff lists only changed and appended items with both lengths", () => {
  const before = conversation();
  const after = structuredClone(before);
  after.messages[1].content = "world";
  after.agentEvents[0].status = "completed";
  after.agentEvents.push({ id: "e2" });
  after.messageQueue = [];
  const patch = diffConversationItems({ revision: 7, items: serializeConversationItems(before) }, serializeConversationItems(after));
  assert.equal(patch.baseRevision, 7);
  assert.deepEqual(patch.groups.messages, { baseLength: 2, length: 2, changes: [{ position: 1, item: JSON.stringify(after.messages[1]) }] });
  assert.deepEqual(patch.groups.agentHistory, { baseLength: 1, length: 1, changes: [] });
  assert.deepEqual(patch.groups.agentEvents.changes.map((change) => change.position), [0, 1]);
  assert.deepEqual(patch.groups.messageQueue, { baseLength: 1, length: 0, changes: [] });
});

test("the first save is full, later saves send only changed items", async () => {
  const store = fakeStore();
  const saver = createConversationItemSaver(store.request);
  const value = conversation();
  await saver.save("s1", 3, value);
  assert.equal(store.calls[0].itemPatch, undefined);
  assert.equal(store.calls[0].position, 3);
  assert.equal(store.calls[0].conversation.messages.length, 2);

  value.messages[1].content = "world";
  await saver.save("s1", 3, value);
  const patch = store.calls[1].itemPatch;
  assert.equal(patch.baseRevision, 1);
  assert.equal(store.calls[1].conversation.messages, undefined);
  assert.deepEqual(patch.groups.messages.changes, [{ position: 1, item: JSON.stringify(value.messages[1]) }]);

  await saver.save("s1", 3, value);
  assert.equal(store.calls[2].itemPatch.baseRevision, 2);
  assert.equal(store.calls.at(-1).itemPatch.groups.messages.changes.length, 0);
});

test("a patch the store rejects as outdated is resent in full", async () => {
  const store = fakeStore({ fail: (args, call) => (call === 2 ? `invalid: ${AI_ITEM_PATCH_CONFLICT}` : undefined) });
  const saver = createConversationItemSaver(store.request);
  const value = conversation();
  await saver.save("s1", 0, value);
  value.messages.push({ id: "m3" });
  await saver.save("s1", 0, value);
  assert.ok(store.calls[1].itemPatch);
  assert.equal(store.calls[2].itemPatch, undefined);
  assert.equal(store.calls[2].conversation.messages.length, 3);
  await saver.save("s1", 0, value);
  assert.equal(store.calls[3].itemPatch.baseRevision, 2);
});

test("any other failure is reported and the next save is full", async () => {
  const store = fakeStore({ fail: (_args, call) => (call === 2 ? "disk full" : undefined) });
  const saver = createConversationItemSaver(store.request);
  const value = conversation();
  await saver.save("s1", 0, value);
  await assert.rejects(saver.save("s1", 0, value), /disk full/);
  assert.deepEqual(saver.remembered(), []);
  await saver.save("s1", 0, value);
  assert.equal(store.calls[2].itemPatch, undefined);
});

test("only the most recently saved conversations are remembered, and forget drops one", async () => {
  const store = fakeStore();
  const saver = createConversationItemSaver(store.request, 2);
  await saver.save("s1", 0, conversation("a"));
  await saver.save("s1", 1, conversation("b"));
  await saver.save("s2", 0, conversation("a"));
  assert.deepEqual(saver.remembered(), [["s1", "b"], ["s2", "a"]]);
  saver.forget("a");
  assert.deepEqual(saver.remembered(), [["s1", "b"]]);
  await saver.save("s2", 0, conversation("a"));
  assert.equal(store.calls.at(-1).itemPatch, undefined);
});

test("a save without an item revision is not remembered", async () => {
  const calls = [];
  const saver = createConversationItemSaver(async (args) => {
    calls.push(args);
    return null;
  });
  await saver.save("s1", 0, conversation());
  await saver.save("s1", 0, conversation());
  assert.equal(calls[1].itemPatch, undefined);
});
