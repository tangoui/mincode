<script setup lang="ts">
import { Refresh, VideoPlay } from "@element-plus/icons-vue";
import { computed, provide, ref, shallowReactive, watch } from "vue";
import AiMessageBubble from "@/components/ai/AiMessageBubble.vue";
import AiTimelineApprovalRow, { type InlineToolApprovalLabels } from "@/components/ai/timeline/AiTimelineApprovalRow.vue";
import AiTimelineDelegationRow from "@/components/ai/timeline/AiTimelineDelegationRow.vue";
import AiTimelineErrorRow from "@/components/ai/timeline/AiTimelineErrorRow.vue";
import AiTimelineNoticeRow from "@/components/ai/timeline/AiTimelineNoticeRow.vue";
import AiTimelinePlanRow from "@/components/ai/timeline/AiTimelinePlanRow.vue";
import AiTimelineThoughtRow from "@/components/ai/timeline/AiTimelineThoughtRow.vue";
import AiTimelineToolRow from "@/components/ai/timeline/AiTimelineToolRow.vue";
import AiTimelineToolSummaryRow from "@/components/ai/timeline/AiTimelineToolSummaryRow.vue";
import { aiTimelineExpansion, type AiTimelineLabels } from "@/components/ai/timeline/timeline-row-state";
import { groupAiEventsForTimeline } from "@/domain/ai/agent-event-groups";
import type { AiAgentActivityKind } from "@/domain/ai/agent-event-activity";
import type { AiTaskStepStatus } from "@/domain/ai/event-presentations";
import { mergeAiTokenUsage, type AiTokenUsage } from "@/domain/ai/token-usage";
import { buildAiTimelineItems, isAiTimelineDelegationSettled, type AiTimelineDelegationChild } from "@/domain/ai/timeline-items";
import type { ToolApprovalDecision, ToolApprovalQueueItem } from "@/domain/ai/tool-approval-queue";
import {
  alignAiTimelineRenderStartToTurn,
  earlierAiMessageRenderStart,
  filledAiMessageRenderStart,
  firstPaintAiMessageRenderStart,
  initialAiMessageRenderStart,
} from "@/domain/ai/message-render-window";
import { useVueI18n } from "@/i18n/messages";
import type { AiAssistantEvent, AiAssistantMessage, AiAssistantMode, AiAssistantPermission } from "@/stores/ai-assistant";

const props = defineProps<{
  pinnedUserTarget?: HTMLElement;
  messages: AiAssistantMessage[];
  events: AiAssistantEvent[];
  delegatedChildren?: AiTimelineDelegationChild[];
  delegationModelLabels?: Record<string, string>;
  delegationStatusLabels?: Record<string, string>;
  approvals?: ToolApprovalQueueItem[];
  approvalLabels?: InlineToolApprovalLabels;
  permission: AiAssistantPermission | null;
  composerHasDraft: boolean;
  expansionChoices?: Map<string, boolean>;
  mode: AiAssistantMode;
  title: string;
  hint: string;
  activityLabels: Record<AiAgentActivityKind | "detail" | "hideDetail" | "toolName" | "result" | "runningDuration" | "totalDuration" | "duration", string>;
  taskStepsLabel: string;
  dismissTaskStepsLabel: string;
  sourcesLabel: string;
  artifactsLabel: string;
  previewLabel: string;
  collapseLabel: string;
  copyLabel: string;
  copiedLabel: string;
  copyResultLabel: string;
  copiedResultLabel: string;
  copyResultFailedLabel: string;
  eventStatusLabels: Record<string, string>;
  taskStepStatusLabels: Record<AiTaskStepStatus, string>;
  emptyMessageLabel: string;
  metadataLabel: string;
  expandMessageLabel: string;
  collapseMessageLabel: string;
  copyMessageLabel: string;
  copiedMessageLabel: string;
  copyMessageFailedLabel: string;
  copyImageLabel: string;
  copiedImageLabel: string;
  copyImageFailedLabel: string;
  /** Waiting for first output: shows the thinking row. */
  running: boolean;
  runningLabel?: string;
  /** The conversation is running; its current turn's latest reply is still being generated. */
  generating?: boolean;
  startLabel?: string;
  defaultDirectoryActionLabel?: string;
  effectiveDirectory?: string;
  effectiveDirectoryLabel?: string;
  stopAgentLabel?: string;
  starting?: boolean;
  failed?: boolean;
  editFailedLabel?: string;
  loadEarlierLabel?: string;
  /** Sent turns can be edited, regenerated or retried (idle, non-ACP conversations). */
  canRewind?: boolean;
  editMessageLabel?: string;
  regenerateLabel?: string;
  errorLabels?: { title: string; hint: string; retry: string; settings: string };
  /** The latest step-limit message offers "continue"; disabled while the conversation runs. */
  canContinue?: boolean;
  continueLabel?: string;
}>();

