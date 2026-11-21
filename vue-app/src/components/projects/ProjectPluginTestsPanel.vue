<script setup lang="ts">
/**
 * Project sidebar "Tests" tab: the plugin test providers
 * (contributes.testProviders) of process plugins active in this workspace.
 * Opening the tab discovers the selected provider's tests (a `read`-risk
 * action; the discovery is shared with the editor gutter through
 * stores/test-discovery.ts). Tests run only on the user's action. Nothing is
 * persisted.
 *
 * Two run paths:
 * - A provider with `debugRun` (host 0.3.0) runs and debugs tests as debug
 *   sessions with streamed results (services/test-run-launcher.ts): the
 *   toolbar offers Run all, Debug all, Rerun failed and Stop, and the lazily
 *   loaded ProjectTestRunResults shows the newest run of the provider in
 *   this project (live tree, summary, details). One run at a time per
 *   provider and project: a new one stops the previous (with a hint).
 * - Otherwise the provider's synchronous run action: a run action whose
 *   risk is not `read` asks once per provider and app session, and results
 *   for ids that are not in the discovered tree are ignored.
 */
import { computed, defineAsyncComponent, onBeforeUnmount, ref, shallowRef, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { CircleCheck, CircleClose, Refresh, RefreshLeft, Remove, VideoPlay, WarningFilled } from "@element-plus/icons-vue";
import {
  aggregateTestStatus,
  testIds,
  testProviderKey,
  testProviderTitle,
  type PluginTestProvider,
  type TestItem,
  type TestResult,
  type TestStatus,
} from "@/domain/plugins/test-providers";
import { findTestItem, pickTestItems } from "@/domain/testing/test-discovery";
import { failedTestIds } from "@/domain/testing/test-runs";
import { useVueI18n } from "@/i18n/messages";
import { authorizeWithoutPrompt } from "@/services/plugin-full-trust";
import { providerErrorText } from "@/services/plugin-provider-invoke";
import { runTests } from "@/services/plugin-test-provider-service";
import { stopTestRun } from "@/services/plugin-test-runs";
import { launchTestRun } from "@/services/test-run-launcher";
import { usePluginTestProvidersStore } from "@/stores/plugin-test-providers";
import { useTestDiscovery } from "@/stores/test-discovery";
import { testRunProviderKey, useTestRuns } from "@/stores/test-runs";

const ProjectTestRunResults = defineAsyncComponent(() => import("@/components/projects/ProjectTestRunResults.vue"));

const props = defineProps<{ projectRoot: string }>();
const emit = defineEmits<{
  "open-file": [path: string, line: number | undefined];
  /** A debugger address in a stack trace, with the run's session and debugger type. */
  "open-address": [address: string, sessionId: string, type: string];
}>();

const STATUS_ICONS = { passed: CircleCheck, failed: CircleClose, errored: WarningFilled, skipped: Remove } as const;

const { t, localeChain } = useVueI18n();
const store = usePluginTestProvidersStore();
const discovery = useTestDiscovery();
const testRuns = useTestRuns();
const selectedKey = ref<string>();
const results = shallowRef(new Map<string, TestResult>());
const output = ref("");
const outputTruncated = ref(false);
const discovering = ref(false);
const running = ref(false);
const discovered = ref(false);
const error = ref("");
const generation = ref(0);
let runController: AbortController | undefined;

const providers = computed(() => store.providers);
const selected = computed<PluginTestProvider | undefined>(
  () => providers.value.find((provider) => testProviderKey(provider) === selectedKey.value) ?? providers.value[0],
);
const options = computed(() =>
  providers.value.map((provider) => ({
    key: testProviderKey(provider),
    label: `${testProviderTitle(provider, localeChain.value)} · ${provider.pluginName}`,
  })),
);
const locale = computed(() => localeChain.value[0] ?? "en");
/** The discovered tests (the shared discovery, also refreshed by the editor after a save). */
const tests = computed<TestItem[]>(() => {
  const provider = selected.value;
  return provider && discovered.value ? (discovery.entry(provider, props.projectRoot)?.tests ?? []) : [];
});
/** The discovery's notice (kept in sync when the editor discovers again after a save). */
const notice = computed(() => {
  const provider = selected.value;
  const current = provider && discovered.value ? discovery.entry(provider, props.projectRoot) : undefined;
  return current ? noticeFor(current.dropped, current.truncated) : "";
});
const knownIds = computed(() => new Set(testIds(tests.value)));
const summary = computed(() => {
  const counts: Record<TestStatus, number> = { passed: 0, failed: 0, skipped: 0, errored: 0 };
  for (const result of results.value.values()) counts[result.status] += 1;
  return counts;
});
const hasResults = computed(() => results.value.size > 0);

// ---- streaming runs (debugRun) ----

const streaming = computed(() => Boolean(selected.value?.debugRun));
/** The provider's newest run in this project (a snapshot: a new object per change of the run). */
const currentRun = computed(() => {
  const provider = selected.value;
  return provider?.debugRun ? testRuns.currentRun(provider.pluginId, provider.id, props.projectRoot) : undefined;
});
/** The run whose results the user put away ("Show all tests"). */
const dismissedRunId = ref<string>();
const shownRun = computed(() => (currentRun.value && currentRun.value.runId !== dismissedRunId.value ? currentRun.value : undefined));
const runActive = computed(() => Boolean(currentRun.value && currentRun.value.phase !== "ended"));
const launching = computed(() => {
  const provider = selected.value;
  return provider ? testRuns.isLaunching(testRunProviderKey(provider.pluginId, provider.id, props.projectRoot)) : false;
});
// Derived from the snapshot itself (never from a computed holding its nodes).
const rerunIds = computed(() => (currentRun.value ? failedTestIds(currentRun.value) : []));
/** The streaming results view replaces the plain tree (a run, or discovered tests to run). */
const showResults = computed(() =>
  Boolean(streaming.value && selected.value && !error.value && (shownRun.value || (!discovering.value && tests.value.length))),
);
const streamBusy = computed(() => runActive.value || launching.value || discovering.value);

function abortAll() {
  runController?.abort();
  runController = undefined;
}

function noticeFor(dropped: number, truncated: boolean) {
  if (truncated) return t("pluginContrib.testProviders.truncated");
  if (dropped) return t("pluginContrib.testProviders.dropped", { count: dropped });
  return "";
}

/**
 * Discovers the selected provider's tests. A fresh kept discovery (shared
 * with the editor, see TEST_DISCOVERY_FRESH_MS) is reused unless `refresh`
 * (the Refresh button), which also puts an ended run's results away.
 */
async function discover(refresh = false) {
  const dismissResults = refresh;
  abortAll();
  generation.value += 1;
  const current = generation.value;
  const provider = selected.value;
  results.value = new Map();
  output.value = "";
  outputTruncated.value = false;
  error.value = "";
  discovered.value = false;
  running.value = false;
  if (dismissResults && currentRun.value?.phase === "ended") dismissedRunId.value = currentRun.value.runId;
  if (!provider || !props.projectRoot) {
    discovering.value = false;
    return;
  }
  if (!refresh && discovery.isFresh(provider, props.projectRoot, locale.value)) {
    discovering.value = false;
    discovered.value = true;
    return;
  }
  discovering.value = true;
  const result = await discovery.discover(provider, props.projectRoot, locale.value);
  if (current !== generation.value) return;
  discovering.value = false;
  if (!result.ok) {
    if (result.error.code !== "cancelled") error.value = providerErrorText(result.error);
    return;
  }
  discovered.value = true;
}

async function confirmRisk(provider: PluginTestProvider) {
  const key = testProviderKey(provider);
  if (provider.runRisk === "read" || store.isRunConfirmed(key)) return true;
  // A trusted skip (decided and audited by Go) is not remembered as the user's confirmation.
  if (await authorizeWithoutPrompt(provider.pluginId, "testRun", provider.runRisk, provider.id)) return true;
  try {
    await ElMessageBox.confirm(
      t("pluginContrib.testProviders.confirmRun", {
        provider: testProviderTitle(provider, localeChain.value),
        plugin: provider.pluginName,
        risk: t(`pluginContrib.testProviders.risk.${provider.runRisk}`),
      }),
      t("pluginContrib.testProviders.confirmRunTitle"),
      { type: "warning", confirmButtonText: t("pluginContrib.testProviders.run"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return false;
  }
  store.confirmRun(key);
  return true;
}

/** Runs the given test ids; an empty list runs every test of the provider. */
async function run(ids: string[]) {
  const provider = selected.value;
  if (!provider || running.value || discovering.value || !discovered.value) return;
  if (provider.debugRun) {
    await runStreaming(ids, false);
    return;
  }
  if (!(await confirmRisk(provider))) return;
  if (provider !== selected.value) return;
  const current = generation.value;
  running.value = true;
  const controller = new AbortController();
  runController = controller;
  const result = await runTests(
    provider,
    { projectRoot: props.projectRoot, locale: locale.value, tests: ids, known: knownIds.value },
    controller.signal,
  );
  if (current !== generation.value) return;
  running.value = false;
  runController = undefined;
  if (!result.ok) {
    if (result.error.code !== "cancelled") {
      ElMessage({ type: "warning", grouping: true, message: t("pluginContrib.testProviders.runFailed", { error: providerErrorText(result.error) }) });
    }
    return;
  }
  const next = ids.length ? new Map(results.value) : new Map<string, TestResult>();
  for (const item of result.response.results) next.set(item.id, item);
  results.value = next;
  output.value = result.response.output ?? "";
  outputTruncated.value = result.response.outputTruncated;
  const runNotice = noticeFor(result.response.dropped, result.response.truncated);
  if (runNotice) ElMessage({ type: "info", grouping: true, message: runNotice });
}

/** The debug panel name of a run of ids: the provider for all tests, else the first test's name. */
function runName(provider: PluginTestProvider, ids: readonly string[]) {
  if (!ids.length) return testProviderTitle(provider, localeChain.value);
  const first = findTestItem(tests.value, ids[0]!)?.label ?? currentRun.value?.nodes[ids[0]!]?.label ?? ids[0]!;
  return ids.length > 1 ? `${first} +${ids.length - 1}` : first;
}

/** Runs or debugs ids (all when empty) as a streaming test run of the selected provider. */
async function runStreaming(ids: string[], debug: boolean) {
  const provider = selected.value;
  if (!provider?.debugRun || launching.value || discovering.value) return;
  dismissedRunId.value = undefined;
  await launchTestRun({
    pluginId: provider.pluginId,
    providerId: provider.id,
    debuggerType: provider.debugRun.debuggerType,
    projectRoot: props.projectRoot,
    testIds: [...ids],
    debug,
    name: runName(provider, ids),
    discovered: pickTestItems(tests.value, ids),
  });
}

async function stopRun() {
  const runId = currentRun.value?.runId;
  if (!runId || !runActive.value) return;
  try {
    await stopTestRun(runId);
  } catch (failure) {
    ElMessage.warning({ message: t("pluginContrib.testProviders.stopFailed", { error: failure instanceof Error ? failure.message : String(failure) }), showClose: true, grouping: true });
  }
}

function openAddress(address: string) {
  const provider = selected.value;
  if (!provider?.debugRun) return;
  emit("open-address", address, currentRun.value?.sessionId ?? "", provider.debugRun.debuggerType);
}

function statusOf(item: TestItem) {
  return aggregateTestStatus(item, results.value);
}

function nodeTitle(item: TestItem) {
  const lines = [item.label];
  if (item.path) lines.push(item.line ? `${item.path}:${item.line}` : item.path);
  const result = results.value.get(item.id);
  if (result) {
    lines.push(t(`pluginContrib.testProviders.status.${result.status}`) + (result.durationMs !== undefined ? ` · ${result.durationMs} ms` : ""));
    if (result.message) lines.push(result.message);
  }
  return lines.join("\n");
}

// Failed and errored tests show the start of their message under the name,
// so the reason is visible without hovering; the full text stays in the title.
function failureText(item: TestItem) {
  const result = results.value.get(item.id);
  if (!result?.message || (result.status !== "failed" && result.status !== "errored")) return "";
  return result.message.split("\n").map((line) => line.trim()).filter(Boolean).slice(0, 3).join("\n");
}

function onNodeClick(item: TestItem) {
  if (item.path) emit("open-file", item.path, item.line);
}

watch(
  () => [selected.value ? testProviderKey(selected.value) : "", store.revision, props.projectRoot, locale.value],
  () => void discover(),
  { immediate: true },
);

onBeforeUnmount(() => {
  generation.value += 1;
  abortAll();
});
</script>

<template>
  <section class="project-plugin-tests-panel" :aria-label="t('pluginContrib.testProviders.tab')">
    <header class="project-plugin-tests-panel__toolbar">
      <el-select
        v-if="options.length > 1"
        :model-value="selected ? testProviderKey(selected) : undefined"
        size="small"
        :aria-label="t('pluginContrib.testProviders.select')"
        @update:model-value="(value: string) => (selectedKey = value)"
      >
        <el-option v-for="option in options" :key="option.key" :value="option.key" :label="option.label" />
      </el-select>
      <span v-else-if="selected" class="project-plugin-tests-panel__title" :title="selected.pluginName">
        {{ testProviderTitle(selected, localeChain) }}
      </span>
      <template v-if="streaming">
        <el-tooltip :content="t('pluginContrib.testProviders.runAll')" placement="bottom">
          <el-button
            text
            native-type="button"
            data-action="run-all"
            :aria-label="t('pluginContrib.testProviders.runAll')"
            :disabled="!discovered || streamBusy"
            @click="runStreaming([], false)"
          >
            <el-icon><VideoPlay /></el-icon>
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('pluginContrib.testProviders.debugAll')" placement="bottom">
          <el-button
            text
            native-type="button"
            data-action="debug-all"
            :aria-label="t('pluginContrib.testProviders.debugAll')"
            :disabled="!discovered || streamBusy"
            @click="runStreaming([], true)"
          >
            <el-icon>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
                <rect x="7" y="8" width="10" height="12" rx="5" />
                <path d="M9 8a3 3 0 0 1 6 0M12 12v8M3 13h4M17 13h4M4 7l3 2M20 7l-3 2M4 20l3-2M20 20l-3-2" />
              </svg>
            </el-icon>
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('pluginContrib.testProviders.rerunFailed')" placement="bottom">
          <el-button
            text
            native-type="button"
            data-action="rerun-failed"
            :aria-label="t('pluginContrib.testProviders.rerunFailed')"
            :disabled="!rerunIds.length || streamBusy"
            @click="runStreaming(rerunIds, Boolean(currentRun?.debug))"
          >
            <el-icon><RefreshLeft /></el-icon>
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('pluginContrib.testProviders.stop')" placement="bottom">
          <el-button
            text
            native-type="button"
            data-action="stop"
            class="project-plugin-tests-panel__stop"
            :aria-label="t('pluginContrib.testProviders.stop')"
            :disabled="!runActive || Boolean(currentRun?.stopping)"
            @click="stopRun"
          >
            <el-icon>
              <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="1.5" /></svg>
            </el-icon>
          </el-button>
        </el-tooltip>
      </template>
      <el-tooltip v-else :content="t('pluginContrib.testProviders.runAll')" placement="bottom">
        <el-button
          text
          native-type="button"
          :aria-label="t('pluginContrib.testProviders.runAll')"
          :loading="running"
          :disabled="!discovered || discovering"
          @click="run([])"
        >
          <el-icon><VideoPlay /></el-icon>
        </el-button>
      </el-tooltip>
      <el-tooltip :content="t('pluginContrib.testProviders.refresh')" placement="bottom">
        <el-button
          text
          native-type="button"
          :aria-label="t('pluginContrib.testProviders.refresh')"
          :loading="discovering"
          :disabled="!selected || running || runActive || launching"
          @click="discover(true)"
        >
          <el-icon><Refresh /></el-icon>
        </el-button>
      </el-tooltip>
    </header>

    <ProjectTestRunResults
      v-if="showResults"
      class="project-plugin-tests-panel__results"
      :run="shownRun"
      :tests="tests"
      :project-root="projectRoot"
      :busy="streamBusy"
      :notice="notice"
      @run="runStreaming"
      @open-file="(path: string, line: number | undefined) => emit('open-file', path, line)"
      @open-address="openAddress"
      @dismiss="dismissedRunId = shownRun?.runId"
    />

    <div v-if="hasResults && !streaming" class="project-plugin-tests-panel__summary" aria-live="polite">
      <span class="is-passed">{{ t("pluginContrib.testProviders.summary.passed", { count: summary.passed }) }}</span>
      <span class="is-failed">{{ t("pluginContrib.testProviders.summary.failed", { count: summary.failed + summary.errored }) }}</span>
      <span class="is-skipped">{{ t("pluginContrib.testProviders.summary.skipped", { count: summary.skipped }) }}</span>
    </div>

    <el-scrollbar v-if="!showResults" class="project-plugin-tests-panel__body">
      <p v-if="!selected" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("pluginContrib.testProviders.none") }}</p>
      <p v-else-if="error" class="project-sidebar-empty project-sidebar-empty--compact" dir="auto">
        {{ t("pluginContrib.testProviders.discoverFailed", { error }) }}
      </p>
      <p v-else-if="discovering" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("pluginContrib.testProviders.discovering") }}</p>
      <p v-else-if="!tests.length" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("pluginContrib.testProviders.empty") }}</p>
      <template v-else>
        <el-tree
          :key="generation"
          class="project-plugin-tests-panel__tree"
          node-key="id"
          :data="tests"
          :props="{ label: 'label', children: 'children' }"
          :expand-on-click-node="false"
          :indent="12"
          default-expand-all
          @node-click="onNodeClick"
        >
          <template #default="{ data }">
            <span class="project-plugin-tests-panel__entry" :title="nodeTitle(data)">
              <span class="project-plugin-tests-panel__node">
                <el-icon
                  v-if="statusOf(data)"
                  class="project-plugin-tests-panel__status"
                  :class="`is-${statusOf(data)}`"
                  :aria-label="t(`pluginContrib.testProviders.status.${statusOf(data) as TestStatus}`)"
                >
                  <component :is="STATUS_ICONS[statusOf(data) as TestStatus]" />
                </el-icon>
                <span v-else class="project-plugin-tests-panel__status is-pending" aria-hidden="true" />
                <span class="project-plugin-tests-panel__label" dir="auto">{{ data.label }}</span>
                <el-button
                  class="project-plugin-tests-panel__run"
                  text
                  size="small"
                  native-type="button"
                  :aria-label="t('pluginContrib.testProviders.runOne', { name: data.label })"
                  :disabled="running || discovering"
                  @click.stop="run([data.id])"
                >
                  <el-icon><VideoPlay /></el-icon>
                </el-button>
              </span>
              <span v-if="failureText(data)" class="project-plugin-tests-panel__message" dir="auto">{{ failureText(data) }}</span>
            </span>
          </template>
        </el-tree>
        <p v-if="notice" class="project-plugin-tests-panel__notice">{{ notice }}</p>
      </template>
    </el-scrollbar>

    <details v-if="output" class="project-plugin-tests-panel__output">
      <summary>
        {{ t("pluginContrib.testProviders.output") }}
        <small v-if="outputTruncated">{{ t("pluginContrib.testProviders.outputTruncated") }}</small>
      </summary>
      <pre dir="auto">{{ output }}</pre>
    </details>
  </section>
