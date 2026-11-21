// Frontend side of the backup channel (backend/plugin/host_backup.go):
// collects and writes back MinCode's own stored data for the Go host and
// shows the two host-owned dialogs. Loaded lazily by services/plugin-host-ui.ts
// on the first plugin:backup-request event, so none of this is in the entry
// bundle. Replies carry fixed error texts only, never values or messages.
import { ElMessage } from "element-plus";
import {
  backupRequestCancelId,
  parseBackupRequest,
  type BackupDialogRequest,
  type BackupReply,
  type BackupReplyError,
  type BackupRequest,
  type BackupStoredValue,
} from "@/domain/backup/requests";
import { BACKUP_RESTORED_EVENT, type BackupRestoredDetail } from "@/domain/backup/restored-event";
import { useVueI18n } from "@/i18n/messages";
import {
  buildConfigExport,
  isPortableConfigKey,
  prepareConfigValuesForWrite,
  type ConfigExportEntry,
} from "@/services/config-portability";
import { applyPersistedBatch, freezePersistedWrites } from "@/services/persistent-store";
import { updateSettingsRuntimeSnapshot } from "@/services/settings-persistence";
import { invokeBackend } from "@/services/desktop-bridge";
import { usePluginHostUiStore } from "@/stores/plugin-host-ui";

/**
 * Every portable stored value. Without secrets, secret fields inside values
 * are blanked but no key is dropped: an overwrite restore deletes local keys
 * the backup lacks. Strict: a failed native load fails the collect instead of
 * returning the partial browser copy.
 */
export async function collectBackupValues(includeSecrets: boolean): Promise<Record<string, ConfigExportEntry>> {
  return (await buildConfigExport({ includeSecrets, strict: true })).values;
}

/** Restored keys whose stores reload in place (ssh-connections, database-workspace). */
export const LIVE_RELOADED_BACKUP_KEYS: ReadonlySet<string> = new Set([
  "app_ssh_connections_v1",
  "app_ssh_credentials_v1",
  "app_ssh_connection_group_order_v1",
  "app_db_connections",
  "app_db_connection_group_order",
]);

export interface BackupApplyOutcome {
  /** Every key set or deleted. */
  keys: string[];
  /**
   * Some restored data is held by stores that cannot reload it in place:
   * every write stays frozen and the window must reload.
   */
  reloadRequired: boolean;
}

/**
 * Writes a restore (or its rollback) in one native batch, prepared exactly
 * like a config import. An abort before the batch starts writes nothing;
 * once it started it is finished.
 *
 * Stores keep their old in-memory copies, and their flushes would write them
 * back. So before the batch, writes are frozen (persistent-store): all of
 * them when a restored key has no in-place reload (the window reloads once
 * the whole restore is over, which lifts the freeze; a rollback batch is a
 * restore batch too and writes through), else just the SSH/database keys until
 * those stores have reloaded.
 */
/** How long an SSH/database-only restore waits for those stores to reload before unfreezing. */
export const BACKUP_LIVE_RELOAD_TIMEOUT_MS = 30_000;

function liveReloadTimedOut() {
  const { t } = useVueI18n();
  ElMessage({ type: "warning", message: t("pluginBackup.restored.restartNeeded"), duration: 0, showClose: true, grouping: true });
}

/** A step after the written batch failed: logged without values, never reported as a failed write. */
function reportPostBatchFailure(step: string, error: unknown) {
  // eslint-disable-next-line no-console
  console.warn(`[backup-host] after the restore batch, ${step} failed: ${error instanceof Error ? error.name : "error"}`);
}

