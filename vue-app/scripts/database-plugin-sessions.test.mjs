// Phase E T4b：插件数据库驱动在前端运行时的行为（描述符、能力门控、可移植性、AI 审批标注）。
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  builtinDriverDescriptor,
  connectionDialectType,
  dialectProjection,
  dialectTypeForSqlDialect,
  pluginDriverLabelParts,
  resolveConnectionDriver,
} from "../src/domain/database/driver-descriptor.ts";
import {
  databaseConnectionCapabilities,
  loadsTablesPerDatabase,
  supportsSchemaDdl,
  supportsTableDataTransfer,
} from "../src/domain/database/db-type-capabilities.ts";
import { tableStructureCapabilities } from "../src/domain/database/table-structure-capabilities.ts";
import {
  connectionConfigHasSecrets,
  parseConnectionConfig,
  portableConnection,
  portableDriverOptions,
} from "../src/domain/database/connection-portability.ts";
import { restoreDatabaseConnection } from "../src/domain/workspace/environment-pack.ts";
import {
  databaseCapabilities,
  labelPluginDriverApprovals,
} from "../src/services/ai-tools/database-tools.ts";

const source = (path) => readFile(new URL(`../src/${path}`, import.meta.url), "utf8");

const driverRef = { pluginId: "com.acme.db", driverId: "acme" };

function catalogEntry(overrides = {}) {
  return {
    pluginId: "com.acme.db",
    pluginName: "Acme DB",
    driverId: "acme",
    title: "Acme",
    sqlDialect: "postgres",
    dialectType: "postgresql",
    fields: ["host", "port", "username", "password", "database"],
    defaultPort: 7777,
    features: ["metadata.tables", "metadata.columns", "tableRows", "rows.insert"],
    ...overrides,
  };
}

function connection(overrides = {}) {
  return {
    id: "conn-1",
    name: "acme",
    dbType: "plugin",
    host: "db.local",
    port: 7777,
    username: "app",
    password: "s3cret",
    ssl: false,
    tags: [],
    createdAt: 1,
    updatedAt: 1,
    driver: driverRef,
    options: { schema: "public", fetchSize: 100 },
    ...overrides,
  };
}

test("builtin connections keep the builtin descriptor and dialect", () => {
  const descriptor = resolveConnectionDriver({ dbType: "mysql" });
  assert.equal(descriptor.source, "builtin");
  assert.equal(descriptor.dialectType, "mysql");
  assert.equal(descriptor.available, true);
  assert.equal(builtinDriverDescriptor("plugin").available, false);
});

test("plugin connections resolve through the catalog and map dialects", () => {
  const descriptor = resolveConnectionDriver(connection(), [catalogEntry()]);
  assert.equal(descriptor.source, "plugin");
  assert.equal(descriptor.dialectType, "postgresql");
  assert.equal(descriptor.title, "Acme");
  assert.equal(descriptor.available, true);
  assert.deepEqual(pluginDriverLabelParts(descriptor), {
    pluginName: "Acme DB",
    pluginId: "com.acme.db",
  });
  // replaces：dbType 保持被替代的内置类型，方言随之。
  assert.equal(
    connectionDialectType(connection({ dbType: "mysql" }), [catalogEntry({ dialectType: "postgresql" })]),
    "mysql",
  );
  assert.equal(dialectTypeForSqlDialect("generic"), "plugin");
  assert.equal(dialectTypeForSqlDialect(undefined), "plugin");
  const projected = dialectProjection(connection(), descriptor);
  assert.equal(projected.dbType, "postgresql");
  assert.deepEqual(projected.driver, driverRef);
});

test("missing or disabled drivers fail closed with no builtin fallback (E-15)", () => {
  const missing = resolveConnectionDriver(connection({ dbType: "mysql" }), []);
  assert.equal(missing.source, "plugin");
  assert.equal(missing.available, false);
  assert.equal(missing.unavailableReason, "notInstalled");
  assert.equal(missing.dialectType, "mysql");

  const disabled = resolveConnectionDriver(connection(), [catalogEntry()], {
    available: false,
    reason: "disabled",
  });
  assert.equal(disabled.available, false);
  assert.equal(disabled.unavailableReason, "disabled");

  const odd = resolveConnectionDriver(connection(), [catalogEntry()], {
    available: false,
    reason: "<script>",
  });
  assert.equal(odd.unavailableReason, "error");

  // 宿主状态权威：目录未命中但宿主报告可用时不预判为不可用。
  assert.equal(resolveConnectionDriver(connection(), [], { available: true }).available, true);
});

