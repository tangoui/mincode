import assert from "node:assert/strict";
import test from "node:test";
import {
  databaseConnectionFromImportUrl,
  databaseImportCommitRows,
  databaseImportErrorCode,
  databaseImportTargetGroup,
  parseDatabaseImportPreview,
  parseDatabaseImportUrls,
  parseDetectedImportLocations,
  resolveDatabaseImportRows,
} from "../src/domain/database/connection-import.ts";

function row(overrides) {
  return {
    id: "r1",
    name: "Row",
    sourceType: "mysql8",
    status: "ready",
    dbType: "mysql",
    host: "db.test",
    port: 3306,
    ssl: false,
    hasPassword: false,
    passwordUnavailable: false,
    ...overrides,
  };
}

function preview(rows, extra = {}) {
  return parseDatabaseImportPreview({ importId: "imp-1", source: "dbeaver", path: "/x", rows, ...extra });
}

test("validates preview payloads and drops malformed rows", () => {
  assert.equal(parseDatabaseImportPreview(null), undefined);
  assert.equal(parseDatabaseImportPreview({ importId: "a", source: "other", rows: [] }), undefined);
  assert.equal(parseDatabaseImportPreview({ source: "navicat", rows: [] }), undefined);
  const parsed = preview(
    [
      row({ id: "r1", password: "leak", tunnel: { type: "ssh", host: "jump", username: "u", hasPassword: true } }),
      row({ id: "bad id" }),
      row({ id: "r3", status: "ready", dbType: "nope" }),
      row({ id: "r4", status: "needsPlugin", dbType: undefined, pluginDriverId: "hive" }),
      row({ id: "r5", status: "needsPlugin", dbType: undefined, pluginDriverId: "Bad!" }),
      row({ id: "r6", port: 99999, warnings: ["tunnel_incomplete", "<script>", 5] }),
      "junk",
    ],
    { warnings: ["truncated"] },
  );
  assert.deepEqual(
    parsed.rows.map((item) => [item.id, item.status]),
    [
      ["r1", "ready"],
      ["r3", "unsupported"],
      ["r4", "unsupported"],
      ["r5", "unsupported"],
      ["r6", "ready"],
    ],
  );
  assert.equal("password" in parsed.rows[0], false);
  assert.deepEqual(parsed.rows[0].tunnel, {
    type: "ssh",
    host: "jump",
    port: 22,
    username: "u",
    authType: "password",
    keyPath: undefined,
    hasPassword: true,
    hasPassphrase: false,
  });
  assert.equal(parsed.rows[4].port, 0);
  assert.deepEqual(parsed.rows[4].warnings, ["tunnel_incomplete"]);
  assert.deepEqual(parsed.warnings, ["truncated"]);
});

test("validates detected locations", () => {
  assert.deepEqual(
    parseDetectedImportLocations([
      { source: "dbeaver", path: "/a/.dbeaver", label: "DBeaver · General" },
      { source: "other", path: "/b" },
      { source: "workbench", path: "" },
      { source: "datagrip", path: "/c" },
    ]),
    [
      { source: "dbeaver", path: "/a/.dbeaver", label: "DBeaver · General" },
      { source: "datagrip", path: "/c", label: "/c" },
    ],
  );
  assert.deepEqual(parseDetectedImportLocations("x"), []);
});

