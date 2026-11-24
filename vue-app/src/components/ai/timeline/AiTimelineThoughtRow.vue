<script setup lang="ts">
import { ArrowDown, ArrowRight, SetUp } from "@element-plus/icons-vue";
import { computed } from "vue";
import { aiAgentEventDurationMs, formatAiAgentEventDuration } from "@/domain/ai/agent-event-timing";
import { aiTimelineEventKey } from "@/domain/ai/timeline-items";
import { useVueI18n } from "@/i18n/messages";
import type { AiAssistantEvent } from "@/stores/ai-assistant";
import AiTimelineEventContents from "./AiTimelineEventContents.vue";
import { useAiTimelineExpansion, type AiTimelineLabels } from "./timeline-row-state";

const props = defineProps<{ event: AiAssistantEvent; labels: AiTimelineLabels }>();
const { t } = useVueI18n();
const { expanded, toggle } = useAiTimelineExpansion(() => `thought:${aiTimelineEventKey(props.event)}`, () => false);
const title = computed(() => {
  const duration = formatAiAgentEventDuration(aiAgentEventDurationMs(props.event));
  return duration ? t("ai.timeline.thoughtFor", { duration }) : props.labels.activity.analysis;
});
</script>

<template>
  <section class="ai-timeline-thought">
    <button type="button" class="ai-timeline__row" :aria-expanded="expanded" @click="toggle">
      <SetUp class="ai-timeline__icon" aria-hidden="true" />
      <strong class="ai-timeline__title">{{ title }}</strong>
      <component :is="expanded ? ArrowDown : ArrowRight" class="ai-timeline__icon" aria-hidden="true" />
    </button>
    <AiTimelineEventContents v-if="expanded" :event="event" :labels="labels" />
  </section>
</template>
