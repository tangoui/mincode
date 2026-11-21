<script setup lang="ts">
import { computed, ref, watch } from "vue";
import {
  ChatDotRound,
  CircleCheck,
  Close,
  Loading,
  Refresh,
  Select,
  VideoPause,
  WarningFilled,
} from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import {
  agentRunStepPercent,
  isActiveAgentRun,
  type AgentRun,
  type AgentRunPhase,
} from "@/domain/ai/session-progress";
import { useAgentRunNavigation } from "@/composables/use-agent-run-navigation";
import { useSessionProgressStore } from "@/stores/session-progress";
import { useWorkbenchStore } from "@/stores/workbench";

const { t, intlLocale } = useVueI18n();
const progress = useSessionProgressStore();
const navigation = useAgentRunNavigation();
const workbench = useWorkbenchStore();
const visible = ref(false);
// Desktop navigation (for example a click on a desktop pet) can ask for the list.
watch(() => progress.panelRequest, () => { visible.value = true; });

void progress.ensureListening();

const badgeCount = computed(() => progress.attentionCount || progress.activeCount);
const buttonLabel = computed(() => progress.activeCount
  ? t("ai.taskProgress.activeLabel", { count: progress.activeCount })
  : t("ai.taskProgress.title"));

const groupedRuns = computed(() => {
  const visibleRuns = progress.visibleRuns;
  const visibleIds = new Set(visibleRuns.map((run) => run.runId));
  const children = new Map<string, AgentRun[]>();
  for (const run of visibleRuns) {
    if (!run.parentRunId || !visibleIds.has(run.parentRunId)) continue;
    const siblings = children.get(run.parentRunId) ?? [];
    siblings.push(run);
    children.set(run.parentRunId, siblings);
  }
  return visibleRuns
    .filter((run) => !run.parentRunId || !visibleIds.has(run.parentRunId))
    .map((run) => ({
      run,
      children: (children.get(run.runId) ?? [])
        .sort((a, b) => a.progress.createdAt - b.progress.createdAt),
    }));
});

const phaseKeys: Record<AgentRunPhase, Parameters<typeof t>[0]> = {
  queued: "ai.taskProgress.phase.queued",
  thinking: "ai.taskProgress.phase.thinking",
  streaming: "ai.taskProgress.phase.streaming",
  tool_running: "ai.taskProgress.phase.toolRunning",
  waiting_approval: "ai.taskProgress.phase.waitingApproval",
  waiting_input: "ai.taskProgress.phase.waitingInput",
  cancelling: "ai.taskProgress.phase.cancelling",
  completed: "ai.taskProgress.phase.completed",
  failed: "ai.taskProgress.phase.failed",
  cancelled: "ai.taskProgress.phase.cancelled",
};

function phaseLabel(phase: AgentRunPhase) {
  return t(phaseKeys[phase]);
}

function phaseIcon(phase: AgentRunPhase) {
  if (phase === "failed") return WarningFilled;
  if (phase === "completed") return CircleCheck;
  if (phase === "cancelled") return VideoPause;
  if (phase === "waiting_approval" || phase === "waiting_input") return Select;
  return Loading;
}

function isSpinning(phase: AgentRunPhase) {
  return ["queued", "thinking", "streaming", "tool_running", "cancelling"].includes(phase);
}

function timeLabel(timestamp: number) {
  return new Date(timestamp).toLocaleTimeString(intlLocale.value, {
    hour: "2-digit",
    minute: "2-digit",
  });
}

function runSummary(run: AgentRun) {
  return run.progress.summary || phaseLabel(run.progress.phase);
}

function stepProgressColor(run: AgentRun) {
  if (run.progress.phase === "failed" || run.progress.phase === "cancelled") return "var(--el-color-danger)";
  return run.progress.stepsCompleted === run.progress.stepsTotal
    ? "var(--el-color-success)"
    : "var(--el-color-primary)";
}

async function openRun(run: AgentRun) {
  if (await navigation.openRun(run)) visible.value = false;
}

async function cancelRun(run: AgentRun) {
  try {
    await progress.requestCancel(run.runId);
  } catch (error) {
    ElMessage.error(t("ai.taskProgress.cancelFailed", { message: String(error) }));
  }
}

async function dismissRun(run: AgentRun) {
  try {
    await progress.dismiss(run.runId);
  } catch (error) {
    ElMessage.error(t("ai.taskProgress.dismissFailed", { message: String(error) }));
  }
}
</script>

