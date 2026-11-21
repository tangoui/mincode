import assert from "node:assert/strict";
import test from "node:test";
import {
  ByteLru,
  CONTRIBUTION_ASSET_BATCH_KEYS,
  createContributionAssetLoader,
  isContributionAssetKey,
  planContributionAssetBatches,
} from "../src/domain/plugins/contribution-assets.ts";

const key = (n) => n.toString(16).padStart(64, "0");
const b64 = (text) => Buffer.from(text).toString("base64");

test("asset keys are lower-case hex sha256", () => {
  assert.equal(isContributionAssetKey(key(1)), true);
  assert.equal(isContributionAssetKey(key(1).toUpperCase().replace(/^0/, "A")), false);
  assert.equal(isContributionAssetKey("../x"), false);
  assert.equal(isContributionAssetKey(key(1) + "0"), false);
});

test("batches deduplicate and split at 64 keys", () => {
  const keys = Array.from({ length: 130 }, (_, index) => key(index));
  const { batches, invalid } = planContributionAssetBatches([...keys, key(0), "bad"]);
  assert.deepEqual(batches.map((batch) => batch.length), [64, 64, 2]);
  assert.equal(CONTRIBUTION_ASSET_BATCH_KEYS, 64);
  assert.deepEqual(invalid, ["bad"]);
});

test("byte LRU evicts oldest by bytes and reports evictions", () => {
  const evicted = [];
  const lru = new ByteLru(10, (k) => evicted.push(k));
  lru.set("a", 1, 6);
  lru.set("b", 2, 4);
  lru.get("a");
  lru.set("c", 3, 4);
  assert.deepEqual(evicted, ["b"]);
  assert.equal(lru.bytes, 10);
  assert.equal(lru.set("huge", 4, 11), false);
  lru.deleteWhere((k) => k === "a");
  assert.deepEqual(evicted, ["b", "a"]);
  assert.equal(lru.bytes, 4);
});

test("loader coalesces, batches, follows deferred and caches", async () => {
  const calls = [];
  const loader = createContributionAssetLoader({
    coalesceMs: 1,
    invoke: async (request) => {
      calls.push(request.keys);
      // Serve one asset per call and defer the rest; key(9) is stale, key(8) missing.
      const assets = [];
      const deferred = [];
      const missing = [];
      const stale = [];
      for (const k of request.keys) {
        if (k === key(9)) stale.push(k);
        else if (k === key(8)) missing.push(k);
        else if (assets.length === 0) assets.push({ key: k, mime: "text/plain", data: b64(k.slice(-1)) });
        else deferred.push(k);
      }
      return { assets, missing, stale, deferred };
    },
  });
  const [first, second] = await Promise.all([
    loader.fetchAssets("p", "fileIcons", [key(1), key(9)]),
    loader.fetchAssets("p", "fileIcons", [key(2), key(8), "bad"]),
  ]);
  assert.equal(calls[0].length, 4);
  assert.equal(calls.length, 2);
  assert.equal(new TextDecoder().decode(first.assets.get(key(1)).bytes), "1");
  assert.deepEqual(first.stale, [key(9)]);
  assert.equal(second.assets.get(key(2)).mime, "text/plain");
  assert.deepEqual(second.missing.sort(), [key(8), "bad"].sort());

  const cached = await loader.fetchAssets("p", "fileIcons", [key(1), key(2)]);
  assert.equal(cached.assets.size, 2);
  assert.equal(calls.length, 2);
});

test("blob URLs are cached and revoked on release", async () => {
  const revoked = [];
  let counter = 0;
  const loader = createContributionAssetLoader({
    coalesceMs: 0,
    invoke: async (request) => ({
      assets: request.keys.map((k) => ({ key: k, mime: "image/svg+xml", data: b64("<svg/>") })),
      missing: [],
      stale: [],
      deferred: [],
    }),
    createObjectURL: () => `blob:${++counter}`,
    revokeObjectURL: (url) => revoked.push(url),
  });
  const url = await loader.objectURL("p", "fileIcons", key(1));
  assert.equal(url, "blob:1");
  assert.equal(await loader.objectURL("p", "fileIcons", key(1)), "blob:1");
  loader.releasePlugin("p");
  assert.deepEqual(revoked, ["blob:1"]);
  assert.equal(loader.cache.size, 0);
});

test("a response without progress does not loop", async () => {
  let calls = 0;
  const loader = createContributionAssetLoader({
    coalesceMs: 0,
    invoke: async (request) => {
      calls += 1;
      return { assets: [], missing: [], stale: [], deferred: request.keys };
    },
  });
  const result = await loader.fetchAssets("p", "x", [key(1)]);
  assert.equal(calls, 1);
  assert.deepEqual(result.missing, [key(1)]);
});
