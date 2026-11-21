import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

// Execute the real external-open function, replacing only native I/O and pane
// mounting. This catches startup-restored tabs skipping the user's association.
const source = await readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8");
const start = source.indexOf("async function openExternalFile(");
const end = source.indexOf("\nconst projectViewActive", start);
const code = ts.transpileModule(source.slice(start, end), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;

function harness({ existing, kind = "binary", selected = { pluginId: "file-viewer", id: "office" } } = {}) {
  const path = existing?.path ?? "/fixture/book.xlsx";
  const calls = [];
  const project = {
    documents: existing ? [existing] : [],
    openDocument(value) { calls.push(["activate", value]); },
    setDocumentLoaded(value, content, metadata) { calls.push(["loaded", value, content, metadata]); },
    setDocumentError(value, key) { calls.push(["error", value, key]); },
  };
  const open = new Function("invokeBackend", "project", "externalCustomEditorFor", "openDocumentWithCustomEditor", "ElMessage", "t", code + "; return openExternalFile;")(
    async (command) => {
      calls.push(["native", command]);
      return command === "local_fs_inspect_external_file" ? { path, kind, size: 48000 } : "a,b\n1,2";
    }, project,
    async () => { calls.push(["resolve-default"]); return selected; },
    (value, editor) => calls.push(["plugin", value, editor]),
    { error() {} }, (key) => key,
  );
  return { open: (options) => open(path, options), calls };
}

for (const extension of ["xlsx", "docx", "pptx", "csv"]) {
  test(`restored ${extension} tab uses the default custom editor before builtin reads`, async () => {
    const path = `/fixture/test.${extension}`;
    const h = harness({ existing: { path, external: true, loaded: false }, kind: extension === "csv" ? "text" : "binary" });
    await h.open();
    assert.ok(h.calls.some(([type]) => type === "plugin"), "restored tab never reached the plugin");
    assert.equal(h.calls.some(([type, cmd]) => type === "native" && cmd === "local_fs_read_external_text_file"), false);
  });
}

test("explicit builtin reopen is respected even for an unloaded tab", async () => {
  const h = harness({ existing: { path: "/fixture/table.csv", loaded: false }, kind: "text" });
  await h.open({ builtin: true });
  assert.equal(h.calls.some(([type]) => type === "plugin"), false);
  assert.ok(h.calls.some(([type, cmd]) => type === "native" && cmd === "local_fs_read_external_text_file"));
});

test("a loaded builtin document and an already selected plugin keep their current editor", async () => {
  for (const existing of [
    { path: "/fixture/book.xlsx", loaded: true },
    { path: "/fixture/book.xlsx", loaded: true, editor: { pluginId: "chosen", editorId: "main" } },
  ]) {
    const h = harness({ existing }); await h.open();
    assert.equal(h.calls.some(([type]) => type === "resolve-default" || type === "plugin"), false);
  }
});

test("fresh files still resolve defaults and unavailable plugins keep builtin fallback", async () => {
  const fresh = harness(); await fresh.open();
  assert.ok(fresh.calls.some(([type]) => type === "plugin"));
  const missing = harness({ existing: { path: "/fixture/book.xlsx", loaded: false }, selected: null });
  await missing.open();
  assert.ok(missing.calls.some(([type]) => type === "loaded"));
  assert.equal(missing.calls.some(([type]) => type === "plugin"), false);
});
