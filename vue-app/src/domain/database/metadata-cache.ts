export type DatabaseMetadataKind = "columns" | "primaryKeys" | "indexes" | "foreignKeys" | "triggers" | "ddl";

export interface DatabaseMetadataKey {
  sessionId: string;
  database: string;
  table: string;
  kind: DatabaseMetadataKind;
}

interface DatabaseMetadataSelector {
  sessionId?: string;
  database?: string;
  table?: string;
  kind?: DatabaseMetadataKind;
}

interface CacheEntry {
  value?: unknown;
  expiresAt: number;
  inFlight?: Promise<unknown>;
}

export const createDatabaseMetadataCache = ({
  ttlMs = 60_000,
  now = Date.now,
}: { ttlMs?: number; now?: () => number } = {}) => {
  const entries = new Map<string, CacheEntry>();
  const keyOf = ({ sessionId, database, table, kind }: DatabaseMetadataKey) =>
    JSON.stringify([sessionId, database, table, kind]);

  const get = async <T>(key: DatabaseMetadataKey, load: () => Promise<T>): Promise<T> => {
    const cacheKey = keyOf(key);
    const entry = entries.get(cacheKey);
    if (entry?.value !== undefined && entry.expiresAt > now()) return entry.value as T;
    if (entry?.inFlight) return entry.inFlight as Promise<T>;

    const pendingEntry: CacheEntry = { expiresAt: 0 };
    const inFlight = load().then(
      (value) => {
        if (entries.get(cacheKey) === pendingEntry) {
          entries.set(cacheKey, { value, expiresAt: now() + ttlMs });
        }
        return value;
      },
      (error: unknown) => {
        if (entries.get(cacheKey) === pendingEntry) entries.delete(cacheKey);
        throw error;
      },
    );
    pendingEntry.inFlight = inFlight;
    entries.set(cacheKey, pendingEntry);
    return inFlight;
  };

  const invalidate = (selector: DatabaseMetadataSelector = {}) => {
    for (const [cacheKey] of entries) {
      const [sessionId, database, table, kind] = JSON.parse(cacheKey) as string[];
      if (selector.sessionId !== undefined && selector.sessionId !== sessionId) continue;
      if (selector.database !== undefined && selector.database !== database) continue;
      if (selector.table !== undefined && selector.table !== table) continue;
      if (selector.kind !== undefined && selector.kind !== kind) continue;
      entries.delete(cacheKey);
    }
  };

  return { get, invalidate };
};