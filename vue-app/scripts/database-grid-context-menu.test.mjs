import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  buildColumnAlterSql,
  supportsCopyAsAlterSql,
} from "../src/domain/database/alter-sql.ts";
import {
  DATABASE_VALUE_GENERATORS,
  databaseValueGeneratorsForKind,
} from "../src/domain/database/value-generator.ts";

const gridStateSource = await readFile(
  new URL("../src/composables/database/useGridState.ts", import.meta.url),
  "utf8",
);
const tableFiltersSource = await readFile(
  new URL("../src/composables/database/useTableFilters.ts", import.meta.url),
  "utf8",
);
const rowMutationsSource = await readFile(
  new URL("../src/composables/database/useRowMutations.ts", import.meta.url),
  "utf8",
);
const tableV2GridSource = await readFile(
  new URL("../src/components/database/TableV2ResultGrid.vue", import.meta.url),
  "utf8",
);
const virtualGridSource = await readFile(
  new URL("../src/components/database/VirtualResultGrid.vue", import.meta.url),
  "utf8",
);
const resultPaneSource = await readFile(
  new URL("../src/components/database/results/DatabaseResultPane.vue", import.meta.url),
  "utf8",
);
const bulkDialogSource = await readFile(
  new URL("../src/components/database/results/DatabaseBulkUpdateDialog.vue", import.meta.url),
  "utf8",
);
const viewSource = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8",
);
const zhLocale = await readFile(
  new URL("../src/i18n/locales/zh.ts", import.meta.url),
  "utf8",
);
const enLocale = await readFile(
  new URL("../src/i18n/locales/en.ts", import.meta.url),
  "utf8",
);

test("buildColumnAlterSql renders dialect-aware column definitions", () => {
  const mysql = buildColumnAlterSql({
    dbType: "mysql",
    database: "app",
    table: "users",
    column: {
      name: "email",
      dataType: "varchar(255)",
      nullable: false,
      defaultValue: null,
      comment: "邮箱",
    },
  });
  assert.equal(
    mysql,
    "ALTER TABLE `app`.`users`\nADD `email` varchar(255) NOT NULL COMMENT '邮箱';",
  );

  const postgres = buildColumnAlterSql({
    dbType: "postgres",
    database: "app",
    table: "users",
    column: { name: "age", dataType: "integer", nullable: true, defaultValue: "0" },
  });
  assert.equal(postgres, 'ALTER TABLE "app"."users"\nADD "age" integer DEFAULT 0;');

  // sqlserver 未提供 database 时回退到 dbo，注释不进入列定义
  const sqlserver = buildColumnAlterSql({
    dbType: "sqlserver",
    table: "users",
    column: { name: "nick", dataType: "nvarchar(64)", nullable: true, comment: "ignored" },
  });
  assert.equal(sqlserver, "ALTER TABLE [dbo].[users]\nADD [nick] nvarchar(64);");
});

test("buildColumnAlterSql rejects unsupported dialects and empty table", () => {
  assert.equal(supportsCopyAsAlterSql("mysql"), true);
  assert.equal(supportsCopyAsAlterSql("mongodb"), false);
  assert.throws(
    () =>
      buildColumnAlterSql({
        dbType: "mongodb",
        table: "users",
        column: { name: "a", dataType: "text", nullable: true },
      }),
    /not supported/,
  );
  assert.throws(
    () =>
      buildColumnAlterSql({
        dbType: "mysql",
        table: "  ",
        column: { name: "a", dataType: "text", nullable: true },
      }),
    /table name/,
  );
});

