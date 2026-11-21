/**
 * Scheduled database backup jobs: the frontend model for the `db_backup_*`
 * commands (backend/db/backup_*.go). The Go scheduler owns timing,
 * credentials and dump files; this module only validates, normalizes and
 * describes jobs. Native payloads are parsed defensively before reaching UI.
 */
import type { DatabaseConnection } from "@/domain/database/types";

export type BackupScheduleKind = "interval" | "daily" | "weekly";
export type BackupRunTrigger = "schedule" | "manual" | "catchup";
export type BackupRunStatus = "running" | "success" | "failed" | "cancelled";

export interface BackupTarget {
  database: string;
  /** Empty or missing means every table in the database. */
  tables?: string[];
}

export interface BackupSchedule {
  kind: BackupScheduleKind;
  intervalHours?: number;
  /** HH:MM in the machine's local time. */
  time?: string;
  /** 0 = Sunday … 6 = Saturday. */
  weekdays?: number[];
}

export interface BackupRetention {
  keepLast: number;
  keepDays: number;
}

export interface BackupJobState {
  lastScheduledAt?: number;
  lastRunAt?: number;
  lastStatus?: BackupRunStatus;
}

export interface BackupJob {
  format?: "sql" | "archive";
  id: string;
  name: string;
  connectionId: string;
  targets: BackupTarget[];
  schedule: BackupSchedule;
  destination: string;
  fileNamePrefix: string;
  gzip: boolean;
  retention: BackupRetention;
  enabled: boolean;
  notifyOnSuccess: boolean;
  createdAt: number;
  updatedAt: number;
  state: BackupJobState;
}

export interface BackupRun {
  id: string;
  jobId: string;
  trigger: BackupRunTrigger;
  status: BackupRunStatus;
  startedAt: number;
  finishedAt?: number;
  durationMs?: number;
  file?: string;
  sizeBytes?: number;
  tables?: number;
  rows?: number;
  removed?: number;
  error?: string;
  warnings?: string[];
}

export interface BackupJobView extends BackupJob {
  nextRunAt?: number;
  running: boolean;
  lastRun?: BackupRun;
}

export interface BackupRunEvent {
  jobId: string;
  jobName: string;
  connectionId: string;
  notifyOnSuccess: boolean;
  run: BackupRun;
}

/** Editable form state; `tables` applies to the single selected database (empty = all tables). */
export interface BackupJobDraft {
  format?: "sql" | "archive";
  id: string;
  name: string;
  connectionId: string;
  databases: string[];
  tables: string[];
  scheduleKind: BackupScheduleKind;
  intervalHours: number;
  time: string;
  weekdays: number[];
  destination: string;
  fileNamePrefix: string;
  gzip: boolean;
  keepLast: number;
  keepDays: number;
  enabled: boolean;
  notifyOnSuccess: boolean;
}

export type BackupDraftError =
  | "name"
  | "connection"
  | "databases"
  | "tablesNeedOneDatabase"
  | "interval"
  | "time"
  | "weekdays"
  | "destination"
  | "retention";

export const BACKUP_MAX_INTERVAL_HOURS = 168;
export const BACKUP_MAX_KEEP_LAST = 10000;
export const BACKUP_MAX_KEEP_DAYS = 3650;
export { BACKUP_RUN_EVENT } from "@/domain/database/backup-events";

const TIME_PATTERN = /^([01]\d|2[0-3]):([0-5]\d)$/;
const SCHEDULE_KINDS = new Set<BackupScheduleKind>(["interval", "daily", "weekly"]);
const TRIGGERS = new Set<BackupRunTrigger>(["schedule", "manual", "catchup"]);
const STATUSES = new Set<BackupRunStatus>(["running", "success", "failed", "cancelled"]);

/** All saved connections can be inspected; native discovery decides actual capability. */
export function isBackupCandidateConnection(connection: Pick<DatabaseConnection, "dbType" | "driver">): boolean {
  return Boolean(connection.dbType);
}

export function createBackupJobDraft(connectionId = "", database = ""): BackupJobDraft {
  return {
    id: "",
    format: "sql",
    name: "",
    connectionId,
    databases: database ? [database] : [],
    tables: [],
    scheduleKind: "daily",
    intervalHours: 24,
    time: "02:00",
    weekdays: [1],
    destination: "",
    fileNamePrefix: "",
    gzip: true,
    keepLast: 7,
    keepDays: 0,
    enabled: true,
    notifyOnSuccess: false,
  };
}

function isAbsolutePath(path: string): boolean {
  return path.startsWith("/") || /^[A-Za-z]:[\\/]/.test(path) || path.startsWith("\\\\");
}

function isWholeNumberIn(value: number, min: number, max: number): boolean {
  return Number.isInteger(value) && value >= min && value <= max;
}

