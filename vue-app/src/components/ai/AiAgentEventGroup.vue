<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElButton, ElCard, ElCollapse, ElCollapseItem, ElIcon, ElMessage, ElScrollbar, ElTag } from "element-plus";
import {
  ArrowDown,
  ArrowRight,
  CircleCheck,
  CircleClose,
  CopyDocument,
  Check,
  Loading,
  Operation,
  Position,
  SetUp,
  Tools,
  Warning,
} from "@element-plus/icons-vue";
import AiAgentEventDetail from "@/components/ai/AiAgentEventDetail.vue";
import AiAgentEventElapsed from "@/components/ai/AiAgentEventElapsed.vue";
import AiArtifactCard from "@/components/ai/AiArtifactCard.vue";
import AiSourcesList from "@/components/ai/AiSourcesList.vue";
import AiTaskSteps from "@/components/ai/AiTaskSteps.vue";
import { useKeepReadingPosition } from "@/composables/use-keep-reading-position";
import type { AiAgentActivityKind } from "@/domain/ai/agent-event-activity";
import {
  summarizeAiAgentEventStatuses,
  type AiAgentEventStatusSummary,
} from "@/domain/ai/agent-event-groups";
import type { AiTaskStepStatus } from "@/domain/ai/event-presentations";
import { aiAgentEventView } from "@/domain/ai/agent-event-view";
import type { AiAssistantEvent } from "@/stores/ai-assistant";
import { copyText } from "@/services/desktop-bridge";

const props = withDefaults(defineProps<{
  groupKey: string;
  title: string;
  events: AiAssistantEvent[];
  defaultExpanded?: boolean;
  /** Events whose tool call waits for the approval card shown below this group. */
  awaitingApprovalIds?: readonly string[];
  labels: {
    thought: string;
    tool: string;
    plan: string;
    mode: string;
    taskSteps: string;
    dismissTaskSteps: string;
    sources: string;
    artifacts: string;
    preview: string;
    collapse: string;
    copy: string;
    copied: string;
    copyResult: string;
    copiedResult: string;
    copyResultFailed: string;
    activity: Record<AiAgentActivityKind | "detail" | "hideDetail" | "toolName" | "result" | "runningDuration" | "totalDuration" | "duration", string>;
    eventCounts: Record<keyof AiAgentEventStatusSummary, string>;
    eventStatuses: Record<string, string>;
    taskStepStatuses: Record<AiTaskStepStatus, string>;
  };
}>(), {
  defaultExpanded: false,
});

const emit = defineEmits<{ dismiss: [event: AiAssistantEvent] }>();

const runningStatuses = new Set(["running", "pending", "in_progress"]);
const failedStatuses = new Set(["failed", "error", "cancelled"]);
const activeNames = ref<string[]>(props.defaultExpanded ? [props.groupKey] : []);
const expandedItemNames = ref<string[]>([]);
const copiedEventKey = ref("");
let copiedResetTimer: ReturnType<typeof setTimeout> | undefined;

watch(() => props.defaultExpanded, (shouldExpand) => {
  if (shouldExpand && !activeNames.value.includes(props.groupKey)) activeNames.value = [props.groupKey];
});
watch(() => props.groupKey, (groupKey) => {
  activeNames.value = props.defaultExpanded ? [groupKey] : [];
  expandedItemNames.value = [];
});

const groupExpanded = computed(() => activeNames.value.includes(props.groupKey));
const statusSummary = computed(() => summarizeAiAgentEventStatuses(props.events));
const countLabels = computed(() =>
  (Object.keys(statusSummary.value) as Array<keyof AiAgentEventStatusSummary>)
    .filter((status) => statusSummary.value[status] > 0)
    .map((status) => ({
      status,
      label: props.labels.eventCounts[status].replace(
        "{count}",
        String(statusSummary.value[status]),
      ),
    })),
);
const artifactLabels = computed(() => ({
  preview: props.labels.preview,
  collapse: props.labels.collapse,
  copy: props.labels.copy,
  copied: props.labels.copied,
}));
const groupStatus = computed(() => {
  if (statusSummary.value.running > 0) return "running";
  if (statusSummary.value.failed === 0) return "completed";
  if (statusSummary.value.succeeded === 0) return "failed";
  return "mixed";
});
const groupStatusLabel = computed(() =>
  props.labels.eventStatuses[groupStatus.value] ?? props.labels.eventStatuses.unknown,
);
const groupTimingTemplate = computed(() => (groupStatus.value === "running"
  ? props.labels.activity.runningDuration
  : props.labels.activity.totalDuration));