export async function applyBackupValues(
  sets: Record<string, BackupStoredValue>,
  deletes: readonly string[],
  options: {
    signal?: AbortSignal;
    /** Injected for tests. */
    liveReloadTimeoutMs?: number;
    onLiveReloadTimeout?: () => void;
  } = {},
): Promise<BackupApplyOutcome> {
  const { signal, liveReloadTimeoutMs = BACKUP_LIVE_RELOAD_TIMEOUT_MS, onLiveReloadTimeout = liveReloadTimedOut } = options;
  const keys = [...Object.keys(sets), ...deletes];
  for (const key of keys) {
    if (!isPortableConfigKey(key)) throw new Error("backup_key_not_portable");
  }
  signal?.throwIfAborted();
  const prepared = await prepareConfigValuesForWrite(sets);
  signal?.throwIfAborted();
  const reloadRequired = keys.some((key) => !LIVE_RELOADED_BACKUP_KEYS.has(key));
  const unfreeze = freezePersistedWrites(reloadRequired ? "all" : keys);
  try {
    await applyPersistedBatch({ sets: prepared.sets, deleteKeys: deletes }, { ignoreWriteFreeze: true });
  } catch (error) {
    // Nothing was written (one native transaction): the old copies are still right.
    unfreeze();
    throw error;
  }
  // The data is written. Nothing below may turn that into a failure: Go would
  // think nothing changed, and a full freeze would never be followed by a reload.
  try {
    if (prepared.settingsSnapshot) updateSettingsRuntimeSnapshot(prepared.settingsSnapshot);
  } catch (error) {
    reportPostBatchFailure("updating the settings snapshot", error);
  }
  const reloads: Promise<unknown>[] = [];
  try {
    window.dispatchEvent(
      new CustomEvent<BackupRestoredDetail>(BACKUP_RESTORED_EVENT, {
        detail: { keys, waitUntil: (reload) => void reloads.push(reload) },
      }),
    );
  } catch (error) {
    reportPostBatchFailure("announcing the restored keys", error);
  }
  if (!reloadRequired) {
    // The SSH/database stores dropped their pending saves and reload the
    // restored values; a reload that hangs must not freeze them for good.
    let timer: ReturnType<typeof setTimeout> | undefined;
    const timedOut = await Promise.race([
      Promise.allSettled(reloads).then(() => false),
      new Promise<boolean>((resolve) => (timer = setTimeout(() => resolve(true), liveReloadTimeoutMs))),
    ]);
    clearTimeout(timer);
    unfreeze();
    if (timedOut) {
      try {
        onLiveReloadTimeout();
      } catch (error) {
        reportPostBatchFailure("the restart notice", error);
      }
    }
  }
  return { keys, reloadRequired };
}

export type BackupDialogAnswer =
  | { cancelled: true }
  | { password: string; remember: boolean }
  | { confirmed: boolean };

export interface BackupRequestHandlerDeps {
  reply: (reply: BackupReply) => Promise<unknown> | void;
  /** Queues a dialog; false when it cannot be shown (it is then answered as cancelled). */
  showDialog: (request: BackupDialogRequest) => boolean;
  removeDialog: (id: string) => void;
  /**
   * While a reload waits for the finished event: shows that edits are not
   * saved; the notice stays until reloadWindow.
   */
  showRestorePending: () => void;
  /**
   * Shows the "MinCode will reload" notice for the outcome (true: restored,
   * false: undone or, with detail.rolledBack false, not undone; undefined:
   * not confirmed) and reloads the window.
   */
  reloadWindow: (ok: boolean | undefined, detail?: BackupRestoreFinishedDetail) => void;
  /** Starts a timer; returns its cancel function (injected for tests). */
  setTimer?: (callback: () => void, ms: number) => () => void;
}

/**
 * If plugin:backup-restore-finished never arrives after a restore wrote data
 * that needs a reload, reload anyway; writes stay frozen until then.
 */
export const BACKUP_RESTORE_FINISHED_TIMEOUT_MS = 10 * 60 * 1000;

function defaultTimer(callback: () => void, ms: number) {
  const timer = setTimeout(callback, ms);
  return () => clearTimeout(timer);
}

function isAbort(error: unknown) {
  return error instanceof Error && error.name === "AbortError";
}

function dialogReply(request: BackupDialogRequest, answer: BackupDialogAnswer): BackupReply {
  if (request.kind === "backupPassword" && "password" in answer) {
    return { id: request.id, result: { password: answer.password, remember: answer.remember } };
  }
  if (request.kind === "backupRestoreConfirm" && "confirmed" in answer) {
    return { id: request.id, result: { confirmed: answer.confirmed } };
  }
  return { id: request.id, cancelled: true };
}