export function validateBackupJobDraft(draft: BackupJobDraft): BackupDraftError[] {
  const errors: BackupDraftError[] = [];
  if (!draft.name.trim()) errors.push("name");
  if (!draft.connectionId) errors.push("connection");
  const databases = uniqueNonEmpty(draft.databases);
  if (!databases.length) errors.push("databases");
  if (uniqueNonEmpty(draft.tables).length && databases.length !== 1) errors.push("tablesNeedOneDatabase");
  if (draft.scheduleKind === "interval") {
    if (!isWholeNumberIn(draft.intervalHours, 1, BACKUP_MAX_INTERVAL_HOURS)) errors.push("interval");
  } else if (!TIME_PATTERN.test(draft.time)) {
    errors.push("time");
  }
  if (draft.scheduleKind === "weekly" && !normalizeWeekdays(draft.weekdays).length) errors.push("weekdays");
  if (!isAbsolutePath(draft.destination.trim())) errors.push("destination");
  if (
    !isWholeNumberIn(draft.keepLast, 0, BACKUP_MAX_KEEP_LAST) ||
    !isWholeNumberIn(draft.keepDays, 0, BACKUP_MAX_KEEP_DAYS)
  ) {
    errors.push("retention");
  }
  return errors;
}

function uniqueNonEmpty(values: string[]): string[] {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))];
}

function normalizeWeekdays(values: number[]): number[] {
  return [...new Set(values.filter((day) => Number.isInteger(day) && day >= 0 && day <= 6))].sort((a, b) => a - b);
}

/** Payload for `db_backup_save`; state and timestamps are owned by the backend. */
export function backupJobFromDraft(draft: BackupJobDraft): Omit<BackupJob, "createdAt" | "updatedAt" | "state"> {
  const tables = uniqueNonEmpty(draft.tables);
  const databases = uniqueNonEmpty(draft.databases);
  const schedule: BackupSchedule =
    draft.scheduleKind === "interval"
      ? { kind: "interval", intervalHours: draft.intervalHours }
      : draft.scheduleKind === "daily"
        ? { kind: "daily", time: draft.time }
        : { kind: "weekly", time: draft.time, weekdays: normalizeWeekdays(draft.weekdays) };
  return {
    id: draft.id,
    ...(draft.format ? { format: draft.format } : {}),
    name: draft.name.trim(),
    connectionId: draft.connectionId,
    targets: databases.map((database) => (tables.length ? { database, tables } : { database })),
    schedule,
    destination: draft.destination.trim(),
    fileNamePrefix: draft.fileNamePrefix.trim(),
    gzip: draft.gzip,
    retention: { keepLast: draft.keepLast, keepDays: draft.keepDays },
    enabled: draft.enabled,
    notifyOnSuccess: draft.notifyOnSuccess,
  };
}

export function draftFromBackupJob(job: BackupJob): BackupJobDraft {
  const draft = createBackupJobDraft(job.connectionId);
  const tables = job.targets.find((target) => target.tables?.length)?.tables ?? [];
  return {
    ...draft,
    id: job.id,
    format: job.format || "sql",
    name: job.name,
    databases: job.targets.map((target) => target.database),
    tables: [...tables],
    scheduleKind: job.schedule.kind,
    intervalHours: job.schedule.intervalHours ?? draft.intervalHours,
    time: job.schedule.time ?? draft.time,
    weekdays: job.schedule.weekdays?.length ? [...job.schedule.weekdays] : draft.weekdays,
    destination: job.destination,
    fileNamePrefix: job.fileNamePrefix,
    gzip: job.gzip,
    keepLast: job.retention.keepLast,
    keepDays: job.retention.keepDays,
    enabled: job.enabled,
    notifyOnSuccess: job.notifyOnSuccess,
  };
}

// ── Native payload parsing ──

type UnknownRecord = Record<string, unknown>;

function asRecord(value: unknown): UnknownRecord | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as UnknownRecord) : undefined;
}

