import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  cleanDatabaseAiQueryOutput,
  databaseAiQueryGuide,
} from "../src/domain/database/ai-query.ts";

const service = await readFile(
  new URL("../src/services/ai-database-query.ts", import.meta.url),
  "utf8",
);
const workspace = await readFile(
  new URL("../src/components/database/query/DatabaseQueryWorkspace.vue", import.meta.url),
  "utf8",
);
const dialog = await readFile(
  new URL("../src/components/database/query/DatabaseAiQueryDialog.vue", import.meta.url),
  "utf8",
);
const view = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8",
);

test("AI query guides cover every executable query protocol", () => {
  assert.equal(databaseAiQueryGuide("mongodb").family, "mongodb");
  assert.match(databaseAiQueryGuide("mongodb").instruction, /"collection"/);
  assert.equal(databaseAiQueryGuide("elasticsearch").family, "elasticsearch");
  assert.equal(databaseAiQueryGuide("opensearch").family, "elasticsearch");
  assert.match(databaseAiQueryGuide("opensearch").instruction, /OpenSearch/);
  assert.equal(databaseAiQueryGuide("couchdb").family, "couchdb");
  assert.equal(databaseAiQueryGuide("influxdb").family, "influxdb");
  assert.match(databaseAiQueryGuide("influxdb").instruction, /Flux/);
  assert.equal(databaseAiQueryGuide("neo4j").family, "neo4j");
  assert.match(databaseAiQueryGuide("neo4j").instruction, /Cypher/);
  assert.equal(databaseAiQueryGuide("cassandra").family, "cassandra");
  assert.match(databaseAiQueryGuide("cassandra").instruction, /CQL/);
  assert.equal(databaseAiQueryGuide("redis").family, "redis");
  assert.equal(databaseAiQueryGuide("sqlserver").family, "sql-sqlserver");
  assert.match(databaseAiQueryGuide("sqlserver").instruction, /TOP \(100\)/);
  assert.equal(databaseAiQueryGuide("oracle").family, "sql-oracle");
  assert.match(databaseAiQueryGuide("oracle").instruction, /FETCH FIRST 100 ROWS ONLY/);
  assert.equal(databaseAiQueryGuide("firebird").family, "sql-firebird");
  assert.match(databaseAiQueryGuide("firebird").instruction, /ROWS 1 TO 100/);
});

test("AI query guides map SQL dialect and vector families", () => {
  assert.equal(databaseAiQueryGuide("mysql").family, "sql-mysql");
  assert.equal(databaseAiQueryGuide("clickhouse").family, "sql-mysql");
  assert.match(databaseAiQueryGuide("mysql").instruction, /反引号/);
  assert.equal(databaseAiQueryGuide("postgres").family, "sql-standard");
  assert.equal(databaseAiQueryGuide("sqlite").family, "sql-standard");
  for (const vectorType of ["pgvector", "qdrant", "milvus", "weaviate", "pinecone", "chroma"]) {
    assert.equal(databaseAiQueryGuide(vectorType).family, "vector", vectorType);
  }
  assert.match(databaseAiQueryGuide("pgvector").instruction, /vectorColumn/);
  assert.match(databaseAiQueryGuide("milvus").instruction, /outputFields/);
  assert.match(databaseAiQueryGuide("pinecone").instruction, /namespace/);
  assert.match(databaseAiQueryGuide("qdrant").instruction, /"filter": \{\}/);
});

test("every guide forbids explanations and markdown", () => {
  for (const dbType of ["mysql", "mongodb", "redis", "influxdb", "pgvector", "sqlserver"]) {
    assert.match(databaseAiQueryGuide(dbType).instruction, /只输出查询本身/, dbType);
  }
});

test("AI query output cleaning strips markdown fences", () => {
  assert.equal(cleanDatabaseAiQueryOutput("  SELECT 1;  "), "SELECT 1;");
  assert.equal(cleanDatabaseAiQueryOutput("```sql\nSELECT 1;\n```"), "SELECT 1;");
  assert.equal(
    cleanDatabaseAiQueryOutput('```json\n{"filter": {}}\n```'),
    '{"filter": {}}',
  );
  assert.equal(cleanDatabaseAiQueryOutput('{"filter": {}}'), '{"filter": {}}');
});

test("AI query service uses a short tool-less stream with cancellation", () => {
  assert.match(service, /streamAiTurnWithFailover\(\{/);
  assert.match(service, /tools: \[\]/);
  assert.match(service, /reasoningEffort: undefined/);
  assert.match(service, /DATABASE_AI_QUERY_MAX_OUTPUT_TOKENS = 2048/);
  assert.match(service, /signal: options\.signal/);
  assert.match(service, /options\.onDelta\?\.\(event\.text\)/);
  assert.match(service, /cleanDatabaseAiQueryOutput\(generated\)/);
});

test("AI query prompt carries bounded schema context", () => {
  assert.match(service, /DATABASE_AI_QUERY_MAX_COLUMNS = 60/);
  assert.match(service, /DATABASE_AI_QUERY_MAX_TABLES = 50/);
  assert.match(service, /数据库类型：\$\{context\.dbType\}/);
  assert.match(service, /当前表\/集合：/);
  assert.match(service, /字段：/);
  assert.match(service, /同库其他表\/集合：/);
});

test("query workspace exposes the AI generate action and the view applies results", () => {
  assert.match(workspace, /\(e: "ai-generate"\)/);
  assert.match(workspace, /database\.aiGenerateQuery/);
  assert.match(view, /@ai-generate="openAiQueryDialog"/);
  assert.match(view, /import DatabaseAiQueryDialog/);
  assert.match(view, /tab\.sql = trimmed;/);
  assert.match(view, /database\.aiQueryApplyConfirm/);
  assert.match(view, /:db-type="(?:activeConnection\.dbType|activeDialectType \?\? activeConnection\.dbType)"/);
});

test("AI query dialog streams, aborts, validates auth and applies", () => {
  assert.match(dialog, /generateAiDatabaseQuery\(\{/);
  assert.match(dialog, /controller\?\.abort\(\)/);
  assert.match(dialog, /emit\("apply", query\)/);
  assert.match(dialog, /loadTableMetadata/);
  assert.match(dialog, /validateAiProviderAuth/);
  assert.match(dialog, /hydrateSettingsSnapshot/);
  assert.match(dialog, /AI_QUERY_GENERATION_TIMEOUT_MS = 90_000/);
});
