<script setup lang="ts">
import { CircleCloseFilled, RefreshRight, Setting } from "@element-plus/icons-vue";
import { ElButton } from "element-plus";

defineProps<{
  title: string;
  detail: string;
  hint: string;
  retryLabel: string;
  settingsLabel: string;
  canRetry: boolean;
}>();
const emit = defineEmits<{ retry: []; "open-settings": [] }>();
</script>

<template>
  <section class="ai-timeline-error" role="alert">
    <div class="ai-timeline__row">
      <CircleCloseFilled class="ai-timeline__icon" aria-hidden="true" />
      <strong class="ai-timeline__title">{{ title }}</strong>
    </div>
    <div class="ai-timeline__contents">
      <p class="ai-timeline__error-detail">{{ detail }}</p>
      <div class="ai-timeline__approval-actions">
        <ElButton v-if="canRetry" size="small" :icon="RefreshRight" @click="emit('retry')">{{ retryLabel }}</ElButton>
        <ElButton size="small" :icon="Setting" @click="emit('open-settings')">{{ settingsLabel }}</ElButton>
        <small v-if="canRetry">{{ hint }}</small>
      </div>
    </div>
  </section>
</template>
