import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const view = await readFile(
  new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
  "utf8",
);

test("resource file menu compares an unopened text file with the active document", () => {
  assert.match(view, /function canCompareResourceNodeWithCurrent\(node: WorkspaceTreeNode\)/);
  assert.match(view, /async function compareResourceNodeWithCurrent\(node: WorkspaceTreeNode\)/);
  assert.match(
    view,
    /invokeBackend<string>\("local_fs_read_text_file",\s*\{[\s\S]*?projectRoot: node\.projectRoot,[\s\S]*?filePath: nodeRelativePath\(node\),[\s\S]*?\}\)/,
  );
  assert.match(view, /kind: "file",[\s\S]*?originalPath: nodeAbsolutePath\(node\),[\s\S]*?originalContent/);
});

test("resource comparison keeps the current document active and is only enabled for another text file", () => {
  assert.match(view, /const target = activeDocument\.value;[\s\S]*?!target\?\.loaded/);
  assert.match(view, /nodeAbsolutePath\(node\) !== target\.path/);
  assert.match(view, /!isImageDocument\(node\.path\)/);
  const handler = view.slice(
    view.indexOf("async function compareResourceNodeWithCurrent"),
    view.indexOf("async function compareWithOpenDocument"),
  );
  assert.doesNotMatch(handler, /project\.openDocument/);
  assert.match(
    view,
    /<template v-else>[\s\S]*?<el-dropdown-item[\s\S]*?:disabled="!canCompareResourceNodeWithCurrent\(contextMenu\.node\)"[\s\S]*?@click="compareResourceNodeWithCurrent\(contextMenu\.node\)"[\s\S]*?project\.editor\.compareWithCurrent/,
  );
});