test("filters unsupported types and retains duplicate detection", () => {
  const candidates = preview([
    row({ id: "r1", name: "Orders" }),
    row({ id: "r2", name: "Orders copy" }),
    row({ id: "r3", name: "Existing", host: "DB.test", port: 3306, database: "shop", username: "app" }),
    row({ id: "r4", name: "Hive", status: "needsPlugin", dbType: undefined, pluginDriverId: "hive", port: 10000 }),
    row({ id: "r5", name: "Vertica", status: "needsPlugin", dbType: undefined, pluginDriverId: "vertica" }),
    row({ id: "r6", name: "CH", dbType: "clickhouse", port: 8123 }),
    row({ id: "r7", name: "Yashan", dbType: "yashandb", port: 1688 }),
    row({ id: "r8", name: "DB2", status: "unsupported", dbType: undefined }),
    row({ id: "r9", name: "Cache", dbType: "sqlite", host: "", port: 0, filePath: "/tmp/a.db" }),
    row({ id: "r10", name: "Old hive", status: "needsPlugin", dbType: undefined, pluginDriverId: "hive", host: "old.test" }),
  ]).rows;
  const resolved = resolveDatabaseImportRows(candidates, {
    connections: [
      { name: "Shop", dbType: "mysql", host: "db.test", port: 3306, database: "shop", username: "app" },
      { name: "Local", dbType: "sqlite", host: "", port: 0, filePath: "/tmp/a.db" },
      { name: "Legacy hive", dbType: "plugin", host: "old.test", port: 3306, driver: { pluginId: "hive-driver", driverId: "hive" } },
    ],
    supportedTypes: new Set(["mysql", "sqlite", "postgres"]),
  });
  const byName = Object.fromEntries(resolved.map((item) => [item.candidate.name, item]));
  assert.equal(byName.Orders.status, "ready");
  assert.equal(byName.Orders.defaultSelected, true);
  assert.equal(byName["Orders copy"].duplicateOf, "Orders");
  assert.equal(byName["Orders copy"].defaultSelected, false);
  assert.equal(byName.Existing.duplicateOf, "Shop");
  assert.equal(byName.Hive.status, "unsupported");
  assert.equal(byName.Vertica.status, "unsupported");
  assert.equal(byName.CH.status, "unsupported");
  assert.equal(byName.Yashan.status, "unsupported");
  assert.equal(byName.DB2.status, "unsupported");
  assert.equal(byName.DB2.defaultSelected, false);
  assert.equal(byName.Cache.duplicateOf, "Local");
  assert.equal(byName.Hive.duplicateOf, undefined);

  const selected = new Set(resolved.map((item) => item.candidate.id));
  assert.deepEqual(
    databaseImportCommitRows(resolved, selected).map((item) => item.id),
    ["r1", "r2", "r3", "r9"],
  );
  assert.deepEqual(databaseImportCommitRows(resolved, new Set(["r4", "r5", "r8"])), []);
});

test("unknown build capabilities keep every built-in type ready", () => {
  const [resolved] = resolveDatabaseImportRows([preview([row({ dbType: "clickhouse" })]).rows[0]], {
    connections: [],
    supportedTypes: null,
  });
  assert.equal(resolved.status, "ready");
  assert.equal(resolved.driver, undefined);
});

test("builds target groups", () => {
  assert.equal(databaseImportTargetGroup("Imported", "Prod/Reports", true), "Imported/Prod/Reports");
  assert.equal(databaseImportTargetGroup("", "/Prod/", true), "Prod");
  assert.equal(databaseImportTargetGroup(" Imported ", "Prod", false), "Imported");
  assert.equal(databaseImportTargetGroup("g", "x".repeat(200), true), "g");
});

test("parses pasted URL lines", () => {
  const result = parseDatabaseImportUrls(
    [
      "# comment",
      "mysql://app:secret@db.test:3307/shop",
      "",
      "jdbc:postgresql://pg.test/analytics?sslmode=require",
      "not a url",
      "mongodb://mongo.test/admin",
    ].join("\n"),
  );
  assert.deepEqual(result.invalidLines, [5]);
  assert.equal(result.truncated, false);
  assert.deepEqual(
    result.rows.map((item) => [item.line, item.candidate.name, item.candidate.dbType, item.candidate.port, item.candidate.hasPassword]),
    [
      [2, "db.test/shop", "mysql", 3307, true],
      [4, "pg.test/analytics", "postgres", 5432, false],
      [6, "mongo.test/admin", "mongodb", 27017, false],
    ],
  );
  assert.equal(result.rows[1].candidate.ssl, true);
  const many = parseDatabaseImportUrls(Array.from({ length: 205 }, (_, index) => `mysql://h${index}.test/db`).join("\n"));
  assert.equal(many.rows.length, 200);
  assert.equal(many.truncated, true);
});

test("builds connections from pasted URLs", () => {
  const [urlRow] = parseDatabaseImportUrls("mysql://app:secret@db.test:3307/shop").rows;
  const connection = databaseConnectionFromImportUrl(urlRow, { id: "c1", group: " Team ", now: 42 });
  assert.deepEqual(connection, {
    id: "c1",
    name: "db.test/shop",
    dbType: "mysql",
    host: "db.test",
    port: 3307,
    username: "app",
    password: "secret",
    database: "shop",
    token: undefined,
    org: undefined,
    authSource: undefined,
    ssl: false,
    savePassword: true,
    group: "Team",
    tags: [],
    createdAt: 42,
    updatedAt: 42,
  });
  const [chRow] = parseDatabaseImportUrls("clickhouse://ch.test:8123/default").rows;
  const bound = databaseConnectionFromImportUrl(
    chRow,
    { id: "c2", group: "", now: 1 },
  );
  assert.equal(bound.savePassword, false);
  assert.equal(bound.group, undefined);
  assert.equal(bound.driver, undefined);
});

test("extracts backend import error codes", () => {
  assert.equal(databaseImportErrorCode(new Error("db_import: file_too_large")), "file_too_large");
  assert.equal(databaseImportErrorCode("db_import: malformed: unexpected EOF"), "malformed");
  assert.equal(databaseImportErrorCode(new Error("other")), undefined);
});