test("capabilities follow builtin allow-lists or declared driver features", () => {
  const builtin = databaseConnectionCapabilities({ dbType: "mysql" });
  assert.equal(builtin.pluginDriver, false);
  assert.equal(builtin.dropDatabase, true);
  assert.equal(builtin.tableDataImport, true);
  assert.equal(supportsSchemaDdl({ dbType: "mysql" }), true);
  assert.equal(supportsTableDataTransfer({ dbType: "mysql" }), true);

  const descriptor = resolveConnectionDriver(connection(), [catalogEntry()]);
  const plugin = databaseConnectionCapabilities(connection(), descriptor);
  assert.equal(plugin.pluginDriver, true);
  assert.equal(plugin.dialectType, "postgresql");
  assert.equal(plugin.tableRows, true);
  assert.equal(plugin.tableDataExport, true);
  assert.equal(plugin.rowInsert, true);
  assert.equal(plugin.tableDataImport, true);
  assert.equal(plugin.rowEdit, false);
  assert.equal(plugin.rowDelete, false);
  assert.equal(plugin.schemaDdl, false);
  assert.equal(plugin.dropTable, false);
  assert.equal(plugin.clearTable, false);
  // 库级破坏性操作与结构同步需要驱动声明；字符集修改对插件会话恒为 false。
  assert.equal(plugin.clearDatabase, false);
  assert.equal(plugin.dropDatabase, false);
  assert.equal(plugin.schemaSync, false);
  assert.equal(plugin.charsetChange, false);
  assert.equal(plugin.alwaysStream, true);
  assert.equal(loadsTablesPerDatabase(connection(), descriptor), true);

  const syncFeatures = ["metadata.tables", "metadata.columns", "metadata.primaryKeys", "metadata.indexes"];
  const syncable = databaseConnectionCapabilities(
    connection(),
    resolveConnectionDriver(connection(), [catalogEntry({ dialectType: "postgres", features: syncFeatures })]),
  );
  assert.equal(syncable.schemaSync, true);
  // 结构同步还要求方言能生成 DDL：duckdb 方言即使声明了全部元数据也不行。
  const duckdbDialect = databaseConnectionCapabilities(
    connection(),
    resolveConnectionDriver(connection(), [catalogEntry({ sqlDialect: "duckdb", dialectType: "duckdb", features: syncFeatures })]),
  );
  assert.equal(duckdbDialect.schemaSync, false);
  const databaseLevel = databaseConnectionCapabilities(
    connection(),
    resolveConnectionDriver(connection(), [
      catalogEntry({ features: ["metadata.tables", "destructive.dropDatabase", "destructive.clearDatabase"] }),
    ]),
  );
  assert.equal(databaseLevel.dropDatabase, true);
  assert.equal(databaseLevel.clearDatabase, true);

  const destructive = databaseConnectionCapabilities(
    connection(),
    resolveConnectionDriver(connection(), [
      catalogEntry({ features: ["destructive.dropTable", "metadata.ddl", "rows.update", "rows.delete"] }),
    ]),
  );
  assert.equal(destructive.dropTable, true);
  assert.equal(destructive.clearTable, false);
  assert.equal(destructive.schemaDdl, true);
  assert.equal(destructive.rowEdit, true);
  assert.equal(destructive.rowDelete, true);
  assert.equal(destructive.tableDataExport, false);

  // replaces mysql 但驱动没有声明能力：不能继承内置 mysql 的能力。
  const replaced = databaseConnectionCapabilities(
    connection({ dbType: "mysql" }),
    resolveConnectionDriver(connection({ dbType: "mysql" }), [catalogEntry({ features: [] })]),
  );
  assert.equal(replaced.dropDatabase, false);
  assert.equal(replaced.tableDataImport, false);
  assert.equal(replaced.schemaDdl, false);

  // 没有描述符的插件连接失败关闭。
  const closed = databaseConnectionCapabilities(connection());
  assert.equal(closed.available, false);
  assert.equal(closed.tableRows, false);
  assert.equal(supportsTableDataTransfer(connection()), false);
});