</template>

<style scoped>
.project-plugin-tests-panel {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}

.project-plugin-tests-panel__toolbar {
  display: flex;
  align-items: center;
  gap: 4px;
  min-height: 32px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--app-border);
}

.project-plugin-tests-panel__toolbar .el-button + .el-button {
  margin-left: 0;
}

.project-plugin-tests-panel__toolbar .el-select {
  min-width: 0;
  flex: 1;
}

.project-plugin-tests-panel__title {
  overflow: hidden;
  min-width: 0;
  flex: 1;
  color: var(--app-text);
  font-size: 12px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-plugin-tests-panel__summary {
  display: flex;
  gap: 10px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--app-border);
  font-size: 11px;
}

.project-plugin-tests-panel__summary .is-passed {
  color: var(--el-color-success);
}

.project-plugin-tests-panel__summary .is-failed {
  color: var(--el-color-danger);
}

.project-plugin-tests-panel__summary .is-skipped {
  color: var(--app-text-muted);
}

.project-plugin-tests-panel__body,
.project-plugin-tests-panel__results {
  min-height: 0;
  flex: 1;
}

.project-plugin-tests-panel__tree {
  background: transparent;
}

/* A failure message makes a node two or three lines tall. */
.project-plugin-tests-panel__tree :deep(.el-tree-node__content) {
  height: auto;
  min-height: 26px;
}

