<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import { Refresh } from "@element-plus/icons-vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import PluginCliCard from "@/components/plugins/development/PluginCliCard.vue";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { declarativeContributionCounts, declarativeContributionIssues, filterDevelopmentCards, isDevelopmentCardActive, type DevelopmentStatusFilter } from "@/domain/plugins/development-summary";
import {
  listPluginDevelopmentRunners,
  PLUGIN_DEVELOPMENT_SCAN_MAX_PROJECTS,
  scanPluginDevelopmentProjects,
  startPluginDevelopmentProject,
  stopPluginDevelopmentProject,
  type PluginDevelopmentProject,
  type PluginDevelopmentRunnerState,
  approvePluginDevelopmentPairing,
  disconnectPluginDevelopmentSession,
  listPluginDevelopmentPairings,
  listPluginDevelopmentSessions,
  listPluginViews,
  rejectPluginDevelopmentPairing,
  type PluginDevelopmentPairing,
  type PluginDevelopmentSession,
} from "@/services/plugin-service";
import { localizedPluginTitle } from "@/services/plugin-titles";
import { openPluginViewTab } from "@/domain/workspace/open-plugin-view";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceScopedPersistence } from "@/composables/use-workspace-state-scope";
import { chooseNativeDirectory, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

const { t } = useVueI18n();
const router = useRouter();
const workbench = useWorkbenchStore();
const projects = ref<PluginDevelopmentProject[]>([]);
const scanRoot = ref("");
const searchQuery = ref("");
const statusFilter = ref<DevelopmentStatusFilter>("all");
const runners = ref<PluginDevelopmentRunnerState[]>([]);
const runnerBusyRoot = ref("");
// 正在弹窗审批的配对 ID：审批逐条进行，同一时间只弹一个确认框。
let promptedPairingId = "";

function runnerFor(project: PluginDevelopmentProject) {
  return runners.value.find((runner) => runner.projectRoot === project.path);
}

function runnerActive(project: PluginDevelopmentProject) {
  const status = runnerFor(project)?.status;
  return status === "starting" || status === "running" || status === "stopping";
}

interface DevelopmentCard {
  key: string;
  path: string;
  pluginName: string;
  pluginId: string;
  description: string;
  project?: PluginDevelopmentProject;
  session?: PluginDevelopmentSession;
  runnerStatus?: PluginDevelopmentRunnerState["status"];
  runnerError?: string;
}

const developmentCards = computed<DevelopmentCard[]>(() => {
  const cards = new Map<string, DevelopmentCard>();
  for (const project of projects.value) {
    cards.set(project.path, {
      key: project.path,
      path: project.path,
      pluginName: project.pluginName,
      pluginId: project.pluginId,
      description: project.description,
      project,
      runnerStatus: runnerFor(project)?.status,
      runnerError: runnerFor(project)?.error,
    });
  }
  for (const session of sessions.value) {
    const existing = cards.get(session.projectRoot);
    cards.set(session.projectRoot, {
      key: existing?.key ?? (session.projectRoot || session.id),
      path: session.projectRoot,
      pluginName: session.pluginName || existing?.pluginName || session.pluginId,
      pluginId: session.pluginId || existing?.pluginId || "",
      description: existing?.description || "",
      project: existing?.project,
      session,
      runnerStatus: existing?.runnerStatus,
      runnerError: existing?.runnerError,
    });
  }
  return [...cards.values()];
});

const runningCardCount = computed(() => developmentCards.value.filter(isDevelopmentCardActive).length);
const statusOptions = computed(() => [
  { value: "all", label: t("plugins.development.status.all", { count: developmentCards.value.length }) },
  { value: "running", label: t("plugins.development.status.running", { count: runningCardCount.value }) },
  { value: "notRunning", label: t("plugins.development.status.notRunning", { count: developmentCards.value.length - runningCardCount.value }) },
]);
const filteredDevelopmentCards = computed(() =>
  filterDevelopmentCards(developmentCards.value, statusFilter.value, searchQuery.value)
);

async function refreshRunners() {
  if (!isDesktopRuntime()) return;
  runners.value = await listPluginDevelopmentRunners();
}

async function toggleProject(project: PluginDevelopmentProject) {
  if (runnerBusyRoot.value || (project.kind !== "source" && project.kind !== "package") || project.status === "error") return;
  runnerBusyRoot.value = project.path;
  try {
    if (runnerActive(project)) {
      await stopPluginDevelopmentProject(project.path);
    } else {
      await startPluginDevelopmentProject(project.path);
      ElMessage.info(t("plugins.development.startRequested"));
    }
    await refresh();
  } catch (cause) {
    ElMessage.error(String(cause));
  } finally {
    runnerBusyRoot.value = "";
  }
}

// 声明式（纯数据）插件没有进程、激活方式或视图，卡片只展示各贡献点的条目数与校验问题。
function isDeclarativeSession(session: PluginDevelopmentSession) {
  return session.manifest.runtimeType === "declarative";
}

const contributionCountKeys: Record<string, MessageKey> = {
  keymaps: "plugins.development.keymapCount",
  themes: "plugins.development.themeCount",
  snippets: "plugins.development.snippetCount",
  languages: "plugins.development.languageCount",
  aiPrompts: "plugins.development.aiPromptCount",
};

function contributionCountLabel(point: string, count: number) {
  const key = contributionCountKeys[point];
  return key ? t(key, { count }) : t("plugins.development.contributionCount", { point, count });
}

async function openSessionView(session: PluginDevelopmentSession) {
  try {
    const views = await listPluginViews();
    const view = views.find((item) => item.pluginId === session.pluginId);
    if (!view || disposed) {
      if (!disposed) ElMessage.warning(t("plugins.development.openUnavailable"));
      return;
    }
    openPluginViewTab(workbench, router, view.pluginId, view.id, localizedPluginTitle(view));
  } catch (cause) {
    if (!disposed) ElMessage.error(String(cause));
  }
}
const scanning = ref(false);
const scanError = ref("");
const scanned = ref(false);

// 扫描目录与结果属于工作区运行时状态：切换标签页组件卸载后恢复，避免重复扫描。
interface PluginDevelopmentScanState {
  scanRoot: string;
  projects: PluginDevelopmentProject[];
}
const scanPersistence = useWorkspaceScopedPersistence<PluginDevelopmentScanState>("plugin-development-scan");

function sanitizeProjects(value: unknown): PluginDevelopmentProject[] {
  if (!Array.isArray(value)) return [];
  const projects: PluginDevelopmentProject[] = [];
  for (const item of value.slice(0, PLUGIN_DEVELOPMENT_SCAN_MAX_PROJECTS)) {
    if (!item || typeof item !== "object") continue;
    const candidate = item as Partial<PluginDevelopmentProject>;
    if (typeof candidate.path !== "string" || !candidate.path) continue;
    projects.push({
      path: candidate.path,
      manifestPath: typeof candidate.manifestPath === "string" ? candidate.manifestPath : "",
      packageRoot: typeof candidate.packageRoot === "string" ? candidate.packageRoot : "",
      pluginId: typeof candidate.pluginId === "string" ? candidate.pluginId : "",
      pluginName: typeof candidate.pluginName === "string" ? candidate.pluginName : candidate.path,
      description: typeof candidate.description === "string" ? candidate.description : "",
      version: typeof candidate.version === "string" ? candidate.version : "",
      kind: candidate.kind === "source" || candidate.kind === "package" ? candidate.kind : "unknown",
      status: candidate.status === "error" ? "error" : "discovered",
      error: typeof candidate.error === "string" ? candidate.error : "",
    });
  }
  return projects;
}

async function restoreScanState() {
  const stored = await scanPersistence.load();
  if (disposed || !stored || typeof stored !== "object") return;
  if (typeof stored.scanRoot === "string") scanRoot.value = stored.scanRoot;
  const restored = sanitizeProjects(stored.projects);
  if (restored.length) {
    projects.value = restored;
    scanned.value = true;
  }
}

async function scanProjects(selectDirectory = true) {
  if (scanning.value || !isDesktopRuntime()) return;
  const root = selectDirectory
    ? await chooseNativeDirectory(t("plugins.development.chooseDirectory"), scanRoot.value)
    : scanRoot.value;
  if (!root || disposed) return;

  scanning.value = true;
  scanError.value = "";
  try {
    const [result, states] = await Promise.all([
      scanPluginDevelopmentProjects(root),
      listPluginDevelopmentRunners(),
    ]);
    if (disposed) return;
    scanRoot.value = root;
    projects.value = result;
    runners.value = states;
    scanned.value = true;
    void scanPersistence.save({ scanRoot: root, projects: result });
  } catch (cause) {
    if (!disposed) scanError.value = String(cause);
  } finally {
    if (!disposed) scanning.value = false;
  }
}

async function refreshProjects() {
  await scanProjects(false);
  await refresh();
}

const pairings = ref<PluginDevelopmentPairing[]>([]);
const sessions = ref<PluginDevelopmentSession[]>([]);
const loading = ref(false);
const busyId = ref("");
const error = ref("");
let disposed = false;
let generation = 0;
let refreshPending = false;
let refreshTimer: ReturnType<typeof setInterval> | undefined;
let stopPairing: (() => void) | undefined;
let stopChanged: (() => void) | undefined;
let stopRunnerChanged: (() => void) | undefined;

async function refresh() {
  if (disposed) return;
  if (loading.value) {
    refreshPending = true;
    return;
  }
  if (!isDesktopRuntime()) {
    error.value = t("common.desktopRequired");
    return;
  }
  loading.value = true;
  error.value = "";
  const current = ++generation;
  try {
    const [nextPairings, nextSessions, nextRunners] = await Promise.all([
      listPluginDevelopmentPairings(),
      listPluginDevelopmentSessions(),
      listPluginDevelopmentRunners(),
    ]);
    if (disposed || current !== generation) return;
    pairings.value = nextPairings;
    sessions.value = nextSessions;
    runners.value = nextRunners;
    promptNextPairing();
  } catch (cause) {
    if (!disposed && current === generation) error.value = String(cause);
  } finally {
    if (!disposed && current === generation) {
      loading.value = false;
      if (refreshPending) {
        refreshPending = false;
        void refresh();
      }
    }
  }
}

async function confirmPairing(pairing: PluginDevelopmentPairing) {
  let approved = false;
  try {
    await ElMessageBox.confirm(
      t("plugins.development.approveDescription", {
        path: pairing.projectRoot,
        id: pairing.pluginId,
        capabilities: pairing.capabilities.length ? pairing.capabilities.join(", ") : t("plugins.noCapabilities"),
      }),
      t("plugins.development.approveTitle"),
      {
        type: "warning",
        confirmButtonText: t("plugins.development.approve"),
        cancelButtonText: t("common.cancel"),
      },
    );
    approved = true;
  } catch {
    // 用户取消确认框即拒绝，配对随之关闭。
  }
  // The user's decision must reach the backend even if the panel unmounted meanwhile.
  if (!disposed) busyId.value = pairing.id;
  try {
    if (approved) {
      await approvePluginDevelopmentPairing(pairing.id);
      ElMessage.success(t("plugins.development.approved"));
    } else {
      await rejectPluginDevelopmentPairing(pairing.id);
      ElMessage.info(t("plugins.development.rejected"));
    }
  } catch (cause) {
    ElMessage.error(String(cause));
  } finally {
    if (!disposed) busyId.value = "";
    if (promptedPairingId === pairing.id) promptedPairingId = "";
  }
  await refresh();
}

// 新的配对连接统一走弹窗审批：批准后建立会话，取消即拒绝并关闭。
function promptNextPairing() {
  if (promptedPairingId || disposed) return;
  const pairing = pairings.value[0];
  if (!pairing) return;
  promptedPairingId = pairing.id;
  void confirmPairing(pairing);
}

async function disconnect(session: PluginDevelopmentSession) {
  if (busyId.value) return;
  busyId.value = session.id;
  try {
    await disconnectPluginDevelopmentSession(session.id);
    await refresh();
    await refreshRunners();
  } catch (cause) {
    ElMessage.error(String(cause));
  } finally {
    busyId.value = "";
  }
}

onMounted(() => {
  if (isDesktopRuntime()) {
    stopPairing = listenAppEvent("plugin:development-pairing", () => { void refresh(); });
    stopChanged = listenAppEvent("plugin:development-changed", () => { void refresh(); });
    stopRunnerChanged = listenAppEvent("plugin:development-runner-changed", () => { void refresh(); });
    refreshTimer = setInterval(() => {
      if (!document.hidden) void refresh();
    }, 2000);
  }
  void refresh();
  void restoreScanState();
});

onBeforeUnmount(() => {
  disposed = true;
  generation++;
  if (refreshTimer) clearInterval(refreshTimer);
  stopPairing?.();
  stopChanged?.();
  stopRunnerChanged?.();
});
</script>

<template>
  <el-scrollbar class="plugin-development-panel">
    <div class="plugin-development-panel__content" :aria-busy="loading">
      <header class="plugin-development-panel__header">
        <div>
          <h2>{{ t("plugins.development.title") }}</h2>
          <p>{{ t("plugins.development.description") }}</p>
        </div>
        <el-button
          :icon="Refresh"
          :loading="loading || scanning"
          :disabled="!isDesktopRuntime() || !scanRoot"
          :aria-label="t('common.refresh')"
          :title="t('common.refresh')"
          @click="refreshProjects"
        >
          {{ t("common.refresh") }}
        </el-button>
      </header>

      <el-alert v-if="error" type="error" :title="error" :closable="false" show-icon />

      <PluginCliCard />

      <section class="plugin-development-panel__section" aria-labelledby="plugin-development-projects" :aria-busy="scanning || loading">
        <div class="plugin-development-panel__toolbar">
          <div class="plugin-development-panel__section-title">
            <h3 id="plugin-development-projects">{{ t("plugins.development.projects") }}</h3>
            <el-tag size="small" effect="plain">{{ developmentCards.length }}</el-tag>
          </div>
          <div class="plugin-development-panel__toolbar-actions">
            <el-radio-group
              v-model="statusFilter"
              :aria-label="t('plugins.development.status.label')"
            >
              <el-radio-button v-for="option in statusOptions" :key="option.value" :value="option.value">
                {{ option.label }}
              </el-radio-button>
            </el-radio-group>
            <AppSearchField
              v-model="searchQuery"
              clearable
              :placeholder="t('plugins.development.searchPlaceholder')"
              :clear-label="t('common.clear')"
            />
            <el-button :loading="scanning" :disabled="!isDesktopRuntime()" @click="scanProjects">
              {{ t("plugins.development.chooseDirectory") }}
            </el-button>
          </div>
        </div>
        <p v-if="scanRoot" class="plugin-development-panel__path" :title="scanRoot">{{ scanRoot }}</p>
        <el-alert v-if="scanError" type="error" :title="scanError" :closable="false" show-icon />
        <el-empty
          v-if="!scanning && !loading && !developmentCards.length"
          :image-size="72"
          :description="t(scanned ? 'plugins.development.projectsEmpty' : 'plugins.development.projectsHint')"
        />
        <el-empty
          v-else-if="!scanning && !loading && !filteredDevelopmentCards.length"
          :image-size="72"
          :description="t('plugins.development.searchEmpty')"
        />
        <div v-else class="plugin-development-panel__grid">
          <el-card
            v-for="card in filteredDevelopmentCards"
            :key="card.key"
            shadow="never"
            class="plugin-development-panel__card"
          >
            <div class="plugin-development-panel__card-body">
              <div class="plugin-development-panel__card-info">
                <div class="plugin-development-panel__identity">
                  <strong>{{ card.pluginName }}</strong>
                  <code v-if="card.pluginId">{{ card.pluginId }}</code>
                </div>
                <p class="plugin-development-panel__path" :title="card.path">{{ card.path }}</p>
                <p v-if="card.description" class="plugin-development-panel__desc" :title="card.description">{{ card.description }}</p>
                <el-alert
                  v-if="card.project?.status === 'error'"
                  type="error"
                  :title="card.project.error"
                  :closable="false"
                />
                <el-alert v-if="card.runnerError" type="error" :title="card.runnerError" :closable="false" show-icon />
                <template v-if="card.session">
                  <div class="plugin-development-panel__session-summary">
                    <span>{{ t("plugins.development.generation", { generation: card.session.generation }) }}</span>
                    <span>v{{ card.session.manifest.apiVersion }}</span>
                    <template v-if="isDeclarativeSession(card.session)">
                      <span>{{ t("plugins.development.declarativeRuntime") }}</span>
                      <span v-for="item in declarativeContributionCounts(card.session.manifest)" :key="item.point">
                        {{ contributionCountLabel(item.point, item.count) }}
                      </span>
                    </template>
                    <template v-else>
                      <span>{{ card.session.manifest.runtimeType }} · {{ card.session.manifest.activation }}</span>
                      <span>{{ card.session.uiUrl ? t("plugins.development.hmrEnabled") : t("plugins.development.packagedUi") }}</span>
                    </template>
                  </div>
                  <el-alert
                    v-if="isDeclarativeSession(card.session) && declarativeContributionIssues(card.session.manifest).length"
                    type="warning"
                    :title="t('plugins.development.contributionIssues', { count: declarativeContributionIssues(card.session.manifest).length })"
                    :closable="false"
                  >
                    <ul class="plugin-development-panel__issues">
                      <li v-for="(issue, index) in declarativeContributionIssues(card.session.manifest)" :key="index">{{ issue }}</li>
                    </ul>
                  </el-alert>
                  <el-alert
                    v-if="card.session.buildError"
                    type="error"
                    :title="t('plugins.development.buildFailed')"
                    :description="card.session.buildError"
                    :closable="false"
                  />
                  <details v-if="card.session.logs.length" class="plugin-development-panel__logs">
                    <summary>{{ t("plugins.development.logs", { count: card.session.logs.length }) }}</summary>
                    <el-scrollbar max-height="180px">
                      <ol aria-live="polite">
                        <li v-for="(log, index) in card.session.logs" :key="`${log.createdAt}-${index}`" :class="`is-${log.level}`">
                          <time>{{ log.createdAt }}</time>
                          <strong>{{ log.level }}</strong>
                          <span>{{ log.message }}</span>
                        </li>
                      </ol>
                    </el-scrollbar>
                  </details>
                </template>
              </div>
              <div class="plugin-development-panel__card-side">
                <div class="plugin-development-panel__status">
                  <el-tag
                    v-if="card.session"
                    size="small"
                    type="success"
                    effect="plain"
                  >
                    {{ t("plugins.development.connected") }}
                  </el-tag>
                  <el-tag v-if="card.project" size="small" effect="plain">
                    {{ t(card.project.kind === 'source' ? 'plugins.development.kind.source' : card.project.kind === 'package' ? 'plugins.development.kind.package' : 'plugins.development.kind.unknown') }}
                  </el-tag>
                </div>
                <div class="plugin-development-panel__actions">
                  <el-button
                    v-if="card.session && card.session.manifest.viewCount > 0"
                    size="small"
                    type="primary"
                    plain
                    :disabled="!!busyId"
                    @click="openSessionView(card.session)"
                  >
                    {{ t("plugins.openView") }}
                  </el-button>
                  <el-button
                    v-if="card.session"
                    size="small"
                    :loading="busyId === card.session.id"
                    :disabled="!!busyId && busyId !== card.session.id"
                    @click="disconnect(card.session)"
                  >
                    {{ t("plugins.development.disconnect") }}
                  </el-button>
                  <el-button
                    v-if="card.project && (card.project.kind === 'source' || card.project.kind === 'package') && card.project.status !== 'error'"
                    size="small"
                    :type="runnerActive(card.project) ? 'danger' : 'primary'"
                    plain
                    :loading="runnerBusyRoot === card.path || runnerFor(card.project)?.status === 'starting' || runnerFor(card.project)?.status === 'stopping'"
                    @click="toggleProject(card.project)"
                  >
                    {{ runnerActive(card.project) ? t("plugins.development.stop") : t("plugins.development.start") }}
                  </el-button>
                </div>
              </div>
            </div>
          </el-card>
        </div>
      </section>
    </div>
  </el-scrollbar>
</template>
