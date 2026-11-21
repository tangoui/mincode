interface TerminalSnapshot {
  data: string;
  savedAt: number;
}

const snapshots = new Map<string, TerminalSnapshot>();
const MAX_SNAPSHOT_AGE_MS = 30 * 60 * 1000;
const MAX_SNAPSHOT_ENTRIES = 96;
const MAX_SNAPSHOT_BYTES = 4 * 1024 * 1024;

function snapshotBytes(snapshot: TerminalSnapshot) {
  return snapshot.data.length * 2;
}

function pruneTerminalSnapshots(now = Date.now()) {
  for (const [sessionId, snapshot] of snapshots) {
    if (now - snapshot.savedAt > MAX_SNAPSHOT_AGE_MS) snapshots.delete(sessionId);
  }

  let totalBytes = [...snapshots.values()].reduce(
    (total, snapshot) => total + snapshotBytes(snapshot),
    0,
  );
  while (snapshots.size > MAX_SNAPSHOT_ENTRIES || totalBytes > MAX_SNAPSHOT_BYTES) {
    const oldest = snapshots.entries().next().value as
      | [string, TerminalSnapshot]
      | undefined;
    if (!oldest) break;
    snapshots.delete(oldest[0]);
    totalBytes -= snapshotBytes(oldest[1]);
  }
}

export function saveTerminalSnapshot(sessionId: string, data: string) {
  if (!data) return;
  // 覆盖时重新插入，使 Map 顺序可作为 LRU。
  snapshots.delete(sessionId);
  snapshots.set(sessionId, { data, savedAt: Date.now() });
  pruneTerminalSnapshots();
}

export function readTerminalSnapshot(sessionId: string) {
  pruneTerminalSnapshots();
  const snapshot = snapshots.get(sessionId);
  if (!snapshot) return "";
  snapshots.delete(sessionId);
  snapshots.set(sessionId, snapshot);
  return snapshot.data;
}

export function deleteTerminalSnapshot(sessionId: string) {
  snapshots.delete(sessionId);
}