.project-plugin-tests-panel__entry {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
}

.project-plugin-tests-panel__message {
  display: -webkit-box;
  overflow: hidden;
  margin: 0 4px 4px 20px;
  color: var(--el-color-danger);
  font-family: var(--app-font-mono, monospace);
  font-size: 11px;
  line-height: 1.45;
  white-space: pre-wrap;
  word-break: break-word;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
  line-clamp: 3;
}

.project-plugin-tests-panel__node {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 6px;
  padding-right: 4px;
  font-size: 12px;
}

.project-plugin-tests-panel__status {
  display: inline-flex;
  width: 14px;
  height: 14px;
  flex: none;
  color: var(--app-text-muted);
  font-size: 14px;
}

.project-plugin-tests-panel__status.is-pending::before {
  width: 6px;
  height: 6px;
  margin: auto;
  border-radius: 50%;
  background: var(--app-border);
  content: "";
}

.project-plugin-tests-panel__status.is-passed {
  color: var(--el-color-success);
}

.project-plugin-tests-panel__status.is-failed,
.project-plugin-tests-panel__status.is-errored {
  color: var(--el-color-danger);
}

.project-plugin-tests-panel__label {
  overflow: hidden;
  min-width: 0;
  flex: 1;
  color: var(--app-text);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-plugin-tests-panel__run {
  visibility: hidden;
  height: 20px;
  padding: 0 4px;
}

.project-plugin-tests-panel__node:hover .project-plugin-tests-panel__run,
.project-plugin-tests-panel__run:focus-visible {
  visibility: visible;
}

.project-plugin-tests-panel__notice {
  margin: 4px 8px;
  color: var(--app-text-muted);
  font-size: 11px;
}

.project-plugin-tests-panel__output {
  max-height: 40%;
  overflow: auto;
  border-top: 1px solid var(--app-border);
  font-size: 11px;
}

.project-plugin-tests-panel__output summary {
  padding: 4px 8px;
  color: var(--app-text);
  cursor: pointer;
}

.project-plugin-tests-panel__output small {
  margin-left: 6px;
  color: var(--app-text-muted);
}

.project-plugin-tests-panel__output pre {
  margin: 0;
  padding: 4px 8px 8px;
  color: var(--app-text);
  font-family: var(--app-font-mono, monospace);
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
