<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { Refresh } from "@element-plus/icons-vue";
import type { DatabaseColumnInfo } from "@/domain/database/types";
import {
  databaseRowFieldKind,
  DatabaseRowFieldValueError,
  parseDatabaseRowFieldValue,
  type DatabaseRowFieldValue,
} from "@/domain/database/row-field";
import { databaseValueGeneratorsForKind } from "@/domain/database/value-generator";
import type { BulkUpdatePayload, BulkUpdateTarget } from "@/composables/database/useRowMutations";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  target?: BulkUpdateTarget;
  columns: DatabaseColumnInfo[];
}>();
const emit = defineEmits<{
  close: [];
  apply: [payload: BulkUpdatePayload];
}>();
const { t } = useVueI18n();

const mode = ref<"value" | "generator">("value");
const valueRaw = ref<DatabaseRowFieldValue>("");
const generatorId = ref("");
const generatorPreview = ref("");

const column = computed(() =>
  props.columns.find((item) => item.name === props.target?.column)
);
const fieldKind = computed(() =>
  databaseRowFieldKind(column.value?.dataType, props.target?.column ?? "")
);
const generators = computed(() => databaseValueGeneratorsForKind(fieldKind.value));
const activeGenerator = computed(() =>
  generators.value.find((item) => item.id === generatorId.value)
);

function regeneratePreview() {
  const generator = activeGenerator.value;
  generatorPreview.value = generator ? String(generator.generate()) : "";
}

watch(
  () => props.target,
  (target) => {
    if (!target) return;
    mode.value = target.initialMode;
    valueRaw.value = "";
    generatorId.value = generators.value[0]?.id ?? "";
    regeneratePreview();
  },
  { immediate: true }
);

function updateTemporalValue(value: string | null | undefined) {
  valueRaw.value = value ?? (column.value?.nullable ? null : "");
}

function handleApply() {
  if (!props.target) return;
  if (mode.value === "generator") {
    if (!activeGenerator.value) return;
    emit("apply", { mode: "generator", generatorId: activeGenerator.value.id });
    return;
  }
  try {
    const value = parseDatabaseRowFieldValue(fieldKind.value, valueRaw.value, Boolean(column.value?.nullable));
    emit("apply", { mode: "value", value });
  } catch (cause) {
    if (cause instanceof DatabaseRowFieldValueError) {
      ElMessage.error(t(`database.${cause.reason}`));
      return;
    }
    ElMessage.error(String(cause));
  }
}
</script>

