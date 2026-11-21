<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { Lock } from "@element-plus/icons-vue";
import { approvalScopesForRisk } from "@/domain/ai/tool-approval";
import {
  normalizeApprovalFeedback,
  type ToolApprovalDecision,
  type ToolApprovalQueueItem,
} from "@/domain/ai/tool-approval-queue";
import type { AiCapabilityRisk } from "@/domain/ai/capability-registry";
import { sessionGrantBindsParameters, type ToolAuthorizationScope } from "@/domain/ai/tool-authorization";

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
  approval: ToolApprovalQueueItem;
  labels: InlineToolApprovalLabels;
}>();

const emit = defineEmits<{
  decide: [id: string, decision: ToolApprovalDecision];
}>();

const root = ref<HTMLElement | { $el?: HTMLElement }>();
const feedbackText = ref("");
const feedback = computed(() => normalizeApprovalFeedback(feedbackText.value));
const rejectLabel = computed(() => (feedback.value ? props.labels.rejectWithFeedback : props.labels.reject));
const titleId = computed(() => `tool-approval-${props.approval.id}-title`);
const descriptionId = computed(() => `tool-approval-${props.approval.id}-description`);
let previousFocus: HTMLElement | null = null;

// The exact call being approved, shown in full only here: the summary may be redacted for
// logs and relayed notifications, but the local user must see what will actually run.
const parameters = computed(() =>
  Object.entries(props.approval.request.parameters)
    .filter(([key, value]) => key !== "resourceId" && value !== undefined)
    .map(([key, value]) => ({ key, text: typeof value === "string" ? value : JSON.stringify(value, null, 2) ?? String(value) })),
);
const sessionLabel = computed(() =>
  sessionGrantBindsParameters(props.approval.request) ? props.labels.allowSessionSameParameters : props.labels.allowSession,
);

onMounted(() => {
  previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  void nextTick(() => {
    const element = root.value instanceof HTMLElement ? root.value : root.value?.$el;
    element?.focus();
  });
});

onBeforeUnmount(() => {
  if (previousFocus?.isConnected) previousFocus.focus();
});

function allow(scope: ToolAuthorizationScope) {
  emit("decide", props.approval.id, { scope });
}

function reject() {
  emit("decide", props.approval.id, feedback.value ? { feedback: feedback.value } : {});
}
</script>

<template>
  <el-card
    ref="root"
    class="ai-inline-tool-approval-card"
    shadow="never"
    role="region"
    aria-live="assertive"
    tabindex="-1"
    :aria-labelledby="titleId"
    :aria-describedby="descriptionId"
    :data-approval-id="approval.id"
    :data-risk="approval.request.risk"
  >
    <header class="ai-inline-tool-approval-card__header">
      <Lock aria-hidden="true" />
      <strong :id="titleId">{{ labels.title }}</strong>
      <el-tag :type="approval.request.risk === 'dangerous' ? 'danger' : 'warning'" effect="plain" size="small">
        {{ labels.risk[approval.request.risk] }}
      </el-tag>
    </header>
    <dl :id="descriptionId" class="ai-inline-tool-approval-card__details">
      <div><dt>{{ labels.tool }}</dt><dd>{{ approval.request.toolName }}</dd></div>
      <div><dt>{{ labels.resource }}</dt><dd>{{ approval.request.resourceId }}</dd></div>
      <div><dt>{{ labels.summary }}</dt><dd>{{ approval.summary }}</dd></div>
    </dl>
    <section v-if="parameters.length" class="ai-inline-tool-approval-card__parameters" :aria-label="labels.parameters">
      <span class="ai-inline-tool-approval-card__parameters-title">{{ labels.parameters }}</span>
      <el-scrollbar max-height="240px">
        <dl class="ai-inline-tool-approval-card__parameter-list">
          <div v-for="item in parameters" :key="item.key">
            <dt>{{ item.key }}</dt>
            <dd><pre :data-approval-parameter="item.key">{{ item.text }}</pre></dd>
          </div>
        </dl>
      </el-scrollbar>
    </section>
    <div class="ai-inline-tool-approval-card__feedback" data-approval-feedback>
      <el-input
        v-model="feedbackText"
        size="small"
        maxlength="500"
        show-word-limit
        :placeholder="labels.feedbackPlaceholder"
        :aria-label="labels.feedbackPlaceholder"
      />
    </div>
    <div class="ai-inline-tool-approval-card__actions">
      <el-button
        size="small"
        data-approval-action="reject"
        :aria-label="rejectLabel"
        @click="reject()"
      >{{ rejectLabel }}</el-button>
      <el-button
        size="small"
        data-approval-scope="once"
        :aria-label="labels.allowOnce"
        @click="allow('once')"
      >{{ labels.allowOnce }}</el-button>
      <el-button
        size="small"
        data-approval-scope="session"
        :aria-label="sessionLabel"
        @click="allow('session')"
      >{{ sessionLabel }}</el-button>
      <el-button
        v-if="approvalScopesForRisk(approval.request.risk).includes('global')"
        type="primary"
        size="small"
        data-approval-scope="global"
        :aria-label="labels.allowGlobal"
        @click="allow('global')"
      >{{ labels.allowGlobal }}</el-button>
    </div>
  </el-card>
</template>
