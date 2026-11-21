/**
 * Snapshot store retention: after conversations are deleted, each project's
 * private store keeps only the trees the remaining turn records reference, and
 * stays under a size cap by dropping the oldest turns' snapshots first.
 */
import type { TurnSnapshot } from "@/domain/ai/turn-snapshots";

export interface RetainedTurnSnapshot {
  conversationId: string;
  record: TurnSnapshot;
}

export interface ConversationTurnSnapshots {
  conversationId: string;
  records: readonly TurnSnapshot[];
}

const sameTrees = (left: TurnSnapshot, right: TurnSnapshot) =>
  left.userMessageId === right.userMessageId
  && left.projectRoot === right.projectRoot
  && left.before?.tree === right.before?.tree
  && left.after?.tree === right.after?.tree;

/**
 * Every conversation's records from storage and memory.
 * - `union` (pruning): both copies count. Another window may have stored records
 *   this window's copy has not seen, and an unsaved in-memory record is not stored
 *   yet; keeping a tree too many is harmless, dropping a live one is not.
 * - otherwise (overlap detection): the in-memory copy replaces the stored one,
 *   since it reflects rewinds not saved yet.
 */
export function combineTurnSnapshotRecords(
  stored: readonly ConversationTurnSnapshots[],
  memory: readonly ConversationTurnSnapshots[],
  options: { union?: boolean; excludeConversationId?: string } = {},
): RetainedTurnSnapshot[] {
  const byConversation = new Map<string, TurnSnapshot[]>();
  for (const { conversationId, records } of stored) byConversation.set(conversationId, [...records]);
  for (const { conversationId, records } of memory) {
    const existing = options.union ? byConversation.get(conversationId) ?? [] : [];
    const added = records.filter((record) => !existing.some((item) => sameTrees(item, record)));
    byConversation.set(conversationId, [...existing, ...added]);
  }
  if (options.excludeConversationId) byConversation.delete(options.excludeConversationId);
  return [...byConversation].flatMap(([conversationId, records]) => records.map((record) => ({ conversationId, record })));
}

const OBJECT_ID = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/;

function treesOf(record: TurnSnapshot) {
  return [record.before?.tree, record.after?.tree].filter((tree): tree is string => typeof tree === "string" && OBJECT_ID.test(tree));
}

/** Sorted, deduplicated, valid tree IDs the records reference. */
export function snapshotKeepTrees(records: readonly TurnSnapshot[]): string[] {
  return [...new Set(records.flatMap(treesOf))].sort();
}

export interface PruneSnapshotRootInput {
  /** Backend-canonical root; records are matched to it exactly. */
  projectRoot: string;
  /** Records of every remaining conversation (any root). */
  records: readonly RetainedTurnSnapshot[];
  /** Prunes the store down to `keep`; resolves to the store size in bytes afterwards. */
  prune(projectRoot: string, keep: string[]): Promise<number>;
  maxBytes: number;
  /** Called with each batch of dropped records before the store is pruned without them. */
  onEvict?(items: RetainedTurnSnapshot[]): void;
}

/** Per-project store cap (matches the backend's snapshot.MaxStoreBytes). */
export const SNAPSHOT_STORE_MAX_BYTES = 2 ** 30;

/**
 * Prunes one project's store. While it stays over `maxBytes`, the oldest
 * turns (by `startedAt`) leave `keep` – one turn, then twice as many each
 * round, so a long history needs few gc runs – and the store is pruned again.
 * Stops under the cap or once `keep` is empty: the store always keeps the
 * latest capture's objects, so it may never drop below the cap.
 * Returns the records whose snapshots were dropped; the caller marks them
 * `no_snapshot`.
 */
export async function pruneSnapshotRoot(input: PruneSnapshotRootInput): Promise<{ bytes: number; evicted: RetainedTurnSnapshot[] }> {
  const candidates = input.records
    .filter((item) => item.record.projectRoot === input.projectRoot && treesOf(item.record).length > 0)
    .map((item, index) => ({ item, index }))
    .sort((left, right) => (left.item.record.startedAt ?? 0) - (right.item.record.startedAt ?? 0) || left.index - right.index)
    .map(({ item }) => item);
  let keep = snapshotKeepTrees(candidates.map((item) => item.record));
  let bytes = await input.prune(input.projectRoot, keep);
  const evicted: RetainedTurnSnapshot[] = [];
  let batch = 1;
  while (bytes > input.maxBytes && evicted.length < candidates.length) {
    const dropped = candidates.slice(evicted.length, evicted.length + batch);
    evicted.push(...dropped);
    // Marked first: if the prune below fails, the records lose a snapshot that still exists, never the reverse.
    input.onEvict?.(dropped);
    batch *= 2;
    const next = snapshotKeepTrees(candidates.slice(evicted.length).map((item) => item.record));
    // Trees shared with newer turns stay; pruning to the same set frees nothing.
    if (next.length === keep.length && evicted.length < candidates.length) continue;
    keep = next;
    bytes = await input.prune(input.projectRoot, keep);
  }
  return { bytes, evicted };
}

/** The startup sweep prunes a root's store at most this often. */
export const SNAPSHOT_STARTUP_PRUNE_INTERVAL_MS = 24 * 60 * 60 * 1000;

/** Last prune time (ms) per project root, as stored; anything malformed is dropped. */
export function normalizeSnapshotPruneTimes(value: unknown): Record<string, number> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const times: Record<string, number> = {};
  for (const [root, time] of Object.entries(value as Record<string, unknown>)) {
    if (root && typeof time === "number" && Number.isFinite(time) && time >= 0) times[root] = time;
  }
  return times;
}

/**
 * Roots whose store was not pruned within `intervalMs`, deduplicated in input
 * order. A prune time in the future (the clock moved back) counts as due.
 */
export function snapshotRootsDueForPrune(
  roots: Iterable<string>,
  lastPrunedAt: Readonly<Record<string, number>>,
  now: number,
  intervalMs = SNAPSHOT_STARTUP_PRUNE_INTERVAL_MS,
): string[] {
  const due = new Set<string>();
  for (const root of roots) {
    if (!root) continue;
    const last = lastPrunedAt[root];
    if (last !== undefined && last <= now && now - last < intervalMs) continue;
    due.add(root);
  }
  return [...due];
}
