/**
 * Per-turn project snapshots for restoring files on rewind.
 *
 * Before the first side-effecting tool call of a user turn the project is
 * captured (`before`), and after the turn ends it is captured again (`after`).
 * Records are kept per (userMessageId, projectRoot): a turn that writes to two
 * allowed roots gets one record for each.
 */
import type { AiCapabilityRisk } from "@/domain/ai/capability-registry";
import type { SnapshotCapture } from "@/services/ai-snapshot-service";

export interface TurnSnapshotPoint {
  tree: string;
  head: string;
  branch: string;
  /** Files over the size limit that the capture left out (only set when non-empty). */
  skipped?: string[];
}

export type TurnSnapshotStatus = "captured" | "no_snapshot" | "unsupported";

export interface TurnSnapshot {
  userMessageId: string;
  projectRoot: string;
  before?: TurnSnapshotPoint;
  after?: TurnSnapshotPoint;
  /** "captured" only when both `before` and `after` exist. */
  status: TurnSnapshotStatus;
  /** When the first side effect asked for the before snapshot (ms since epoch). */
  startedAt?: number;
  /** When the after snapshot finished (ms since epoch). */
  finishedAt?: number;
}

export function isSideEffectRisk(risk: AiCapabilityRisk): boolean {
  return risk !== "read";
}

type Timer = unknown;

export interface TurnSnapshotRecorderDeps {
  capture(root: string): Promise<SnapshotCapture>;
  /** Longest wait for the before snapshot; the tool runs anyway afterwards. */
  timeoutMs: number;
  /** Longest wait for the after snapshot, so a hung backend cannot hold the turn open. */
  finishTimeoutMs?: number;
  setTimer?: (callback: () => void, ms: number) => Timer;
  clearTimer?: (timer: Timer) => void;
  now?: () => number;
}

export interface TurnSnapshotRecorder {
  beforeSideEffect(userMessageId: string, projectRoot: string): Promise<void>;
  finish(userMessageId: string): Promise<TurnSnapshot[]>;
  /** Background baseline when a conversation is bound to a project. Never rejects. */
  warmUp(projectRoot: string): Promise<void>;
  /** Copies of the records of turns not finished yet (including ones taking their after snapshot). */
  inFlight(): TurnSnapshot[];
}

const DEFAULT_FINISH_TIMEOUT_MS = 30_000;
const TIMED_OUT = Symbol("timed-out");

interface PendingTurnSnapshot {
  record: TurnSnapshot;
  /** Settles once the before snapshot is captured, abandoned or failed. */
  ready: Promise<void>;
}

function point(capture: SnapshotCapture): TurnSnapshotPoint {
  return {
    tree: capture.tree,
    head: capture.head,
    branch: capture.branch,
    ...(capture.skipped?.length ? { skipped: [...capture.skipped] } : {}),
  };
}

