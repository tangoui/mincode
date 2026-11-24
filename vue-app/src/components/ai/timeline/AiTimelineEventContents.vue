<script setup lang="ts">
import { Check, CopyDocument } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { computed, onBeforeUnmount, ref } from "vue";
import AiAgentEventDetail from "@/components/ai/AiAgentEventDetail.vue";
import AiArtifactCard from "@/components/ai/AiArtifactCard.vue";
import AiSourcesList from "@/components/ai/AiSourcesList.vue";
import AiTaskSteps from "@/components/ai/AiTaskSteps.vue";
import { aiAgentEventView } from "@/domain/ai/agent-event-view";
import { formatAiAgentEventDetail } from "@/domain/ai/agent-event-activity";
import { copyText } from "@/services/desktop-bridge";
import type { AiAssistantEvent } from "@/stores/ai-assistant";
import type { AiTimelineLabels } from "./timeline-row-state";

const props = defineProps<{ event: AiAssistantEvent; labels: AiTimelineLabels; failed?: boolean; diff?: boolean }>();
const view = computed(() => aiAgentEventView(props.event));
const presentation = computed(() => view.value.presentation);
const parameters = computed(() => formatAiAgentEventDetail(props.event.toolInput ?? ""));
const copied = ref(false);
let resetTimer: ReturnType<typeof setTimeout> | undefined;

async function copyResult() {
  try {
    await copyText(view.value.formattedDetail);
    copied.value = true;
    clearTimeout(resetTimer);
    resetTimer = setTimeout(() => { copied.value = false; }, 2000);
    ElMessage.success(props.labels.copiedResult);
  } catch (error) {
    ElMessage.error(props.labels.copyResultFailed.replace("{message}", String(error)));
  }
}
onBeforeUnmount(() => clearTimeout(resetTimer));
</script>

<template>
  <div class="ai-timeline__contents">
    <div v-if="event.toolName" class="ai-timeline__metadata">
      {{ labels.activity.toolName }} <code>{{ event.toolName }}</code>
    </div>
    <details v-if="parameters" class="ai-timeline__parameters">
      <summary>{{ labels.parameters }}</summary>
      <pre>{{ parameters }}</pre>
    </details>
    <div v-if="presentation.text" class="ai-timeline__result">
      <AiAgentEventDetail
        :event="event" :content="presentation.text" :failed="failed" :diff="diff"
        :role="failed ? 'alert' : undefined"
      />
      <button
        type="button" class="ai-timeline__icon-button"
        :aria-label="copied ? labels.copiedResult : labels.copyResult"
        :title="copied ? labels.copiedResult : labels.copyResult" @click="copyResult"
      >
        <Check v-if="copied" aria-hidden="true" />
        <CopyDocument v-else aria-hidden="true" />
      </button>
    </div>
    <AiTaskSteps
      v-if="presentation.steps.length" inline :label="labels.taskSteps" :steps="presentation.steps"
      :status-labels="labels.taskStepStatuses"
    />
    <AiSourcesList v-if="presentation.sources.length" :label="labels.sources" :sources="presentation.sources" />
    <section v-if="presentation.artifacts.length" class="ai-artifacts" :aria-label="labels.artifacts">
      <strong class="ai-presentation-heading">{{ labels.artifacts }}</strong>
      <AiArtifactCard
        v-for="(artifact, index) in presentation.artifacts" :key="`${index}-${artifact.title}`"
        :artifact="artifact" :labels="labels"
      />
    </section>
  </div>
</template>
