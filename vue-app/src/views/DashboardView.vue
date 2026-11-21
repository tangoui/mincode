<script setup lang="ts">
import { computed, defineAsyncComponent, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, shallowRef } from "vue";
import { useRouter } from "vue-router";
import {
  ArrowRight,
  Clock,
  Close,
  Connection,
  DataAnalysis,
  Link,
  Monitor,
  Promotion,
  Refresh,
} from "@element-plus/icons-vue";
import MinCodeMark from "@/assets/brand/mincode-mark.svg";
import { useVueI18n } from "@/i18n/messages";
import type { MessageKey } from "@/i18n/messages";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { scheduleIdleTask } from "@/services/idle-task";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { syncSessionToolRoots } from "@/services/session-tool-preferences";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useWorkbenchStore } from "@/stores/workbench";
import type { WorkspaceTab, WorkspaceView } from "@/types/workspace";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { formatDashboardClock, greetingKeyForHour, msUntilNextSecond } from "@/domain/dashboard/clock";

// Plugin contributions (walkthroughs, dashboardWidgets) load lazily: they render nothing without plugins.
const PluginGetStartedCard = defineAsyncComponent(() => import("@/components/dashboard/PluginGetStartedCard.vue"));
const PluginDashboardWidgets = defineAsyncComponent(() => import("@/components/dashboard/PluginDashboardWidgets.vue"));

interface RecentSession {
  id?: string;
  uuid?: string;
  title?: string;
  cwd?: string;
  decodedPath?: string;
  lastAt?: number;
  messageCount?: number;
  model?: string;
}

interface ClaudeProject {
  encodedDir: string;
  decodedPath: string;
  sessionCount: number;
  lastAt: number;
}

interface ActivityItem {
  id: string;
  provider: "Codex" | "Claude";
  title: string;
  path: string;
  lastAt: number;
  count: number;
}

type EnvironmentCheckId = "shell" | "git" | "node" | "packageManager" | "docker";

interface EnvironmentCheck {
  id: EnvironmentCheckId;
  available: boolean;
  command?: string;
  path?: string;
}

interface EnvironmentStatus {
  platform: string;
  architecture: string;
  checks: EnvironmentCheck[];
}

interface CliToolInfo {
  name: string;
  version: string;
  installed: boolean;
}

interface EnvironmentRow {
  id: string;
  label: string;
  value: string;
  ready: boolean;
  detail?: string;
}

const router = useRouter();
const workbench = useWorkbenchStore();
const database = useDatabaseWorkspaceStore();
const ssh = useSshConnectionsStore();
const { t, intlLocale } = useVueI18n();

const loading = ref(false);
// Probes are deferred past the first paint; start in the loading state so the card reads "checking".
const environmentLoading = ref(isDesktopRuntime());
const environmentStatus = ref<EnvironmentStatus | null>(null);
const cliTools = ref<CliToolInfo[]>([]);
const recentActivity = ref<ActivityItem[]>([]);
const recentLoading = ref(isDesktopRuntime());
const refreshedAt = ref(0);
const DASHBOARD_REFRESH_TTL_MS = 5 * 60 * 1000;
let refreshPromise: Promise<void> | null = null;

// Live clock: ticks on each whole second while the dashboard is the active route, so
// the time, the date and the greeting all follow the system clock.
const now = shallowRef(new Date());
let clockTimer: ReturnType<typeof setTimeout> | undefined;
function tick() {
  now.value = new Date();
  clockTimer = setTimeout(tick, msUntilNextSecond(now.value));
}
function startClock() {
  stopClock();
  tick();
}
function stopClock() {
  clearTimeout(clockTimer);
  clockTimer = undefined;
}
const clock = computed(() => formatDashboardClock(now.value, intlLocale.value));
const greeting = computed(() => t(greetingKeyForHour(now.value.getHours())));

