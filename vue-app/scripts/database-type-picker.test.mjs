import assert from "node:assert/strict";
import test from "node:test";
import {
  groupDatabaseTypeMatches,
  moveGridFocus,
  normalizeDatabaseTypeSearch,
  recentDatabaseTypes,
} from "../src/domain/database/type-picker.ts";
import { readFile } from "node:fs/promises";
import {
  databaseDefaultPorts,
  databaseDriverInfo,
  databaseTypeLabels,
} from "../src/domain/database/types.ts";
import { databaseTypeMatchesSearch } from "../src/domain/database/type-picker-search.ts";
import { databaseTypeBrand } from "../src/domain/database/type-brands.ts";
import { parseDatabaseConnectionUrl } from "../src/domain/database/connection-url.ts";
import { databaseConnectionCapabilities } from "../src/domain/database/db-type-capabilities.ts";
import { mysqlLikeTypes, postgresLikeTypes } from "../src/domain/database/object-naming.ts";

test("derives recently used database types from saved connections", () => {
  const connections = [
    { dbType: "mysql", lastConnectedAt: 10, updatedAt: 5 },
    { dbType: "redis", updatedAt: 40 },
    { dbType: "mysql", lastConnectedAt: 50 },
    { dbType: "postgres", lastConnectedAt: 30 },
    { dbType: "mongodb", lastConnectedAt: 99, driver: { pluginId: "p", driverId: "d" } },
    { dbType: "oracle", lastConnectedAt: 70 },
  ];
  assert.deepEqual(recentDatabaseTypes(connections, (type) => type !== "oracle"), ["mysql", "redis", "postgres"]);
  assert.deepEqual(recentDatabaseTypes(connections, () => true, 2), ["oracle", "mysql"]);
  assert.deepEqual(recentDatabaseTypes([], () => true), []);
});

test("groups search matches by category without duplicates", () => {
  const categories = [
    { id: "rel", types: ["mysql", "postgres", "sqlite"] },
    { id: "domestic", types: ["kingbase", "tidb"] },
    { id: "vector", types: ["pgvector"] },
  ];
  const drivers = { mysql: "MySQL", postgres: "PostgreSQL", sqlite: "SQLite", kingbase: "PostgreSQL", tidb: "MySQL", pgvector: "PostgreSQL" };
  const text = (type) => `${type} ${drivers[type]}`;
  assert.deepEqual(
    groupDatabaseTypeMatches(categories, " Postgre SQL ", text).map((group) => [group.category.id, group.types]),
    [["rel", ["postgres"]], ["domestic", ["kingbase"]], ["vector", ["pgvector"]]],
  );
  assert.deepEqual(groupDatabaseTypeMatches(categories, "", text), []);
  assert.deepEqual(groupDatabaseTypeMatches([...categories, { id: "recent", types: ["mysql"] }], "mysql", text).map((group) => group.category.id), ["rel", "domestic"]);
  assert.equal(normalizeDatabaseTypeSearch(" Open_Gauss-DB "), "opengaussdb");
});

test("moves keyboard focus through the type grid", () => {
  assert.equal(moveGridFocus(-1, 10, 4, "ArrowRight"), 0);
  assert.equal(moveGridFocus(-1, 10, 4, "ArrowUp"), 9);
  assert.equal(moveGridFocus(1, 10, 4, "ArrowDown"), 5);
  assert.equal(moveGridFocus(8, 10, 4, "ArrowDown"), 9);
  assert.equal(moveGridFocus(2, 10, 4, "ArrowUp"), 0);
  assert.equal(moveGridFocus(9, 10, 4, "ArrowRight"), 9);
  assert.equal(moveGridFocus(3, 10, 4, "Enter"), 3);
  assert.equal(moveGridFocus(0, 0, 4, "ArrowDown"), -1);
});

