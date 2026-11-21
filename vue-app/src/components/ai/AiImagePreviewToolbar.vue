<script setup lang="ts">
import {
  CopyDocument,
  FullScreen,
  RefreshLeft,
  RefreshRight,
  ZoomIn,
  ZoomOut,
} from "@element-plus/icons-vue";
import { ElMessage, type ImageViewerAction } from "element-plus";
import type { AiAttachment } from "@/domain/ai/attachments";
import { createAiMessageClipboardPayload } from "@/domain/ai/message-clipboard";
import { copyImage } from "@/services/clipboard";

const props = defineProps<{
  attachments: AiAttachment[];
  activeIndex: number;
  actions: (action: ImageViewerAction, options?: object) => void;
  reset: () => void;
  copyLabel: string;
  copiedLabel: string;
  copyFailedLabel: string;
}>();

async function copyActiveImage() {
  const attachment = props.attachments[props.activeIndex];
  if (!attachment) return;
  try {
        const image = createAiMessageClipboardPayload("", [attachment]).images[0];
    if (!image) return;
    await copyImage(image);
    ElMessage.success(props.copiedLabel);
  } catch (error) {
    ElMessage.error(props.copyFailedLabel.replace("{message}", String(error)));
  }
}
</script>

<template>
  <el-icon @click="actions('zoomOut')"><ZoomOut /></el-icon>
  <el-icon @click="actions('zoomIn')"><ZoomIn /></el-icon>
  <i class="el-image-viewer__actions__divider" />
  <el-icon @click="reset"><FullScreen /></el-icon>
  <i class="el-image-viewer__actions__divider" />
  <el-icon @click="actions('anticlockwise')"><RefreshLeft /></el-icon>
  <el-icon @click="actions('clockwise')"><RefreshRight /></el-icon>
  <i class="el-image-viewer__actions__divider" />
  <el-tooltip :content="copyLabel" placement="top">
    <el-icon
      class="ai-image-viewer-toolbar__copy"
      role="button"
      tabindex="0"
      :aria-label="copyLabel"
      @click="copyActiveImage"
      @keydown.enter.prevent="copyActiveImage"
      @keydown.space.prevent="copyActiveImage"
    >
      <CopyDocument />
    </el-icon>
  </el-tooltip>
</template>

<style scoped>
.ai-image-viewer-toolbar__copy {
  cursor: pointer;
}
</style>
