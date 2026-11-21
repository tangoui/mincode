<script setup lang="ts">
import type {
  DatabaseColumnInfo,
  DatabaseQueryHistoryItem,
  DatabaseQueryResult,
} from "@/domain/database/types";
import type { DatabaseRowFieldValue } from "@/domain/database/row-field";
import DatabaseRowDialog from "@/components/database/results/DatabaseRowDialog.vue";
import { CopyDocument } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { copyText } from "@/services/desktop-bridge";

const props = defineProps<{
  columns: DatabaseColumnInfo[];
  rowForm: Record<string, DatabaseRowFieldValue>;
  rowMode?: "insert" | "edit";
  cellDetail?: { column: string; value: unknown };
  executionPlan?: DatabaseQueryResult;
  diagnosticHistory: DatabaseQueryHistoryItem[];
}>();
const emit = defineEmits<{
  saveRow: [];
  openHistory: [item: DatabaseQueryHistoryItem];
  updateRowField: [column: string, value: DatabaseRowFieldValue];
}>();
const rowVisible = defineModel<boolean>("rowVisible", { required: true });
const cellVisible = defineModel<boolean>("cellVisible", { required: true });
const planVisible = defineModel<boolean>("planVisible", { required: true });
const diagnosticsVisible = defineModel<boolean>("diagnosticsVisible", { required: true });
const { t } = useVueI18n();

function formatCellDetail(value: unknown) {
  if (typeof value === "string") return value;
  if (value === null || value === undefined) return String(value ?? "");
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

// 复制单元格详情全文：桌面端经 宿主剪贴板通道，避免打包 WebView 写不进系统剪贴板
async function copyCellDetail() {
  if (!props.cellDetail) return;
  try {
    await copyText(formatCellDetail(props.cellDetail.value));
    ElMessage.success(t("database.cellCopied"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

function formatHistoryTime(timestamp: number) {
  return new Intl.DateTimeFormat(undefined, {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(timestamp);
}

function openHistory(item: DatabaseQueryHistoryItem) {
  emit("openHistory", item);
  diagnosticsVisible.value = false;
}
</script>

<template>
  <DatabaseRowDialog
    v-model:row-visible="rowVisible"
    :columns="props.columns"
    :row-form="props.rowForm"
    :row-mode="props.rowMode"
    @save-row="emit('saveRow')"
    @update-row-field="(column, value) => emit('updateRowField', column, value)"
  />

  <el-dialog v-model="cellVisible" :title="cellDetail ? `${t('database.cellDetail')} · ${cellDetail.column}` : t('database.cellDetail')" width="640px" append-to-body>
    <el-input class="db-cell-detail" type="textarea" :model-value="cellDetail ? formatCellDetail(cellDetail.value) : ''" :autosize="{ minRows: 8, maxRows: 22 }" readonly />
    <template #footer>
      <el-button :icon="CopyDocument" @click="copyCellDetail">{{ t("common.copy") }}</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="planVisible" :title="t('database.executionPlan')" width="860px" append-to-body>
    <el-table :data="executionPlan?.rows.map((row) => Object.fromEntries((executionPlan?.columns ?? []).map((column, index) => [column, row[index]]))) ?? []" max-height="520">
      <el-table-column v-for="column in executionPlan?.columns ?? []" :key="column" :prop="column" :label="column" min-width="150" />
    </el-table>
  </el-dialog>

  <el-dialog v-model="diagnosticsVisible" :title="t('database.diagnostics')" width="900px" append-to-body>
    <el-alert :title="t('database.diagnosticsDescription')" type="info" :closable="false" />
    <el-table class="mt-4" :data="diagnosticHistory" max-height="520" stripe>
      <el-table-column :label="t('database.time')" width="150"><template #default="scope">{{ formatHistoryTime(scope.row.startedAt) }}</template></el-table-column>
      <el-table-column :label="t('database.status')" width="90"><template #default="scope"><el-tag :type="scope.row.success ? 'warning' : 'danger'">{{ scope.row.success ? t("database.slowQuery") : t("database.failed") }}</el-tag></template></el-table-column>
      <el-table-column prop="executionMs" :label="t('database.durationMs')" width="100" />
      <el-table-column prop="query" label="SQL" min-width="280" show-overflow-tooltip />
      <el-table-column prop="error" :label="t('database.error')" min-width="220" show-overflow-tooltip />
      <el-table-column :label="t('database.action')" width="90"><template #default="scope"><el-button link type="primary" @click="openHistory(scope.row)">{{ t("database.open") }}</el-button></template></el-table-column>
    </el-table>
  </el-dialog>
</template>
