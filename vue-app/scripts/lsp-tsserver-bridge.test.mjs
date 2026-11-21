import assert from "node:assert/strict";
import test from "node:test";
import {
  TSSERVER_BRIDGES,
  parseTsserverRequest,
  tsserverExecuteCommand,
  tsserverResponseBody,
  tsserverResponseParams,
} from "../src/domain/projects/lsp-tsserver-bridge.ts";

test("tsserver requests accept the positional wire framing and reject malformed tuples", () => {
  const args = { file: "/repo/App.vue", needFileNameList: false };
  assert.deepEqual(parseTsserverRequest([[7, "_vue:projectInfo", args]]), { id: 7, command: "_vue:projectInfo", args });
  assert.deepEqual(parseTsserverRequest([8, "quickinfo", null]), { id: 8, command: "quickinfo", args: null });
  for (const invalid of [null, {}, [], [[]], [["7", "x"]], [[1.5, "x"]], [[1, ""]], [[1, 2]], [[1, "x".repeat(201)]]]) {
    assert.equal(parseTsserverRequest(invalid), undefined, JSON.stringify(invalid));
  }
});

test("requests become typescript.tsserverRequest commands and responses carry only the body", () => {
  const request = { id: 3, command: "_vue:getComponentProps", args: { file: "/a.vue", tag: "Foo" } };
  assert.deepEqual(tsserverExecuteCommand(request), {
    command: "typescript.tsserverRequest",
    arguments: ["_vue:getComponentProps", { file: "/a.vue", tag: "Foo" }, { isAsync: true, lowPriority: true }],
  });
  assert.deepEqual(tsserverResponseBody({ seq: 1, success: true, body: ["msg"] }), ["msg"]);
  assert.equal(tsserverResponseBody({ success: false, body: undefined }), null);
  assert.deepEqual(tsserverResponseBody({ configFileName: "/tsconfig.json" }), { configFileName: "/tsconfig.json" });
  assert.equal(tsserverResponseBody(undefined), null);
  assert.deepEqual(tsserverResponseParams(3, undefined), [[3, null]]);
  assert.deepEqual(tsserverResponseParams(4, { a: 1 }), [[4, { a: 1 }]]);
  assert.equal(TSSERVER_BRIDGES["vue-ls"], "vue-ts");
});
