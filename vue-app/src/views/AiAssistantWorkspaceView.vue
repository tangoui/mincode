<script setup lang="ts">
import { useVueI18n } from "@/i18n/messages";

// AI 标签页的真实界面由全局单例 AiAssistantDrawer 以覆盖层形式渲染，
// 保证关闭标签页不会拆除 AI 运行时监听与后台任务。这里只提供加载占位。
defineOptions({ name: "AiAssistantWorkspaceView" });
const { t } = useVueI18n();
</script>

<template>
  <div class="ai-assistant-workspace-placeholder" role="status" :aria-label="t('common.loading')">
    <span>{{ t("common.loading") }}</span>
  </div>
</template>

<style scoped>
.ai-assistant-workspace-placeholder {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  color: var(--app-text-muted);
  font-size: 12px;
}
</style>
