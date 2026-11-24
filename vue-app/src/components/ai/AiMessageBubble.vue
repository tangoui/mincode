<script setup lang="ts">
import { ArrowDown, ArrowUp, CopyDocument, Document, EditPen, RefreshRight } from "@element-plus/icons-vue";
import { ElCard, ElMessage } from "element-plus";
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import AiImagePreviewToolbar from "@/components/ai/AiImagePreviewToolbar.vue";
import MarkdownPreview from "@/components/ui/MarkdownPreview.vue";
import { getAttachmentImageSrc } from "@/domain/ai/attachments";
import type { AiAttachment } from "@/domain/ai/attachments";
import { createAiMessageClipboardPayload } from "@/domain/ai/message-clipboard";
import {
  formatAiMessageFullTime,
  formatAiMessageShortTime,
  normalizeAiMessageTimestamp,
} from "@/domain/ai/message-time";
import { copyRichContent } from "@/services/clipboard";
import { aiReplyModel, compactTokenCount, unreportedAiTokenRequestCount } from "@/domain/ai/token-usage";
import { aiMessageExpansion } from "@/domain/ai/message-expansion";
import { useVueI18n } from "@/i18n/messages";
import type { AiAssistantMessage } from "@/stores/ai-assistant";

const DEFAULT_COLLAPSED_MESSAGE_HEIGHT = 320;
const MIN_COLLAPSED_MESSAGE_HEIGHT = 160;
// Leave room for the message card padding, footer controls, and conversation
// viewport spacing so a collapsed message fits within one visible page.
const MESSAGE_VIEWPORT_RESERVED_HEIGHT = 72;
// A pinned user prompt is useful while an agent works, but a full transcript
// must never cover the response that follows it.
const MAX_STICKY_USER_TURN_HEIGHT = 96;

const props = defineProps<{
  message: AiAssistantMessage;
  streaming?: boolean;
  pinned?: boolean;
  emptyMessageLabel: string;
  metadataLabel: string;
  expandLabel: string;
  collapseLabel: string;
      copyLabel: string;
  copiedLabel: string;
  copyFailedLabel: string;
  copyImageLabel: string;
  copiedImageLabel: string;
  copyImageFailedLabel: string;
  editable?: boolean;
  editLabel?: string;
  regenerable?: boolean;
  regenerateLabel?: string;
}>();
const emit = defineEmits<{ edit: [message: AiAssistantMessage]; regenerate: [message: AiAssistantMessage] }>();
const { t } = useVueI18n();

const tokenUsageLabel = computed(() => {
  const usage = props.message.tokenUsage;
  if (!usage) return "";
  const label = t("ai.message.tokenUsage", {
    total: compactTokenCount(usage.inputTokens + usage.outputTokens),
    cached: compactTokenCount(usage.cachedInputTokens),
  });
  // Request counts only matter when some requests returned no usage.
  return unreportedAiTokenRequestCount(usage) > 0
    ? t("ai.message.tokenUsagePartial", { label, reported: usage.requestCount, attempted: usage.attemptedRequestCount })
    : label;
});
const replyModel = computed(() => aiReplyModel(props.message.tokenUsage));
const replyModelTitle = computed(() => {
  const model = replyModel.value;
  if (!model) return "";
  const others = (props.message.tokenUsage?.models ?? []).filter((item) => item !== model);
  return others.length
    ? t("ai.message.answeredByWithAttempts", { model, others: others.join(", ") })
    : t("ai.message.answeredBy", { model });
});
const tokenUsageTitle = computed(() => {
  const usage = props.message.tokenUsage;
  if (!usage) return "";
  return t("ai.message.tokenUsageDetail", {
    input: usage.inputTokens,
    output: usage.outputTokens,
    cached: usage.cachedInputTokens,
    cacheWrite: usage.cacheWriteTokens,
    reasoning: usage.reasoningTokens,
    reported: usage.requestCount,
    attempted: usage.attemptedRequestCount,
    unreported: unreportedAiTokenRequestCount(usage),
  });
});

