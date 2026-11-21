<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import {
  aiAgentEventDurationMs,
  aiAgentEventGroupDurationMs,
  formatAiAgentEventDuration,
  isAiAgentEventRunning,
} from "@/domain/ai/agent-event-timing";
import type { AiAssistantEvent } from "@/stores/ai-assistant";

// Owns the per-second clock so running events only re-render this label,
// not the whole event group (whose rows parse event details).
const props = defineProps<{
  events: AiAssistantEvent[];
  /** Whole-group span instead of the single event's own duration. */
  aggregate?: boolean;
  /** Text template containing `{duration}`; omitted means the bare duration. */
  template?: string;
  /** Title template containing `{duration}`. */
  titleTemplate?: string;
  /** Shown when there is no duration; otherwise nothing renders. */
  fallback?: string;
}>();

const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | undefined;

const running = computed(() => props.events.some(isAiAgentEventRunning));
const duration = computed(() => formatAiAgentEventDuration(props.aggregate
  ? aiAgentEventGroupDurationMs(props.events, now.value)
  : props.events[0] ? aiAgentEventDurationMs(props.events[0], now.value) : undefined));
const text = computed(() => {
  if (!duration.value) return props.fallback ?? "";
  return props.template ? props.template.replace("{duration}", duration.value) : duration.value;
});
const title = computed(() => duration.value && props.titleTemplate
  ? props.titleTemplate.replace("{duration}", duration.value)
  : undefined);

watch(running, (isRunning) => {
  if (timer) clearInterval(timer);
  timer = undefined;
  now.value = Date.now();
  if (isRunning) {
    timer = setInterval(() => {
      now.value = Date.now();
    }, 1_000);
  }
}, { immediate: true });

onBeforeUnmount(() => {
  if (timer) clearInterval(timer);
});
</script>

<template>
  <small v-if="text" :title="title">{{ text }}</small>
</template>
