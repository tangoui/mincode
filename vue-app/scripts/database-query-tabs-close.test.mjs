import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import { queryTabSqlIsModified } from "../src/domain/database/query-tab-snapshot.ts";

const queryTabs = await readFile(
  new URL("../src/composables/database/useQueryTabs.ts", import.meta.url),
  "utf8"
);
const workspace = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8"
);

test("treats only non-empty SQL that differs from its initial content or template as unsaved", () => {
  const template = "SELECT * FROM users LIMIT 100;";
  assert.equal(queryTabSqlIsModified("", [template]), false);
  assert.equal(queryTabSqlIsModified("  \n ", [undefined, template]), false);
  assert.equal(queryTabSqlIsModified(`  ${template}\n`, [undefined, template]), false);
  assert.equal(queryTabSqlIsModified("SELECT 1", ["SELECT 1", template]), false);
  assert.equal(queryTabSqlIsModified("SELECT * FROM orders", [undefined, template]), true);
  assert.equal(queryTabSqlIsModified("SELECT * FROM orders", [undefined, ""]), true);
  assert.equal(queryTabSqlIsModified("SELECT 2", ["SELECT 1", template]), true);
});

test("query tabs remember the SQL they were created with", () => {
  assert.match(queryTabs, /initialQuerySql\.set\(tab\.id, tab\.sql\);/);
  assert.match(queryTabs, /initialQuerySql\.delete\(tabId\);/);
  assert.match(
    queryTabs,
    /queryTabSqlIsModified\(tab\.sql, \[initialQuerySql\.get\(tabId\), template\]\)/
  );
  assert.match(queryTabs, /return \{[\s\S]*queryTabsWithUnsavedSql,[\s\S]*\};/);
});

test("closing query tabs with edited SQL asks for confirmation and aborts on cancel", () => {
  const confirm = workspace.match(
    /async function confirmDiscardQueryTabs\([\s\S]*?\n\}\n/
  )?.[0];
  assert.ok(confirm, "confirmDiscardQueryTabs should exist");
  assert.match(confirm, /queryTabsWithUnsavedSql\(tabIds\)/);
  assert.match(confirm, /if \(!unsaved\.length\) return true;/);
  assert.match(confirm, /await ElMessageBox\.confirm\(/);
  assert.match(confirm, /catch \{\s*return false;\s*\}/);

  const remove = workspace.match(/async function removeDatabaseObjectTab\([\s\S]*?\n\}\n/)?.[0];
  assert.ok(remove, "removeDatabaseObjectTab should be async");
  assert.match(remove, /if \(!\(await confirmDiscardQueryTabs\(\[name\]\)\)\) return;\s*(?:if \([^\n]*confirmLeavePendingTable\(\)\)\) return;\s*)?closeDatabaseObjectTab\(name\);/);

  const closeMany = workspace.match(/async function closeObjectTabsByNames\([\s\S]*?\n\}\n/)?.[0];
  assert.ok(closeMany, "closeObjectTabsByNames should be async");
  assert.match(closeMany, /^async function closeObjectTabsByNames\(names: string\[\]\) \{\n  if \(!\(await confirmDiscardQueryTabs\(names\)\)\) return;/);
  assert.match(closeMany, /closeDatabaseObjectTab\(name\)/);
  assert.doesNotMatch(closeMany, /removeDatabaseObjectTab\(/);

  assert.match(workspace, /case "close":\s*void removeDatabaseObjectTab\(menu\.name\);/);
  assert.match(workspace, /case "close-all":\s*void closeObjectTabsByNames\(names\);/);
});
