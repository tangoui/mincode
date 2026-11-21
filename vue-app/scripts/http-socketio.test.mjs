import assert from "node:assert/strict";
import test from "node:test";
import {
  checkSocketIOArgs,
  formatSocketIOArgs,
  isReservedSocketIOEvent,
  isValidSocketIOAuth,
  normalizeSocketIOSavedEvents,
  SOCKETIO_SAVED_EVENT_LIMIT,
  upsertSocketIOSavedEvent,
} from "../src/domain/http/socketio.ts";

test("validates Socket.IO event arguments and auth payloads", () => {
  assert.deepEqual(checkSocketIOArgs(""), { ok: true });
  assert.deepEqual(checkSocketIOArgs('["hi", {"id": 1}]'), { ok: true });
  assert.deepEqual(checkSocketIOArgs('{"id": 1}'), { ok: false, reason: "notArray" });
  assert.deepEqual(checkSocketIOArgs("[1,"), { ok: false, reason: "invalidJson" });
  assert.equal(isValidSocketIOAuth(""), true);
  assert.equal(isValidSocketIOAuth('{"token":"abc"}'), true);
  assert.equal(isValidSocketIOAuth("[]"), false);
  assert.equal(isValidSocketIOAuth("null"), false);
  assert.equal(isReservedSocketIOEvent(" connect "), true);
  assert.equal(isReservedSocketIOEvent("chat"), false);
});

test("normalizes saved Socket.IO events from untrusted storage", () => {
  const items = normalizeSocketIOSavedEvents([
    null,
    { id: "a", event: "chat", args: "[1]", ack: true, updatedAt: 1 },
    { id: "b", event: " chat ", args: " [1] ", ack: false, updatedAt: 5 },
    { id: "c", event: "connect", args: "", updatedAt: 9 },
    { id: "", event: "x", args: "", updatedAt: 9 },
    { id: "d", event: "join", args: 3, updatedAt: "x" },
  ]);
  assert.deepEqual(items, [
    { id: "b", event: "chat", args: " [1] ", ack: false, updatedAt: 5 },
    { id: "d", event: "join", args: "", ack: false, updatedAt: 0 },
  ]);
  const many = Array.from({ length: SOCKETIO_SAVED_EVENT_LIMIT + 5 }, (_, index) => ({
    id: `id-${index}`,
    event: `event-${index}`,
    args: "",
    updatedAt: index,
  }));
  const limited = normalizeSocketIOSavedEvents(many);
  assert.equal(limited.length, SOCKETIO_SAVED_EVENT_LIMIT);
  assert.equal(limited[0].id, `id-${SOCKETIO_SAVED_EVENT_LIMIT + 4}`);
});

test("upserts saved Socket.IO events by name and arguments", () => {
  let next = 0;
  const createId = () => `saved-${++next}`;
  let items = upsertSocketIOSavedEvent([], { event: " chat ", args: "[1]", ack: false }, createId, 10);
  items = upsertSocketIOSavedEvent(items, { event: "join", args: "", ack: false }, createId, 20);
  items = upsertSocketIOSavedEvent(items, { event: "chat", args: "[1] ", ack: true }, createId, 30);
  assert.deepEqual(
    items.map((item) => [item.id, item.event, item.ack, item.updatedAt]),
    [
      ["saved-1", "chat", true, 30],
      ["saved-2", "join", false, 20],
    ]
  );
});

test("formats received Socket.IO arguments for display", () => {
  assert.equal(formatSocketIOArgs('[{"a":1}]'), '{\n  "a": 1\n}');
  assert.equal(formatSocketIOArgs("[1,2]"), "[\n  1,\n  2\n]");
  assert.equal(formatSocketIOArgs("[]"), "[]");
  assert.equal(formatSocketIOArgs("not json"), "not json");
});