const activeTabs = computed(() =>
  [...workbench.workspaceTabs].sort(
    (left, right) => Number(Boolean(right.pinned)) - Number(Boolean(left.pinned))
  )
);
const connectedDatabaseCount = computed(() => database.connectedConnections.length);
const environmentCheckLabels: Record<EnvironmentCheckId, MessageKey> = {
  shell: "dashboard.environment.shell",
  git: "dashboard.environment.git",
  node: "dashboard.environment.node",
  packageManager: "dashboard.environment.packageManager",
  docker: "dashboard.environment.docker",
};
const environmentRows = computed<EnvironmentRow[]>(() => {
  const desktopRow = {
    id: "desktop",
    label: t("dashboard.environment.desktop"),
    value: isDesktopRuntime() ? t("dashboard.environment.ready") : t("dashboard.environment.browser"),
    ready: isDesktopRuntime(),
  };

  if (!isDesktopRuntime()) return [desktopRow];
  if (environmentLoading.value && !environmentStatus.value) {
    return [
      desktopRow,
      {
        id: "checking",
        label: t("dashboard.environment.tools"),
        value: t("dashboard.environment.checking"),
        ready: false,
      },
    ];
  }
  if (!environmentStatus.value) {
    return [
      desktopRow,
      {
        id: "unavailable",
        label: t("dashboard.environment.tools"),
        value: t("dashboard.environment.checkFailed"),
        ready: false,
      },
    ];
  }

  return [
    desktopRow,
    ...environmentStatus.value.checks.map((check) => ({
      id: check.id,
      label: t(environmentCheckLabels[check.id]),
      value: check.available
        ? t("dashboard.environment.available", { command: check.command ?? check.id })
        : t("dashboard.environment.unavailable"),
      ready: check.available,
      detail: check.path,
    })),
    ...cliTools.value.map((tool) => ({
      id: `cli:${tool.name}`,
      label: tool.name,
      value: tool.installed ? tool.version : t("dashboard.environment.unavailable"),
      ready: tool.installed,
      detail: tool.version,
    })),
  ];
});

const quickActions: Array<{
  view: WorkspaceView;
  titleKey: MessageKey;
  descriptionKey: MessageKey;
  icon: typeof Monitor;
  tone: string;
}> = [
  {
    view: "terminal",
    titleKey: "dashboard.quick.terminal",
    descriptionKey: "dashboard.quick.terminalHint",
    icon: Monitor,
    tone: "teal",
  },
  {
    view: "ssh",
    titleKey: "dashboard.quick.ssh",
    descriptionKey: "dashboard.quick.sshHint",
    icon: Connection,
    tone: "green",
  },
  {
    view: "database",
    titleKey: "dashboard.quick.database",
    descriptionKey: "dashboard.quick.databaseHint",
    icon: DataAnalysis,
    tone: "orange",
  },
];

function basename(path: string) {
  return (
    path
      .replace(/[\\/]+$/, "")
      .split(/[\\/]/)
      .filter(Boolean)
      .pop() ?? ""
  );
}

function formatRelativeTime(timestamp: number) {
  if (!timestamp) return t("dashboard.time.unknown");
  const elapsed = Math.max(0, Date.now() - timestamp);
  if (elapsed < 60_000) return t("dashboard.time.justNow");
  if (elapsed < 3_600_000)
    return t("dashboard.time.minutes", { count: Math.floor(elapsed / 60_000) });
  if (elapsed < 86_400_000)
    return t("dashboard.time.hours", { count: Math.floor(elapsed / 3_600_000) });
  return t("dashboard.time.days", { count: Math.floor(elapsed / 86_400_000) });
}

function tabTitle(tab: WorkspaceTab) {
  return tab.title?.trim() || t(tab.titleKey as MessageKey);
}

function tabKindLabel(tab: WorkspaceTab) {
  if (tab.view === "plugin-view") return t("nav.plugins");
  if (tab.view === "service-result") return t("plugins.services.result.title");
  return t(`nav.${tab.view}`);
}

function openView(view: WorkspaceView) {
  if (!workbench.openView(view) && view !== "dashboard") return;
  void router.push({ name: view });
}

function selectTab(tab: WorkspaceTab) {
  workbench.selectTab(tab.id);
  void router.push(workspaceTabRoute(tab));
}

