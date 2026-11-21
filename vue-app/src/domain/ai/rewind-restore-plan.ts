/**
 * What rewinding a conversation can do to the project files the rewound agent
 * turns changed (see the "回退恢复文件" section of the agent workflow spec).
 *
 * The rewound turns' snapshot records give a `target` (the earliest turn's
 * before tree) and an `expected` tree (the last turn's after tree). Candidate
 * files are the `target → expected` diff; files somebody else may have changed
 * in the meantime are conflicts and are never touched. The backend re-checks
 * each file against `expected` right before writing it.
 */
import type { TurnSnapshot } from "@/domain/ai/turn-snapshots";
import type { SnapshotFileChange } from "@/services/ai-snapshot-service";

export type RewindRestoreBlock = "unsupported" | "no_snapshot" | "git_state_changed";

export interface RewindRestorePlan {
  projectRoot: string;
  /** Tree the files are restored from: before the earliest rewound turn. */
  target: string;
  /** Tree the files must still match: after the last rewound turn. */
  expected: string;
  /** Files written back from `target`. */
  restore: string[];
  /** Files the turns created; deleted. */
  remove: string[];
  /** Files changed outside these turns; left alone. */
  conflicts: string[];
  /** Nested repositories and files over the snapshot size limit; never restored. */
  uncovered: string[];
  /** Another conversation's turn on this root ran during these turns. */
  overlapped: boolean;
  blocked?: RewindRestoreBlock;
}

export interface RewindRestoreDiffs {
  /** Index N → files changed between turn N's after and turn N+1's before snapshot. */
  between: Map<number, string[]>;
  /** The `target → expected` diff. */
  changed: SnapshotFileChange[];
  /** Files other conversations' turns on this root changed during these turns. */
  overlappingPaths?: string[];
}

const time = (record: TurnSnapshot) => record.startedAt ?? record.finishedAt ?? 0;

/** Oldest first; records without times keep their relative order. */
export function sortTurnSnapshots(turns: readonly TurnSnapshot[]): TurnSnapshot[] {
  return turns.map((record, index) => ({ record, index }))
    .sort((left, right) => time(left.record) - time(right.record) || left.index - right.index)
    .map(({ record }) => record);
}

/** Records grouped by their (backend-canonical) project root, in first-seen order. */
export function groupTurnSnapshotsByRoot(records: readonly TurnSnapshot[]): Array<{ projectRoot: string; turns: TurnSnapshot[] }> {
  const groups = new Map<string, TurnSnapshot[]>();
  for (const record of records) {
    const group = groups.get(record.projectRoot);
    if (group) group.push(record);
    else groups.set(record.projectRoot, [record]);
  }
  return [...groups].map(([projectRoot, turns]) => ({ projectRoot, turns: sortTurnSnapshots(turns) }));
}

function restoreBlock(turns: readonly TurnSnapshot[]): RewindRestoreBlock | undefined {
  if (turns.some((turn) => turn.status === "unsupported")) return "unsupported";
  if (!turns.length || turns.some((turn) => turn.status !== "captured" || !turn.before || !turn.after)) return "no_snapshot";
  const first = turns[0].before!;
  const points = turns.flatMap((turn) => [turn.before!, turn.after!]);
  if (points.some((point) => point.head !== first.head || point.branch !== first.branch)) return "git_state_changed";
  return undefined;
}

function unique(paths: Iterable<string>) {
  return [...new Set(paths)];
}

export function planRewindRestore(turns: TurnSnapshot[], diffs: RewindRestoreDiffs): RewindRestorePlan {
  const ordered = sortTurnSnapshots(turns);
  const first = ordered[0];
  const last = ordered[ordered.length - 1];
  const plan: RewindRestorePlan = {
    projectRoot: first?.projectRoot ?? "",
    target: first?.before?.tree ?? "",
    expected: last?.after?.tree ?? "",
    restore: [],
    remove: [],
    conflicts: [],
    uncovered: [],
    overlapped: false,
  };
  const blocked = restoreBlock(ordered);
  if (blocked) return { ...plan, blocked };

  const overlapping = new Set(diffs.overlappingPaths ?? []);
  const between = new Set([...diffs.between.values()].flat());
  // A file over the size limit is missing from that tree, so the diff reports it
  // as added or deleted; restoring or removing it would act on a guess.
  const skipped = new Set([...(first.before!.skipped ?? []), ...(last.after!.skipped ?? [])]);
  const seen = new Set<string>();
  for (const change of diffs.changed) {
    if (seen.has(change.path)) continue;
    seen.add(change.path);
    if (change.status === "submodule" || skipped.has(change.path)) plan.uncovered.push(change.path);
    else if (between.has(change.path) || overlapping.has(change.path)) plan.conflicts.push(change.path);
    else if (change.status === "added") plan.remove.push(change.path);
    else plan.restore.push(change.path);
  }
  plan.overlapped = overlapping.size > 0;
  return plan;
}

const spanOf = (turns: readonly TurnSnapshot[]) => {
  const starts = turns.flatMap((turn) => (turn.startedAt === undefined ? [] : [turn.startedAt]));
  const ends = turns.flatMap((turn) => [turn.finishedAt, turn.startedAt].filter((value): value is number => value !== undefined));
  return starts.length && ends.length ? { start: Math.min(...starts), end: Math.max(...ends) } : undefined;
};

