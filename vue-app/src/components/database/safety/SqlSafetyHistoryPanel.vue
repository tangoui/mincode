<script setup lang="ts">
/**
 * 当前连接的 SQL 安全网操作记录（ElDrawer）：时间、来源、表、语句摘要、影响行数、状态；
 * 展开一行看语句全文、留底原因与前 20 行留底样例；「撤回」打开 SqlSafetyUndoDialog。
 */
import { computed, reactive, ref, watch } from "vue";
import { Refresh } from "@element-plus/icons-vue";
import {
  historyRowView,
  sampleCellText,
  type SqlSafetyHistoryDetail,
  type SqlSafetyHistoryRowView,
  type SqlSafetyRecord,
} from "@/domain/database/sql-safety";
import type { DatabaseConnection } from "@/domain/database/types";
import { useVueI18n } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { getSqlSafetyHistory, listSqlSafetyHistory } from "@/services/sql-safety-history";
import { nativeErrorMessage } from "@/services/desktop-bridge";
import SqlSafetyUndoDialog from "@/components/database/safety/SqlSafetyUndoDialog.vue";

const props = defineProps<{
  modelValue: boolean;
  connection: DatabaseConnection;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", value: boolean): void;
  /** 撤回已提交（或可能已部分生效）：打开着这张表的表格需要重新加载。 */
  (e: "undone", record: SqlSafetyRecord): void;
}>();

const STATEMENT_SUMMARY_LIMIT = 160;

const { t, intlLocale } = useVueI18n();
const databaseStore = useDatabaseWorkspaceStore();
/** 当前连接的会话；未连接时不能撤回。 */
const sessionId = computed<string | undefined>(() => databaseStore.sessionIds[props.connection.id]);

const records = ref<SqlSafetyRecord[]>([]);
const loading = ref(false);
const loadError = ref("");
const details = reactive(new Map<string, { loading: boolean; detail?: SqlSafetyHistoryDetail; error?: string }>());
const undoVisible = ref(false);
const undoRecord = ref<SqlSafetyRecord>();
let loadSeq = 0;

watch(
  () => [props.modelValue, props.connection.id] as const,
  ([open]) => {
    if (open) void load();
  },
  { immediate: true },
);

async function load() {
  const connectionId = props.connection.id;
  const seq = ++loadSeq;
  loading.value = true;
  loadError.value = "";
  try {
    const next = await listSqlSafetyHistory(connectionId);
    if (seq !== loadSeq) return;
    records.value = next;
    // 留底样例不随撤回变化：保留仍在列表里的（展开的行不会再触发 expand-change），其余丢弃。
    const ids = new Set(next.map((record) => record.id));
    for (const id of [...details.keys()]) if (!ids.has(id)) details.delete(id);
  } catch (cause) {
    if (seq !== loadSeq) return;
    records.value = [];
    loadError.value = t("database.safety.history.loadFailed", { error: nativeErrorMessage(cause) });
  } finally {
    if (seq === loadSeq) loading.value = false;
  }
}

async function loadDetail(record: SqlSafetyRecord) {
  const cached = details.get(record.id);
  if (cached?.detail || cached?.loading) return;
  details.set(record.id, { loading: true });
  try {
    details.set(record.id, { loading: false, detail: await getSqlSafetyHistory(record.id) });
  } catch (cause) {
    details.set(record.id, { loading: false, error: nativeErrorMessage(cause) });
  }
}

function onExpandChange(record: SqlSafetyRecord, expanded: SqlSafetyRecord[]) {
  if (expanded.some((row) => row.id === record.id)) void loadDetail(record);
}

function view(record: SqlSafetyRecord): SqlSafetyHistoryRowView {
  const base = historyRowView(record);
  if (base.canUndo && !sessionId.value) {
    return { ...base, canUndo: false, disabledReasonKey: "database.safety.history.notConnected" };
  }
  return base;
}

function timeText(record: SqlSafetyRecord) {
  const date = new Date(record.createdAt);
  return Number.isNaN(date.getTime()) ? record.createdAt : date.toLocaleString(intlLocale.value, { hour12: false });
}

function sourceText(record: SqlSafetyRecord) {
  const key = view(record).sourceKey;
  return key ? t(key) : record.source;
}

function tableText(record: SqlSafetyRecord) {
  const { schema, name } = record.table;
  return schema ? `${schema}.${name}` : name;
}

function statementSummary(record: SqlSafetyRecord) {
  const text = record.statement.replace(/\s+/g, " ").trim();
  return text.length > STATEMENT_SUMMARY_LIMIT ? `${text.slice(0, STATEMENT_SUMMARY_LIMIT)}…` : text;
}

/** 展开行里要显示的留底样例（修改前 / 修改后，空的不显示）；单元格已转成安全的显示文本。 */
function sampleParts(record: SqlSafetyRecord) {
  const detail = details.get(record.id)?.detail;
  if (!detail) return [];
  return (["before", "after"] as const)
    .filter((part) => detail.sample[part].length > 0)
    .map((part) => ({
      part,
      columns: detail.sample.columns,
      rows: detail.sample[part].map((row) => ({ cells: row.map((value) => sampleCellText(value)) })),
    }));
}

function openUndo(record: SqlSafetyRecord) {
  undoRecord.value = record;
  undoVisible.value = true;
}

function undoDone() {
  void load();
  if (undoRecord.value) emit("undone", undoRecord.value);
}
</script>

