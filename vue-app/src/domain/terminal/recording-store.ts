export interface TerminalRecordingFrame {
  t: number;
  d: string;
}

export interface SavedTerminalRecording {
  id: string;
  name: string;
  frames: TerminalRecordingFrame[];
  createdAt: number;
}

export const TERMINAL_RECORDINGS_LIMIT = 20;
export const LEGACY_TERMINAL_RECORDINGS_KEY = "app_terminal_recordings_v1";
export const TERMINAL_RECORDINGS_INDEX_KEY = "app_terminal_recordings_index_v2";
const TERMINAL_RECORDING_KEY_PREFIX = "app_terminal_recording_v2_";

export function terminalRecordingKey(id: string) {
  return `${TERMINAL_RECORDING_KEY_PREFIX}${id}`;
}

interface TerminalRecordingIndexEntry {
  id: string;
  name: string;
  createdAt: number;
}

export interface TerminalRecordingPersistence {
  load(key: string): Promise<unknown>;
  /** Must apply sets before deletes and should be atomic where the backend supports it. */
  apply(batch: { sets: { key: string; value: unknown }[]; deleteKeys: string[] }): Promise<void>;
}

export interface TerminalRecordingStore {
  /** Loads persisted recordings once; concurrent callers share the same promise. */
  ensureLoaded(): Promise<readonly SavedTerminalRecording[]>;
  add(recording: SavedTerminalRecording): Promise<void>;
  /** Renames a recording, keeping the index and the stored entry in step. */
  rename(id: string, name: string): Promise<void>;
  remove(id: string): Promise<void>;
}

function isRecordingFrame(value: unknown): value is TerminalRecordingFrame {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Record<string, unknown>;
  return typeof candidate.t === "number" && candidate.t >= 0 && typeof candidate.d === "string";
}

function isIndexEntry(value: unknown): value is TerminalRecordingIndexEntry {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate.id === "string" &&
    candidate.id.length > 0 &&
    typeof candidate.name === "string" &&
    typeof candidate.createdAt === "number"
  );
}

export function isSavedTerminalRecording(value: unknown): value is SavedTerminalRecording {
  if (!isIndexEntry(value)) return false;
  const frames = (value as { frames?: unknown }).frames;
  return Array.isArray(frames) && frames.every(isRecordingFrame);
}

function indexEntry({ id, name, createdAt }: SavedTerminalRecording): TerminalRecordingIndexEntry {
  return { id, name, createdAt };
}

function uniqueById<T extends { id: string }>(items: readonly T[]) {
  const seen = new Set<string>();
  return items.filter((item) => !seen.has(item.id) && Boolean(seen.add(item.id)));
}

/**
 * Recordings live under one index key plus one key per recording, so adding or
 * deleting one never re-serializes the others. The legacy single-key array is
 * migrated on first load and stays readable if that migration fails.
 */
export function createTerminalRecordingStore(
  persistence: TerminalRecordingPersistence,
  onChange: (recordings: readonly SavedTerminalRecording[]) => void,
): TerminalRecordingStore {
  let recordings: readonly SavedTerminalRecording[] = [];
  let loading: Promise<readonly SavedTerminalRecording[]> | undefined;
  let writes: Promise<void> = Promise.resolve();

  function publish(next: readonly SavedTerminalRecording[]) {
    recordings = next;
    onChange(next);
  }

  function enqueueWrite(task: () => Promise<void>) {
    const next = writes.then(task);
    writes = next.catch(() => undefined);
    return next;
  }

  async function loadFromIndex(index: unknown[]) {
    const entries = uniqueById(index.filter(isIndexEntry)).slice(0, TERMINAL_RECORDINGS_LIMIT);
    const loaded = await Promise.all(
      entries.map(async (entry) => {
        const value = await persistence.load(terminalRecordingKey(entry.id)).catch(() => null);
        return isSavedTerminalRecording(value) ? { ...entry, frames: value.frames } : null;
      }),
    );
    return loaded.filter((item): item is SavedTerminalRecording => item !== null);
  }

  async function loadLegacy() {
    const legacy = await persistence.load(LEGACY_TERMINAL_RECORDINGS_KEY);
    if (!Array.isArray(legacy)) return [];
    const migrated = uniqueById(legacy.filter(isSavedTerminalRecording)).slice(
      0,
      TERMINAL_RECORDINGS_LIMIT,
    );
    await enqueueWrite(() =>
      persistence.apply({
        sets: [
          ...migrated.map((recording) => ({ key: terminalRecordingKey(recording.id), value: recording })),
          { key: TERMINAL_RECORDINGS_INDEX_KEY, value: migrated.map(indexEntry) },
        ],
        deleteKeys: [LEGACY_TERMINAL_RECORDINGS_KEY],
      }),
    ).catch(() => {
      // 迁移失败时旧键保持原样，下次加载仍从旧键读取。
    });
    return migrated;
  }

  async function load() {
    const index = await persistence.load(TERMINAL_RECORDINGS_INDEX_KEY);
    return Array.isArray(index) ? loadFromIndex(index) : loadLegacy();
  }

  function ensureLoaded() {
    loading ??= load().then(
      (loaded) => {
        publish(loaded);
        return loaded;
      },
      () => {
        loading = undefined;
        return recordings;
      },
    );
    return loading;
  }

  return {
    ensureLoaded,
    async add(recording) {
      await ensureLoaded();
      const next = [recording, ...recordings.filter((item) => item.id !== recording.id)].slice(
        0,
        TERMINAL_RECORDINGS_LIMIT,
      );
      const kept = new Set(next.map((item) => item.id));
      const evicted = recordings.filter((item) => !kept.has(item.id));
      publish(next);
      await enqueueWrite(() =>
        persistence.apply({
          sets: [
            { key: terminalRecordingKey(recording.id), value: recording },
            { key: TERMINAL_RECORDINGS_INDEX_KEY, value: next.map(indexEntry) },
          ],
          deleteKeys: evicted.map((item) => terminalRecordingKey(item.id)),
        }),
      );
    },
    async remove(id) {
      await ensureLoaded();
      if (!recordings.some((item) => item.id === id)) return;
      const next = recordings.filter((item) => item.id !== id);
      publish(next);
      await enqueueWrite(() =>
        persistence.apply({
          sets: [{ key: TERMINAL_RECORDINGS_INDEX_KEY, value: next.map(indexEntry) }],
          deleteKeys: [terminalRecordingKey(id)],
        }),
      );
    },
    async rename(id, name) {
      await ensureLoaded();
      const nextName = name.trim();
      const target = recordings.find((item) => item.id === id);
      // The list is the source of names, so an empty draft or the same name keeps
      // the stored bytes untouched.
      if (!target || !nextName || nextName === target.name) return;
      const renamed = { ...target, name: nextName };
      const next = recordings.map((item) => (item.id === id ? renamed : item));
      publish(next);
      await enqueueWrite(() =>
        persistence.apply({
          sets: [
            // The entry carries a copy of the name; the index is what the list
            // reads, so both are written to keep a rebuild from resurrecting the
            // old one.
            { key: terminalRecordingKey(id), value: renamed },
            { key: TERMINAL_RECORDINGS_INDEX_KEY, value: next.map(indexEntry) },
          ],
          deleteKeys: [],
        }),
      );
    },
  };
}
