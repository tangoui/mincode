import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  isActiveAgentRun,
  isTerminalAgentRun,
  needsAgentRunAttention,
  prunableAgentRunIds,
  shouldApplyAgentRun,
  type AgentRun,
} from "@/domain/ai/session-progress";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import {
  listAgentRuns,
  listenAgentRunProgress,
  removeAgentRun,
  requestAgentRunCancel,
} from "@/services/session-progress-service";

const COMPLETED_VISIBILITY_MS = 5 * 60_000;
const FAILED_VISIBILITY_MS = 30 * 60_000;
const MAX_VISIBLE_RUNS = 20;
const MAX_FINISHED_RUNS = 50;
const MAX_PRUNED_TOMBSTONES = 500;

export const useSessionProgressStore = defineStore("session-progress", () => {
  const workspaceScope = useWorkspaceStateScope();
  const runsById = ref<Record<string, AgentRun>>({});
  const hydrated = ref(false);
  const loadError = ref("");
  const visibilityClock = ref(Date.now());
  // Incremented to ask the AI task list in the title bar to open.
  const panelRequest = ref(0);
  let stopListening: (() => void) | undefined;
  let hydratePromise: Promise<void> | undefined;
  let visibilityTimer: ReturnType<typeof setInterval> | undefined;
  // Last known revision of pruned runs, so late out-of-order events cannot resurrect them.
  const prunedRuns = new Map<string, AgentRun>();

  const runs = computed(() => Object.values(runsById.value));
  const activeRuns = computed(() => runs.value
    .filter(isActiveAgentRun)
    .sort((a, b) => b.progress.updatedAt - a.progress.updatedAt));
  const attentionRuns = computed(() => runs.value
    .filter(needsAgentRunAttention)
    .sort((a, b) => b.progress.updatedAt - a.progress.updatedAt));
  const activeCount = computed(() => activeRuns.value.length);
  const attentionCount = computed(() => attentionRuns.value.length);
  const currentWorkspaceRuns = computed(() => runs.value.filter(
    (run) => run.workspaceScopeId === workspaceScope.scopeId.value,
  ));
  const visibleRuns = computed(() => {
    const now = visibilityClock.value;
    return runs.value
      .filter((run) => {
        if (isActiveAgentRun(run)) return true;
        const age = now - (run.progress.completedAt ?? run.progress.updatedAt);
        return run.progress.phase === "failed"
          ? age <= FAILED_VISIBILITY_MS
          : age <= COMPLETED_VISIBILITY_MS;
      })
      .sort((a, b) => {
        const activeDelta = Number(isActiveAgentRun(b)) - Number(isActiveAgentRun(a));
        if (activeDelta) return activeDelta;
        const attentionDelta = Number(needsAgentRunAttention(b)) - Number(needsAgentRunAttention(a));
        return attentionDelta || b.progress.updatedAt - a.progress.updatedAt;
      })
      .slice(0, MAX_VISIBLE_RUNS);
  });

  function apply(run: AgentRun) {
    const current = runsById.value[run.runId] ?? prunedRuns.get(run.runId);
    if (!shouldApplyAgentRun(current, run)) return false;
    prunedRuns.delete(run.runId);
    runsById.value[run.runId] = run;
    if (isTerminalAgentRun(run)) pruneFinishedRuns();
    return true;
  }

  function pruneFinishedRuns() {
    for (const runId of prunableAgentRunIds(Object.values(runsById.value), MAX_FINISHED_RUNS)) {
      const run = runsById.value[runId];
      delete runsById.value[runId];
      if (!run) continue;
      prunedRuns.set(runId, run);
      if (prunedRuns.size > MAX_PRUNED_TOMBSTONES) {
        const oldest = prunedRuns.keys().next().value;
        if (oldest !== undefined) prunedRuns.delete(oldest);
      }
    }
  }

  function ensureListening() {
    if (!stopListening) stopListening = listenAgentRunProgress(apply);
    if (!visibilityTimer) {
      visibilityTimer = setInterval(() => {
        visibilityClock.value = Date.now();
      }, 30_000);
    }
    if (!hydratePromise) {
      loadError.value = "";
      hydrated.value = false;
      hydratePromise = listAgentRuns()
        .then((listed) => {
          for (const run of listed) apply(run);
        })
        .catch((error) => {
          loadError.value = error instanceof Error ? error.message : String(error);
        })
        .finally(() => {
          hydrated.value = true;
        });
    }
    return hydratePromise;
  }

  function retryHydrate() {
    hydratePromise = undefined;
    return ensureListening();
  }

  function requestPanel() {
    panelRequest.value += 1;
  }

  async function requestCancel(runId: string) {
    const run = await requestAgentRunCancel(runId);
    apply(run);
  }

  async function dismiss(runId: string) {
    await removeAgentRun(runId);
    delete runsById.value[runId];
  }

  function dispose() {
    stopListening?.();
    stopListening = undefined;
    hydratePromise = undefined;
    if (visibilityTimer) clearInterval(visibilityTimer);
    visibilityTimer = undefined;
    hydrated.value = false;
    loadError.value = "";
  }

  return {
    runsById,
    runs,
    activeRuns,
    attentionRuns,
    activeCount,
    attentionCount,
    currentWorkspaceRuns,
    visibleRuns,
    hydrated,
    loadError,
    panelRequest,
    requestPanel,
    apply,
    ensureListening,
    retryHydrate,
    requestCancel,
    dismiss,
    dispose,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useSessionProgressStore, import.meta.hot));
}