test("value generators are filtered by field kind and produce plausible values", () => {
  assert.deepEqual(
    databaseValueGeneratorsForKind("number").map((item) => item.id),
    ["randomInt", "randomFloat"],
  );
  assert.deepEqual(
    databaseValueGeneratorsForKind("boolean").map((item) => item.id),
    ["randomBoolean"],
  );
  assert.deepEqual(
    databaseValueGeneratorsForKind("time").map((item) => item.id),
    ["nowTime"],
  );
  const textIds = databaseValueGeneratorsForKind("text").map((item) => item.id);
  for (const id of ["uuid", "nowDatetime", "today", "randomString", "email", "chineseName", "phone"]) {
    assert.ok(textIds.includes(id), id);
  }

  const byId = new Map(DATABASE_VALUE_GENERATORS.map((item) => [item.id, item]));
  assert.match(String(byId.get("uuid").generate()), /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  assert.match(String(byId.get("today").generate()), /^\d{4}-\d{2}-\d{2}$/);
  assert.match(String(byId.get("nowTime").generate()), /^\d{2}:\d{2}:\d{2}$/);
  assert.match(String(byId.get("phone").generate()), /^1[35789]\d{9}$/);
  assert.equal(typeof byId.get("randomInt").generate(), "number");
  assert.equal(typeof byId.get("randomBoolean").generate(), "boolean");
  for (const generator of DATABASE_VALUE_GENERATORS) {
    assert.ok(zhLocale.includes(`"database.valueGenerator.${generator.id}"`), `zh ${generator.id}`);
    assert.ok(enLocale.includes(`"database.valueGenerator.${generator.id}"`), `en ${generator.id}`);
  }
});

test("grid state persists frozen column count per grid", () => {
  assert.match(gridStateSource, /frozenColumnCount: number;/);
  assert.match(gridStateSource, /function setFrozenColumnCount\(count: number\)/);
  assert.match(gridStateSource, /updateActiveGridState\(\{ frozenColumnCount: next \}\)/);
});

test("table filters support excluding values with notEquals", () => {
  assert.match(tableFiltersSource, /function excludeByColumnValues\(column: string, values: Array<string \| null>\)/);
  assert.match(tableFiltersSource, /\("notEquals" as const\)/);
  assert.match(tableFiltersSource, /function excludeByColumnValue\(column: string, value: string \| null\)/);
});

test("filtering or excluding a NULL cell uses IS NULL / IS NOT NULL instead of the string NULL", () => {
  assert.match(tableFiltersSource, /operator: value === null \? \("isNull" as const\) : \("equals" as const\)/);
  assert.match(tableFiltersSource, /operator: value === null \? \("isNotNull" as const\) : \("notEquals" as const\)/);
  assert.match(
    rowMutationsSource,
    /function gridContextFilterValue\(value: unknown\): string \| null \{\s*return value === null \|\| value === undefined \? null : options\.formatCell\(value\);/,
  );
  assert.match(rowMutationsSource, /options\.filterByColumnValue\(context\.column, gridContextFilterValue\(context\.value\)\)/);
  assert.match(rowMutationsSource, /options\.excludeByColumnValue\(context\.column, gridContextFilterValue\(context\.value\)\)/);
  assert.doesNotMatch(rowMutationsSource, /ByColumnValue\(context\.column, options\.formatCell\(context\.value\)\)/);
});

test("row mutations expose set-null and bulk update flows", () => {
  assert.match(rowMutationsSource, /function setGridContextCellNull\(\)/);
  assert.match(rowMutationsSource, /function canSetGridContextNull\(\)/);
  assert.match(rowMutationsSource, /const bulkUpdateTarget = ref<BulkUpdateTarget>\(\)/);
  assert.match(rowMutationsSource, /function openBulkUpdate\(column: string, rowIndexes: number\[\], initialMode/);
  assert.match(rowMutationsSource, /function applyBulkUpdate\(payload: BulkUpdatePayload\)/);
  assert.match(rowMutationsSource, /function excludeByGridContextValue\(\)/);
});

test("grids emit header context menu and table-v2 pins frozen columns", () => {
  assert.match(virtualGridSource, /export interface ResultGridHeaderContextMenu/);
  assert.match(virtualGridSource, /"header-context-menu": \[payload: ResultGridHeaderContextMenu\]/);
  assert.match(virtualGridSource, /@contextmenu\.prevent="handleHeaderContextMenu\(\$event, column\.name\)"/);

  assert.match(tableV2GridSource, /"header-context-menu": \[payload: ResultGridHeaderContextMenu\]/);
  assert.match(tableV2GridSource, /frozenCount\?: number;/);
  assert.match(tableV2GridSource, /gridLayout\.value\.columns\.slice\(frozenColumnCount\.value\)/);
  assert.match(tableV2GridSource, /leftPad: Math\.max\(0, visible\[0\]\.left - frozenWidth\.value\)/);
  assert.match(tableV2GridSource, /fixed: TableV2FixedDir\.LEFT/);
});

test("result pane renders cell and header context menus", () => {
  assert.equal(resultPaneSource.match(/<AppContextMenu/g).length, 2);
  for (const eventName of [
    "exclude-context-value",
    "sort-context-column",
    "set-context-null",
    "bulk-update-context",
    "toggle-layout",
    "clear-selection",
    "export-grid",
    "refresh-grid",
    "header-context-menu",
    "copy-header-alter-sql",
    "hide-header-column",
    "freeze-header-column",
    "unfreeze-columns",
    "bulk-update-header",
  ]) {
    assert.ok(resultPaneSource.includes(`"${eventName}"`), eventName);
  }
  // 查询模式网格也接线右键菜单
  const queryGridSection = resultPaneSource.slice(0, resultPaneSource.indexOf("object === 'table' && selectedTable"));
  assert.ok(queryGridSection.includes("@context-menu=\"(payload) => emit('context-menu', payload)\""));
  assert.ok(queryGridSection.includes(":frozen-count=\"gridState.frozenColumnCount\""));
});

test("bulk update dialog offers fixed value and generator modes", () => {
  assert.match(bulkDialogSource, /databaseValueGeneratorsForKind\(fieldKind\.value\)/);
  assert.match(bulkDialogSource, /parseDatabaseRowFieldValue\(fieldKind\.value, valueRaw\.value/);
  assert.match(bulkDialogSource, /emit\("apply", \{ mode: "generator", generatorId: activeGenerator\.value\.id \}\)/);
});

test("grid context menu locale keys exist in zh and en", () => {
  const keys = [
    "database.sortAscending",
    "database.sortDescending",
    "database.clearSort",
    "database.excludeByCellValue",
    "database.excludeBySelectedValues",
    "database.columnDetails",
    "database.rowDetails",
    "database.setNullTitle",
    "database.pending.stagedRows",
    "database.pending.revert",
    "database.bulkUpdateTitle",
    "database.bulkUpdateSelected",
    "database.generateValue",
    "database.bulkUpdateAffectedRows",
    "database.bulkUpdateModeValue",
    "database.bulkUpdateModeGenerator",
    "database.bulkUpdateValueLabel",
    "database.bulkUpdateGeneratorLabel",
    "database.bulkUpdateGeneratorPreview",
    "database.bulkUpdateRegenerate",
    "database.bulkUpdateGeneratorHint",
    "database.bulkUpdateApply",
    "database.transposeView",
    "database.clearSelection",
    "database.copyColumnName",
    "database.copyAllColumnNames",
    "database.columnNameCopied",
    "database.copyAsAlterSql",
    "database.alterSqlCopied",
    "database.hideThisColumn",
    "database.freezeUpToColumn",
    "database.unfreezeColumns",
  ];
  for (const key of keys) {
    assert.ok(zhLocale.includes(`"${key}"`), `zh ${key}`);
    assert.ok(enLocale.includes(`"${key}"`), `en ${key}`);
  }
});
