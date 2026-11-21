<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Switch } from "@element-plus/icons-vue";
import type {
  DatabaseConnection,
  DatabaseSchemaChange,
  DatabaseSchemaComparison,
  DatabaseSchemaSyncExecutionResult,
  DatabaseSchemaSyncPlan,
  DatabaseSchemaSyncRisk,
  DatabaseSchemaSyncSelection,
} from "@/domain/database/types";
import {
  defaultSchemaSyncSelection,
  normalizeSchemaSyncSelection,
  schemaSyncDialectForDbType,
  schemaSyncExecutionStatus,
  schemaSyncPlanMatchesSelection,
} from "@/domain/database/schema-sync";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

const props = defineProps<{
  modelValue: boolean;
  initialConnectionId?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t } = useVueI18n();
const databaseStore = useDatabaseWorkspaceStore();

const supportedTypes = new Set(["mysql", "postgres", "sqlite", "sqlserver", "oracle", "dameng", "dm"]);
const sourceConnectionId = ref("");
const sourceDatabase = ref("");
const targetConnectionId = ref("");
const targetDatabase = ref("");
const comparison = ref<DatabaseSchemaComparison>();
const selectedChangeIds = ref<string[]>([]);
const plan = ref<DatabaseSchemaSyncPlan>();
const execution = ref<DatabaseSchemaSyncExecutionResult>();
const executionPlan = ref<DatabaseSchemaSyncPlan>();
const loadingComparison = ref(false);
const loadingPlan = ref(false);
const executing = ref(false);
const error = ref("");
const refreshError = ref("");
const comparisonSelectionKey = ref("");
let comparisonRequestId = 0;
let planRequestId = 0;
let executionRequestId = 0;

const connectedConnections = computed(() =>
  // 内置驱动按类型白名单判断；插件驱动需方言可生成 DDL 且声明了所需元数据功能。
  databaseStore.connectedConnections.filter((connection) =>
    connection.driver
      ? databaseStore.connectionCapabilities(connection).schemaSync
      : supportedTypes.has(connection.dbType)
  )
);
// 插件驱动连接按其 SQL 方言参与对比，可与同方言的内置连接互为源和目标。
function syncDialect(connection: DatabaseConnection | undefined) {
  if (!connection) return undefined;
  return schemaSyncDialectForDbType(databaseStore.connectionCapabilities(connection).dialectType);
}
const sourceConnection = computed(() => connectionById(sourceConnectionId.value));
const targetConnections = computed(() =>
  connectedConnections.value.filter(
    (connection) =>
      connection.id !== sourceConnectionId.value &&
      syncDialect(connection) === syncDialect(sourceConnection.value)
  )
);
const targetConnection = computed(() => connectionById(targetConnectionId.value));
const sourceDatabases = computed(() => databaseStore.schemas[sourceConnectionId.value]?.databases ?? []);
const targetDatabases = computed(() => databaseStore.schemas[targetConnectionId.value]?.databases ?? []);
const selection = computed<DatabaseSchemaSyncSelection>(() => ({
  sourceConnectionId: sourceConnectionId.value,
  sourceDatabase: sourceDatabase.value,
  targetConnectionId: targetConnectionId.value,
  targetDatabase: targetDatabase.value,
}));
const canCompare = computed(
  () =>
    databaseStore.desktopRuntime &&
    Boolean(
      sourceConnection.value &&
        targetConnection.value &&
        sourceDatabase.value &&
        targetDatabase.value &&
        syncDialect(sourceConnection.value) === syncDialect(targetConnection.value)
    ) &&
    !loadingComparison.value &&
    !loadingPlan.value &&
    !executing.value
);
const selectedChanges = computed(() => {
  const selected = new Set(selectedChangeIds.value);
  return comparison.value?.changes.filter((change) => selected.has(change.id)) ?? [];
});
const planIsCurrent = computed(() =>
  comparisonSelectionKey.value === selectionKey(selection.value) &&
  schemaSyncPlanMatchesSelection(
    plan.value,
    selection.value,
    comparison.value?.source.fingerprint,
    comparison.value?.target.fingerprint,
    selectedChangeIds.value,
    syncDialect(targetConnection.value)
  )
);
const riskCounts = computed(() => {
  const counts: Record<DatabaseSchemaSyncRisk, number> = { safe: 0, review: 0, dangerous: 0 };
  for (const change of comparison.value?.changes ?? []) counts[change.risk] += 1;
  return counts;
});
const executionDdlFailed = computed(() => execution.value?.statements.some((statement) => statement.status === "failed") ?? false);
const executionStatements = computed(() =>
  (executionPlan.value?.statements ?? []).map((statement) => ({
    ...statement,
    execution: schemaSyncExecutionStatus(execution.value, statement.changeId),
  }))
);
const isCompactDialog = computed(
  () => !comparison.value && !plan.value && !execution.value && !executionPlan.value
);
const canSwap = computed(
  () =>
    Boolean(sourceConnectionId.value && targetConnectionId.value) &&
    !executing.value &&
    !loadingComparison.value &&
    !loadingPlan.value
);