<template>
  <el-dialog
    append-to-body
    :model-value="Boolean(target)"
    class="db-bulk-update-dialog"
    :title="t('database.bulkUpdateTitle')"
    width="460px"
    @update:model-value="!$event && emit('close')"
  >
    <el-form v-if="target" class="db-bulk-update-form" label-position="top">
      <div class="db-bulk-update-form__summary">
        <span class="db-bulk-update-form__column">
          <strong>{{ target.column }}</strong>
          <el-tag v-if="column?.dataType" size="small" effect="plain">{{ column.dataType }}</el-tag>
          <el-tag v-if="column?.nullable" size="small" type="info" effect="plain">
            {{ t("database.nullable") }}
          </el-tag>
        </span>
        <span class="db-bulk-update-form__rows">
          {{ t("database.bulkUpdateAffectedRows", { count: target.rowIndexes.length }) }}
        </span>
      </div>
      <el-radio-group v-model="mode" class="db-bulk-update-form__mode">
        <el-radio-button value="value">{{ t("database.bulkUpdateModeValue") }}</el-radio-button>
        <el-radio-button value="generator" :disabled="!generators.length">
          {{ t("database.bulkUpdateModeGenerator") }}
        </el-radio-button>
      </el-radio-group>

      <el-form-item v-if="mode === 'value'" :label="t('database.bulkUpdateValueLabel')">
        <el-switch
          v-if="fieldKind === 'boolean'"
          :model-value="Boolean(valueRaw)"
          :active-text="t('database.booleanTrue')"
          :inactive-text="t('database.booleanFalse')"
          @update:model-value="valueRaw = Boolean($event)"
        />
        <el-input
          v-else-if="fieldKind === 'number'"
          :model-value="String(valueRaw ?? '')"
          type="number"
          inputmode="decimal"
          :placeholder="column?.nullable ? t('database.nullableInputHint') : undefined"
          @update:model-value="valueRaw = String($event ?? '')"
        />
        <el-date-picker
          v-else-if="fieldKind === 'date'"
          :model-value="String(valueRaw ?? '')"
          type="date"
          value-format="YYYY-MM-DD"
          :clearable="column?.nullable"
          @update:model-value="updateTemporalValue"
        />
        <el-time-picker
          v-else-if="fieldKind === 'time'"
          :model-value="String(valueRaw ?? '')"
          value-format="HH:mm:ss.SSS"
          format="HH:mm:ss.SSS"
          :clearable="column?.nullable"
          @update:model-value="updateTemporalValue"
        />
        <el-date-picker
          v-else-if="fieldKind === 'datetime'"
          :model-value="String(valueRaw ?? '')"
          type="datetime"
          value-format="YYYY-MM-DDTHH:mm:ss.SSS"
          format="YYYY-MM-DD HH:mm:ss.SSS"
          :clearable="column?.nullable"
          @update:model-value="updateTemporalValue"
        />
        <el-input
          v-else-if="fieldKind === 'textarea' || fieldKind === 'json'"
          :model-value="String(valueRaw ?? '')"
          type="textarea"
          :autosize="{ minRows: 3, maxRows: 8 }"
          :placeholder="fieldKind === 'json' ? t('database.jsonInputHint') : column?.nullable ? t('database.nullableInputHint') : undefined"
          @update:model-value="valueRaw = String($event ?? '')"
        />
        <el-input
          v-else
          :model-value="String(valueRaw ?? '')"
          :placeholder="column?.nullable ? t('database.nullableInputHint') : undefined"
          clearable
          @update:model-value="valueRaw = String($event ?? '')"
        />
      </el-form-item>

      <template v-else>
        <el-form-item :label="t('database.bulkUpdateGeneratorLabel')">
          <el-select
            v-model="generatorId"
            class="db-bulk-update-form__generator"
            @change="regeneratePreview"
          >
            <el-option
              v-for="generator in generators"
              :key="generator.id"
              :label="t(`database.valueGenerator.${generator.id}`)"
              :value="generator.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('database.bulkUpdateGeneratorPreview')">
          <div class="db-bulk-update-form__preview">
            <code class="db-bulk-update-form__preview-value">{{ generatorPreview }}</code>
            <el-button
              text
              native-type="button"
              :icon="Refresh"
              :aria-label="t('database.bulkUpdateRegenerate')"
              :title="t('database.bulkUpdateRegenerate')"
              @click="regeneratePreview"
            />
          </div>
        </el-form-item>
        <p class="db-bulk-update-form__hint">{{ t("database.bulkUpdateGeneratorHint") }}</p>
      </template>
    </el-form>
    <template #footer>
      <el-button @click="emit('close')">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" @click="handleApply">{{ t("database.bulkUpdateApply") }}</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.db-bulk-update-form__summary {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.db-bulk-update-form__column {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.db-bulk-update-form__rows {
  color: var(--app-text-muted);
  font-size: 12px;
  white-space: nowrap;
}

.db-bulk-update-form__mode {
  margin-bottom: 4px;
}

.db-bulk-update-form__generator {
  width: 100%;
}

.db-bulk-update-form__preview {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
}

.db-bulk-update-form__preview-value {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 4px 8px;
  border: 1px solid var(--app-border);
  border-radius: 4px;
  background: var(--app-canvas);
  color: var(--app-text-regular);
}

.db-bulk-update-form__hint {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
</style>
