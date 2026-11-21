<script setup lang="ts">
import { Delete, Document, EditPen, Promotion } from "@element-plus/icons-vue";
import { ref } from "vue";
import AiImagePreviewToolbar from "@/components/ai/AiImagePreviewToolbar.vue";
import { getAttachmentImageSrc } from "@/domain/ai/attachments";
import type { AiAttachment } from "@/domain/ai/attachments";
import type { AiQueuedMessage } from "@/stores/ai-assistant";

// Messages sent while a task runs wait above the composer, one compact line each, so the thread
// below keeps showing the running turn. Editing loads a message back into the composer.
defineProps<{
  queue: AiQueuedMessage[];
  editingId?: string;
  labels: {
    title: string;
    badge: string;
    edit: string;
    delete: string;
    sendNow: string;
    attachmentsOnly: (count: number) => string;
    copyImage: string;
    copiedImage: string;
    copyImageFailed: string;
  };
}>();
const emit = defineEmits<{ edit: [id: string]; delete: [id: string]; "send-now": [id: string] }>();

const failedImageAttachmentIds = ref(new Set<string>());

function previewImageAttachments(message: AiQueuedMessage) {
  return (message.attachments ?? []).filter((attachment) => canShowImageAttachment(attachment));
}

function queuedPreviewImageUrls(message: AiQueuedMessage) {
  return previewImageAttachments(message).map(getAttachmentImageSrc);
}

function canShowImageAttachment(attachment: AiAttachment) {
  return attachment.kind === "image"
    && Boolean(getAttachmentImageSrc(attachment))
    && !failedImageAttachmentIds.value.has(attachment.id);
}

function markImageAttachmentFailed(attachmentId: string) {
  failedImageAttachmentIds.value = new Set(failedImageAttachmentIds.value).add(attachmentId);
}

function fileAttachmentCount(message: AiQueuedMessage) {
  return (message.attachments ?? []).filter((attachment) => !canShowImageAttachment(attachment)).length;
}

function summary(message: AiQueuedMessage) {
  return message.content.replace(/\s+/g, " ").trim();
}
</script>

<template>
  <section v-if="queue.length" class="ai-queued-messages" :aria-label="labels.title">
    <el-scrollbar max-height="104px">
      <ol class="ai-queued-messages__list">
        <li
          v-for="(message, index) in queue"
          :key="message.id"
          class="ai-queued-message"
          :class="{ 'is-editing': message.id === editingId }"
        >
          <span class="ai-queued-message__badge">#{{ index + 1 }} {{ labels.badge }}</span>
          <span v-if="message.attachments?.length" class="ai-queued-message__attachments">
            <el-image
              v-for="attachment in previewImageAttachments(message)"
              :key="attachment.id"
              class="ai-queued-message__thumb"
              :src="getAttachmentImageSrc(attachment)"
              :alt="attachment.name"
              :preview-src-list="queuedPreviewImageUrls(message)"
              :initial-index="queuedPreviewImageUrls(message).indexOf(getAttachmentImageSrc(attachment))"
              preview-teleported
              hide-on-click-modal
              fit="cover"
              @error="markImageAttachmentFailed(attachment.id)"
            >
              <template #toolbar="{ actions, reset, activeIndex }">
                <AiImagePreviewToolbar
                  :attachments="previewImageAttachments(message)"
                  :active-index="activeIndex"
                  :actions="actions"
                  :reset="reset"
                  :copy-label="labels.copyImage"
                  :copied-label="labels.copiedImage"
                  :copy-failed-label="labels.copyImageFailed"
                />
              </template>
            </el-image>
            <!-- An attachments-only message already reads "N attachments" in its summary. -->
            <span v-if="fileAttachmentCount(message) && summary(message)" class="ai-queued-message__files">
              <el-icon aria-hidden="true"><Document /></el-icon>{{ fileAttachmentCount(message) }}
            </span>
          </span>
          <span class="ai-queued-message__text" :title="message.content">
            {{ summary(message) || labels.attachmentsOnly(message.attachments?.length ?? 0) }}
          </span>
          <span class="ai-queued-message__actions">
            <el-tooltip :content="labels.sendNow" placement="top">
              <el-button
                text
                size="small"
                class="ai-queued-message__send-now"
                :icon="Promotion"
                :aria-label="labels.sendNow"
                @click="emit('send-now', message.id)"
              />
            </el-tooltip>
            <el-button text size="small" :icon="EditPen" :title="labels.edit" :aria-label="labels.edit" @click="emit('edit', message.id)" />
            <el-button text size="small" :icon="Delete" :title="labels.delete" :aria-label="labels.delete" @click="emit('delete', message.id)" />
          </span>
        </li>
      </ol>
    </el-scrollbar>
  </section>
</template>