<template>
  <el-popover
    v-model:visible="visible"
    placement="bottom-end"
    trigger="click"
    :width="370"
    popper-class="app-ai-task-progress-popover"
  >
    <template #reference>
      <el-button
        text
        native-type="button"
        class="app-header-button app-ai-task-progress-button"
        :class="{ 'has-attention': progress.attentionCount > 0 }"
        :aria-label="buttonLabel"
        :title="buttonLabel"
      >
        <el-icon :class="{ 'is-loading': progress.activeCount > 0 && !progress.attentionCount }">
          <WarningFilled v-if="progress.attentionCount" />
          <Loading v-else-if="progress.activeCount" />
          <ChatDotRound v-else />
        </el-icon>
        <span v-if="badgeCount" class="app-ai-task-progress-button__badge">
          {{ badgeCount > 99 ? "99+" : badgeCount }}
        </span>
      </el-button>
    </template>

    <section class="app-ai-task-progress" :aria-label="t('ai.taskProgress.title')">
      <header class="app-ai-task-progress__header">
        <strong>{{ t("ai.taskProgress.title") }}</strong>
        <span>{{ t("ai.taskProgress.activeCount", { count: progress.activeCount }) }}</span>
      </header>

      <div v-if="progress.loadError" class="app-ai-task-progress__load-error">
        <el-alert
          type="error"
          show-icon
          :closable="false"
          :title="t('ai.taskProgress.loadFailed', { message: progress.loadError })"
        />
        <el-button
          text
          native-type="button"
          :icon="Refresh"
          @click="void progress.retryHydrate()"
        >
          {{ t("common.refresh") }}
        </el-button>
      </div>

      <el-scrollbar v-if="progress.visibleRuns.length" max-height="420px">
        <div class="app-ai-task-progress__list">
          <template v-for="group in groupedRuns" :key="group.run.runId">
            <article
              class="app-ai-task-progress__item"
              :class="[`is-${group.run.progress.phase}`, {
                'is-current-workspace': group.run.workspaceId === workbench.activeWorkspaceId,
              }]"
            >
              <el-button
                text
                native-type="button"
                class="app-ai-task-progress__open"
                @click="void openRun(group.run)"
              >
                <el-icon
                  class="app-ai-task-progress__phase-icon"
                  :class="{ 'is-loading': isSpinning(group.run.progress.phase) }"
                >
                  <component :is="phaseIcon(group.run.progress.phase)" />
                </el-icon>
                <span class="app-ai-task-progress__content">
                  <span class="app-ai-task-progress__title-row">
                    <strong>{{ group.run.title }}</strong>
                    <time :datetime="new Date(group.run.progress.updatedAt).toISOString()">
                      {{ timeLabel(group.run.progress.updatedAt) }}
                    </time>
                  </span>
                  <span class="app-ai-task-progress__summary">{{ runSummary(group.run) }}</span>
                  <el-progress
                    v-if="agentRunStepPercent(group.run.progress) !== undefined"
                    class="app-ai-task-progress__bar"
                    :percentage="agentRunStepPercent(group.run.progress)"
                    :color="stepProgressColor(group.run)"
                    :stroke-width="4"
                    :format="(value: number) => `${value}%`"
                  />
                  <span class="app-ai-task-progress__meta">
                    {{ phaseLabel(group.run.progress.phase) }} · {{ group.run.source === "acp" ? "ACP" : t("ai.taskProgress.localAgent") }}
                  </span>
                </span>
              </el-button>
              <el-button
                v-if="isActiveAgentRun(group.run) && group.run.progress.phase !== 'cancelling'"
                text native-type="button"
                class="app-ai-task-progress__action"
                :icon="VideoPause"
                :aria-label="t('ai.taskProgress.cancel')"
                :title="t('ai.taskProgress.cancel')"
                @click="void cancelRun(group.run)"
              />
              <el-button
                v-else-if="!isActiveAgentRun(group.run)"
                text native-type="button"
                class="app-ai-task-progress__action"
                :icon="Close"
                :aria-label="t('ai.taskProgress.dismiss')"
                :title="t('ai.taskProgress.dismiss')"
                @click="void dismissRun(group.run)"
              />
            </article>
            <article
              v-for="child in group.children"
              :key="child.runId"
              class="app-ai-task-progress__item app-ai-task-progress__item--child"
              :class="`is-${child.progress.phase}`"
            >
              <el-button
                text native-type="button"
                class="app-ai-task-progress__open"
                @click="void openRun(child)"
              >
                <el-icon
                  class="app-ai-task-progress__phase-icon"
                  :class="{ 'is-loading': isSpinning(child.progress.phase) }"
                >
                  <component :is="phaseIcon(child.progress.phase)" />
                </el-icon>
                <span class="app-ai-task-progress__content">
                  <span class="app-ai-task-progress__title-row">
                    <strong>{{ child.title }}</strong>
                    <time :datetime="new Date(child.progress.updatedAt).toISOString()">
                      {{ timeLabel(child.progress.updatedAt) }}
                    </time>
                  </span>
                  <span class="app-ai-task-progress__summary">{{ runSummary(child) }}</span>
                  <span class="app-ai-task-progress__meta">
                    {{ phaseLabel(child.progress.phase) }}
                    <template v-if="child.branch"> · {{ child.branch }}</template>
                  </span>
                </span>
              </el-button>
              <el-button
                v-if="isActiveAgentRun(child) && child.progress.phase !== 'cancelling'"
                text native-type="button"
                class="app-ai-task-progress__action"
                :icon="VideoPause"
                :aria-label="t('ai.taskProgress.cancel')"
                :title="t('ai.taskProgress.cancel')"
                @click="void cancelRun(child)"
              />
              <el-button
                v-else-if="!isActiveAgentRun(child)"
                text native-type="button"
                class="app-ai-task-progress__action"
                :icon="Close"
                :aria-label="t('ai.taskProgress.dismiss')"
                :title="t('ai.taskProgress.dismiss')"
                @click="void dismissRun(child)"
              />
            </article>
          </template>
        </div>
      </el-scrollbar>

      <el-empty
        v-else
        :image-size="44"
        :description="t('ai.taskProgress.empty')"
      />
      <footer class="app-ai-task-progress__safety">
        {{ t("ai.taskProgress.safetySummary") }}
      </footer>
    </section>
  </el-popover>
</template>
