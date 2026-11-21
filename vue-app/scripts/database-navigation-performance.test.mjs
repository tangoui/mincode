import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const storeSource = await readFile(
  new URL("../src/stores/database-workspace.ts", import.meta.url),
  "utf8",
);
const schemaSource = await readFile(
  new URL("../../backend/db/schema.go", import.meta.url),
  "utf8",
);
const dbSource = await readFile(
  new URL("../../backend/db/db.go", import.meta.url),
  "utf8",
);
const redisSource = await readFile(
  new URL("../../backend/db/redis.go", import.meta.url),
  "utf8",
);
const redisPanelSource = await readFile(
  new URL("../src/components/database/RedisWorkspacePanel.vue", import.meta.url),
  "utf8",
);
const tableOpsSource = await readFile(
  new URL("../../backend/db/table_ops.go", import.meta.url),
  "utf8",
);
const capabilitiesSource = await readFile(
  new URL("../src/domain/database/db-type-capabilities.ts", import.meta.url),
  "utf8",
);
const workspaceViewSource = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8",
);

test("MySQL navigation schema avoids expensive table statistics", () => {
  const mysqlNavigationSchema = schemaSource.match(
    /func mysqlNavigationSchema\([\s\S]*?\n}\n\nfunc mysqlDatabaseTables/,
  );
  assert.ok(mysqlNavigationSchema, "MySQL navigation schema implementation should be present");
  assert.doesNotMatch(mysqlNavigationSchema[0], /TABLE_ROWS|DATA_LENGTH|INDEX_LENGTH|CREATE_TIME|UPDATE_TIME|TABLE_COMMENT/);
});

test("database connection publishes the session before schema inspection completes", () => {
  const connectBody = storeSource.match(/async function connect\(id: string\) \{[\s\S]*?\n  \}\n\n  async function disconnectFromScope/);
  assert.ok(connectBody, "database connect implementation should be present");
  const source = connectBody[0];
  const sessionAssignment = source.indexOf("runtime.sessionIds =");
  const schemaInspection = source.indexOf("loadSchemaForConnection(scopeId, id, sessionId)");

  assert.ok(sessionAssignment >= 0, "connection should publish its session");
  assert.ok(schemaInspection >= 0, "connection should still load schema");
  assert.ok(
    sessionAssignment < schemaInspection,
    "session state must be published before the potentially slow schema request",
  );
});

test("database schema refresh uses the lightweight navigation contract", () => {
  assert.match(
    storeSource,
    /async function loadSchemaForConnection\(scopeId: string, connectionId: string, sessionId: string\)/,
  );
  assert.match(storeSource, /"db_get_navigation_schema"/);
  assert.match(
    storeSource,
    /loadSchemaForConnection\(scopeId, id, sessionId\)/,
  );
});

test("MySQL schema lists databases without eagerly loading every table", () => {
  const mysqlNavigationSchema = schemaSource.match(
    /func mysqlNavigationSchema\([\s\S]*?\n}\n\nfunc mysqlDatabaseTables/,
  );
  assert.ok(mysqlNavigationSchema, "MySQL navigation schema implementation should be present");
  assert.match(mysqlNavigationSchema[0], /SCHEMA_NAME/);
  assert.doesNotMatch(mysqlNavigationSchema[0], /information_schema\.TABLES/);
  assert.match(dbSource, /db_get_database_tables/);
  assert.match(dbSource, /db_get_navigation_schema/);
  assert.match(dbSource, /info, err := getSchema\(ctx, s\)/);
});

test("Redis navigation lists databases without scanning keys, then loads panel keys after schema", () => {
  const navigation = redisSource.match(/func redisNavigationSchema\([\s\S]*?\n}\n\n\/\/ redisSchema/);
  assert.ok(navigation, "Redis lightweight navigation should be present");
  assert.match(navigation[0], /redisSchemaDBIndexes/);
  assert.doesNotMatch(navigation[0], /redisSchemaScanKeys|redisSchemaKeyTypes/);
  assert.match(schemaSource, /s\.kind == kindRedis[\s\S]*?redisNavigationSchema/);
  assert.match(redisPanelSource, /store\.activeSchema[\s\S]*?void refresh\(true\)/);
});

