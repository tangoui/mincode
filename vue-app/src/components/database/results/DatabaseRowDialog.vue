<script setup lang="ts">
import type { DatabaseColumnInfo } from "@/domain/database/types";
import {
  databaseRowFieldKind,
  isDatabaseDescriptionField,
  type DatabaseRowFieldKind,
  type DatabaseRowFieldValue,
} from "@/domain/database/row-field";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  columns: DatabaseColumnInfo[];
  rowForm: Record<string, DatabaseRowFieldValue>;
  rowMode?: "insert" | "edit";
}>();
const emit = defineEmits<{
  saveRow: [];
  updateRowField: [column: string, value: DatabaseRowFieldValue];
}>();
const rowVisible = defineModel<boolean>("rowVisible", { required: true });
const { t } = useVueI18n();

function rowFieldKind(column: DatabaseColumnInfo): DatabaseRowFieldKind {
  return databaseRowFieldKind(column.dataType, column.name);
}

function updateTextField(column: string, value: unknown) {
  emit("updateRowField", column, String(value ?? ""));
}

function updateTemporalField(column: DatabaseColumnInfo, value: string | null | undefined) {
  emit("updateRowField", column.name, value ?? (column.nullable ? null : ""));
}
</script>

<template>
  <el-dialog
    append-to-body
    v-model="rowVisible"
    class="db-row-dialog"
    :title="props.rowMode === 'edit' ? t('database.editRowTitle') : t('database.addRowTitle')"
    width="720px"
  >
    <el-scrollbar class="db-row-form-scroll" max-height="min(62vh, 620px)">
      <el-form class="db-row-form" label-position="top">
      <el-form-item
        v-for="column in columns.filter((item) => !item.autoIncrement)"
        :key="column.name"
        :class="{ 'db-row-form__field--wide': ['textarea', 'json'].includes(rowFieldKind(column)) }"
      >
        <template #label>
          <span class="db-row-form__label">
            <strong>{{ column.name }}</strong>
            <span class="db-row-form__meta">
              <el-tag size="small" effect="plain">{{ column.dataType }}</el-tag>
              <el-tag v-if="column.isPrimaryKey" size="small" type="warning" effect="plain">PK</el-tag>
              <el-tag v-if="column.nullable" size="small" type="info" effect="plain">
                {{ t("database.nullable") }}
              </el-tag>
            </span>
          </span>
        </template>
        <el-switch
          v-if="rowFieldKind(column) === 'boolean'"
          :model-value="Boolean(props.rowForm[column.name])"
          :active-text="t('database.booleanTrue')"
          :inactive-text="t('database.booleanFalse')"
          @update:model-value="emit('updateRowField', column.name, Boolean($event))"
        />
        <el-input
          v-else-if="rowFieldKind(column) === 'number'"
          :model-value="String(props.rowForm[column.name] ?? '')"
          type="number"
          inputmode="decimal"
          :placeholder="column.nullable ? t('database.nullableInputHint') : undefined"
          @update:model-value="updateTextField(column.name, $event)"
        />
        <el-date-picker
          v-else-if="rowFieldKind(column) === 'date'"
          :model-value="String(props.rowForm[column.name] ?? '')"
          type="date"
          value-format="YYYY-MM-DD"
          :clearable="column.nullable"
          @update:model-value="updateTemporalField(column, $event)"
        />
        <el-time-picker
          v-else-if="rowFieldKind(column) === 'time'"
          :model-value="String(props.rowForm[column.name] ?? '')"
          value-format="HH:mm:ss.SSS"
          format="HH:mm:ss.SSS"
          :clearable="column.nullable"
          @update:model-value="updateTemporalField(column, $event)"
        />
        <el-date-picker
          v-else-if="rowFieldKind(column) === 'datetime'"
          :model-value="String(props.rowForm[column.name] ?? '')"
          type="datetime"
          value-format="YYYY-MM-DDTHH:mm:ss.SSS"
          format="YYYY-MM-DD HH:mm:ss.SSS"
          :clearable="column.nullable"
          @update:model-value="updateTemporalField(column, $event)"
        />
        <el-input
          v-else-if="['textarea', 'json'].includes(rowFieldKind(column))"
          :model-value="String(props.rowForm[column.name] ?? '')"
          type="textarea"
          :class="{ 'db-row-form__textarea--prose': isDatabaseDescriptionField(column.name) }"
          :autosize="{ minRows: 3, maxRows: 8 }"
          :placeholder="rowFieldKind(column) === 'json' ? t('database.jsonInputHint') : column.nullable ? t('database.nullableInputHint') : undefined"
          @update:model-value="updateTextField(column.name, $event)"
        />
        <el-input
          v-else
          :model-value="String(props.rowForm[column.name] ?? '')"
          :placeholder="column.nullable ? t('database.nullableInputHint') : undefined"
          clearable
          @update:model-value="updateTextField(column.name, $event)"
        />
      </el-form-item>
      </el-form>
    </el-scrollbar>
    <template #footer>
      <el-button @click="rowVisible = false">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" @click="emit('saveRow')">{{ t("database.pending.stage") }}</el-button>
    </template>
  </el-dialog>
</template>
