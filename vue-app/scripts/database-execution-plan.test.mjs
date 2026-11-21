import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const transpileOptions = {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
};

function toDataUrl(code) {
  return `data:text/javascript;base64,${Buffer.from(code).toString("base64")}`;
}

const vectorCompiled = ts.transpileModule(
  await readFile(new URL("../src/domain/database/vector-database.ts", import.meta.url), "utf8"),
  transpileOptions,
).outputText;

let templateCompiled = ts.transpileModule(
  await readFile(new URL("../src/domain/database/query-template.ts", import.meta.url), "utf8"),
  transpileOptions,
).outputText;
templateCompiled = templateCompiled.replaceAll(
  "@/domain/database/vector-database",
  toDataUrl(vectorCompiled),
);
const queryTemplate = await import(toDataUrl(templateCompiled));

test("buildTableExplainSourceSql quotes mysql-like tables and applies default limit", () => {
  assert.equal(
    queryTemplate.buildTableExplainSourceSql({ dbType: "mysql", database: "app_db", table: "agent" }),
    "SELECT * FROM `app_db`.`agent` LIMIT 100",
  );
  assert.equal(
    queryTemplate.buildTableExplainSourceSql({ dbType: "mysql", database: "", table: "agent", limit: 50 }),
    "SELECT * FROM `agent` LIMIT 50",
  );
});

test("buildTableExplainSourceSql follows engine-specific select syntax", () => {
  assert.equal(
    queryTemplate.buildTableExplainSourceSql({ dbType: "sqlserver", database: "dbo", table: "orders" }),
    "SELECT TOP (100) * FROM [dbo].[orders]",
  );
  assert.equal(
    queryTemplate.buildTableExplainSourceSql({ dbType: "oracle", database: "hr", table: "emp" }),
    'SELECT * FROM "hr"."emp" FETCH FIRST 100 ROWS ONLY',
  );
  assert.equal(
    queryTemplate.buildTableExplainSourceSql({ dbType: "sqlite", database: "main", table: "users" }),
    'SELECT * FROM "users" LIMIT 100',
  );
});

test("non-sql engines and missing tables yield no source sql", () => {
  assert.equal(
    queryTemplate.buildTableExplainSourceSql({ dbType: "mongodb", database: "d", table: "t" }),
    "",
  );
  assert.equal(
    queryTemplate.buildTableExplainSourceSql({ dbType: "redis", database: "", table: "t" }),
    "",
  );
  assert.equal(
    queryTemplate.buildTableExplainSourceSql({ dbType: "pgvector", database: "d", table: "t" }),
    "",
  );
  assert.equal(
    queryTemplate.buildTableExplainSourceSql({ dbType: "mysql", database: "d", table: "" }),
    "",
  );
});

test("execution plan falls back to the current table instead of silently returning", async () => {
  const execution = await readFile(
    new URL("../src/composables/database/useQueryExecution.ts", import.meta.url),
    "utf8",
  );
  assert.match(execution, /buildTableExplainSourceSql\(\{/);
  assert.match(execution, /t\("database\.executionPlanNoSource"\)/);
  assert.doesNotMatch(execution, /if \(!queryText\.value\.trim\(\)\) return;/);
});

test("successful statements report affected rows and backend notices", async () => {
  const [execution, zh, en] = await Promise.all([
    readFile(new URL("../src/composables/database/useQueryExecution.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8"),
  ]);

  assert.match(execution, /result\.affectedRows != null/);
  assert.match(execution, /ElMessage\.success\(t\("database\.statementExecuted"/);
  assert.match(execution, /if \(result\.notice\) ElMessage\.warning\(result\.notice\)/);
  for (const locale of [zh, en]) {
    assert.match(locale, /"database\.statementExecuted":/);
  }
});
