<script setup lang="ts">
import { QuestionFilled } from "@element-plus/icons-vue";

/**
 * 统一问号提示图标：把设置行/卡片标题下的说明文字收进 tooltip，
 * 保持紧凑单行密度。图标可键盘聚焦，说明同时作为 aria-label 暴露给读屏器。
 */
defineProps<{ text: string }>();
</script>

<template>
  <el-tooltip
    :content="text"
    placement="top"
    :trigger="['hover', 'focus']"
    popper-class="app-hint-tooltip"
  >
    <el-button
      class="app-hint-button"
      text
      circle
      size="small"
      :icon="QuestionFilled"
      :aria-label="text"
    />
  </el-tooltip>
</template>
