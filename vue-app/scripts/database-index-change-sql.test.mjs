import assert from "node:assert/strict";
import test from "node:test";
import {
  buildCreateIndexStatements,
  buildDropIndexStatements,
  buildEditIndexStatements,
  formatIndexChangeSql,
  supportsDatabaseIndexMutations,
} from "../src/domain/database/index-change-sql.ts";

const index = { name: "idx_users_name", columns: ["name", "tenant_id"], unique: true };

test("uses one ALTER TABLE statement to edit MySQL-family indexes", () => {
  assert.deepEqual(
    buildEditIndexStatements(
      { dbType: "mysql", database: "app-db", table: "users" },
      "idx_old",
      index
    ),
    [
      "ALTER TABLE `app-db`.`users` DROP INDEX `idx_old`, ADD UNIQUE INDEX `idx_users_name` (`name`, `tenant_id`)",
    ]
  );
});

test("uses schema-qualified drop/create statements for PostgreSQL index edits", () => {
  assert.deepEqual(
    buildEditIndexStatements(
      { dbType: "postgres", database: "public", table: "users" },
      "idx_old",
      index
    ),
    [
      'DROP INDEX "public"."idx_old"',
      'CREATE UNIQUE INDEX "idx_users_name" ON "public"."users" ("name", "tenant_id")',
    ]
  );
});

test("keeps SQLite table and index names unqualified", () => {
  const target = { dbType: "sqlite", database: "/tmp/app.db", table: "users" };
  assert.deepEqual(buildCreateIndexStatements(target, index), [
    'CREATE UNIQUE INDEX "idx_users_name" ON "users" ("name", "tenant_id")',
  ]);
  assert.deepEqual(buildDropIndexStatements(target, index.name), [
    'DROP INDEX "idx_users_name"',
  ]);
});

test("uses SQL Server DROP_EXISTING when the index name is unchanged", () => {
  assert.deepEqual(
    buildEditIndexStatements(
      { dbType: "sqlserver", database: "dbo", table: "odd]users" },
      index.name,
      index
    ),
    [
      "CREATE UNIQUE INDEX [idx_users_name] ON [dbo].[odd]]users] ([name], [tenant_id]) WITH (DROP_EXISTING = ON)",
    ]
  );
});

test("uses owner-qualified Oracle-compatible index names", () => {
  const target = { dbType: "oracle", database: "APP", table: "USERS" };
  assert.deepEqual(buildCreateIndexStatements(target, index), [
    'CREATE UNIQUE INDEX "APP"."idx_users_name" ON "APP"."USERS" ("name", "tenant_id")',
  ]);
  assert.deepEqual(buildDropIndexStatements(target, index.name), [
    'DROP INDEX "APP"."idx_users_name"',
  ]);
});

test("formats generated statement plans for review and blocks unverified dialects", () => {
  assert.equal(formatIndexChangeSql(["DROP INDEX old;", "CREATE INDEX next"]), "DROP INDEX old;\nCREATE INDEX next;");
  assert.equal(supportsDatabaseIndexMutations("firebird"), true);
  assert.equal(supportsDatabaseIndexMutations("doris"), false);
});