const emit = defineEmits<{
  start: [];
  startInDefaultDirectory: [];
  stopAgent: [];
  "decide-approval": [id: string, decision: ToolApprovalDecision];
  "answer-permission": [optionId?: string];
  "dismiss-event": [event: AiAssistantEvent];
  "open-delegation": [id: string];
  "edit-failed": [message: AiAssistantMessage];
  "load-earlier": [count: number];
  regenerate: [message: AiAssistantMessage];
  retry: [message: AiAssistantMessage];
  continue: [];
  "open-settings": [];
}>();

const { t } = useVueI18n();
provide(aiTimelineExpansion, props.expansionChoices ?? shallowReactive(new Map<string, boolean>()));

function isLastMessage(index: number) {
  return index === props.messages.length - 1;
}
const messageIndexes = computed(() => {
  const indexes = new Map<string, number>();
  for (let index = 0; index < props.messages.length; index += 1) {
    const id = props.messages[index]?.id;
    if (id) indexes.set(id, index);
  }
  return indexes;
});
const anchoredEvents = computed(() => {
  const grouped = new Map<number, AiAssistantEvent[]>();
  for (const event of props.events) {
    const triggerIndex = event.triggerMessageId
      ? messageIndexes.value.get(event.triggerMessageId)
      : undefined;
    const anchorIndex = triggerIndex ?? event.afterMessageIndex;
    if (anchorIndex === undefined) continue;
    const events = grouped.get(anchorIndex) ?? [];
    events.push(event);
    grouped.set(anchorIndex, events);
  }
  return grouped;
});
const timelineEventGroups = computed(() =>
  groupAiEventsForTimeline(props.messages, props.events, messageIndexes.value),
);
const timelineEventGroupsByAnchor = computed(() =>
  new Map(timelineEventGroups.value.map((group) => [group.anchorIndex, group])),
);
const activeEvents = computed(() => props.events.filter((event) => {
  if (event.triggerMessageId && messageIndexes.value.has(event.triggerMessageId)) return false;
  return event.afterMessageIndex === undefined;
}));

const anchoredApprovals = computed(() => {
  const grouped = new Map<number, ToolApprovalQueueItem[]>();
  for (const approval of props.approvals ?? []) {
    const anchorIndex = approval.triggerMessageId
      ? messageIndexes.value.get(approval.triggerMessageId)
      : undefined;
    if (anchorIndex === undefined) continue;
    const approvals = grouped.get(anchorIndex) ?? [];
    approvals.push(approval);
    grouped.set(anchorIndex, approvals);
  }
  return grouped;
});
const activeApprovals = computed(() => (props.approvals ?? []).filter((approval) =>
  !approval.triggerMessageId || !messageIndexes.value.has(approval.triggerMessageId),
));
function shouldRenderMessage(message: AiAssistantMessage, index: number) {
  if (message.presentation || message.content?.trim() || message.attachments?.length) return true;
  return !message.eventAnchor && !anchoredEvents.value.get(index)?.length;
}