export function createBackupRequestHandler(deps: BackupRequestHandlerDeps) {
  const running = new Map<string, AbortController>();
  const openDialogs = new Set<string>();
  const setTimer = deps.setTimer ?? defaultTimer;
  // A restore wrote data that needs a window reload. Go applies the plugins'
  // data afterwards and may send a rollback backupApply, so the reload waits
  // for plugin:backup-restore-finished (or the safety-net timer).
  let pendingReload = false;
  let reloaded = false;
  let cancelSafetyNet: (() => void) | undefined;

  function reloadOnce(ok: boolean | undefined, detail?: BackupRestoreFinishedDetail) {
    if (reloaded) return;
    reloaded = true;
    pendingReload = false;
    cancelSafetyNet?.();
    cancelSafetyNet = undefined;
    if (detail) deps.reloadWindow(ok, detail);
    else deps.reloadWindow(ok);
  }

  function awaitRestoreFinished() {
    if (pendingReload || reloaded) return;
    pendingReload = true;
    cancelSafetyNet = setTimer(() => reloadOnce(undefined), BACKUP_RESTORE_FINISHED_TIMEOUT_MS);
    try {
      deps.showRestorePending();
    } catch {
      // The notice is best effort; the reload still follows.
    }
  }

  /**
   * plugin:backup-restore-finished {restoreId, ok, rolledBack, preRestoreName?}:
   * the whole restore is over, reload if data waits for it.
   */
  function restoreFinished(event?: unknown) {
    if (!pendingReload) return;
    const data = (event ?? {}) as { ok?: unknown; rolledBack?: unknown; preRestoreName?: unknown };
    const ok = typeof data.ok === "boolean" ? data.ok : undefined;
    if (ok !== false || typeof data.rolledBack !== "boolean") {
      reloadOnce(ok);
      return;
    }
    const detail: BackupRestoreFinishedDetail = { rolledBack: data.rolledBack };
    if (typeof data.preRestoreName === "string") detail.preRestoreName = data.preRestoreName;
    reloadOnce(ok, detail);
  }

  async function send(reply: BackupReply) {
    // The request may already be closed on the Go side (timeout, plugin stopped).
    try {
      await deps.reply(reply);
    } catch {
      // Nothing left to answer.
    }
  }

  /**
   * Runs collect or apply. A cancelled collect is not answered. A cancelled
   * apply is not answered only if it stopped before writing; once the batch
   * started its outcome is still reported. afterReply runs once the reply
   * was sent.
   */
  async function run(
    request: BackupRequest,
    failure: BackupReplyError,
    work: (signal: AbortSignal) => Promise<{ result: Record<string, unknown>; afterReply?: () => void }>,
    { answersAfterCancel }: { answersAfterCancel: boolean },
  ) {
    const controller = new AbortController();
    running.set(request.id, controller);
    try {
      let outcome: Awaited<ReturnType<typeof work>>;
      try {
        outcome = await work(controller.signal);
      } catch (error) {
        if (controller.signal.aborted && (!answersAfterCancel || isAbort(error))) return;
        await send({ id: request.id, error: failure });
        return;
      }
      if (controller.signal.aborted && !answersAfterCancel) return;
      await send({ id: request.id, result: outcome.result });
      outcome.afterReply?.();
    } finally {
      running.delete(request.id);
    }
  }

  async function handle(event: unknown) {
    const parsed = parseBackupRequest(event);
    if ("error" in parsed) {
      if (parsed.id) await send({ id: parsed.id, error: parsed.error });
      return;
    }
    const { request } = parsed;
    // A repeated id is the same request; it is already being answered.
    if (running.has(request.id) || openDialogs.has(request.id)) return;
    switch (request.kind) {
      case "backupPassword":
      case "backupRestoreConfirm":
        if (deps.showDialog(request)) openDialogs.add(request.id);
        else await send({ id: request.id, cancelled: true });
        return;
      case "backupCollect":
        return run(
          request,
          "collect_failed",
          async () => ({ result: { values: await collectBackupValues(request.includeSecrets) } }),
          { answersAfterCancel: false },
        );
      case "backupApply":
        return run(
          request,
          "apply_failed",
          async (signal) => {
            const { reloadRequired } = await applyBackupValues(request.sets, request.deletes, { signal });
            // A rollback of non-live keys joins the same pending reload: one reload in total.
            return { result: { ok: true }, ...(reloadRequired ? { afterReply: awaitRestoreFinished } : {}) };
          },
          { answersAfterCancel: true },
        );
    }
  }

  /** plugin:backup-request-cancel: removes a queued or visible dialog, or stops a pending write. */
  function cancel(event: unknown) {
    const id = backupRequestCancelId(event);
    if (!id) return;
    running.get(id)?.abort();
    if (openDialogs.delete(id)) deps.removeDialog(id);
  }

  async function answer(request: BackupDialogRequest, value: BackupDialogAnswer) {
    // Cancelled meanwhile, or answered already.
    if (!openDialogs.delete(request.id)) return;
    deps.removeDialog(request.id);
    await send(dialogReply(request, value));
  }

  return {
    handle,
    cancel,
    answer,
    restoreFinished,
    /** Whether a window reload waits for plugin:backup-restore-finished. */
    reloadPending: () => pendingReload,
  };
}

