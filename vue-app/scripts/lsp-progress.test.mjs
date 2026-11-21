import assert from "node:assert/strict";
import test from "node:test";
import {
  applyWorkDoneProgress,
  parseWorkDoneProgress,
  pickLspProgress,
} from "../src/domain/projects/lsp-progress.ts";

test("work-done progress is parsed from begin/report/end and rejects malformed input", () => {
  assert.deepEqual(parseWorkDoneProgress({ token: "t1", value: { kind: "begin", title: "Intelephense indexing", percentage: 0 } }),
    { token: "t1", kind: "begin", title: "Intelephense indexing", percentage: 0 });
  assert.deepEqual(parseWorkDoneProgress({ token: 7, value: { kind: "report", percentage: 45.6, message: "5305 of 11633 files" } }),
    { token: "7", kind: "report", percentage: 46, message: "5305 of 11633 files" });
  assert.deepEqual(parseWorkDoneProgress({ token: "t1", value: { kind: "end" } }), { token: "t1", kind: "end" });
  for (const bad of [null, {}, { token: "t" }, { token: {}, value: { kind: "end" } }, { token: "t", value: { kind: "other" } }]) {
    assert.equal(parseWorkDoneProgress(bad), undefined, JSON.stringify(bad));
  }
  // Out-of-range percentages are dropped, control characters and length bounded.
  const odd = parseWorkDoneProgress({ token: "t", value: { kind: "report", percentage: 140, message: `a\u0007b${"x".repeat(500)}` } });
  assert.equal(odd?.percentage, undefined);
  assert.equal(odd?.message?.startsWith("a b"), true);
  assert.ok((odd?.message?.length ?? 0) <= 160);
});

test("progress tokens accumulate per token and end removes them", () => {
  let tokens = applyWorkDoneProgress(new Map(), { token: "a", kind: "begin", title: "Indexing", percentage: 0 });
  tokens = applyWorkDoneProgress(tokens, { token: "a", kind: "report", percentage: 40, message: "4 of 10 files" });
  assert.deepEqual(tokens.get("a"), { title: "Indexing", percentage: 40, message: "4 of 10 files" });
  // A report keeps fields it does not repeat.
  tokens = applyWorkDoneProgress(tokens, { token: "a", kind: "report", message: "5 of 10 files" });
  assert.deepEqual(tokens.get("a"), { title: "Indexing", percentage: 40, message: "5 of 10 files" });
  // The backend keeps only the latest message per token within a short
  // window, so a begin can be replaced by its first report: show it anyway.
  assert.deepEqual(applyWorkDoneProgress(tokens, { token: "b", kind: "report", percentage: 1 }).get("b"), { title: "", percentage: 1 });
  const before = tokens;
  tokens = applyWorkDoneProgress(tokens, { token: "a", kind: "end" });
  assert.equal(tokens.size, 0);
  assert.notEqual(tokens, before, "a new map, so reactive state sees the change");
});

test("the status bar shows reported progress first, then the file scan, then a starting server", () => {
  const indexing = new Map([["a", { title: "Intelephense indexing", percentage: 46, message: "5305 of 11633 files" }]]);
  assert.equal(pickLspProgress([]), undefined);
  assert.equal(pickLspProgress([{ name: "Go / gopls", phase: "ready", scanning: false, tokens: new Map() }]), undefined, "a ready, idle server shows nothing");
  assert.deepEqual(pickLspProgress([
    { name: "Go / gopls", phase: "starting", scanning: false, tokens: new Map() },
    { name: "PHP / Intelephense", phase: "ready", scanning: true, tokens: new Map() },
    { name: "PHP / Intelephense", phase: "ready", scanning: false, tokens: indexing },
  ]), { kind: "progress", name: "PHP / Intelephense", title: "Intelephense indexing", message: "5305 of 11633 files", percentage: 46, busy: 3 });
  assert.deepEqual(pickLspProgress([{ name: "PHP / Intelephense", phase: "ready", scanning: true, tokens: new Map() }]),
    { kind: "scanning", name: "PHP / Intelephense", busy: 1 });
  assert.deepEqual(pickLspProgress([{ name: "Java", phase: "initializing", scanning: false, tokens: new Map() }]),
    { kind: "starting", name: "Java", busy: 1 });
  // Of several reporting servers the least advanced one decides when all are ready.
  assert.equal(pickLspProgress([
    { name: "A", phase: "ready", scanning: false, tokens: new Map([["x", { title: "A", percentage: 90 }]]) },
    { name: "B", phase: "ready", scanning: false, tokens: new Map([["y", { title: "B", percentage: 10 }]]) },
  ])?.name, "B");
});

test("the progress sits in the project editor's run bar, after the run configuration", async () => {
  const { readFile } = await import("node:fs/promises");
  const view = await readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8");
  const bar = view.slice(view.indexOf('<footer v-if="runProjectRoot" class="project-editor-run-bar">'));
  const footer = bar.slice(0, bar.indexOf("</footer>"));
  assert.ok(footer.indexOf("<RunConfigSelector") >= 0 && footer.indexOf("<LspProgressIndicator") > footer.indexOf("<RunConfigSelector"));
});
