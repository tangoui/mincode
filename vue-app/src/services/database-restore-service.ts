/**
 * Restoring a backup into a database: the IPC boundary for the
 * db_backup_restore_* commands and the db-backup:restore event. Restores
 * always write to the target, so they run only after the user confirms the
 * plan; payloads are validated before reaching the UI.
 */
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

export const BACKUP_RESTORE_EVENT = "db-backup:restore";

export interface RestoreMapping { database: string; object: string; targetDatabase: string; targetObject: string; }
export interface RestorePlan {
  planId?: string;
  expiresAt?: number;
  mode?: "constrained" | "advanced";
  format: "sql" | "archive";
  engine?: string;
  supported: boolean;
  reason?: string;
  objects: { database: string; object: string; records: number }[];
  conflicts: string[];
  statements: number;
  destructive: boolean;
  sizeBytes: number;
  warnings: string[];
}

export interface RestoreOptions {
  /** Write into objects that already exist (rows are added, nothing dropped). */
  allowExisting: boolean;
  /** Let a SQL script run its own DROP / TRUNCATE / DELETE statements. */
  allowDestructive: boolean;
}

export interface RestoreResult {
  statements: number;
  objects: number;
  records: number;
  durationMs: number;
  warnings: string[];
}

export interface RestoreEvent {
  restoreId: string;
  object?: string;
  statements: number;
  records: number;
  bytesRead: number;
  totalBytes: number;
  done: boolean;
  result?: RestoreResult;
  error?: string;
}

type Raw = Record<string, unknown>;
const fail = (what: string): never => { throw new Error(`Malformed ${what}`); };
const record = (v: unknown, what: string): Raw =>
  v && typeof v === "object" && !Array.isArray(v) ? (v as Raw) : fail(what);
const count = (v: unknown) => (typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : 0);
const strings = (v: unknown) => (Array.isArray(v) ? v.filter((s): s is string => typeof s === "string") : []);

export function parseRestorePlan(value: unknown): RestorePlan {
  const raw = record(value, "restore plan");
  if ((raw.format !== "sql" && raw.format !== "archive") || typeof raw.supported !== "boolean" || !Array.isArray(raw.objects)) {
    return fail("restore plan");
  }
  return {
    format: raw.format,
    ...(typeof raw.planId === "string" ? { planId: raw.planId } : {}),
    ...(typeof raw.expiresAt === "number" ? { expiresAt: raw.expiresAt } : {}),
    ...(raw.mode === "constrained" || raw.mode === "advanced" ? { mode: raw.mode } : {}),
    ...(typeof raw.engine === "string" && raw.engine ? { engine: raw.engine } : {}),
    supported: raw.supported,
    ...(typeof raw.reason === "string" && raw.reason ? { reason: raw.reason } : {}),
    objects: raw.objects.map((item) => {
      const o = record(item, "restore plan object");
      return { database: typeof o.database === "string" ? o.database : "", object: typeof o.object === "string" ? o.object : "", records: count(o.records) };
    }),
    conflicts: strings(raw.conflicts),
    statements: count(raw.statements),
    destructive: raw.destructive === true,
    sizeBytes: count(raw.sizeBytes),
    warnings: strings(raw.warnings),
  };
}

function parseRestoreResult(value: unknown): RestoreResult | undefined {
  if (!value || typeof value !== "object") return undefined;
  const raw = value as Raw;
  return {
    statements: count(raw.statements), objects: count(raw.objects), records: count(raw.records),
    durationMs: count(raw.durationMs), warnings: strings(raw.warnings),
  };
}

export function parseRestoreEvent(value: unknown): RestoreEvent | undefined {
  if (!value || typeof value !== "object") return undefined;
  const raw = value as Raw;
  if (typeof raw.restoreId !== "string" || !raw.restoreId) return undefined;
  const result = parseRestoreResult(raw.result);
  return {
    restoreId: raw.restoreId,
    ...(typeof raw.object === "string" && raw.object ? { object: raw.object } : {}),
    statements: count(raw.statements), records: count(raw.records),
    bytesRead: count(raw.bytesRead), totalBytes: count(raw.totalBytes),
    done: raw.done === true,
    ...(result ? { result } : {}),
    ...(typeof raw.error === "string" && raw.error ? { error: raw.error } : {}),
  };
}

export async function planRestore(connectionId: string, path: string, signal?: AbortSignal, mode: "constrained" | "advanced" = "constrained", mappings: RestoreMapping[] = [], allowExisting = false, workspaceId = "", sourceDialect = ""): Promise<RestorePlan> {
  return parseRestorePlan(await invokeBackend<unknown>("db_backup_restore_plan", { connectionId, path, mode, mappings, allowExisting, workspaceId, sourceDialect }, { signal }));
}
export async function startRestore(planId: string, idempotencyKey: string, options: RestoreOptions): Promise<string> {
  const raw = record(await invokeBackend<unknown>("db_backup_restore_start", { planId, idempotencyKey, allowExisting: options.allowExisting, unrestrictedRiskAccepted: options.allowDestructive }), "restore start");
  return typeof raw.restoreId === "string" && raw.restoreId ? raw.restoreId : fail("restore start");
}

export async function cancelRestore(restoreId: string): Promise<boolean> {
  return (await invokeBackend<unknown>("db_backup_restore_cancel", { restoreId })) === true;
}

/** Subscribes to validated restore events; returns the unsubscribe function. */
export function onRestoreEvent(handler: (event: RestoreEvent) => void): () => void {
  if (!isDesktopRuntime()) return () => undefined;
  return listenAppEvent<unknown>(BACKUP_RESTORE_EVENT, (payload) => {
    const event = parseRestoreEvent(payload);
    if (event) handler(event);
  });
}
