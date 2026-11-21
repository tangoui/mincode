// Requests the Go host sends over the backup channel
// (backend/plugin/host_backup.go, host_backup_restore.go):
//
//   - event plugin:backup-request {id, kind, pluginId, pluginName, payload}
//   - event plugin:backup-request-cancel {id}
//   - command plugin_host_backup_reply {id, cancelled?, error?, result?}
//
// Everything here is pure: parsing and validating the event, the password
// rule of the password dialog and the grouping of the restore confirmation.
import { visibleReviewText } from "@/domain/plugin-ui/ai-config";
import { BACKUP_CORE_SECTIONS, type BackupCoreSectionId } from "@/domain/backup/sections";

export const BACKUP_REQUEST_EVENT = "plugin:backup-request";
export const BACKUP_REQUEST_CANCEL_EVENT = "plugin:backup-request-cancel";

export type BackupStorageMode = "plain" | "secure";

export interface BackupStoredValue {
  storage: BackupStorageMode;
  value: unknown;
}

export type BackupChangeKind = "add" | "update" | "remove" | "keep" | "more";

export interface BackupChange {
  section: string;
  sectionTitle: string;
  kind: BackupChangeKind;
  label: string;
}

interface BackupRequestOrigin {
  id: string;
  pluginId: string;
  /** The host's display name for the plugin (event field, never payload text). */
  pluginName: string;
}

export interface BackupCollectRequest extends BackupRequestOrigin {
  kind: "backupCollect";
  includeSecrets: boolean;
}

export interface BackupPasswordRequest extends BackupRequestOrigin {
  kind: "backupPassword";
  mode: "set" | "open";
  /**
   * Open mode: a backup password is already remembered or set for this run,
   * so "remember" would replace it. Absent from older hosts: true.
   */
  hasPassword?: boolean;
}

export interface BackupRestoreConfirmRequest extends BackupRequestOrigin {
  kind: "backupRestoreConfirm";
  mode: "overwrite" | "merge";
  changes: BackupChange[];
}

export interface BackupApplyRequest extends BackupRequestOrigin {
  kind: "backupApply";
  sets: Record<string, BackupStoredValue>;
  deletes: string[];
}

export type BackupRequest =
  | BackupCollectRequest
  | BackupPasswordRequest
  | BackupRestoreConfirmRequest
  | BackupApplyRequest;

/** Requests answered by a host dialog, one at a time. */
export type BackupDialogRequest = BackupPasswordRequest | BackupRestoreConfirmRequest;

/** Fixed error texts: never values or exception messages. */
export type BackupReplyError = "invalid_request" | "collect_failed" | "apply_failed";

export type BackupReply =
  | { id: string; cancelled: true }
  | { id: string; error: BackupReplyError }
  | { id: string; result: Record<string, unknown> };

export type BackupRequestParse = { request: BackupRequest } | { error: "invalid_request"; id?: string };

const REQUEST_ID = /^[A-Za-z0-9_.:-]{1,128}$/;
// Same rule as backupFrontendKeyPattern in host_backup.go.
const STORAGE_KEY = /^app_[A-Za-z0-9_.-]{1,128}$/;
const CHANGE_KINDS: ReadonlySet<string> = new Set(["add", "update", "remove", "keep", "more"]);
/** The Go side caps the rows at 2000 plus one "more" row. */
const MAX_CHANGES = 2001;
const MAX_TEXT_LENGTH = 4096;

