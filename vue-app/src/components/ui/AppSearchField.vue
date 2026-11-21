<script setup lang="ts">
import { ref } from "vue";
import { Close, Search } from "@element-plus/icons-vue";

const props = withDefaults(
  defineProps<{
    modelValue: string;
    placeholder?: string;
    ariaLabel?: string;
    clearable?: boolean;
    clearLabel?: string;
  }>(),
  {
    placeholder: "",
    ariaLabel: "",
    clearable: false,
    clearLabel: "",
  }
);

const emit = defineEmits<{
  "update:modelValue": [value: string];
  input: [value: string];
  clear: [];
  submit: [];
}>();

const input = ref<{ focus: () => void }>();

defineExpose({
  focus: () => input.value?.focus(),
});

function updateValue(value: string) {
  emit("update:modelValue", value);
  emit("input", value);
}

function clearValue() {
  emit("update:modelValue", "");
  emit("input", "");
  emit("clear");
}
</script>

<template>
  <el-input
    ref="input"
    class="app-search-field"
    :model-value="props.modelValue"
    :placeholder="props.placeholder"
    :aria-label="props.ariaLabel || props.placeholder || undefined"
    @update:model-value="updateValue"
    @keyup.enter="emit('submit')"
  >
    <template #prefix>
      <el-icon aria-hidden="true"><Search /></el-icon>
    </template>
    <!-- ElInput only notices a suffix slot present at first render; keep it and toggle the button. -->
    <template #suffix>
      <el-button
        v-if="props.clearable && props.modelValue"
        class="app-search-field__clear"
        text
        :icon="Close"
        :aria-label="props.clearLabel || props.ariaLabel || props.placeholder || undefined"
        :title="props.clearLabel || props.ariaLabel || props.placeholder || undefined"
        @keyup.enter.stop
        @click.stop="clearValue"
      />
    </template>
  </el-input>
</template>

<style scoped>
.app-search-field {
  min-width: 0;
  height: var(--app-search-field-height, 28px);
  font-size: 12px;
}

.app-search-field :deep(.el-input__wrapper) {
  min-height: var(--app-search-field-height, 28px);
  height: var(--app-search-field-height, 28px);
  padding: 0 9px;
}

.app-search-field :deep(.el-input__prefix) {
  color: var(--app-text-muted);
  font-size: 14px;
}

.app-search-field :deep(.el-input__inner) {
  line-height: calc(var(--app-search-field-height, 28px) - 2px);
}

.app-search-field__clear.el-button {
  width: 18px;
  height: 18px;
  min-height: 18px;
  padding: 0;
  color: var(--app-text-muted);
}

.app-search-field__clear.el-button:hover,
.app-search-field__clear.el-button:focus-visible {
  background: var(--app-accent-soft);
  color: var(--app-accent);
}
</style>