/**
 * Other conversations' turns on the same root whose [startedAt, finishedAt]
 * intersects the rewound turns' span. A turn without a finish time may still
 * have been running, so only its start counts.
 */
export function overlappingTurnSnapshots(rewound: readonly TurnSnapshot[], others: readonly TurnSnapshot[]): TurnSnapshot[] {
  const results: TurnSnapshot[] = [];
  for (const { projectRoot, turns } of groupTurnSnapshotsByRoot(rewound)) {
    const span = spanOf(turns);
    if (!span) continue;
    for (const other of others) {
      if (other.projectRoot !== projectRoot || other.startedAt === undefined) continue;
      const end = other.finishedAt ?? Number.POSITIVE_INFINITY;
      if (other.startedAt <= span.end && end >= span.start) results.push(other);
    }
  }
  return results;
}

/** A run whose snapshot records this window cannot see (another window, or an ACP agent). */
export interface UntrackedRun {
  /** The conversation's bound project root, when known. */
  projectRoot?: string;
  /** When the run started (ms); unknown counts as "before every turn". */
  startedAt?: number;
}

const normalizedRoot = (root: string) => (root.length > 1 ? root.replace(/[\\/]+$/, "") : root);
const isWithin = (parent: string, child: string) =>
  child === parent || ["/", "\\"].some((separator) => child.startsWith(parent.endsWith(separator) ? parent : parent + separator));

/**
 * Stand-in records for runs still in progress whose changes nobody has
 * recorded: one per rewound root the run may write to (same, nested or
 * enclosing root, or an unknown one), still running, so every candidate file
 * of an overlapping turn becomes a conflict. Roots are compared as spelled;
 * two spellings of one directory through a symlink are not matched.
 */
export function untrackedRunTurnSnapshots(rewoundRoots: readonly string[], runs: readonly UntrackedRun[]): TurnSnapshot[] {
  const records: TurnSnapshot[] = [];
  for (const run of runs) {
    const runRoot = run.projectRoot?.trim() ? normalizedRoot(run.projectRoot.trim()) : undefined;
    for (const projectRoot of new Set(rewoundRoots)) {
      const root = normalizedRoot(projectRoot);
      if (runRoot !== undefined && !isWithin(root, runRoot) && !isWithin(runRoot, root)) continue;
      records.push({ userMessageId: "", projectRoot, status: "no_snapshot", startedAt: run.startedAt ?? 0 });
    }
  }
  return records;
}

export type SnapshotDiff = (projectRoot: string, from: string, to: string) => Promise<SnapshotFileChange[]>;

const paths = (changes: readonly SnapshotFileChange[]) => changes.map((change) => change.path);

function isUnsupported(error: unknown) {
  return error instanceof Error && error.name === "SnapshotUnsupportedError";
}

async function planRoot(turns: TurnSnapshot[], others: readonly TurnSnapshot[], diff: SnapshotDiff): Promise<RewindRestorePlan> {
  const empty = planRewindRestore(turns, { between: new Map(), changed: [], overlappingPaths: [] });
  if (empty.blocked) return empty;
  const root = empty.projectRoot;
  const between = new Map<number, string[]>();
  try {
    const changed = empty.target === empty.expected ? [] : await diff(root, empty.target, empty.expected);
    for (let index = 0; index + 1 < turns.length; index += 1) {
      const from = turns[index].after!.tree;
      const to = turns[index + 1].before!.tree;
      if (from !== to) between.set(index, paths(await diff(root, from, to)));
    }
    const overlappingPaths: string[] = [];
    for (const other of overlappingTurnSnapshots(turns, others)) {
      // Without both snapshots nobody knows which files that run changed: assume all of them.
      if (other.status !== "captured" || !other.before || !other.after) overlappingPaths.push(...paths(changed));
      else if (other.before.tree !== other.after.tree) overlappingPaths.push(...paths(await diff(root, other.before.tree, other.after.tree)));
    }
    return planRewindRestore(turns, { between, changed, overlappingPaths: unique(overlappingPaths) });
  } catch (error) {
    // A tree evicted by the size cap or a backend failure: nothing to restore from.
    return { ...empty, blocked: isUnsupported(error) ? "unsupported" : "no_snapshot" };
  }
}

/** One plan per project root the rewound turns' records name. */
export async function buildRewindRestorePlans(input: {
  rewound: readonly TurnSnapshot[];
  /** Records of every other conversation (used to detect overlapping runs). */
  others: readonly TurnSnapshot[];
  diff: SnapshotDiff;
}): Promise<RewindRestorePlan[]> {
  const plans: RewindRestorePlan[] = [];
  for (const { turns } of groupTurnSnapshotsByRoot(input.rewound)) plans.push(await planRoot(turns, input.others, input.diff));
  return plans;
}

const changesFiles = (plan: RewindRestorePlan) => plan.restore.length > 0 || plan.remove.length > 0;

/** Plans with files to write back or delete. */
export function restorablePlans(plans: readonly RewindRestorePlan[]): RewindRestorePlan[] {
  return plans.filter((plan) => !plan.blocked && changesFiles(plan));
}

/** The dialog is shown when a root is blocked or any file differs; otherwise the rewind is plain. */
export function shouldOfferRewindRestore(plans: readonly RewindRestorePlan[]): boolean {
  return plans.some((plan) => plan.blocked || changesFiles(plan) || plan.conflicts.length > 0 || plan.uncovered.length > 0);
}