function text(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function count(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : undefined;
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function parseSchedule(value: unknown): BackupSchedule | undefined {
  const record = asRecord(value);
  const kind = text(record?.kind) as BackupScheduleKind;
  if (!record || !SCHEDULE_KINDS.has(kind)) return undefined;
  const schedule: BackupSchedule = { kind };
  const hours = count(record.intervalHours);
  if (hours !== undefined) schedule.intervalHours = hours;
  if (typeof record.time === "string") schedule.time = record.time;
  if (Array.isArray(record.weekdays)) schedule.weekdays = normalizeWeekdays(record.weekdays.filter((day): day is number => typeof day === "number"));
  return schedule;
}

export function parseBackupRun(value: unknown): BackupRun | undefined {
  const record = asRecord(value);
  if (!record) return undefined;
  const id = text(record.id);
  const jobId = text(record.jobId);
  const trigger = text(record.trigger) as BackupRunTrigger;
  const status = text(record.status) as BackupRunStatus;
  const startedAt = count(record.startedAt);
  if (!id || !jobId || !TRIGGERS.has(trigger) || !STATUSES.has(status) || startedAt === undefined) return undefined;
  const run: BackupRun = { id, jobId, trigger, status, startedAt };
  for (const key of ["finishedAt", "durationMs", "sizeBytes", "tables", "rows", "removed"] as const) {
    const number = count(record[key]);
    if (number !== undefined) run[key] = number;
  }
  if (text(record.file)) run.file = text(record.file);
  if (text(record.error)) run.error = text(record.error);
  const warnings = stringList(record.warnings);
  if (warnings.length) run.warnings = warnings;
  return run;
}

export function parseBackupJobView(value: unknown): BackupJobView | undefined {
  const record = asRecord(value);
  if (!record) return undefined;
  const id = text(record.id);
  const connectionId = text(record.connectionId);
  const schedule = parseSchedule(record.schedule);
  if (!id || !connectionId || !schedule) return undefined;
  const targets: BackupTarget[] = [];
  for (const item of Array.isArray(record.targets) ? record.targets : []) {
    const target = asRecord(item);
    const database = text(target?.database);
    if (!database) continue;
    const tables = stringList(target?.tables);
    targets.push(tables.length ? { database, tables } : { database });
  }
  const retention = asRecord(record.retention);
  const state = asRecord(record.state);
  const lastStatus = text(state?.lastStatus) as BackupRunStatus;
  const view: BackupJobView = {
    id,
    ...(record.format === "archive" || record.format === "sql" ? { format: record.format } : {}),
    name: text(record.name),
    connectionId,
    targets,
    schedule,
    destination: text(record.destination),
    fileNamePrefix: text(record.fileNamePrefix),
    gzip: record.gzip === true,
    retention: { keepLast: count(retention?.keepLast) ?? 0, keepDays: count(retention?.keepDays) ?? 0 },
    enabled: record.enabled === true,
    notifyOnSuccess: record.notifyOnSuccess === true,
    createdAt: count(record.createdAt) ?? 0,
    updatedAt: count(record.updatedAt) ?? 0,
    state: {
      ...(count(state?.lastScheduledAt) !== undefined ? { lastScheduledAt: count(state?.lastScheduledAt) } : {}),
      ...(count(state?.lastRunAt) !== undefined ? { lastRunAt: count(state?.lastRunAt) } : {}),
      ...(STATUSES.has(lastStatus) ? { lastStatus } : {}),
    },
    running: record.running === true,
  };
  const nextRunAt = count(record.nextRunAt);
  if (nextRunAt) view.nextRunAt = nextRunAt;
  const lastRun = parseBackupRun(record.lastRun);
  if (lastRun) view.lastRun = lastRun;
  return view;
}

export function parseBackupJobViews(value: unknown): BackupJobView[] {
  return (Array.isArray(value) ? value : []).flatMap((item) => {
    const view = parseBackupJobView(item);
    return view ? [view] : [];
  });
}

export function parseBackupRuns(value: unknown): BackupRun[] {
  return (Array.isArray(value) ? value : []).flatMap((item) => {
    const run = parseBackupRun(item);
    return run ? [run] : [];
  });
}

export function parseBackupRunEvent(value: unknown): BackupRunEvent | undefined {
  const record = asRecord(value);
  const run = parseBackupRun(record?.run);
  const jobId = text(record?.jobId);
  if (!record || !run || !jobId || run.jobId !== jobId) return undefined;
  return {
    jobId,
    jobName: text(record.jobName),
    connectionId: text(record.connectionId),
    notifyOnSuccess: record.notifyOnSuccess === true,
    run,
  };
}

/** Replaces the job's latest run in a list view model after a run event. */
export function applyBackupRunEvent(views: BackupJobView[], event: BackupRunEvent): BackupJobView[] {
  return views.map((view) => {
    if (view.id !== event.jobId) return view;
    const running = event.run.status === "running";
    return {
      ...view,
      running,
      lastRun: event.run,
      state: running ? view.state : { ...view.state, lastRunAt: event.run.startedAt, lastStatus: event.run.status },
    };
  });
}

// ── Notifications and formatting ──

export type BackupNotificationKind = "failed" | "warning" | "success";

/** Which notification (if any) a finished run should raise. Cancellations are silent. */
export function backupNotificationKind(event: BackupRunEvent): BackupNotificationKind | undefined {
  switch (event.run.status) {
    case "failed":
      return "failed";
    case "success":
      if (event.run.warnings?.length) return "warning";
      return event.notifyOnSuccess ? "success" : undefined;
    default:
      return undefined;
  }
}

export function formatBackupBytes(bytes: number | undefined): string {
  if (bytes === undefined || !Number.isFinite(bytes) || bytes < 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return unit === 0 ? `${value} B` : `${value.toFixed(value >= 100 ? 0 : 1)} ${units[unit]}`;
}

export function formatBackupDuration(ms: number | undefined): string {
  if (ms === undefined || !Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

/** Final component of a path on any platform (for compact file display). */
export function backupFileBaseName(path: string): string {
  const parts = path.split(/[\\/]/);
  return parts[parts.length - 1] || path;
}