function closeTab(tab: WorkspaceTab) {
  const wasActive = workbench.activeTabId === tab.id;
  workbench.closeTab(tab.id, true);
  if (!wasActive) return;
  const next = workbench.activeTab;
  void router.push(workspaceTabRoute(next));
}

async function loadEnvironmentChecks(force = false) {
  if (!isDesktopRuntime()) {
    environmentStatus.value = null;
    cliTools.value = [];
    return;
  }

  environmentLoading.value = true;
  try {
    const [status, tools] = await Promise.all([
      invokeBackend<EnvironmentStatus>("system_get_environment_checks", {}),
      invokeBackend<CliToolInfo[]>("detect_cli_tools", { force }).catch(() => []),
    ]);
    environmentStatus.value = status;
    cliTools.value = tools;
  } catch {
    environmentStatus.value = null;
    cliTools.value = [];
  } finally {
    environmentLoading.value = false;
  }
}

async function loadRecentActivity() {
  if (!isDesktopRuntime()) {
    recentActivity.value = [];
    recentLoading.value = false;
    return;
  }
  recentLoading.value = true;
  await syncSessionToolRoots();
  const [codexSessions, claudeProjects] = await Promise.all([
    invokeBackend<RecentSession[]>("list_codex_sessions", {}).catch(() => []),
    invokeBackend<ClaudeProject[]>("list_claude_projects", {}).catch(() => []),
  ]).finally(() => {
    recentLoading.value = false;
  });
  recentActivity.value = [
    ...codexSessions.map(
      (session, index): ActivityItem => ({
        id: `codex:${session.uuid ?? session.id ?? index}`,
        provider: "Codex",
        title:
          session.title?.trim() || basename(session.cwd ?? "") || t("dashboard.sessions.untitled"),
        path: session.cwd ?? session.decodedPath ?? "",
        lastAt: session.lastAt ?? 0,
        count: session.messageCount ?? 0,
      })
    ),
    ...claudeProjects.map(
      (item): ActivityItem => ({
        id: `claude:${item.encodedDir}`,
        provider: "Claude",
        title: basename(item.decodedPath) || item.decodedPath,
        path: item.decodedPath,
        lastAt: item.lastAt,
        count: item.sessionCount,
      })
    ),
  ]
    .sort((left, right) => right.lastAt - left.lastAt)
    .slice(0, 5);
}

function refresh(force = false): Promise<void> {
  if (refreshPromise) return refreshPromise;
  if (!force && refreshedAt.value && Date.now() - refreshedAt.value < DASHBOARD_REFRESH_TTL_MS) {
    return Promise.resolve();
  }

  loading.value = true;
  const firstLoad = !refreshedAt.value;
  refreshPromise = Promise.all([
    database.initialize().catch(() => undefined),
    // 仅在 Dashboard 真正激活时读取 SSH 配置，避免非 SSH 用户启动即解密凭据。
    ssh.hydrate(),
    // 环境探测会拉起 shell/git/node/docker 子进程并扫描 ~/.codex、~/.claude；首次激活放到首帧之后。
    (firstLoad ? afterIdle() : Promise.resolve()).then(() =>
      Promise.all([loadEnvironmentChecks(force), loadRecentActivity()])
    ),
  ])
    .then(() => {
      refreshedAt.value = Date.now();
    })
    .finally(() => {
      loading.value = false;
      refreshPromise = null;
    });
  return refreshPromise;
}

function afterIdle() {
  return new Promise<void>((resolve) => {
    scheduleIdleTask(resolve);
  });
}

function handleRefresh() {
  void refresh(true);
}

// keep-alive: plugin widgets load and refresh only while the dashboard is the active route.
const routeActive = ref(true);

onActivated(() => {
  routeActive.value = true;
  startClock();
  void refresh();
});

onDeactivated(() => {
  routeActive.value = false;
  stopClock();
});

// onMounted covers a dashboard rendered outside KeepAlive; startClock is idempotent.
onMounted(startClock);
onBeforeUnmount(stopClock);
</script>