const groupIcon = computed(() => {
  if (groupStatus.value === "running") return Loading;
  if (groupStatus.value === "failed") return CircleClose;
  if (groupStatus.value === "mixed") return Warning;
  return CircleCheck;
});

watch(groupExpanded, (expanded) => {
  // Reopening the outer activity card always starts with a compact list.
  if (!expanded) expandedItemNames.value = [];
});

// A step line: open while its steps run so the reader sees each one, folded to its one-line summary
// once they all succeed. Failed steps keep it open. A group the reader toggled keeps that choice.
let readerToggled = false;
watch(groupStatus, (status, previous) => {
  if (readerToggled) return;
  if (status === "running") activeNames.value = [props.groupKey];
  else if (previous === "running" && status === "completed") activeNames.value = [];
}, { immediate: true });
function markReaderToggled() {
  readerToggled = true;
}
// Folding happens when the run ends, often while the reader is below this group.
const card = ref<InstanceType<typeof ElCard>>();
useKeepReadingPosition(computed(() => card.value?.$el as HTMLElement | undefined));

function statusOf(event: AiAssistantEvent) {
  return event.status ?? (event.kind === "tool" ? "pending" : "completed");
}
function isFailed(event: AiAssistantEvent) {
  return failedStatuses.has(statusOf(event));
}
function isRunning(event: AiAssistantEvent) {
  return runningStatuses.has(statusOf(event));
}
function isAwaitingApproval(event: AiAssistantEvent) {
  return isRunning(event) && Boolean(props.awaitingApprovalIds?.includes(event.id));
}
function statusLabel(event: AiAssistantEvent) {
  if (isAwaitingApproval(event)) return props.labels.eventStatuses.awaiting_approval ?? props.labels.eventStatuses.unknown;
  return props.labels.eventStatuses[statusOf(event)] ?? props.labels.eventStatuses.unknown;
}
function statusTagType(event: AiAssistantEvent) {
  if (isAwaitingApproval(event)) return "warning";
  return isFailed(event) ? "danger" : "primary";
}
function statusIcon(event: AiAssistantEvent) {
  if (isRunning(event)) return Loading;
  if (isFailed(event)) return CircleClose;
  if (statusOf(event) === "completed") return CircleCheck;
  if (event.kind === "thought") return SetUp;
  if (event.kind === "plan") return Position;
  if (event.kind === "mode") return Operation;
  return Tools;
}
function presentationFor(event: AiAssistantEvent) {
  return aiAgentEventView(event).presentation;
}
function eventKey(event: AiAssistantEvent) {
  return `${event.id}-${event.triggerMessageId ?? event.afterMessageIndex ?? "active"}`;
}
function isItemExpanded(event: AiAssistantEvent) {
  return expandedItemNames.value.includes(eventKey(event));
}
function toggleItem(event: AiAssistantEvent) {
  const key = eventKey(event);
  expandedItemNames.value = isItemExpanded(event)
    ? expandedItemNames.value.filter((name) => name !== key)
    : [...expandedItemNames.value, key];
}
function activityFor(event: AiAssistantEvent) {
  return aiAgentEventView(event).activity;
}
function activityTypeLabel(event: AiAssistantEvent) {
  return props.labels.activity[activityFor(event).kind];
}
function activityTarget(event: AiAssistantEvent) {
  return activityFor(event).target;
}
function activityTitle(event: AiAssistantEvent) {
  const type = activityTypeLabel(event);
  const target = activityTarget(event);
  return target ? `${type} · ${target}` : type;
}
function hasPresentationDetail(event: AiAssistantEvent) {
  return aiAgentEventView(event).hasPresentationDetail;
}
function canExpand(event: AiAssistantEvent) {
  return activityFor(event).expandable && (hasPresentationDetail(event) || isRunning(event) || isFailed(event));
}
function formattedDetail(event: AiAssistantEvent) {
  return aiAgentEventView(event).formattedDetail;
}
function detailToggleLabel(event: AiAssistantEvent) {
  return isItemExpanded(event) ? props.labels.activity.hideDetail : props.labels.activity.detail;
}
async function copyEventDetail(event: AiAssistantEvent) {
  const detail = formattedDetail(event);
  if (!detail) return;
  try {
    await copyText(detail);
    copiedEventKey.value = eventKey(event);
    if (copiedResetTimer) clearTimeout(copiedResetTimer);
    copiedResetTimer = setTimeout(() => {
      copiedEventKey.value = "";
      copiedResetTimer = undefined;
    }, 2000);
    ElMessage.success(props.labels.copiedResult);
  } catch (error) {
    ElMessage.error(props.labels.copyResultFailed.replace("{message}", String(error)));
  }
}
function isEventDetailCopied(event: AiAssistantEvent) {
  return copiedEventKey.value === eventKey(event);
}
onBeforeUnmount(() => {
  if (copiedResetTimer) clearTimeout(copiedResetTimer);
});
</script>

