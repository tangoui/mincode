import assert from "node:assert/strict";
import test from "node:test";
import { formatJsonOffThread, setJsonFormatRunner } from "../src/services/json-format.ts";
import { formatJsonResponseBody } from "../src/domain/http/response-body.ts";

test("without a worker it falls back to the same formatting as the synchronous path", async () => {
  setJsonFormatRunner(undefined);
  const body = '{"a":1,"b":[1,2]}';
  assert.equal(await formatJsonOffThread(body), formatJsonResponseBody(body));
  assert.equal(await formatJsonOffThread("not json"), "not json");
});

test("uses the injected off-thread runner and returns its result", async () => {
  const calls = [];
  setJsonFormatRunner(async (body) => { calls.push(body); return `formatted:${body.length}`; });
  assert.equal(await formatJsonOffThread('{"x":1}'), "formatted:7");
  assert.deepEqual(calls, ['{"x":1}']);
  setJsonFormatRunner(undefined);
});

test("a failing runner falls back to synchronous formatting", async () => {
  setJsonFormatRunner(async () => { throw new Error("worker crashed"); });
  assert.equal(await formatJsonOffThread('{"y":2}'), formatJsonResponseBody('{"y":2}'));
  setJsonFormatRunner(undefined);
});
