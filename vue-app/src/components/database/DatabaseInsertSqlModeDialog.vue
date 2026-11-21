<script setup lang="ts">
import type { InsertSqlMode } from "@/domain/database/insert-sql";
import { useVueI18n } from "@/i18n/messages";

defineProps<{
  modelValue: boolean;
  mode: InsertSqlMode;
  /** 已选行数；为 0 时（如整表导出）不展示行数说明。 */
  rowCount?: number;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  "update:mode": [value: InsertSqlMode];
  confirm: [];
  cancel: [];
}>();

const { t } = useVueI18n();

function handleVisibleChange(value: boolean) {
  emit("update:modelValue", value);
  if (!value) emit("cancel");
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('database.insertSqlModeTitle')"
    width="440px"
    append-to-body
    @update:model-value="handleVisibleChange"
  >
    <p v-if="rowCount" class="db-insert-sql-mode__count">
      {{ t("database.insertSqlModeRowCount", { count: rowCount }) }}
    </p>
    <el-radio-group
      :model-value="mode"
      class="db-insert-sql-mode__group"
      @update:model-value="emit('update:mode', $event as InsertSqlMode)"
    >
      <el-radio value="rows" class="db-insert-sql-mode__option">
        <span class="db-insert-sql-mode__label">{{ t("database.insertSqlModeRows") }}</span>
        <span class="db-insert-sql-mode__hint">{{ t("database.insertSqlModeRowsHint") }}</span>
      </el-radio>
      <el-radio value="batch" class="db-insert-sql-mode__option">
        <span class="db-insert-sql-mode__label">{{ t("database.insertSqlModeBatch") }}</span>
        <span class="db-insert-sql-mode__hint">{{ t("database.insertSqlModeBatchHint") }}</span>
      </el-radio>
    </el-radio-group>
    <template #footer>
      <el-button native-type="button" @click="handleVisibleChange(false)">
        {{ t("common.cancel") }}
      </el-button>
      <el-button type="primary" native-type="button" @click="emit('confirm')">
        {{ t("common.confirm") }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.db-insert-sql-mode__count {
  margin: 0 0 8px;
  font-size: 12px;
  color: var(--app-text-muted);
}

.db-insert-sql-mode__group {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 100%;
}

.db-insert-sql-mode__option {
  display: flex;
  align-items: flex-start;
  width: 100%;
  margin-right: 0;
  padding: 6px 8px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
}

.db-insert-sql-mode__option:hover {
  background: var(--app-hover);
}

.db-insert-sql-mode__option :deep(.el-radio__label) {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.db-insert-sql-mode__label {
  font-size: 12px;
  color: var(--app-text);
}

.db-insert-sql-mode__hint {
  font-size: 12px;
  color: var(--app-text-muted);
}
</style>
