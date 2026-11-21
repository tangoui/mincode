<script setup lang="ts">
import { Filter } from "@element-plus/icons-vue";

const props = withDefaults(
  defineProps<{
    active?: boolean;
    label: string;
    title?: string;
    disabled?: boolean;
  }>(),
  {
    active: false,
    title: "",
    disabled: false,
  }
);

const emit = defineEmits<{ click: [] }>();
</script>

<template>
  <el-button
    class="app-filter-button"
    text
    native-type="button"
    :class="{ 'is-active': props.active }"
    :disabled="props.disabled"
    :aria-label="props.label"
    :title="props.title || props.label"
    :aria-pressed="props.active"
    @click="emit('click')"
  >
    <el-icon aria-hidden="true"><Filter /></el-icon>
  </el-button>
</template>

<style scoped>
.app-filter-button.el-button {
  --el-button-size: var(--app-search-field-height, 28px);
  --el-button-bg-color: var(--app-muted);
  --el-button-text-color: var(--app-text-muted);
  --el-button-hover-text-color: var(--app-accent);
  --el-button-hover-bg-color: var(--app-accent-soft);
  --el-button-active-bg-color: var(--app-accent-soft);
  display: grid;
  /* 跟随所在工具栏的搜索框高度，保证同一排控件等高。 */
  width: var(--app-search-field-height, 28px);
  height: var(--app-search-field-height, 28px);
  flex: none;
  place-items: center;
  border: 0;
  border-radius: var(--app-control-radius);
  background: var(--app-muted);
  margin: 0;
  padding: 0;
  color: var(--app-text-muted);
  transition:
    background-color 140ms ease,
    color 140ms ease;
}

.app-filter-button.el-button:hover,
.app-filter-button.el-button:focus-visible {
  background: var(--app-accent-soft);
  color: var(--app-accent);
}

.app-filter-button.el-button.is-active {
  --el-button-text-color: var(--app-accent);
  background: var(--app-accent-soft);
  color: var(--app-accent);
}

.app-filter-button.el-button.is-disabled {
  opacity: 0.55;
}
</style>
