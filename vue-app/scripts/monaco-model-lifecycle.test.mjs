import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const editorPath = new URL("../src/components/ui/MonacoTextEditor.vue", import.meta.url);
const poolPath = new URL("../src/services/monaco-model-pool.ts", import.meta.url);
const workspacePath = new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url);

test("model pool disposes only the last owned lease", async () => {
  const pool = await readFile(poolPath, "utf8");

  assert.match(pool, /leased\.references \+= 1/);
  assert.match(pool, /leased\.references -= 1/);
  assert.match(pool, /if \(leased\.references > 0\) return/);
  assert.match(pool, /if \(leased\.owned && !model\.isDisposed\(\)\) model\.dispose\(\)/);
});
