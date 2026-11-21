import { acceptHMRUpdate, defineStore } from "pinia";
import { ref } from "vue";
import { mergeTransferTask, transferTerminal, type TransferTask } from "@/domain/database/transfer-tasks";
import { getTransferTask, listTransferTasks, onTransferTask, submitCheckedImport, submitPreparedImport } from "@/services/database-transfer-service";
// Backend records are device-local; dialog selections remain workspace-scoped.
export const useDatabaseTransferTasks = defineStore("database-transfer-tasks", () => {
  const tasks = ref<Record<string, TransferTask>>({});
  const error = ref("");
  // target: the plan's session and database, for the SQL safety check a not-yet-admitted retry needs.
  const pendingImports = ref<Record<string, { key: string; workspaceId: string; target?: { sessionId: string; database: string } }>>({});
  function rememberImport(planId: string, key: string, workspaceId: string, target?: { sessionId: string; database: string }) {
    pendingImports.value[planId] = target ? { key, workspaceId, target } : { key, workspaceId };
  }
  function forgetImport(planId: string) { delete pendingImports.value[planId]; }
  async function retryImport(planId: string) {
    const pending = pendingImports.value[planId];
    if (!pending) return;
    const task = pending.target ? await submitCheckedImport(pending.target, planId, pending.key) : await submitPreparedImport(planId, pending.key);
    if (!task) return;
    merge(task); forgetImport(planId);
  }
  function merge(task: TransferTask) { tasks.value[task.id] = mergeTransferTask(tasks.value[task.id], task); }
  async function refresh(id: string) { const task = await getTransferTask(id); merge(task); return tasks.value[id] ?? task; }
  function attach(kind: TransferTask["kind"], workspaceId = "") {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const stop = onTransferTask((task) => {
      if (!disposed && task.kind === kind && (!workspaceId || task.workspaceId === workspaceId)) {
        merge(task);
        if (!timer && !transferTerminal(task)) timer = setTimeout(() => { timer = undefined; void reconcile(); }, 2000);
      }
    });
    async function reconcile() {
      try { const rows = await listTransferTasks(kind, workspaceId); if (!disposed) { rows.forEach(merge); error.value = ""; } }
      catch (cause) { if (!disposed) error.value = String(cause); }
      if (!disposed) { if (timer) clearTimeout(timer); timer = setTimeout(() => { timer = undefined; void reconcile(); }, document.hidden ? 10000 : 2000); }
    }
    void reconcile();
    return () => { disposed = true; stop(); if (timer) clearTimeout(timer); };
  }
  return { tasks, error, pendingImports, rememberImport, forgetImport, retryImport, merge, refresh, attach };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useDatabaseTransferTasks, import.meta.hot));
}