function record(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function shortText(value: unknown): value is string {
  return typeof value === "string" && value.length <= MAX_TEXT_LENGTH;
}

function requestId(value: unknown): string | undefined {
  return typeof value === "string" && REQUEST_ID.test(value) ? value : undefined;
}

function parseChange(value: unknown): BackupChange | undefined {
  const change = record(value);
  if (!change) return undefined;
  const { section, sectionTitle, kind, label } = change;
  if (!shortText(section) || !shortText(sectionTitle) || !shortText(label)) return undefined;
  if (typeof kind !== "string" || !CHANGE_KINDS.has(kind)) return undefined;
  if (kind === "more" && !/^\d{1,9}$/.test(label)) return undefined;
  return { section, sectionTitle, kind: kind as BackupChangeKind, label };
}

function parseStoredValue(value: unknown): BackupStoredValue | undefined {
  const entry = record(value);
  if (!entry || (entry.storage !== "plain" && entry.storage !== "secure") || !("value" in entry)) return undefined;
  return { storage: entry.storage, value: entry.value };
}

function parseApply(payload: Record<string, unknown>) {
  const sets = record(payload.sets);
  const deletes = payload.deletes;
  if (!sets || !Array.isArray(deletes)) return undefined;
  const parsedSets: Record<string, BackupStoredValue> = {};
  for (const [key, value] of Object.entries(sets)) {
    const parsed = STORAGE_KEY.test(key) ? parseStoredValue(value) : undefined;
    if (!parsed) return undefined;
    parsedSets[key] = parsed;
  }
  const parsedDeletes: string[] = [];
  for (const key of deletes) {
    if (typeof key !== "string" || !STORAGE_KEY.test(key) || key in parsedSets) return undefined;
    parsedDeletes.push(key);
  }
  return { sets: parsedSets, deletes: parsedDeletes };
}

function parsePayload(kind: unknown, payload: Record<string, unknown>) {
  switch (kind) {
    case "backupCollect":
      return typeof payload.includeSecrets === "boolean"
        ? { kind, includeSecrets: payload.includeSecrets }
        : undefined;
    case "backupPassword":
      if (payload.mode === "set") return { kind, mode: payload.mode };
      if (payload.mode !== "open" || (payload.hasPassword !== undefined && typeof payload.hasPassword !== "boolean")) return undefined;
      return { kind, mode: payload.mode, hasPassword: payload.hasPassword ?? true };
    case "backupRestoreConfirm": {
      const { mode, changes } = payload;
      if ((mode !== "overwrite" && mode !== "merge") || !Array.isArray(changes) || changes.length > MAX_CHANGES) {
        return undefined;
      }
      const parsed: BackupChange[] = [];
      for (const change of changes) {
        const item = parseChange(change);
        if (!item) return undefined;
        parsed.push(item);
      }
      return { kind, mode, changes: parsed };
    }
    case "backupApply": {
      const apply = parseApply(payload);
      return apply ? { kind, ...apply } : undefined;
    }
    default:
      return undefined;
  }
}

/** Validates a plugin:backup-request event; an invalid one keeps its id so it can be answered. */
export function parseBackupRequest(event: unknown): BackupRequestParse {
  const value = record(event);
  const id = requestId(value?.id);
  const invalid: BackupRequestParse = id ? { error: "invalid_request", id } : { error: "invalid_request" };
  if (!value || !id) return invalid;
  const { pluginId, pluginName } = value;
  if (typeof pluginId !== "string" || !pluginId || !shortText(pluginName)) return invalid;
  const payload = record(value.payload);
  const parsed = payload ? parsePayload(value.kind, payload) : undefined;
  if (!parsed) return invalid;
  return { request: { id, pluginId, pluginName: pluginName || pluginId, ...parsed } as BackupRequest };
}

export function backupRequestCancelId(event: unknown): string | undefined {
  return requestId(record(event)?.id);
}

// ---------------------------------------------------------------------------
// Password dialog.

export const BACKUP_PASSWORD_MIN_LENGTH = 12;
/** backupPasswordMaxBytes in host_backup.go. */
export const BACKUP_PASSWORD_MAX_BYTES = 1024;

export type BackupPasswordIssue = "" | "empty" | "tooShort" | "tooLong" | "mismatch";

/** Characters as the user sees them: code points, not UTF-16 units. */
export function backupPasswordLength(password: string) {
  return [...password].length;
}

/** Whether a password that would replace the existing one (open mode with a password). */
const replacesPassword = (request: BackupPasswordRequest) => request.mode === "open" && request.hasPassword !== false;

/**
 * "Remember" starts checked, except when opening a backup would replace the
 * password future backups use: then the user opts in.
 */
export function backupPasswordRememberDefault(request: BackupPasswordRequest) {
  return !replacesPassword(request);
}

export function backupPasswordRememberLabelKey(request: BackupPasswordRequest) {
  return replacesPassword(request) ? "pluginBackup.password.rememberReplace" : "pluginBackup.password.remember";
}

export function backupPasswordRememberHintKey(request: BackupPasswordRequest, remember: boolean) {
  if (!remember) return request.mode === "open" ? "pluginBackup.password.openOnlyHint" : "pluginBackup.password.sessionHint";
  return replacesPassword(request) ? "pluginBackup.password.rememberReplaceHint" : "pluginBackup.password.rememberHint";
}

export function backupPasswordIssue(mode: "set" | "open", password: string, repeat: string): BackupPasswordIssue {
  if (new TextEncoder().encode(password).length > BACKUP_PASSWORD_MAX_BYTES) return "tooLong";
  if (mode === "open") return password ? "" : "empty";
  if (backupPasswordLength(password) < BACKUP_PASSWORD_MIN_LENGTH) return "tooShort";
  return password === repeat ? "" : "mismatch";
}

// ---------------------------------------------------------------------------
// Restore confirmation.

export type BackupVisibleChangeKind = Exclude<BackupChangeKind, "more">;

export interface BackupChangeGroup {
  kind: BackupVisibleChangeKind;
  labels: string[];
}

export interface BackupChangeSection {
  section: string;
  /** Core sections are titled by the host's own copy; others show sectionTitle. */
  core: boolean;
  coreId?: BackupCoreSectionId;
  pluginId?: string;
  sectionTitle: string;
  groups: BackupChangeGroup[];
}

export interface GroupedBackupChanges {
  sections: BackupChangeSection[];
  /** Changes not sent to the dialog ("N more changes not shown"). */
  more: number;
}

const KIND_ORDER: readonly BackupVisibleChangeKind[] = ["add", "update", "remove", "keep"];

function isCoreSection(section: string): section is BackupCoreSectionId {
  return (BACKUP_CORE_SECTIONS as readonly string[]).includes(section);
}

/** Groups rows by section (core sections first, in display order) and then by kind; all text made visible. */
export function groupRestoreChanges(changes: readonly BackupChange[]): GroupedBackupChanges {
  let more = 0;
  const bySection = new Map<string, { title: string; kinds: Map<BackupVisibleChangeKind, string[]> }>();
  for (const change of changes) {
    if (change.kind === "more") {
      more += Number(change.label) || 0;
      continue;
    }
    let entry = bySection.get(change.section);
    if (!entry) {
      entry = { title: visibleReviewText(change.sectionTitle), kinds: new Map() };
      bySection.set(change.section, entry);
    }
    const labels = entry.kinds.get(change.kind) ?? [];
    labels.push(visibleReviewText(change.label));
    entry.kinds.set(change.kind, labels);
  }
  const order = [...bySection.keys()].sort((a, b) => sectionRank(a) - sectionRank(b));
  const sections = order.map((section): BackupChangeSection => {
    const entry = bySection.get(section)!;
    const groups = KIND_ORDER.flatMap((kind) => {
      const labels = entry.kinds.get(kind);
      return labels ? [{ kind, labels }] : [];
    });
    const core = isCoreSection(section);
    return {
      section,
      core,
      ...(core ? { coreId: section } : {}),
      ...(section.startsWith("plugin:") ? { pluginId: visibleReviewText(section.slice("plugin:".length)) } : {}),
      sectionTitle: entry.title,
      groups,
    };
  });
  return { sections, more };
}

function sectionRank(section: string) {
  const index = (BACKUP_CORE_SECTIONS as readonly string[]).indexOf(section);
  return index >= 0 ? index : BACKUP_CORE_SECTIONS.length;
}
