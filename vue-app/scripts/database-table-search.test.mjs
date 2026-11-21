import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

import {
  buildMongoTableFilterQuery,
  buildTableFilterCountSql,
  buildTableFilterSql,
  buildTableRowCountSql,
  buildTableSearchCountSql,
  buildTableSearchSql,
  createTableRowFilter,
  filterTableRows,
  isTextSearchableColumnType,
  lastPageFromTotal,
  tableQueryPageSize,
} from "../src/domain/database/table-search.ts";

const columns = ["id", "name", "created_at", "enabled"];
const rows = [
  [1, "Alpha", "2026-01-10", true],
  [2, "Beta", "2026-02-20", false],
  [3, "Alphabet", "2026-03-15", "1"],
];

function condition(id, column, fieldType, operator, value, valueTo) {
  return { id, column, fieldType, operator, value, valueTo };
}

test("returns the original rows when there are no complete filters", () => {
  const result = filterTableRows(rows, columns, {
    logic: "and",
    conditions: [condition("empty", "name", "text", "contains", "")],
  });

  assert.equal(result, rows);
});

test("combines compiled text and numeric filters without changing row identity", () => {
  const result = filterTableRows(rows, columns, {
    logic: "and",
    conditions: [
      condition("name", "name", "text", "contains", "alpha"),
      condition("id", "id", "number", "greaterThan", "1"),
    ],
  });

  assert.deepEqual(result, [rows[2]]);
  assert.equal(result[0], rows[2]);
});

test("short-circuits OR filters and preserves missing-column behavior", () => {
  const matches = createTableRowFilter(columns, {
    logic: "or",
    conditions: [
      condition("missing", "missing", "text", "equals", "anything"),
      condition("enabled", "enabled", "boolean", "isTrue"),
    ],
  });

  assert.ok(matches);
  assert.equal(matches(rows[0]), true);
  assert.equal(matches(rows[1]), false);
  assert.equal(matches(rows[2]), true);
});

test("compares date ranges with precompiled boundaries", () => {
  const result = filterTableRows(rows, columns, {
    logic: "and",
    conditions: [
      condition("created", "created_at", "date", "between", "2026-02-01", "2026-03-01"),
    ],
  });

  assert.deepEqual(result, [rows[1]]);
});

test("distinguishes NULL-only filters from empty strings", () => {
  const nullableRows = [[null], [undefined], [""], ["NULL"], ["value"]];
  const onlyNull = filterTableRows(nullableRows, ["value"], {
    logic: "and",
    conditions: [condition("null", "value", "text", "isNull")],
  });
  const onlyNotNull = filterTableRows(nullableRows, ["value"], {
    logic: "and",
    conditions: [condition("not-null", "value", "text", "isNotNull")],
  });

  assert.deepEqual(onlyNull, nullableRows.slice(0, 2));
  assert.deepEqual(onlyNotNull, nullableRows.slice(2));
});

test("supports lexical greater-than and less-than filters for text cells", () => {
  const textRows = [["Alpha"], ["Beta"], ["Gamma"]];
  assert.deepEqual(filterTableRows(textRows, ["name"], {
    logic: "and",
    conditions: [condition("gt", "name", "text", "greaterThan", "Beta")],
  }), [textRows[2]]);
  assert.deepEqual(filterTableRows(textRows, ["name"], {
    logic: "and",
    conditions: [condition("lt", "name", "text", "lessThan", "Beta")],
  }), [textRows[0]]);
});

test("generates exact NULL predicates for server-side filtering", () => {
  const nullSql = buildTableFilterSql("mysql", "shop", "orders", {
    logic: "and",
    conditions: [condition("null", "note", "text", "isNull")],
  }, 50, 0);
  const notNullSql = buildTableFilterSql("postgres", "shop", "orders", {
    logic: "and",
    conditions: [condition("not-null", "note", "text", "isNotNull")],
  }, 50, 0);

  assert.match(nullSql, /`note` IS NULL/);
  assert.match(notNullSql, /"note" IS NOT NULL/);
});

test("uses the first matching column when query results contain duplicate names", () => {
  const duplicateRows = [["first", "second"]];
  const result = filterTableRows(duplicateRows, ["name", "name"], {
    logic: "and",
    conditions: [condition("name", "name", "text", "equals", "first")],
  });

  assert.deepEqual(result, duplicateRows);
});

test("excludes MySQL-family vector columns from full-table text search", () => {
  // MySQL 9.x 的 VECTOR 列无法 CAST(列 AS CHAR)，运行时抛 Error 1210，必须排除。
  assert.equal(isTextSearchableColumnType("mysql", "vector(1024)"), false);
  assert.equal(isTextSearchableColumnType("mysql", "VECTOR"), false);
  assert.equal(isTextSearchableColumnType("mariadb", "vector"), false);
  assert.equal(isTextSearchableColumnType("tidb", "vector"), false);
});

