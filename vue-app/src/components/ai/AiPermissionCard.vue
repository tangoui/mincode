<script setup lang="ts">
import { Lock } from "@element-plus/icons-vue";

interface PermissionOption {
  optionId: string;
  name: string;
  kind?: string;
}

defineProps<{
  title: string;
  description: string;
  cancelLabel: string;
  options: PermissionOption[];
}>();

const emit = defineEmits<{ decide: [optionId?: string] }>();
</script>

<template>
  <section class="ai-permission-card" role="alertdialog" :aria-label="title">
    <header class="ai-permission-card__header">
      <el-icon class="ai-permission-card__icon" aria-hidden="true"><Lock /></el-icon>
      <span class="ai-permission-card__copy">
        <strong>{{ title }}</strong>
        <small>{{ description }}</small>
      </span>
    </header>
    <div class="ai-permission-card__actions">
      <el-button
        v-for="option in options"
        :key="option.optionId"
        :data-option-id="option.optionId"
        :type="option.kind?.startsWith('allow') ? 'primary' : 'default'"
        size="small"
        @click="emit('decide', option.optionId)"
      >{{ option.name }}</el-button>
      <el-button data-action="cancel" size="small" @click="emit('decide', undefined)">{{ cancelLabel }}</el-button>
    </div>
  </section>
</template>