const compatibleTypes = [
  { dbType: "dolt", label: "Dolt", driver: "MySQL", port: 3306, family: mysqlLikeTypes },
  { dbType: "greatsql", label: "GreatSQL", driver: "MySQL", port: 3306, family: mysqlLikeTypes },
  { dbType: "opentenbase", label: "OpenTenBase", driver: "PostgreSQL", port: 5432, family: postgresLikeTypes },
];

const dialogSource = await readFile(
  new URL("../src/components/database/DatabaseConnectionDialog.vue", import.meta.url),
  "utf8",
);
const zh = await readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8");
const en = await readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8");

for (const { dbType, label, driver, port, family } of compatibleTypes) {
  test(`${label} is a built-in type on the ${driver} driver`, () => {
    assert.equal(databaseTypeLabels[dbType], label);
    assert.deepEqual(databaseDriverInfo[dbType], { label: driver, available: true });
    assert.equal(databaseDefaultPorts[dbType], port);
    assert.ok(family.has(dbType));
    assert.notDeepEqual(databaseTypeBrand(dbType), databaseTypeBrand("not-a-type"));
    const capabilities = databaseConnectionCapabilities({ dbType });
    assert.ok(capabilities.schemaDdl && capabilities.rowInsert && capabilities.tableSearch);
  });

  test(`${label} is offered by the picker and found by its name`, () => {
    assert.match(dialogSource, new RegExp(`^\\s*"${dbType}",$`, "m"));
    assert.ok(databaseTypeMatchesSearch(dbType, label));
    assert.ok(databaseTypeMatchesSearch(dbType, label.toLowerCase()));
    assert.ok(databaseTypeMatchesSearch(dbType, driver));
    for (const source of [zh, en]) {
      assert.ok(source.includes(`"database.quickConnectPlaceholder.${dbType}"`));
    }
  });

  test(`${label} connection URLs keep the selected type`, () => {
    const scheme = driver === "MySQL" ? "mysql" : "postgresql";
    const parsed = parseDatabaseConnectionUrl(`${scheme}://u:p@db.test/app`, dbType);
    assert.equal(parsed?.dbType, dbType);
    assert.equal(parsed?.port, port);
    assert.equal(parseDatabaseConnectionUrl(`${dbType}://u:p@db.test/app`)?.dbType, dbType);
  });
}

test("the picker groups Dolt with relational and the domestic forks with domestic databases", () => {
  const category = (id) => {
    const start = dialogSource.indexOf(`id: "${id}"`);
    return dialogSource.slice(start, dialogSource.indexOf("]", start));
  };
  assert.match(category("rel"), /"dolt"/);
  assert.match(category("domestic"), /"greatsql"/);
  assert.match(category("domestic"), /"opentenbase"/);
});

test("custom MySQL / PostgreSQL searches find the generic entries", () => {
  for (const query of ["Custom MySQL", "custom-mysql", "自定义 MySQL", "自定义mysql", "MySQL 兼容"]) {
    assert.ok(databaseTypeMatchesSearch("mysql", query), query);
    assert.equal(databaseTypeMatchesSearch("postgres", query), false, query);
  }
  for (const query of ["Custom PostgreSQL", "自定义 PostgreSQL", "postgresql compatible"]) {
    assert.ok(databaseTypeMatchesSearch("postgres", query), query);
    assert.equal(databaseTypeMatchesSearch("mysql", query), false, query);
  }
  assert.ok(databaseTypeMatchesSearch("mysql", "  "));
  assert.equal(databaseTypeMatchesSearch("dolt", "postgres"), false);
});

test("MySQL and PostgreSQL cards carry the compatible-database hint in both locales", () => {
  for (const key of ["database.typeCompatHint.mysql", "database.typeCompatHint.postgres"]) {
    assert.ok(dialogSource.includes(`"${key}"`), key);
    assert.ok(zh.includes(`"${key}"`) && en.includes(`"${key}"`), key);
  }
  assert.match(zh, /"database\.typeCompatHint\.mysql": "[^"]*Dolt[^"]*GreatSQL/);
  assert.match(en, /"database\.typeCompatHint\.mysql": "[^"]*Dolt[^"]*GreatSQL/);
});
