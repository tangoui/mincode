<script setup lang="ts">
import { computed, reactive, watch } from "vue";
import type {
  DatabaseCharsetOption,
  DatabaseCharsetSelection,
  DatabaseCharsetTarget,
} from "@/domain/database/charset-change";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  modelValue: boolean;
  target?: DatabaseCharsetTarget;
  options: DatabaseCharsetOption[];
  current?: DatabaseCharsetSelection;
  loading?: boolean;
  applying?: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  apply: [selection: DatabaseCharsetSelection];
}>();

const { t } = useVueI18n();
const draft = reactive<DatabaseCharsetSelection>({ characterSet: "", collation: "" });
const collations = computed(
  () => props.options.find((option) => option.characterSet === draft.characterSet)?.collations ?? []
);
const targetName = computed(() => {
  if (!props.target) return "";
  return props.target.kind === "database"
    ? props.target.database
    : `${props.target.database}.${props.target.table}`;
});

watch(
  () => [props.modelValue, props.current, props.options] as const,
  ([visible]) => {
    if (!visible) return;
    const firstOption = props.options[0];
    draft.characterSet = props.current?.characterSet ?? firstOption?.characterSet ?? "";
    draft.collation = props.current?.collation ?? firstOption?.collations[0] ?? "";
  },
  { immediate: true }
);

watch(
  () => draft.characterSet,
  () => {
    if (!collations.value.includes(draft.collation)) draft.collation = collations.value[0] ?? "";
  }
);

function submit() {
  if (!draft.characterSet || !draft.collation) return;
  emit("apply", { characterSet: draft.characterSet, collation: draft.collation });
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('database.changeCharset')"
    width="520px"
    append-to-body
    :close-on-click-modal="!loading && !applying"
    :close-on-press-escape="!loading && !applying"
    :show-close="!loading && !applying"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div v-loading="loading">
      <el-alert
        :title="
          target?.kind === 'table'
            ? t('database.tableCharsetWarning')
            : t('database.databaseCharsetWarning')
        "
        type="warning"
        :closable="false"
        show-icon
      />
      <el-form label-position="top" class="db-charset-form">
        <el-form-item :label="t('database.charsetTarget')">
          <el-input :model-value="targetName" disabled />
        </el-form-item>
        <el-form-item :label="t('database.characterSet')" required>
          <el-select
            v-model="draft.characterSet"
            filterable
            :disabled="loading || applying"
            :placeholder="t('database.selectCharacterSet')"
          >
            <el-option
              v-for="option in options"
              :key="option.characterSet"
              :label="option.characterSet"
              :value="option.characterSet"
            />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('database.collation')" required>
          <el-select
            v-model="draft.collation"
            filterable
            :disabled="loading || applying || !draft.characterSet"
            :placeholder="t('database.selectCollation')"
          >
            <el-option v-for="collation in collations" :key="collation" :label="collation" :value="collation" />
          </el-select>
        </el-form-item>
      </el-form>
    </div>
    <template #footer>
      <el-button :disabled="applying" @click="emit('update:modelValue', false)">
        {{ t("common.cancel") }}
      </el-button>
      <el-button
        type="primary"
        :loading="applying"
        :disabled="loading || !draft.characterSet || !draft.collation"
        @click="submit"
      >
        {{ t("database.generateAndConfirm") }}
      </el-button>
    </template>
  </el-dialog>
</template>
