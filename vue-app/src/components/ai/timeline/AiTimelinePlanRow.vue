<script setup lang="ts">
import { ArrowDown, ArrowRight, Close, Position } from "@element-plus/icons-vue";
import { computed } from "vue";
import { aiAgentEventView } from "@/domain/ai/agent-event-view";
import { aiTimelineEventKey } from "@/domain/ai/timeline-items";
import { useVueI18n } from "@/i18n/messages";
import type { AiAssistantEvent } from "@/stores/ai-assistant";
import AiTimelineEventContents from "./AiTimelineEventContents.vue";
import { useAiTimelineExpansion, type AiTimelineLabels } from "./timeline-row-state";

const props = defineProps<{ event: AiAssistantEvent; labels: AiTimelineLabels }>();
const emit = defineEmits<{ dismiss: [eventId: string] }>();
const { t } = useVueI18n();
const { expanded, toggle } = useAiTimelineExpansion(() => `plan:${aiTimelineEventKey(props.event)}`, () => true);
const title = computed(() => {
  const { steps } = aiAgentEventView(props.event).presentation;
  return t("ai.timeline.planUpdated", { complete: steps.filter(({ status }) => status === "completed").length, total: steps.length });
});
</script>

<template>
  <section class="ai-timeline-plan">
    <div class="ai-timeline__action-row">
      <button type="button" class="ai-timeline__row" :aria-expanded="expanded" @click="toggle">
        <Position class="ai-timeline__icon" aria-hidden="true" />
        <strong class="ai-timeline__title">{{ title }}</strong>
        <component :is="expanded ? ArrowDown : ArrowRight" class="ai-timeline__icon" aria-hidden="true" />
      </button>
      <button
        type="button" class="ai-timeline__icon-button" :aria-label="labels.dismissTaskSteps"
        :title="labels.dismissTaskSteps" @click="emit('dismiss', event.id)"
      >
        <Close aria-hidden="true" />
      </button>
    </div>
    <AiTimelineEventContents v-if="expanded" :event="event" :labels="labels" />
  </section>
</template>