/** How long the "MinCode will reload" notice shows before the window reloads. */
const RELOAD_AFTER_RESTORE_DELAY_MS = 1000;
/** Longer when the restore could not be undone: the notice names the backup to restore. */
const RELOAD_AFTER_FAILED_ROLLBACK_DELAY_MS = 10_000;

/** What plugin:backup-restore-finished says about a failed restore. */
export interface BackupRestoreFinishedDetail {
  /** false: the restore could not be undone and some data may have changed. */
  rolledBack: boolean;
  /** The pre-restore backup's file name. */
  preRestoreName?: string;
}

const notUndone = (ok: boolean | undefined, detail?: BackupRestoreFinishedDetail) => ok === false && detail?.rolledBack === false;

/** A file name shown as plain text: one line, bounded. */
function displayableName(name: string | undefined) {
  const control = (char: string) => char.charCodeAt(0) < 0x20 || char.charCodeAt(0) === 0x7f;
  return name && name.length <= 200 && ![...name].some(control) ? name : undefined;
}

type RestoreReloadKey =
  | "pluginBackup.restored.reloading"
  | "pluginBackup.restored.undone"
  | "pluginBackup.restored.unconfirmed"
  | "pluginBackup.restored.notUndone";

/** The reload notice for the restore outcome from plugin:backup-restore-finished. */
export function restoreReloadMessageKey(ok: boolean | undefined): RestoreReloadKey {
  if (ok === true) return "pluginBackup.restored.reloading";
  return ok === false ? "pluginBackup.restored.undone" : "pluginBackup.restored.unconfirmed";
}

/** The reload notice with the pre-restore backup's name when the restore could not be undone. */
export function restoreReloadMessage(
  ok: boolean | undefined,
  detail?: BackupRestoreFinishedDetail,
): { key: RestoreReloadKey } | { key: "pluginBackup.restored.notUndoneNamed"; params: { name: string } } {
  if (!notUndone(ok, detail)) return { key: restoreReloadMessageKey(ok) };
  const name = displayableName(detail?.preRestoreName);
  if (name) return { key: "pluginBackup.restored.notUndoneNamed", params: { name } };
  return { key: "pluginBackup.restored.notUndone" };
}

export function restoreReloadDelayMs(ok: boolean | undefined, detail?: BackupRestoreFinishedDetail) {
  return notUndone(ok, detail) ? RELOAD_AFTER_FAILED_ROLLBACK_DELAY_MS : RELOAD_AFTER_RESTORE_DELAY_MS;
}

let restorePendingNotice: { close: () => void } | undefined;

function showRestorePending() {
  const { t } = useVueI18n();
  restorePendingNotice = ElMessage({ type: "warning", message: t("pluginBackup.restored.pending"), duration: 0, showClose: false });
}

/**
 * Restored data the open stores cannot reload in place: writes are frozen
 * (applyBackupValues), so reload the window to read it from storage.
 */
function reloadWindow(ok: boolean | undefined, detail?: BackupRestoreFinishedDetail) {
  const { t } = useVueI18n();
  restorePendingNotice?.close();
  restorePendingNotice = undefined;
  const notice = restoreReloadMessage(ok, detail);
  // Plain text: the name is interpolated, never rendered as HTML.
  const message = "params" in notice ? t(notice.key, notice.params) : t(notice.key);
  const failedRollback = notUndone(ok, detail);
  ElMessage({ type: failedRollback ? "error" : ok === false ? "warning" : "success", message, duration: 0, showClose: failedRollback });
  setTimeout(() => window.location.reload(), restoreReloadDelayMs(ok, detail));
}

const defaultHandler = createBackupRequestHandler({
  reply: (reply) => invokeBackend("plugin_host_backup_reply", reply),
  showDialog: (request) => usePluginHostUiStore().pushBackupDialog(request),
  removeDialog: (id) => usePluginHostUiStore().removeBackupDialog(id),
  showRestorePending,
  reloadWindow,
});

export const handleBackupRequest = defaultHandler.handle;
export const cancelBackupRequest = defaultHandler.cancel;
export const answerBackupDialog = defaultHandler.answer;
export const handleBackupRestoreFinished = defaultHandler.restoreFinished;
