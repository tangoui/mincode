<script setup lang="ts">
import { Loading, Operation } from "@element-plus/icons-vue";

defineProps<{ text: string; spinning?: boolean }>();
</script>

<template>
  <div class="ai-timeline-notice ai-timeline__row">
    <component :is="spinning ? Loading : Operation" class="ai-timeline__icon" :class="{ 'is-spinning': spinning }" aria-hidden="true" />
    <span class="ai-timeline__title"><span :class="{ 'ai-timeline__shimmer': spinning }">{{ text }}</span></span>
  </div>
</template>
