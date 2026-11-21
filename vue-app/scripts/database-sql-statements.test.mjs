import assert from "node:assert/strict";
import test from "node:test";
import { formatSqlForEditor } from "../src/domain/database/sql-format.ts";
import {
  resolveSqlForExecution,
  splitSqlStatements,
} from "../src/domain/database/sql-statements.ts";

test("splitSqlStatements ignores protected semicolons", () => {
  const statements = splitSqlStatements("select ';' as value; -- keep ;\nselect 2;");
  assert.deepEqual(statements.map((statement) => statement.text), [
    "select ';' as value",
    "-- keep ;\nselect 2",
  ]);
});

test("resolveSqlForExecution prefers selection and then cursor statement", () => {
  const sql = "select 1;\nselect 2;";
  assert.equal(resolveSqlForExecution(sql, {
    startLineNumber: 2,
    startColumn: 1,
    endLineNumber: 2,
    endColumn: 9,
  }), "select 2");
  assert.equal(resolveSqlForExecution(sql, {
    startLineNumber: 2,
    startColumn: 4,
    endLineNumber: 2,
    endColumn: 4,
  }), "select 2");
  assert.equal(resolveSqlForExecution(sql, undefined, true), sql);
});

test("formatSqlForEditor preserves quoted values and comments", () => {
  const formatted = formatSqlForEditor("select 'from where' as value from users where id = 1 -- select from");
  assert.match(formatted, /^SELECT 'from where' AS value\nFROM users\nWHERE id = 1 -- select from$/);
});
