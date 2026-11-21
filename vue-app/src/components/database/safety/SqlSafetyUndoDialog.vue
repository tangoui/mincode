<script setup lang="ts">
/**
 * 撤回一条操作记录：先预览（可恢复行数与冲突）→ 有冲突时选择只撤回未改动的行或全部覆盖 →
 * 预计写入超过高风险阈值时输入表名 → 带着用户看到的这次预览的 previewToken 撤回。
 * previewStale 时自动重新预览并显示新状态，绝不自动执行。
 */
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import {
  confirmTargetMatches,
  parseSqlSafetyError,
  SQL_SAFETY_CONFIRM_LITERAL,
  undoConfirmTarget,
  undoConflictKeyText,
  type SqlSafetyError,
  type SqlSafetyRecord,
  type SqlSafetyUndoConflict,
  type SqlSafetyUndoMode,
  type SqlSafetyUndoPreview,
} from "@/domain/database/sql-safety";
import { useVueI18n } from "@/i18n/messages";
import { previewSqlSafetyUndo, sqlSafetyHighRiskRows, undoSqlSafety } from "@/services/sql-safety-history";
import { nativeErrorMessage } from "@/services/desktop-bridge";

const props = defineProps<{
  modelValue: boolean;
  record?: SqlSafetyRecord;
  sessionId?: string;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", value: boolean): void;
  /** 撤回已提交（或可能已部分生效），列表需要刷新。 */
  (e: "done"): void;
}>();

/** 冲突表最多显示的行数（预览本身最多带回 1000 条）。 */
const CONFLICT_ROWS_SHOWN = 200;

const { t } = useVueI18n();

const preview = ref<SqlSafetyUndoPreview>();
const loading = ref(false);
const running = ref(false);
const mode = ref<SqlSafetyUndoMode>("skipConflicts");
const confirmText = ref("");
/** 后端以 confirmMismatch 要求的确认目标（前端估算未超阈值但实际超过时）。 */
const forcedTarget = ref<string>();
const highRiskRows = ref<number>();
/** 阈值读取完成前不能提交（读取失败时阈值为 undefined，按需要输入表名处理）。 */
const thresholdLoaded = ref(false);
let thresholdSeq = 0;
/** 阻止撤回的错误（预览失败、表不存在）。 */
const blockingError = ref("");
/** 上一次撤回失败的说明；可以修正后重试。 */
const actionError = ref("");
const refreshedNotice = ref(false);
let previewSeq = 0;

const hasConflicts = computed(() => (preview.value?.conflictCount ?? 0) > 0);
const effectiveMode = computed<SqlSafetyUndoMode>(() => (hasConflicts.value ? mode.value : "skipConflicts"));
const uncertain = computed(() => preview.value?.record.undoState === "uncertain");
const shownConflicts = computed(() => preview.value?.conflicts.slice(0, CONFLICT_ROWS_SHOWN) ?? []);
const hiddenConflicts = computed(() => (preview.value?.conflictCount ?? 0) - shownConflicts.value.length);
const confirmTarget = computed(() => {
  if (!preview.value) return undefined;
  return forcedTarget.value ?? undoConfirmTarget(preview.value, effectiveMode.value, highRiskRows.value);
});
const typePrompt = computed(() => {
  const target = confirmTarget.value ?? "";
  return target === SQL_SAFETY_CONFIRM_LITERAL
    ? t("database.safety.typeConfirmWord", { word: target })
    : t("database.safety.typeTableName", { name: target });
});
const summary = computed(() => {
  const value = preview.value;
  if (!value) return "";
  if (value.total === 0) return t("database.safety.history.previewNothing");
  return hasConflicts.value
    ? t("database.safety.history.previewSummary", { total: value.total, conflicts: value.conflictCount })
    : t("database.safety.history.previewNoConflicts", { total: value.total });
});
const canSubmit = computed(() =>
  Boolean(preview.value && props.sessionId)
  && thresholdLoaded.value
  && !loading.value
  && !running.value
  && !uncertain.value
  && !blockingError.value
  && (!confirmTarget.value || confirmTargetMatches(confirmTarget.value, confirmText.value)));

watch(
  () => [props.modelValue, props.record?.id] as const,
  ([open]) => {
    if (!open || !props.record) return;
    preview.value = undefined;
    mode.value = "skipConflicts";
    confirmText.value = "";
    forcedTarget.value = undefined;
    actionError.value = "";
    refreshedNotice.value = false;
    thresholdLoaded.value = false;
    highRiskRows.value = undefined;
    const seq = ++thresholdSeq;
    void sqlSafetyHighRiskRows().then((rows) => {
      if (seq !== thresholdSeq) return;
      highRiskRows.value = rows;
      thresholdLoaded.value = true;
    });
    void loadPreview();
  },
  { immediate: true },
);

function failureText(failure: SqlSafetyError): string {
  if (failure.code === "tableMissing") return t("database.safety.history.tableMissing");
  if (failure.code === "partial") {
    const applied = failure.applied ?? 0;
    return t("database.safety.partial", { applied, index: (failure.failedIndex ?? applied) + 1 });
  }
  if (failure.code === "reconfirm") return t("database.safety.reconfirm");
  return t(`database.safety.error.${failure.code}`);
}

