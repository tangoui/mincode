/**
 * Pure helpers for plugin contribution assets: package files a contribution
 * point declares in its contract (`assets`) and references in the
 * contributions snapshot only by the hex sha256 of their canonical bytes.
 * The host command `plugin_host_contribution_assets` serves them in batches
 * of at most CONTRIBUTION_ASSET_BATCH_KEYS keys; see
 * services/contribution-assets.ts for the fetching side.
 */

/** Maximum keys per `plugin_host_contribution_assets` call (host limit). */
export const CONTRIBUTION_ASSET_BATCH_KEYS = 64;

/** Byte budget of the frontend blob/bytes LRU. */
export const CONTRIBUTION_ASSET_CACHE_BYTES = 16 * 1024 * 1024;

const assetKeyPattern = /^[0-9a-f]{64}$/;

/** Asset keys are lower-case hex sha256 digests (64 characters). */
export function isContributionAssetKey(value: unknown): value is string {
  return typeof value === "string" && assetKeyPattern.test(value);
}

/**
 * Deduplicates valid keys in first-seen order and splits them into batches of
 * at most `size` keys. Invalid keys are returned separately so callers can
 * report them as missing without a host round trip.
 */
export function planContributionAssetBatches(
  keys: readonly string[],
  size = CONTRIBUTION_ASSET_BATCH_KEYS,
): { batches: string[][]; invalid: string[] } {
  const limit = Math.max(1, Math.floor(size));
  const seen = new Set<string>();
  const valid: string[] = [];
  const invalid: string[] = [];
  for (const key of keys) {
    if (seen.has(key)) continue;
    seen.add(key);
    if (isContributionAssetKey(key)) valid.push(key);
    else invalid.push(key);
  }
  const batches: string[][] = [];
  for (let index = 0; index < valid.length; index += limit) {
    batches.push(valid.slice(index, index + limit));
  }
  return { batches, invalid };
}

/** Cache key of one asset: assets are content-addressed per plugin and point. */
export function contributionAssetCacheKey(pluginId: string, point: string, key: string): string {
  return `${pluginId}\u0000${point}\u0000${key}`;
}

/** Decodes the base64 `data` field of a host asset DTO. */
export function decodeContributionAssetData(data: string): Uint8Array {
  const binary = atob(data);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
}

/**
 * Byte-bounded LRU. `onEvict` runs for every entry removed by eviction,
 * `delete`, `deleteWhere` or `clear` (for example to revoke a blob URL).
 * An entry larger than the whole budget is not stored.
 */
export class ByteLru<V> {
  private readonly entries = new Map<string, { value: V; bytes: number }>();
  private total = 0;

  readonly maxBytes: number;
  private readonly onEvict: (key: string, value: V) => void;

  constructor(maxBytes: number, onEvict: (key: string, value: V) => void = () => {}) {
    this.maxBytes = maxBytes;
    this.onEvict = onEvict;
  }

  get bytes(): number {
    return this.total;
  }

  get size(): number {
    return this.entries.size;
  }

  get(key: string): V | undefined {
    const entry = this.entries.get(key);
    if (!entry) return undefined;
    this.entries.delete(key);
    this.entries.set(key, entry);
    return entry.value;
  }

  has(key: string): boolean {
    return this.entries.has(key);
  }

  /** Stores a value; returns false when it exceeds the whole budget. */
  set(key: string, value: V, bytes: number): boolean {
    this.delete(key);
    if (bytes > this.maxBytes) return false;
    this.entries.set(key, { value, bytes });
    this.total += bytes;
    for (const [oldest, entry] of this.entries) {
      if (this.total <= this.maxBytes) break;
      this.entries.delete(oldest);
      this.total -= entry.bytes;
      this.onEvict(oldest, entry.value);
    }
    return true;
  }

  delete(key: string): boolean {
    const entry = this.entries.get(key);
    if (!entry) return false;
    this.entries.delete(key);
    this.total -= entry.bytes;
    this.onEvict(key, entry.value);
    return true;
  }

  /** Removes every entry whose key matches; returns the number removed. */
  deleteWhere(predicate: (key: string) => boolean): number {
    let removed = 0;
    for (const key of [...this.entries.keys()]) {
      if (predicate(key) && this.delete(key)) removed += 1;
    }
    return removed;
  }

  clear(): void {
    this.deleteWhere(() => true);
  }
}

/** Request/response of the host command `plugin_host_contribution_assets`. */
export interface ContributionAssetsRequest {
  pluginId: string;
  point: string;
  keys: string[];
}

export interface ContributionAssetsResponse {
  assets: Array<{ key: string; mime: string; data: string }>;
  missing: string[];
  stale: string[];
  deferred: string[];
}

export interface ContributionAsset {
  key: string;
  mime: string;
  bytes: Uint8Array;
}

export interface ContributionAssetsResult {
  assets: Map<string, ContributionAsset>;
  /** Not referenced by the plugin's current contributions, or invalid now. */
  missing: string[];
  /** Changed on disk since the contributions were resolved (reload needed). */
  stale: string[];
}

export interface ContributionAssetLoaderOptions {
  invoke: (request: ContributionAssetsRequest) => Promise<ContributionAssetsResponse>;
  /** Coalescing delay; defaults to 16 ms. */
  coalesceMs?: number;
  maxBytes?: number;
  createObjectURL?: (blob: Blob) => string;
  revokeObjectURL?: (url: string) => void;
}

interface CachedAsset {
  asset: ContributionAsset;
  url?: string;
}

interface PendingGroup {
  pluginId: string;
  point: string;
  keys: Set<string>;
  waiters: Array<{ keys: string[]; resolve: (result: ContributionAssetsResult) => void; reject: (error: unknown) => void }>;
}

