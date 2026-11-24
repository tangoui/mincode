<script setup lang="ts">
import { ArrowDown, ArrowRight, CircleCheck, Loading } from "@element-plus/icons-vue";
import { computed } from "vue";
import { aiAgentEventView } from "@/domain/ai/agent-event-view";
import { aiTimelineEventKey } from "@/domain/ai/timeline-items";
import { useVueI18n } from "@/i18n/messages";
import type { AiAssistantEvent } from "@/stores/ai-assistant";
import AiTimelineToolRow from "./AiTimelineToolRow.vue";
import { useAiTimelineExpansion, type AiTimelineLabels } from "./timeline-row-state";

const props = defineProps<{
  events: AiAssistantEvent[];
  running: boolean;
  conversationRunning: boolean;
  labels: AiTimelineLabels;
}>();
const { t } = useVueI18n();
const { expanded, toggle } = useAiTimelineExpansion(
  () => `toolSummary:${props.events[0] ? aiTimelineEventKey(props.events[0]) : ""}`,
  () => false,
);
const title = computed(() => {
  if (props.running) return t("ai.timeline.exploring");
  const files = props.events.filter((event) => {
    const { kind } = aiAgentEventView(event).activity;
    return kind === "readFile" || kind === "openFile";
  }).length;
  const searches = props.events.length - files;
  if (!files) return t(searches === 1 ? "ai.timeline.exploredSearchOne" : "ai.timeline.exploredSearches", { searches });
  if (!searches) return t(files === 1 ? "ai.timeline.exploredFileOne" : "ai.timeline.exploredFiles", { files });
  return t("ai.timeline.explored", {
    files: t(files === 1 ? "ai.timeline.exploredFileOne" : "ai.timeline.exploredFiles", { files }),
    searches: t(searches === 1 ? "ai.timeline.exploredSearchOne" : "ai.timeline.exploredSearches", { searches }),
  });
});
</script>

<template>
  <section class="ai-timeline-summary">
    <button type="button" class="ai-timeline__row" :aria-expanded="expanded" @click="toggle">
      <component :is="running ? Loading : CircleCheck" class="ai-timeline__icon" :class="{ 'is-spinning': running }" aria-hidden="true" />
      <strong class="ai-timeline__title">{{ title }}</strong>
      <component :is="expanded ? ArrowDown : ArrowRight" class="ai-timeline__icon" aria-hidden="true" />
    </button>
    <div v-if="expanded" class="ai-timeline__contents">
      <AiTimelineToolRow
        v-for="event in events" :key="aiTimelineEventKey(event)" :event="event" :awaiting-approval="false"
        :conversation-running="conversationRunning" :labels="labels"
      />
    </div>
  </section>
</template>
