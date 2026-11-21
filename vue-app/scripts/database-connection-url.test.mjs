import assert from "node:assert/strict";
import test from "node:test";
import { parseDatabaseConnectionUrl } from "../src/domain/database/connection-url.ts";

test("parses Elasticsearch HTTP basic-auth URLs for the selected database type", () => {
  assert.deepEqual(
    parseDatabaseConnectionUrl("http://elastic:secret@10.199.1.77:9200", "elasticsearch"),
    {
      dbType: "elasticsearch",
      host: "10.199.1.77",
      port: 9200,
      username: "elastic",
      password: "secret",
      database: "",
      token: "",
      org: "",
      authSource: "",
      ssl: false,
    }
  );
});

test("parses Elasticsearch API keys and marks HTTPS connections as SSL", () => {
  const parsed = parseDatabaseConnectionUrl(
    "https://search.example.com:9243?apiKey=encoded-api-key",
    "elasticsearch"
  );

  assert.equal(parsed?.dbType, "elasticsearch");
  assert.equal(parsed?.host, "search.example.com");
  assert.equal(parsed?.port, 9243);
  assert.equal(parsed?.token, "encoded-api-key");
  assert.equal(parsed?.ssl, true);
});

test("keeps the selected compatible database type for shared wire protocols", () => {
  const parsed = parseDatabaseConnectionUrl(
    "postgresql://root:secret@db.example.com:26257/app",
    "cockroachdb"
  );

  assert.equal(parsed?.dbType, "cockroachdb");
  assert.equal(parsed?.database, "app");
});

test("parses InfluxDB bucket, organization, and token from an HTTP URL", () => {
  const parsed = parseDatabaseConnectionUrl(
    "http://influx.example.com:8086/metrics?org=operations&token=token-value",
    "influxdb"
  );

  assert.equal(parsed?.dbType, "influxdb");
  assert.equal(parsed?.database, "metrics");
  assert.equal(parsed?.org, "operations");
  assert.equal(parsed?.token, "token-value");
});

test("does not guess a database type from a bare HTTP URL", () => {
  assert.equal(parseDatabaseConnectionUrl("http://localhost:9200"), undefined);
});

test("keeps Redshift selected for PostgreSQL URLs and reads SSL mode", () => {
  const parsed = parseDatabaseConnectionUrl(
    "postgresql://user:secret@cluster.example.com:5439/analytics?sslmode=require",
    "redshift"
  );

  assert.equal(parsed?.dbType, "redshift");
  assert.equal(parsed?.database, "analytics");
  assert.equal(parsed?.ssl, true);
});

test("parses Databricks warehouse, catalog, schema, and access token", () => {
  const parsed = parseDatabaseConnectionUrl(
    "https://workspace.example.com/warehouse-id?token=access-token&catalog=main&schema=default",
    "databricks"
  );

  assert.equal(parsed?.dbType, "databricks");
  assert.equal(parsed?.database, "warehouse-id");
  assert.equal(parsed?.org, "main");
  assert.equal(parsed?.authSource, "default");
  assert.equal(parsed?.token, "access-token");
  assert.equal(parsed?.ssl, true);
});

test("parses BigQuery project and location", () => {
  const parsed = parseDatabaseConnectionUrl(
    "https://bigquery.googleapis.com/sample-project?token=oauth-token&location=us-central1",
    "bigquery"
  );

  assert.equal(parsed?.dbType, "bigquery");
  assert.equal(parsed?.database, "sample-project");
  assert.equal(parsed?.org, "us-central1");
  assert.equal(parsed?.token, "oauth-token");
});

test("parses Snowflake database, warehouse, schema, and bearer token", () => {
  const parsed = parseDatabaseConnectionUrl(
    "https://account.snowflakecomputing.com/analytics?token=jwt-token&warehouse=compute_wh&schema=public",
    "snowflake"
  );

  assert.equal(parsed?.dbType, "snowflake");
  assert.equal(parsed?.database, "analytics");
  assert.equal(parsed?.org, "compute_wh");
  assert.equal(parsed?.authSource, "public");
  assert.equal(parsed?.token, "jwt-token");
  assert.equal(parsed?.ssl, true);
});