/**
 * Coalescing, batching, caching asset loader. Requests for the same plugin and
 * point made within `coalesceMs` share host calls of at most 64 keys; deferred
 * keys are requested again until served. Bytes and blob URLs share one byte
 * LRU; blob URLs are revoked when their entry is evicted or released.
 */
export function createContributionAssetLoader(options: ContributionAssetLoaderOptions) {
  const coalesceMs = options.coalesceMs ?? 16;
  const createURL = options.createObjectURL ?? ((blob: Blob) => URL.createObjectURL(blob));
  const revokeURL = options.revokeObjectURL ?? ((url: string) => URL.revokeObjectURL(url));
  const cache = new ByteLru<CachedAsset>(options.maxBytes ?? CONTRIBUTION_ASSET_CACHE_BYTES, (_key, value) => {
    if (value.url) revokeURL(value.url);
  });
  const pending = new Map<string, PendingGroup>();
  // Released plugins bump their generation so in-flight responses are not cached.
  const generations = new Map<string, number>();

  async function load(group: PendingGroup): Promise<{ served: Map<string, ContributionAsset>; missing: Set<string>; stale: Set<string> }> {
    const served = new Map<string, ContributionAsset>();
    const missing = new Set<string>();
    const stale = new Set<string>();
    const generation = generations.get(group.pluginId) ?? 0;
    const { batches, invalid } = planContributionAssetBatches([...group.keys]);
    for (const key of invalid) missing.add(key);
    const queue = batches;
    while (queue.length > 0) {
      const batch = queue.shift()!;
      const response = await options.invoke({ pluginId: group.pluginId, point: group.point, keys: batch });
      for (const dto of response?.assets ?? []) {
        if (!batch.includes(dto.key)) continue;
        const asset: ContributionAsset = { key: dto.key, mime: dto.mime, bytes: decodeContributionAssetData(dto.data) };
        served.set(dto.key, asset);
        if ((generations.get(group.pluginId) ?? 0) === generation) {
          cache.set(contributionAssetCacheKey(group.pluginId, group.point, dto.key), { asset }, asset.bytes.byteLength);
        }
      }
      for (const key of response?.missing ?? []) missing.add(key);
      for (const key of response?.stale ?? []) stale.add(key);
      const deferred = (response?.deferred ?? []).filter((key) => batch.includes(key));
      if (deferred.length === 0) continue;
      if (!(response?.assets ?? []).length) {
        // No progress: report instead of looping forever.
        for (const key of deferred) missing.add(key);
        continue;
      }
      queue.unshift(deferred);
    }
    return { served, missing, stale };
  }

  function flush(groupKey: string) {
    const group = pending.get(groupKey);
    if (!group) return;
    pending.delete(groupKey);
    load(group).then(
      ({ served, stale }) => {
        for (const waiter of group.waiters) {
          const result: ContributionAssetsResult = { assets: new Map(), missing: [], stale: [] };
          for (const key of new Set(waiter.keys)) {
            const asset = served.get(key);
            if (asset) result.assets.set(key, asset);
            else if (stale.has(key)) result.stale.push(key);
            else result.missing.push(key);
          }
          waiter.resolve(result);
        }
      },
      (error) => {
        for (const waiter of group.waiters) waiter.reject(error);
      },
    );
  }

  /** Fetches assets by key, from the cache when possible. */
  function fetchAssets(pluginId: string, point: string, keys: readonly string[]): Promise<ContributionAssetsResult> {
    const cached = new Map<string, ContributionAsset>();
    const needed: string[] = [];
    for (const key of new Set(keys)) {
      const hit = cache.get(contributionAssetCacheKey(pluginId, point, key));
      if (hit) cached.set(key, hit.asset);
      else needed.push(key);
    }
    if (needed.length === 0) return Promise.resolve({ assets: cached, missing: [], stale: [] });
    const groupKey = `${pluginId}\u0000${point}`;
    let group = pending.get(groupKey);
    if (!group) {
      group = { pluginId, point, keys: new Set(), waiters: [] };
      pending.set(groupKey, group);
      setTimeout(() => flush(groupKey), coalesceMs);
    }
    for (const key of needed) group.keys.add(key);
    return new Promise((resolve, reject) => {
      group!.waiters.push({
        keys: needed,
        resolve: (result) => {
          for (const [key, asset] of cached) result.assets.set(key, asset);
          resolve(result);
        },
        reject,
      });
    });
  }

  async function assetBytes(pluginId: string, point: string, key: string): Promise<ContributionAsset | null> {
    const result = await fetchAssets(pluginId, point, [key]);
    return result.assets.get(key) ?? null;
  }

  /** Returns a blob URL for one asset (cached with the bytes), or null. */
  async function objectURL(pluginId: string, point: string, key: string): Promise<string | null> {
    const cacheKey = contributionAssetCacheKey(pluginId, point, key);
    const hit = cache.get(cacheKey);
    if (hit?.url) return hit.url;
    const asset = hit?.asset ?? (await assetBytes(pluginId, point, key));
    if (!asset) return null;
    const current = cache.get(cacheKey);
    if (current?.url) return current.url;
    if (!current) return null; // released or too large to cache: never hand out an unowned URL
    current.url = createURL(new Blob([asset.bytes as BlobPart], { type: asset.mime }));
    return current.url;
  }

  /** Drops cached bytes and revokes blob URLs of one plugin. */
  function releasePlugin(pluginId: string) {
    generations.set(pluginId, (generations.get(pluginId) ?? 0) + 1);
    cache.deleteWhere((cacheKey) => cacheKey.startsWith(`${pluginId}\u0000`));
  }

  return { fetchAssets, assetBytes, objectURL, releasePlugin, clear: () => cache.clear(), cache };
}
