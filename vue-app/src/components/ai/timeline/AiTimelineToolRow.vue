<script setup lang="ts">
import "@/styles/workspaces/ai/timeline.css";
import { ArrowDown, ArrowRight, CircleCheck, CircleClose, Loading, Warning } from "@element-plus/icons-vue";
import { computed, ref } from "vue";
import AiAgentEventElapsed from "@/components/ai/AiAgentEventElapsed.vue";
import { useKeepReadingPosition } from "@/composables/use-keep-reading-position";
import { aiAgentEventView } from "@/domain/ai/agent-event-view";
import { isAiAgentEventRunning } from "@/domain/ai/agent-event-timing";
import { aiTimelineEventKey } from "@/domain/ai/timeline-items";
import { useVueI18n } from "@/i18n/messages";
import type { AiAssistantEvent } from "@/stores/ai-assistant";
import AiTimelineEventContents from "./AiTimelineEventContents.vue";
import { useAiTimelineExpansion, type AiTimelineLabels } from "./timeline-row-state";

const props = defineProps<{
  event: AiAssistantEvent;
  awaitingApproval: boolean;
  conversationRunning: boolean;
  labels: AiTimelineLabels;
}>();
defineEmits<{ dismiss: [eventId: string] }>();

const { t } = useVueI18n();
const root = ref<HTMLElement>();
useKeepReadingPosition(root);
const view = computed(() => aiAgentEventView(props.event));
const activity = computed(() => view.value.activity);
const status = computed(() => props.event.status?.trim().toLowerCase() || "pending");
const failed = computed(() => ["failed", "error", "cancelled"].includes(status.value));
const unfinished = computed(() => isAiAgentEventRunning(props.event));
const running = computed(() => !props.awaitingApproval && props.conversationRunning && unfinished.value);
const interrupted = computed(() => !props.awaitingApproval && !props.conversationRunning && unfinished.value);
const statusLabel = computed(() => {
  if (props.awaitingApproval) return props.labels.eventStatuses.awaiting_approval;
  if (interrupted.value) return t("ai.timeline.interrupted");
  return props.labels.eventStatuses[status.value];
});
const icon = computed(() => {
  if (props.awaitingApproval || interrupted.value) return Warning;
  if (running.value) return Loading;
  return failed.value ? CircleClose : CircleCheck;
});
const expandable = computed(() => view.value.hasPresentationDetail || Boolean(props.event.toolInput)
  || running.value || failed.value);
const { expanded, toggle } = useAiTimelineExpansion(
  () => `tool:${aiTimelineEventKey(props.event)}`,
  () => failed.value || (running.value && view.value.hasPresentationDetail),
);
const elapsedEvents = computed(() => [props.event]);
const diff = computed(() => {
  if (activity.value.kind !== "writeFile" && activity.value.kind !== "createFile") return undefined;
  const lines = view.value.presentation.text.split(/\r?\n/);
  let inHunk = false;
  let added = 0;
  let removed = 0;
  for (const line of lines) {
    if (/^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@/.test(line)) inHunk = true;
    else if (line.startsWith("diff --git ")) inHunk = false;
    else if (inHunk && line.startsWith("+") && !line.startsWith("+++ ")) added += 1;
    else if (inHunk && line.startsWith("-") && !line.startsWith("--- ")) removed += 1;
  }
  return added || removed ? { added, removed } : undefined;
});
</script>

<template>
  <article ref="root" class="ai-timeline-tool" :class="{ 'is-failed': failed, 'is-attention': awaitingApproval }">
    <component
      :is="expandable ? 'button' : 'div'" class="ai-timeline__row"
      :type="expandable ? 'button' : undefined" :aria-expanded="expandable ? expanded : undefined"
      :title="expandable ? expanded ? labels.activity.hideDetail : labels.activity.detail : undefined"
      @click="expandable && toggle()"
    >
      <component :is="icon" class="ai-timeline__icon" :class="{ 'is-spinning': running }" aria-hidden="true" />
      <span class="ai-timeline__title">
        <strong>{{ labels.activity[activity.kind] }}</strong>
        <code v-if="activity.target" :title="activity.target">{{ activity.target }}</code>
      </span>
      <span class="ai-timeline__meta">
        <span v-if="diff" class="ai-timeline__diff">
          <span>+{{ diff.added }}</span><span>−{{ diff.removed }}</span>
        </span>
        <AiAgentEventElapsed
          v-if="!interrupted && !awaitingApproval && event.startedAt" :events="elapsedEvents"
          :title-template="labels.activity.duration"
        />
        <span v-if="awaitingApproval || interrupted || failed || running" role="status">{{ statusLabel }}</span>
        <component
          :is="expanded ? ArrowDown : ArrowRight" v-if="expandable" class="ai-timeline__icon" aria-hidden="true"
        />
      </span>
    </component>
    <AiTimelineEventContents v-if="expandable && expanded" :event="event" :labels="labels" :failed="failed" :diff="Boolean(diff)" />
  </article>
</template>
