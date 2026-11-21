<script setup lang="ts">
/**
 * Scheduled backup management: list, create/edit, enable, run now, cancel,
 * history and open folder. Jobs live in Go (db_backup_*); this dialog only
 * edits them. Run events refresh the list while the dialog is open.
 */
import { computed, onBeforeUnmount, reactive, ref, toRef, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Delete, Edit, Folder, Plus, Refresh, RefreshLeft, VideoPause, VideoPlay, Tickets } from "@element-plus/icons-vue";
import {
  BACKUP_MAX_INTERVAL_HOURS,
  BACKUP_MAX_KEEP_DAYS,
  BACKUP_MAX_KEEP_LAST,
  applyBackupRunEvent,
  backupFileBaseName,
  createBackupJobDraft,
  draftFromBackupJob,
  formatBackupBytes,
  formatBackupDuration,
  isBackupCandidateConnection,
  validateBackupJobDraft,
  type BackupDraftError,
  type BackupJobDraft,
  type BackupJobView,
  type BackupRun,
  type BackupRunStatus,
  type BackupRunTrigger,
  type BackupSchedule,
} from "@/domain/database/backup-jobs";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import {
  cancelBackupJob,
  deleteBackupJob,
  listBackupJobs,
  listBackupRuns,
  onBackupRunEvent,
  revealBackupPath,
  runBackupJobNow,
  saveBackupJob,
} from "@/services/database-backup-service";
import { chooseNativeDirectory, isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";
import { useBackupDiscovery } from "@/composables/use-backup-discovery";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import DatabaseRestoreDialog from "@/components/database/DatabaseRestoreDialog.vue";

const props = defineProps<{
  modelValue: boolean;
  initialConnectionId?: string;
  initialDatabase?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();

const { t, intlLocale } = useVueI18n();
const databaseStore = useDatabaseWorkspaceStore();
const desktop = isDesktopRuntime();

type Mode = "list" | "edit" | "history";
const mode = ref<Mode>("list");
const jobs = ref<BackupJobView[]>([]);
const loading = ref(false);
const saving = ref(false);
const busyJobIds = ref<string[]>([]);
const manualRequestKeys = new Map<string, string>();
const draft = reactive<BackupJobDraft>(createBackupJobDraft());
const showErrors = ref(false);
const historyJob = ref<BackupJobView>();
const historyRuns = ref<BackupRun[]>([]);
const historyLoading = ref(false);
let stopRunEvents: (() => void) | undefined;
let listRequestId = 0;

const restoreOpen = ref(false);
const restorePath = ref("");
const restoreConnectionId = ref("");
function openRestore(path: string, connectionId?: string) {
  restorePath.value = path;
  restoreConnectionId.value = connectionId ?? "";
  restoreOpen.value = true;
}

const weekdayOrder = [1, 2, 3, 4, 5, 6, 0];

const candidateConnections = computed(() => databaseStore.connections.filter(isBackupCandidateConnection));
const connectionNames = computed(() => new Map(databaseStore.connections.map((connection) => [connection.id, connection.name])));
const workspaceScope = useWorkspaceStateScope();
const discoveryActive = computed(() => props.modelValue && mode.value === "edit" && desktop);
const discoveryDatabase = computed(() => draft.databases.length === 1 ? draft.databases[0] ?? "" : "");
const discovery = useBackupDiscovery(toRef(draft, "connectionId"), discoveryDatabase, discoveryActive);
const draftDatabases = computed(() => {
  const cached = databaseStore.schemas[draft.connectionId]?.databases ?? [];
  const loaded = discovery.result.value?.schema.databases ?? [];
  const byName = new Map([...cached, ...loaded].map(item => [item.name, item]));
  return [...byName.values()];
});
const discoveryBlocked = computed(() => {
  if (discovery.loading.value || discovery.error.value) return draft.format === "archive";
  const capability = discovery.result.value?.capability;
  if (!capability) return draft.format === "archive";
  return draft.format === "archive" ? !capability.supported : !capability.sqlFormat;
});
const backupScope = computed(() => discovery.result.value?.capability.scope ?? "table");
// Before discovery answers, keep a saved SQL job editable.
const sqlFormatAvailable = computed(() => discovery.result.value?.capability.sqlFormat ?? draft.format === "sql");
function capabilityText(code: string): string {
  // Codes from a newer backend may not have text yet; show the code then.
  return (t(`database.backup.capability.${code}` as MessageKey) as string | undefined) || code;
}
const capabilityReason = computed(() => {
  const reason = discovery.result.value?.capability.reason;
  return reason ? capabilityText(reason) : "";
});
const formatEvidence = computed(() => discovery.result.value?.capability.formats?.find((format) => format.format === draft.format));
const capabilityLimitations = computed(() =>
  (discovery.result.value?.capability.limitations ?? []).map(capabilityText).join("；"));
// Discovery defaults new untouched drafts only; existing/user-selected formats stay put.
watch(() => discovery.result.value, (result) => {
  if (result && !draft.id && !formatTouched.value) draft.format = result.capability.defaultFormat ?? (result.capability.sqlFormat ? "sql" : "archive");
});
const draftTables = computed(() => {
  if (draft.databases.length !== 1) return [];
  const database = draftDatabases.value.find((item) => item.name === draft.databases[0]);
  return (database?.tables ?? []).filter((table) => !/view|sequence/i.test(table.kind)).map((table) => table.name);
});
const draftErrors = computed(() => new Set(validateBackupJobDraft(draft)));
const editingExisting = computed(() => Boolean(draft.id));

function errorFor(error: BackupDraftError): string {
  return showErrors.value && draftErrors.value.has(error) ? t(`database.backup.error.${error}` as MessageKey) : "";
}

function formatTime(ms: number | undefined): string {
  if (!ms) return "—";
  return new Date(ms).toLocaleString(intlLocale.value, { dateStyle: "short", timeStyle: "short" });
}

function scheduleLabel(schedule: BackupSchedule): string {
  if (schedule.kind === "interval") return t("database.backup.schedule.everyHours", { hours: schedule.intervalHours ?? 0 });
  if (schedule.kind === "daily") return t("database.backup.schedule.dailyAt", { time: schedule.time ?? "" });
  const days = weekdayOrder
    .filter((day) => schedule.weekdays?.includes(day))
    .map((day) => t(`database.backup.weekday.${day}` as MessageKey))
    .join(" ");
  return t("database.backup.schedule.weeklyAt", { days, time: schedule.time ?? "" });
}

function statusLabel(status: BackupRunStatus | undefined): string {
  return status ? t(`database.backup.status.${status}` as MessageKey) : t("database.backup.status.never");
}

function statusType(status: BackupRunStatus | undefined): "success" | "danger" | "warning" | "info" | "primary" {
  if (status === "success") return "success";
  if (status === "failed") return "danger";
  if (status === "cancelled") return "warning";
  if (status === "running") return "primary";
  return "info";
}

function triggerLabel(trigger: BackupRunTrigger): string {
  return t(`database.backup.trigger.${trigger}` as MessageKey);
}

function targetLabel(job: BackupJobView): string {
  const databases = job.targets.map((target) => target.database).join(", ");
  const tables = job.targets[0]?.tables?.length ?? 0;
  return tables ? t("database.backup.targetWithTables", { databases, count: tables }) : databases;
}

async function refresh() {
  if (!desktop) return;
  const requestId = ++listRequestId;
  loading.value = true;
  try {
    const next = await listBackupJobs();
    if (requestId === listRequestId) jobs.value = next;
  } catch (error) {
    if (requestId === listRequestId) ElMessage.error(t("database.backup.loadFailed", { message: nativeErrorMessage(error) }));
  } finally {
    if (requestId === listRequestId) loading.value = false;
  }
}

async function refreshHistory() {
  const job = historyJob.value;
  if (!job) return;
  historyLoading.value = true;
  try {
    historyRuns.value = await listBackupRuns(job.id);
  } catch (error) {
    ElMessage.error(t("database.backup.loadFailed", { message: nativeErrorMessage(error) }));
  } finally {
    historyLoading.value = false;
  }
}

const formatTouched = ref(false);
let hydratingDraft = false;
function resetDraft(next: BackupJobDraft) {
  formatTouched.value = Boolean(next.id);
  hydratingDraft = true;
  Object.assign(draft, next);
  hydratingDraft = false;
  showErrors.value = false;
}

function openCreate() {
  const connectionId =
    candidateConnections.value.find((connection) => connection.id === props.initialConnectionId)?.id ??
    candidateConnections.value.find((connection) => connection.id === databaseStore.activeConnectionId)?.id ??
    candidateConnections.value[0]?.id ??
    "";
  const database = connectionId === props.initialConnectionId ? (props.initialDatabase ?? "") : "";
  resetDraft(createBackupJobDraft(connectionId, database));
  mode.value = "edit";
}

function openEdit(job: BackupJobView) {
  resetDraft(draftFromBackupJob(job));
  mode.value = "edit";
}

async function openHistory(job: BackupJobView) {
  historyJob.value = job;
  historyRuns.value = [];
  mode.value = "history";
  await refreshHistory();
}

function backToList() {
  mode.value = "list";
  historyJob.value = undefined;
  void refresh();
}

watch(
  () => draft.connectionId,
  (next, previous) => {
    if (previous !== undefined && next !== previous && mode.value === "edit" && !hydratingDraft) {
      draft.databases = [];
      draft.tables = [];
    }
  },
  { flush: "sync" }
);

watch(
  () => workspaceScope.scopeId.value,
  () => { mode.value = "list"; }
);

watch(
  () => draft.databases.length,
  (length) => {
    if (length !== 1) draft.tables = [];
  }
);

async function chooseDestination() {
  const selected = await chooseNativeDirectory(t("database.backup.chooseFolder"), draft.destination || undefined).catch(
    () => null
  );
  if (selected) draft.destination = selected;
}

async function submit() {
  showErrors.value = true;
  if (draftErrors.value.size || discoveryBlocked.value) return;
  saving.value = true;
  try {
    await saveBackupJob({ ...draft, databases: [...draft.databases], tables: [...draft.tables], weekdays: [...draft.weekdays] });
    ElMessage.success(t("database.backup.saved"));
    backToList();
  } catch (error) {
    ElMessage.error(t("database.backup.saveFailed", { message: nativeErrorMessage(error) }));
  } finally {
    saving.value = false;
  }
}

async function withJobBusy(job: BackupJobView, action: () => Promise<void>) {
  if (busyJobIds.value.includes(job.id)) return;
  busyJobIds.value = [...busyJobIds.value, job.id];
  try {
    await action();
  } finally {
    busyJobIds.value = busyJobIds.value.filter((id) => id !== job.id);
  }
}

function toggleEnabled(job: BackupJobView, enabled: boolean) {
  void withJobBusy(job, async () => {
    try {
      await saveBackupJob({ ...draftFromBackupJob(job), enabled });
    } catch (error) {
      ElMessage.error(t("database.backup.saveFailed", { message: nativeErrorMessage(error) }));
    }
    await refresh();
  });
}

function runNow(job: BackupJobView) {
  void withJobBusy(job, async () => {
    try {
      const key = manualRequestKeys.get(job.id) ?? crypto.randomUUID();
      manualRequestKeys.set(job.id, key);
      await runBackupJobNow(job.id, key);
      manualRequestKeys.delete(job.id);
      ElMessage.info(t("database.backup.runStarted", { name: job.name }));
    } catch (error) {
      ElMessage.error(t("database.backup.runFailed", { message: nativeErrorMessage(error) }));
    }
    await refresh();
  });
}

function cancelRun(job: BackupJobView) {
  void withJobBusy(job, async () => {
    try {
      await cancelBackupJob(job.id);
    } catch (error) {
      ElMessage.error(nativeErrorMessage(error));
    }
  });
}

async function removeJob(job: BackupJobView) {
  try {
    await ElMessageBox.confirm(t("database.backup.deleteConfirm", { name: job.name }), t("database.backup.delete"), {
      type: "warning",
      confirmButtonText: t("database.backup.delete"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  await withJobBusy(job, async () => {
    try {
      await deleteBackupJob(job.id);
    } catch (error) {
      ElMessage.error(nativeErrorMessage(error));
    }
    await refresh();
  });
}

async function reveal(path: string, select: boolean) {
  try {
    await revealBackupPath(path, select);
  } catch (error) {
    ElMessage.error(t("database.backup.openFolderFailed", { message: nativeErrorMessage(error) }));
  }
}

watch(
  () => props.modelValue,
  (visible) => {
    if (visible) {
      mode.value = "list";
      void refresh();
      stopRunEvents ??= onBackupRunEvent((event) => {
        jobs.value = applyBackupRunEvent(jobs.value, event);
        if (event.run.status !== "running") void refresh();
        if (historyJob.value?.id === event.jobId) void refreshHistory();
      });
    } else {
      stopRunEvents?.();
      stopRunEvents = undefined;
    }
  },
  { immediate: true }
);

onBeforeUnmount(() => {
  stopRunEvents?.();
  stopRunEvents = undefined;
});

function updateVisible(value: boolean) {
  if (saving.value) return;
  emit("update:modelValue", value);
}
</script>

<template>
  <el-dialog
    append-to-body
    :model-value="modelValue"
    class="db-backup-dialog"
    width="960px"
    destroy-on-close
    :close-on-click-modal="false"
    @update:model-value="updateVisible"
  >
    <template #header>
      <div class="db-backup-dialog__header">
        <strong>{{ t("database.backup.title") }}</strong>
        <span v-if="mode === 'list'">{{ t("database.backup.subtitle") }}</span>
        <span v-else-if="mode === 'edit'">{{
          editingExisting ? t("database.backup.editTitle") : t("database.backup.createTitle")
        }}</span>
        <span v-else>{{ t("database.backup.historyTitle", { name: historyJob?.name ?? "" }) }}</span>
      </div>
    </template>

    <el-alert
      v-if="!desktop"
      type="warning"
      :closable="false"
      :title="t('database.backup.desktopRequired')"
      show-icon
    />

    <!-- 任务列表 -->
    <template v-else-if="mode === 'list'">
      <div class="db-backup-toolbar">
        <el-button type="primary" :icon="Plus" :disabled="!candidateConnections.length" @click="openCreate">
          {{ t("database.backup.create") }}
        </el-button>
        <el-button :icon="Refresh" :loading="loading" @click="refresh">{{ t("database.backup.refresh") }}</el-button>
        <el-button :icon="RefreshLeft" @click="openRestore('', props.initialConnectionId)">{{ t("database.restore.fromFile") }}</el-button>
        <span class="db-backup-toolbar__hint">{{ t("database.backup.localTimeHint") }}</span>
      </div>
      <el-table v-loading="loading" :data="jobs" max-height="440" :empty-text="t('database.backup.empty')">
        <el-table-column :label="t('database.backup.name')" min-width="170">
          <template #default="{ row }">
            <div class="db-backup-job">
              <strong>{{ row.name }}</strong>
              <small>{{ connectionNames.get(row.connectionId) ?? t("database.backup.missingConnection") }} · {{ targetLabel(row) }}</small>
            </div>
          </template>
        </el-table-column>
        <el-table-column :label="t('database.backup.schedule.label')" min-width="140">
          <template #default="{ row }">{{ scheduleLabel(row.schedule) }}</template>
        </el-table-column>
        <el-table-column :label="t('database.backup.nextRun')" width="130">
          <template #default="{ row }">{{ row.enabled ? formatTime(row.nextRunAt) : t("database.backup.disabled") }}</template>
        </el-table-column>
        <el-table-column :label="t('database.backup.lastRun')" width="200">
          <template #default="{ row }">
            <div class="db-backup-last-run">
              <el-tooltip
                :disabled="!row.lastRun?.error"
                :content="row.lastRun?.error"
                placement="top"
              >
                <el-tag :type="statusType(row.running ? 'running' : row.lastRun?.status)" effect="plain" size="small">
                  {{ statusLabel(row.running ? "running" : row.lastRun?.status) }}
                </el-tag>
              </el-tooltip>
              <small class="db-backup-muted">{{ formatTime(row.lastRun?.startedAt) }}</small>
            </div>
          </template>
        </el-table-column>
        <el-table-column :label="t('database.backup.enabled')" width="72" align="center">
          <template #default="{ row }">
            <el-switch
              :model-value="row.enabled"
              size="small"
              :aria-label="t('database.backup.enabled')"
              :disabled="busyJobIds.includes(row.id)"
              @update:model-value="toggleEnabled(row, Boolean($event))"
            />
          </template>
        </el-table-column>
        <el-table-column :label="t('database.backup.actions')" width="180" align="right">
          <template #default="{ row }">
            <div class="db-backup-actions">
              <el-button
                v-if="row.running"
                text
                size="small"
                :icon="VideoPause"
                :title="t('database.backup.cancelRun')"
                :aria-label="t('database.backup.cancelRun')"
                :disabled="busyJobIds.includes(row.id)"
                @click="cancelRun(row)"
              />
              <el-button
                v-else
                text
                size="small"
                :icon="VideoPlay"
                :title="t('database.backup.runNow')"
                :aria-label="t('database.backup.runNow')"
                :disabled="busyJobIds.includes(row.id)"
                @click="runNow(row)"
              />
              <el-button
                text
                size="small"
                :icon="Tickets"
                :title="t('database.backup.history')"
                :aria-label="t('database.backup.history')"
                @click="openHistory(row)"
              />
              <el-button
                text
                size="small"
                :icon="Folder"
                :title="t('database.backup.openFolder')"
                :aria-label="t('database.backup.openFolder')"
                @click="reveal(row.destination, false)"
              />
              <el-button
                text
                size="small"
                :icon="Edit"
                :title="t('database.backup.edit')"
                :aria-label="t('database.backup.edit')"
                :disabled="row.running"
                @click="openEdit(row)"
              />
              <el-button
                text
                size="small"
                type="danger"
                :icon="Delete"
                :title="t('database.backup.delete')"
                :aria-label="t('database.backup.delete')"
                :disabled="busyJobIds.includes(row.id)"
                @click="removeJob(row)"
              />
            </div>
          </template>
        </el-table-column>
      </el-table>
    </template>

    <!-- 编辑表单 -->
    <el-scrollbar v-else-if="mode === 'edit'" max-height="62vh">
      <el-form class="db-backup-form" label-position="right" label-width="120px" @submit.prevent="submit">
        <el-form-item :label="t('database.backup.name')" :error="errorFor('name')" required>
          <el-input v-model="draft.name" maxlength="120" :placeholder="t('database.backup.namePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('database.backup.connection')" :error="errorFor('connection')" required>
          <el-select v-model="draft.connectionId" filterable :placeholder="t('database.backup.selectConnection')">
            <el-option
              v-for="connection in candidateConnections"
              :key="connection.id"
              :label="connection.name"
              :value="connection.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('database.backup.databases')" :error="errorFor('databases')" required>
          <el-select
            v-model="draft.databases"
            multiple
            filterable
            allow-create
            default-first-option
            :reserve-keyword="false"
            :loading="discovery.loading.value"
            :placeholder="t('database.backup.databasesPlaceholder')"
          >
            <el-option v-for="database in draftDatabases" :key="database.name" :label="database.name" :value="database.name" />
          </el-select>
          <div class="db-backup-form__hint">{{ t("database.backup.databasesHint") }}</div>
          <el-button text :loading="discovery.loading.value" @click="discovery.refresh()">{{ t("database.backup.refresh") }}</el-button>
          <el-alert v-if="discovery.error.value || capabilityReason" type="error" :closable="false" :title="discovery.error.value || capabilityReason" />
          <el-alert v-else-if="capabilityLimitations" type="warning" :closable="false" :title="capabilityLimitations" />
          <el-alert v-if="formatEvidence" type="info" :closable="false" :title="t('database.backup.formatEvidence', { constrained: formatEvidence.constrained ? t('database.backup.subsetOnly') : t('database.backup.advancedOnly'), evidence: formatEvidence.verification })" />
        </el-form-item>
        <el-form-item :label="t(`database.backup.scope.${backupScope}` as MessageKey)" :error="errorFor('tablesNeedOneDatabase')">
          <el-select
            v-model="draft.tables"
            multiple
            filterable
            allow-create
            default-first-option
            collapse-tags
            collapse-tags-tooltip
            :max-collapse-tags="6"
            :reserve-keyword="false"
            :disabled="draft.databases.length !== 1 || backupScope === 'keys' || backupScope === 'graph'"
            :loading="discovery.loading.value"
            :placeholder="t(backupScope === 'keys' || backupScope === 'graph' ? 'database.backup.wholeScopeOnly' : 'database.backup.tablesPlaceholder')"
          >
            <el-option v-for="table in draftTables" :key="table" :label="table" :value="table" />
          </el-select>
        </el-form-item>

        <el-form-item :label="t('database.backup.format')">
          <el-select v-model="draft.format" @change="formatTouched = true">
            <el-option value="archive" :label="t('database.backup.archiveFormat')" />
            <el-option value="sql" :label="t('database.backup.sqlFormat')" :disabled="!sqlFormatAvailable" />
          </el-select>
          <div v-if="!sqlFormatAvailable" class="db-backup-form__hint">{{ t("database.backup.sqlFormatUnavailable") }}</div>
        </el-form-item>

        <el-form-item :label="t('database.backup.schedule.label')" required>
          <div class="db-backup-form__row">
            <el-radio-group v-model="draft.scheduleKind">
              <el-radio-button value="interval">{{ t("database.backup.schedule.interval") }}</el-radio-button>
              <el-radio-button value="daily">{{ t("database.backup.schedule.daily") }}</el-radio-button>
              <el-radio-button value="weekly">{{ t("database.backup.schedule.weekly") }}</el-radio-button>
            </el-radio-group>
            <template v-if="draft.scheduleKind === 'interval'">
              <el-input-number
                v-model="draft.intervalHours"
                :min="1"
                :max="BACKUP_MAX_INTERVAL_HOURS"
                :step="1"
                step-strictly
                :aria-label="t('database.backup.schedule.hours')"
              />
              <span>{{ t("database.backup.schedule.hours") }}</span>
            </template>
            <el-time-select
              v-else
              v-model="draft.time"
              start="00:00"
              step="00:15"
              end="23:45"
              editable
              :clearable="false"
              :aria-label="t('database.backup.schedule.time')"
            />
          </div>
          <el-checkbox-group v-if="draft.scheduleKind === 'weekly'" v-model="draft.weekdays" class="db-backup-form__weekdays">
            <el-checkbox-button v-for="day in weekdayOrder" :key="day" :value="day">
              {{ t(`database.backup.weekday.${day}` as MessageKey) }}
            </el-checkbox-button>
          </el-checkbox-group>
          <div v-if="errorFor('interval') || errorFor('time') || errorFor('weekdays')" class="db-backup-form__error">
            {{ errorFor("interval") || errorFor("time") || errorFor("weekdays") }}
          </div>
        </el-form-item>

        <el-form-item :label="t('database.backup.destination')" :error="errorFor('destination')" required>
          <div class="db-backup-form__row db-backup-form__row--grow">
            <el-input v-model="draft.destination" :placeholder="t('database.backup.destinationPlaceholder')" />
            <el-button :icon="Folder" @click="chooseDestination">{{ t("database.backup.chooseFolder") }}</el-button>
          </div>
        </el-form-item>
        <el-form-item :label="t('database.backup.fileNamePrefix')">
          <el-input v-model="draft.fileNamePrefix" maxlength="64" :placeholder="t('database.backup.fileNamePrefixPlaceholder')" />
          <div class="db-backup-form__hint">{{ t(draft.format === "archive" ? "database.backup.archiveFileHint" : "database.backup.fileNameHint") }}</div>
        </el-form-item>
        <el-form-item :label="t('database.backup.retention')" :error="errorFor('retention')">
          <div class="db-backup-form__row">
            <span>{{ t("database.backup.keepLast") }}</span>
            <el-input-number v-model="draft.keepLast" :min="0" :max="BACKUP_MAX_KEEP_LAST" step-strictly :aria-label="t('database.backup.keepLast')" />
            <span>{{ t("database.backup.keepDays") }}</span>
            <el-input-number v-model="draft.keepDays" :min="0" :max="BACKUP_MAX_KEEP_DAYS" step-strictly :aria-label="t('database.backup.keepDays')" />
          </div>
          <div class="db-backup-form__hint">{{ t("database.backup.retentionHint") }}</div>
        </el-form-item>
        <el-form-item :label="t('database.backup.options')">
          <div class="db-backup-form__row">
            <el-checkbox v-model="draft.gzip">{{ t("database.backup.gzip") }}</el-checkbox>
            <el-checkbox v-model="draft.enabled">{{ t("database.backup.enabled") }}</el-checkbox>
            <el-checkbox v-model="draft.notifyOnSuccess">{{ t("database.backup.notifyOnSuccess") }}</el-checkbox>
          </div>
          <div class="db-backup-form__hint">{{ t("database.backup.credentialsHint") }}</div>
        </el-form-item>
      </el-form>
    </el-scrollbar>

    <!-- 运行历史 -->
    <template v-else>
      <el-table v-loading="historyLoading" :data="historyRuns" max-height="440" :empty-text="t('database.backup.historyEmpty')">
        <el-table-column :label="t('database.backup.startedAt')" width="150">
          <template #default="{ row }">{{ formatTime(row.startedAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('database.backup.trigger.label')" width="90">
          <template #default="{ row }">{{ triggerLabel(row.trigger) }}</template>
        </el-table-column>
        <el-table-column :label="t('database.backup.statusLabel')" width="96">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" effect="plain" size="small">{{ statusLabel(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('database.backup.result')" min-width="260">
          <template #default="{ row }">
            <div class="db-backup-run">
              <span v-if="row.file" :title="row.file">{{ backupFileBaseName(row.file) }}</span>
              <small v-if="row.status === 'success'" class="db-backup-muted">
                {{ t("database.backup.runSummary", {
                  size: formatBackupBytes(row.sizeBytes),
                  tables: row.tables ?? 0,
                  rows: row.rows ?? 0,
                  duration: formatBackupDuration(row.durationMs),
                }) }}<template v-if="row.removed">
                  · {{ t("database.backup.removedOld", { count: row.removed }) }}</template>
              </small>
              <span v-if="row.error" class="db-backup-run__error">{{ row.error }}</span>
              <el-tooltip v-if="row.warnings?.length" placement="top">
                <template #content>
                  <div v-for="(warning, index) in row.warnings" :key="index">{{ warning }}</div>
                </template>
                <small class="db-backup-run__warning">{{ t("database.backup.warningCount", { count: row.warnings.length }) }}</small>
              </el-tooltip>
            </div>
          </template>
        </el-table-column>
        <el-table-column width="96" align="right">
          <template #default="{ row }">
            <el-button
              v-if="row.file && row.status === 'success'"
              text
              size="small"
              :icon="RefreshLeft"
              :title="t('database.restore.fromRun')"
              :aria-label="t('database.restore.fromRun')"
              @click="openRestore(row.file, historyJob?.connectionId)"
            />
            <el-button
              v-if="row.file"
              text
              size="small"
              :icon="Folder"
              :title="t('database.backup.showFile')"
              :aria-label="t('database.backup.showFile')"
              @click="reveal(row.file, true)"
            />
          </template>
        </el-table-column>
      </el-table>
    </template>

    <template #footer>
      <template v-if="mode === 'edit'">
        <el-button :disabled="saving" @click="backToList">{{ t("common.cancel") }}</el-button>
        <el-button type="primary" :loading="saving" :disabled="discoveryBlocked" @click="submit">{{ t("common.save") }}</el-button>
      </template>
      <template v-else-if="mode === 'history'">
        <el-button :icon="Refresh" :loading="historyLoading" @click="refreshHistory">{{ t("database.backup.refresh") }}</el-button>
        <el-button @click="backToList">{{ t("database.backup.back") }}</el-button>
      </template>
      <el-button v-else @click="updateVisible(false)">{{ t("common.close") }}</el-button>
    </template>
  </el-dialog>
  <DatabaseRestoreDialog
    v-if="desktop"
    v-model="restoreOpen"
    :initial-path="restorePath"
    :initial-connection-id="restoreConnectionId"
  />
</template>

<style scoped>
.db-backup-dialog__header {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.db-backup-dialog__header span,
.db-backup-muted,
.db-backup-toolbar__hint,
.db-backup-form__hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.db-backup-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.db-backup-toolbar__hint {
  margin-left: auto;
}
.db-backup-job,
.db-backup-run {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.db-backup-last-run {
  display: flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
.db-backup-actions {
  display: flex;
  justify-content: flex-end;
  flex-wrap: nowrap;
  gap: 2px;
}
.db-backup-actions .el-button {
  margin-left: 0;
  padding: 4px 6px;
}
.db-backup-job small {
  overflow: hidden;
  color: var(--el-text-color-secondary);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.db-backup-run span {
  overflow-wrap: anywhere;
}
.db-backup-run__error,
.db-backup-form__error {
  color: var(--el-color-danger);
  font-size: 12px;
}
.db-backup-run__warning {
  color: var(--el-color-warning);
  cursor: default;
}
.db-backup-form {
  padding-right: 12px;
}
.db-backup-form :deep(.el-select) {
  width: 100%;
}
.db-backup-form__row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.db-backup-form__row--grow {
  width: 100%;
  flex-wrap: nowrap;
}
.db-backup-form__weekdays {
  margin-top: 8px;
}
.db-backup-form__hint {
  width: 100%;
  line-height: 1.5;
}
</style>
