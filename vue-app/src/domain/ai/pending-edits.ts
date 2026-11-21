export type PendingEditStatus = "pending" | "accepted" | "rejected" | "applied" | "failed";

export interface PendingFileEdit {
  id: string;
  path: string;
  projectRoot: string;
  before: string | null;
  after: string;
  status: PendingEditStatus;
  error?: string;
  createdAt: number;
}

export interface DiffStats {
  additions: number;
  deletions: number;
}

export interface StagePendingEditInput {
  path: string;
  projectRoot: string;
  before: string | null;
  after: string;
}

export function computePendingEditDiff(before: string | null, after: string): DiffStats {
  const previous = (before ?? "").split(/\r?\n/);
  const next = after.split(/\r?\n/);
  const length = Math.max(previous.length, next.length);
  let additions = 0;
  let deletions = 0;
  for (let index = 0; index < length; index += 1) {
    if (previous[index] === next[index]) continue;
    if (previous[index] !== undefined) deletions += 1;
    if (next[index] !== undefined) additions += 1;
  }
  return { additions, deletions };
}

export function createPendingEdit(
  input: StagePendingEditInput,
  createId: () => string,
  now: () => number = Date.now,
): PendingFileEdit {
  return {
    id: createId(),
    path: input.path,
    projectRoot: normalizedProjectRoot(input.projectRoot),
    before: input.before,
    after: input.after,
    status: "pending",
    createdAt: now(),
  };
}

export function normalizedProjectRoot(projectRoot: string) {
  const normalized = projectRoot.trim().replace(/\\/g, "/");
  return normalized.length > 1 ? normalized.replace(/\/+$/, "") : normalized;
}

function sameFile(edit: PendingFileEdit, input: StagePendingEditInput) {
  return normalizedProjectRoot(edit.projectRoot) === normalizedProjectRoot(input.projectRoot)
    && edit.path === input.path;
}

/**
 * Stages an AI write. Only a still-open (pending/accepted) entry is extended so its
 * original `before` is kept; finished entries (rejected/failed) describe stale disk
 * state and are replaced by a fresh entry built from the current `before`.
 */
export function stagePendingEdit(
  edits: PendingFileEdit[],
  input: StagePendingEditInput,
  createId: () => string,
  now: () => number = Date.now,
): { edits: PendingFileEdit[]; edit: PendingFileEdit } {
  const index = edits.findIndex(
    (edit) => sameFile(edit, input) && (edit.status === "pending" || edit.status === "accepted"),
  );
  if (index < 0) {
    const edit = createPendingEdit(input, createId, now);
    return {
      edits: [
        ...edits.filter((item) => !(sameFile(item, input) && (item.status === "rejected" || item.status === "failed"))),
        edit,
      ],
      edit,
    };
  }
  const next = [...edits];
  const edit: PendingFileEdit = {
    ...edits[index],
    after: input.after,
    status: "pending",
    error: undefined,
    createdAt: now(),
  };
  next[index] = edit;
  return { edits: next, edit };
}

export function mergePendingEdit(
  edits: PendingFileEdit[],
  input: StagePendingEditInput,
  createId: () => string,
  now: () => number = Date.now,
): PendingFileEdit[] {
  return stagePendingEdit(edits, input, createId, now).edits;
}

/** Returns actionable AI edits belonging to one project workspace only. */
export function pendingEditsForProjectRoot(
  edits: PendingFileEdit[],
  projectRoot: string,
): PendingFileEdit[] {
  const root = normalizedProjectRoot(projectRoot);
  if (!root) return [];
  return edits.filter(
    (edit) => normalizedProjectRoot(edit.projectRoot) === root
      && (edit.status === "pending" || edit.status === "accepted" || edit.status === "failed"),
  );
}

export function pendingEditStats(edits: PendingFileEdit[]): DiffStats {
  return edits
    .filter((edit) => edit.status === "pending" || edit.status === "accepted" || edit.status === "failed")
    .reduce(
      (total, edit) => {
        const stats = computePendingEditDiff(edit.before, edit.after);
        return {
          additions: total.additions + stats.additions,
          deletions: total.deletions + stats.deletions,
        };
      },
      { additions: 0, deletions: 0 },
    );
}
