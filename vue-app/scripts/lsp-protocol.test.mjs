import assert from "node:assert/strict";
import test from "node:test";
import {
  documentContentChanges,
  initializeCapabilities,
  isPathInLspRoot,
  parseDocumentSync,
  parseDiagnosticProvider,
  parseDocumentDiagnosticReport,
  parseWorkspaceDiagnosticReport,
  MAX_WORKSPACE_DIAGNOSTIC_REPORTS,
} from "../src/domain/projects/lsp-protocol.ts";

test("initialize requires real capabilities and UTF-16 positions", () => {
  for (const result of [null, {}, { capabilities: null }, { capabilities: [] }]) {
    assert.throws(() => initializeCapabilities(result), /capabilities missing/);
  }
  assert.deepEqual(initializeCapabilities({ capabilities: {} }), {});
  assert.throws(() => initializeCapabilities({ capabilities: { positionEncoding: "utf-8" } }), /encoding/);
  assert.equal(initializeCapabilities({ capabilities: { positionEncoding: "utf-16" } }).positionEncoding, "utf-16");
});

test("numeric and options sync forms respect none, openClose, save and includeText", () => {
  assert.deepEqual(parseDocumentSync(undefined), { openClose: false, change: 0, save: false, saveIncludeText: false });
  assert.deepEqual(parseDocumentSync(1), { openClose: true, change: 1, save: false, saveIncludeText: false });
  assert.deepEqual(parseDocumentSync(2), { openClose: true, change: 2, save: false, saveIncludeText: false });
  assert.deepEqual(parseDocumentSync({ openClose: false, change: 2, save: true }),
    { openClose: false, change: 2, save: true, saveIncludeText: false });
  assert.deepEqual(parseDocumentSync({ openClose: true, change: 0, save: { includeText: true } }),
    { openClose: true, change: 0, save: true, saveIncludeText: true });
  assert.equal(parseDocumentSync({ change: 99 }).change, 0);
});

test("incremental replacement range uses the OLD UTF-16 text, including CRLF, CR and trailing lines", () => {
  for (const [old, end] of [
    ["", { line: 0, character: 0 }],
    ["😀x", { line: 0, character: 3 }],
    ["a\r\nb😀", { line: 1, character: 3 }],
    ["a\rb\n", { line: 2, character: 0 }],
  ]) {
    assert.deepEqual(documentContentChanges(2, old, "new text"), [{
      range: { start: { line: 0, character: 0 }, end }, text: "new text",
    }]);
  }
  assert.deepEqual(documentContentChanges(1, "old", "new"), [{ text: "new" }]);
  assert.deepEqual(documentContentChanges(0, "old", "new"), []);
  assert.deepEqual(documentContentChanges(2, "same", "same"), []);
});

test("diagnostic provider and full/unchanged reports are validated", () => {
  assert.deepEqual(parseDiagnosticProvider({ documentSelector: null, interFileDependencies: false, workspaceDiagnostics: false }),
    { workspaceDiagnostics: false });
  assert.deepEqual(parseDiagnosticProvider({ identifier: "json" }), { identifier: "json", workspaceDiagnostics: false });
  assert.deepEqual(parseDiagnosticProvider({ workspaceDiagnostics: true }), { workspaceDiagnostics: true });
  assert.deepEqual(parseDiagnosticProvider({ workspaceDiagnostics: "yes" }), { workspaceDiagnostics: false });
  for (const value of [undefined, null, false, true, []]) assert.equal(parseDiagnosticProvider(value), undefined);
  assert.deepEqual(parseDocumentDiagnosticReport({ kind: "full", items: [], resultId: "one" }),
    { kind: "full", items: [], resultId: "one" });
  assert.deepEqual(parseDocumentDiagnosticReport({ kind: "unchanged", resultId: "two" }),
    { kind: "unchanged", resultId: "two" });
  assert.equal(parseDocumentDiagnosticReport({ kind: "unchanged" }), undefined);
  assert.equal(parseDocumentDiagnosticReport({ kind: "full", items: null }), undefined);
  assert.deepEqual(parseDocumentDiagnosticReport({ kind: "full", items: [null, { message: "missing range" }] }),
    { kind: "full", items: [] });
});

test("workspace reports keep valid file items, versions and both report kinds within the bound", () => {
  const range = { start: { line: 1, character: 2 }, end: { line: 1, character: 5 } };
  assert.equal(parseWorkspaceDiagnosticReport(null), undefined);
  assert.equal(parseWorkspaceDiagnosticReport({ items: null }), undefined);
  assert.deepEqual(parseWorkspaceDiagnosticReport({ items: [
    { kind: "full", uri: "file:///repo/a.go", version: null, items: [{ range, message: "bad" }, { message: "no range" }], resultId: "r1" },
    { kind: "unchanged", uri: "file:///repo/b.go", version: 3, resultId: "r2" },
    { kind: "full", uri: "https://example.com/x", version: null, items: [] },
    { kind: "full", version: null, items: [] },
    { kind: "unchanged", uri: "file:///repo/c.go", version: null },
  ] }), [
    { uri: "file:///repo/a.go", version: null, report: { kind: "full", items: [{ range, message: "bad" }], resultId: "r1" } },
    { uri: "file:///repo/b.go", version: 3, report: { kind: "unchanged", resultId: "r2" } },
  ]);
  const many = Array.from({ length: MAX_WORKSPACE_DIAGNOSTIC_REPORTS + 5 }, (_, index) => (
    { kind: "full", uri: `file:///repo/${index}.go`, version: null, items: [] }
  ));
  assert.equal(parseWorkspaceDiagnosticReport({ items: many }).length, MAX_WORKSPACE_DIAGNOSTIC_REPORTS);
});

test("root matching respects path boundaries, Windows separators, drive case and filesystem roots", () => {
  assert.equal(isPathInLspRoot("/repo-other/a.ts", "/repo"), false);
  assert.equal(isPathInLspRoot("/repo/a.ts", "/repo/"), true);
  assert.equal(isPathInLspRoot("/repo/../secret", "/repo"), false);
  assert.equal(isPathInLspRoot("c:\\Repo\\a.ts", "C:/repo"), true);
  assert.equal(isPathInLspRoot("C:/repo-other/a.ts", "C:\\repo"), false);
  assert.equal(isPathInLspRoot("/a.ts", "/"), true);
  assert.equal(isPathInLspRoot("C:/a.ts", "C:\\"), true);
  assert.equal(isPathInLspRoot("/a.ts", ""), false);
});
