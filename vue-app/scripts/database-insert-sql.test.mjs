import assert from "node:assert/strict";
import test from "node:test";
import {
  buildInsertRowSql,
  buildInsertRowsSql,
  supportsCopyAsInsertSql,
} from "../src/domain/database/insert-sql.ts";

test("builds MySQL-family INSERT SQL with escaped identifiers and text", () => {
  assert.equal(
    buildInsertRowSql({
      dbType: "mysql",
      database: "sales-db",
      table: "order`archive",
      columns: ["id", "customer", "metadata"],
      row: [42, "O'Reilly\\North", { active: true }],
    }),
    "INSERT INTO `sales-db`.`order``archive` (`id`, `customer`, `metadata`)\n" +
      "VALUES (42, 'O''Reilly\\\\North', '{\"active\":true}');"
  );
});

test("formats MySQL-family temporal columns without changing text columns", () => {
  const mysqlFamily = [
    "mysql",
    "mariadb",
    "tidb",
    "oceanbase",
    "polardb",
    "tdsql",
    "dolt",
    "greatsql",
    "gbase",
    "gbase8t",
    "goldendb",
    "starrocks",
    "doris",
  ];
  for (const dbType of mysqlFamily) {
    assert.equal(
      buildInsertRowSql({
        dbType,
        database: "testdb",
        table: "events",
        columns: ["created_at", "event_date", "description"],
        columnTypes: ["datetime(6)", "date", "varchar(64)"],
        row: [
          "2026-08-15T00:00:00.123456Z",
          "2026-08-15T00:00:00Z",
          "2026-08-15T00:00:00Z",
        ],
      }),
      "INSERT INTO `testdb`.`events` (`created_at`, `event_date`, `description`)\n" +
        "VALUES ('2026-08-15 00:00:00.123456', '2026-08-15', '2026-08-15T00:00:00Z');",
      dbType
    );
  }
});

test("formats PostgreSQL-family timestamps according to time-zone semantics", () => {
  const postgresFamily = [
    "gbase8c",
    "postgres",
    "kingbase",
    "gaussdb",
    "opengauss",
    "highgo",
    "vastbase",
    "opentenbase",
    "cockroachdb",
    "yugabyte",
    "greenplum",
    "questdb",
    "materialize",
    "risingwave",
  ];
  for (const dbType of postgresFamily) {
    assert.match(
      buildInsertRowSql({
        dbType,
        database: "public",
        table: "events",
        columns: ["local_at", "zoned_at", "note"],
        columnTypes: ["timestamp without time zone", "timestamp with time zone", "text"],
        row: [
          "2026-08-15T00:00:00.123Z",
          "2026-08-15T00:00:00.123Z",
          "2026-08-15T00:00:00.123Z",
        ],
      }),
      /VALUES \('2026-08-15 00:00:00\.123', '2026-08-15 00:00:00\.123\+00:00', '2026-08-15T00:00:00\.123Z'\);$/,
      dbType
    );
  }
});

test("formats SQL Server local and offset-aware temporal columns", () => {
  assert.match(
    buildInsertRowSql({
      dbType: "sqlserver",
      database: "dbo",
      table: "events",
      columns: ["local_at", "zoned_at"],
      columnTypes: ["datetime2(6)", "datetimeoffset(6)"],
      row: ["2026-08-15T00:00:00.123456Z", "2026-08-15T00:00:00.123456Z"],
    }),
    /VALUES \(N'2026-08-15T00:00:00\.123456', N'2026-08-15T00:00:00\.123456\+00:00'\);$/
  );
});

test("uses explicit Oracle-family temporal conversion expressions", () => {
  for (const dbType of ["oracle", "dameng", "yashandb"]) {
    assert.match(
      buildInsertRowSql({
        dbType,
        database: "APP",
        table: "EVENTS",
        columns: ["CREATED_ON", "CREATED_AT", "ZONED_AT"],
        columnTypes: ["DATE", "TIMESTAMP(6)", "TIMESTAMP(6) WITH TIME ZONE"],
        row: [
          "2026-08-15T00:00:00Z",
          "2026-08-15T00:00:00.123456Z",
          "2026-08-15T00:00:00.123456Z",
        ],
      }),
      /VALUES \(TO_DATE\('2026-08-15 00:00:00', 'YYYY-MM-DD HH24:MI:SS'\), TO_TIMESTAMP\('2026-08-15 00:00:00\.123456', 'YYYY-MM-DD HH24:MI:SS\.FF'\), TO_TIMESTAMP_TZ\('2026-08-15 00:00:00\.123456 \+00:00', 'YYYY-MM-DD HH24:MI:SS\.FF TZH:TZM'\)\);$/,
      dbType
    );
  }
});

test("uses typed Trino date and timestamp literals", () => {
  assert.match(
    buildInsertRowSql({
      dbType: "trino",
      database: "default",
      table: "events",
      columns: ["event_date", "local_at", "zoned_at"],
      columnTypes: ["date", "timestamp(3)", "timestamp(3) with time zone"],
      row: [
        "2026-08-15T00:00:00Z",
        "2026-08-15T00:00:00.123Z",
        "2026-08-15T00:00:00.123Z",
      ],
    }),
    /VALUES \(DATE '2026-08-15', TIMESTAMP '2026-08-15 00:00:00\.123', TIMESTAMP '2026-08-15 00:00:00\.123 \+00:00'\);$/
  );
});