test("table structure capabilities use declared metadata features", () => {
  const descriptor = resolveConnectionDriver(connection(), [
    catalogEntry({ features: ["metadata.indexes", "metadata.foreignKeys"] }),
  ]);
  const caps = tableStructureCapabilities("postgresql", descriptor);
  assert.equal(caps.indexes, true);
  assert.equal(caps.foreignKeys, true);
  assert.equal(caps.triggers, false);
  assert.equal(caps.ddl, false);
  const none = tableStructureCapabilities(
    "postgresql",
    resolveConnectionDriver(connection(), [catalogEntry({ features: [] })]),
  );
  assert.equal(none.indexes, false);
});

test("connection export keeps only the driver ref and non-secret options, never grants", () => {
  const exported = portableConnection(
    connection({
      grants: ["leak"],
      options: { schema: "public", nested: { a: 1 }, "Bad-Key": "x", fetchSize: 100 },
    }),
  );
  assert.deepEqual(exported.driver, driverRef);
  assert.deepEqual(exported.options, { schema: "public", fetchSize: 100 });
  assert.equal(exported.password, undefined);
  const json = JSON.stringify({ connections: [exported] });
  assert.equal(connectionConfigHasSecrets(JSON.parse(json)), false);

  const withSecrets = portableConnection(connection(), { includeSecrets: true });
  assert.equal(withSecrets.password, "s3cret");

  const builtin = portableConnection(connection({ dbType: "mysql", driver: undefined, options: { a: 1 } }));
  assert.equal(builtin.driver, undefined);
  assert.equal(builtin.options, undefined);
});

test("connection import round-trips plugin bindings and rejects broken refs", () => {
  const exported = portableConnection(connection());
  const [imported] = parseConnectionConfig({ connections: [exported] }, "invalid");
  assert.equal(imported.dbType, "plugin");
  assert.deepEqual(imported.driver, driverRef);
  assert.deepEqual(imported.options, { schema: "public", fetchSize: 100 });
  assert.equal(imported.password, undefined);
  assert.equal("grants" in imported, false);

  assert.throws(
    () => parseConnectionConfig({ connections: [{ ...exported, driver: undefined }] }, "invalid"),
    /invalid/,
  );
  assert.throws(
    () =>
      parseConnectionConfig(
        { connections: [{ ...exported, dbType: "mysql", driver: { pluginId: "../x", driverId: "y" } }] },
        "invalid",
      ),
    /invalid/,
  );
  const tooMany = Object.fromEntries(Array.from({ length: 40 }, (_, index) => [`k${index}`, index]));
  assert.equal(Object.keys(portableDriverOptions(tooMany)).length, 16);
});

test("environment pack restores plugin bindings and fails closed on bad refs", () => {
  const restored = restoreDatabaseConnection(connection());
  assert.deepEqual(restored.driver, driverRef);
  assert.deepEqual(restored.options, { schema: "public", fetchSize: 100 });
  assert.equal(restoreDatabaseConnection(connection({ driver: undefined })), null);
  assert.equal(
    restoreDatabaseConnection(connection({ dbType: "mysql", driver: { pluginId: 1 } })),
    null,
  );
  const builtin = restoreDatabaseConnection(connection({ dbType: "mysql", driver: undefined }));
  assert.equal(builtin.driver, undefined);
});

