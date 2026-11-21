import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const source = readFileSync(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8");
const action = source.slice(source.indexOf("async function deleteSelected()"), source.indexOf("async function revealSelected()"));
const executable = ts.transpile(action, { target: ts.ScriptTarget.ES2022 });

async function deleteNode({ cancel = false, fail = false, isDir = false } = {}) {
  const selected = { id: "/second:src/item", projectRoot: "/second", path: "src/item", name: "item", isDir };
  const calls = [];
  const context = {
    selectedNode: { value: selected }, resourceGitSelection: { value: selected },
    errorKey: { value: null }, t: (key) => key,
    ElMessageBox: { confirm: async () => { if (cancel) throw Error("cancel"); } },
    project: { projectRoot: "/first", removeDocuments: (...args) => calls.push(["documents", ...args]) },
    invokeBackend: async (command, args) => {
      calls.push([command, args.projectRoot, args.path]);
      if (fail) throw Error("delete failed");
    },
    toDocumentPath: (root, path) => `${root}/${path}`,
    fileTreeRef: { value: { getNode: (id) => ({ id }), remove: (node) => calls.push(["remove", node.id]) } },
    scheduleProjectIndexRefresh: (root) => calls.push(["index", root]),
    refreshProject: () => { throw Error("must not rebuild the tree"); },
  };
  await runInNewContext(`${executable}; deleteSelected()`, context);
  return { calls, context };
}

for (const isDir of [false, true]) {
  test(`deleting a ${isDir ? "directory" : "file"} updates only its node using its own root`, async () => {
    const { calls, context } = await deleteNode({ isDir });
    assert.deepEqual(calls, [
      ["local_fs_delete", "/second", "src/item"],
      ["documents", "/second/src/item", isDir],
      ["remove", "/second:src/item"], ["index", "/second"],
    ]);
    assert.equal(context.selectedNode.value, null);
    assert.equal(context.resourceGitSelection.value, null);
    assert.equal(context.errorKey.value, null);
  });
}
test("cancel leaves filesystem, tree and editors untouched", async () => {
  assert.deepEqual((await deleteNode({ cancel: true })).calls, []);
});
test("failed deletion leaves the tree and editors untouched", async () => {
  const { calls, context } = await deleteNode({ fail: true });
  assert.deepEqual(calls, [["local_fs_delete", "/second", "src/item"]]);
  assert.ok(context.selectedNode.value);
  assert.equal(context.errorKey.value, "project.operationFailed");
});
