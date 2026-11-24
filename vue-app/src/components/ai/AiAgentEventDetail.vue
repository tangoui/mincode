<script setup lang="ts">
import { ArrowDown, ArrowUp } from "@element-plus/icons-vue";
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId } from "vue";
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
  diff?: boolean;
}>();

const presentation = computed<AiAgentEventDetailPresentation>(() =>
  props.diff
    ? { kind: "code", language: "diff", content: props.content }
    : presentAiAgentEventDetail(props.event, props.content),
);

const { t } = useVueI18n();
const PREVIEW_LINES = 12;
const viewportId = useId();
const viewport = ref<HTMLElement>();
const body = ref<HTMLElement>();
const expanded = ref(false);
const hasOverflow = ref(false);
const collapsed = computed(() => hasOverflow.value && !expanded.value);
const showsTail = computed(() => props.failed || presentation.value.kind === "terminal");
let observer: ResizeObserver | undefined;

onMounted(() => {
  if (!body.value || typeof ResizeObserver === "undefined") return;
  // The body stays full-height so clipping and expanding never change what we measure.
  observer = new ResizeObserver(([entry]) => {
    if (!entry || !viewport.value) return;
    const lineHeight = Number.parseFloat(getComputedStyle(viewport.value).lineHeight);
    hasOverflow.value = entry.contentRect.height > lineHeight * PREVIEW_LINES + 0.5;
  });
  observer.observe(body.value);
});
onBeforeUnmount(() => observer?.disconnect());

async function revealFocusedContent(event: FocusEvent) {
  const target = event.target;
  if (!collapsed.value || !viewport.value || !(target instanceof HTMLElement)) return;
  const { top, bottom } = target.getBoundingClientRect();
  const clip = viewport.value.getBoundingClientRect();
  if (top >= clip.top && bottom <= clip.bottom) return;
  expanded.value = true;
  await nextTick();
  target.scrollIntoView({ block: "nearest" });
}
</script>

<template>
  <div
    class="ai-agent-event-detail is-inline"
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
    <div
      :id="viewportId" ref="viewport" class="ai-agent-event-detail__viewport"
      :class="{ 'is-collapsed': collapsed, 'is-tail': showsTail }"
      :style="collapsed ? { maxBlockSize: `${PREVIEW_LINES}lh` } : undefined"
      @focusin="revealFocusedContent"
    >
      <div ref="body" class="ai-agent-event-detail__body">
        <MarkdownPreview v-if="presentation.kind === 'markdown'" inline :content="presentation.content" />
        <pre v-else><code :class="`language-${presentation.language}`">{{ presentation.content }}</code></pre>
      </div>
    </div>
    <button
      v-if="hasOverflow" type="button" class="ai-timeline__output-toggle"
      :aria-controls="viewportId" :aria-expanded="expanded"
      @click="expanded = !expanded"
    >
      <span>{{ expanded ? t('ai.timeline.collapseOutput') : t('ai.timeline.showFullOutput') }}</span>
      <component :is="expanded ? ArrowUp : ArrowDown" aria-hidden="true" />
    </button>
  </div>
</template>
