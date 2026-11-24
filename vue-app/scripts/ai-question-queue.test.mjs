import assert from "node:assert/strict";
import test from "node:test";
import { createQuestionQueue } from "../src/domain/ai/question-queue.ts";

const item = { source: "acp", conversationId: "conversation", questions: [] };

test("questions remain visible in insertion order and only settle once by ID", async () => {
  const queue = createQuestionQueue();
  const first = queue.enqueue(item);
  const second = queue.enqueue(item);
  assert.deepEqual(queue.list(item.conversationId).map(({ id }) => id), [first.id, second.id]);
  const answer = { kind: "answered", answers: [{ questionId: "q", values: ["yes"] }] };
  queue.resolve(second.id, answer);
  queue.resolve(second.id, { kind: "withdrawn" });
  assert.deepEqual(await second.outcome, answer);
  assert.deepEqual(queue.list().map(({ id }) => id), [first.id]);
  queue.dispose();
});

test("aborts remove pending questions and pre-aborted signals never display a row", async () => {
  const queue = createQuestionQueue();
  const controller = new AbortController();
  let notifications = 0;
  const unsubscribe = queue.subscribe(() => { notifications += 1; });
  const pending = queue.enqueue(item, controller.signal);
  controller.abort();
  assert.deepEqual(await pending.outcome, { kind: "cancelled" });
  assert.deepEqual(queue.list(), []);
  assert.deepEqual(await queue.enqueue(item, controller.signal).outcome, { kind: "cancelled" });
  assert.equal(notifications, 2);
  unsubscribe();
});

test("conversation cancellation is scoped and disposal settles all remaining questions", async () => {
  const queue = createQuestionQueue();
  const first = queue.enqueue(item);
  const second = queue.enqueue(item);
  const other = queue.enqueue({ ...item, conversationId: "other" });
  queue.cancelConversation(item.conversationId);
  for (const ticket of [first, second]) {
    assert.deepEqual(await ticket.outcome, { kind: "cancelled" });
  }
  assert.deepEqual(queue.list().map(({ id }) => id), [other.id]);
  queue.dispose();
  assert.deepEqual(await other.outcome, { kind: "cancelled" });
  assert.deepEqual(await queue.enqueue(item).outcome, { kind: "cancelled" });
});
