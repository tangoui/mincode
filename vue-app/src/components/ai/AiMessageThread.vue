<script setup lang="ts">
import { Loading, Refresh, VideoPlay } from "@element-plus/icons-vue";
import { computed, ref, watch } from "vue";
import AiMessageBubble from "@/components/ai/AiMessageBubble.vue";
import AiTaskSummaryCard from "@/components/ai/AiTaskSummaryCard.vue";
import AiAgentEventGroup from "@/components/ai/AiAgentEventGroup.vue";
import AiInlineToolApprovalCard, { type InlineToolApprovalLabels } from "@/components/ai/AiInlineToolApprovalCard.vue";
import AiErrorCard from "@/components/ai/AiErrorCard.vue";
import {
  groupAiEventsForTimeline,
  type AiAgentEventStatusSummary,
  type AiAnchoredEventGroup,
} from "@/domain/ai/agent-event-groups";
import type { AiAgentActivityKind } from "@/domain/ai/agent-event-activity";
import type { AiTaskStepStatus } from "@/domain/ai/event-presentations";
import type { ToolApprovalDecision, ToolApprovalQueueItem } from "@/domain/ai/tool-approval-queue";
import {
  alignAiMessageRenderStartToTurn,
  earlierAiMessageRenderStart,
  filledAiMessageRenderStart,
  firstPaintAiMessageRenderStart,
  nextAiMessageRenderStart,
} from "@/domain/ai/message-render-window";
import type { AiAssistantEvent, AiAssistantMessage, AiAssistantMode } from "@/stores/ai-assistant";

const props = defineProps<{
  messages: AiAssistantMessage[];
  events: AiAssistantEvent[];
  approvals?: ToolApprovalQueueItem[];
  approvalLabels?: InlineToolApprovalLabels;
  mode: AiAssistantMode;
  title: string;
  hint: string;
  thoughtLabel: string;
  toolLabel: string;
  planLabel: string;
  modeLabel: string;
  activityLabel: string;
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
  taskSummaryTitle: string;
  taskSummaryStatus: string;
  /** Waiting for the model's first output: shows the processing card. */
  running: boolean;
  runningLabel: string;
  /** The conversation is running; its current turn's latest reply is still being generated. */
  generating?: boolean;
  eventGroupCountLabels: Record<keyof AiAgentEventStatusSummary, string>;
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
            "dismiss-event": [event: AiAssistantEvent];
  "edit-failed": [message: AiAssistantMessage];
  "load-earlier": [count: number];
  regenerate: [message: AiAssistantMessage];
  retry: [message: AiAssistantMessage];
  continue: [];
  "open-settings": [];
}>();

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

type EventDisplayGroup = {
  groupKey: string;
  title: string;
  events: AiAssistantEvent[];
};

function activityGroup(group: AiAnchoredEventGroup): EventDisplayGroup {
  return {
    groupKey: `activity-${group.anchorIndex}`,
    title: props.activityLabel,
    events: group.events,
  };
}

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
// Tool steps whose call waits for an approval card; agentic events are `loop-tool-<call id>`,
// local Agent events use the call id itself.
const awaitingApprovalEventIds = computed(() =>
  (props.approvals ?? []).flatMap((approval) => {
    const callId = approval.request.toolCallId;
    return callId ? [`loop-tool-${callId}`, callId] : [];
  }),
);
const activeApprovals = computed(() => (props.approvals ?? []).filter((approval) =>
  !approval.triggerMessageId || !messageIndexes.value.has(approval.triggerMessageId),
));
function decideApproval(id: string, decision: ToolApprovalDecision) {
  emit("decide-approval", id, decision);
}
function shouldRenderMessage(message: AiAssistantMessage, index: number) {
  if (message.content?.trim() || message.attachments?.length) return true;
  return !message.eventAnchor && !anchoredEvents.value.get(index)?.length;
}

const streamingAssistantMessageIndex = computed(() => {
  // `running` turns false at the first output, which is exactly when the reply starts streaming.
  if (!props.generating) return -1;
  for (let index = props.messages.length - 1; index >= 0; index -= 1) {
    const message = props.messages[index];
    // Only the current turn streams; never re-mark the previous turn's answer.
    if (message?.role === "user") return -1;
    if (message?.role === "assistant" && message.content.trim()) return index;
  }
  return -1;
});

function isStreamingAssistantMessage(message: AiAssistantMessage, index: number) {
  return message.role === "assistant" && streamingAssistantMessageIndex.value === index;
}

