import assert from "node:assert/strict";
import test from "node:test";
import {
  NOTIFICATION_ACTIONS_MAX,
  NOTIFICATION_ACTION_TITLE_MAX_RUNES,
  parsePluginNotificationActions,
} from "../src/domain/notifications/plugin-actions.ts";
import { createOrderedDelivery } from "../src/domain/notifications/ordered-delivery.ts";

test("notification actions: parse", () => {
  assert.equal(NOTIFICATION_ACTIONS_MAX, 2);
  assert.equal(NOTIFICATION_ACTION_TITLE_MAX_RUNES, 24);

  const two = [
    { id: "retry", title: "重试", command: "sync.retry", pluginId: "other" },
    { id: "open-log", title: "View log", command: "sync.log" },
  ];
  assert.deepEqual(parsePluginNotificationActions(two, "p"), [
    { id: "retry", title: "重试", pluginId: "p", command: "sync.retry" },
    { id: "open-log", title: "View log", pluginId: "p", command: "sync.log" },
  ], "pluginId always comes from the event, never from the action");

  // Absent or empty: no actions at all (not an error).
  assert.equal(parsePluginNotificationActions(undefined, "p"), undefined);
  assert.equal(parsePluginNotificationActions(null, "p"), undefined);
  assert.equal(parsePluginNotificationActions([], "p"), undefined);

  // 24 runes is the limit (counted in code points, not UTF-16 units).
  const max = "界".repeat(23) + "😀";
  assert.equal(parsePluginNotificationActions([{ id: "a", title: max, command: "c" }], "p")?.[0].title, max);

  // Command IDs follow the manifest contribution ID pattern, which allows ":".
  assert.deepEqual(parsePluginNotificationActions([{ id: "retry", title: "Retry", command: "sync:retry" }], "p"), [
    { id: "retry", title: "Retry", pluginId: "p", command: "sync:retry" },
  ]);
  assert.equal(parsePluginNotificationActions([{ id: "a", title: "OK", command: ":retry" }], "p"), undefined, "may not start with a colon");
  assert.equal(parsePluginNotificationActions([{ id: "a:b", title: "OK", command: "c" }], "p"), undefined, "action IDs still allow no colon");

  const valid = { id: "a", title: "OK", command: "sync.retry" };
  for (const [label, raw] of [
    ["three actions", [valid, { ...valid, id: "b" }, { ...valid, id: "c" }]],
    ["title over 24 runes", [{ ...valid, title: "x".repeat(25) }]],
    ["title with a newline", [{ ...valid, title: "OK\nnow" }]],
    ["title with a tab", [{ ...valid, title: "OK\tnow" }]],
    ["title with a right-to-left override (Cf)", [{ ...valid, title: "OK‮now" }]],
    ["title with a bidi isolate (Cf)", [{ ...valid, title: "⁦OK⁩" }]],
    ["title with a zero-width space (Cf)", [{ ...valid, title: "O​K" }]],
    ["title with a zero-width joiner (Cf)", [{ ...valid, title: "O‍K" }]],
    ["title with a BOM (Cf)", [{ ...valid, title: "﻿OK" }]],
    ["title with a line separator", [{ ...valid, title: "OK now" }]],
    ["title with a paragraph separator", [{ ...valid, title: "OK now" }]],
    ["title with a C1 control", [{ ...valid, title: "OK\u0085" }]],
    ["blank title", [{ ...valid, title: "  " }]],
    ["duplicate ids", [valid, { ...valid, title: "Again" }]],
    ["bad id", [{ ...valid, id: "a b" }]],
    ["long id", [{ ...valid, id: "a".repeat(33) }]],
    ["bad command", [{ ...valid, command: "-x" }]],
    ["missing command", [{ id: "a", title: "OK" }]],
    ["one bad entry drops the group", [valid, { id: "b", title: 3, command: "c" }]],
    ["not an array", { 0: valid }],
    ["a string", "retry"],
  ]) {
    assert.equal(parsePluginNotificationActions(raw, "p"), undefined, label);
  }
  assert.equal(parsePluginNotificationActions([valid], ""), undefined, "no plugin id, no actions");
});

test("notification actions: lazy parsing keeps arrival order", async () => {
  const delivered = [];
  const deliver = createOrderedDelivery();

  deliver(undefined, () => delivered.push("plain-1"));
  assert.deepEqual(delivered, ["plain-1"], "without actions and nothing pending: synchronous");

  let resolveSlow;
  deliver(new Promise((resolve) => { resolveSlow = resolve; }), (actions) => delivered.push(`with-actions:${actions}`));
  deliver(undefined, () => delivered.push("plain-2"));
  deliver(Promise.resolve("fast"), (actions) => delivered.push(`fast:${actions}`));
  deliver(Promise.reject(new Error("chunk failed")), (actions) => delivered.push(`failed:${actions}`));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(delivered, ["plain-1"], "later items wait for the pending parse");

  resolveSlow("parsed");
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(delivered, ["plain-1", "with-actions:parsed", "plain-2", "fast:fast", "failed:undefined"]);

  deliver(undefined, () => delivered.push("plain-3"));
  assert.equal(delivered.at(-1), "plain-3", "synchronous again once the queue drained");
});
