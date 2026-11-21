import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const editorFiles = [
  "../src/components/projects/MonacoTextEditor.vue",
  "../src/components/projects/MonacoDiffEditor.vue",
  "../src/components/ui/ReadonlyCodeViewer.vue",
];

for (const editorFile of editorFiles) {
  test(`${editorFile} pins external Monaco overflow widgets to the viewport`, async () => {
    const source = await readFile(new URL(editorFile, import.meta.url), "utf8");

    assert.match(
      source,
      /overflowWidgetsDomNode:\s*getMonacoOverflowWidgetsHost\(\),/,
      "the editor must use the shared overflow host",
    );
    assert.match(
      source,
      /fixedOverflowWidgets:\s*true,/,
      "external overflow widgets need viewport coordinates to stay anchored to the hovered symbol",
    );
  });
}
