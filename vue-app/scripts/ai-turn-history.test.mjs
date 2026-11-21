import assert from "node:assert/strict";
import test from "node:test";
import { selectAiTurnHistory } from "../src/domain/ai/turn-history.ts";

const persisted = [
  { role: "user", content: "第 1 集剧本" },
  { role: "assistant", content: "……" },
];
const current = { role: "user", content: "第 2 集分镜" };
const messages = [...persisted, current];

function select(overrides = {}) {
  const calls = { continue: 0, messages: 0 };
  const result = selectAiTurnHistory({
    historyNone: false,
    directConversation: false,
    persistedHistory: persisted,
    currentMessage: current,
    continueHistory: (history, message) => {
      calls.continue += 1;
      return [...history, message];
    },
    messageHistory: () => {
      calls.messages += 1;
      return messages;
    },
    ...overrides,
  });
  return { ...result, calls };
}

test("history none sends only the current prompt and never overwrites saved history", () => {
  for (const persistedHistory of [persisted, []]) {
    const result = select({ historyNone: true, persistedHistory });
    assert.deepEqual(result.messages, [current]);
    assert.equal(result.persist, false);
    assert.deepEqual(result.calls, { continue: 0, messages: 0 }, "no earlier message may be read");
  }
});

test("history none wins over a direct conversational turn", () => {
  const result = select({ historyNone: true, directConversation: true, persistedHistory: [] });
  assert.deepEqual(result.messages, [current]);
  assert.equal(result.persist, false);
});

test("default turns keep replaying the persisted loop history", () => {
  const result = select();
  assert.deepEqual(result.messages, [...persisted, current]);
  assert.equal(result.persist, true);
  assert.equal(result.calls.continue, 1);
});

test("default turns without loop history fall back to the visible messages", () => {
  const result = select({ persistedHistory: [] });
  assert.equal(result.messages, messages);
  assert.equal(result.persist, true);
  assert.equal(result.calls.messages, 1);
});

test("a direct conversational turn skips the history but keeps an earlier task saved", () => {
  const withTask = select({ directConversation: true });
  assert.deepEqual(withTask.messages, [current]);
  assert.equal(withTask.persist, false);
  const fresh = select({ directConversation: true, persistedHistory: [] });
  assert.deepEqual(fresh.messages, [current]);
  assert.equal(fresh.persist, true);
});