export function createTurnSnapshotRecorder(deps: TurnSnapshotRecorderDeps): TurnSnapshotRecorder {
  const setTimer = deps.setTimer ?? ((callback: () => void, ms: number) => setTimeout(callback, ms));
  const clearTimer = deps.clearTimer ?? ((timer: Timer) => clearTimeout(timer as ReturnType<typeof setTimeout>));
  const now = deps.now ?? (() => Date.now());
  const finishTimeoutMs = deps.finishTimeoutMs ?? DEFAULT_FINISH_TIMEOUT_MS;
  const turns = new Map<string, Map<string, PendingTurnSnapshot>>();
  const finished = new Set<string>();
  /** Turns whose finish() is taking the after snapshots; still in flight for overlap checks. */
  const finishing = new Map<string, PendingTurnSnapshot[]>();
  /** Roots the backend reported as not snapshottable; kept for this session. */
  const unsupportedRoots = new Set<string>();
  const warmingRoots = new Map<string, Promise<void>>();
  const warmedRoots = new Set<string>();
  /**
   * Requested root → the canonical root the backend resolved it to (absolute,
   * symlinks resolved). Records are keyed by the canonical root, so two spellings
   * of one project share a record and Task 12 groups and prunes them together.
   */
  const canonicalRoots = new Map<string, string>();

  // A trailing separator is the one spelling difference that can be matched without the backend.
  const knownCanonicalRoot = (root: string) => canonicalRoots.get(root) ?? canonicalRoots.get(trimSeparators(root));
  const canonicalRoot = (root: string) => knownCanonicalRoot(root) ?? root;

  /** Learns the canonical root and support status from any capture result, in time or late. */
  function learn(capture: SnapshotCapture, requested: string) {
    const canonical = capture.projectRoot || requested;
    for (const spelling of [requested, trimSeparators(requested), canonical]) canonicalRoots.set(spelling, canonical);
    if (capture.unsupported) {
      unsupportedRoots.add(requested);
      unsupportedRoots.add(canonical);
    }
    return canonical;
  }

  /**
   * Resolves to the capture, or TIMED_OUT; an abandoned capture settles silently.
   * `onResult` sees every successful result, including one that arrives too late.
   */
  function captureWithin(
    root: string,
    ms: number,
    onResult?: (capture: SnapshotCapture) => void,
  ): Promise<SnapshotCapture | typeof TIMED_OUT> {
    return new Promise((resolve, reject) => {
      let settled = false;
      const timer = setTimer(() => {
        if (settled) return;
        settled = true;
        resolve(TIMED_OUT);
      }, ms);
      let capture: Promise<SnapshotCapture>;
      try {
        capture = deps.capture(root);
      } catch (error) {
        capture = Promise.reject(error);
      }
      capture.then(
        (value) => {
          onResult?.(value);
          if (settled) return;
          settled = true;
          clearTimer(timer);
          resolve(value);
        },
        (error: unknown) => {
          if (settled) return;
          settled = true;
          clearTimer(timer);
          reject(error);
        },
      );
    });
  }

  async function captureBefore(record: TurnSnapshot) {
    const requested = record.projectRoot;
    try {
      // A late result never changes the status, but still names the canonical root.
      const result = await captureWithin(requested, deps.timeoutMs, (capture) => {
        const canonical = learn(capture, requested);
        if (!finished.has(record.userMessageId)) record.projectRoot = canonical;
      });
      if (result === TIMED_OUT) {
        record.status = "no_snapshot";
        return;
      }
      if (result.unsupported) record.status = "unsupported";
      else record.before = point(result);
    } catch {
      record.status = "no_snapshot";
    }
  }

  async function captureAfter(record: TurnSnapshot) {
    try {
      const result = await captureWithin(record.projectRoot, finishTimeoutMs, (capture) => learn(capture, record.projectRoot));
      if (result === TIMED_OUT) {
        record.status = "no_snapshot";
        return;
      }
      if (result.unsupported) {
        record.status = "unsupported";
        return;
      }
      record.after = point(result);
      record.status = "captured";
    } catch {
      record.status = "no_snapshot";
    }
  }

  async function finishEntries(entries: PendingTurnSnapshot[]) {
    // A stopped run can end while a tool is still waiting for its before snapshot.
    await Promise.all(entries.map((item) => item.ready));
    // Two spellings of one project that both captured before their canonical root
    // was known: keep the earliest record. Its before predates every later write;
    // if it has no snapshot, the turn has none for that root either.
    const pending: PendingTurnSnapshot[] = [];
    for (const entry of entries) {
      entry.record.projectRoot = canonicalRoot(entry.record.projectRoot);
      const earlier = pending.find((item) => item.record.projectRoot === entry.record.projectRoot);
      if (!earlier) pending.push(entry);
    }
    await Promise.all(pending.map(async ({ record }) => {
      if (record.before && record.status !== "unsupported") await captureAfter(record);
      record.finishedAt = now();
    }));
    return pending.map(({ record }) => ({ ...record }));
  }

  return {
    beforeSideEffect(userMessageId, projectRoot) {
      if (!userMessageId || !projectRoot || finished.has(userMessageId)) return Promise.resolve();
      let roots = turns.get(userMessageId);
      if (!roots) {
        roots = new Map();
        turns.set(userMessageId, roots);
      }
      const known = knownCanonicalRoot(projectRoot);
      const existing = roots.get(projectRoot)
        ?? (known ? [...roots.values()].find((item) => item.record.projectRoot === known) : undefined);
      if (existing) return existing.ready;
      // Until the after snapshot lands the turn cannot be restored.
      const record: TurnSnapshot = { userMessageId, projectRoot: canonicalRoot(projectRoot), status: "no_snapshot", startedAt: now() };
      if (unsupportedRoots.has(projectRoot) || (known !== undefined && unsupportedRoots.has(known))) {
        record.status = "unsupported";
        const ready = Promise.resolve();
        roots.set(projectRoot, { record, ready });
        return ready;
      }
      const ready = captureBefore(record);
      roots.set(projectRoot, { record, ready });
      return ready;
    },

    async finish(userMessageId) {
      finished.add(userMessageId);
      const roots = turns.get(userMessageId);
      turns.delete(userMessageId);
      if (!roots) return [];
      const entries = [...roots.values()];
      finishing.set(userMessageId, entries);
      try {
        return await finishEntries(entries);
      } finally {
        if (finishing.get(userMessageId) === entries) finishing.delete(userMessageId);
      }
    },

    inFlight() {
      const entries = [...[...turns.values()].flatMap((roots) => [...roots.values()]), ...[...finishing.values()].flat()];
      return entries.map(({ record }) => ({ ...record }));
    },

    warmUp(projectRoot) {
      if (!projectRoot || warmedRoots.has(projectRoot) || unsupportedRoots.has(projectRoot)) return Promise.resolve();
      const running = warmingRoots.get(projectRoot);
      if (running) return running;
      const warm = (async () => {
        try {
          learn(await deps.capture(projectRoot), projectRoot);
          warmedRoots.add(projectRoot);
        } catch {
          // A baseline only speeds up the next capture; a failure is retried next time.
        } finally {
          warmingRoots.delete(projectRoot);
        }
      })();
      warmingRoots.set(projectRoot, warm);
      return warm;
    },
  };
}

