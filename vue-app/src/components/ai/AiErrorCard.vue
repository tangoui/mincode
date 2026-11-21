<script setup lang="ts">
import { CircleCloseFilled, RefreshRight, Setting } from "@element-plus/icons-vue";

defineProps<{
  title: string;
  detail: string;
  hint: string;
  retryLabel: string;
  settingsLabel: string;
  /** Retry is offered only for the latest failed turn of an idle conversation. */
  canRetry: boolean;
}>();
const emit = defineEmits<{ retry: []; "open-settings": [] }>();
</script>

<template>
  <section class="ai-error-card" role="alert">
    <header class="ai-error-card__header">
      <el-icon class="ai-error-card__icon" aria-hidden="true"><CircleCloseFilled /></el-icon>
      <strong>{{ title }}</strong>
    </header>
    <p class="ai-error-card__detail">{{ detail }}</p>
    <footer class="ai-error-card__actions">
      <el-button v-if="canRetry" size="small" type="primary" :icon="RefreshRight" @click="emit('retry')">
        {{ retryLabel }}
      </el-button>
      <el-button size="small" :icon="Setting" @click="emit('open-settings')">{{ settingsLabel }}</el-button>
      <span v-if="canRetry" class="ai-error-card__hint">{{ hint }}</span>
    </footer>
  </section>
</template>

<style scoped>
.ai-error-card {
  display: grid;
  gap: 8px;
  margin: 4px 0 12px;
  border: 1px solid color-mix(in srgb, var(--app-danger) 35%, var(--app-border));
  border-radius: 8px;
  background: color-mix(in srgb, var(--app-danger) 6%, var(--app-surface));
  padding: 10px 12px;
}

.ai-error-card__header {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--app-danger);
  font-size: 13px;
}

.ai-error-card__detail {
  margin: 0;
  color: var(--app-text);
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.ai-error-card__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.ai-error-card__actions .el-button + .el-button {
  margin-left: 0;
}

.ai-error-card__hint {
  color: var(--app-text-muted);
  font-size: 12px;
}
</style>
