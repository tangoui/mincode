<script setup lang="ts">
import { WarningFilled } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";

defineProps<{
  title?: string;
  description: string;
  retryable?: boolean;
}>();

const emit = defineEmits<{
  retry: [];
}>();

const { t } = useVueI18n();
</script>

<template>
  <div class="plugin-view-unavailable" role="status">
    <el-icon class="plugin-view-unavailable__icon" aria-hidden="true"><WarningFilled /></el-icon>
    <strong>{{ title || t("plugins.viewUnavailable") }}</strong>
    <p>{{ description }}</p>
    <el-button v-if="retryable" native-type="button" @click="emit('retry')">
      {{ t("plugins.retryView") }}
    </el-button>
  </div>
</template>