const streamingAssistantMessageIndex = computed(() => {
  if (!props.generating) return -1;
  for (let index = props.messages.length - 1; index >= 0; index -= 1) {
    const message = props.messages[index];
    // Only the current turn streams; never re-mark the previous turn's answer.
    if (message?.role === "user") return -1;
    if (message?.role === "assistant" && !message.presentation && message.content.trim()) return index;
  }
  return -1;
});

function isStreamingAssistantMessage(message: AiAssistantMessage, index: number) {
  return message.role === "assistant" && streamingAssistantMessageIndex.value === index;
}

const continueAt = computed(() => {
  const index = props.messages.length - 1;
  const last = props.messages[index];
  return props.continueLabel && last?.role === "assistant" && last.presentation === "loop-limit" ? index : null;
});
const visibleMessages = computed(() => props.messages.flatMap((message, index) =>
  shouldRenderMessage(message, index) ? [{ message, index }] : [],
));
const finalReplyMetadata = computed(() => {
  const metadata = new Map<number, AiTokenUsage | undefined>();
  const renderedIndexes = new Set(visibleMessages.value.map(({ index }) => index));
  let finalIndex: number | undefined;
  let usage: AiTokenUsage | undefined;
  for (const [index, message] of props.messages.entries()) {
    if (message.role === "user") {
      if (finalIndex !== undefined) metadata.set(finalIndex, usage);
      finalIndex = undefined;
      usage = undefined;
    } else if (message.role === "assistant") {
      if (renderedIndexes.has(index)) finalIndex = index;
      if (message.tokenUsage) usage = mergeAiTokenUsage(usage, message.tokenUsage);
    }
  }
  if (finalIndex !== undefined) metadata.set(finalIndex, usage);
  return metadata;
});
const timelineItems = computed(() => buildAiTimelineItems({
  messages: visibleMessages.value,
  groupsByAnchor: timelineEventGroupsByAnchor.value,
  activeEvents: activeEvents.value,
  anchoredApprovals: anchoredApprovals.value,
  activeApprovals: activeApprovals.value,
  permission: props.permission ?? null,
  continueAt: continueAt.value,
  running: props.running,
  conversationRunning: props.generating,
  delegations: props.delegatedChildren?.length
    ? { children: props.delegatedChildren, messages: props.messages }
    : undefined,
}));
const itemCount = computed(() => timelineItems.value.length);
const windowStart = ref(firstPaintAiMessageRenderStart(itemCount.value));
const widenedByReader = ref(false);
const visibleStart = computed(() => alignAiTimelineRenderStartToTurn(timelineItems.value, windowStart.value));
const visibleItems = computed(() => {
  const start = visibleStart.value;
  const items = timelineItems.value;
  if (!start) return items;
  // Keep an older pending decision visible with the tool that owns it.
  const alwaysVisible = new Set<number>();
  for (let index = 0; index <= start; index += 1) {
    const item = items[index];
    if (item?.kind === "delegation" && (index === start || item.children.some(child => !isAiTimelineDelegationSettled(child)))) {
      if (index < start) alwaysVisible.add(index);
      if (items[index - 1]?.key === item.toolKey && item.toolKey) alwaysVisible.add(index - 1);
    }
    if (item?.kind !== "approval" && item?.kind !== "permission") continue;
    if (index < start) alwaysVisible.add(index);
    const tool = items[index - 1];
    if (tool?.kind === "tool" && tool.awaitingApproval) alwaysVisible.add(index - 1);
  }
  return [...Array.from(alwaysVisible).sort((a, b) => a - b).map((index) => items[index]!), ...items.slice(start)];
});
function loadEarlierMessages() {
  const previousStart = visibleStart.value;
  widenedByReader.value = true;
  windowStart.value = earlierAiMessageRenderStart(previousStart);
  emit("load-earlier", previousStart - visibleStart.value);
}
function revealMessage(messageId: string) {
  const index = timelineItems.value.findIndex((item) => item.kind === "message" && item.message.id === messageId);
  if (index < 0) return false;
  widenedByReader.value = true;
  windowStart.value = Math.min(windowStart.value, index);
  return true;
}
watch(itemCount, (count, previousCount) => {
  const regularStart = initialAiMessageRenderStart(count);
  if (!widenedByReader.value) {
    windowStart.value = count < previousCount ? Math.min(regularStart, windowStart.value) : Math.max(regularStart, windowStart.value);
  } else {
    windowStart.value = Math.min(windowStart.value, regularStart);
  }
});