const markdownHeadingPattern = /^(#{1,6})\s+(.+?)\s*$/;
const metadataHeadings = new Set(["attachment", "last-prompt", "permission-mode"]);
const body = ref<HTMLElement>();
const expanded = ref(aiMessageExpansion.initiallyExpanded(props.message, Boolean(props.streaming)));
const collapsible = ref(false);
const contentHeight = ref(0);
const collapsedMessageHeight = ref(DEFAULT_COLLAPSED_MESSAGE_HEIGHT);
const shouldPinUserTurn = computed(
  () => props.pinned && contentHeight.value > 0 && contentHeight.value <= MAX_STICKY_USER_TURN_HEIGHT,
);
let resizeObserver: ResizeObserver | undefined;
let messageViewport: HTMLElement | undefined;

function messagePresentation(content = "") {
  // Metadata sections always start with a markdown heading.
  if (!content.includes("#")) return { content: content.trim(), metadata: [] };
  const metadata: Array<{ title: string; content: string }> = [];
  const visibleLines: string[] = [];
  let currentMetadata: { title: string; lines: string[] } | undefined;

  function flushMetadata() {
    if (!currentMetadata) return;
    metadata.push({ title: currentMetadata.title, content: currentMetadata.lines.join("\n").trim() });
    currentMetadata = undefined;
  }

  for (const line of content.split("\n")) {
    const heading = line.trim().match(markdownHeadingPattern);
    if (heading) {
      flushMetadata();
      const title = heading[2] ?? "";
      if (metadataHeadings.has(title.toLowerCase())) {
        currentMetadata = { title, lines: [] };
        continue;
      }
    }
    if (currentMetadata) currentMetadata.lines.push(line);
    else visibleLines.push(line);
  }
  flushMetadata();

  return { content: visibleLines.join("\n").trim(), metadata };
}

const presentation = computed(() => messagePresentation(props.message.content));
const canCopyMessage = computed(() => Boolean(
  props.message.content.trim() || props.message.attachments?.some((attachment) =>
    attachment.name.trim() || (attachment.kind === "image" && attachment.data.trim())
  ),
));
const timestamp = computed(() => normalizeAiMessageTimestamp(props.message.createdAt));
const shortTime = computed(() => formatAiMessageShortTime(timestamp.value));
const fullTime = computed(() => formatAiMessageFullTime(timestamp.value));
const failedImageAttachmentIds = ref(new Set<string>());
const previewImageAttachments = computed(() =>
  (props.message.attachments ?? []).filter(
    (attachment) => attachment.kind === "image"
      && getAttachmentImageSrc(attachment)
      && !failedImageAttachmentIds.value.has(attachment.id),
  ),
);
const previewImageUrls = computed(() =>
  previewImageAttachments.value.map(getAttachmentImageSrc),
);

function canShowImageAttachment(attachment: AiAttachment) {
  return attachment.kind === "image"
    && Boolean(getAttachmentImageSrc(attachment))
    && !failedImageAttachmentIds.value.has(attachment.id);
}

function markImageAttachmentFailed(attachmentId: string) {
  failedImageAttachmentIds.value = new Set(failedImageAttachmentIds.value).add(attachmentId);
}

function setBodyMeasurementObserved(observed: boolean) {
  if (!resizeObserver || !body.value) return;
  if (observed) resizeObserver.observe(body.value);
  else resizeObserver.unobserve(body.value);
}

function measureMessage() {
  if (!body.value) return;
  const viewportHeight = messageViewport?.clientHeight ?? 0;
  collapsedMessageHeight.value = viewportHeight > 0
    ? Math.max(MIN_COLLAPSED_MESSAGE_HEIGHT, viewportHeight - MESSAGE_VIEWPORT_RESERVED_HEIGHT)
    : DEFAULT_COLLAPSED_MESSAGE_HEIGHT;
  contentHeight.value = body.value.scrollHeight;
  const nextCollapsible = contentHeight.value > collapsedMessageHeight.value + 2;
  collapsible.value = nextCollapsible;
}

function toggleExpanded() {
  expanded.value = !expanded.value;
  if (props.message.id) aiMessageExpansion.remember(props.message.id, expanded.value);
}

async function copyMessage() {
  if (!canCopyMessage.value) return;
  try {
    const clipboardPayload = createAiMessageClipboardPayload(
      props.message.content,
      props.message.attachments,
    );
    await copyRichContent(clipboardPayload);
    ElMessage.success(props.copiedLabel);
  } catch (error) {
    ElMessage.error(props.copyFailedLabel.replace("{message}", String(error)));
  }
}

watch(
  () => props.streaming,
  (streaming) => {
    setBodyMeasurementObserved(!streaming);
    if (!streaming) void nextTick(measureMessage);
  },
);

onMounted(() => {
  resizeObserver = new ResizeObserver(measureMessage);
  if (body.value) {
    setBodyMeasurementObserved(!props.streaming);
    messageViewport = body.value.closest<HTMLElement>(".ai-assistant-messages > .el-scrollbar__wrap") ?? undefined;
    if (messageViewport) resizeObserver.observe(messageViewport);
  }
  void nextTick(measureMessage);
});

onBeforeUnmount(() => {
  resizeObserver?.disconnect();
});
</script>

<template>
  <article
    :class="[
      'ai-message-bubble',
      `is-${message.role}`,
      { 'is-pinned-user-turn': shouldPinUserTurn },
    ]"
    :data-ai-message-id="message.role === 'user' ? message.id : undefined"
  >
    <component
      :is="message.role === 'assistant' ? 'div' : ElCard" :class="['ai-message-card', `is-${message.role}`]"
      :shadow="message.role === 'assistant' ? undefined : 'never'"
    >
      <div
        ref="body"
        class="ai-message-card__content"
        :class="{ 'is-collapsed': collapsible && !expanded }"
        :style="{ '--ai-message-collapse-height': `${collapsedMessageHeight}px` }"
      >
        <div v-if="message.attachments?.length" class="ai-message-attachments">
          <div v-for="attachment in message.attachments" :key="attachment.id" :title="attachment.name">
            <el-image
              v-if="canShowImageAttachment(attachment)"
              :src="getAttachmentImageSrc(attachment)"
              :alt="attachment.name"
              :preview-src-list="previewImageUrls"
              :initial-index="previewImageUrls.indexOf(getAttachmentImageSrc(attachment))"
              preview-teleported
              hide-on-click-modal
              fit="cover"
              @error="markImageAttachmentFailed(attachment.id)"
            >
              <template #toolbar="{ actions, reset, activeIndex }">
                <AiImagePreviewToolbar
                  :attachments="previewImageAttachments"
                  :active-index="activeIndex"
                  :actions="actions"
                  :reset="reset"
                                                      :copy-label="copyImageLabel"
                  :copied-label="copiedImageLabel"
                  :copy-failed-label="copyImageFailedLabel"
                />
              </template>
            </el-image>
            <template v-else>
              <Document aria-hidden="true" />
              <span>{{ attachment.name }}</span>
            </template>
          </div>
        </div>
        <!-- One instance for streaming and finished states, so the reply is not rebuilt when it ends.
             Metadata sections (imported sessions only) are split only once the reply has finished. -->
        <MarkdownPreview
          v-if="streaming ? message.content : presentation.content"
          inline
          code-copy
          :streaming="streaming"
          :content="streaming ? message.content : presentation.content"
        />
        <el-collapse v-if="!streaming && presentation.metadata.length" class="ai-message-metadata">
          <el-collapse-item name="metadata">
            <template #title>{{ metadataLabel }}</template>
            <section
              v-for="item in presentation.metadata"
              :key="item.title"
              class="ai-message-metadata__section"
            >
              <el-tag type="info" effect="plain" size="small">{{ item.title }}</el-tag>
              <MarkdownPreview v-if="item.content" inline code-copy :content="item.content" />
              <el-empty v-else :description="emptyMessageLabel" :image-size="28" />
            </section>
          </el-collapse-item>
        </el-collapse>
        <el-empty
          v-if="!message.content?.trim() && !message.attachments?.length"
          class="ai-message-empty"
          :description="emptyMessageLabel"
          :image-size="36"
        />
      </div>
    </component>

    <footer v-if="collapsible || timestamp || replyModel || tokenUsageLabel || canCopyMessage || editable || regenerable" class="ai-message-bubble__footer">
      <el-button
        v-if="collapsible"
        text
        class="ai-message-bubble__expand"
        :icon="expanded ? ArrowUp : ArrowDown"
        :aria-expanded="expanded"
        @click="toggleExpanded"
      >
        {{ expanded ? collapseLabel : expandLabel }}
      </el-button>
      <time v-if="timestamp" :datetime="new Date(timestamp).toISOString()" :title="fullTime">
        {{ shortTime }}
      </time>
      <span v-if="replyModel" class="ai-message-bubble__model" :title="replyModelTitle">{{ replyModel }}</span>
      <span v-if="tokenUsageLabel" class="ai-message-bubble__token-usage" :title="tokenUsageTitle">
        {{ tokenUsageLabel }}
      </span>
      <el-button
        v-if="editable"
        text
        :icon="EditPen"
        :aria-label="editLabel"
        :title="editLabel"
        @click="emit('edit', message)"
      />
      <el-button
        v-if="regenerable"
        text
        :icon="RefreshRight"
        :aria-label="regenerateLabel"
        :title="regenerateLabel"
        @click="emit('regenerate', message)"
      />
      <el-button
        v-if="canCopyMessage"
        text
        class="ai-message-bubble__copy"
        :icon="CopyDocument"
        :aria-label="copyLabel"
        @click="copyMessage"
      />
    </footer>
  </article>
</template>
