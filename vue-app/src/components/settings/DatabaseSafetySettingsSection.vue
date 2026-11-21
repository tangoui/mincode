<script setup lang="ts">
// 设置 → 数据库：SQL 安全网（db_safety_settings_get / set）。总量上限在界面以 GiB 编辑，按字节保存；
// 取值范围与 Go Settings.Validate 一致，后端保存时再校验一次，失败时显示后端错误。
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import AppHintIcon from "@/components/ui/AppHintIcon.vue";
import {
  SQL_SAFETY_SETTINGS_LIMITS,
  safetyBytesToGib,
  safetyGibToBytes,
  validateSafetySettings,
  type SqlSafetySettings,
  type SqlSafetySettingsField,
} from "@/domain/database/sql-safety";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { loadSqlSafetySettings, saveSqlSafetySettings } from "@/services/sql-safety-settings";
import { nativeErrorMessage } from "@/services/desktop-bridge";

type NumberDraftKey = "highRiskRows" | "retentionDays" | "maxSnapshotRows" | "maxTotalGib";
type Draft = { enabled: boolean } & Record<NumberDraftKey, number | null | undefined>;

interface NumberField {
  key: NumberDraftKey;
  field: SqlSafetySettingsField;
  label: MessageKey;
  hint?: MessageKey;
  min: number;
  max: number;
  step: number;
  precision?: number;
}

const { t } = useVueI18n();
const limits = SQL_SAFETY_SETTINGS_LIMITS;
const numberFields: NumberField[] = [
  {
    key: "highRiskRows",
    field: "highRiskRows",
    label: "database.safety.settings.highRiskRows",
    hint: "database.safety.settings.highRiskRowsHint",
    ...limits.highRiskRows,
    step: 100,
    precision: 0,
  },
  {
    key: "retentionDays",
    field: "retentionDays",
    label: "database.safety.settings.retentionDays",
    hint: "database.safety.settings.retentionDaysHint",
    ...limits.retentionDays,
    step: 1,
    precision: 0,
  },
  {
    key: "maxSnapshotRows",
    field: "maxSnapshotRows",
    label: "database.safety.settings.maxSnapshotRows",
    hint: "database.safety.settings.maxSnapshotRowsHint",
    ...limits.maxSnapshotRows,
    step: 1000,
    precision: 0,
  },
  {
    key: "maxTotalGib",
    field: "maxTotalBytes",
    label: "database.safety.settings.maxTotalGib",
    hint: "database.safety.settings.maxTotalGibHint",
    min: safetyBytesToGib(limits.maxTotalBytes.min),
    max: safetyBytesToGib(limits.maxTotalBytes.max),
    step: 0.5,
  },
];

const saved = ref<SqlSafetySettings>();
const draft = reactive<Draft>({
  enabled: true,
  highRiskRows: undefined,
  retentionDays: undefined,
  maxSnapshotRows: undefined,
  maxTotalGib: undefined,
});
const loadError = ref("");
const saveError = ref("");
const saving = ref(false);
let disposed = false;

/** 空输入转为 NaN，交给 validateSafetySettings 判为无效。 */
function draftNumber(value: number | null | undefined): number {
  return typeof value === "number" ? value : Number.NaN;
}

const pending = computed<SqlSafetySettings>(() => ({
  enabled: draft.enabled,
  highRiskRows: draftNumber(draft.highRiskRows),
  retentionDays: draftNumber(draft.retentionDays),
  maxSnapshotRows: draftNumber(draft.maxSnapshotRows),
  maxTotalBytes: typeof draft.maxTotalGib === "number" ? safetyGibToBytes(draft.maxTotalGib) : Number.NaN,
}));
const invalidFields = computed(() => new Set(validateSafetySettings(pending.value)));
const dirty = computed(() => {
  const current = saved.value;
  if (!current) return false;
  const next = pending.value;
  return (Object.keys(current) as Array<keyof SqlSafetySettings>).some((key) => current[key] !== next[key]);
});

function fill(settings: SqlSafetySettings) {
  saved.value = settings;
  Object.assign(draft, {
    enabled: settings.enabled,
    highRiskRows: settings.highRiskRows,
    retentionDays: settings.retentionDays,
    maxSnapshotRows: settings.maxSnapshotRows,
    maxTotalGib: safetyBytesToGib(settings.maxTotalBytes),
  });
}

