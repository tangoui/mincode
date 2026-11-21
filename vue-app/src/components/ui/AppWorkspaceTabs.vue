<script setup lang="ts">
import type { TabPaneName } from "element-plus";

defineOptions({ inheritAttrs: false });

defineProps<{
  modelValue?: TabPaneName;
}>();

const emit = defineEmits<{
  "update:modelValue": [name: TabPaneName];
  "tab-remove": [name: TabPaneName];
}>();

function updateModelValue(name: TabPaneName) {
  emit("update:modelValue", name);
}

function removeTab(name: TabPaneName) {
  emit("tab-remove", name);
}

// el-tabs only scrolls its nav on horizontal wheel deltas (trackpad swipe or
// Shift+wheel). Re-dispatch a plain vertical mouse wheel over this header as a
// horizontal one so it goes through el-tabs' own scroll handling.
function scrollTabsWithWheel(event: WheelEvent) {
  if (event.shiftKey || Math.abs(event.deltaY) <= Math.abs(event.deltaX)) return;
  const root = event.currentTarget as HTMLElement;
  const header = (event.target as Element | null)?.closest(".el-tabs__header");
  if (!header || header.parentElement !== root) return;
  const nav = header.querySelector<HTMLElement>(".el-tabs__nav");
  if (!nav) return;
  event.preventDefault();
  event.stopPropagation();
  nav.dispatchEvent(
    new WheelEvent("wheel", {
      deltaX: event.deltaY,
      deltaMode: event.deltaMode,
      bubbles: true,
      cancelable: true,
    }),
  );
}
</script>

<template>
  <el-tabs
    v-bind="$attrs"
    class="app-workspace-tabs"
    type="card"
    :model-value="modelValue"
    @update:model-value="updateModelValue"
    @tab-remove="removeTab"
    @wheel.capture="scrollTabsWithWheel"
  >
    <slot />
  </el-tabs>
</template>
