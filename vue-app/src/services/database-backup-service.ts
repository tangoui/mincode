/**
 * Scheduled database backups: the IPC boundary for the `db_backup_*`
 * commands and the `db-backup:run` event. Credentials never pass through here;
 * the Go scheduler decrypts saved connections itself.
 */
import {
  BACKUP_RUN_EVENT,
  backupFileBaseName,
  backupJobFromDraft,
  backupNotificationKind,
  parseBackupJobView,
  parseBackupJobViews,
  parseBackupRun,
  parseBackupRunEvent,
  parseBackupRuns,
  type BackupJobDraft,
  type BackupJobView,
  type BackupRun,
  type BackupRunEvent,
} from "@/domain/database/backup-jobs";
import { useVueI18n } from "@/i18n/messages";
import { notifyApp } from "@/services/notifications";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

export async function listBackupJobs(): Promise<BackupJobView[]> {
  if (!isDesktopRuntime()) return [];
  return parseBackupJobViews(await invokeBackend<unknown>("db_backup_list", {}));
}

export async function saveBackupJob(draft: BackupJobDraft): Promise<BackupJobView> {
  const view = parseBackupJobView(await invokeBackend<unknown>("db_backup_save", { job: backupJobFromDraft(draft) }));
  if (!view) throw new Error("db_backup_save: malformed response");
  return view;
}

export async function deleteBackupJob(id: string): Promise<void> {
  await invokeBackend<void>("db_backup_delete", { id });
}

export async function runBackupJobNow(id: string, idempotencyKey: string = crypto.randomUUID()): Promise<BackupRun> {
  let failure: unknown;
  for (let attempt = 0; attempt < 2; attempt++) {
    try {
      const run = parseBackupRun(await invokeBackend<unknown>("db_backup_run_now", { id, idempotencyKey }));
      if (!run) throw new Error("db_backup_run_now: malformed response");
      return run;
    } catch (cause) { failure = cause; }
  }
  throw failure;
}

export async function cancelBackupJob(id: string): Promise<boolean> {
  return (await invokeBackend<unknown>("db_backup_cancel", { id })) === true;
}

export async function listBackupRuns(id: string): Promise<BackupRun[]> {
  return parseBackupRuns(await invokeBackend<unknown>("db_backup_history", { id }));
}

/** Opens the system file manager at a backup file (selected) or folder. */
export async function revealBackupPath(path: string, select = false): Promise<void> {
  await invokeBackend<void>("local_fs_show_in_file_manager", { path, select });
}

/** Subscribes to validated run events; returns the unsubscribe function. */
export function onBackupRunEvent(handler: (event: BackupRunEvent) => void): () => void {
  if (!isDesktopRuntime()) return () => undefined;
  return listenAppEvent<unknown>(BACKUP_RUN_EVENT, (payload) => {
    const event = parseBackupRunEvent(payload);
    if (event) handler(event);
  });
}

/** Entry point for the app-level `db-backup:run` listener, which receives the raw payload. */
export function notifyBackupRunPayload(payload: unknown): void {
  const event = parseBackupRunEvent(payload);
  if (event) notifyBackupRun(event);
}

/** App-level notifications: failures and warnings always, success only when the job asks for it. */
export function notifyBackupRun(event: BackupRunEvent): void {
  const kind = backupNotificationKind(event);
  if (!kind) return;
  const { t } = useVueI18n();
  const name = event.jobName || t("database.backup.untitled");
  const target = { kind: "view", view: "database" } as const;
  if (kind === "failed") {
    notifyApp({
      source: "db",
      level: "error",
      title: t("database.backup.notify.failedTitle", { name }),
      message: event.run.error || t("database.backup.status.failed"),
      target,
    });
    return;
  }
  const file = event.run.file ? backupFileBaseName(event.run.file) : "";
  if (kind === "warning") {
    notifyApp({
      source: "db",
      level: "warning",
      title: t("database.backup.notify.warningTitle", { name }),
      message: t("database.backup.notify.warningMessage", {
        count: event.run.warnings?.length ?? 0,
        warning: event.run.warnings?.[0] ?? "",
      }),
      detail: file || undefined,
      target,
    });
    return;
  }
  notifyApp({
    source: "db",
    level: "success",
    title: t("database.backup.notify.successTitle", { name }),
    message: file || undefined,
    target,
  });
}


