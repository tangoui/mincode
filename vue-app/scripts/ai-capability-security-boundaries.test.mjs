import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

async function toolSource(name) {
  return readFile(new URL(`../src/services/ai-tools/${name}`, import.meta.url), "utf8");
}

function capabilityBlock(source, name) {
  const marker = new RegExp(`(?:tool\\(\\s*"${name}"|name:\\s*"${name}")`);
  const start = source.search(marker);
  assert.ok(start >= 0, `missing ${name}`);
  const nextStart = source.slice(start + 1).search(/(?:tool\(\s*"|name:\s*")/);
  const next = nextStart < 0 ? -1 : start + 1 + nextStart;
  return source.slice(start, next < 0 ? source.length : next);
}

function assertWriteCapability(source, name) {
  const block = capabilityBlock(source, name);
  assert.match(block, /mode:\s*"write"/);
  assert.match(block, /risk:\s*"write"/);
  assert.match(block, /approvalPreview/);
}

test("AI capabilities that change workspace state require write approval", async () => {
  const [database, redis, http] = await Promise.all([
    toolSource("database-tools.ts"),
    toolSource("redis-tools.ts"),
    toolSource("http-tools.ts"),
  ]);

  for (const name of [
    "database.new_sql_tab",
    "database.open_connection_dialog",
    "database.edit_connection_dialog",
  ]) assertWriteCapability(database, name);
  assertWriteCapability(redis, "redis.format_open_json");
  for (const name of [
    "http.new_request_tab",
    "http.select_environment",
    "http.open_saved_request",
    "http.open_history_item",
  ]) assertWriteCapability(http, name);
});

test("HTTP environment capabilities declare every handler-required identifier", async () => {
  const http = await toolSource("http-tools.ts");
  for (const name of [
    "http.create_environment",
    "http.select_environment",
    "http.delete_environment",
  ]) {
    const block = capabilityBlock(http, name);
    assert.match(block, /collectionId:\s*\{\s*type:\s*"string"\s*\}/);
    assert.match(block, /\["collectionId", "(?:name|environmentId)"\]/);
    assert.match(block, /collectionId:\s*requiredString\(/);
  }
});

test("terminal and SSH tool results are sanitized before reaching AI", async () => {
  const [terminal, ssh] = await Promise.all([
    toolSource("terminal-tools.ts"),
    toolSource("ssh-tools.ts"),
  ]);
  assert.match(capabilityBlock(terminal, "terminal.run_command"), /sanitizeAiResult\(await dependencies\.invoke/);
  for (const name of [
    "ssh.list_known_hosts",
    "ssh.discover_config",
    "ssh.server_snapshot",
    "ssh.service_logs",
    "ssh.container_logs",
  ]) {
    assert.match(capabilityBlock(ssh, name), /sanitizeSshResult\(await dependencies\.invoke/);
  }
  assert.match(ssh, /function sanitizeSshResult[\s\S]*?"identityFile"/);
  assert.match(ssh, /\? \[key, "\[REDACTED\]"\]/);
  assert.doesNotMatch(capabilityBlock(ssh, "ssh.discover_config"), /properties:\s*\{\s*path:/);
});