<template>
  <el-scrollbar class="dashboard-workspace">
    <section class="dashboard-shell">
      <header class="dashboard-hero">
        <img class="dashboard-logo" :src="MinCodeMark" alt="" aria-hidden="true" />
        <div class="dashboard-hero__copy">
          <h1>{{ greeting }}</h1>
          <p>
            {{
              t("dashboard.subtitle", { workspace: workbench.activeWorkspace?.name || "MinCode" })
            }}
          </p>
        </div>
        <time class="dashboard-clock" :datetime="clock.iso">
          <strong>{{ clock.time }}</strong>
          <span>{{ clock.date }}</span>
        </time>
        <el-button
          class="dashboard-refresh"
          text
          :icon="Refresh"
          :loading="loading"
          @click="handleRefresh"
        >
          {{ t("common.refresh") }}
        </el-button>
      </header>

      <section class="dashboard-actions" :aria-label="t('dashboard.quick.title')">
        <el-button
          v-for="action in quickActions"
          :key="action.view"
          text
          native-type="button"
          class="dashboard-action"
          @click="openView(action.view)"
        >
          <span class="dashboard-action__icon" :class="`is-${action.tone}`">
            <el-icon><component :is="action.icon" /></el-icon>
          </span>
          <span class="dashboard-action__content">
            <strong>{{ t(action.titleKey) }}</strong>
            <small>{{ t(action.descriptionKey) }}</small>
          </span>
          <el-icon class="dashboard-action__arrow"><ArrowRight /></el-icon>
        </el-button>
      </section>

      <div class="dashboard-grid">
        <div class="dashboard-grid__column dashboard-grid__column--primary">

          <article class="dashboard-card dashboard-card--sessions">
            <header class="dashboard-card__header">
              <div>
                <h2>{{ t("dashboard.active.title") }}</h2>
                <p>{{ t("dashboard.active.description", { count: activeTabs.length }) }}</p>
              </div>
              <el-button text native-type="button" class="dashboard-text-button" @click="openView('terminal')">
                {{ t("dashboard.active.new") }} <el-icon><Promotion /></el-icon>
              </el-button>
            </header>
            <div v-if="activeTabs.length" class="dashboard-session-list">
              <div
                v-for="tab in activeTabs.slice(0, 6)"
                :key="tab.id"
                class="dashboard-session-row"
                :class="{ 'is-active': tab.id === workbench.activeTabId }"
              >
                <el-button
                  text
                  native-type="button"
                  class="dashboard-session-row__select"
                  @click="selectTab(tab)"
                >
                  <span class="dashboard-session-row__dot" />
                  <span class="dashboard-session-row__main">
                    <strong>{{ tabTitle(tab) }}</strong>
                    <small>{{ tabKindLabel(tab) }}</small>
                  </span>
                  <el-tag v-if="tab.pinned" size="small" effect="plain">{{
                    t("dashboard.active.pinned")
                  }}</el-tag>
                </el-button>
                <el-button
                  text
                  native-type="button"
                  class="dashboard-session-row__close"
                  :aria-label="t('common.close')"
                  @click="closeTab(tab)"
                >
                  <el-icon><Close /></el-icon>
                </el-button>
              </div>
            </div>
            <el-button
              v-else
              text
              native-type="button"
              class="dashboard-empty"
              @click="openView('terminal')"
            >
              <el-icon><Monitor /></el-icon>
              <strong>{{ t("dashboard.active.empty") }}</strong>
              <span>{{ t("dashboard.active.emptyHint") }}</span>
            </el-button>
          </article>

          <article class="dashboard-card dashboard-card--recent">
            <header class="dashboard-card__header">
              <div>
                <h2>{{ t("dashboard.sessions.title") }}</h2>
                <p>{{ t("dashboard.sessions.description") }}</p>
              </div>
              <el-button text native-type="button" class="dashboard-text-button" @click="openView('sessions')">
                {{ t("dashboard.viewAll") }} <el-icon><ArrowRight /></el-icon>
              </el-button>
            </header>
            <div v-if="recentActivity.length" class="dashboard-recent-list">
              <el-button
                v-for="item in recentActivity"
                :key="item.id"
                text
                native-type="button"
                @click="openView('sessions')"
              >
                <span
                  class="dashboard-provider"
                  :class="{ 'is-claude': item.provider === 'Claude' }"
                  >{{ item.provider.slice(0, 1) }}</span
                >
                <span class="dashboard-recent-list__main">
                  <strong>{{ item.title }}</strong>
                  <small>{{ item.path || t("dashboard.sessions.noPath") }}</small>
                </span>
                <span class="dashboard-recent-list__meta">
                  <small>{{ formatRelativeTime(item.lastAt) }}</small>
                  <em>{{ t("dashboard.sessions.count", { count: item.count }) }}</em>
                </span>
              </el-button>
            </div>
            <div v-else class="dashboard-card-empty">
              <Clock />
              <span>{{
                !isDesktopRuntime()
                  ? t("common.desktopRequired")
                  : recentLoading
                    ? t("common.loading")
                    : t("dashboard.sessions.empty")
              }}</span>
            </div>
          </article>
        </div>
        <div class="dashboard-grid__column dashboard-grid__column--secondary">
          <PluginGetStartedCard />
          <article class="dashboard-card dashboard-card--connections">
            <header class="dashboard-card__header">
              <div>
                <h2>{{ t("dashboard.connections.title") }}</h2>
                <p>{{ t("dashboard.connections.description") }}</p>
              </div>
              <el-icon class="dashboard-card__header-icon"><Link /></el-icon>
            </header>
            <div class="dashboard-metrics">
              <el-button text native-type="button" @click="openView('ssh')">
                <span class="dashboard-metric__icon is-green"><Connection /></span>
                <span
                  ><strong>{{ ssh.profiles.length }}</strong
                  ><small>{{ t("dashboard.connections.ssh") }}</small></span
                >
                <em>{{ t("dashboard.connections.saved") }}</em>
              </el-button>
              <el-button text native-type="button" @click="openView('database')">
                <span class="dashboard-metric__icon is-orange"><DataAnalysis /></span>
                <span
                  ><strong>{{ database.connections.length }}</strong
                  ><small>{{ t("dashboard.connections.database") }}</small></span
                >
                <em>{{ t("dashboard.connections.connected", { count: connectedDatabaseCount }) }}</em>
              </el-button>
            </div>
          </article>

          <article class="dashboard-card dashboard-card--environment">
            <header class="dashboard-card__header">
              <div>
                <h2>{{ t("dashboard.environment.title") }}</h2>
                <p>{{ t("dashboard.environment.description") }}</p>
              </div>
              <div class="dashboard-card__header-actions">
                <el-button
                  text
                  native-type="button"
                  class="dashboard-text-button"
                  :disabled="!isDesktopRuntime() || environmentLoading"
                  @click="loadEnvironmentChecks(true)"
                >
                  {{ t("dashboard.environment.detectAgain") }} <el-icon><Refresh /></el-icon>
                </el-button>
              </div>
            </header>
            <div class="dashboard-environment-list">
              <div v-for="row in environmentRows" :key="row.id">
                <span class="dashboard-health-dot" :class="{ 'is-ready': row.ready }" />
                <span>{{ row.label }}</span>
                <strong :title="row.detail">{{ row.value }}</strong>
              </div>
            </div>
          </article>
        </div>
      </div>

      <PluginDashboardWidgets :active="routeActive" />

      <footer class="dashboard-footer">
        <span>{{
          t("dashboard.footer", { workspace: workbench.activeWorkspace?.name || "MinCode" })
        }}</span>
        <span>{{ t("dashboard.refreshed", { time: formatRelativeTime(refreshedAt) }) }}</span>
      </footer>
    </section>
  </el-scrollbar>
