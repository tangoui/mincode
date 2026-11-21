import assert from "node:assert/strict";
import test from "node:test";
import {
  truncateUtf8,
  utf8ByteLength,
} from "../src/domain/persistence/byte-budget.ts";
import {
  HTTP_HISTORY_MAX_ITEMS,
  HTTP_HISTORY_MAX_TOTAL_BYTES,
  normalizeHttpHistoryForPersistence,
} from "../src/domain/http/persistence-budget.ts";
import {
  SSH_BATCH_HISTORY_MAX_TOTAL_BYTES,
  SSH_BATCH_OUTPUT_TRUNCATED_SUFFIX,
  normalizeSshBatchHistoryForPersistence,
} from "../src/domain/ssh/batch-history-budget.ts";

function jsonBytes(value) {
  return utf8ByteLength(JSON.stringify(value));
}

test("UTF-8 truncation preserves Unicode code points and byte limit", () => {
  const value = "中文🙂abcdef";
  const result = truncateUtf8(value, 13, "…");
  assert.ok(utf8ByteLength(result) <= 13);
  assert.doesNotMatch(result, /\uFFFD/);
  assert.ok(result.endsWith("…"));
});

test("HTTP history enforces item count, URL and total JSON budgets", () => {
  const items = Array.from({ length: 250 }, (_, index) => ({
    id: `history-${index}`,
    method: "GET",
    url: `https://example.test/${"中".repeat(10_000)}/${index}`,
    status: 200,
    elapsed: 12,
    createdAt: Date.now() - index,
  }));
  const normalized = normalizeHttpHistoryForPersistence(items);
  assert.ok(normalized.length <= HTTP_HISTORY_MAX_ITEMS);
  assert.ok(jsonBytes(normalized) <= HTTP_HISTORY_MAX_TOTAL_BYTES);
  assert.match(normalized[0].url, /URL truncated/);
  assert.equal(items[0].url.includes("URL truncated"), false);
});

test("SSH batch persistence is immutable and respects output plus total budgets", () => {
  const output = "输🙂".repeat(80_000);
  const entries = Array.from({ length: 20 }, (_, index) => ({
    id: String(index),
    command: `echo ${index}`,
    serverHosts: [`root@host-${index}`],
    timestamp: Date.now() - index,
    results: [{
      id: `server-${index}`,
      host: `root@host-${index}`,
      stdout: output,
      stderr: output,
      exitCode: 0,
      durationMs: 10,
    }],
  }));
  const normalized = normalizeSshBatchHistoryForPersistence(entries);
  assert.ok(jsonBytes(normalized) <= SSH_BATCH_HISTORY_MAX_TOTAL_BYTES);
  assert.ok(normalized.length < entries.length);
  assert.match(normalized[0].results[0].stdout, /persisted output truncated/);
  assert.match(normalized[0].results[0].stderr, /persisted output truncated/);
  assert.equal(entries[0].results[0].stdout, output);
  assert.equal(entries[0].results[0].stderr, output);
  assert.equal(entries[0].results[0].stdout.endsWith(SSH_BATCH_OUTPUT_TRUNCATED_SUFFIX), false);
});
