import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { downloadPinned, pluginAssetCacheDir, sha256 } from './plugin-asset-download.mjs';

const body = Buffer.from('pinned asset body '.repeat(100));
const pin = { name: 'asset.bin', url: 'https://example.test/asset.bin', bytes: body.length, sha256: sha256(body) };

async function withCache(run) {
  const cache = await mkdtemp(join(tmpdir(), 'mincode-asset-download-'));
  try {
    await run({ env: { ...process.env, MINCODE_PLUGIN_ASSETS_CACHE: cache }, dir: join(cache, 'demo') });
  } finally {
    await rm(cache, { recursive: true, force: true });
  }
}

test('the cache directory is per plugin and honours MINCODE_PLUGIN_ASSETS_CACHE', () => {
  assert.equal(pluginAssetCacheDir('demo', { MINCODE_PLUGIN_ASSETS_CACHE: '/c' }), resolve('/c/demo'));
  assert.match(pluginAssetCacheDir('demo', {}), /mincode-plugin-assets[\\/]demo$/);
});

test('a verified download is cached and then used without fetching', () => withCache(async ({ env, dir }) => {
  let fetches = 0;
  const data = await downloadPinned(pin, 'demo', { env, fetchImpl: async () => { fetches++; return new Response(body); } });
  assert.deepEqual(data, body);
  assert.equal(fetches, 1);
  assert.deepEqual(await readFile(join(dir, pin.name)), body);
  assert.deepEqual(await readdir(dir), [pin.name], 'no partial file left');
  assert.deepEqual(await downloadPinned(pin, 'demo', { env, fetchImpl: async () => { throw new Error('must not fetch'); } }), body);
}));

test('a mismatching cached file is discarded and a wrong download is not retried', () => withCache(async ({ env, dir }) => {
  await mkdir(dir, { recursive: true });
  await writeFile(join(dir, pin.name), Buffer.alloc(pin.bytes));
  let fetches = 0;
  await assert.rejects(downloadPinned(pin, 'demo', { env, retryDelayMs: 1, fetchImpl: async () => { fetches++; return new Response(Buffer.alloc(pin.bytes, 7)); } }),
    /SHA-256 mismatch.*Place the verified file at/s);
  assert.equal(fetches, 1);
  assert.equal(existsSync(join(dir, pin.name)), false);
  fetches = 0;
  await assert.rejects(downloadPinned(pin, 'demo', { env, retryDelayMs: 1, fetchImpl: async () => { fetches++; return new Response(Buffer.alloc(10)); } }), /size mismatch/);
  assert.equal(fetches, 1);
  // A body larger than the pin is abandoned while streaming.
  let pulled = 0;
  const endless = async () => { fetches++; return new Response(new ReadableStream({ pull(controller) { pulled += 1024; controller.enqueue(new Uint8Array(1024)); } })); };
  fetches = 0;
  await assert.rejects(downloadPinned(pin, 'demo', { env, retryDelayMs: 1, fetchImpl: endless }), /exceeds the pinned/);
  assert.equal(fetches, 1);
  assert.ok(pulled < pin.bytes + 16 * 1024, `stops reading near the pinned size (read ${pulled})`);
  assert.deepEqual(await readdir(dir), []);
}));

test('a network error is retried', () => withCache(async ({ env }) => {
  let calls = 0;
  const flaky = async () => { calls++; if (calls === 1) throw new TypeError('fetch failed'); return new Response(body); };
  assert.deepEqual(await downloadPinned(pin, 'demo', { env, retryDelayMs: 1, fetchImpl: flaky }), body);
  assert.equal(calls, 2);
}));

test('HTTP errors are retried three times', () => withCache(async ({ env }) => {
  let calls = 0;
  await assert.rejects(downloadPinned(pin, 'demo', { env, retryDelayMs: 1, fetchImpl: async () => { calls++; return new Response(null, { status: 404 }); } }), /HTTP 404/);
  assert.equal(calls, 3);
}));

test('abandons a stalled download and retries it', () => withCache(async ({ env }) => {
  let calls = 0;
  // A connection that delivers a few bytes and then nothing, without ever closing.
  const stalled = async () => {
    calls++;
    return new Response(new ReadableStream({ start(controller) { controller.enqueue(new Uint8Array(16)); } }));
  };
  await assert.rejects(downloadPinned(pin, 'demo', { env, fetchImpl: stalled, retryDelayMs: 1, stallMs: 50 }), /no data for/);
  assert.equal(calls, 3, 'a stall is retried like a network error');
  // Response headers that never arrive count as a stall too.
  calls = 0;
  const silent = async () => { calls++; return new Promise(() => {}); };
  await assert.rejects(downloadPinned(pin, 'demo', { env, fetchImpl: silent, retryDelayMs: 1, stallMs: 50 }), /no data for/);
  assert.equal(calls, 3);
}));

test('refuses non-HTTPS pins', () => withCache(async ({ env }) => {
  await assert.rejects(downloadPinned({ ...pin, url: 'http://example.test/asset.bin' }, 'demo', { env }), /non-HTTPS/);
}));