// The window's own start follows new messages (see message-render-window); what is rendered begins
// at the user message opening that turn, so "load earlier" never shows half a turn.
// The thread is keyed by conversation, so switching conversations mounts it afresh: paint only the
// newest messages. The drawer calls fillRenderWindow when the reader scrolls up towards the top;
// the fill reports itself like "load earlier", so the reading position holds.
const windowStart = ref(firstPaintAiMessageRenderStart(props.messages.length));
const visibleMessageStart = computed(() => alignAiMessageRenderStartToTurn(props.messages, windowStart.value));
const visibleMessages = computed(() =>
  props.messages.slice(visibleMessageStart.value).map((message, offset) => ({
    message,
    index: visibleMessageStart.value + offset,
  })),
);
const hiddenMessageCount = computed(() => visibleMessageStart.value);
function loadEarlierMessages() {
  const previousStart = visibleMessageStart.value;
  windowStart.value = earlierAiMessageRenderStart(Math.min(windowStart.value, previousStart));
  emit("load-earlier", previousStart - visibleMessageStart.value);
}
function revealMessage(messageId: string) {
  const index = props.messages.findIndex((message) => message.id === messageId);
  if (index < 0) return false;
  windowStart.value = Math.min(windowStart.value, index);
  return true;
}
watch(
  () => props.messages.length,
  (length, previousLength) => {
    windowStart.value = nextAiMessageRenderStart(
      windowStart.value,
      previousLength,
      length,
    );
  },
);
/** Widens a first-paint window to the regular one; true when it rendered more messages. */
function fillRenderWindow() {
  const next = filledAiMessageRenderStart(windowStart.value, props.messages.length);
  if (next >= windowStart.value) return false;
  const previousStart = visibleMessageStart.value;
  windowStart.value = next;
  const added = previousStart - visibleMessageStart.value;
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

const labels = {
  get thought() { return props.thoughtLabel; },
  get tool() { return props.toolLabel; },
  get plan() { return props.planLabel; },
  get mode() { return props.modeLabel; },
  get activity() { return props.activityLabels; },
  get eventCounts() { return props.eventGroupCountLabels; },
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
    <div v-if="!messages.length && !events.length" class="ai-assistant-welcome">
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
      v-if="hiddenMessageCount"
      class="ai-message-thread__load-earlier"
      text
      @click="loadEarlierMessages"
    >
      {{ loadEarlierLabel }}
    </el-button>
    <template
      v-for="{ message, index } in visibleMessages"
      :key="message.id ?? String(index)"
    >
      <AiTaskSummaryCard
        v-if="message.role === 'assistant' && message.presentation === 'task-summary'"
        :summary="message.content"
        :title="taskSummaryTitle"
        :status-label="taskSummaryStatus"
        :created-at="message.createdAt"
        :token-usage="message.tokenUsage"
                          :copy-label="copyMessageLabel"
        :copied-label="copiedMessageLabel"
        :copy-failed-label="copyMessageFailedLabel"
      />
      <AiErrorCard
        v-else-if="message.role === 'assistant' && message.presentation === 'error' && errorLabels"
        :title="errorLabels.title"
        :detail="message.content"
        :hint="errorLabels.hint"
        :retry-label="errorLabels.retry"
        :settings-label="errorLabels.settings"
        :can-retry="Boolean(canRewind) && isLastMessage(index)"
        @retry="emit('retry', message)"
        @open-settings="emit('open-settings')"
      />
      <AiMessageBubble
        v-else-if="shouldRenderMessage(message, index)"
        :message="message"
        :streaming="isStreamingAssistantMessage(message, index)"
        :pinned="index === pinnedUserMessageIndex"
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
        :editable="message.role === 'user' && (canRewind || (failed && index === pinnedUserMessageIndex))"
        :edit-label="canRewind ? editMessageLabel : editFailedLabel"
        :regenerable="Boolean(canRewind) && message.role === 'assistant' && isLastMessage(index)"
        :regenerate-label="regenerateLabel"
        @edit="emit('edit-failed', $event)"
        @regenerate="emit('regenerate', $event)"
      />
      <div
        v-if="continueLabel && message.role === 'assistant' && message.presentation === 'loop-limit' && isLastMessage(index)"
        class="ai-message-thread__continue"
      >
        <el-button size="small" type="primary" :icon="VideoPlay" :disabled="!canContinue" @click="emit('continue')">
          {{ continueLabel }}
        </el-button>
      </div>
      <section v-if="timelineEventGroupsByAnchor.get(index)" class="ai-agent-events" :aria-label="activityLabel">
        <AiAgentEventGroup
          v-bind="activityGroup(timelineEventGroupsByAnchor.get(index)!)"
          :awaiting-approval-ids="awaitingApprovalEventIds"
          :labels="labels"
          @dismiss="emit('dismiss-event', $event)"
        />
      </section>
      <template v-if="approvalLabels">
        <AiInlineToolApprovalCard
          v-for="approval in anchoredApprovals.get(index) ?? []"
          :key="approval.id"
          :approval="approval"
          :labels="approvalLabels"
          @decide="decideApproval"
        />
      </template>
    </template>
    <section v-if="activeEvents.length" class="ai-agent-events" :aria-label="activityLabel">
      <AiAgentEventGroup
        v-bind="activityGroup({ anchorIndex: -1, anchorIndexes: [-1], events: activeEvents })"
        :labels="labels"
        @dismiss="emit('dismiss-event', $event)"
      />
    </section>
    <template v-if="approvalLabels">
      <AiInlineToolApprovalCard
        v-for="approval in activeApprovals"
        :key="approval.id"
        :approval="approval"
        :labels="approvalLabels"
        @decide="decideApproval"
      />
    </template>
    <el-card
      v-if="running"
      class="ai-thinking-card"
      shadow="never"
    >
      <div class="ai-thinking-card__content">
        <el-icon class="is-loading" aria-hidden="true"><Loading /></el-icon>
        <span>{{ runningLabel }}</span>
      </div>
    </el-card>
  </div>
</template>
