import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { URL } from "node:url";
import test from "node:test";

function read(path) {
  return readFile(new URL(path, import.meta.url), "utf8");
}

test("HTTP Stream transport failure stops the backend session and enters error state", async () => {
  const subscription = await read("../src/services/http-stream-subscription.ts");
  const start = subscription.indexOf("const handleTransportFailure =");
  const end = subscription.indexOf("const ready = new Promise<void>", start);
  const failureHandler = subscription.slice(start, end);

  assert.ok(start >= 0 && end > start);
  assert.match(failureHandler, /http_stream_disconnect/);
  assert.match(failureHandler, /options\.onTransportLost\(\)/);
  assert.match(subscription, /socket\.addEventListener\("close"/);
  assert.match(subscription, /socket\.addEventListener\("error"/);

  for (const panel of ["HttpStreamPanel.vue", "HttpSocketIOPanel.vue"]) {
    const source = await read(`../src/components/http/${panel}`);
    const lostStart = source.indexOf("onTransportLost()");
    const lostEnd = source.indexOf("stopListening = subscription.stop", lostStart);
    const onLost = source.slice(lostStart, lostEnd);
    assert.ok(lostStart >= 0 && lostEnd > lostStart, panel);
    assert.match(onLost, /state\.value = "error"/, panel);
    assert.match(onLost, /sessionId = ""/, panel);
  }
});