function trimSeparators(path: string) {
  return path.length > 1 ? path.replace(/[\\/]+$/, "") : path;
}

function containsPath(root: string, path: string) {
  const base = trimSeparators(root);
  const target = trimSeparators(path);
  if (!base) return false;
  return target === base || target.startsWith(`${base}/`) || target.startsWith(`${base}\\`);
}

function owningRoot(path: string, roots: readonly string[]) {
  let best: string | undefined;
  for (const root of roots) {
    if (containsPath(root, path) && (!best || trimSeparators(root).length > trimSeparators(best).length)) best = root;
  }
  return best;
}

/**
 * Which project a side-effecting call writes to:
 * - a resource bound to a project root (workspace, git, SFTP transfer) → that root,
 *   only if it is one of the conversation's projects (a subagent's private
 *   worktree is not);
 * - a command's working directory → the most specific project containing it,
 *   else the conversation's bound project;
 * - anything else (MCP tools, database, browser, ...) → the bound project.
 * Without a bound project nothing is snapshotted.
 */
export function resolveTurnSnapshotRoot(input: {
  resourceRoot?: string;
  cwd?: string;
  boundRoot?: string;
  allowedRoots: readonly string[];
}): string | undefined {
  const boundRoot = input.boundRoot?.trim() || undefined;
  const roots = [...new Set([...(boundRoot ? [boundRoot] : []), ...input.allowedRoots.map((root) => root.trim()).filter(Boolean)])];
  const resourceRoot = input.resourceRoot?.trim();
  if (resourceRoot) return roots.find((root) => trimSeparators(root) === trimSeparators(resourceRoot));
  const cwd = input.cwd?.trim();
  if (cwd) return owningRoot(cwd, roots) ?? boundRoot;
  return boundRoot;
}