</template>

<style scoped>
.dashboard-workspace {
  height: 100%;
  min-height: 0;
  background: var(--app-surface);
  color: var(--app-text);
}
.dashboard-shell {
  /* Breakpoints follow the dashboard's own width: docked AI panels or pop-out windows narrow it
     while the window itself stays wide. */
  container: dashboard / inline-size;
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  margin: 0;
  padding: clamp(24px, 2vw, 36px) clamp(24px, 3vw, 56px) clamp(24px, 2vw, 32px);
}
.dashboard-hero {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px 16px;
  margin-bottom: 24px;
}
.dashboard-logo {
  display: block;
  width: 52px;
  height: 52px;
  flex: none;
  object-fit: contain;
}
.dashboard-hero__copy {
  min-width: 0;
  /* Wrap the clock and refresh below the greeting before the text gets squeezed. */
  flex: 1 1 220px;
}
.dashboard-hero h1 {
  margin: 0 0 5px;
  color: var(--app-text);
  font-size: 28px;
  line-height: 1.2;
  letter-spacing: -0.02em;
}
.dashboard-hero p {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 13px;
}
.dashboard-clock {
  display: flex;
  flex: none;
  flex-direction: column;
  align-items: flex-end;
  gap: 3px;
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.3;
}
.dashboard-clock strong {
  color: var(--app-text);
  font-size: 22px;
  font-weight: 500;
  font-variant-numeric: tabular-nums;
  letter-spacing: 0.01em;
}
.dashboard-refresh {
  color: var(--app-text-muted);
}
.dashboard-actions {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.dashboard-action.el-button,
.dashboard-session-row__select.el-button,
.dashboard-empty.el-button,
.dashboard-metrics .el-button,
.dashboard-recent-list .el-button,
.dashboard-project.el-button {
  --el-button-text-color: inherit;
  --el-button-hover-text-color: inherit;
  height: auto;
  margin: 0;
}
/* Element Plus owns the immediate slot wrapper, so scoped styles must cross that boundary. */
.dashboard-action.el-button :deep(> span) {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 13px;
  text-align: left;
}
.dashboard-session-row__select.el-button :deep(> span) {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 10px;
  text-align: left;
}
.dashboard-empty.el-button :deep(> span) {
  display: grid;
  width: 100%;
  min-height: 180px;
  place-items: center;
  align-content: center;
  gap: 6px;
}
.dashboard-metrics .el-button :deep(> span),
.dashboard-recent-list .el-button :deep(> span),
.dashboard-project.el-button :deep(> span) {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 10px;
  text-align: left;
}
.dashboard-action.el-button {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 13px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-surface);
  padding: 14px 15px;
  text-align: left;
  cursor: pointer;
  transition:
    border-color 0.16s,
    background 0.16s;
}
.dashboard-action:hover {
  border-color: color-mix(in srgb, var(--app-accent) 55%, var(--app-surface));
  background: color-mix(in srgb, var(--app-accent) 8%, var(--app-surface));
}
.dashboard-action__icon {
  display: grid;
  width: 38px;
  height: 38px;
  flex: none;
  place-items: center;
  border-radius: 6px;
  font-size: 19px;
}
.dashboard-action__icon.is-teal,
.dashboard-action__icon.is-blue {
  background: color-mix(in srgb, var(--app-accent) 12%, var(--app-surface));
  color: var(--app-accent);
}
.dashboard-action__icon.is-green {
  background: color-mix(in srgb, var(--el-color-success) 14%, var(--app-surface));
  color: var(--el-color-success);
}
.dashboard-action__icon.is-orange {
  background: color-mix(in srgb, var(--el-color-warning) 14%, var(--app-surface));
  color: var(--el-color-warning);
}
.dashboard-action__content {
  min-width: 0;
  flex: 1;
}
.dashboard-action__content strong,
.dashboard-action__content small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dashboard-action__content strong {
  margin-bottom: 3px;
  color: var(--app-text);
  font-size: 13px;
}
.dashboard-action__content small {
  color: var(--app-text-muted);
  font-size: 11px;
}
.dashboard-action__arrow {
  color: var(--app-border-strong);
}
.dashboard-grid {
  display: grid;
  grid-template-columns: minmax(0, 7fr) minmax(340px, 5fr);
  align-items: start;
  gap: 16px;
}
.dashboard-grid__column {
  display: grid;
  min-width: 0;
  align-content: start;
  gap: 16px;
}
.dashboard-card {
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-surface);
}
.dashboard-card__header {
  display: flex;
  min-height: 68px;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 17px 19px 13px;
}
.dashboard-card__header h2 {
  margin: 0 0 4px;
  color: var(--app-text);
  font-size: 14px;
  font-weight: 600;
}
.dashboard-card__header-actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
}
.dashboard-card__header-actions .dashboard-text-button:disabled {
  cursor: not-allowed;
  color: var(--app-border-strong);
}
.dashboard-card__header p {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 11px;
}
.dashboard-card__header-icon {
  margin-top: 3px;
  color: var(--app-text-muted);
  font-size: 18px;
}
.dashboard-text-button.el-button {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 4px;
  border: 0;
  background: transparent;
  padding: 3px 0;
  color: var(--app-accent);
  font-size: 11px;
  cursor: pointer;
}
.dashboard-text-button.el-button :deep(> span) {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.dashboard-text-button:hover {
  color: var(--app-accent-hover);
}
.dashboard-session-list,
.dashboard-recent-list {
  border-top: 1px solid var(--app-border);
}
.dashboard-session-row {
  display: flex;
  width: 100%;
  min-height: 46px;
  align-items: stretch;
  border-bottom: 1px solid var(--app-border);
  background: transparent;
}
.dashboard-session-row:last-child,
.dashboard-recent-list :is(button, .el-button):last-child {
  border-bottom: 0;
}
.dashboard-session-row:hover,
.dashboard-session-row.is-active {
  background: var(--app-accent-soft);
}
.dashboard-session-row__select.el-button {
  display: flex;
  min-width: 0;
  min-height: 46px;
  flex: 1;
  align-items: center;
  gap: 10px;
  margin: 0;
  border: 0;
  border-radius: 0;
  padding: 7px 8px 7px 19px;
  color: inherit;
  text-align: left;
}
.dashboard-session-row__dot {
  width: 7px;
  height: 7px;
  flex: none;
  border-radius: 50%;
  background: var(--el-color-success);
}
.dashboard-session-row__main {
  min-width: 0;
  flex: 1;
}
.dashboard-session-row__main strong,
.dashboard-session-row__main small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dashboard-session-row__main strong {
  margin-bottom: 2px;
  color: var(--app-text);
  font-size: 12px;
  font-weight: 600;
}
.dashboard-session-row__main small {
  color: var(--app-text-muted);
  font-size: 10px;
}
.dashboard-session-row__close.el-button {
  display: grid;
  width: 24px;
  height: 24px;
  flex: none;
  place-items: center;
  align-self: center;
  margin: 0 9px 0 0;
  border-radius: 4px;
  padding: 0;
  color: var(--app-text-muted);
  opacity: 1;
}
.dashboard-session-row__close:hover {
  background: var(--app-hover);
  color: var(--app-text-regular);
}
.dashboard-empty.el-button {
  display: grid;
  width: 100%;
  min-height: 180px;
  place-items: center;
  align-content: center;
  gap: 6px;
  border: 0;
  border-top: 1px solid var(--app-border);
  background: transparent;
  color: var(--app-text-muted);
  cursor: pointer;
}
.dashboard-empty .el-icon {
  margin-bottom: 4px;
  color: var(--app-accent);
  font-size: 28px;
}
.dashboard-empty strong {
  color: var(--app-text-regular);
  font-size: 13px;
}
.dashboard-empty span {
  font-size: 11px;
}
.dashboard-metrics {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
  padding: 0 16px 17px;
}
.dashboard-metrics :is(button, .el-button) {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-muted);
  padding: 13px 11px;
  text-align: left;
  cursor: pointer;
}
.dashboard-metrics :is(button, .el-button):hover {
  border-color: color-mix(in srgb, var(--app-accent) 55%, var(--app-surface));
  background: var(--app-accent-soft);
}
.dashboard-metric__icon {
  display: grid;
  width: 34px;
  height: 34px;
  flex: none;
  place-items: center;
  border-radius: 6px;
}
.dashboard-metric__icon.is-green {
  background: color-mix(in srgb, var(--el-color-success) 14%, var(--app-surface));
  color: var(--el-color-success);
}
.dashboard-metric__icon.is-orange {
  background: color-mix(in srgb, var(--el-color-warning) 14%, var(--app-surface));
  color: var(--el-color-warning);
}
.dashboard-metrics .dashboard-metric__icon + span {
  min-width: 0;
  flex: 1;
}
.dashboard-metrics strong,
.dashboard-metrics small {
  display: block;
}
.dashboard-metrics strong {
  color: var(--app-text);
  font-size: 19px;
  line-height: 1;
}
.dashboard-metrics small {
  margin-top: 4px;
  color: var(--app-text-muted);
  font-size: 10px;
}
.dashboard-metrics em {
  align-self: flex-end;
  color: var(--app-text-muted);
  font-size: 9px;
  font-style: normal;
  white-space: nowrap;
}
.dashboard-recent-list :is(button, .el-button) {
  display: flex;
  width: 100%;
  min-height: 49px;
  align-items: center;
  gap: 10px;
  border: 0;
  border-bottom: 1px solid var(--app-border);
  background: transparent;
  padding: 7px 18px;
  text-align: left;
  cursor: pointer;
}
.dashboard-recent-list :is(button, .el-button):hover {
  background: var(--app-accent-soft);
}
.dashboard-provider {
  display: grid;
  width: 29px;
  height: 29px;
  flex: none;
  place-items: center;
  border-radius: 6px;
  background: color-mix(in srgb, var(--app-accent) 12%, var(--app-surface));
  color: var(--app-accent);
  font-size: 12px;
  font-weight: 700;
}
.dashboard-provider.is-claude {
  background: color-mix(in srgb, var(--el-color-warning) 14%, var(--app-surface));
  color: var(--el-color-warning);
}
.dashboard-recent-list__main {
  min-width: 0;
  flex: 1;
}
.dashboard-recent-list__main strong,
.dashboard-recent-list__main small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dashboard-recent-list__main strong {
  margin-bottom: 2px;
  color: var(--app-text);
  font-size: 12px;
}
.dashboard-recent-list__main small {
  color: var(--app-text-muted);
  font-size: 10px;
}
.dashboard-recent-list__meta {
  flex: none;
  text-align: right;
}
.dashboard-recent-list__meta small,
.dashboard-recent-list__meta em {
  display: block;
  color: var(--app-text-muted);
  font-size: 9px;
  font-style: normal;
}
.dashboard-recent-list__meta em {
  margin-top: 3px;
}
.dashboard-card-empty {
  display: flex;
  min-height: 180px;
  align-items: center;
  justify-content: center;
  gap: 8px;
  border-top: 1px solid var(--app-border);
  color: var(--app-text-muted);
  font-size: 11px;
}
.dashboard-card-empty svg {
  width: 17px;
}
.dashboard-environment-list {
  display: grid;
  gap: 0;
  padding: 0 18px 10px;
}
.dashboard-environment-list > div {
  display: grid;
  grid-template-columns: 8px minmax(90px, 1fr) minmax(0, 1.5fr);
  align-items: center;
  gap: 8px;
  min-height: 34px;
  border-bottom: 1px solid var(--app-border);
  font-size: 11px;
}
.dashboard-environment-list strong {
  overflow: hidden;
  color: var(--app-text-regular);
  font-size: 10px;
  font-weight: 500;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dashboard-health-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--app-border-strong);
}
.dashboard-health-dot.is-ready {
  background: var(--el-color-success);
}
.dashboard-footer {
  display: flex;
  justify-content: space-between;
  padding: 14px 2px 0;
  color: var(--app-text-muted);
  font-size: 10px;
}
@container dashboard (max-width: 1080px) {
  .dashboard-actions {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .dashboard-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
@container dashboard (max-width: 520px) {
  .dashboard-actions {
    grid-template-columns: minmax(0, 1fr);
  }
  .dashboard-hero h1 {
    font-size: 24px;
  }
  .dashboard-clock {
    align-items: flex-start;
  }
}
</style>
