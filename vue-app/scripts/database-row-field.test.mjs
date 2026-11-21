import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  databaseCellEditFieldKind,
  databaseRowFieldKind,
  DatabaseRowFieldValueError,
  isDatabaseDescriptionField,
  parseDatabaseRowFieldValue,
} from "../src/domain/database/row-field.ts";

test("uses a textarea for descriptive VARCHAR columns", () => {
  assert.equal(databaseRowFieldKind("varchar(512)", "description"), "textarea");
  assert.equal(databaseRowFieldKind("varchar(512)", "productDescription"), "textarea");
  assert.equal(databaseRowFieldKind("varchar(255)", "title"), "text");
});

test("keeps structured types specialized when their name is descriptive", () => {
  assert.equal(databaseRowFieldKind("json", "description"), "json");
  assert.equal(databaseRowFieldKind("int", "description"), "number");
  assert.equal(isDatabaseDescriptionField("internal_note"), true);
});

test("inline cell edits keep VARCHAR text verbatim instead of guessing a type", () => {
  const kind = databaseCellEditFieldKind("varchar(32)", "code", "00123");
  assert.equal(kind, "text");
  assert.equal(parseDatabaseRowFieldValue(kind, "00123", true), "00123");
  assert.equal(parseDatabaseRowFieldValue(kind, "+8613800138000", true), "+8613800138000");
  assert.equal(parseDatabaseRowFieldValue(kind, "true", true), "true");
  assert.equal(parseDatabaseRowFieldValue(kind, "NULL", true), null);
  assert.equal(parseDatabaseRowFieldValue(kind, "NULL", false), "NULL");
});

test("inline cell edits parse typed columns and fall back to the raw cell type for unknown schemas", () => {
  assert.equal(databaseCellEditFieldKind("bigint", "id", 1), "number");
  assert.equal(databaseCellEditFieldKind("Int32", "count", 42), "number");
  assert.equal(databaseCellEditFieldKind("Boolean", "enabled", true), "boolean");
  assert.equal(databaseCellEditFieldKind("Document", "profile", { name: "a" }), "json");
  assert.equal(databaseCellEditFieldKind("String", "phone", "+86"), "text");
  assert.equal(databaseCellEditFieldKind("varchar(10)", "code", 123), "text");
  assert.equal(databaseCellEditFieldKind("datetime", "created_at", "2024-01-01T00:00:00Z"), "text");
});

test("number fields keep precision beyond IEEE-754 doubles and reject invalid input", () => {
  assert.equal(parseDatabaseRowFieldValue("number", "9007199254740993", false), "9007199254740993");
  assert.equal(parseDatabaseRowFieldValue("number", " 1.5 ", false), 1.5);
  assert.equal(parseDatabaseRowFieldValue("number", "00123", false), 123);
  assert.throws(
    () => parseDatabaseRowFieldValue("number", "12abc", false),
    (error) => error instanceof DatabaseRowFieldValueError && error.reason === "invalidNumber",
  );
});

test("boolean fields parse textual input instead of treating every non-empty string as true", () => {
  assert.equal(parseDatabaseRowFieldValue("boolean", "false", false), false);
  assert.equal(parseDatabaseRowFieldValue("boolean", "0", false), false);
  assert.equal(parseDatabaseRowFieldValue("boolean", "TRUE", false), true);
  assert.equal(parseDatabaseRowFieldValue("boolean", true, false), true);
});

test("inline cell commits use the column-aware parser", async () => {
  const source = await readFile(new URL("../src/composables/database/useRowMutations.ts", import.meta.url), "utf8");
  assert.doesNotMatch(source, /parseFieldValue/);
  assert.match(source, /parseDatabaseRowFieldValue\(\s*databaseCellEditFieldKind\(/);
  assert.match(source, /stageCell\(edit\.rowIndex, edit\.column, parsedValue\)/);
});