const STATUSES = new Set<TurnSnapshotStatus>(["captured", "no_snapshot", "unsupported"]);

function normalizePoint(value: unknown): TurnSnapshotPoint | undefined {
  if (!value || typeof value !== "object") return undefined;
  const candidate = value as Record<string, unknown>;
  if (typeof candidate.tree !== "string" || typeof candidate.head !== "string" || typeof candidate.branch !== "string") return undefined;
  const skipped = Array.isArray(candidate.skipped) ? candidate.skipped.filter((item): item is string => typeof item === "string") : [];
  return { tree: candidate.tree, head: candidate.head, branch: candidate.branch, ...(skipped.length ? { skipped } : {}) };
}

function normalizeRecord(value: unknown): TurnSnapshot | undefined {
  if (!value || typeof value !== "object") return undefined;
  const candidate = value as Record<string, unknown>;
  if (typeof candidate.userMessageId !== "string" || !candidate.userMessageId) return undefined;
  if (typeof candidate.projectRoot !== "string" || !candidate.projectRoot) return undefined;
  if (!STATUSES.has(candidate.status as TurnSnapshotStatus)) return undefined;
  const before = normalizePoint(candidate.before);
  const after = normalizePoint(candidate.after);
  const status = candidate.status === "captured" && !(before && after) ? "no_snapshot" : candidate.status as TurnSnapshotStatus;
  const time = (key: "startedAt" | "finishedAt") =>
    typeof candidate[key] === "number" && Number.isFinite(candidate[key]) ? { [key]: candidate[key] as number } : {};
  return {
    userMessageId: candidate.userMessageId,
    projectRoot: candidate.projectRoot,
    ...(before ? { before } : {}),
    ...(after ? { after } : {}),
    status,
    ...time("startedAt"),
    ...time("finishedAt"),
  };
}

/** JSON with sorted keys, so key order does not count as a difference. */
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value && typeof value === "object") {
    const entries = Object.entries(value).filter(([, item]) => item !== undefined).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    return `{${entries.map(([key, item]) => `${JSON.stringify(key)}:${canonical(item)}`).join(",")}}`;
  }
  return JSON.stringify(value) ?? "null";
}

function sameRecord(left: TurnSnapshot, right: unknown) {
  return canonical(left) === canonical(right);
}

/** Stored records with malformed entries dropped; valid input keeps its identity, missing input becomes []. */
export function normalizeTurnSnapshots(value: unknown): TurnSnapshot[] {
  if (!Array.isArray(value)) return [];
  const normalized = value.map(normalizeRecord);
  const unchanged = normalized.every((record, index) => record && sameRecord(record, value[index]));
  return unchanged ? value as TurnSnapshot[] : normalized.filter((record): record is TurnSnapshot => Boolean(record));
}

const recordKey = (record: TurnSnapshot) => `${record.userMessageId}\u0000${record.projectRoot}`;

/** Adds records, replacing any earlier one for the same turn and root. */
export function mergeTurnSnapshots(existing: readonly TurnSnapshot[], added: readonly TurnSnapshot[]): TurnSnapshot[] {
  const replaced = new Map(added.map((record) => [recordKey(record), record]));
  const merged = existing.map((record) => {
    const next = replaced.get(recordKey(record));
    if (next) replaced.delete(recordKey(record));
    return next ?? record;
  });
  return [...merged, ...replaced.values()];
}

/** Records of the user messages still in the conversation (used after a rewind). */
export function retainTurnSnapshots(records: readonly TurnSnapshot[], userMessageIds: ReadonlySet<string>): TurnSnapshot[] {
  return records.filter((record) => userMessageIds.has(record.userMessageId));
}