function connectionById(id: string): DatabaseConnection | undefined {
  return connectedConnections.value.find((connection) => connection.id === id);
}

function selectionKey(value: DatabaseSchemaSyncSelection): string {
  return JSON.stringify(value);
}

function invalidateComparison() {
  comparisonRequestId += 1;
  planRequestId += 1;
  comparison.value = undefined;
  selectedChangeIds.value = [];
  plan.value = undefined;
  comparisonSelectionKey.value = "";
}

function resetResults() {
  executionRequestId += 1;
  invalidateComparison();
  execution.value = undefined;
  executionPlan.value = undefined;
  error.value = "";
  refreshError.value = "";
}

function initializeSelection() {
  const preferred = connectedConnections.value.find(
    (connection) => connection.id === props.initialConnectionId
  );
  sourceConnectionId.value = preferred?.id ?? connectedConnections.value[0]?.id ?? "";
  sourceDatabase.value = databaseStore.schemas[sourceConnectionId.value]?.databases[0]?.name ?? "";
  targetConnectionId.value = targetConnections.value[0]?.id ?? "";
  targetDatabase.value = databaseStore.schemas[targetConnectionId.value]?.databases[0]?.name ?? "";
  resetResults();
}

watch(
  () => props.modelValue,
  (visible) => {
    if (visible) initializeSelection();
  }
);
watch(sourceConnectionId, () => {
  sourceDatabase.value = keepDatabase(sourceDatabases.value, sourceDatabase.value);
  if (!targetConnections.value.some((connection) => connection.id === targetConnectionId.value)) {
    targetConnectionId.value = targetConnections.value[0]?.id ?? "";
  }
  targetDatabase.value = keepDatabase(targetDatabases.value, targetDatabase.value);
  resetResults();
});
watch(targetConnectionId, () => {
  targetDatabase.value = keepDatabase(targetDatabases.value, targetDatabase.value);
  resetResults();
});
watch([sourceDatabase, targetDatabase], resetResults);

function keepDatabase(databases: { name: string }[], current: string): string {
  return databases.some((database) => database.name === current) ? current : databases[0]?.name ?? "";
}

function swapSides() {
  if (!canSwap.value) return;
  [sourceConnectionId.value, targetConnectionId.value] = [targetConnectionId.value, sourceConnectionId.value];
  [sourceDatabase.value, targetDatabase.value] = [targetDatabase.value, sourceDatabase.value];
}

async function compare() {
  if (!canCompare.value) return;
  const requestId = ++comparisonRequestId;
  const requestSelection = { ...selection.value };
  const requestSelectionKey = selectionKey(requestSelection);
  loadingComparison.value = true;
  error.value = "";
  refreshError.value = "";
  plan.value = undefined;
  try {
    const response = await databaseStore.compareSchema(requestSelection);
    if (requestId !== comparisonRequestId || requestSelectionKey !== selectionKey(selection.value)) return;
    comparison.value = response;
    comparisonSelectionKey.value = requestSelectionKey;
    selectedChangeIds.value = defaultSchemaSyncSelection(response.changes);
  } catch (cause) {
    if (requestId !== comparisonRequestId) return;
    comparison.value = undefined;
    selectedChangeIds.value = [];
    error.value = String(cause);
  } finally {
    if (requestId === comparisonRequestId) loadingComparison.value = false;
  }
}

