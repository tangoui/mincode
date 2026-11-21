<script setup lang="ts">
import { computed, ref } from "vue";
import { ElButton, ElCard, ElCollapseTransition, ElProgress, ElTooltip } from "element-plus";
import { ArrowDown, CircleCheck, CircleClose, Close, Loading } from "@element-plus/icons-vue";
import type { AiTaskStepPresentation, AiTaskStepStatus } from "@/domain/ai/event-presentations";

const props = withDefaults(defineProps<{
  label: string;
  steps: AiTaskStepPresentation[];
  statusLabels: Record<AiTaskStepStatus, string>;
  summary?: string;
  dismissible?: boolean;
  dismissLabel?: string;
  defaultExpanded?: boolean;
}>(), {
  summary: "",
  dismissible: false,
  dismissLabel: "",
  defaultExpanded: true,
});

const emit = defineEmits<{ dismiss: [] }>();
const expanded = ref(props.defaultExpanded);
const completedCount = computed(() => props.steps.filter((step) => step.status === "completed").length);
const progress = computed(() => `${completedCount.value}/${props.steps.length}`);
const percent = computed(() => (props.steps.length
  ? Math.round((completedCount.value / props.steps.length) * 100)
  : 0));
// Use colors rather than ElProgress status so the percentage text stays visible.
const progressColor = computed(() => {
  if (props.steps.some((step) => step.status === "failed" || step.status === "cancelled")) {
    return "var(--el-color-danger)";
  }
  return props.steps.length && completedCount.value === props.steps.length
    ? "var(--el-color-success)"
    : "var(--el-color-primary)";
});
</script>

<template>
  <ElCard class="ai-task-steps" shadow="never">
    <header class="ai-task-steps__header">
      <ElButton
        text
        class="ai-task-steps__toggle"
        :aria-expanded="expanded"
        :aria-label="`${label} ${progress} ${percent}%`"
        @click="expanded = !expanded"
      >
        <ArrowDown class="ai-task-steps__chevron" :class="{ 'is-collapsed': !expanded }" aria-hidden="true" />
        <strong>{{ label }}<span>({{ progress }})</span></strong>
      </ElButton>
      <ElProgress
        v-if="steps.length"
        class="ai-task-steps__progress"
        :percentage="percent"
        :color="progressColor"
        :stroke-width="4"
        :format="(value: number) => `${value}%`"
      />
      <ElTooltip v-if="dismissible" :content="dismissLabel" placement="top">
        <ElButton
          text
          size="small"
          class="ai-task-steps__dismiss"
          :icon="Close"
          :aria-label="dismissLabel"
          @click="emit('dismiss')"
        />
      </ElTooltip>
    </header>

    <ElCollapseTransition>
      <div v-show="expanded" class="ai-task-steps__content">
        <p v-if="summary" class="ai-task-steps__summary">{{ summary }}</p>
        <ol>
          <li v-for="(step, index) in steps" :key="`${index}-${step.title}`" :data-task-status="step.status">
            <CircleCheck v-if="step.status === 'completed'" aria-hidden="true" />
            <CircleClose v-else-if="step.status === 'failed' || step.status === 'cancelled'" aria-hidden="true" />
            <Loading v-else-if="step.status === 'in_progress'" class="is-spinning" aria-hidden="true" />
            <i v-else aria-hidden="true" />
            <span>
              <span class="sr-only">{{ statusLabels[step.status] }}</span>
              <b>{{ step.title }}</b>
              <small v-if="step.detail">{{ step.detail }}</small>
            </span>
          </li>
        </ol>
      </div>
    </ElCollapseTransition>
  </ElCard>
</template>
