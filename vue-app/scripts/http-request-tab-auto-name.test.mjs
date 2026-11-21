import assert from "node:assert/strict";
import { readSourceSync as readFileSync } from "./read-source.mjs";
import test from "node:test";

const view = readFileSync(new URL("../src/views/HttpWorkspaceView.vue", import.meta.url), "utf8");

test("sending never renames a tab bound to a saved collection request", () => {
  // 绑定标签的名字会被 deep watcher 经 syncBoundRequest 写回集合里的请求名。
  const guard = view.match(/function shouldAutoNameRequestTab\([\s\S]*?\n\}\n/)?.[0];
  assert.ok(guard, "shouldAutoNameRequestTab should exist");
  assert.match(guard, /if \(tab\.savedRequestId\) return false;/);
  assert.match(
    guard,
    /return tab\.name === t\("http\.newRequest"\) \|\| tab\.name === autoTabNames\.get\(tab\.id\);/
  );

  const send = view.match(/async function send\(\) \{[\s\S]*?\n\}\n/)?.[0];
  assert.ok(send, "send should exist");
  assert.match(
    send,
    /if \(shouldAutoNameRequestTab\(tab\)\) \{\s*tab\.name = requestUrlTabName\(tab\.url\);\s*autoTabNames\.set\(tab\.id, tab\.name\);\s*\}/
  );
  assert.doesNotMatch(send, /\n    tab\.name = /);
});

test("auto names are tracked per tab and released when the tab closes", () => {
  assert.match(view, /const autoTabNames = new Map<string, string>\(\);/);
  assert.match(view, /function openHistory[\s\S]*?autoTabNames\.set\(tab\.id, tab\.name\);/);
  assert.match(view, /function closeRequest[\s\S]*?autoTabNames\.delete\(tabId\);/);
});