test("formats temporal values for the remaining supported database dialects", () => {
  const cases = [
    ["sqlite", "datetime", "'2026-08-15T00:00:00Z'"],
    ["duckdb", "timestamp", "'2026-08-15 00:00:00'"],
    ["duckdb", "timestamptz", "'2026-08-15 00:00:00+00:00'"],
    ["firebird", "timestamp", "'2026-08-15 00:00:00'"],
    ["firebird", "timestamp with time zone", "'2026-08-15 00:00:00+00:00'"],
    ["clickhouse", "DateTime64(6)", "parseDateTime64BestEffort('2026-08-15T00:00:00Z', 6)"],
    ["tdengine", "timestamp", "'2026-08-15T00:00:00Z'"],
    ["cassandra", "date", "'2026-08-15'"],
    ["cassandra", "timestamp", "'2026-08-15T00:00:00Z'"],
  ];
  for (const [dbType, columnType, expected] of cases) {
    const sql = buildInsertRowSql({
      dbType,
      database: "main",
      table: "events",
      columns: ["created_at"],
      columnTypes: [columnType],
      row: ["2026-08-15T00:00:00Z"],
    });
    assert.equal(sql.endsWith(`VALUES (${expected});`), true, dbType);
  }
});

test("builds PostgreSQL-family INSERT SQL and preserves typed literals", () => {
  assert.equal(
    buildInsertRowSql({
      dbType: "postgres",
      database: "analytics",
      table: "user\"events",
      columns: ["id", "enabled", "note", "payload"],
      row: [9007199254740993n, true, null, ["a", 2]],
    }),
    'INSERT INTO "analytics"."user""events" ("id", "enabled", "note", "payload")\n' +
      `VALUES (9007199254740993, TRUE, NULL, '["a",2]');`
  );
});

test("omits SQLite main database qualification", () => {
  assert.equal(
    buildInsertRowSql({
      dbType: "sqlite",
      database: "main",
      table: "users",
      columns: ["id", "name"],
      row: [1, "Ada"],
    }),
    'INSERT INTO "users" ("id", "name")\nVALUES (1, \'Ada\');'
  );
});

test("uses SQL Server identifiers, Unicode strings, and binary literals", () => {
  assert.equal(
    buildInsertRowSql({
      dbType: "sqlserver",
      database: "dbo",
      table: "odd]table",
      columns: ["id", "display]name", "data"],
      row: [1, "中文", new Uint8Array([0, 15, 255])],
    }),
    "INSERT INTO [dbo].[odd]]table] ([id], [display]]name], [data])\n" +
      "VALUES (1, N'中文', 0x000FFF);"
  );
});

test("uses Oracle-compatible boolean and binary literals", () => {
  assert.equal(
    buildInsertRowSql({
      dbType: "oracle",
      database: "APP",
      table: "FLAGS",
      columns: ["ENABLED", "PAYLOAD"],
      row: [false, new Uint8Array([0xab, 0xcd])],
    }),
    'INSERT INTO "APP"."FLAGS" ("ENABLED", "PAYLOAD")\n' +
      "VALUES (0, HEXTORAW('ABCD'));"
  );
});

test("builds Cassandra CQL collections", () => {
  assert.equal(
    buildInsertRowSql({
      dbType: "cassandra",
      database: "shop",
      table: "users",
      columns: ["id", "tags", "profile"],
      row: ["user-1", ["new", "o'clock"], { active: true, score: 3 }],
    }),
    'INSERT INTO "shop"."users" ("id", "tags", "profile")\n' +
      "VALUES ('user-1', ['new', 'o''clock'], {'active': TRUE, 'score': 3});"
  );
});

test("builds one independent INSERT per row in rows mode", () => {
  assert.equal(
    buildInsertRowsSql({
      dbType: "mysql",
      database: "shop",
      table: "users",
      columns: ["id", "name"],
      rows: [
        [1, "Ada"],
        [2, "O'Neil"],
      ],
      mode: "rows",
    }),
    "INSERT INTO `shop`.`users` (`id`, `name`)\nVALUES (1, 'Ada');\n" +
      "INSERT INTO `shop`.`users` (`id`, `name`)\nVALUES (2, 'O''Neil');"
  );
});

test("merges all rows into one INSERT in batch mode", () => {
  assert.equal(
    buildInsertRowsSql({
      dbType: "postgres",
      database: "public",
      table: "users",
      columns: ["id", "enabled"],
      rows: [
        [1, true],
        [2, false],
      ],
      mode: "batch",
    }),
    'INSERT INTO "public"."users" ("id", "enabled")\nVALUES\n  (1, TRUE),\n  (2, FALSE);'
  );
});

test("defaults to rows mode and rejects empty row sets", () => {
  const single = buildInsertRowsSql({
    dbType: "sqlite",
    database: "main",
    table: "users",
    columns: ["id"],
    rows: [[1]],
  });
  assert.equal(single, 'INSERT INTO "users" ("id")\nVALUES (1);');
  assert.throws(
    () =>
      buildInsertRowsSql({
        dbType: "sqlite",
        table: "users",
        columns: ["id"],
        rows: [],
      }),
    /at least one row/
  );
});

test("rejects non-SQL data sources and mismatched rows", () => {
  assert.equal(supportsCopyAsInsertSql("mongodb"), false);
  assert.throws(
    () =>
      buildInsertRowSql({
        dbType: "mongodb",
        database: "shop",
        table: "users",
        columns: ["id"],
        row: [1],
      }),
    /not supported/
  );
  assert.throws(
    () =>
      buildInsertRowSql({
        dbType: "duckdb",
        database: "main",
        table: "users",
        columns: ["id", "name"],
        row: [1],
      }),
    /counts do not match/
  );
});