/** What a "database" target is for the engine; also picks the object label. */
export type BackupScope = "table" | "collection" | "keys" | "index" | "document" | "measurement" | "graph";
const BACKUP_SCOPES = new Set<BackupScope>(["table", "collection", "keys", "index", "document", "measurement", "graph"]);

export interface BackupFormatCapability {
  format: "sql" | "archive";
  export: boolean;
  constrained: boolean;
  advanced: boolean;
  recovery: string;
  verification: "unverified" | "adapter-contract" | "real-export" | "real-roundtrip";
}
export interface BackupDiscovery {
  /** reason and limitations are stable codes, translated by the dialog. */
  capability: {
    engine: string; supported: boolean; format: "archive"; fidelity: string; reason?: string; limitations: string[];
    version?: 1; formats?: BackupFormatCapability[]; scope: BackupScope; sqlFormat: boolean; defaultFormat?: "sql" | "archive"; recovery?: string; verification?: string;
  };
  schema: { databases: { name: string; tables: { name: string; kind: string }[] }[] };
}
export function parseBackupDiscovery(value: unknown): BackupDiscovery {
  const fail = (): never => { throw new Error("Malformed backup discovery response"); };
  const record = (v: unknown): Record<string, unknown> => {
    if (!v || typeof v !== "object" || Array.isArray(v)) return fail();
    return v as Record<string, unknown>;
  };
  const raw = record(value), capability = record(raw.capability), schema = record(raw.schema);
  if (typeof capability.engine !== "string" || typeof capability.supported !== "boolean" ||
      capability.format !== "archive" || typeof capability.fidelity !== "string" ||
      (capability.reason !== undefined && typeof capability.reason !== "string") ||
      !Array.isArray(capability.limitations) || capability.limitations.some(item => typeof item !== "string") ||
      !BACKUP_SCOPES.has(capability.scope as BackupScope) || typeof capability.sqlFormat !== "boolean" ||
      !Array.isArray(schema.databases)) return fail();
  let formats: BackupFormatCapability[] | undefined;
  if (capability.version !== undefined) {
    if (capability.version !== 1 || !Array.isArray(capability.formats)) return fail();
    formats = capability.formats.map((item): BackupFormatCapability => {
      const f = record(item);
      if ((f.format !== "sql" && f.format !== "archive") || typeof f.export !== "boolean" || typeof f.constrained !== "boolean" || typeof f.advanced !== "boolean" || typeof f.recovery !== "string" || !["unverified", "adapter-contract", "real-export", "real-roundtrip"].includes(String(f.verification))) return fail();
      return { format: f.format, export: f.export, constrained: f.constrained, advanced: f.advanced, recovery: f.recovery, verification: f.verification as BackupFormatCapability["verification"] };
    });
  }
  if (capability.defaultFormat !== undefined && capability.defaultFormat !== "sql" && capability.defaultFormat !== "archive") return fail();
  const databases = schema.databases.map(item => {
    const db = record(item);
    if (typeof db.name !== "string" || !Array.isArray(db.tables)) return fail();
    const tables = db.tables.map(item => {
      const table = record(item);
      if (typeof table.name !== "string" || typeof table.kind !== "string") return fail();
      return { name: table.name, kind: table.kind };
    });
    return { name: db.name, tables };
  });
  return {
    capability: {
      engine: capability.engine, supported: capability.supported, format: "archive", fidelity: capability.fidelity,
      ...(formats ? { version: 1 as const, formats } : {}),
      ...(capability.reason === undefined ? {} : { reason: capability.reason as string }),
      limitations: capability.limitations as string[],
      scope: capability.scope as BackupScope, sqlFormat: capability.sqlFormat as boolean,
      defaultFormat: capability.defaultFormat === "sql" ? "sql" : "archive",
      recovery: typeof capability.recovery === "string" ? capability.recovery : "manual",
      verification: typeof capability.verification === "string" ? capability.verification : "unverified",
    },
    schema: { databases },
  };
}
export async function discoverBackupTargets(connectionId: string, database: string, signal?: AbortSignal): Promise<BackupDiscovery> {
  return parseBackupDiscovery(await invokeBackend<unknown>("db_backup_discover", { connectionId, database }, { signal }));
}
