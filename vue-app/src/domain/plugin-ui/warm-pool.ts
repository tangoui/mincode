// Warm pool for closed plugin view tabs (pure logic; the store owns timers and
// events, PluginWorkspaceView owns the frames).
//
// Closing a plugin workspace tab normally destroys its sandboxed iframe and UI
// session, so reopening pays process activation, frame load and handshake
// again. A healthy (connected) workspace page frame may instead be parked here:
// it stays mounted but hidden, inert and without user input, and reopening the
// same tab (same workspace, plugin and view, hence the same deterministic tab
// ID) reuses it. Entries are bounded by count (least recently parked first out)
// and by age, and every revocation path discards them (see the store).

/** Backend event after a plugin's UI sessions were revoked: {pluginId, sessionIds}. */
export const PLUGIN_UI_REVOKED_EVENT = "plugin:ui-revoked";

/** Default number of parked frames; 0 disables the pool. */
// Off by default: a closed plugin view releases its page at once (and its process
// can stop). Users who prefer instant reopening opt in under Settings > General.
export const DEFAULT_PLUGIN_VIEW_WARM_LIMIT = 0;
export const MIN_PLUGIN_VIEW_WARM_LIMIT = 0;
export const MAX_PLUGIN_VIEW_WARM_LIMIT = 6;
/** A parked frame older than this is destroyed. */
export const PLUGIN_VIEW_WARM_TTL_MS = 10 * 60_000;

export interface PluginViewWarmEntry {
  /** The closed tab's ID; reopening the same view in the same workspace yields it again. */
  key: string;
  workspaceId: string;
  pluginId: string;
  viewId: string;
  /** When the tab was closed (ms since epoch). */
  parkedAt: number;
}

export function normalizePluginViewWarmLimit(value: unknown): number {
  if (value === undefined || value === null || value === "") return DEFAULT_PLUGIN_VIEW_WARM_LIMIT;
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) return DEFAULT_PLUGIN_VIEW_WARM_LIMIT;
  return Math.min(MAX_PLUGIN_VIEW_WARM_LIMIT, Math.max(MIN_PLUGIN_VIEW_WARM_LIMIT, Math.round(parsed)));
}

/** Drops the oldest entries until at most `limit` remain. */
export function trimPluginViewWarmEntries(entries: readonly PluginViewWarmEntry[], limit: number) {
  const max = Math.max(0, Math.floor(limit));
  if (entries.length <= max) return { entries: [...entries], evicted: [] as PluginViewWarmEntry[] };
  const ordered = [...entries].sort((left, right) => left.parkedAt - right.parkedAt);
  const evicted = ordered.slice(0, ordered.length - max);
  const evictedKeys = new Set(evicted.map((entry) => entry.key));
  return { entries: entries.filter((entry) => !evictedKeys.has(entry.key)), evicted };
}

/**
 * Parks one entry (replacing an entry with the same key) and evicts the least
 * recently parked ones beyond `limit`. With limit 0 the entry itself is evicted.
 */
export function parkPluginViewWarmEntry(entries: readonly PluginViewWarmEntry[], entry: PluginViewWarmEntry, limit: number) {
  // A replaced entry has the same key and therefore the same frame: it is not evicted.
  const next = [...entries.filter((item) => item.key !== entry.key), entry];
  return trimPluginViewWarmEntries(next, limit);
}

/**
 * Takes the entry for a reopened tab. The tab must describe the same workspace,
 * plugin and view; anything else is discarded rather than reused.
 */
export function takePluginViewWarmEntry(
  entries: readonly PluginViewWarmEntry[],
  target: { key: string; workspaceId: string; pluginId: string; viewId: string },
  now: number,
  ttlMs = PLUGIN_VIEW_WARM_TTL_MS,
) {
  const entry = entries.find((item) => item.key === target.key);
  const rest = entries.filter((item) => item.key !== target.key);
  if (!entry) return { entries: rest, entry: undefined, discarded: undefined };
  const matches = entry.workspaceId === target.workspaceId
    && entry.pluginId === target.pluginId
    && entry.viewId === target.viewId
    && now - entry.parkedAt < ttlMs;
  return matches
    ? { entries: rest, entry, discarded: undefined }
    : { entries: rest, entry: undefined, discarded: entry };
}

/** Splits off entries parked `ttlMs` or longer ago. */
export function expirePluginViewWarmEntries(entries: readonly PluginViewWarmEntry[], now: number, ttlMs = PLUGIN_VIEW_WARM_TTL_MS) {
  const expired = entries.filter((entry) => now - entry.parkedAt >= ttlMs);
  if (expired.length === 0) return { entries: [...entries], expired };
  return { entries: entries.filter((entry) => now - entry.parkedAt < ttlMs), expired };
}

/** Milliseconds until the next entry expires, or undefined when the pool is empty. */
export function nextPluginViewWarmExpiry(entries: readonly PluginViewWarmEntry[], now: number, ttlMs = PLUGIN_VIEW_WARM_TTL_MS) {
  if (entries.length === 0) return undefined;
  const oldest = Math.min(...entries.map((entry) => entry.parkedAt));
  return Math.max(0, oldest + ttlMs - now);
}
