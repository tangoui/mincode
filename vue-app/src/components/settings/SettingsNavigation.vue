<script setup lang="ts">
import type { Component } from "vue";
import { Refresh } from "@element-plus/icons-vue";
import { useVueI18n, type MessageKey } from "@/i18n/messages";

interface SettingsNavigationItem {
  id: string;
  label: MessageKey;
  icon: Component;
  badge?: boolean;
}

const props = defineProps<{
  modelValue: string;
  items: SettingsNavigationItem[];
}>();
const emit = defineEmits<{
  "update:modelValue": [value: string];
  reset: [];
}>();
const { t } = useVueI18n();
</script>

<template>
  <aside class="settings-dialog__nav">
    <nav>
      <el-button
        v-for="item in props.items"
        :key="item.id"
        class="settings-dialog__nav-item"
        text
        native-type="button"
        :class="{ 'is-active': props.modelValue === item.id }"
        :aria-current="props.modelValue === item.id ? 'page' : undefined"
        @click="emit('update:modelValue', item.id)"
      >
        <el-icon><component :is="item.icon" /></el-icon>
        <span>{{ t(item.label) }}</span>
        <span v-if="item.badge" class="settings-dialog__nav-dot" aria-hidden="true" />
      </el-button>
    </nav>
    <el-button text native-type="button" class="settings-dialog__reset" @click="emit('reset')">
      <el-icon><Refresh /></el-icon>
      {{ t("settings.resetDefaults") }}
    </el-button>
  </aside>
</template>

<style scoped>
.settings-dialog__nav-dot {
  width: 7px;
  height: 7px;
  margin-left: auto;
  border-radius: 50%;
  background: var(--el-color-danger);
}
</style>