/** Widens the window when the reader scrolls towards older activity. */
function fillRenderWindow() {
  const next = filledAiMessageRenderStart(windowStart.value, itemCount.value);
  if (next >= windowStart.value) return false;
  const previousStart = visibleStart.value;
  widenedByReader.value = true;
  windowStart.value = next;
  const added = previousStart - visibleStart.value;
  if (added > 0) emit("load-earlier", added);
  return added > 0;
}
defineExpose({ revealMessage, fillRenderWindow });

// Queued messages live outside `messages`, so the newest user message here is
// the turn that most recently started executing. Keep it pinned until another
// queued item actually enters the conversation and becomes the next turn.
const pinnedUserMessageIndex = computed(() => {
  for (let index = props.messages.length - 1; index >= 0; index -= 1) {
    const message = props.messages[index];
    if (message?.role === "user" && shouldRenderMessage(message, index)) return index;
  }
  return -1;
});

const labels: AiTimelineLabels = {
  get activity() { return props.activityLabels; },
  get taskSteps() { return props.taskStepsLabel; },
  get dismissTaskSteps() { return props.dismissTaskStepsLabel; },
  get sources() { return props.sourcesLabel; },
  get artifacts() { return props.artifactsLabel; },
  get preview() { return props.previewLabel; },
  get collapse() { return props.collapseLabel; },
  get copy() { return props.copyLabel; },
  get copied() { return props.copiedLabel; },
  get copyResult() { return props.copyResultLabel; },
  get copiedResult() { return props.copiedResultLabel; },
  get copyResultFailed() { return props.copyResultFailedLabel; },
  get eventStatuses() { return props.eventStatusLabels; },
  get taskStepStatuses() { return props.taskStepStatusLabels; },
  get parameters() { return props.approvalLabels?.parameters ?? t("ai.agentic.approvalParameters"); },
};
</script>

