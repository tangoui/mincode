import assert from "node:assert/strict";
import test from "node:test";
import { precedingUserMessage, rewindConversation } from "../src/domain/ai/conversation-rewind.ts";

const messages = [
  { id: "u1", role: "user", content: "first" },
  { id: "a1", role: "assistant", content: "answer 1" },
  { id: "u2", role: "user", content: "second" },
  { id: "a2", role: "assistant", content: "answer 2" },
];
const agentHistory = [
  { role: "user", content: "first" },
  { role: "assistant", content: "answer 1" },
  { role: "user", content: "second" },
  { role: "assistant", content: "", toolCalls: [{ id: "c1", name: "read", arguments: "{}" }] },
  { role: "tool", toolCallId: "c1", content: "ok" },
  { role: "assistant", content: "answer 2" },
];
const agentEvents = [
  { id: "e1", kind: "tool", title: "t", detail: "", triggerMessageId: "a1" },
  { id: "e2", kind: "tool", title: "t", detail: "", triggerMessageId: "a2" },
  { id: "e3", kind: "mode", title: "m", detail: "", afterMessageIndex: 2 },
  { id: "e4", kind: "mode", title: "m", detail: "", afterMessageIndex: 0 },
];

test("rewinding the last turn keeps earlier messages, events and model history", () => {
  const rewound = rewindConversation({ messages, agentEvents, agentHistory }, "u2");
  assert.deepEqual(rewound.messages.map((message) => message.id), ["u1", "a1"]);
  assert.equal(rewound.removed.content, "second");
  assert.deepEqual(rewound.agentHistory, agentHistory.slice(0, 2));
  assert.deepEqual(rewound.agentEvents.map((event) => event.id), ["e1", "e4"]);
});

test("rewinding an earlier turn drops every later turn", () => {
  const rewound = rewindConversation({ messages, agentEvents, agentHistory }, "u1");
  assert.deepEqual(rewound.messages, []);
  assert.deepEqual(rewound.agentHistory, []);
  assert.deepEqual(rewound.agentEvents.map((event) => event.id), []);
});

test("a history that does not line up with the messages is cleared instead of mis-cut", () => {
  const compacted = [{ role: "system", content: "summary" }, ...agentHistory.slice(2)];
  const rewound = rewindConversation({ messages, agentEvents, agentHistory: compacted }, "u1");
  assert.deepEqual(rewound.agentHistory, []);
  const smallTalk = [...messages, { id: "u3", role: "user", content: "thanks" }, { id: "a3", role: "assistant", content: "you're welcome" }];
  const rewoundSmallTalk = rewindConversation({ messages: smallTalk, agentEvents: [], agentHistory }, "u2");
  assert.deepEqual(rewoundSmallTalk.agentHistory, [], "small talk kept out of history shifts the count");
});

test("only user messages can be rewound, and the preceding user turn is found", () => {
  assert.equal(rewindConversation({ messages, agentEvents, agentHistory }, "a2"), undefined);
  assert.equal(rewindConversation({ messages, agentEvents, agentHistory }, "missing"), undefined);
  assert.equal(precedingUserMessage(messages, "a2")?.id, "u2");
  assert.equal(precedingUserMessage(messages, "u1"), undefined);
});