function fieldError(field: NumberField): string {
  return invalidFields.value.has(field.field)
    ? t("database.safety.settings.outOfRange", { min: field.min, max: field.max })
    : "";
}

async function load() {
  loadError.value = "";
  try {
    const settings = await loadSqlSafetySettings();
    if (!disposed) fill(settings);
  } catch (cause) {
    if (!disposed) loadError.value = nativeErrorMessage(cause);
  }
}

async function save() {
  if (saving.value || !dirty.value || invalidFields.value.size) return;
  saving.value = true;
  saveError.value = "";
  try {
    const result = await saveSqlSafetySettings(pending.value);
    if (disposed) return;
    fill(result);
    ElMessage.success(t("database.safety.settings.saved"));
  } catch (cause) {
    if (!disposed) saveError.value = nativeErrorMessage(cause);
  } finally {
    if (!disposed) saving.value = false;
  }
}

// 保存失败的提示只对应失败时的那份草稿；用户再改动就清掉。
watch(draft, () => {
  saveError.value = "";
});

onMounted(() => void load());
onBeforeUnmount(() => {
  disposed = true;
});
</script>

<template>
  <section class="settings-flat-page app-sql-safety-settings">
    <div class="settings-section-label">{{ t("database.safety.settings.title") }}</div>
    <p class="app-sql-safety-settings__description">{{ t("database.safety.settings.description") }}</p>
    <el-alert
      v-if="loadError"
      type="error"
      :closable="false"
      show-icon
      :title="t('database.safety.settings.loadFailed', { error: loadError })"
    />
    <el-form
      v-else-if="saved"
      class="app-sql-safety-settings__form"
      :disabled="saving"
      @submit.prevent="save"
    >
      <div class="settings-flat-row">
        <strong>{{ t("database.safety.settings.enabled") }}</strong>
        <el-switch v-model="draft.enabled" :aria-label="t('database.safety.settings.enabled')" />
      </div>
      <el-alert
        v-if="!draft.enabled"
        class="app-sql-safety-settings__warning"
        type="warning"
        :closable="false"
        show-icon
        :title="t('database.safety.settings.disabledWarning')"
      />
      <div v-for="field in numberFields" :key="field.key" class="settings-flat-row">
        <span class="app-sql-safety-settings__label">
          <span class="app-inline-label">
            <strong>{{ t(field.label) }}</strong>
            <AppHintIcon v-if="field.hint" :text="t(field.hint)" />
          </span>
          <small v-if="fieldError(field)" class="app-sql-safety-settings__error">{{ fieldError(field) }}</small>
        </span>
        <el-input-number
          v-model="draft[field.key]"
          class="app-sql-safety-settings__number"
          :min="field.min"
          :max="field.max"
          :step="field.step"
          :precision="field.precision"
          controls-position="right"
          :aria-label="t(field.label)"
        />
      </div>
      <div class="app-sql-safety-settings__actions">
        <el-button
          type="primary"
          native-type="submit"
          :loading="saving"
          :disabled="!dirty || invalidFields.size > 0"
        >
          {{ t("common.save") }}
        </el-button>
      </div>
    </el-form>
    <el-alert
      v-if="saveError"
      type="error"
      :closable="false"
      show-icon
      :title="t('database.safety.settings.saveFailed', { error: saveError })"
    />
  </section>
</template>

<style scoped>
.app-sql-safety-settings__description {
  margin: 0 0 8px;
  color: var(--app-text-muted);
  font-size: var(--app-ui-font-size-extra-small);
  line-height: 1.6;
}

.app-sql-safety-settings__warning {
  margin: 10px 0;
}

.app-sql-safety-settings__label {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.app-sql-safety-settings__error {
  color: var(--el-color-danger);
  font-size: var(--app-ui-font-size-extra-small);
}

.app-sql-safety-settings__number {
  width: 160px;
}

.app-sql-safety-settings__actions {
  display: flex;
  justify-content: flex-end;
  padding-top: 14px;
}
</style>