function toggleChange(change: DatabaseSchemaChange, selected: boolean) {
  plan.value = undefined;
  const next = selected
    ? [...selectedChangeIds.value, change.id]
    : selectedChangeIds.value.filter((id) => id !== change.id);
  selectedChangeIds.value = normalizeSchemaSyncSelection(comparison.value?.changes ?? [], next);
}

async function generatePlan() {
  if (!comparison.value || !selectedChangeIds.value.length || loadingPlan.value || loadingComparison.value) return;
  const requestId = ++planRequestId;
  const requestSelection = { ...selection.value };
  const requestSelectionKey = selectionKey(requestSelection);
  const sourceFingerprint = comparison.value.source.fingerprint;
  const targetFingerprint = comparison.value.target.fingerprint;
  const requestedChangeIds = [...selectedChangeIds.value];
  loadingPlan.value = true;
  error.value = "";
  try {
    const response = await databaseStore.generateSchemaSync(
      requestSelection,
      sourceFingerprint,
      targetFingerprint,
      requestedChangeIds
    );
    if (
      requestId !== planRequestId ||
      requestSelectionKey !== selectionKey(selection.value) ||
      sourceFingerprint !== comparison.value?.source.fingerprint ||
      targetFingerprint !== comparison.value?.target.fingerprint ||
      requestedChangeIds.join("\u0000") !== selectedChangeIds.value.join("\u0000")
    ) return;
    plan.value = response;
  } catch (cause) {
    if (requestId !== planRequestId) return;
    plan.value = undefined;
    error.value = String(cause);
  } finally {
    if (requestId === planRequestId) loadingPlan.value = false;
  }
}

async function executePlan() {
  if (!comparison.value || !plan.value || !planIsCurrent.value || !selectedChangeIds.value.length) return;
  const currentPlan = plan.value;
  const requestSelection = { ...selection.value };
  const requestedChangeIds = [...selectedChangeIds.value];
  const statementCount = currentPlan.statements.length;
  const hasDanger = currentPlan.statements.some((statement) => statement.risk === "dangerous");
  try {
    await ElMessageBox.confirm(
      t(hasDanger ? "database.schemaSync.confirmDanger" : "database.schemaSync.confirm", {
        count: statementCount,
        target: `${targetConnection.value?.name ?? ""} / ${targetDatabase.value}`,
      }),
      t("database.schemaSync.confirmTitle"),
      {
        type: hasDanger ? "error" : "warning",
        confirmButtonText: t("database.schemaSync.execute"),
        cancelButtonText: t("common.cancel"),
        distinguishCancelAndClose: true,
      }
    );
  } catch {
    return;
  }

  if (!planIsCurrent.value || selectionKey(requestSelection) !== selectionKey(selection.value)) return;
  const requestId = ++executionRequestId;
  executing.value = true;
  error.value = "";
  refreshError.value = "";
  try {
    // 本对话框已确认执行；SQL 安全网只在高风险（如删除有数据的表）时再要求输入表名。
    const result = await databaseStore.executeSchemaSync(
      requestSelection,
      currentPlan.sourceFingerprint,
      currentPlan.targetFingerprint,
      requestedChangeIds,
      "preconfirmed"
    );
    if (!result) return;
    if (requestId !== executionRequestId || selectionKey(requestSelection) !== selectionKey(selection.value)) return;

    execution.value = result;
    executionPlan.value = currentPlan;
    invalidateComparison();
    refreshError.value = result.refreshError ?? "";
    if (!refreshError.value) {
      const refreshRequestId = ++comparisonRequestId;
      loadingComparison.value = true;
      try {
        const refreshed = await databaseStore.compareSchema(requestSelection);
        if (
          refreshRequestId !== comparisonRequestId ||
          requestId !== executionRequestId ||
          selectionKey(requestSelection) !== selectionKey(selection.value)
        ) return;
        comparison.value = refreshed;
        comparisonSelectionKey.value = selectionKey(requestSelection);
        selectedChangeIds.value = defaultSchemaSyncSelection(refreshed.changes);
      } catch (cause) {
        if (refreshRequestId !== comparisonRequestId || requestId !== executionRequestId) return;
        refreshError.value = String(cause);
        invalidateComparison();
      } finally {
        if (refreshRequestId === comparisonRequestId) loadingComparison.value = false;
      }
    }
    if (requestId !== executionRequestId || selectionKey(requestSelection) !== selectionKey(selection.value)) return;
    if (refreshError.value) {
      ElMessage.error(t(executionDdlFailed.value ? "database.schemaSync.stoppedRefreshFailed" : "database.schemaSync.completedRefreshFailed"));
    } else if (result.completed) {
      ElMessage.success(t("database.schemaSync.completed"));
    } else {
      ElMessage.error(t("database.schemaSync.stoppedOnFailure"));
    }
  } catch (cause) {
    if (requestId === executionRequestId) error.value = String(cause);
  } finally {
    if (requestId === executionRequestId) executing.value = false;
  }
}

