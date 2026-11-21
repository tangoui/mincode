import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const editorSource = await readFile(
  new URL(
    "../src/components/database/DatabaseGridCellEditor.vue",
    import.meta.url,
  ),
  "utf8",
);

test("database inline editor commits when focus moves outside the editor and picker panel", () => {
  assert.match(
    editorSource,
    /document\.addEventListener\("pointerdown", handleDocumentPointerDown, true\)/,
  );
  assert.match(
    editorSource,
    /document\.removeEventListener\("pointerdown", handleDocumentPointerDown, true\)/,
  );
  assert.match(editorSource, /editor\.value\?\.\$el\?\.contains\(target\)/);
  assert.match(editorSource, /target\.closest\(`\.\$\{pickerPopperClass\}`\)/);
  assert.match(
    editorSource,
    /function handleDocumentPointerDown[\s\S]*?emit\("commit"\)/,
    "clicking outside must submit the edited value through the existing confirmation flow",
  );
  assert.equal(
    editorSource.match(/:popper-class="pickerPopperClass"/g)?.length,
    3,
    "date, time, and datetime panels must stay inside the editor interaction boundary",
  );
  assert.match(
    editorSource,
    /@keydown\.esc\.prevent="emit\('cancel'\)"/,
    "Escape must remain an explicit cancel action",
  );
  assert.doesNotMatch(
    editorSource,
    /@blur="emit\('commit'\)"/,
    "the document interaction boundary must own outside commits to avoid picker blur races",
  );
});
