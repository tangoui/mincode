import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const diffEditorSource = await readFile(
  new URL("../src/components/ui/MonacoDiffEditor.vue", import.meta.url),
  "utf8",
);

test("Git diff exposes previous and next change navigation", () => {
  assert.match(diffEditorSource, /goToDiff\('previous'\)/);
  assert.match(diffEditorSource, /goToDiff\('next'\)/);
  // Keys come from the active keymap (global mincode.diff.* rules, see
  // services/monaco-keymap.ts); the component only registers its navigator.
  assert.match(diffEditorSource, /registerMonacoDiffNavigation\(diffEditor, navigateFromKeymap\)/);
  assert.match(diffEditorSource, /if \(props\.navigation\) goToDiff\(direction\)/);
  assert.doesNotMatch(diffEditorSource, /\.addCommand\(/);
  assert.doesNotMatch(diffEditorSource, /\.addAction\(/);
  assert.doesNotMatch(diffEditorSource, /KeyMod\.Alt/);
  assert.match(diffEditorSource, /project\.git\.previousChange/);
  assert.match(diffEditorSource, /project\.git\.nextChange/);
});
