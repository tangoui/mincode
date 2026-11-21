import assert from "node:assert/strict";
import test from "node:test";
import { indexesWithPrimaryKey, resolvePrimaryKeyColumns } from "../src/domain/database/table-index.ts";

test("shows a primary key as an index when index introspection omits it", () => {
  assert.deepEqual(indexesWithPrimaryKey([], ["tenant_id"]), [
    { name: "PRIMARY", columns: ["tenant_id"], unique: true, primary: true },
  ]);
});

test("uses the database constraint name and keeps secondary indexes", () => {
  assert.deepEqual(
    indexesWithPrimaryKey(
      [{ name: "orders_status_idx", columns: ["status"], unique: false }],
      ["id"],
      "orders_pkey"
    ),
    [
      { name: "orders_pkey", columns: ["id"], unique: true, primary: true },
      { name: "orders_status_idx", columns: ["status"], unique: false, primary: false },
    ]
  );
});

test("does not duplicate a primary index returned by a database driver", () => {
  const indexes = [{ name: "PRIMARY", columns: ["tenant_id", "id"], unique: true }];

  assert.deepEqual(indexesWithPrimaryKey(indexes, ["tenant_id", "id"]), [
    { ...indexes[0], primary: true },
  ]);
});


test("uses primary-key flags from column metadata when the dedicated key query is empty", () => {
  const columns = [
    { name: "local_date", dataType: "date", nullable: false, isPrimaryKey: true, autoIncrement: false },
    { name: "log_type", dataType: "varchar(32)", nullable: false, isPrimaryKey: true, autoIncrement: false },
    { name: "app_name", dataType: "varchar(255)", nullable: false, isPrimaryKey: true, autoIncrement: false },
    { name: "execution_id", dataType: "varchar(100)", nullable: false, isPrimaryKey: true, autoIncrement: false },
    { name: "created_at", dataType: "datetime", nullable: false, isPrimaryKey: false, autoIncrement: false },
  ];

  const primaryKeys = resolvePrimaryKeyColumns([], columns);
  assert.deepEqual(primaryKeys, ["local_date", "log_type", "app_name", "execution_id"]);
  assert.deepEqual(indexesWithPrimaryKey([], primaryKeys), [
    {
      name: "PRIMARY",
      columns: ["local_date", "log_type", "app_name", "execution_id"],
      unique: true,
      primary: true,
    },
  ]);
});
