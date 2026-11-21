import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import {
  appendStreamLogs,
  createFrameListBuffer,
  isStreamLogTruncated,
  streamLogPreview,
} from "../src/domain/http/stream-log.ts";
import {
  formatJsonResponseBody,
  isLargeResponseBody,
  looksLikeJsonBody,
} from "../src/domain/http/response-body.ts";

const read = (path) => readFile(new URL(`../src/${path}`, import.meta.url), "utf8");
const [sshStore, app, streamPanel, httpView] = await Promise.all([
  read("stores/ssh-connections.ts"),
  read("App.vue"),
  read("components/http/HttpStreamPanel.vue"),
  read("views/HttpWorkspaceView.vue"),
]);

test("ssh profiles persist with a debounce and flush on quit", () => {
  assert.match(sshStore, /PROFILE_PERSIST_DEBOUNCE_MS = 300/);
  assert.match(sshStore, /watch\(\s*profiles,\s*\(\) => \{\s*if \(!hydrated\.value\) return;\s*schedulePersistProfiles\(\);/);
  const persist = sshStore.match(/function persistProfiles\(\) \{[\s\S]*?\n  \}/)?.[0];
  assert.ok(persist);
  assert.match(persist, /clearProfilePersistTimer\(\)/);
  assert.match(sshStore, /function flushPersistence\(\)/);
  const remove = sshStore.match(/function removeProfile\([\s\S]*?\n  \}/)?.[0];
  assert.match(remove, /persistProfiles\(\)/);
  const flush = app.match(/async function flushPersistentUiState\(\) \{[\s\S]*?\n\}/)?.[0];
  assert.match(flush, /sshConnections\.flushPersistence\(\)/);
});

test("stream buffer applies queued items once per frame", () => {
  const frames = [];
  const applied = [];
  const buffer = createFrameListBuffer(
    (items) => applied.push(items),
    (callback) => frames.push(callback),
    () => {}
  );
  buffer.push(1);
  buffer.push(2);
  buffer.push(3);
  assert.equal(frames.length, 1);
  assert.deepEqual(applied, []);
  frames[0]();
  assert.deepEqual(applied, [[1, 2, 3]]);
  buffer.push(4);
  buffer.clear();
  assert.equal(frames.length, 2);
  frames[1]();
  assert.deepEqual(applied, [[1, 2, 3]]);
});

test("stream logs keep the newest entries and preview large payloads", () => {
  assert.deepEqual(appendStreamLogs([1, 2], [3, 4], 3), [2, 3, 4]);
  const current = [1];
  assert.notEqual(appendStreamLogs(current, [2]), current);
  const big = "x".repeat(5000);
  assert.equal(isStreamLogTruncated(big), true);
  assert.equal(streamLogPreview(big).length, 4097);
  assert.equal(streamLogPreview("short"), "short");
  assert.match(streamPanel, /const logs = shallowRef<LogEntry\[\]>/);
  assert.match(streamPanel, /logBuffer\.push\(/);
  assert.doesNotMatch(streamPanel, /logs\.value\.push\(/);
});

test("large responses skip automatic JSON formatting", () => {
  assert.equal(formatJsonResponseBody('{"a":1}'), '{\n  "a": 1\n}');
  assert.equal(formatJsonResponseBody("not json"), "not json");
  assert.equal(isLargeResponseBody("x".repeat(10)), false);
  assert.equal(isLargeResponseBody("x".repeat(2_000_000)), true);
  assert.equal(looksLikeJsonBody("  [1]"), true);
  assert.equal(looksLikeJsonBody("<html>"), false);
  const pretty = httpView.match(/const prettyBody = computed\(\(\) => \{[\s\S]*?\n\}\);/)?.[0];
  assert.ok(pretty);
  assert.match(pretty, /if \(!isLargeResponseBody\(body\)\) return formatJsonResponseBody\(body\);/);
  assert.match(httpView, /@click="formatLargeResponseBody"/);
});