test("AI approval previews label plugin drivers without changing risk", async () => {
  const labels = { "conn-1": "Acme · Acme DB (com.acme.db)" };
  const base = databaseCapabilities({
    invoke: async () => ({}),
    databaseAction: async () => ({}),
  });
  const labelled = databaseCapabilities({
    invoke: async () => ({}),
    databaseAction: async () => ({}),
    databaseDriverLabel: (id) => labels[id],
  });
  assert.deepEqual(
    labelled.map((capability) => [capability.tool.name, capability.risk]),
    base.map((capability) => [capability.tool.name, capability.risk]),
  );
  const richInput = {
    connectionId: "conn-1",
    sourceConnectionId: "conn-1",
    targetConnectionId: "conn-2",
    sourceDatabase: "a",
    targetDatabase: "b",
    database: "db",
    table: "t",
    name: "n",
    newName: "n2",
    previousName: "g",
    nextName: "h",
    query: "select 1",
    queryId: "q1",
    fields: { id: 1 },
    keys: { id: 1 },
    data: "id\n1",
    groups: ["g"],
    group: "g",
    groupName: "g",
    fromGroup: "g",
    toGroup: "h",
    order: ["g"],
    sql: "select 1",
    path: "/tmp/x.csv",
    format: "csv",
    changeIds: ["c1"],
    rows: [{ id: 1 }],
    row: { id: 1 },
    values: { id: 1 },
    primaryKey: { id: 1 },
    where: { id: 1 },
    dimension: 3,
  };
  const nonRead = labelled.filter((capability) => capability.risk !== "read");
  assert.ok(nonRead.length > 0);
  for (const capability of nonRead) {
    const preview = await capability.approvalPreview({
      input: richInput,
      resource: { id: "database:conn-1", metadata: { connectionId: "conn-1", sessionId: "s1" } },
    });
    assert.match(preview, /via plugin driver Acme · Acme DB \(com\.acme\.db\)/, capability.tool.name);
  }
  const executeQuery = labelled.find((capability) => capability.tool.name === "database.execute_query");
  const viaResource = await executeQuery.approvalPreview({
    input: { query: "select 1" },
    resource: { id: "database:conn-1", metadata: { connectionId: "conn-1" } },
  });
  assert.match(viaResource, /via plugin driver/);
  const builtinPreview = await executeQuery.approvalPreview({
    input: { query: "select 1" },
    resource: { id: "database:conn-2", metadata: { connectionId: "conn-2" } },
  });
  assert.doesNotMatch(builtinPreview, /plugin driver/);
  // 只读工具不包装。
  for (const capability of labelled.filter((item) => item.risk === "read")) {
    const original = base.find((item) => item.tool.name === capability.tool.name);
    assert.equal(capability.approvalPreview === undefined, original.approvalPreview === undefined);
  }
  assert.equal(labelPluginDriverApprovals(base, undefined), base);
});

test("AI tools cannot bind drivers or grant credential release", async () => {
  const tools = await source("services/ai-tools/database-tools.ts");
  assert.doesNotMatch(tools, /database_driver_grant|databaseDriverGrant|ensureDatabaseDriverGrant/);
  const actions = await source("composables/database/useAiDatabaseActions.ts");
  assert.doesNotMatch(actions, /ensureDatabaseDriverGrant|grantDatabaseDriver|driver:\s*input/);
  assert.match(actions, /not available for plugin database drivers/);
  // Per-plugin trust is a user choice made in the host frame only.
  const { readdir } = await import("node:fs/promises");
  const aiToolFiles = (await readdir(new URL("../src/services/ai-tools/", import.meta.url))).filter((name) => name.endsWith(".ts"));
  assert.ok(aiToolFiles.length > 0);
  for (const text of [actions, ...(await Promise.all(aiToolFiles.map((name) => source(`services/ai-tools/${name}`))))]) {
    assert.doesNotMatch(text, /database_driver_trust|DatabaseDriverTrust|database_driver_grant/);
  }
});

test("workspace store routes plugin sessions through the host with explicit grant", async () => {
  const store = await source("stores/database-workspace.ts");
  assert.match(store, /ensureDatabaseDriverGrant/);
  assert.match(store, /connectionId: connection\.id/);
  assert.match(store, /db:sessions-closed/);
  assert.match(store, /pruneDatabaseDriverGrants/);
  assert.match(store, /sameDatabaseDriverRef\(previousDriver, normalized\.driver\)/);
});

test("cloud companion and schema sync admit plugin-driver connections only with a grant and declared features", async () => {
  const bridge = await readFile(
    new URL("../../backend/cloudcompanion/database_bridge.go", import.meta.url),
    "utf8",
  );
  // The phone sees a plugin-bound connection only after the desktop allowed credential release,
  // and a "plugin" type without a binding never falls back to a built-in driver.
  assert.match(bridge, /cloudDatabaseConnectionSupported/);
  assert.match(bridge, /driverReleaseAllowed\(driver, connection\.ID\)/);
  assert.match(bridge, /connection\.DbType != "plugin"/);
  const syncDialog = await source("components/database/DatabaseSchemaSyncDialog.vue");
  // Plugin connections join schema sync through the declared-feature capability, in their dialect.
  assert.match(syncDialog, /connection\.driver\s*\?\s*databaseStore\.connectionCapabilities\(connection\)\.schemaSync/);
  assert.match(syncDialog, /connectionCapabilities\(connection\)\.dialectType/);
});