<template>
  <el-drawer
    append-to-body
    direction="rtl"
    size="min(1000px, 96vw)"
    :model-value="modelValue"
    :title="`${t('database.safety.history.title')} · ${connection.name}`"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="db-safety-history">
      <div class="db-safety-history__toolbar">
        <el-button text native-type="button" :loading="loading" @click="load">
          <el-icon><Refresh /></el-icon><span>{{ t("database.safety.history.refresh") }}</span>
        </el-button>
      </div>
      <el-alert v-if="loadError" type="error" show-icon :closable="false" :title="loadError" />
      <div class="db-safety-history__table">
        <el-table
          v-loading="loading"
          :data="records"
          row-key="id"
          size="small"
          height="100%"
          :empty-text="t('database.safety.history.empty')"
          @expand-change="onExpandChange"
        >
          <el-table-column type="expand" width="32">
            <template #default="{ row }">
              <div v-loading="details.get(row.id)?.loading" class="db-safety-history__detail">
                <dl class="db-safety-history__meta">
                  <dt>{{ t("database.safety.history.detail.connection") }}</dt>
                  <dd>{{ row.connectionName || row.connectionId }}</dd>
                  <dt>{{ t("database.safety.history.detail.database") }}</dt>
                  <dd>{{ row.database || "-" }}</dd>
                  <dt>{{ t("database.safety.history.detail.kind") }}</dt>
                  <dd>{{ t(view(row).kindKey) }}</dd>
                </dl>
                <p v-if="row.parentId" class="db-safety-history__muted">
                  {{ t("database.safety.history.detail.parent", { id: row.parentId }) }}
                </p>
                <el-alert
                  v-if="view(row).disabledReasonKey"
                  :type="row.undoState === 'uncertain' ? 'error' : 'info'"
                  show-icon
                  :closable="false"
                  :title="t(view(row).disabledReasonKey!)"
                />
                <el-alert
                  v-if="view(row).noticeKey"
                  type="warning"
                  show-icon
                  :closable="false"
                  :title="t(view(row).noticeKey!)"
                />
                <div class="db-safety-history__label">{{ t("database.safety.history.detail.statement") }}</div>
                <el-scrollbar max-height="200px" class="db-safety-history__sql">
                  <pre>{{ row.statement }}</pre>
                </el-scrollbar>
                <el-alert
                  v-if="details.get(row.id)?.error"
                  type="error"
                  show-icon
                  :closable="false"
                  :title="t('database.safety.history.loadFailed', { error: details.get(row.id)?.error ?? '' })"
                />
                <template v-if="details.get(row.id)?.detail">
                  <p v-if="!sampleParts(row).length" class="db-safety-history__muted">
                    {{ t("database.safety.history.detail.noSample") }}
                  </p>
                  <template v-for="sample in sampleParts(row)" :key="sample.part">
                    <div class="db-safety-history__label">{{ t(`database.safety.history.detail.${sample.part}`) }}</div>
                    <el-table
                      :data="sample.rows"
                      size="small"
                      border
                      max-height="260"
                      class="db-safety-history__sample"
                    >
                      <el-table-column
                        v-for="(column, index) in sample.columns"
                        :key="`${index}:${column}`"
                        :label="column"
                        min-width="120"
                        show-overflow-tooltip
                      >
                        <template #default="{ row: cells }">{{ cells.cells[index] ?? "" }}</template>
                      </el-table-column>
                    </el-table>
                  </template>
                </template>
              </div>
            </template>
          </el-table-column>
          <el-table-column :label="t('database.safety.history.column.time')" width="160">
            <template #default="{ row }">{{ timeText(row) }}</template>
          </el-table-column>
          <el-table-column :label="t('database.safety.history.column.source')" width="100" show-overflow-tooltip>
            <template #default="{ row }">{{ sourceText(row) }}</template>
          </el-table-column>
          <el-table-column :label="t('database.safety.history.column.table')" min-width="120" show-overflow-tooltip>
            <template #default="{ row }">{{ tableText(row) }}</template>
          </el-table-column>
          <el-table-column :label="t('database.safety.history.column.statement')" min-width="200" show-overflow-tooltip>
            <template #default="{ row }">
              <span class="db-safety-history__kind">{{ t(view(row).kindKey) }}</span>
              <code class="db-safety-history__statement">{{ statementSummary(row) }}</code>
            </template>
          </el-table-column>
          <el-table-column :label="t('database.safety.history.column.affected')" width="90" align="right">
            <template #default="{ row }">{{ row.affected.toLocaleString(intlLocale) }}</template>
          </el-table-column>
          <el-table-column :label="t('database.safety.history.column.state')" width="110">
            <template #default="{ row }">
              <el-tag :type="view(row).stateType" size="small" disable-transitions>{{ t(view(row).stateKey) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('database.safety.history.column.actions')" width="130" fixed="right">
            <template #default="{ row }">
              <el-tooltip
                :disabled="view(row).canUndo"
                :content="view(row).disabledReasonKey ? t(view(row).disabledReasonKey!) : ''"
                placement="left"
              >
                <span class="db-safety-history__action">
                  <el-button
                    link
                    type="primary"
                    native-type="button"
                    :disabled="!view(row).canUndo"
                    @click="openUndo(row)"
                  >
                    {{ t(view(row).undoLabelKey) }}
                  </el-button>
                </span>
              </el-tooltip>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </div>
    <SqlSafetyUndoDialog
      v-model="undoVisible"
      :record="undoRecord"
      :session-id="sessionId"
      @done="undoDone"
    />
  </el-drawer>
</template>
