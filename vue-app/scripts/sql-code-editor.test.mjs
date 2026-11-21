import assert from "node:assert/strict";
import test from "node:test";
import { codeMirrorSnippet, offsetOf, positionOf, selectionOf, sqlEditorDialect, viewStateOf } from "../src/domain/database/sql-code-editor.ts";

/** A TextLines over a plain string, as CodeMirror's Text would give. */
function textLines(text) {
  const starts = [0];
  for (let i = 0; i < text.length; i++) if (text[i] === "\n") starts.push(i + 1);
  return {
    lines: starts.length,
    lineStart: (line) => starts[line - 1],
    lineLength: (line) => (line < starts.length ? starts[line] - 1 : text.length) - starts[line - 1],
    lineAt: (offset) => {
      let line = 1;
      while (line < starts.length && starts[line] <= offset) line++;
      return line;
    },
  };
}

test("maps 1-based line/column to offsets and back", () => {
  const doc = textLines("SELECT 1;\nSELECT *\nFROM t;");
  assert.equal(offsetOf(doc, 1, 1), 0);
  assert.equal(offsetOf(doc, 2, 8), 17);
  assert.equal(offsetOf(doc, 3, 8), 26);
  assert.deepEqual(positionOf(doc, 17), { line: 2, column: 8 });
  assert.deepEqual(positionOf(doc, 0), { line: 1, column: 1 });
});

test("clamps positions outside the document (a view state saved for longer text)", () => {
  const doc = textLines("ab\ncd");
  assert.equal(offsetOf(doc, 9, 9), 5);
  assert.equal(offsetOf(doc, 1, 99), 2);
  assert.equal(offsetOf(doc, 0, 0), 0);
});

test("round-trips a selection through the persisted view state", () => {
  const doc = textLines("SELECT a\nFROM t\nWHERE x = 1;");
  const state = viewStateOf(doc, 9, 15, 120, 4);
  assert.deepEqual(state, {
    lineNumber: 2, column: 7,
    selectionStartLineNumber: 2, selectionStartColumn: 1,
    positionLineNumber: 2, positionColumn: 7,
    scrollTop: 120, scrollLeft: 4,
  });
  assert.deepEqual(selectionOf(doc, state), { anchor: 9, head: 15 });
});

test("a reversed selection keeps its anchor and head", () => {
  const doc = textLines("abc\ndef");
  const state = viewStateOf(doc, 6, 1, 0, 0);
  assert.deepEqual(selectionOf(doc, state), { anchor: 6, head: 1 });
});

test("chooses the dialect of the connection type", () => {
  assert.equal(sqlEditorDialect("mysql"), "MySQL");
  assert.equal(sqlEditorDialect("tidb"), "MySQL");
  assert.equal(sqlEditorDialect("mariadb"), "MariaSQL");
  assert.equal(sqlEditorDialect("postgres"), "PostgreSQL");
  assert.equal(sqlEditorDialect("kingbase"), "PostgreSQL");
  assert.equal(sqlEditorDialect("sqlite"), "SQLite");
  assert.equal(sqlEditorDialect("sqlserver"), "MSSQL");
  assert.equal(sqlEditorDialect("oracle"), "PLSQL");
  assert.equal(sqlEditorDialect("clickhouse"), "StandardSQL");
  assert.equal(sqlEditorDialect(undefined), "StandardSQL");
});

test("converts Monaco snippets to CodeMirror snippet fields", () => {
  assert.equal(codeMirrorSnippet("SELECT ${1:*}\nFROM ${2:table}\nWHERE ${3:condition};"), "SELECT ${*}\nFROM ${table}\nWHERE ${condition};");
  assert.equal(codeMirrorSnippet("DELETE FROM ${1:table}$0"), "DELETE FROM ${table}${}");
  assert.equal(codeMirrorSnippet("plain text"), "plain text");
});
