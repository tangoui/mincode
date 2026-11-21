import { parseTransferTask, transferTerminal, type TransferTask } from "@/domain/database/transfer-tasks";
import { importInlineRows } from "@/services/database-row-import-service";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { runWithSafety, sqlSafetyArgs, type SqlSafetyToken } from "@/services/sql-safety-service";
import { parseSqlSafetyError } from "@/domain/database/sql-safety";
export async function getTransferTask(taskId: string): Promise<TransferTask> {
  return parseTransferTask(await invokeBackend<unknown>("db_transfer_task_get", { taskId }));
}
export async function listTransferTasks(kind: TransferTask["kind"], workspaceId = ""): Promise<TransferTask[]> {
  if (!isDesktopRuntime()) return [];
  const tasks: TransferTask[] = [];
  const seen = new Set<string>();
  let cursor = "";
  do {
    const value = await invokeBackend<unknown>("db_transfer_task_list", { kind, workspaceId, limit: 100, cursor });
    if (!value || typeof value !== "object") throw new Error("Malformed task list");
    const page = value as { tasks?: unknown; nextCursor?: unknown };
    if (!Array.isArray(page.tasks) || (page.nextCursor !== undefined && typeof page.nextCursor !== "string")) throw new Error("Malformed task list");
    tasks.push(...page.tasks.map(parseTransferTask));
    cursor = typeof page.nextCursor === "string" ? page.nextCursor : "";
    if (cursor && seen.has(cursor)) throw new Error("Repeated task page cursor");
    seen.add(cursor);
  } while (cursor);
  return tasks;
}
export async function cancelTransferTask(taskId: string): Promise<TransferTask> {
  return parseTransferTask(await invokeBackend<unknown>("db_transfer_task_cancel", { taskId }));
}
export function onTransferTask(handler: (task: TransferTask) => void): () => void {
  if (!isDesktopRuntime()) return () => undefined;
  return listenAppEvent<unknown>("db-transfer:task", (raw) => { try { handler(parseTransferTask(raw)); } catch { /* Untrusted events are hints; query remains authoritative. */ } });
}
export async function submitPreparedImport(planId: string, idempotencyKey: string, token?: SqlSafetyToken): Promise<TransferTask> {
  // A retry must never mint another key or prepare another input after admission. A lost response
  // is retried with the same token: the backend needs none to repeat a submission it admitted.
  const args = { planId, idempotencyKey, ...(token ? sqlSafetyArgs(token) : {}) };
  let failure: unknown;
  for (let attempt = 0; attempt < 2; attempt++) {
    try { return parseTransferTask(await invokeBackend<unknown>("db_data_import_start", args)); }
    catch (cause) {
      // A safety-net refusal is final: retrying with the spent token would hide the real reason.
      if (parseSqlSafetyError(cause)) throw cause;
      failure = cause;
    }
  }
  throw failure;
}
/** Submits a prepared import through the SQL safety net; undefined when the user cancels the confirmation. */
export function submitCheckedImport(target: { sessionId: string; database: string }, planId: string, idempotencyKey: string) {
  return runWithSafety({
    sessionId: target.sessionId,
    database: target.database,
    operation: { kind: "import", planId },
    source: "import",
    origin: "preconfirmed",
    execute: (token) => submitPreparedImport(planId, idempotencyKey, token),
  });
}
/** Imports a file into a table; undefined when the user cancels the safety confirmation. */
export async function importSelectedTableData(sessionId: string, database: string, table: string, format: string, path: string, workspaceId = ""): Promise<number | undefined> {
  const preparation = await invokeBackend<{ planId: string }>("db_data_import_prepare", { sessionId, database, table, format, path, workspaceId });
  if (typeof preparation?.planId !== "string" || !preparation.planId) throw new Error("Malformed import preparation");
  const { useDatabaseTransferTasks } = await import("@/stores/database-transfer-tasks");
  const store = useDatabaseTransferTasks();
  const key = crypto.randomUUID();
  store.rememberImport(preparation.planId, key, workspaceId, { sessionId, database });
  const task = await submitCheckedImport({ sessionId, database }, preparation.planId, key);
  if (!task) {
    store.forgetImport(preparation.planId);
    return undefined;
  }
  store.merge(task);
  store.forgetImport(preparation.planId);
  for (;;) {
    const current = await store.refresh(task.id);
    if (transferTerminal(current)) {
      if (current.state !== "succeeded") throw new Error(`${current.errorCode ?? current.state} (${current.result.records}; ${current.result.certainty})`);
      return current.result.records;
    }
    await new Promise<void>((resolve) => window.setTimeout(resolve, 500));
  }
}

/** Imports inline rows through the SQL safety net; undefined when the user cancels the confirmation. */
export async function importInlineTableData(sessionId: string, database: string, table: string, format: string, data: string): Promise<number | undefined> {
  return runWithSafety({
    sessionId,
    database,
    operation: { kind: "import", table, format, data },
    source: "import",
    origin: "preconfirmed",
    execute: (token) => importInlineRows(sessionId, database, table, format, data, invokeBackend, sqlSafetyArgs(token)),
  });
}
