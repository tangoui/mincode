<script setup lang="ts">
import { ref } from "vue";
import { useVueI18n } from "@/i18n/messages";

// Body of the "Format Document With…" picker (rendered inside ElMessageBox by
// services/contributions/formatters.ts). Titles are plugin text and rendered
// as plain text only.
export interface FormatterPickerOption {
  value: string;
  label: string;
  plugin?: string;
}

const props = defineProps<{
  language: string;
  options: FormatterPickerOption[];
  initial: string;
}>();

const emit = defineEmits<{
  change: [value: { choice: string; remember: boolean }];
}>();

const { t } = useVueI18n();
const choice = ref(props.initial);
const remember = ref(false);

function update() {
  emit("change", { choice: choice.value, remember: remember.value });
}
</script>

<template>
  <div class="formatter-picker">
    <el-radio-group v-model="choice" class="formatter-picker__options" :aria-label="t('pluginContrib.formatters.formatWith')" @change="update">
      <el-radio v-for="option in options" :key="option.value" :value="option.value" class="formatter-picker__option">
        <span class="formatter-picker__label">{{ option.label }}</span>
        <span v-if="option.plugin" class="formatter-picker__plugin">{{ option.plugin }}</span>
      </el-radio>
    </el-radio-group>
    <el-checkbox v-model="remember" @change="update">
      {{ t("pluginContrib.formatters.useAsDefault", { language }) }}
    </el-checkbox>
  </div>
</template>

<style scoped>
.formatter-picker {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: min(360px, 80vw);
}

.formatter-picker__options {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 4px;
}

.formatter-picker__option {
  height: auto;
  min-height: 28px;
  margin-right: 0;
}

.formatter-picker__label {
  overflow-wrap: anywhere;
}

.formatter-picker__plugin {
  margin-left: 8px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
</style>
