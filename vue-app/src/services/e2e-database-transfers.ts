/** Runs only in the opt-in desktop harness with its isolated HOME and fixtures. */
import { invokeBackend } from "@/services/desktop-bridge";
import { getTransferTask } from "@/services/database-transfer-service";
import { transferTerminal } from "@/domain/database/transfer-tasks";
import { planRestore, startRestore } from "@/services/database-restore-service";
import type { DatabaseQueryResult } from "@/domain/database/types";
import type { SqlSafetyCheck } from "@/domain/database/sql-safety";
import { runWithSafety, sqlSafetyArgs } from "@/services/sql-safety-service";

export async function databaseTransfersScenario(filePath: string, sessionId: string, log: { done(step: string): void }) {
  const destination = filePath.slice(0, Math.max(filePath.lastIndexOf("/"), filePath.lastIndexOf("\\")));
  const sourceId = "e2e-transfer-source";
  const connections = [{ id: sourceId, name: "E2E source", dbType: "sqlite", filePath, host: "", port: 0 }];
  async function wait(id: string) {
    const deadline = Date.now() + 20000;
    while (Date.now() < deadline) {
      const task = await getTransferTask(id);
      if (transferTerminal(task)) {
        if (task.state !== "succeeded") throw new Error(`transfer ${task.kind}: ${task.state} ${task.errorCode ?? ""}`);
        return task;
      }
      await new Promise<void>(resolve => setTimeout(resolve, 50));
    }
    throw new Error("transfer did not finish before deadline");
  }
  const prepared = await invokeBackend<{ planId: string }>("db_data_import_prepare", {
    sessionId, database: "main", table: "e2e_items", format: "json", data: '[{"id":3,"name":"imported"}]', workspaceId: "e2e-transfer",
  });
  // The harness confirms the SQL safety check itself; the repeat reuses the spent token, which the
  // backend accepts because repeating an admitted submission needs none.
  const confirmInHarness = async (check: SqlSafetyCheck) => (check.risk === "high" ? { confirmText: check.confirmTarget } : {});
  const idempotencyKey = crypto.randomUUID();
  let args: Record<string, unknown> = {};
  const admitted = await runWithSafety({
    sessionId, database: "main", operation: { kind: "import", planId: prepared.planId }, source: "e2e", origin: "preconfirmed",
    confirm: confirmInHarness,
    execute: (token) => {
      args = { planId: prepared.planId, idempotencyKey, ...sqlSafetyArgs(token) };
      return invokeBackend<{ id: string }>("db_data_import_start", args);
    },
  });
  if (!admitted) throw new Error("e2e SQL safety confirmation declined");
  const repeated = await invokeBackend<{ id: string }>("db_data_import_start", args);
  if (admitted.id !== repeated.id) throw new Error("import idempotency created a second task");
  if ((await wait(admitted.id)).result.records !== 1) throw new Error("import committed count differs");
  log.done("prepared import committed once; same-key admission recovered same task");
  let rejected = false;
  try {
    await invokeBackend("db_data_import_prepare", { sessionId, database: "main", table: "e2e_items", format: "json", data: '[{"id":4,"name":"bad"},{"id":}]' });
  } catch { rejected = true; }
  if (!rejected) throw new Error("malformed late import accepted");
  const count = await invokeBackend<DatabaseQueryResult>("db_execute_query", { sessionId, query: "SELECT COUNT(*) FROM e2e_items" });
  if (String(count.rows[0]?.[0]) !== "3") throw new Error("malformed preparation wrote rows");
  log.done("late malformed import rejected before writes");

  for (const format of ["archive", "sql"] as const) {
    const targetId = `e2e-transfer-${format}`;
    const targetPath = `${filePath}.${format}.restored`;
    const targetSession = await invokeBackend<string>("db_connect", { config: { dbType: "sqlite", filePath: targetPath, host: "", port: 0 } });
    connections.push({ id: targetId, name: targetId, dbType: "sqlite", filePath: targetPath, host: "", port: 0 });
    // Only this harness's isolated configuration is replaced; no user store is used.
    await invokeBackend("store_set", { key: "app_db_connections", value: connections });
    let jobId = "";
    try {
      const job = await invokeBackend<{ id: string }>("db_backup_save", { job: {
        name: `E2E ${format}`, connectionId: sourceId, format, targets: [{ database: "main", tables: ["e2e_items"] }],
        destination, fileNamePrefix: `e2e-${format}`, gzip: format === "sql", enabled: false,
        schedule: { kind: "interval", intervalHours: 1 }, retention: { keepLast: 0, keepDays: 0 }, notifyOnSuccess: false,
      } });
      jobId = job.id;
      const run = await invokeBackend<{ id: string }>("db_backup_run_now", { id: jobId });
      await wait(run.id);
      const snapshot = await invokeBackend<{ result: { artifact: string } }>("db_transfer_task_get", { taskId: run.id });
      const path = snapshot.result.artifact;
      if (!path?.endsWith(format === "sql" ? ".sql.gz" : ".zip")) throw new Error("backup extension differs from content contract");
      const mode = format === "archive" ? "constrained" : "advanced";
      const mappings = [{ database: "main", object: "e2e_items", targetDatabase: "main", targetObject: "e2e_items" }];
      const plan = await planRestore(targetId, path, undefined, mode, mappings, false, "e2e-transfer", "sqlite");
      if (!plan.supported || !plan.planId) throw new Error(`preparation refused ${format}: ${plan.reason ?? ""}`);
      const empty = await invokeBackend<DatabaseQueryResult>("db_execute_query", { sessionId: targetSession, query: "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='e2e_items'" });
      if (String(empty.rows[0]?.[0]) !== "0") throw new Error("preparation changed target");
      const restoreId = await startRestore(plan.planId, crypto.randomUUID(), { allowExisting: false, allowDestructive: mode === "advanced" });
      await wait(restoreId);
      const rows = await invokeBackend<DatabaseQueryResult>("db_execute_query", { sessionId: targetSession, query: "SELECT id, name FROM e2e_items ORDER BY id" });
      if (rows.rowCount !== 3 || String(rows.rows[2]?.[0]) !== "3" || rows.rows[2]?.[1] !== "imported") throw new Error(`restored rows differ: ${format}`);
      log.done(`${format} backup published; preparation zero-write; reviewed restore and independent query matched`);
    } finally {
      await invokeBackend("db_disconnect", { sessionId: targetSession }).catch(() => undefined);
      if (jobId) await invokeBackend("db_backup_delete", { id: jobId }).catch(() => undefined);
    }
  }
}