function riskLabel(risk: DatabaseSchemaSyncRisk) {
  return t(`database.schemaSync.risk.${risk}` as MessageKey);
}

function kindLabel(change: DatabaseSchemaChange) {
  const knownKinds = new Set([
    "create_table", "drop_table", "add_column", "drop_column", "alter_column",
    "add_primary_key", "drop_primary_key", "create_index", "drop_index",
  ]);
  if (!knownKinds.has(change.kind)) return `${change.objectName} (${change.kind})`;
  return t(`database.schemaSync.kind.${change.kind}` as MessageKey);
}

function statusLabel(changeId: string) {
  const status = schemaSyncExecutionStatus(execution.value, changeId)?.status;
  if (status) return t(`database.schemaSync.status.${status}` as MessageKey);
  if (execution.value) return t("database.schemaSync.status.skipped");
  return t("database.schemaSync.status.pending");
}

function updateVisible(value: boolean) {
  if (executing.value) return;
  emit("update:modelValue", value);
}

function close() {
  updateVisible(false);
}
</script>

<template>
  <el-dialog
    append-to-body
    :model-value="modelValue"
    class="db-schema-sync-dialog"
    :class="{ 'db-schema-sync-dialog--compact': isCompactDialog }"
    :width="isCompactDialog ? '800px' : '1120px'"
    destroy-on-close
    :close-on-click-modal="!executing"
    :close-on-press-escape="!executing"
    :show-close="!executing"
    @update:model-value="updateVisible"
  >
    <template #header>
      <div class="db-schema-sync-dialog__header">
        <strong>{{ t("database.schemaSync.title") }}</strong>
        <span>{{ t("database.schemaSync.subtitle") }}</span>
      </div>
    </template>

    <el-scrollbar
      class="db-schema-sync-dialog__body"
      :max-height="isCompactDialog ? '360px' : '72vh'"
      v-loading="loadingComparison || loadingPlan || executing"
    >
      <el-alert
        v-if="!databaseStore.desktopRuntime"
        type="warning"
        :closable="false"
        :title="t('database.schemaSync.desktopRequired')"
        show-icon
      />
      <el-alert
        v-else-if="connectedConnections.length < 2"
        type="info"
        :closable="false"
        :title="t('database.schemaSync.connectionsRequired')"
        show-icon
      />

      <section class="db-schema-sync-selector" :aria-label="t('database.schemaSync.selection')">
        <div class="db-schema-sync-side db-schema-sync-side--source">
          <div class="db-schema-sync-side__title">
            <span class="db-schema-sync-side__badge" aria-hidden="true">S</span>
            <strong>{{ t("database.schemaSync.source") }}</strong>
          </div>
          <el-select
            filterable
            v-model="sourceConnectionId"
            :aria-label="t('database.schemaSync.sourceConnection')"
            :placeholder="t('database.schemaSync.selectConnection')"
            :disabled="executing || loadingComparison || loadingPlan || !databaseStore.desktopRuntime"
          >
            <el-option
              v-for="connection in connectedConnections"
              :key="connection.id"
              :label="connection.name"
              :value="connection.id"
            />
          </el-select>
          <el-select
            filterable
            v-model="sourceDatabase"
            :aria-label="t('database.schemaSync.sourceDatabase')"
            :placeholder="t('database.schemaSync.selectDatabase')"
            :disabled="executing || loadingComparison || loadingPlan || !sourceConnectionId"
          >
            <el-option
              v-for="database in sourceDatabases"
              :key="database.name"
              :label="database.name"
              :value="database.name"
            />
          </el-select>
        </div>
        <el-tooltip :content="t('database.schemaSync.swap')" placement="top">
          <el-button
            class="db-schema-sync-selector__swap"
            text
            circle
            :icon="Switch"
            :aria-label="t('database.schemaSync.swap')"
            :disabled="!canSwap"
            @click="swapSides"
          />
        </el-tooltip>
        <div class="db-schema-sync-side db-schema-sync-side--target">
          <div class="db-schema-sync-side__title">
            <span class="db-schema-sync-side__badge" aria-hidden="true">T</span>
            <strong>{{ t("database.schemaSync.target") }}</strong>
          </div>
          <el-select
            filterable
            v-model="targetConnectionId"
            :aria-label="t('database.schemaSync.targetConnection')"
            :placeholder="t('database.schemaSync.selectConnection')"
            :disabled="executing || loadingComparison || loadingPlan || !sourceConnectionId"
          >
            <el-option
              v-for="connection in targetConnections"
              :key="connection.id"
              :label="connection.name"
              :value="connection.id"
            />
          </el-select>
          <el-select
            filterable
            v-model="targetDatabase"
            :aria-label="t('database.schemaSync.targetDatabase')"
            :placeholder="t('database.schemaSync.selectDatabase')"
            :disabled="executing || loadingComparison || loadingPlan || !targetConnectionId"
          >
            <el-option
              v-for="database in targetDatabases"
              :key="database.name"
              :label="database.name"
              :value="database.name"
            />
          </el-select>
        </div>
      </section>

      <el-alert v-if="error" type="error" :closable="false" :title="error" show-icon />
      <el-alert
        v-if="refreshError"
        type="warning"
        :closable="false"
        :title="t(
                  executionDdlFailed ? 'database.schemaSync.stoppedRefreshFailedDetail' : 'database.schemaSync.completedRefreshFailedDetail',
                  { message: refreshError }
                )"
        show-icon
      />

      <template v-if="comparison">
        <div class="db-schema-sync-summary" role="status">
          <span>{{ t("database.schemaSync.changeCount", { count: comparison.changes.length }) }}</span>
          <el-tag type="success" effect="plain">{{ riskLabel("safe") }} {{ riskCounts.safe }}</el-tag>
          <el-tag type="warning" effect="plain">{{ riskLabel("review") }} {{ riskCounts.review }}</el-tag>
          <el-tag type="danger" effect="plain">{{ riskLabel("dangerous") }} {{ riskCounts.dangerous }}</el-tag>
          <span>{{ t("database.schemaSync.selectedCount", { count: selectedChangeIds.length }) }}</span>
        </div>

        <el-empty
          v-if="!comparison.changes.length"
          :description="t('database.schemaSync.noChanges')"
          :image-size="64"
        />
        <el-table v-else class="db-schema-sync-changes" :data="comparison.changes" max-height="300">
          <el-table-column width="52" align="center">
            <template #header>{{ t("database.schemaSync.select") }}</template>
            <template #default="scope">
              <el-checkbox
                :model-value="selectedChangeIds.includes(scope.row.id)"
                :aria-label="t('database.schemaSync.selectChange', { name: `${scope.row.table}.${scope.row.objectName}` })"
                :disabled="executing || loadingComparison || loadingPlan"
                @change="toggleChange(scope.row, Boolean($event))"
              />
            </template>
          </el-table-column>
          <el-table-column :label="t('database.schemaSync.risk')" width="110">
            <template #default="scope">
              <el-tag
                :type="scope.row.risk === 'safe' ? 'success' : scope.row.risk === 'review' ? 'warning' : 'danger'"
                effect="plain"
              >{{ riskLabel(scope.row.risk) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('database.schemaSync.change')" min-width="170">
            <template #default="scope">{{ kindLabel(scope.row) }}</template>
          </el-table-column>
          <el-table-column prop="table" :label="t('database.schemaSync.table')" min-width="160" />
          <el-table-column prop="objectName" :label="t('database.schemaSync.object')" min-width="160" />
        </el-table>

        <section v-if="plan" class="db-schema-sync-plan" :aria-label="t('database.schemaSync.planTitle')">
          <div class="db-schema-sync-plan__header">
            <strong>{{ t("database.schemaSync.planTitle") }}</strong>
            <span>{{ t("database.schemaSync.statementCount", { count: plan.statements.length }) }}</span>
          </div>
          <ol>
            <li v-for="statement in plan.statements" :key="statement.changeId">
              <div>
                <el-tag
                  :type="statement.risk === 'safe' ? 'success' : statement.risk === 'review' ? 'warning' : 'danger'"
                  effect="plain"
                >{{ riskLabel(statement.risk) }}</el-tag>
              </div>
              <pre>{{ statement.sql }}</pre>
            </li>
          </ol>
        </section>
      </template>

      <section v-if="execution && executionPlan" class="db-schema-sync-plan" :aria-label="t('database.schemaSync.executionTitle')">
        <div class="db-schema-sync-plan__header">
          <strong>{{ t("database.schemaSync.executionTitle") }}</strong>
          <span>{{ t("database.schemaSync.executionProgress", { executed: execution.statements.length, total: executionPlan.statements.length }) }}</span>
        </div>
        <ol>
          <li v-for="statement in executionStatements" :key="statement.changeId">
            <div>
              <el-tag
                :type="statement.risk === 'safe' ? 'success' : statement.risk === 'review' ? 'warning' : 'danger'"
                effect="plain"
              >{{ riskLabel(statement.risk) }}</el-tag>
              <span>{{ statusLabel(statement.changeId) }}</span>
            </div>
            <pre>{{ statement.sql }}</pre>
            <el-alert
              v-if="statement.execution?.error"
              type="error"
              :closable="false"
              :title="statement.execution.error"
            />
          </li>
        </ol>
      </section>

      <el-empty
        v-if="!comparison && !execution && databaseStore.desktopRuntime && connectedConnections.length >= 2 && !loadingComparison"
        :description="t('database.schemaSync.compareHint')"
        :image-size="64"
      />
    </el-scrollbar>

    <template #footer>
      <el-button :disabled="executing" @click="close">{{ t("common.close") }}</el-button>
      <el-button type="primary" :loading="loadingComparison" :disabled="!canCompare" @click="compare">
        {{ comparison || execution ? t("database.schemaSync.recompare") : t("database.schemaSync.compare") }}
      </el-button>
      <el-button
        :loading="loadingPlan"
        :disabled="!comparison || !selectedChanges.length || refreshError || executing || loadingComparison || loadingPlan"
        @click="generatePlan"
      >{{ t("database.schemaSync.generatePlan") }}</el-button>
      <el-button
        type="danger"
        :loading="executing"
        :disabled="!planIsCurrent || !selectedChangeIds.length || executing || loadingComparison || loadingPlan"
        @click="executePlan"
      >{{ t("database.schemaSync.execute") }}</el-button>
    </template>
  </el-dialog>
</template>
