import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const editorPath = new URL("../src/components/projects/MonacoTextEditor.vue", import.meta.url);
const poolPath = new URL("../src/services/monaco-model-pool.ts", import.meta.url);
const workspacePath = new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url);

test("project editor releases models after their tabs close", async () => {
  const [editor, workspace] = await Promise.all([
    readFile(editorPath, "utf8"),
    readFile(workspacePath, "utf8"),
  ]);

  assert.match(editor, /watch\([\s\S]*props\.openPaths[\s\S]*releaseClosedModels\(openPaths \?\? \[path\]\)/);
  assert.match(editor, /for \(const model of ownedModels\.values\(\)\) releaseMonacoModel\(model\)/);
  // Every open text document keeps its model; plugin custom editor tabs (document.editor) have none.
  assert.match(workspace, /const openDocumentPaths = computed\(\(\) =>\s*project\.documents\.map\(\(document\) => document\.path\),?\s*\)/);
  assert.match(workspace, /:open-paths="openDocumentPaths"/);
});

test("model pool disposes only the last owned lease", async () => {
  const pool = await readFile(poolPath, "utf8");

  assert.match(pool, /leased\.references \+= 1/);
  assert.match(pool, /leased\.references -= 1/);
  assert.match(pool, /if \(leased\.references > 0\) return/);
  assert.match(pool, /if \(leased\.owned && !model\.isDisposed\(\)\) model\.dispose\(\)/);
});
