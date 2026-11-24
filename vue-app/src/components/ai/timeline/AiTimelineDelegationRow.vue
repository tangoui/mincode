<script setup lang="ts">
import { ArrowDown, ArrowRight, Share, TopRight } from "@element-plus/icons-vue";
import { ElScrollbar } from "element-plus";
import { computed, ref } from "vue";
import MarkdownPreview from "@/components/ui/MarkdownPreview.vue";
import {
  aiTimelineDelegationStatus,
  isAiTimelineDelegationSettled,
  type AiTimelineItem,
} from "@/domain/ai/timeline-items";
import { useVueI18n } from "@/i18n/messages";
import type { AiConversationStatus } from "@/stores/ai-assistant";
import { useAiTimelineExpansion } from "./timeline-row-state";

const props = defineProps<{
  group: Extract<AiTimelineItem, { kind: "delegation" }>;
  modelLabels?: Record<string, string>;
  statusLabels?: Record<string, string>;
}>();
const emit = defineEmits<{ open: [id: string] }>();
const { t } = useVueI18n();
const settled = computed(() => props.group.children.filter(isAiTimelineDelegationSettled).length);
const failed = computed(() => props.group.children.filter(child => aiTimelineDelegationStatus(child) === "failed").length);
const running = computed(() => settled.value < props.group.children.length);
const { expanded, setExpanded, toggle } = useAiTimelineExpansion(() => props.group.key, () => false);
const openReportId = ref<string>();

const title = computed(() => {
  if (running.value) return t("ai.timeline.delegationReports");
  const count = props.group.children.length;
  if (failed.value) return t("ai.timeline.delegationsFailed", { count, failed: failed.value });
  return t("ai.timeline.delegationsCompleted", { count });
});

function toggleReport(id: string) {
  openReportId.value = openReportId.value === id ? undefined : id;
  if (openReportId.value) setExpanded(true);
}

function statusTag(status: AiConversationStatus) {
  if (status === "completed") return "success";
  if (status === "failed") return "danger";
  if (status === "interrupted" || status === "needs_input") return "warning";
  return status === "in_progress" ? "primary" : "info";
}
</script>

<template>
  <section class="ai-timeline-delegation" :aria-label="t('ai.timeline.delegationReports')">
    <div v-if="running" class="ai-timeline__row">
      <Share class="ai-timeline__icon" aria-hidden="true" />
      <span class="ai-timeline__title">{{ title }}</span>
      <span class="ai-timeline__meta">{{ t('ai.timeline.delegationProgress', { settled, total: group.children.length }) }}</span>
    </div>
    <button v-else type="button" class="ai-timeline__row" :aria-expanded="expanded" @click="toggle">
      <Share class="ai-timeline__icon" aria-hidden="true" />
      <span class="ai-timeline__title">{{ title }}</span>
      <component :is="expanded ? ArrowDown : ArrowRight" class="ai-timeline__icon" aria-hidden="true" />
    </button>
    <div v-if="running || expanded" class="ai-timeline__contents">
      <div v-for="child in group.children" :key="child.id" class="ai-delegation-reports__row">
        <details class="ai-delegation-reports__item" :open="openReportId === child.id">
          <summary @click.prevent="toggleReport(child.id)">
            <strong :title="child.title">{{ child.title }}</strong>
            <span v-if="child.selectedModel" class="ai-delegation-reports__model" :title="modelLabels?.[child.selectedModel] ?? child.selectedModel">
              {{ modelLabels?.[child.selectedModel] ?? child.selectedModel }}
            </span>
            <span class="ai-delegation-reports__status" :class="`is-${statusTag(aiTimelineDelegationStatus(child))}`">
              {{ statusLabels?.[aiTimelineDelegationStatus(child)] }}
            </span>
          </summary>
          <div v-if="openReportId === child.id" class="ai-delegation-reports__result">
            <ElScrollbar v-if="child.delegation?.result" max-height="260px">
              <MarkdownPreview inline code-copy :content="child.delegation.result" />
            </ElScrollbar>
            <span v-else class="ai-delegation-reports__empty">
              {{ statusLabels?.[aiTimelineDelegationStatus(child)] }}
            </span>
          </div>
        </details>
        <button type="button" class="ai-timeline__icon-button" :title="t('ai.timeline.openDelegation')"
          :aria-label="t('ai.timeline.openDelegation')" @click="emit('open', child.id)">
          <TopRight aria-hidden="true" />
        </button>
      </div>
    </div>
  </section>
</template>
