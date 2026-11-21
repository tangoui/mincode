// Pinned, checksummed downloads for the build hooks of official plugins
// (official-plugins/*/scripts/fetch-assets.mjs, run by build-go-plugins.mjs). Files are
// cached under ~/.cache/mincode-plugin-assets/<plugin id>/ (MINCODE_PLUGIN_ASSETS_CACHE
// overrides the parent directory); a cached file is used only while its size and SHA-256
// match the pin, so offline builds work once the cache holds the verified files.
import { createHash } from 'node:crypto';
import { mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';

export const sha256 = data => createHash('sha256').update(data).digest('hex');

/** The asset cache directory of one plugin. */
export function pluginAssetCacheDir(pluginId, env = process.env) {
  return resolve(env.MINCODE_PLUGIN_ASSETS_CACHE || join(homedir(), '.cache', 'mincode-plugin-assets'), pluginId);
}

const ATTEMPTS = 3;
// A connection that delivers nothing (no headers, no body bytes) for this long is abandoned
// and retried instead of waiting out the whole attempt.
const STALL_MS = 60 * 1000;

/** The server returned the wrong file: retrying cannot help. */
export class MismatchError extends Error {}

// Settles like promise, or rejects once nothing has happened for stallMs.
async function unlessStalled(promise, stallMs) {
  let timer;
  const stalled = new Promise((_, fail) => {
    timer = setTimeout(() => fail(new Error(`no data for ${stallMs / 1000} s`)), stallMs);
  });
  try {
    return await Promise.race([promise, stalled]);
  } finally {
    clearTimeout(timer);
  }
}

async function fetchPinned(pin, { fetchImpl, stallMs, attemptTimeoutMs }) {
  const abort = new AbortController();
  const signal = AbortSignal.any([abort.signal, AbortSignal.timeout(attemptTimeoutMs)]);
  let reader;
  try {
    const response = await unlessStalled(fetchImpl(pin.url, { redirect: 'follow', signal }), stallMs);
    if (!response.ok || !response.body) throw new Error(`HTTP ${response.status}`);
    reader = response.body.getReader();
    const chunks = [];
    let received = 0;
    for (;;) {
      const { done, value: chunk } = await unlessStalled(reader.read(), stallMs);
      if (done) break;
      received += chunk.length;
      // Stop as soon as the body is larger than the pinned file.
      if (received > pin.bytes) throw new MismatchError(`response exceeds the pinned ${pin.bytes} bytes`);
      chunks.push(chunk);
    }
    const data = Buffer.concat(chunks.map(chunk => Buffer.from(chunk.buffer, chunk.byteOffset, chunk.length)), received);
    if (data.length !== pin.bytes) throw new MismatchError(`size mismatch: expected ${pin.bytes} bytes, got ${data.length}`);
    const digest = sha256(data);
    if (digest !== pin.sha256) throw new MismatchError(`SHA-256 mismatch: expected ${pin.sha256}, got ${digest}`);
    return data;
  } catch (error) {
    // Release the connection of an abandoned attempt.
    abort.abort();
    reader?.cancel().catch(() => {});
    throw error;
  }
}

/**
 * Returns the bytes of a pinned download ({name, url, bytes, sha256}; name is the cache file
 * name), verified, from <cache>/<cacheSubdir>/<name> or over HTTPS: up to three attempts with
 * backoff, a network error or stalled connection is retried, a wrong size or digest is
 * deterministic and not retried. The cached file is read once and that buffer verified (what
 * is checked is exactly what is used); one that does not match is discarded. Only verified
 * bytes are ever written to the cache.
 */
export async function downloadPinned(pin, cacheSubdir, {
  env = process.env, fetchImpl = fetch, retryDelayMs = 2000, stallMs = STALL_MS, attemptTimeoutMs = 15 * 60 * 1000,
} = {}) {
  if (!/^https:\/\//.test(pin.url)) throw new Error(`Refusing a non-HTTPS download: ${pin.url}`);
  const dir = pluginAssetCacheDir(cacheSubdir, env);
  const path = join(dir, pin.name);
  const cached = await readFile(path).catch(error => { if (error.code === 'ENOENT') return null; throw error; });
  if (cached) {
    if (cached.length === pin.bytes && sha256(cached) === pin.sha256) return cached;
    console.warn(`Discarding ${path}: it does not match the pinned SHA-256`);
    await rm(path, { force: true });
  }
  await mkdir(dir, { recursive: true });
  let lastError;
  for (let attempt = 1; attempt <= ATTEMPTS; attempt++) {
    console.log(`Downloading ${pin.name} (${(pin.bytes / 1048576).toFixed(1)} MiB) from ${new URL(pin.url).host}${attempt > 1 ? `, attempt ${attempt}/${ATTEMPTS}` : ''}`);
    try {
      const data = await fetchPinned(pin, { fetchImpl, stallMs, attemptTimeoutMs });
      const partial = `${path}.${process.pid}.partial`;
      try {
        await writeFile(partial, data);
        await rename(partial, path);
      } finally {
        await rm(partial, { force: true });
      }
      return data;
    } catch (error) {
      lastError = error;
      if (error instanceof MismatchError) break;
      if (attempt < ATTEMPTS) await new Promise(done => setTimeout(done, retryDelayMs * 2 ** (attempt - 1)));
    }
  }
  throw new Error(`Could not download ${pin.name} from ${pin.url}: ${lastError.message}. Place the verified file at ${path} to build offline.`, { cause: lastError });
}