async function loadPreview() {
  const record = props.record;
  const sessionId = props.sessionId;
  blockingError.value = "";
  if (!record) return;
  if (!sessionId) {
    blockingError.value = t("database.safety.history.notConnected");
    return;
  }
  const seq = ++previewSeq;
  loading.value = true;
  try {
    const next = await previewSqlSafetyUndo(sessionId, record.id);
    if (seq === previewSeq) preview.value = next;
  } catch (cause) {
    if (seq !== previewSeq) return;
    preview.value = undefined;
    const failure = parseSqlSafetyError(cause);
    blockingError.value = failure
      ? failureText(failure)
      : t("database.safety.history.previewFailed", { error: nativeErrorMessage(cause) });
  } finally {
    if (seq === previewSeq) loading.value = false;
  }
}

async function submit() {
  const current = preview.value;
  const sessionId = props.sessionId;
  if (!current || !sessionId || !canSubmit.value) return;
  running.value = true;
  actionError.value = "";
  refreshedNotice.value = false;
  try {
    const target = confirmTarget.value;
    const result = await undoSqlSafety({
      sessionId,
      id: current.record.id,
      mode: effectiveMode.value,
      previewToken: current.previewToken,
      ...(target ? { confirmText: confirmText.value } : {}),
    });
    ElMessage.success(t("database.safety.history.undoDone", { restored: result.restored, skipped: result.skipped }));
    emit("done");
    emit("update:modelValue", false);
  } catch (cause) {
    const failure = parseSqlSafetyError(cause);
    if (failure?.code === "previewStale") {
      // 预览之后数据又变了：后端未做修改。重新预览给用户看新状态，由用户再次确认。
      // 冲突可能已不同：撤回方式回到默认、清空已输入的表名，让用户按新状态重新选择与确认。
      refreshedNotice.value = true;
      mode.value = "skipConflicts";
      confirmText.value = "";
      await loadPreview();
    } else if (failure?.code === "confirmMismatch" && failure.check?.confirmTarget) {
      forcedTarget.value = failure.check.confirmTarget;
      confirmText.value = "";
      actionError.value = failureText(failure);
    } else if (failure?.code === "tableMissing") {
      blockingError.value = failureText(failure);
    } else if (failure) {
      actionError.value = failureText(failure);
      if (failure.code === "partial") emit("done");
    } else {
      // 普通错误也可能出现在已提交之后（如插件驱动提交后发现覆盖了新改动），刷新列表。
      actionError.value = t("database.safety.history.undoFailed", { error: nativeErrorMessage(cause) });
      emit("done");
    }
  } finally {
    running.value = false;
  }
}

function conflictKindText(row: SqlSafetyUndoConflict) {
  return t(`database.safety.history.conflict.${row.kind}`);
}
</script>

<template>
  <el-dialog
    append-to-body
    class="db-safety-undo-dialog"
    width="min(640px, calc(100vw - 32px))"
    :model-value="modelValue"
    :title="t('database.safety.history.undoTitle')"
    :close-on-click-modal="false"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div v-loading="loading" class="db-safety-undo">
      <el-alert
        v-if="refreshedNotice"
        type="warning"
        show-icon
        :closable="false"
        :title="t('database.safety.history.previewRefreshed')"
      />
      <el-alert v-if="blockingError" type="error" show-icon :closable="false" :title="blockingError" />
      <el-alert
        v-if="uncertain"
        type="error"
        show-icon
        :closable="false"
        :title="t('database.safety.history.uncertain')"
      />
      <template v-if="preview">
        <p class="db-safety-undo__summary">
          <strong>{{ preview.record.table.schema ? `${preview.record.table.schema}.` : "" }}{{ preview.record.table.name }}</strong>
          <span>{{ summary }}</span>
        </p>
        <template v-if="hasConflicts">
          <el-table
            :data="shownConflicts"
            size="small"
            border
            max-height="220"
            class="db-safety-undo__conflicts"
          >
            <el-table-column :label="t('database.safety.history.conflictKey')" min-width="200" show-overflow-tooltip>
              <template #default="{ row }">{{ undoConflictKeyText(row.primaryKey) }}</template>
            </el-table-column>
            <el-table-column :label="t('database.safety.history.conflictKind')" width="150">
              <template #default="{ row }">{{ conflictKindText(row) }}</template>
            </el-table-column>
          </el-table>
          <p v-if="hiddenConflicts > 0" class="db-safety-undo__muted">
            {{ t("database.safety.history.moreConflicts", { count: hiddenConflicts }) }}
          </p>
          <el-radio-group v-model="mode" class="db-safety-undo__mode">
            <el-radio value="skipConflicts">{{ t("database.safety.history.mode.skipConflicts") }}</el-radio>
            <el-radio value="overwrite">{{ t("database.safety.history.mode.overwrite") }}</el-radio>
          </el-radio-group>
        </template>
        <div v-if="confirmTarget" class="db-safety-undo__confirm">
          <label class="db-safety-undo__type" for="db-safety-undo-confirm">{{ typePrompt }}</label>
          <el-input
            id="db-safety-undo-confirm"
            v-model="confirmText"
            :placeholder="confirmTarget"
            autocomplete="off"
            @keyup.enter="submit"
          />
        </div>
      </template>
      <el-alert v-if="actionError" type="error" show-icon :closable="false" :title="actionError" />
    </div>
    <template #footer>
      <el-button native-type="button" @click="emit('update:modelValue', false)">{{ t("common.cancel") }}</el-button>
      <el-button
        type="danger"
        native-type="button"
        :loading="running"
        :disabled="!canSubmit"
        @click="submit"
      >
        {{ t("database.safety.history.confirmUndo") }}
      </el-button>
    </template>
  </el-dialog>
</template>
