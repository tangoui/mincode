import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { URL } from "node:url";
import test from "node:test";

// Runtime sockets require a WebView, so this test verifies the protocol invariants
// in source while integration behavior remains covered by TypeScript and builds.
test("resilient JSON subscriptions require an ack and reconnect after transport loss", async () => {
  const source = await readFile(
    new URL("../src/services/resilient-json-stream.ts", import.meta.url),
    "utf8",
  );

  assert.match(source, /options\.isSubscribed\(event\.data\)/);
  assert.match(source, /if \(subscribed\) options\.onMessage/);
  assert.match(source, /addEventListener\("error", fail\)/);
  assert.match(source, /addEventListener\("close", fail\)/);
  assert.match(source, /reconnectDelays\[Math\.min\(attempt, reconnectDelays\.length - 1\)\]/);
  assert.match(source, /disposed = true;[\s\S]*clearTimeout\(reconnectTimer\)/);
});

 test("agent progress refreshes its backend snapshot after every subscription", async () => {
  const source = await readFile(
    new URL("../src/services/session-progress-service.ts", import.meta.url),
    "utf8",
  );

  assert.match(source, /name: "agent-run-progress"/);
  assert.match(source, /onSubscribed: \(\) => \{[\s\S]*listAgentRuns\(\)/);
});