test("keeps ordinary columns and non-MySQL vector types searchable", () => {
  assert.equal(isTextSearchableColumnType("mysql", "varchar(1000)"), true);
  assert.equal(isTextSearchableColumnType("mysql", "int"), true);
  assert.equal(isTextSearchableColumnType("mysql", undefined), true);
  // PostgreSQL 系 CAST(col AS TEXT) 对 vector 可用，不受影响。
  assert.equal(isTextSearchableColumnType("postgres", "vector"), true);
});

test("quotes the counted table per dialect", () => {
  assert.equal(buildTableRowCountSql("mysql", "shop", "orders"), "SELECT COUNT(*) FROM `shop`.`orders`");
  assert.equal(
    buildTableRowCountSql("postgres", "shop", "public.orders"),
    'SELECT COUNT(*) FROM "shop"."public.orders"'
  );
});

test("counts with COUNT_BIG on SQL Server and never adds paging clauses", () => {
  // COUNT(*) 是 int，超过 2^31 行会报算术溢出。
  const sql = buildTableRowCountSql("sqlserver", "shop", "orders");
  assert.equal(sql, "SELECT COUNT_BIG(*) FROM [shop].[dbo].[orders]");
  assert.doesNotMatch(sql, /ORDER BY|OFFSET|FETCH NEXT|LIMIT/);
});

test("reuses the search WHERE clause without a LIMIT", () => {
  const mysql = buildTableSearchCountSql("mysql", "shop", "orders", ["name", "note"], "a%b");
  assert.match(mysql, /^SELECT COUNT\(\*\) FROM `shop`\.`orders` WHERE /);
  assert.match(mysql, /CAST\(`name` AS CHAR\) LIKE/);
  assert.match(mysql, /ESCAPE/);
  assert.match(mysql, / OR /);
  assert.doesNotMatch(mysql, /LIMIT|OFFSET/);
  // PostgreSQL 用 ILIKE 做大小写不敏感匹配，计数必须沿用同一算子。
  assert.match(buildTableSearchCountSql("postgres", "shop", "orders", ["name"], "x"), /ILIKE/);
});

test("reuses the filter WHERE clause including its logic operator", () => {
  const model = {
    logic: "or",
    conditions: [
      condition("a", "id", "number", "greaterThan", "1"),
      condition("b", "name", "text", "equals", "Alpha"),
    ],
  };
  const sql = buildTableFilterCountSql("mysql", "shop", "orders", model);
  assert.match(sql, /^SELECT COUNT\(\*\) FROM `shop`\.`orders` WHERE /);
  assert.match(sql, / OR /);
  assert.doesNotMatch(sql, /LIMIT|OFFSET/);

  assert.throws(() =>
    buildTableFilterCountSql("mysql", "shop", "orders", {
      logic: "and",
      conditions: [condition("empty", "name", "text", "contains", "")],
    })
  );
});

test("refuses to count on databases without generated SQL", () => {
  for (const dbType of ["mongodb", "elasticsearch", "qdrant", "pinecone"]) {
    assert.throws(() => buildTableRowCountSql(dbType, "shop", "orders"));
    assert.throws(() => buildTableSearchCountSql(dbType, "shop", "orders", ["name"], "x"));
    assert.throws(() =>
      buildTableFilterCountSql(dbType, "shop", "orders", {
        logic: "and",
        conditions: [condition("a", "id", "number", "greaterThan", "1")],
      })
    );
  }
});

test("derives the last page from the page size the query actually honors", () => {
  assert.equal(lastPageFromTotal(0, 50), 1);
  assert.equal(lastPageFromTotal(50, 50), 1);
  assert.equal(lastPageFromTotal(51, 50), 2);

  // 生成的搜索/筛选 SQL 把 LIMIT 夹到 5000，而页大小下拉给到 10000。
  // 用原始页大小做除数会少算，尾页跳不到真正的最后一页。
  assert.equal(tableQueryPageSize(10000), 5000);
  assert.equal(tableQueryPageSize(50), 50);
  assert.equal(lastPageFromTotal(10000, tableQueryPageSize(10000)), 2);
});

