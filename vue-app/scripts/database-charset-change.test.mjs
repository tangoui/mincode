import assert from "node:assert/strict";
import test from "node:test";

import {
  buildCharsetChangeSql,
  charsetOptionsQuery,
  currentCharsetQuery,
  parseCharsetOptions,
  parseCurrentCharset,
  supportsDatabaseCharsetChange,
} from "../src/domain/database/charset-change.ts";

const result = (columns, rows) => ({ columns, rows, rowCount: rows.length, executionMs: 1 });

test("enables only MySQL-compatible dialects with safe ALTER syntax", () => {
  for (const type of ["mysql", "mariadb", "tidb", "oceanbase", "polardb", "tdsql", "dolt", "greatsql"]) {
    assert.equal(supportsDatabaseCharsetChange(type), true, type);
  }
  for (const type of ["postgres", "sqlserver", "oracle", "dameng", "sqlite", "clickhouse"]) {
    assert.equal(supportsDatabaseCharsetChange(type), false, type);
  }
});

test("builds quoted database and table character set statements", () => {
  assert.equal(
    buildCharsetChangeSql(
      { kind: "database", database: "app`data" },
      { characterSet: "utf8mb4", collation: "utf8mb4_0900_ai_ci" }
    ),
    "ALTER DATABASE `app``data` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;"
  );
  assert.equal(
    buildCharsetChangeSql(
      { kind: "table", database: "app", table: "users" },
      { characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci" }
    ),
    "ALTER TABLE `app`.`users` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
  );
  assert.throws(() =>
    buildCharsetChangeSql(
      { kind: "database", database: "app" },
      { characterSet: "utf8mb4; DROP DATABASE app", collation: "utf8mb4_unicode_ci" }
    )
  );
});

test("parses supported options and current values from case-insensitive columns", () => {
  const options = parseCharsetOptions(
    result(
      ["CHARACTERSET", "COLLATION"],
      [
        ["utf8mb4", "utf8mb4_unicode_ci"],
        ["utf8mb4", "utf8mb4_0900_ai_ci"],
        ["latin1", "latin1_swedish_ci"],
        ["bad charset", "bad-collation"],
      ]
    )
  );
  assert.deepEqual(options, [
    { characterSet: "latin1", collations: ["latin1_swedish_ci"] },
    { characterSet: "utf8mb4", collations: ["utf8mb4_0900_ai_ci", "utf8mb4_unicode_ci"] },
  ]);
  assert.deepEqual(
    parseCurrentCharset(result(["characterSet", "collation"], [["utf8mb4", "utf8mb4_unicode_ci"]])),
    { characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci" }
  );
});

test("encodes metadata names without SQL string interpolation", () => {
  assert.match(charsetOptionsQuery(), /information_schema\.COLLATIONS/);
  const query = currentCharsetQuery({
    kind: "table",
    database: "app\\'s",
    table: "用户表",
  });
  assert.match(query, /TABLE_SCHEMA = CONVERT\(X'6170705c2773' USING utf8mb4\)/);
  assert.match(query, /TABLE_NAME = CONVERT\(X'e794a8e688b7e8a1a8' USING utf8mb4\)/);
  assert.doesNotMatch(query, /app\\'s|用户表/);
});