<template>
  <ElCard ref="card" class="ai-agent-event-group" shadow="never" :data-status="groupStatus">
    <ElCollapse v-model="activeNames" class="ai-agent-event-group__collapse" @change="markReaderToggled">
      <ElCollapseItem :name="groupKey">
        <template #title>
          <span class="ai-agent-event-group__header">
            <ElIcon class="ai-agent-event-card__icon" aria-hidden="true">
              <component :is="groupIcon" :class="{ 'is-spinning': groupStatus === 'running' }" />
            </ElIcon>
            <span class="ai-agent-event-card__title">
              <strong>{{ title }}</strong>
              <AiAgentEventElapsed
                :events="events"
                aggregate
                :template="groupTimingTemplate"
                :fallback="groupStatusLabel"
              />
            </span>
            <span class="ai-agent-event-group__counts" role="status">
              <ElTag
                v-for="item in countLabels"
                :key="item.status"
                class="ai-agent-event-group__count"
                :type="item.status === 'failed' ? 'danger' : item.status === 'running' ? 'primary' : 'success'"
                effect="light"
                size="small"
              >
                {{ item.label }}
              </ElTag>
            </span>
          </span>
        </template>
        <ElScrollbar
          v-if="groupExpanded"
          class="ai-agent-event-group__items-scroll"
          max-height="min(360px, 45vh)"
          view-class="ai-agent-event-group__items"
          always
        >
          <article
            v-for="event in events"
            :key="eventKey(event)"
            class="ai-agent-event-group__item"
            :class="{ 'is-expandable': canExpand(event), 'is-expanded': isItemExpanded(event) }"
            :data-status="statusOf(event)"
          >
            <ElButton
              v-if="canExpand(event)"
              text
              class="ai-agent-event-group__item-row"
              :aria-expanded="isItemExpanded(event)"
              :aria-label="`${activityTitle(event)} · ${detailToggleLabel(event)}`"
              @click="toggleItem(event)"
            >
              <ElIcon class="ai-agent-event-group__item-icon" aria-hidden="true">
                <component :is="statusIcon(event)" :class="{ 'is-spinning': isRunning(event) }" />
              </ElIcon>
              <span class="ai-agent-event-group__item-copy" :title="activityTitle(event)">
                <strong>{{ activityTypeLabel(event) }}</strong>
                <span v-if="activityTarget(event)" aria-hidden="true">·</span>
                <code v-if="activityTarget(event)">{{ activityTarget(event) }}</code>
              </span>
              <AiAgentEventElapsed
                class="ai-agent-event-group__item-duration"
                :events="[event]"
                :title-template="labels.activity.duration"
              />
              <ElTag
                v-if="isRunning(event) || isFailed(event)"
                :type="statusTagType(event)"
                effect="light"
                size="small"
                role="status"
              >
                {{ statusLabel(event) }}
              </ElTag>
              <ElIcon class="ai-agent-event-group__item-arrow" aria-hidden="true">
                <component :is="isItemExpanded(event) ? ArrowDown : ArrowRight" />
              </ElIcon>
            </ElButton>
            <div v-else class="ai-agent-event-group__item-row ai-agent-event-group__item-row--static">
              <ElIcon class="ai-agent-event-group__item-icon" aria-hidden="true">
                <component :is="statusIcon(event)" :class="{ 'is-spinning': isRunning(event) }" />
              </ElIcon>
              <span class="ai-agent-event-group__item-copy" :title="activityTitle(event)">
                <strong>{{ activityTypeLabel(event) }}</strong>
                <span v-if="activityTarget(event)" aria-hidden="true">·</span>
                <code v-if="activityTarget(event)">{{ activityTarget(event) }}</code>
              </span>
              <AiAgentEventElapsed
                class="ai-agent-event-group__item-duration"
                :events="[event]"
                :title-template="labels.activity.duration"
              />
              <ElTag
                v-if="isRunning(event) || isFailed(event)"
                :type="statusTagType(event)"
                effect="light"
                size="small"
                role="status"
              >
                {{ statusLabel(event) }}
              </ElTag>
            </div>

            <template v-if="isItemExpanded(event)">
              <dl v-if="event.toolName" class="ai-agent-event-group__metadata">
                <div>
                  <dt>{{ labels.activity.toolName }}</dt>
                  <dd><code>{{ event.toolName }}</code></dd>
                </div>
              </dl>
              <strong v-if="formattedDetail(event)" class="ai-agent-event-group__detail-label">
                {{ labels.activity.result }}
              </strong>
              <div v-if="formattedDetail(event)" class="ai-agent-event-group__detail-shell">
                <ElButton
                  class="ai-agent-event-group__copy-detail"
                  text
                  size="small"
                  native-type="button"
                  :aria-label="isEventDetailCopied(event) ? labels.copiedResult : labels.copyResult"
                  :title="isEventDetailCopied(event) ? labels.copiedResult : labels.copyResult"
                  @click.stop="copyEventDetail(event)"
                >
                  <ElIcon aria-hidden="true">
                    <Check v-if="isEventDetailCopied(event)" />
                    <CopyDocument v-else />
                  </ElIcon>
                </ElButton>
                <AiAgentEventDetail
                  :event="event"
                  :content="presentationFor(event).text"
                  :failed="isFailed(event)"
                  :role="isFailed(event) ? 'alert' : undefined"
                />
              </div>
              <AiTaskSteps
                v-if="presentationFor(event).steps.length"
                :label="labels.taskSteps"
                :steps="presentationFor(event).steps"
                :status-labels="labels.taskStepStatuses"
                :summary="event.kind === 'plan' ? presentationFor(event).text : ''"
                :dismissible="event.kind === 'plan'"
                :dismiss-label="labels.dismissTaskSteps"
                @dismiss="emit('dismiss', event)"
              />
              <AiSourcesList
                v-if="presentationFor(event).sources.length"
                :label="labels.sources"
                :sources="presentationFor(event).sources"
              />
              <section v-if="presentationFor(event).artifacts.length" class="ai-artifacts" :aria-label="labels.artifacts">
                <strong class="ai-presentation-heading">{{ labels.artifacts }}</strong>
                <AiArtifactCard
                  v-for="(artifact, index) in presentationFor(event).artifacts"
                  :key="`${index}-${artifact.title}`"
                  :artifact="artifact"
                  :labels="artifactLabels"
                />
              </section>
            </template>
          </article>
        </ElScrollbar>
      </ElCollapseItem>
    </ElCollapse>
  </ElCard>
</template>
