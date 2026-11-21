/**
 * Front-end wrapper for the private project snapshot store (backend/snapshot).
 * Snapshots exist only in the desktop app; in a browser preview every call says
 * so instead of pretending to succeed.
 */
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

/** Why a project cannot be snapshotted: the backend reports "not_git"; a browser preview reports "browser". */
export type SnapshotUnsupportedReason = "browser" | "not_git" | (string & {});

export interface SnapshotCapture {
  /**
   * The canonical project root the backend captured (absolute, symlinks resolved);
   * the requested root when the backend was not asked. Optional for test doubles.
   */
  projectRoot?: string;
  tree: string;
  head: string;
  branch: string;
  /** Files left out because they exceed the size limit (10 MiB). */
  skipped: string[];
  /** Set when nothing was captured; the other fields are then empty. */
  unsupported?: SnapshotUnsupportedReason;
}

export type SnapshotFileStatus = "added" | "modified" | "deleted" | "submodule";

export interface SnapshotFileChange {
  path: string;
  status: SnapshotFileStatus;
}

export interface SnapshotRestoreRequest {
  projectRoot: string;
  /** Tree whose contents are written back. */
  target: string;
  /** Tree the files must still match; anything else is a conflict and is left alone. */
  expected: string;
  paths: string[];
}

export interface SnapshotRestoreResult {
  restored: string[];
  deleted: string[];
  conflicts: string[];
  unsupported?: SnapshotUnsupportedReason;
}

/** Thrown by calls whose result has no "unsupported" shape (diff, prune, size). */
export class SnapshotUnsupportedError extends Error {
  readonly reason: SnapshotUnsupportedReason;
  constructor(reason: SnapshotUnsupportedReason) {
    super(`ai_snapshot_unsupported:${reason}`);
    this.name = "SnapshotUnsupportedError";
    this.reason = reason;
  }
}

function strings(value: unknown) {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function text(value: unknown) {
  return typeof value === "string" ? value : "";
}

function requireDesktop() {
  if (!isDesktopRuntime()) throw new SnapshotUnsupportedError("browser");
}

export async function captureProjectSnapshot(projectRoot: string): Promise<SnapshotCapture> {
  if (!isDesktopRuntime()) return { projectRoot, tree: "", head: "", branch: "", skipped: [], unsupported: "browser" };
  const raw = await invokeBackend<Partial<Record<keyof SnapshotCapture, unknown>> | null>("ai_snapshot_capture", { projectRoot });
  const unsupported = text(raw?.unsupported);
  return {
    projectRoot: text(raw?.projectRoot) || projectRoot,
    tree: text(raw?.tree),
    head: text(raw?.head),
    branch: text(raw?.branch),
    skipped: strings(raw?.skipped),
    ...(unsupported ? { unsupported } : {}),
  };
}

export async function diffProjectSnapshots(projectRoot: string, from: string, to: string): Promise<SnapshotFileChange[]> {
  requireDesktop();
  const raw = await invokeBackend<{ changes?: unknown } | null>("ai_snapshot_diff", { projectRoot, from, to });
  if (!Array.isArray(raw?.changes)) return [];
  return raw.changes.flatMap((change: { path?: unknown; status?: unknown }) =>
    typeof change?.path === "string" && typeof change.status === "string"
      ? [{ path: change.path, status: change.status as SnapshotFileStatus }]
      : []);
}

export async function restoreProjectSnapshot(request: SnapshotRestoreRequest): Promise<SnapshotRestoreResult> {
  if (!isDesktopRuntime()) return { restored: [], deleted: [], conflicts: [], unsupported: "browser" };
  const raw = await invokeBackend<Partial<Record<keyof SnapshotRestoreResult, unknown>> | null>("ai_snapshot_restore", {
    projectRoot: request.projectRoot,
    target: request.target,
    expected: request.expected,
    paths: request.paths,
  });
  return { restored: strings(raw?.restored), deleted: strings(raw?.deleted), conflicts: strings(raw?.conflicts) };
}

/** Drops every snapshot object not reachable from `keep`; resolves to the store size in bytes afterwards. */
export async function pruneProjectSnapshots(projectRoot: string, keep: string[]): Promise<number> {
  requireDesktop();
  const raw = await invokeBackend<{ bytes?: unknown } | null>("ai_snapshot_prune", { projectRoot, keep });
  return typeof raw?.bytes === "number" ? raw.bytes : 0;
}

export async function projectSnapshotSize(projectRoot: string): Promise<number> {
  requireDesktop();
  const raw = await invokeBackend<{ bytes?: unknown } | null>("ai_snapshot_size", { projectRoot });
  return typeof raw?.bytes === "number" ? raw.bytes : 0;
}
