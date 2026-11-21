import assert from "node:assert/strict";
import test from "node:test";
import {
  aggregateTestStatus,
  collectTestProviders,
  parseTestDiscoverResponse,
  parseTestRunResponse,
  sanitizeTestText,
  testIds,
  testProviderKey,
  testProviderPreviewSummary,
  TEST_PROVIDER_LIMITS,
  validateTestProviderEntry,
} from "../src/domain/plugins/test-providers.ts";

function contribution(pluginId, entries) {
  return {
    pluginId,
    pluginName: pluginId,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: "f", entries, issues: [{ entry: 9, message: "host rejected" }] },
  };
}

const provider = { id: "jest", title: "Jest", discoverAction: "tests.discover", runAction: "tests.run", runRisk: "write" };

test("testProviders: re-validates entries; unknown risk is treated as dangerous", () => {
  assert.equal(validateTestProviderEntry({ ...provider, runAction: provider.discoverAction }).ok, false);
  assert.equal(validateTestProviderEntry({ ...provider, discoverAction: undefined }).ok, false);
  assert.equal(validateTestProviderEntry({ ...provider, runRisk: undefined }).value.runRisk, "read");
  assert.equal(validateTestProviderEntry({ ...provider, runRisk: "weird" }).value.runRisk, "dangerous");
  const { providers, issues } = collectTestProviders([contribution("acme.tests", [provider, { id: "x" }])]);
  assert.deepEqual(providers.map(testProviderKey), ["acme.tests/jest"]);
  assert.equal(providers[0].runRisk, "write");
  assert.deepEqual(issues.map((issue) => issue.stage), ["host", "frontend"]);
});

test("testProviders: discovery keeps a valid tree with unique ids", () => {
  const response = parseTestDiscoverResponse({
    tests: [
      {
        id: "suite",
        label: "Suite",
        path: "src/a.test.ts",
        children: [
          { id: "t1", label: "works", path: "src/a.test.ts", line: 4 },
          { id: "t1", label: "duplicate" },
          { id: "t2", label: "no path line", line: 3 },
          { id: "t3", label: "bad path", path: "/abs/a.ts" },
        ],
      },
      { id: "t4", label: "x\u202ey" },
    ],
  });
  assert.deepEqual(testIds(response.tests), ["suite", "t1"]);
  assert.equal(response.dropped, 4);
  assert.equal(response.tests[0].children[0].line, 4);

  let nested = { id: "d9", label: "deep" };
  for (let depth = 8; depth >= 1; depth -= 1) nested = { id: `d${depth}`, label: `D${depth}`, children: [nested] };
  const deep = parseTestDiscoverResponse({ tests: [nested] });
  assert.equal(testIds(deep.tests).length, TEST_PROVIDER_LIMITS.depthMax);
  assert.equal(deep.dropped, 1);

  const many = parseTestDiscoverResponse({ tests: Array.from({ length: TEST_PROVIDER_LIMITS.testsMax + 1 }, (_, i) => ({ id: `t${i}`, label: "t" })) });
  assert.equal(many.tests.length, TEST_PROVIDER_LIMITS.testsMax);
  assert.equal(many.truncated, true);
});

test("testProviders: run results only for discovered ids; text is sanitized", () => {
  const known = new Set(["suite", "t1", "t2"]);
  const response = parseTestRunResponse({
    results: [
      { id: "t1", status: "passed", durationMs: 12.6 },
      { id: "t2", status: "failed", message: "expected\r\n1\u0007" },
      { id: "t1", status: "failed" },
      { id: "other", status: "passed" },
      { id: "t2", status: "bogus" },
      { id: "suite", status: "skipped", durationMs: -1 },
    ],
    output: "line\u001b[31m red",
  }, known);
  assert.deepEqual(response.results, [
    { id: "t1", status: "passed", durationMs: 13 },
    { id: "t2", status: "failed", message: "expected\n1" },
  ]);
  assert.equal(response.dropped, 4);
  assert.equal(response.output, "line[31m red");
  assert.equal(response.outputTruncated, false);
  assert.deepEqual(sanitizeTestText("abcdef", 3), { text: "abc", truncated: true });
});

test("testProviders: aggregate status prefers failures over passes", () => {
  const tree = { id: "suite", label: "S", children: [{ id: "a", label: "a" }, { id: "b", label: "b" }] };
  const results = new Map([["a", { id: "a", status: "passed" }]]);
  assert.equal(aggregateTestStatus(tree, results), "passed");
  results.set("b", { id: "b", status: "failed" });
  assert.equal(aggregateTestStatus(tree, results), "failed");
  assert.equal(aggregateTestStatus({ id: "x", label: "x" }, results), undefined);
});

test("testProviders: install preview lists titles", () => {
  assert.deepEqual(testProviderPreviewSummary({ testProviders: [provider, { id: "vitest" }] }), ["Jest", "vitest"]);
});
