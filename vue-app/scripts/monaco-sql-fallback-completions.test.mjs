import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { buildSqlCompletionItems } from "../src/domain/database/sql-completion.ts";

const editor = await readFile(new URL("../src/components/projects/MonacoTextEditor.vue", import.meta.url), "utf8");

test("SQL files outside the database editor still get keyword and snippet completions", () => {
  const register = editor.slice(editor.indexOf("function registerSqlCompletions"), editor.indexOf("function applyPreferences"));
  assert.doesNotMatch(register, /if \(!props\.sqlCompletions\) return;/, "the provider must not depend on database context");
  assert.match(register, /props\.sqlCompletions \?\? fallbackSqlCompletions/);
  assert.match(editor, /const fallbackSqlCompletions = buildSqlCompletionItems\(\{\}\)/);
  const labels = buildSqlCompletionItems({}).map(item => item.label);
  for (const keyword of ["SELECT", "FROM", "WHERE", "GROUP BY", "ORDER BY"]) assert.ok(labels.includes(keyword), keyword);
});
