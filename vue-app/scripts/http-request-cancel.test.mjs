import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const view = readFileSync(new URL("../src/views/HttpWorkspaceView.vue", import.meta.url), "utf8");

test("HTTP requests are cancellable and forward an AbortSignal to the backend call", () => {
  assert.match(view, /invokeBackend<ResponseData>\("http_send_request",[\s\S]*?\{ signal: controller\.signal \}\)/);
  assert.match(view, /<el-button\s+v-if="activeTab\.loading"[^>]*@click="cancelSend\(\)"/);
});

test("closing a tab or unmounting the workspace aborts its in-flight request", () => {
  assert.match(view, /function closeRequest[\s\S]*?cancelSend\(tabId\)/);
  assert.match(view, /onBeforeUnmount\([\s\S]*?for \(const controller of pendingRequests\.values\(\)\) controller\.abort\(\)/);
});

test("a superseded request does not clear the newer request's loading state", () => {
  assert.match(view, /if \(pendingRequests\.get\(tab\.id\) === controller\) \{\s*pendingRequests\.delete\(tab\.id\);\s*tab\.loading = false;/);
});

test("closing several tabs from the context menu aborts their requests and keeps one tab", () => {
  const closeMany = view.match(/function closeRequests\([\s\S]*?\n\}\n/)?.[0];
  assert.ok(closeMany, "closeRequests should exist");
  assert.match(closeMany, /syncBoundRequest\(tab\);\s*cancelSend\(tab\.id\);/);
  assert.match(closeMany, /if \(!remaining\.length\) \{\s*tabs\.value = \[createTab\(\)\];/);
  for (const command of ["close-others", "close-left", "close-right", "close-all"]) {
    assert.match(view, new RegExp(`case "${command}":\\s*return closeRequests\\(`));
  }
});
