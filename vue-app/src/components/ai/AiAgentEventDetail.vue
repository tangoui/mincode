<script setup lang="ts">
import { computed } from "vue";
import { ElScrollbar } from "element-plus";
import MarkdownPreview from "@/components/projects/MarkdownPreview.vue";
import {
  presentAiAgentEventDetail,
  type AiAgentEventDetailPresentation,
} from "@/domain/ai/agent-event-detail";
import type { AiAssistantEvent } from "@/stores/ai-assistant";

const props = defineProps<{
  event: AiAssistantEvent;
  content: string;
  failed?: boolean;
}>();

const presentation = computed<AiAgentEventDetailPresentation>(() =>
  presentAiAgentEventDetail(props.event, props.content),
);
</script>

<template>
  <div
    class="ai-agent-event-detail"
    :class="[`is-${presentation.kind}`, { 'is-failed': failed }]"
    :data-kind="presentation.kind"
    :data-language="presentation.language"
  >
    <span
      v-if="presentation.kind !== 'markdown'"
      class="ai-agent-event-detail__language"
      aria-hidden="true"
    >
      {{ presentation.language }}
    </span>
    <MarkdownPreview
      v-if="presentation.kind === 'markdown'"
      inline
      :content="presentation.content"
    />
    <ElScrollbar v-else class="ai-agent-event-detail__scroll" max-height="220px">
      <pre><code :class="`language-${presentation.language}`">{{ presentation.content }}</code></pre>
    </ElScrollbar>
  </div>
</template>