<template>
  <div class="ai-message-thread">
    <div
      v-if="mode === 'agent' && (defaultDirectoryActionLabel || effectiveDirectory)"
      class="flex flex-wrap items-center gap-2"
    >
      <el-button
        v-if="defaultDirectoryActionLabel"
        size="small"
        :loading="starting"
        @click="emit('startInDefaultDirectory')"
      >
        {{ defaultDirectoryActionLabel }}
      </el-button>
      <span
        v-if="effectiveDirectory"
        class="min-w-0 break-all text-xs"
        :title="effectiveDirectory"
      >{{ effectiveDirectoryLabel }}</span>
      <el-button
        v-if="effectiveDirectory && stopAgentLabel"
        size="small"
        :disabled="running || starting"
        @click="emit('stopAgent')"
      >
        {{ stopAgentLabel }}
      </el-button>
    </div>
    <div v-if="!messages.length && !events.length && !delegatedChildren?.length" class="ai-assistant-welcome">
      <div>AI</div>
      <strong>{{ title }}</strong>
      <p>{{ hint }}</p>
                      <el-button
        v-if="mode === 'agent' && startLabel && !effectiveDirectory"
        :icon="Refresh"
        :loading="starting"
        @click="emit('start')"
      >
        {{ startLabel }}
      </el-button>
    </div>
    <el-button
      v-if="visibleStart"
      class="ai-message-thread__load-earlier"
      text
      @click="loadEarlierMessages"
    >
      {{ loadEarlierLabel }}
    </el-button>
    <template v-for="item in visibleItems" :key="item.key">
      <AiMessageBubble
        v-if="item.kind === 'message'"
        :message="item.message"
        :hide-metadata="item.message.role === 'assistant' && !finalReplyMetadata.has(item.index)"
        :turn-token-usage="finalReplyMetadata.get(item.index)"
        :streaming="isStreamingAssistantMessage(item.message, item.index)"
        :pinned="item.index === pinnedUserMessageIndex"
        :pinned-target="pinnedUserTarget"
        :empty-message-label="emptyMessageLabel"
        :metadata-label="metadataLabel"
        :expand-label="expandMessageLabel"
        :collapse-label="collapseMessageLabel"
        :copy-label="copyMessageLabel"
        :copied-label="copiedMessageLabel"
        :copy-failed-label="copyMessageFailedLabel"
        :copy-image-label="copyImageLabel"
        :copied-image-label="copiedImageLabel"
        :copy-image-failed-label="copyImageFailedLabel"
        :editable="item.message.role === 'user' && (canRewind || (failed && item.index === pinnedUserMessageIndex))"
        :edit-label="canRewind ? editMessageLabel : editFailedLabel"
        :regenerable="Boolean(canRewind) && item.message.role === 'assistant' && isLastMessage(item.index)"
        :regenerate-label="regenerateLabel"
        @edit="emit('edit-failed', $event)"
        @regenerate="emit('regenerate', $event)"
      >
        <template v-if="item.message.role === 'assistant' && item.message.presentation === 'error' && errorLabels" #body>
          <AiTimelineErrorRow
            :title="errorLabels.title"
            :detail="item.message.content"
            :hint="errorLabels.hint"
            :retry-label="errorLabels.retry"
            :settings-label="errorLabels.settings"
            :can-retry="Boolean(canRewind) && isLastMessage(item.index)"
            @retry="emit('retry', item.message)"
            @open-settings="emit('open-settings')"
          />
        </template>
      </AiMessageBubble>
      <div
        v-else-if="item.kind === 'continue'"
        class="ai-message-thread__continue"
      >
        <el-button size="small" type="primary" :icon="VideoPlay" :disabled="!canContinue" @click="emit('continue')">
          {{ continueLabel }}
        </el-button>
      </div>
      <AiTimelineToolRow
        v-else-if="item.kind === 'tool'" :event="item.event" :awaiting-approval="item.awaitingApproval"
        :conversation-running="Boolean(generating || running)" :labels="labels"
      />
      <AiTimelineToolSummaryRow
        v-else-if="item.kind === 'toolSummary'" :events="item.events" :running="item.running"
        :conversation-running="Boolean(generating || running)" :labels="labels"
      />
      <AiTimelineThoughtRow
        v-else-if="item.kind === 'thought'" :event="item.event" :labels="labels"
        :conversation-running="Boolean(generating || running)"
      />
      <AiTimelinePlanRow
        v-else-if="item.kind === 'plan'" :event="item.event" :labels="labels" @dismiss="emit('dismiss-event', item.event)"
      />
      <AiTimelineNoticeRow
        v-else-if="item.kind === 'mode'"
        :text="item.event.id === 'mode' || item.event.title === t('ai.mode')
          ? t('ai.timeline.modeChanged', { mode: item.event.detail.trim() || item.event.title })
          : [item.event.title, item.event.detail].filter(Boolean).join(' · ')"
      />
      <AiTimelineApprovalRow
        v-else-if="item.kind === 'approval' && approvalLabels" :source="{ kind: 'tool', approval: item.approval }"
        :labels="approvalLabels" :composer-has-draft="composerHasDraft"
        @decide-tool="emit('decide-approval', item.approval.id, $event)"
      />
      <AiTimelineApprovalRow
        v-else-if="item.kind === 'permission'" :source="{ kind: 'permission', permission: item.permission }"
        :composer-has-draft="composerHasDraft"
        @decide-permission="emit('answer-permission', $event)"
      />
      <AiTimelineDelegationRow
        v-else-if="item.kind === 'delegation'" :group="item" :model-labels="delegationModelLabels"
        :status-labels="delegationStatusLabels" @open="emit('open-delegation', $event)"
      />
      <AiTimelineNoticeRow v-else-if="item.kind === 'thinking'" :text="runningLabel ?? t('ai.timeline.thinking')" spinning />
    </template>
  </div>
</template>
