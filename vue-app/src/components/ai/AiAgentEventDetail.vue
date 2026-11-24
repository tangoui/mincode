<script setup lang="ts">
import { computed, ref } from "vue";
import { ElScrollbar } from "element-plus";
import MarkdownPreview from "@/components/ui/MarkdownPreview.vue";
import {
  presentAiAgentEventDetail,
  type AiAgentEventDetailPresentation,
} from "@/domain/ai/agent-event-detail";
import { useVueI18n } from "@/i18n/messages";
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

const { t } = useVueI18n();
const expanded = ref(false);
const previewLines = computed(() => props.inline && (props.failed || presentation.value.kind !== "markdown")
  ? presentation.value.content.split(/\r?\n/)
  : []);
const hasTailPreview = computed(() => previewLines.value.length > 12);
const showingTail = computed(() => hasTailPreview.value && !expanded.value);
const visibleContent = computed(() => showingTail.value
  ? previewLines.value.slice(-12).join("\n")
  : presentation.value.content);
</script>

<template>
  <div
    class="ai-agent-event-detail"
    :class="[`is-${presentation.kind}`, { 'is-failed': failed, 'is-inline': inline }]"
    :data-kind="presentation.kind"
    :data-language="presentation.language"
  >
    <button
      v-if="hasTailPreview" type="button" class="ai-timeline__output-toggle" :aria-expanded="expanded"
      @click="expanded = !expanded"
    >
      {{ expanded ? t('ai.timeline.showOutputTail') : t('ai.timeline.showFullOutput', { count: previewLines.length }) }}
    </button>
    <span
      v-if="presentation.kind !== 'markdown'"
      class="ai-agent-event-detail__language"
      aria-hidden="true"
    >
      {{ presentation.language }}
    </span>
    <MarkdownPreview
      v-if="presentation.kind === 'markdown' && !showingTail"
      inline
      :content="presentation.content"
    />
    <pre v-else-if="inline"><code :class="`language-${presentation.language}`">{{ visibleContent }}</code></pre>
    <ElScrollbar v-else class="ai-agent-event-detail__scroll" max-height="220px">
      <pre><code :class="`language-${presentation.language}`">{{ presentation.content }}</code></pre>
    </ElScrollbar>
  </div>
</template>
