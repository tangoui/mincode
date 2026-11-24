<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from "vue";
import { Lock } from "@element-plus/icons-vue";
import { ElButton, ElInput } from "element-plus";
import { approvalScopesForRisk } from "@/domain/ai/tool-approval";
import { normalizeApprovalFeedback, type ToolApprovalDecision, type ToolApprovalQueueItem } from "@/domain/ai/tool-approval-queue";
import type { AiCapabilityRisk } from "@/domain/ai/capability-registry";
import { sessionGrantBindsParameters, type ToolAuthorizationScope } from "@/domain/ai/tool-authorization";
import { useVueI18n } from "@/i18n/messages";
import type { AiAssistantPermission } from "@/stores/ai-assistant";

export interface InlineToolApprovalLabels {
  title: string;
  tool: string;
  resource: string;
  summary: string;
  parameters: string;
  risk: Record<AiCapabilityRisk, string>;
  allowOnce: string;
  allowSession: string;
  allowSessionSameParameters: string;
  allowGlobal: string;
  reject: string;
  feedbackPlaceholder: string;
  rejectWithFeedback: string;
}

const props = defineProps<{
  source: { kind: "tool"; approval: ToolApprovalQueueItem } | { kind: "permission"; permission: AiAssistantPermission };
  labels?: InlineToolApprovalLabels;
  composerHasDraft: boolean;
  surfaceHidden?: boolean;
}>();
const emit = defineEmits<{
  "decide-tool": [decision: ToolApprovalDecision];
  "decide-permission": [optionId?: string];
}>();

const { t } = useVueI18n();
const id = useId();
const root = ref<HTMLElement>();
const feedbackOpen = ref(false);
const feedbackText = ref("");
const feedback = computed(() => normalizeApprovalFeedback(feedbackText.value));
const approval = computed(() => props.source.kind === "tool" ? props.source.approval : undefined);
const permission = computed(() => props.source.kind === "permission" ? props.source.permission : undefined);
const request = computed(() => approval.value?.request);
const title = computed(() => t("ai.timeline.approvalRequired"));
const description = computed(() => {
  if (approval.value) return approval.value.summary;
  const call = permission.value?.toolCall;
  if (call && typeof call === "object" && "title" in call && typeof call.title === "string" && call.title.trim()) {
    return call.title.trim();
  }
  return t("ai.permissionHint");
});
const parameters = computed(() => Object.entries(request.value?.parameters ?? {})
  .filter(([key, value]) => key !== "resourceId" && value !== undefined)
  .map(([key, value]) => ({ key, text: typeof value === "string" ? value : JSON.stringify(value, null, 2) ?? String(value) })));
const scopes = computed(() => request.value && approvalScopesForRisk(request.value.risk));
const sessionLabel = computed(() => request.value && sessionGrantBindsParameters(request.value)
  ? props.labels?.allowSessionSameParameters : props.labels?.allowSession);
let previousFocus: HTMLElement | null = null;

function reveal() {
  if (props.surfaceHidden || !root.value?.isConnected) return;
  const active = document.activeElement;
  if (active instanceof HTMLElement && !root.value.contains(active)) previousFocus = active;
  root.value.scrollIntoView?.({ block: "nearest" });
  if (!props.composerHasDraft) root.value.querySelector("button")?.focus({ preventScroll: true });
}
onMounted(() => { void nextTick(reveal); });
watch(() => props.surfaceHidden, (hidden) => { if (!hidden) reveal(); }, { flush: "post" });
onBeforeUnmount(() => {
  if (root.value?.contains(document.activeElement) && previousFocus?.isConnected) previousFocus.focus({ preventScroll: true });
});

function allow(scope: ToolAuthorizationScope) {
  emit("decide-tool", { scope });
}
function reject() {
  emit("decide-tool", feedback.value ? { feedback: feedback.value } : {});
}
function showFeedback() {
  feedbackOpen.value = !feedbackOpen.value;
  if (feedbackOpen.value) void nextTick(() => root.value?.querySelector("input")?.focus());
}
</script>

<template>
  <section
    ref="root" class="ai-timeline-approval is-attention" role="region" aria-live="assertive"
    :aria-labelledby="`${id}-title`" :aria-describedby="`${id}-description`"
    :data-approval-id="approval?.id" :data-risk="request?.risk"
  >
    <div class="ai-timeline__row">
      <Lock class="ai-timeline__icon" aria-hidden="true" />
      <strong :id="`${id}-title`" class="ai-timeline__title">{{ title }}</strong>
      <span v-if="request && labels" class="ai-timeline__meta">{{ labels.risk[request.risk] }}</span>
    </div>
    <div class="ai-timeline__contents">
      <p :id="`${id}-description`">{{ description }}</p>
      <template v-if="request && labels">
        <div class="ai-timeline__metadata">
          <span>{{ labels.tool }} <code>{{ request.toolName }}</code></span>
          <span>{{ labels.resource }} <code>{{ request.resourceId }}</code></span>
        </div>
        <div class="ai-timeline__approval-actions">
          <ElButton size="small" type="primary" data-approval-scope="once" @click="allow('once')">{{ labels.allowOnce }}</ElButton>
          <ElButton size="small" data-approval-scope="session" @click="allow('session')">{{ sessionLabel }}</ElButton>
          <ElButton v-if="scopes?.includes('global')" size="small" data-approval-scope="global" @click="allow('global')">{{ labels.allowGlobal }}</ElButton>
          <ElButton size="small" data-approval-action="reject" :aria-expanded="feedbackOpen" @click="showFeedback">{{ labels.reject }}</ElButton>
        </div>
        <div v-if="feedbackOpen" class="ai-timeline__feedback" data-approval-feedback>
          <ElInput
            :model-value="feedbackText" size="small" maxlength="500"
            :placeholder="labels.feedbackPlaceholder" :aria-label="labels.feedbackPlaceholder"
            @update:model-value="feedbackText = $event.slice(0, 500)" @keydown.enter.prevent="reject"
          />
          <small data-approval-count>{{ feedbackText.length }} / 500</small>
          <ElButton size="small" data-approval-action="confirm-reject" @click="reject">
            {{ feedback ? labels.rejectWithFeedback : labels.reject }}
          </ElButton>
        </div>
        <details v-if="parameters.length" class="ai-timeline__parameters">
          <summary>{{ labels.parameters }}</summary>
          <dl>
            <div v-for="item in parameters" :key="item.key">
              <dt>{{ item.key }}</dt>
              <dd><pre :data-approval-parameter="item.key">{{ item.text }}</pre></dd>
            </div>
          </dl>
        </details>
      </template>
      <div v-else-if="permission" class="ai-timeline__approval-actions">
        <ElButton
          v-for="option in permission.options" :key="option.optionId" :data-option-id="option.optionId"
          :type="option.kind?.startsWith('allow') ? 'primary' : 'default'" size="small"
          @click="emit('decide-permission', option.optionId)"
        >{{ option.name }}</ElButton>
        <ElButton data-action="cancel" size="small" @click="emit('decide-permission', undefined)">{{ t('common.cancel') }}</ElButton>
      </div>
    </div>
  </section>
</template>
