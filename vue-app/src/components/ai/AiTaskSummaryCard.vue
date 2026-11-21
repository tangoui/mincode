<script setup lang="ts">
import { CircleCheck, CopyDocument } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { computed } from "vue";
import MarkdownPreview from "@/components/projects/MarkdownPreview.vue";
import { createAiMessageClipboardPayload } from "@/domain/ai/message-clipboard";
import {
  formatAiMessageFullTime,
  formatAiMessageShortTime,
  normalizeAiMessageTimestamp,
} from "@/domain/ai/message-time";
import { copyRichContent } from "@/services/clipboard";
import { aiReplyModel, compactTokenCount, unreportedAiTokenRequestCount, type AiTokenUsage } from "@/domain/ai/token-usage";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  summary: string;
  title: string;
  statusLabel: string;
  createdAt?: number;
  tokenUsage?: AiTokenUsage;
  copyLabel: string;
  copiedLabel: string;
  copyFailedLabel: string;
}>();

const { t } = useVueI18n();
const replyModel = computed(() => aiReplyModel(props.tokenUsage));
const tokenUsageLabel = computed(() => {
  const usage = props.tokenUsage;
  if (!usage) return "";
  const label = t("ai.message.tokenUsage", {
    total: compactTokenCount(usage.inputTokens + usage.outputTokens),
    cached: compactTokenCount(usage.cachedInputTokens),
  });
  return unreportedAiTokenRequestCount(usage) > 0
    ? t("ai.message.tokenUsagePartial", { label, reported: usage.requestCount, attempted: usage.attemptedRequestCount })
    : label;
});
const tokenUsageTitle = computed(() => props.tokenUsage
  ? t("ai.message.tokenUsageDetail", {
      input: props.tokenUsage.inputTokens,
      output: props.tokenUsage.outputTokens,
      cached: props.tokenUsage.cachedInputTokens,
      cacheWrite: props.tokenUsage.cacheWriteTokens,
      reasoning: props.tokenUsage.reasoningTokens,
      reported: props.tokenUsage.requestCount,
      attempted: props.tokenUsage.attemptedRequestCount,
      unreported: unreportedAiTokenRequestCount(props.tokenUsage),
    })
  : "");
const clipboardPayload = computed(() => createAiMessageClipboardPayload(props.summary));
const canCopySummary = computed(() => Boolean(clipboardPayload.value.text));
const timestamp = computed(() => normalizeAiMessageTimestamp(props.createdAt));
const shortTime = computed(() => formatAiMessageShortTime(timestamp.value));
const fullTime = computed(() => formatAiMessageFullTime(timestamp.value));

async function copySummary() {
  if (!canCopySummary.value) return;
  try {
    await copyRichContent(clipboardPayload.value);
    ElMessage.success(props.copiedLabel);
  } catch (error) {
    ElMessage.error(props.copyFailedLabel.replace("{message}", String(error)));
  }
}
</script>

<template>
  <section class="ai-task-summary ai-message-bubble" :aria-label="title">
    <el-card class="ai-task-summary__card" shadow="never">
      <header class="ai-task-summary__header">
        <span class="ai-task-summary__title">
          <el-icon aria-hidden="true"><CircleCheck /></el-icon>
          <strong>{{ title }}</strong>
        </span>
        <el-tag type="success" effect="light" size="small">{{ statusLabel }}</el-tag>
      </header>
            <div class="ai-task-summary__body">
        <div v-if="summary" class="ai-task-summary__scroll">
          <MarkdownPreview inline code-copy :content="summary" />
        </div>
      </div>
    </el-card>
    <footer v-if="timestamp || replyModel || tokenUsageLabel || canCopySummary" class="ai-message-bubble__footer">
      <time v-if="timestamp" :datetime="new Date(timestamp).toISOString()" :title="fullTime">
        {{ shortTime }}
      </time>
      <span v-if="replyModel" class="ai-message-bubble__model" :title="t('ai.message.answeredBy', { model: replyModel })">{{ replyModel }}</span>
      <span v-if="tokenUsageLabel" class="ai-message-bubble__token-usage" :title="tokenUsageTitle">
        {{ tokenUsageLabel }}
      </span>
      <el-button
        v-if="canCopySummary"
        text
        class="ai-message-bubble__copy"
        :icon="CopyDocument"
        :aria-label="copyLabel"
        @click="copySummary"
      />
    </footer>
  </section>
</template>