test("the last-page button counts without clobbering the visible grid", async () => {
  const pagination = await readFile(
    new URL("../src/composables/database/useTablePagination.ts", import.meta.url),
    "utf8"
  );
  const handler = pagination.match(/async function goToLastPage[\s\S]*?\n  }\n/)?.[0];
  assert.ok(handler, "goToLastPage implementation must be present");

  // executeTableQuery 会写 runtime.result，网格会被那一格计数顶掉。
  assert.match(handler, /executeQueryForConnection\(/);
  assert.doesNotMatch(handler, /executeTableQuery\(/);
  // 除数必须是生效后的页大小，否则筛选状态下尾页少算。
  assert.match(handler, /tableQueryPageSize\(databaseStore\.pageSize\)/);

  const pane = await readFile(
    new URL("../src/components/database/results/DatabaseResultPane.vue", import.meta.url),
    "utf8"
  );
  assert.match(pane, /direction="double-right"/);
  assert.match(pane, /emit\('last-page'\)/);
});

function filterSql(dbType, ...conditions) {
  return buildTableFilterSql(dbType, "shop", "orders", { logic: "and", conditions }, 50, 0);
}

// 按 MySQL/ClickHouse/TDengine 规则（反斜杠与 '' 都是转义）读出一个字符串字面量。
function readBackslashStringLiteral(sql, start) {
  assert.equal(sql[start], "'");
  let value = "";
  for (let index = start + 1; index < sql.length; index += 1) {
    const character = sql[index];
    if (character === "\\") {
      value += sql[index + 1];
      index += 1;
    } else if (character === "'") {
      if (sql[index + 1] !== "'") return { value, end: index + 1 };
      value += "'";
      index += 1;
    } else {
      value += character;
    }
  }
  assert.fail(`unterminated string literal in ${sql}`);
}

const INJECTION = String.raw`\' OR 1=1 -- `;

test("doubles backslashes in literals for dialects that treat them as escapes", () => {
  for (const dbType of ["mysql", "mariadb", "tidb", "doris", "clickhouse", "tdengine"]) {
    const sql = filterSql(dbType, condition("eq", "name", "text", "equals", INJECTION));
    const start = sql.indexOf("= '") + 2;
    const literal = readBackslashStringLiteral(sql, start);
    // 载荷必须完整留在字面量里，字面量之后只能是分页子句。
    assert.equal(literal.value, INJECTION, `${dbType}: ${sql}`);
    assert.equal(sql.slice(literal.end), " LIMIT 50 OFFSET 0", `${dbType}: ${sql}`);
  }

  // 标准 SQL 字符串里反斜杠是普通字符，不能被加倍。
  assert.match(
    filterSql("postgres", condition("eq", "name", "text", "equals", INJECTION)),
    /"name" = '\\'' OR 1=1 -- ' LIMIT/
  );
  assert.match(
    filterSql("sqlserver", condition("eq", "name", "text", "equals", "a\\b")),
    /\[name\] = 'a\\b' ORDER BY/
  );
});

test("keeps the injection payload inside LIKE patterns and full-table search", () => {
  const contains = filterSql("mysql", condition("c", "name", "text", "contains", INJECTION));
  const containsStart = contains.indexOf("LIKE '") + 5;
  const containsLiteral = readBackslashStringLiteral(contains, containsStart);
  assert.equal(containsLiteral.value, `%${INJECTION}%`);
  assert.equal(contains.slice(containsLiteral.end), " ESCAPE '!' LIMIT 50 OFFSET 0");

  const search = buildTableSearchSql("mysql", "shop", "orders", ["name"], INJECTION, 50, 0);
  const searchStart = search.indexOf("LIKE '") + 5;
  const searchLiteral = readBackslashStringLiteral(search, searchStart);
  assert.equal(searchLiteral.value, `%${INJECTION.trim()}%`);
  assert.equal(search.slice(searchLiteral.end), " ESCAPE '!' LIMIT 50 OFFSET 0");

  const clickhouse = buildTableSearchSql("clickhouse", "shop", "orders", ["name"], INJECTION, 50, 0);
  const clickhouseStart = clickhouse.indexOf("LIKE '") + 5;
  const clickhouseLiteral = readBackslashStringLiteral(clickhouse, clickhouseStart);
  // ClickHouse 的 LIKE 以反斜杠转义，字面量解码后应是 %\\' OR 1=1 --%。
  assert.equal(clickhouseLiteral.value, String.raw`%\\' OR 1=1 --%`);
  assert.equal(clickhouse.slice(clickhouseLiteral.end), " LIMIT 50 OFFSET 0");
});

test("escapes user wildcards before adding the contains/startsWith/endsWith anchors", () => {
  assert.match(
    filterSql("mysql", condition("c", "code", "text", "contains", "50%")),
    /CAST\(`code` AS CHAR\) LIKE '%50!%%' ESCAPE '!'/
  );
  assert.match(
    filterSql("postgres", condition("s", "code", "text", "startsWith", "a_b!")),
    /CAST\("code" AS TEXT\) ILIKE 'a!_b!!%' ESCAPE '!'/
  );
  assert.match(
    filterSql("sqlite", condition("e", "code", "text", "endsWith", "%")),
    /LIKE '%!%' ESCAPE '!'/
  );
  assert.match(
    filterSql("sqlserver", condition("n", "code", "text", "notContains", "_")),
    /NOT \(CAST\(\[code\] AS NVARCHAR\(MAX\)\) LIKE '%!_%' ESCAPE '!'\)/
  );
  // ClickHouse 旧版本与 TDengine 不认 ESCAPE 子句，改用反斜杠转义通配符。
  const clickhouse = filterSql("clickhouse", condition("c", "code", "text", "contains", "50%"));
  assert.match(clickhouse, /toString\(`code`\) LIKE '%50\\\\%%' LIMIT/);
  assert.doesNotMatch(clickhouse, /ESCAPE/);
  const tdengine = filterSql("tdengine", condition("c", "code", "text", "startsWith", "a_"));
  assert.match(tdengine, /CAST\(`code` AS NCHAR\) LIKE 'a\\\\_%' LIMIT/);
  assert.doesNotMatch(tdengine, /ESCAPE/);
});

test("treats isEmpty/isNotEmpty as NULL-or-blank only for text fields", () => {
  assert.match(
    filterSql("postgres", condition("e", "note", "text", "isEmpty")),
    /WHERE \("note" IS NULL OR CAST\("note" AS TEXT\) = ''\) LIMIT/
  );
  assert.match(
    filterSql("postgres", condition("ne", "note", "text", "isNotEmpty")),
    /WHERE \("note" IS NOT NULL AND CAST\("note" AS TEXT\) <> ''\) LIMIT/
  );
  assert.match(
    filterSql("mysql", condition("e", "note", "text", "isEmpty")),
    /WHERE \(`note` IS NULL OR `note` = ''\) LIMIT/
  );
  // 数值/日期列在 PostgreSQL 上做 = '' 会报类型错误，只能退化为 NULL 判断。
  const numeric = filterSql("postgres", condition("e", "qty", "number", "isEmpty"));
  assert.match(numeric, /WHERE "qty" IS NULL LIMIT/);
  assert.doesNotMatch(numeric, /''/);
  assert.match(filterSql("postgres", condition("ne", "at", "date", "isNotEmpty")), /WHERE "at" IS NOT NULL LIMIT/);
  assert.match(filterSql("postgres", condition("n", "note", "text", "isNull")), /WHERE "note" IS NULL LIMIT/);

  const mongo = (operator) =>
    JSON.parse(
      buildMongoTableFilterQuery("shop", "orders", { logic: "and", conditions: [condition("m", "note", "text", operator)] }, 50, 0)
    ).filter.$and[0];
  assert.deepEqual(mongo("isEmpty"), { note: { $in: [null, ""] } });
  assert.deepEqual(mongo("isNotEmpty"), { note: { $nin: [null, ""] } });
  assert.deepEqual(mongo("isNull"), { note: null });
  assert.deepEqual(mongo("isNotNull"), { note: { $ne: null } });
});

test("uses TRUE/FALSE for every PostgreSQL-compatible dialect", () => {
  for (const dbType of ["postgres", "cockroachdb", "kingbase", "yugabyte", "greenplum", "sqlite", "duckdb"]) {
    assert.match(filterSql(dbType, condition("t", "enabled", "boolean", "isTrue")), /"enabled" = TRUE/, dbType);
    assert.match(filterSql(dbType, condition("f", "enabled", "boolean", "isFalse")), /"enabled" = FALSE/, dbType);
  }
  assert.match(filterSql("mysql", condition("t", "enabled", "boolean", "isTrue")), /`enabled` = 1/);
  assert.match(filterSql("sqlserver", condition("f", "enabled", "boolean", "isFalse")), /\[enabled\] = 0/);
});

test("emits numeric filter values verbatim so BIGINT keeps its precision", () => {
  assert.match(filterSql("mysql", condition("n", "id", "number", "equals", "9007199254740993")), /`id` = 9007199254740993 LIMIT/);
  assert.match(filterSql("postgres", condition("n", "price", "number", "greaterThan", " 12.50 ")), /"price" > 12\.50 LIMIT/);
  assert.match(filterSql("postgres", condition("n", "price", "number", "lessThan", "-1.5e3")), /"price" < -1\.5e3 LIMIT/);
  assert.match(filterSql("postgres", condition("n", "price", "number", "equals", "+7")), /"price" = 7 LIMIT/);
  assert.match(
    filterSql("mysql", condition("b", "id", "number", "between", "9007199254740993", "9007199254740995")),
    /`id` BETWEEN 9007199254740993 AND 9007199254740995/
  );
  for (const invalid of ["abc", "1; DROP TABLE orders", "0x10", "Infinity", "1 2"]) {
    assert.throws(() => filterSql("mysql", condition("n", "id", "number", "equals", invalid)), /Invalid numeric/, invalid);
  }
});