test("database sidebar renders large table lists in bounded batches", async () => {
  const sidebarSource = await readFile(
    new URL("../src/components/database/sidebar/DatabaseConnectionSidebar.vue", import.meta.url),
    "utf8",
  );
  assert.match(sidebarSource, /SIDEBAR_TABLE_BATCH_SIZE = 40/);
  assert.match(sidebarSource, /filtered\.slice\(0, visibleCount\)/);
  assert.match(sidebarSource, /v-for="table in visibleTablesFor\(connection, database\)"/);
  assert.match(sidebarSource, /remainingTableCount\(connection, database\) > 0/);
});

test("database expansion loads tables only for the selected database", () => {
  assert.match(storeSource, /async function loadTablesForDatabase\(/);
  assert.match(storeSource, /db_get_database_tables/);
  const expansionBody = workspaceViewSource.match(
    /async function toggleDatabaseExpansion\([\s\S]*?\n}\n\nasync function selectConnectionTable/,
  );
  assert.ok(expansionBody, "database expansion implementation should be present");
  assert.match(expansionBody[0], /loadTablesForDatabase/);
  assert.match(expansionBody[0], /(?:loadsTablesPerDatabase\(connection(?:, connectionDescriptor\(connection\))?\)|capabilitiesOf\(connection\)\.loadsTablesPerDatabase)/);
});

test("MySQL table rows do not perform a redundant database switch", () => {
  const rowsBody = tableOpsSource.match(
    /func getTableRowsPage\([\s\S]*?\n}\n\n\/\/ resolvePostgresTableTarget/,
  );
  assert.ok(rowsBody, "table rows implementation should be present");
  assert.doesNotMatch(rowsBody[0], /s\.dialect == "mysql"[\s\S]*?runQuery\(ctx, s, sql, &db\)/);
  assert.doesNotMatch(rowsBody[0], /runQuery\(ctx, s, sql, &db\)/);
});

test("database overviews load per-database object statistics for every supported engine", () => {
  const overviewBody = workspaceViewSource.match(
    /function openDatabaseOverview\([\s\S]*?\n}\n\nfunction switchOverviewDatabase/,
  );
  assert.ok(overviewBody, "database overview implementation should be present");
  assert.match(overviewBody[0], /(?:loadsTablesPerDatabase\(connection(?:, connectionDescriptor\(connection\))?\)|capabilitiesOf\(connection\)\.loadsTablesPerDatabase)/);
  assert.match(overviewBody[0], /loadTablesForDatabase/);
  const loadTypes = capabilitiesSource.match(/perDatabaseTableLoadTypes = new Set<DbType>\(\[([\s\S]*?)\]\)/);
  assert.ok(loadTypes, "per-database load types should be declared");
  // MySQL-protocol engines share a navigation schema without tables, so each must load per database.
  for (const dbType of ["mysql", "mariadb", "tidb", "oceanbase", "doris", "starrocks", "sqlserver", "oracle", "dameng", "clickhouse", "duckdb", "sqlite", "mongodb"]) {
    assert.match(loadTypes[1], new RegExp(`"${dbType}"`), `${dbType} should load objects per database`);
  }
  assert.match(loadTypes[1], /\.\.\.postgresLikeTypes/);
  assert.match(schemaSource, /s\.kind == kindMongo \{\n\t\treturn mongoDatabaseTables/);
  assert.match(schemaSource, /s\.dialect == "sqlite" \{\n\t\treturn sqliteDatabaseTables/);
  assert.match(schemaSource, /supportsTableStats\(s\.dialect\) \{\n\t\treturn sqlDatabaseTablesWithStats/);
});

test("schema refresh invalidates stale per-database table requests", () => {
  const refreshBody = storeSource.match(
    /async function refreshSchema\(\) \{[\s\S]*?\n  }\n\n  function schemaSyncArgs/,
  );
  assert.ok(refreshBody, "schema refresh implementation should be present");
  assert.match(refreshBody[0], /invalidateDatabaseTableLoads\(sessionId\)/);
  assert.match(storeSource, /databaseTableLoadGenerations/);
});

test("schema refresh reloads the tables that were showing and the open database", () => {
  const refreshBody = storeSource.match(
    /async function refreshSchema\(\) \{[\s\S]*?\n  }\n\n  function schemaSyncArgs/,
  )?.[0];
  assert.ok(refreshBody, "schema refresh implementation should be present");
  const collect = refreshBody.indexOf("[...loadedDatabaseTables]");
  const invalidate = refreshBody.indexOf("invalidateDatabaseTableLoads(sessionId)");
  const reload = refreshBody.indexOf("loadTablesForDatabase(connectionId, database)");
  assert.ok(collect > 0 && collect < invalidate, "loaded databases are recorded before the markers are cleared");
  assert.ok(reload > invalidate, "tables are fetched again after the navigation schema reloads");
  assert.match(refreshBody, /(?:loadsTablesPerDatabase\(connection(?:, connectionDescriptor\(connection\))?\)|capabilitiesOf\(connection\)\.loadsTablesPerDatabase) && runtime\.selectedDatabase\) \{\s*reloadDatabases\.add\(runtime\.selectedDatabase\)/);
  assert.match(refreshBody, /\.filter\(\(database\) => available\.has\(database\)\)/, "dropped databases are not requested");
});

test("selecting an already loaded table avoids another first-page request", () => {
  const selectBody = workspaceViewSource.match(
    /async function selectTable\([\s\S]*?\n}\n\nasync function testConnection/,
  );
  assert.ok(selectBody, "table selection implementation should be present");
  assert.match(selectBody[0], /databaseStore\.selectedDatabase === database/);
  assert.match(selectBody[0], /databaseStore\.selectedTable === table/);
  assert.match(selectBody[0], /if \(isCurrentTable\) return/);
  assert.match(selectBody[0], /loadingTableIds/);
});

test("server-side search activates table context without loading an unfiltered page", () => {
  const selectBody = workspaceViewSource.match(
    /async function selectTable\([\s\S]*?\n}\n\nasync function testConnection/,
  );
  assert.ok(selectBody, "table selection implementation should be present");
  assert.match(selectBody[0], /activateTableContext/);
  assert.match(storeSource, /function activateTableContext\(/);
  assert.match(selectBody[0], /if \(search\) \{[\s\S]*?await loadTableSearchPage/);
  assert.doesNotMatch(
    selectBody[0],
    /if \(search\)[\s\S]*?await openTablePage\([\s\S]*?await loadTableSearchPage/,
  );
});

test("table rows render without waiting for column and primary-key metadata", () => {
  const openTableBody = storeSource.match(
    /async function openTableForScope\([\s\S]*?\n  \}\n\n  async function openTable\(/,
  );
  assert.ok(openTableBody, "table opening implementation should be present");
  const source = openTableBody[0];

  assert.doesNotMatch(source, /await Promise\.all\(\[\s*invokeBackend<DatabaseQueryResult>/);
  assert.match(source, /void loadTableMetadata\(sessionId, database, table\)/);
  assert.match(source, /const tableResult = await invokeBackend<DatabaseQueryResult>/);
  assert.doesNotMatch(source, /await loadTableMetadata\(sessionId, database, table\)/);
});

test("tables loaded before the navigation schema arrives are kept and merged in", () => {
  const loadBody = storeSource.match(/async function loadTablesForDatabase\([\s\S]*?\n  }\n/)?.[0];
  assert.ok(loadBody, "per-database table loading should be present");
  assert.doesNotMatch(loadBody, /if \(schema && isCurrent\)/, "a missing schema must not drop fetched tables");
  assert.match(loadBody, /fetchedDatabaseTables\.set\(key, tables \?\? \[\]\)/);
  const schemaBody = storeSource.match(/async function loadSchemaForConnection\([\s\S]*?\n  }\n/)?.[0];
  assert.ok(schemaBody, "navigation schema loading should be present");
  assert.match(schemaBody, /withFetchedTables\(schema, sessionId\)/, "arriving navigation schema merges already fetched tables");
  assert.match(schemaBody, /(?:loadsTablesPerDatabase\(connection(?:, connectionDescriptor\(connection\))?\)|capabilitiesOf\(connection\)\.loadsTablesPerDatabase)[\s\S]*?loadTablesForDatabase\(connectionId, openDatabase\)/, "the database opened by the schema gets its tables loaded");
  assert.match(storeSource, /fetchedDatabaseTables\.delete\(key\)/, "invalidation clears fetched tables");
});
