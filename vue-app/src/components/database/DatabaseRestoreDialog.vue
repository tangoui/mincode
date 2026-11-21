<script setup lang="ts">
/**
 * Restore a backup into a saved connection: choose the file and the target,
 * review the preflight plan, confirm explicitly, then follow progress. The
 * restore runs in Go; closing the dialog does not stop it, cancelling does.
 */
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { FolderOpened } from "@element-plus/icons-vue";
import { backupFileBaseName, formatBackupBytes, formatBackupDuration } from "@/domain/database/backup-jobs";
import { useVueI18n } from "@/i18n/messages";
import {
  planRestore,
  startRestore,
  type RestoreEvent,
  type RestorePlan,
  type RestoreMapping,
} from "@/services/database-restore-service";
import { chooseNativeFile, nativeErrorMessage } from "@/services/desktop-bridge";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { useDatabaseTransferTasks } from "@/stores/database-transfer-tasks";
import { cancelTransferTask, listTransferTasks } from "@/services/database-transfer-service";
import { transferTerminal, type TransferTask } from "@/domain/database/transfer-tasks";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

const props = defineProps<{
  modelValue: boolean;
  /** Backup file to restore (from a history row); empty lets the user pick. */
  initialPath?: string;
  initialConnectionId?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();

const { t } = useVueI18n();
const databaseStore = useDatabaseWorkspaceStore();
const taskStore = useDatabaseTransferTasks();
const workspaceScope = useWorkspaceStateScope();
const sourceDialect = ref("");
const taskSnapshot = ref<TransferTask>();
const recentTask = ref<TransferTask>();
let disposed = false;
let attachmentVersion = 0;
const knownObjects = ref<RestorePlan["objects"]>([]);
const mode = ref<"constrained" | "advanced">("constrained");
const targetDatabase = ref("");
let stopTaskAttachment: (() => void) | undefined;
let taskPoll: ReturnType<typeof setTimeout> | undefined;
const requestKey = ref(crypto.randomUUID());
const submitting = ref(false);

const path = ref("");
const connectionId = ref("");
const plan = ref<RestorePlan>();
const planning = ref(false);
const planError = ref("");
const allowExisting = ref(false);
const allowDestructive = ref(false);
const restoreId = ref("");
const progress = ref<RestoreEvent>();
const finished = ref<RestoreEvent>();
let planController: AbortController | undefined;
let planVersion = 0;

const connections = computed(() => databaseStore.connections);
const connectionName = computed(() => connections.value.find((c) => c.id === connectionId.value)?.name ?? "");
const running = computed(() => Boolean(restoreId.value) && !finished.value);
const objectCount = computed(() => plan.value?.objects.filter((o) => o.object).length ?? 0);
const recordCount = computed(() => plan.value?.objects.reduce((sum, o) => sum + o.records, 0) ?? 0);
const canStart = computed(() => {
  const p = plan.value;
  if (!p?.supported || !p.planId || planning.value || running.value || submitting.value || finished.value) return false;
  if (p.conflicts.length && !allowExisting.value) return false;
  return !(mode.value === "advanced" && !allowDestructive.value);
});
const percent = computed(() => {
  const e = progress.value;
  if (!e?.totalBytes || !e.bytesRead) return 0;
  return Math.min(99, Math.round((e.bytesRead / e.totalBytes) * 100));
});

function reset() {
  planController?.abort();
  planVersion++;
  if (taskPoll) clearTimeout(taskPoll);
  taskSnapshot.value = undefined;
  recentTask.value = undefined;
  knownObjects.value = [];
  path.value = props.initialPath ?? "";
  connectionId.value = props.initialConnectionId && connections.value.some((c) => c.id === props.initialConnectionId)
    ? props.initialConnectionId : "";
  plan.value = undefined;
  planError.value = "";
  planning.value = false;
  allowExisting.value = false;
  allowDestructive.value = false;
  restoreId.value = "";
  requestKey.value = crypto.randomUUID();
  progress.value = undefined;
  finished.value = undefined;
}

async function refreshPlan() {
  planController?.abort();
  const version = ++planVersion;
  const previousObjects = knownObjects.value;
  plan.value = undefined;
  planError.value = "";
  allowDestructive.value = false;
  if (!path.value || !connectionId.value) return;
  const controller = new AbortController();
  planController = controller;
  planning.value = true;
  try {
    const mappings: RestoreMapping[] = previousObjects.map((object) => ({ database: object.database, object: object.object, targetDatabase: targetDatabase.value, targetObject: object.object }));
    const next = await planRestore(connectionId.value, path.value, controller.signal, mode.value, mappings, allowExisting.value, workspaceScope.scopeId.value, sourceDialect.value);
    if (version === planVersion) {
      plan.value = next; knownObjects.value = next.objects;
      requestKey.value = crypto.randomUUID();
      // When discovery returned objects after a mapping was typed, prepare once
      // more with those explicit identities. Do not repeatedly mint plans.
      if (!previousObjects.length && next.objects.length && targetDatabase.value && mode.value === "constrained") void refreshPlan();
    }
  } catch (cause) {
    if (version === planVersion && !controller.signal.aborted) planError.value = nativeErrorMessage(cause);
  } finally {
    if (version === planVersion) planning.value = false;
  }
}

async function chooseFile() {
  const selected = await chooseNativeFile(t("database.restore.chooseFile"), [
    { DisplayName: t("database.restore.backupFiles"), Pattern: "*.zip;*.sql;*.gz" },
  ]).catch(() => null);
  if (selected) path.value = selected;
}

async function start() {
  const p = plan.value;
  if (!p || !canStart.value) return;
  submitting.value = true;
  try {
    await ElMessageBox.confirm(
      t("database.restore.confirmBody", { file: backupFileBaseName(path.value), connection: connectionName.value }),
      t("database.restore.confirmTitle"),
      { type: "warning", confirmButtonText: t("database.restore.start"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    submitting.value = false;
    return;
  }
  if (plan.value?.planId !== p.planId) { submitting.value = false; return; }
  try {
    progress.value = undefined;
    finished.value = undefined;
    submitting.value = true;
    restoreId.value = await startRestore(p.planId ?? "", requestKey.value, {
      allowExisting: allowExisting.value, allowDestructive: allowDestructive.value,
    });
    await reconcileTask(restoreId.value);
  } catch (cause) {
    ElMessage.error(t("database.restore.failed", { message: nativeErrorMessage(cause) }));
  } finally { submitting.value = false; }
}

function applyTask(task: TransferTask) {
  if (task.id !== restoreId.value || (taskSnapshot.value && taskSnapshot.value.revision >= task.revision)) return;
  taskSnapshot.value = task;
  const event: RestoreEvent = { restoreId: task.id, statements: 0, records: task.result.records, bytesRead: 0, totalBytes: 0, done: transferTerminal(task) };
  if (transferTerminal(task)) {
    event.result = { statements: 0, records: task.result.records, objects: task.result.objects, durationMs: 0, warnings: [t("database.restore.effects", { certainty: task.result.certainty })] };
    if (task.state !== "succeeded") event.error = `${task.state}: ${task.errorCode ?? ""}`;
    finished.value = event;
  } else progress.value = event;
}
async function reconcileTask(id: string) {
  const version = attachmentVersion;
  try { const task = await taskStore.refresh(id); if (!disposed && version === attachmentVersion) applyTask(task); }
  catch (cause) { planError.value = nativeErrorMessage(cause); }
  if (!disposed && version === attachmentVersion && !finished.value && restoreId.value === id) taskPoll = setTimeout(() => void reconcileTask(id), 2000);
}
async function cancel() {
  if (!restoreId.value) return;
  try { applyTask(await cancelTransferTask(restoreId.value)); }
  catch (cause) { ElMessage.error(nativeErrorMessage(cause)); }
}
onBeforeUnmount(() => { disposed = true; attachmentVersion++; stopTaskAttachment?.(); if (taskPoll) clearTimeout(taskPoll); planController?.abort(); });

watch([path, connectionId, mode, targetDatabase, allowExisting, sourceDialect], () => { if (!running.value && !finished.value) void refreshPlan(); });
// Opening with a preselected file and target plans immediately: reset()
// assigns both before the watcher above can observe the change.
watch(() => props.modelValue, (open) => {
  if (!open) { attachmentVersion++; stopTaskAttachment?.(); if (taskPoll) clearTimeout(taskPoll); return; }
  const version = ++attachmentVersion;
  reset();
  stopTaskAttachment?.(); stopTaskAttachment = taskStore.attach("restore", workspaceScope.scopeId.value);
  void listTransferTasks("restore", workspaceScope.scopeId.value).then((tasks) => {
    if (disposed || version !== attachmentVersion) return;
    const latest = tasks.find((task) => task.connectionId === connectionId.value);
    recentTask.value = latest;
    if (latest && !transferTerminal(latest)) { restoreId.value = latest.id; void reconcileTask(latest.id); }
  }).catch((cause) => { planError.value = nativeErrorMessage(cause); });
  void refreshPlan();
}, { immediate: true });

watch(() => workspaceScope.scopeId.value, () => { attachmentVersion++; reset(); close(); });
watch(() => taskStore.tasks[restoreId.value], (task) => { if (task) applyTask(task); });

function newRestore() {
  attachmentVersion++;
  if (taskPoll) clearTimeout(taskPoll);
  restoreId.value = ""; finished.value = undefined; progress.value = undefined; taskSnapshot.value = undefined;
  requestKey.value = crypto.randomUUID();
  void refreshPlan();
}
function close() {
  emit("update:modelValue", false);
}
</script>

<template>
  <el-dialog
    append-to-body
    :model-value="modelValue"
    width="640px"
    :title="t('database.restore.title')"
    :close-on-click-modal="false"
    class="db-restore-dialog"
    @update:model-value="close"
  >
    <el-alert :closable="false" type="warning" :title="t('database.restore.safetyHint')" show-icon />
    <el-form label-position="right" label-width="96px" class="db-restore-form">
      <el-form-item :label="t('database.restore.file')">
        <div class="db-restore-form__row">
          <el-input :model-value="path" readonly :placeholder="t('database.restore.filePlaceholder')" />
          <el-button :icon="FolderOpened" :disabled="running" @click="chooseFile">{{ t("database.restore.browse") }}</el-button>
        </div>
      </el-form-item>
      <el-form-item :label="t('database.restore.mode')">
        <el-select v-model="mode" :disabled="running"><el-option value="constrained" :label="t('database.restore.constrained')" /><el-option value="advanced" :label="t('database.restore.advanced')" /></el-select>
      </el-form-item>
      <el-form-item v-if="mode === 'advanced'" :label="t('database.restore.sourceDialect')">
        <el-select v-model="sourceDialect" :disabled="running"><el-option v-for="dialect in ['mysql','postgres','sqlite','sqlserver','oracle','dm','duckdb','firebird','clickhouse']" :key="dialect" :label="dialect" :value="dialect" /></el-select>
      </el-form-item>
      <el-form-item v-if="mode === 'constrained'" :label="t('database.restore.targetDatabase')">
        <el-input v-model="targetDatabase" :disabled="running" :placeholder="t('database.restore.targetDatabaseHint')" />
      </el-form-item>
      <el-form-item :label="t('database.restore.target')">
        <el-select v-model="connectionId" filterable :disabled="running" :placeholder="t('database.restore.targetPlaceholder')">
          <el-option v-for="c in connections" :key="c.id" :label="c.name" :value="c.id" />
        </el-select>
      </el-form-item>
      <el-form-item><el-checkbox v-model="allowExisting" :disabled="running">{{ t('database.restore.allowExisting') }}</el-checkbox></el-form-item>
    </el-form>

    <div v-loading="planning" class="db-restore-plan">
      <el-alert v-if="planError" type="error" :closable="false" :title="planError" />
      <el-alert v-else-if="plan && !plan.supported" type="error" :closable="false" :title="plan.reason || t('database.restore.unsupported')" />
      <template v-else-if="plan">
        <el-descriptions :column="2" size="small" border>
          <el-descriptions-item :label="t('database.restore.format')">
            {{ plan.format === "sql" ? t("database.restore.formatSql") : t("database.restore.formatArchive") }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('database.restore.size')">{{ formatBackupBytes(plan.sizeBytes) }}</el-descriptions-item>
          <el-descriptions-item :label="t('database.restore.objects')">{{ objectCount }}</el-descriptions-item>
          <el-descriptions-item :label="plan.format === 'sql' ? t('database.restore.statements') : t('database.restore.records')">
            {{ plan.format === "sql" ? plan.statements : recordCount }}
          </el-descriptions-item>
        </el-descriptions>
        <el-alert
          v-if="plan.conflicts.length"
          type="warning"
          :closable="false"
          :title="t('database.restore.conflicts', { count: plan.conflicts.length, names: plan.conflicts.slice(0, 5).join('、') })"
        />

        <el-alert v-for="(warning, index) in plan.warnings" :key="index" type="warning" :closable="false" :title="warning" />
        <el-checkbox v-if="mode === 'advanced'" v-model="allowDestructive" :disabled="running">
          {{ t("database.restore.allowDestructive") }}
        </el-checkbox>
      </template>
      <el-empty v-else-if="!planning" :image-size="56" :description="t('database.restore.choosePrompt')" />
    </div>

    <el-alert
      v-if="recentTask && !restoreId"
      :closable="false"
      type="info"
      :title="t('database.restore.previousResult')"
      :description="`${t('database.restore.taskState', { state: recentTask.state, records: recentTask.result.records, certainty: recentTask.result.certainty })}${recentTask.errorCode ? ` — ${recentTask.errorCode}` : ''}`"
    />
    <div v-if="restoreId" class="db-restore-progress">
      <el-alert v-if="taskSnapshot" :closable="false" type="info" :title="t('database.restore.taskState', { state: taskSnapshot.state, records: taskSnapshot.result.records, certainty: taskSnapshot.result.certainty })" />
      <el-progress
        :percentage="finished ? 100 : percent"
        :status="finished?.error ? 'exception' : finished ? 'success' : undefined"
        :indeterminate="running && !percent"
      />
      <small v-if="running">
        {{ t("database.restore.progress", {
          object: progress?.object ?? "",
          statements: progress?.statements ?? 0,
          records: progress?.records ?? 0,
        }) }}
      </small>
      <template v-else-if="finished">
        <span v-if="finished.error" class="db-restore-progress__error">{{ finished.error }}</span>
        <small v-else-if="finished.result">
          {{ t("database.restore.summary", {
            objects: finished.result.objects,
            statements: finished.result.statements,
            records: finished.result.records,
            duration: formatBackupDuration(finished.result.durationMs),
          }) }}
        </small>
        <el-alert
          v-for="(warning, index) in finished.result?.warnings ?? []"
          :key="index"
          type="warning"
          :closable="false"
          :title="warning"
        />
      </template>
    </div>

    <template #footer>
      <el-button v-if="finished" @click="newRestore">{{ t('database.restore.newRestore') }}</el-button>
      <el-button v-if="running" type="danger" plain @click="cancel">{{ t("database.restore.cancel") }}</el-button>
      <el-button @click="close">{{ running ? t("database.restore.hide") : t("common.close") }}</el-button>
      <el-button v-if="!restoreId || finished" type="primary" :disabled="!canStart" @click="start">
        {{ t("database.restore.start") }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.db-restore-form {
  margin-top: 12px;
}
.db-restore-form__row {
  display: flex;
  width: 100%;
  gap: 8px;
}
.db-restore-plan {
  display: flex;
  min-height: 80px;
  flex-direction: column;
  gap: 8px;
}
.db-restore-progress {
  display: flex;
  margin-top: 12px;
  flex-direction: column;
  gap: 6px;
}
.db-restore-progress small {
  color: var(--app-text-muted);
}
.db-restore-progress__error {
  color: var(--el-color-danger);
  word-break: break-word;
}
</style>
