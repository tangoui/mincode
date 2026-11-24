<script setup lang="ts">
import { computed } from "vue";
import { ElScrollbar } from "element-plus";
import MarkdownPreview from "@/components/ui/MarkdownPreview.vue";
import {
  presentAiAgentEventDetail,
  type AiAgentEventDetailPresentation,
} from "@/domain/ai/agent-event-detail";
import type { AiAssistantEvent } from "@/stores/ai-assistant";

const props = defineProps<{
  event: AiAssistantEvent;
  content: string;
  failed?: boolean;
  inline?: boolean;
  diff?: boolean;
}>();

const presentation = computed<AiAgentEventDetailPresentation>(() =>
  props.diff
    ? { kind: "code", language: "diff", content: props.content }
    : presentAiAgentEventDetail(props.event, props.content),
);
</script>

<template>
  <div
    class="ai-agent-event-detail"
    :class="[`is-${presentation.kind}`, { 'is-failed': failed, 'is-inline': inline }]"
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
    <pre v-else-if="inline"><code :class="`language-${presentation.language}`">{{ presentation.content }}</code></pre>
    <ElScrollbar v-else class="ai-agent-event-detail__scroll" max-height="220px">
      <pre><code :class="`language-${presentation.language}`">{{ presentation.content }}</code></pre>
    </ElScrollbar>
  </div>
</template>
