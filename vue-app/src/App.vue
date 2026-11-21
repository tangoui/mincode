<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { RouterView, useRoute, useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import AppSidebar from "@/components/layout/AppSidebar.vue";
import AppTabBar from "@/components/layout/AppTabBar.vue";
import PoppedOutTabPlaceholder from "@/components/layout/PoppedOutTabPlaceholder.vue";
import {
  closePoppedOutWorkbenchTab,
  closePopupWindow,
  isPopupWindow,
  POPUP_TAB_STATE_EVENT,
  POPUP_TAB_WINDOW_CLOSING_EVENT,
  type PopupWindowState,
} from "@/services/popup-tab";
import { recordClosedTabs } from "@/services/closed-tab-history";
import { invokeBackend, isDesktopRuntime, listenAppEvent, nativeErrorMessage } from "@/services/desktop-bridge";
import { currentKeyboardPlatform } from "@/services/keyboard-platform";
import { useSessionProgressStore } from "@/stores/session-progress";
import { useAgentRunNavigation } from "@/composables/use-agent-run-navigation";
import { listPluginViews } from "@/services/plugin-service";
import { installPluginHostUiBridge } from "@/services/plugin-host-ui";
import { usePluginHostUiStore } from "@/stores/plugin-host-ui";
import { usePluginTakeoverStore } from "@/stores/plugin-takeover";
import { usePluginViewWarmPoolStore } from "@/stores/plugin-view-warm-pool";
import { settingsNavigationRequest, type SettingsNavigationCategory } from "@/services/settings-navigation";
import { enqueuePluginAiTurn, notifyPluginAiTurnApproval, notifyPluginAiTurnCancelled, refreshPendingPluginAiTurns } from "@/services/plugin-ai-turns";
import {
  enqueueDatabaseFiles,
  enqueueProjectFiles,
} from "@/services/external-file-open";
import { completeEmbeddedBrowserOpenRequest, completeEmbeddedBrowserTabRequest } from "@/services/browser-service";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import {
  loadAiAssistantStore,
  peekAiAssistantStore,
  useAiAssistantShellStore,
  type AiAssistantStore,
} from "@/stores/ai-assistant-shell";
import { useWorkbenchStore } from "@/stores/workbench";
import { useStatusBarStore } from "@/stores/status-bar";
import { useNotificationsStore } from "@/stores/notifications";
import { useSshMfaStore } from "@/stores/ssh-mfa";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useAppUpdateStore } from "@/stores/app-update";
import { useBrowserWorkspaceStore, type BrowserAiTabRequest } from "@/stores/browser-workspace";
import { useLocalProjectStore } from "@/stores/local-project";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";
import { switchDatabaseWorkspaceScope } from "@/stores/database-workspace-shell";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";
import { resolveWorkspaceRouteInstanceScope } from "@/domain/workspace/route-state-scope";
import {
  DEFAULT_WORKSPACE_CACHE_LIMIT,
  normalizeWorkspaceCacheLimit,
} from "@/domain/workspace/state-scope";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { cachedWorkspaceViewNames } from "@/domain/workspace/view-retention";
import { useTerminalAttention } from "@/composables/use-terminal-attention";
import { useTerminalTabCleanup } from "@/composables/use-terminal-tab-cleanup";
import { useTerminalAttentionStore } from "@/stores/terminal-attention";
import { preloadWorkspaceView, workspaceViewLoaders } from "@/router/workspace-views";
import { useVueI18n } from "@/i18n/messages";
import { hydrateUiAppearance } from "@/services/ui-appearance";
import {
  hydrateSidebarLabels,
  sidebarLabelsVisible,
} from "@/services/sidebar-label-preferences";
import {
  hydrateSettingsSnapshot,
  readSettingsSnapshot,
  settingsSnapshotVersion,
} from "@/services/settings-persistence";
import { loadLocalShellCwds } from "@/services/terminal-service";
import {
  flushUiSessionPersistence,
  registerUiSessionFlusher,
  requestUiSessionCapture,
} from "@/services/ui-session";
import { workspaceViews, type WorkspaceView } from "@/types/workspace";
import type { CommandPaletteOpenRequest } from "@/domain/commands/command-palette";
import { useAppCommands } from "@/composables/use-app-commands";
import { installKeybindingDispatcher } from "@/services/keybinding-dispatcher";
import { pluginCommandParamRequest, startPluginCommandSync } from "@/services/plugin-commands";
import { scheduleIdleTask } from "@/services/idle-task";
import { createStartupIdleBridgeCoordinator } from "@/services/startup-idle-bridges";
import { useCommandPaletteStore } from "@/stores/command-palette";
import { useKeybindingsStore } from "@/stores/keybindings";
import {
  parseDesktopAiConversationTarget,
  parseDesktopPluginTarget,
  type NotificationTarget,
} from "@/domain/notifications/target";
import { notifyApp } from "@/services/notifications";
import { createOrderedDelivery } from "@/domain/notifications/ordered-delivery";
import { globalShortcutNoticeKey, hostGlobalShortcutErrorNotice } from "@/domain/keybindings/plugin-global-shortcut-notices";
import { BACKUP_RUN_EVENT } from "@/domain/database/backup-events";
import { openPluginViewTab } from "@/domain/workspace/open-plugin-view";
import { localizedPluginTitle } from "@/services/plugin-titles";
import { drainPendingDesktopNavigation, handlePluginUriLink } from "@/services/plugin-uri-links";
import { listenNotificationNavigation } from "@/services/notification-navigation";
import { AI_ASSISTANT_DROP_TARGET_ID } from "@/domain/ai/dropped-files";

const AppCommandPaletteHost = defineAsyncComponent(
  () => import("@/components/layout/AppCommandPaletteHost.vue")
);
// Install before startup IPC and lazy rendering so the very first publish is retained.
const PluginHostUiLayer = defineAsyncComponent(() => import("@/components/plugins/PluginHostUiLayer.vue"));
const pluginHostUi = usePluginHostUiStore();
const PluginImmersiveExit = defineAsyncComponent(() => import("@/components/plugins/PluginImmersiveExit.vue"));
// A plugin view may hide the MinCode chrome (features.immersive); the exit control above stays with the host.
const pluginTakeover = usePluginTakeoverStore();
const PluginCommandParamsHost = defineAsyncComponent(
  () => import("@/components/plugins/PluginCommandParamsHost.vue")
);
const PluginServicesRoot = defineAsyncComponent(() => import("@/components/plugins/PluginServicesRoot.vue"));
const AiAssistantDrawer = defineAsyncComponent(
  () => import("@/components/ai/AiAssistantDrawer.vue")
);
// 关联笔记抽屉含编辑器（CodeMirror），首次从各页面入口打开后才加载。
const LinkedNotesDrawer = defineAsyncComponent(() => import("@/components/notes/LinkedNotesDrawer.vue"));
const SshMfaDialog = defineAsyncComponent(
  () => import("@/components/ssh/SshMfaDialog.vue")
);
const PopupTabWindow = defineAsyncComponent(
  () => import("@/components/layout/PopupTabWindow.vue")
);
const SettingsWorkspaceView = defineAsyncComponent(
  () => import("@/views/SettingsWorkspaceView.vue")
);
// 插件工作区不进 KeepAlive：KeepAlive 停用时会把 DOM 移出文档，iframe 被卸载，
// 切回插件 tab 时页面会重新加载。这里为每个有插件 tab 的工作区常驻挂载一层，
// 切到其他 tab 只隐藏，插件页面和未提交的表单保持不变。
const PluginWorkspaceView = defineAsyncComponent(workspaceViewLoaders["plugin-view"]);

const route = useRoute();
const router = useRouter();
const { t } = useVueI18n();
const workbench = useWorkbenchStore();
const terminalWorkspace = useTerminalWorkspaceStore();
const assistant = useAiAssistantShellStore();
const sessionProgress = useSessionProgressStore();
const agentRunNavigation = useAgentRunNavigation();

/** Opens one AI run by ID; an unknown or finished run falls back to the task list. */
async function openAgentRunTarget(runId: string) {
  await sessionProgress.ensureListening();
  const run = sessionProgress.runsById[runId];
  if (!run || !await agentRunNavigation.openRun(run)) sessionProgress.requestPanel();
}
// 待确认修改只服务 AI 抽屉，随 AI 首次打开再加载，不进入启动入口包。
let pendingEdits: ReturnType<typeof import("@/stores/ai-pending-edits").useAiPendingEditsStore> | undefined;
const notifications = useNotificationsStore();
const sshMfa = useSshMfaStore();
const sshConnections = useSshConnectionsStore();
const browserWorkspace = useBrowserWorkspaceStore();
const localProject = useLocalProjectStore();
const workspaceStatePreferences = useWorkspaceStatePreferencesStore();

let workbenchRestorePromise: Promise<void> = Promise.resolve();

interface NativeFileDropEvent {
  files?: string[];
  targetId?: string;
}

function databaseFileCandidate(path: string) {
  return /\.(?:sqlite3?|duckdb|ddb|db)$/i.test(path);
}

async function routeOpenedFiles(files: string[], targetId?: string) {
  const normalized = [...new Set(files.map((file) => file.trim()).filter(Boolean))];
  if (!normalized.length) return;
  await workbenchRestorePromise;
  if (targetId === "app-project-drop-target") {
    if (!workbench.openView("projects")) return;
    enqueueProjectFiles(normalized);
    await router.push({ name: "projects" });
    return;
  }
  if (targetId === "database-workspace-drop-target") {
    enqueueDatabaseFiles(normalized);
    workbench.openView("database");
    await router.push({ name: "database" });
    return;
  }
  const databaseFiles: string[] = [];
  const projectFiles: string[] = [];
  for (const file of normalized) {
    if (!databaseFileCandidate(file)) {
      projectFiles.push(file);
      continue;
    }
    try {
      await invokeBackend("db_detect_file_type", { filePath: file });
      databaseFiles.push(file);
    } catch {
      projectFiles.push(file);
    }
  }
  if (projectFiles.length) {
    enqueueProjectFiles(projectFiles);
    workbench.openView("projects");
    await router.push({ name: "projects" });
  }
  if (databaseFiles.length) {
    enqueueDatabaseFiles(databaseFiles);
    workbench.openView("database");
    await router.push({ name: "database" });
  }
}
const commandPalette = useCommandPaletteStore();
const linkedNotesDrawer = useLinkedNotesDrawer();
const keybindings = useKeybindingsStore();
const settingsOpen = ref(false);
const settingsInitialCategory = ref<SettingsNavigationCategory | undefined>();
const settingsInitialPluginId = ref<string>();
const settingsLoaded = ref(false);
const assistantLoaded = ref(false);
const popupWindow = isPopupWindow();
const unifiedTitleBar = isDesktopRuntime() && currentKeyboardPlatform() === "mac";
document.documentElement.classList.toggle("app-popup-window", popupWindow);
const activePopupLabel = computed(() => workbench.activeTabPopupLabel);
const activeWorkspaceView = computed<WorkspaceView>(() => workbench.activeTab?.view ?? "dashboard");
const routeSyncReady = ref(false);
const routeViewPending = computed(
  () =>
    !activePopupLabel.value &&
    (!routeSyncReady.value ||
      (typeof route.name === "string" && route.name !== activeWorkspaceView.value))
);
const viewResetVersion = ref(0);
// A view stays cached only while it has an open tab: closing its last tab releases it.
const cachedWorkspaceViews = computed(() => cachedWorkspaceViewNames(workbench.tabs));
// 工作区视图 KeepAlive 缓存上限，来自设置面板；始终受安全上限约束，调低时按 LRU 淘汰多余实例。
const workspaceCacheLimit = ref(
  normalizeWorkspaceCacheLimit(readSettingsSnapshot().workspaceCacheLimit ?? DEFAULT_WORKSPACE_CACHE_LIMIT)
);
function refreshWorkspaceCacheLimit() {
  workspaceCacheLimit.value = normalizeWorkspaceCacheLimit(
    readSettingsSnapshot().workspaceCacheLimit ?? DEFAULT_WORKSPACE_CACHE_LIMIT
  );
}
// The last desktop_set_global_shortcut error shown, so a repeated sync does not
// toast the same failure again; a success clears it.
let lastGlobalShortcutError = "";
function syncDesktopSettings() {
  refreshWorkspaceCacheLimit();
  if (!isDesktopRuntime()) return;
  const snapshot = readSettingsSnapshot();
  invokeBackend("desktop_set_global_shortcut", {
    enabled: snapshot.globalShortcutEnabled === true,
    accelerator: "CmdOrCtrl+Shift+Space",
  }).then(
    () => {
      lastGlobalShortcutError = "";
    },
    (error: unknown) => {
      const message = nativeErrorMessage(error);
      if (message === lastGlobalShortcutError) return;
      lastGlobalShortcutError = message;
      const notice = hostGlobalShortcutErrorNotice(message);
      ElMessage.error({ message: t(notice.key, notice.params), duration: 6000 });
    }
  );
}
watch(settingsSnapshotVersion, syncDesktopSettings);
void hydrateSettingsSnapshot<{ globalShortcutEnabled?: unknown } & Record<string, unknown>>().then((snapshot) => {
  void snapshot;
  syncDesktopSettings();
});
const workspaceStateScopeId = computed(() =>
  workspaceStatePreferences.scopeIdFor(workbench.activeWorkspaceId)
);
function routeStateScopeKey(routeName: unknown) {
  return resolveWorkspaceRouteInstanceScope(routeName, workspaceStateScopeId.value);
}
// 有插件 tab 的工作区各保留一个插件宿主（与原 KeepAlive 一样按工作区隔离）；
// 工作区的插件 tab 全部关闭且没有保温中的插件视图后，宿主卸载并释放插件会话。
const pluginViewWarmPool = usePluginViewWarmPoolStore();
const pluginWorkspaceIds = computed(() => {
  const ids = new Set<string>();
  for (const tab of workbench.tabs) {
    if (tab.view === "plugin-view" && tab.pluginView?.pluginId && tab.pluginView.viewId) ids.add(tab.workspaceId);
  }
  for (const workspaceId of pluginViewWarmPool.workspaceIds) ids.add(workspaceId);
  return [...ids];
});
const pluginLayerVisible = computed(
  () => routeSyncReady.value && !activePopupLabel.value && route.name === "plugin-view"
);

// 弹出窗口不渲染命令面板与侧栏。
if (!popupWindow) {
  void commandPalette.hydrate();
}
// 快捷键在弹出窗口也需要，因此两类窗口都加载键位并安装唯一的键盘分发器。
void keybindings.hydrate();
const uninstallKeybindingDispatcher = installKeybindingDispatcher();
// 插件贡献、插件命令和宿主事件都不是首屏绘制所需；首帧后再安装，避免把几十个
// contribution consumer 和宿主桥接同步压进根组件启动路径。每个 loader 自己处理
// 桌面 / 浏览器差异，协调器负责取消与卸载。
const stopStartupIdleBridges = createStartupIdleBridgeCoordinator([
  {
    name: "plugin-contributions",
    load: () => import("@/services/contributions").then(({ startPluginContributionSync }) => startPluginContributionSync()),
  },
  ...(!popupWindow ? [{
    name: "plugin-commands",
    load: () => Promise.resolve(startPluginCommandSync(router)),
  }] : []),
  ...(!popupWindow ? [
    {
      name: "plugin-host-events",
      load: () => import("@/services/plugin-host-events").then(({ installPluginHostEventBridge }) => installPluginHostEventBridge()),
    },
    {
      name: "plugin-associations",
      load: () => import("@/services/plugin-associations").then(({ installPluginAssociations }) => installPluginAssociations()),
    },
    {
      // New terminals can then create their WebGL renderer at open instead of swapping it in later.
      name: "terminal-webgl",
      load: () => import("@/services/terminal-preferences").then(({ preloadTerminalWebgl }) => preloadTerminalWebgl()),
      timeout: 5000,
    },
  ] : []),
]);
onBeforeUnmount(stopStartupIdleBridges);
// host_ui requests are fire-and-forget on the backend. Their listeners must
// exist before the first paint so startup prompts and terminal/editor opens
// cannot be lost while the optional processing bridges wait for idle time.
const stopPluginHostUiBridge = popupWindow ? undefined : installPluginHostUiBridge();
if (stopPluginHostUiBridge) onBeforeUnmount(stopPluginHostUiBridge);
// 参数表单宿主在首次请求时才加载，之后保持挂载以保留对话框关闭动画。
const pluginCommandParamsHostLoaded = ref(false);
watch(pluginCommandParamRequest, (request) => {
  if (request) pluginCommandParamsHostLoaded.value = true;
});
// 插件服务（文件树、编辑器选区、命令面板发起）只在主窗口运行；在首帧后的空闲时挂载 PluginServicesRoot 按需加载，不进入入口包。
const pluginServicesReady = ref(false);
if (!popupWindow) scheduleIdleTask(() => (pluginServicesReady.value = true));
// 插件状态栏（statusBar 贡献点）只在主窗口渲染；仅当有插件贡献条目时才加载组件。
const AppStatusBar = defineAsyncComponent(() => import("@/components/layout/AppStatusBar.vue"));
const statusBarStore = useStatusBarStore();
// 锁屏在主窗口与弹出窗口都必须生效，因此不放进 popupWindow 分支。
void hydrateUiAppearance().catch(() => {
  // Feature entry points surface a localized recovery message when credentials are needed.
});
if (!popupWindow) void hydrateSidebarLabels();
if (!popupWindow) useAppUpdateStore().startAutoCheck();
// 一级侧栏文字标签显隐驱动轨道宽度，类名挂在 app-frame 供全局 CSS 级联。
const sidebarLabels = sidebarLabelsVisible();

watch(settingsOpen, (open) => {
  if (open) settingsLoaded.value = true;
});
function openSettings(initialCategory?: SettingsNavigationCategory, pluginId?: string) {
  settingsInitialCategory.value = initialCategory;
  settingsInitialPluginId.value = pluginId;
  settingsOpen.value = true;
}
// Plugin Manager and plugin iframes open Settings > Plugins through the navigation service.
watch(settingsNavigationRequest, (request) => {
  if (request.seq > 0) openSettings(request.category, request.pluginId);
});
const commandPaletteOpenRequest = ref<CommandPaletteOpenRequest>({ seq: 0 });

const commandPaletteShortcut = computed(() => keybindings.label("builtin:command-palette"));

function openCommandPalette(request: Omit<CommandPaletteOpenRequest, "seq">) {
  if (popupWindow) return;
  // 项目工作区默认搜索项目文件（与旧 ⌘K 行为一致）；显式指定的范围优先。
  const preferFiles = activeWorkspaceView.value === "projects" && Boolean(localProject.projectRoot);
  commandPaletteOpenRequest.value = {
    seq: commandPaletteOpenRequest.value.seq + 1,
    scope: request.scope ?? (preferFiles && !request.prefix ? "files" : undefined),
    prefix: request.prefix,
  };
}

useAppCommands({ openPalette: openCommandPalette, openSettings: (category) => openSettings(category), popup: popupWindow });

function syncAiWorkspaceDirectory(aiStore: AiAssistantStore | undefined = peekAiAssistantStore()) {
  aiStore?.setWorkspaceDirectory(workbench.workspaces.map((workspace) => {
    const scopeId = workspaceStatePreferences.scopeIdFor(workspace.id);
    return {
      workspaceId: workspace.id,
      workspaceName: workspace.name,
      scopeId,
      projectRoot: localProject.projectRootForWorkspace(scopeId).trim()
        || workspace.worktree?.worktreePath?.trim()
        || "",
    };
  }));
}

let assistantLoadPromise: Promise<void> | undefined;
async function ensureAssistantLoaded() {
  if (assistantLoaded.value) return;
  if (assistantLoadPromise) return assistantLoadPromise;

  assistantLoadPromise = (async () => {
    // AI 历史通常是启动阶段最大的可选状态；仅在用户或云端任务真正打开 AI 时恢复。
    await workbenchRestorePromise;
    const aiStore = await loadAiAssistantStore();
    const scopeId = workspaceStatePreferences.scopeIdFor(workbench.activeWorkspaceId);
    syncAiWorkspaceDirectory(aiStore);
    // 单个历史记录损坏时仍允许打开 AI；Store 会保留可恢复的空状态，避免启动优化变成入口阻塞。
    await Promise.allSettled([
      aiStore.hydrate(scopeId, {
        workspaceId: workbench.activeWorkspaceId,
        workspaceName: workbench.activeWorkspace?.name,
        scopeId,
        projectRoot: localProject.projectRoot.trim() || workbench.activeWorktreePath,
      }),
      import("@/stores/ai-pending-edits").then(({ useAiPendingEditsStore }) => {
        pendingEdits = useAiPendingEditsStore();
        return pendingEdits.hydrate();
      }),
      sshConnections.hydrate(),
    ]);
    assistantLoaded.value = true;
  })().finally(() => {
    assistantLoadPromise = undefined;
  });
  return assistantLoadPromise;
}
watch(
  () => assistant.open,
  (open) => {
    if (open && !popupWindow) void ensureAssistantLoaded();
  },
  { immediate: true }
);
// AI 标签页路由激活时确保宿主组件加载；tab 呈现模式下离开标签即视为收起，
// 宿主实例保持挂载，后台任务与监听不受影响。
watch(
  () => route.name,
  (name) => {
    if (popupWindow) return;
    if (name === "ai") {
      void ensureAssistantLoaded();
    } else if (assistant.placement === "tab" && assistant.open) {
      assistant.open = false;
    }
  },
  { immediate: true }
);

async function navigateToNotificationTarget(target: NotificationTarget) {
  if (target.kind === "plugin") {
    const views = await listPluginViews().catch(() => []);
    const view = views.find((item) => item.pluginId === target.pluginId && (!target.viewId || item.id === target.viewId))
      ?? views.find((item) => item.pluginId === target.pluginId);
    if (view) openPluginViewTab(workbench, router, view.pluginId, view.id, localizedPluginTitle(view));
    return;
  }
  if (target.kind === "view") {
    if (!workbench.openView(target.view) && target.view !== "dashboard") return;
    await router.push({ name: target.view }).catch(() => undefined);
    return;
  }

  await ensureAssistantLoaded();
  const aiStore = await loadAiAssistantStore();
  if (target.kind === "ai") {
    aiStore.show("agentic");
    return;
  }
  const switched = await aiStore.switchToConversation(
    target.conversationId,
    target.workspaceScopeId,
  );
  // 会话已被删除或通知来自旧版本时仍打开 AI，避免点击后无反馈。
  aiStore.show(switched ? aiStore.mode : "agentic");
}

const stopNotificationNavigation = popupWindow
  ? undefined
  : listenNotificationNavigation(navigateToNotificationTarget);

// Restore the persisted shell before route changes are allowed to create new
// tabs. The router starts at dashboard, so treating that initial route as a
// user navigation would otherwise erase the restored active tab.
async function restoreWorkbenchSession() {
  try {
    await Promise.all([workbench.hydrate(), workspaceStatePreferences.hydrate()]);
    // Fetch the restored view's chunk while the project workspace is still loading.
    void preloadWorkspaceView(workbench.activeTab?.view ?? "dashboard");
    const scopeId = workspaceStatePreferences.scopeIdFor(workbench.activeWorkspaceId);
    await localProject.hydrate(scopeId);
    localProject.switchWorkspace(scopeId);
    switchDatabaseWorkspaceScope(scopeId);
    const restoredTab = workbench.activeTab;
    const restoredView = restoredTab?.view ?? "dashboard";
    if (route.name !== restoredView || restoredView === "plugin-view") {
      await router.replace(workspaceTabRoute(restoredTab));
    }
  } finally {
    routeSyncReady.value = true;
  }
}

// Terminals that finished or wait for the user mark their tab and add to the Dock badge.
const terminalAttentionStore = useTerminalAttentionStore();
const terminalAttention = popupWindow ? undefined : useTerminalAttention(router);
if (!popupWindow) useTerminalTabCleanup();

if (!popupWindow) {
  workbenchRestorePromise = restoreWorkbenchSession();
  void terminalWorkspace.hydrate();
  notifications.ensureListening();
  void notifications.hydrate().then(() => {
    if (isDesktopRuntime()) {
      watch(() => notifications.unreadCount + terminalAttentionStore.count, (count) => {
        void invokeBackend("desktop_set_badge", { label: count ? (count > 99 ? "99+" : String(count)) : "" }).catch(() => undefined);
      }, { immediate: true });
    }
  });
  sshMfa.ensureListening();
}

let stopPopupClose: (() => void) | undefined;
let stopPopupState: (() => void) | undefined;
let stopBrowserOpen: (() => void) | undefined;
let stopBrowserTabRequest: (() => void) | undefined;
let stopBeforeQuit: (() => void) | undefined;
let stopNativeFileDrop: (() => void) | undefined;
let stopDesktopNavigate: (() => void) | undefined;
let stopPluginAiTurn: (() => void) | undefined;
let stopPluginAiTurnCancel: (() => void) | undefined;
let stopPluginAiTurnApproval: (() => void) | undefined;
let stopPluginNotify: (() => void) | undefined;
let stopBackupRunNotify: (() => void) | undefined;
interface PluginNotifyEvent {
  pluginId?: string;
  pluginName?: string;
  title?: string;
  body?: string;
  level?: "info" | "success" | "warning" | "error";
  system?: boolean;
  viewId?: string;
  /** Host-generated notices carry a code the frontend localizes (e.g. "globalShortcut.locked"). */
  code?: string;
  /** Up to two buttons running the plugin's own commands (host 0.3.0); re-validated by parsePluginNotificationActions. */
  actions?: unknown;
}
const deliverPluginNotice = createOrderedDelivery();
let stopTerminalSessionFlusher: (() => void) | undefined;

if (!popupWindow) {
  stopTerminalSessionFlusher = registerUiSessionFlusher(async () => {
    const paneIds = terminalWorkspace.localPaneIds();
    if (!paneIds.length) return;
    terminalWorkspace.updateLocalPaneCwds(await loadLocalShellCwds(paneIds));
  });
}

async function flushPersistentUiState() {
  requestUiSessionCapture();
  await nextTick();
  await flushUiSessionPersistence();
  await Promise.allSettled([
    workbench.flushPersistence(),
    terminalWorkspace.persistLayout(),
    localProject.flushPersistence(),
    peekAiAssistantStore()?.flushPersistence(),
    pendingEdits?.flushPersistence(),
    workspaceStatePreferences.flushPersistence(),
    sshConnections.flushPersistence(),
  ]);
}

if (!popupWindow) {
  watch(
    [
      () => workbench.activeWorkspaceId,
      () => workspaceStatePreferences.isolationEnabled,
      () => workspaceStatePreferences.sharedWorkspaceId,
      () => localProject.projectRoot,
      () => workbench.activeWorktreePath,
      () => workbench.workspaces.map((workspace) => [
        workspace.id,
        workspace.name,
        workspace.worktree?.worktreePath ?? "",
      ]).join("\u0000"),
    ],
    ([workspaceId]) => {
      if (!workbench.hydrated || !workspaceStatePreferences.hydrated) return;
      const scopeId = workspaceStatePreferences.scopeIdFor(workspaceId);
      if (localProject.hydrated) localProject.switchWorkspace(scopeId);
      syncAiWorkspaceDirectory();
      switchDatabaseWorkspaceScope(scopeId);
      const aiStore = peekAiAssistantStore();
      if (aiStore?.hydrated) {
        void aiStore.followTopWorkspace({
          workspaceId,
          workspaceName: workbench.activeWorkspace?.name,
          scopeId,
          projectRoot: localProject.projectRoot.trim() || workbench.activeWorktreePath,
        });
      }
    },
    { flush: "sync" }
  );
  // Keep scope coordination glue outside the 192 KiB entry chunk. Only the
  // main window owns this bridge, including disposal before the import resolves.
  let stopPluginWorkspaceScopeBridge: (() => void) | undefined;
  let pluginScopeBridgeDisposed = false;
  void import("@/services/plugin-workspace-scope").then(({ installPluginWorkspaceScopeBridge }) => {
    if (!pluginScopeBridgeDisposed) stopPluginWorkspaceScopeBridge = installPluginWorkspaceScopeBridge();
  });
  onBeforeUnmount(() => {
    pluginScopeBridgeDisposed = true;
    stopPluginWorkspaceScopeBridge?.();
  });
  window.addEventListener("pagehide", flushPersistentUiState);
  window.addEventListener("beforeunload", flushPersistentUiState);
  watch(
    () => [route.name, route.params.pluginId, route.params.viewId, route.params.resultId] as const,
    ([routeName, rawPluginId, rawViewId, rawResultId]) => {
      if (!routeSyncReady.value || !workbench.hydrated || typeof routeName !== "string") return;
      if (routeName === "service-result") {
        // Service result tabs are opened by plugin-service-result-tabs.ts only; a route
        // without its tab (e.g. history navigation after closing it) falls back to the active tab.
        const resultId = typeof rawResultId === "string" ? rawResultId : "";
        if (workbench.activeTab?.serviceResult?.resultId === resultId) return;
        const tab = workbench.workspaceTabs.find((item) => item.serviceResult?.resultId === resultId);
        if (tab) workbench.selectTab(tab.id);
        else void router.replace(workspaceTabRoute(workbench.activeTab));
        return;
      }
      if (routeName === "plugin-view") {
        const pluginId = typeof rawPluginId === "string" ? rawPluginId.trim() : "";
        const viewId = typeof rawViewId === "string" ? rawViewId.trim() : "";
        const activePluginView = workbench.activeTab?.pluginView;
        if (
          !pluginId
          || !viewId
          || (activePluginView?.pluginId === pluginId && activePluginView.viewId === viewId)
        ) return;
        if (!workbench.openPluginView(pluginId, viewId, t("plugins.viewTitle"))) {
          void router.replace(workspaceTabRoute(workbench.activeTab));
        }
        return;
      }
      if (workbench.activeTab?.view !== routeName && !workbench.openView(routeName as WorkspaceView) && routeName !== "dashboard") {
        void router.replace(workspaceTabRoute(workbench.activeTab));
      }
    },
    { immediate: true }
  );

  if (isDesktopRuntime()) {
    stopNativeFileDrop = listenAppEvent<NativeFileDropEvent>("native-files-dropped", (event) => {
      // The AI drawer turns drops on its panel into chat attachments itself.
      if (event?.targetId === AI_ASSISTANT_DROP_TARGET_ID) return;
      if (Array.isArray(event?.files)) void routeOpenedFiles(event.files, event.targetId);
    });
    // Desktop navigation (notification clicks, tray items, mincode:// links): live
    // desktop:navigate events and the targets queued before these listeners existed.
    const handleDesktopTarget = (target: { kind?: string; value?: string } | null | undefined) => {
      if (target?.kind === "plugin-uri") {
        void handlePluginUriLink(target.value ?? "");
        return;
      }
      if (target?.kind === "ai") {
        void assistant.show("agentic");
        return;
      }
      // Clicks on floating plugin views (views[].floating): the host already
      // brought this window to the front; open the run or the task list.
      if (target?.kind === "agent-runs") {
        sessionProgress.requestPanel();
        return;
      }
      if (target?.kind === "agent-run") {
        void openAgentRunTarget(target.value ?? "");
        return;
      }
      if (target?.kind === "terminal-pane") {
        void terminalAttention?.focusPane(target.value ?? "");
        return;
      }
      if (target?.kind === "plugin") {
        const pluginTarget = parseDesktopPluginTarget(target.value);
        if (pluginTarget) void navigateToNotificationTarget(pluginTarget);
        return;
      }
      // A system tray plugin item (menus.tray): the host raised this window;
      // the runner looks the command up again.
      if (target?.kind === "plugin-command") {
        // A chunk that fails to load is reported like a command that cannot run.
        const unavailable = (error: unknown) => ElMessage({
          type: "warning",
          grouping: true,
          message: t("pluginContrib.shell.unavailable", { error: error instanceof Error ? error.message : String(error) }),
        });
        void import("@/domain/plugins/tray").then(({ handlePluginCommandTarget }) =>
          handlePluginCommandTarget(target.value, {
            locked: false,
            run: (origin, pluginId, commandId) =>
              import("@/services/plugin-shell-commands")
                .then((module) => module.runShellCommand(origin, pluginId, commandId))
                .catch(unavailable),
          })).catch(unavailable);
        return;
      }
      if (target?.kind === "ai-conversation") {
        const conversationTarget = parseDesktopAiConversationTarget(target.value);
        if (conversationTarget) void navigateToNotificationTarget(conversationTarget);
        return;
      }
      if (target?.kind === "view" && workspaceViews.includes(target.value as WorkspaceView)) {
        void navigateToNotificationTarget({
          kind: "view",
          view: target.value as WorkspaceView,
        });
      }
    };
    stopDesktopNavigate = listenAppEvent<{ kind?: string; value?: string }>("desktop:navigate", handleDesktopTarget);
    // Plugins with the "ai" capability (team-chat bots) ask the assistant to answer;
    // the drawer owns the agent loop, so make sure it is loaded.
    stopPluginAiTurn = listenAppEvent<{ id?: string }>("plugin:ai-turn", (event) => {
      if (enqueuePluginAiTurn(event?.id)) void ensureAssistantLoaded();
    });
    stopPluginAiTurnCancel = listenAppEvent<{ id?: string }>("plugin:ai-turn-cancel", (event) => {
      notifyPluginAiTurnCancelled(event?.id);
    });
    stopPluginAiTurnApproval = listenAppEvent("plugin:ai-turn-approval", (event) => notifyPluginAiTurnApproval(event));
    stopPluginNotify = listenAppEvent<PluginNotifyEvent>("plugin:notify", (event) => {
      if (typeof event?.pluginId !== "string" || typeof event.title !== "string") return;
      const level = ["info", "success", "warning", "error"].includes(event.level ?? "") ? event.level : "info";
      // Host rejections of plugin global shortcuts arrive with a bilingual fallback title and a code.
      const noticeKey = globalShortcutNoticeKey(event.code);
      const title = noticeKey ? t(noticeKey) : event.title;
      // Every action runs a command of the notifying plugin: its pluginId is the event's.
      // The parser loads only for a notification with actions; deliverPluginNotice keeps arrival order
      // and pushes a notification without actions synchronously when nothing is pending.
      const { pluginId, actions: rawActions } = event;
      const pluginName = event.pluginName || pluginId;
      const input = {
        source: "plugin" as const,
        level,
        title: event.pluginName ? `${event.pluginName} · ${title}` : title,
        message: event.body || undefined,
        target: { kind: "plugin" as const, pluginId, ...(event.viewId ? { viewId: event.viewId } : {}) },
      };
      const options = { system: event.system === true };
      const parsing = Array.isArray(rawActions) && rawActions.length
        ? import("@/domain/notifications/plugin-actions").then((module) => module.parsePluginNotificationActions(rawActions, pluginId))
        : undefined;
      deliverPluginNotice(parsing, (actions) => {
        notifyApp(actions ? { ...input, actions, pluginName } : input, options);
      });
    });
    // Scheduled database backups run in Go even when the database view is closed.
    // The backup service is loaded on the first run so it stays out of the entry chunk.
    stopBackupRunNotify = listenAppEvent<unknown>(BACKUP_RUN_EVENT, (payload) => {
      void import("@/services/database-backup-service")
        .then(({ notifyBackupRunPayload }) => notifyBackupRunPayload(payload))
        .catch(() => undefined);
    });
    void refreshPendingPluginAiTurns()
      .then((added) => { if (added) void ensureAssistantLoaded(); })
      .catch(() => undefined);
    stopBeforeQuit = listenAppEvent("app:before-quit", () => {
      void flushPersistentUiState();
    });
    stopPopupClose = listenAppEvent<string>(POPUP_TAB_WINDOW_CLOSING_EVENT, (label) => {
      if (workbench.reclaimPoppedOutTabByLabel(label)) viewResetVersion.value += 1;
    });
    stopBrowserOpen = listenAppEvent<{ requestId: string; url?: string }>(
      "browser:ai-open-request",
      (request) => {
        const requestId = request?.requestId?.trim();
        if (!requestId) return;
        browserWorkspace.requestAiOpenTab(request.url, requestId);
        workbench.openView("browser");
        void router.push({ name: "browser" }).catch(async (error: unknown) => {
          const message = error instanceof Error ? error.message : String(error);
          browserWorkspace.failOpenRequest(requestId, message);
          await completeEmbeddedBrowserOpenRequest({ requestId, error: message }).catch(() => undefined);
        });
      }
    );
    // ACP 的 browser.list_tabs / browser.switch_tab：标签页状态在前端，由 store 回答；
    // 浏览器工作区没打开时也立即回答（没有标签页），不让后端干等到超时。
    // 回答逻辑按需加载，不进入口包；加载失败也回一次错误，每个请求恰好一个回复。
    stopBrowserTabRequest = listenAppEvent<BrowserAiTabRequest>(
      "browser:ai-tab-request",
      (request) => {
        void import("@/services/browser-ai-tabs")
          .then(({ answerAiTabRequest }) => answerAiTabRequest(browserWorkspace, request))
          .catch((error: unknown) => {
            const requestId = typeof request?.requestId === "string" ? request.requestId.trim() : "";
            if (!requestId) return;
            const message = error instanceof Error ? error.message : String(error);
            return completeEmbeddedBrowserTabRequest({ requestId, error: message }).catch(() => undefined);
          });
      }
    );
    stopPopupState = listenAppEvent<PopupWindowState>(POPUP_TAB_STATE_EVENT, (state) => {
      if (state?.type === "terminal-layout") {
        const ownsWindow = Object.values(workbench.poppedOutTabLabels).includes(state.windowLabel);
        if (ownsWindow) terminalWorkspace.applyLayoutSnapshot(state.snapshot);
        return;
      }
      if (state?.type === "close-workbench-tab") {
        closePoppedOutWorkbenchTab(state.windowLabel, {
          tabs: workbench.tabs,
          poppedOutTabLabels: workbench.poppedOutTabLabels,
          activeTabId: workbench.activeTabId,
          closeTab: workbench.closeTab,
          recordClosed: recordClosedTabs,
          navigateToActive: () => void router.replace(workspaceTabRoute(workbench.activeTab)),
          closeWindow: closePopupWindow,
        });
        return;
      }
      if (state?.type === "ai-assistant" && state.windowLabel === "popup-ai-assistant") {
        const snapshot = state.snapshot;
        void loadAiAssistantStore().then((aiStore) => {
          if (aiStore.restoreSnapshot(snapshot)) aiStore.show(snapshot.mode);
        });
      }
    });
    // A deep link that launched the app arrived before these listeners existed. Like files
    // opened at launch, it waits for the workbench restore so the restored tabs do not replace it.
    void drainPendingDesktopNavigation((target) => {
      void workbenchRestorePromise.then(() => handleDesktopTarget(target));
    });
  }
}

onBeforeUnmount(() => {
  uninstallKeybindingDispatcher();
  if (!popupWindow) {
    window.removeEventListener("pagehide", flushPersistentUiState);
    window.removeEventListener("beforeunload", flushPersistentUiState);
    void flushPersistentUiState();
  }
  stopNativeFileDrop?.();
  stopDesktopNavigate?.();
  stopNotificationNavigation?.();
  stopPluginAiTurn?.();
  stopPluginAiTurnCancel?.();
  stopPluginAiTurnApproval?.();
  stopPluginNotify?.();
  stopBackupRunNotify?.();
  stopBeforeQuit?.();
  stopPopupClose?.();
  stopPopupState?.();
  stopBrowserOpen?.();
  stopBrowserTabRequest?.();
  stopTerminalSessionFlusher?.();
  notifications.dispose();
  sshMfa.dispose();
});
</script>

<template>
  <PopupTabWindow v-if="popupWindow" />
  <div v-else class="app-frame" :class="{
      'app-frame--sidebar-labels': sidebarLabels,
      'app-frame--unified-titlebar': unifiedTitleBar,
      'is-plugin-immersive': !!pluginTakeover.immersive,
    }"
    id="app-project-drop-target"
    data-file-drop-target
    :data-file-drop-label="t('project.external.globalDropHint')"
  >
    <AppTabBar />
    <div
      class="app-workspace-shell"
      :class="{
        'is-ai-docked': assistant.open && assistant.layoutMode === 'docked' && !assistant.fullscreen && route.name !== 'ai',
      }"
      :style="{ '--ai-assistant-width': `${assistant.width}px` }"
    >
      <AppSidebar :settings-open="settingsOpen" @open-settings="openSettings()" />
      <div class="app-workspace-column">
      <main class="app-workspace-content" :aria-busy="routeViewPending">
        <div
          v-if="routeViewPending"
          class="app-workspace-route-progress"
          role="progressbar"
          :aria-label="t('common.loading')"
        ></div>
        <PoppedOutTabPlaceholder v-if="activePopupLabel" :label="activePopupLabel" />
        <RouterView v-else-if="routeSyncReady" v-slot="{ Component, route: activeRoute }">
          <KeepAlive
            :include="cachedWorkspaceViews"
            :max="workspaceCacheLimit || undefined"
          >
            <component
              :is="Component"
              v-if="activeRoute.name !== 'plugin-view'"
              :key="`${String(activeRoute.name)}-${routeStateScopeKey(activeRoute.name)}-${viewResetVersion}`"
            />
          </KeepAlive>
        </RouterView>
        <PluginWorkspaceView
          v-for="workspaceId in pluginWorkspaceIds"
          :key="`${workspaceId}-${viewResetVersion}`"
          :workspace-id="workspaceId"
          :visible="pluginLayerVisible && workspaceId === workbench.activeWorkspaceId"
        />
      </main>
      <AppStatusBar v-if="statusBarStore.contributions.length > 0" />
      </div>
    </div>
    <AppCommandPaletteHost
      v-if="commandPaletteOpenRequest.seq > 0"
      :open-request="commandPaletteOpenRequest"
      :shortcut-label="commandPaletteShortcut"
      @open-settings="openSettings()"
    />
    <PluginCommandParamsHost v-if="pluginCommandParamsHostLoaded" />
    <PluginServicesRoot v-if="pluginServicesReady" />
    <PluginImmersiveExit v-if="pluginTakeover.immersive" />
    <PluginHostUiLayer v-if="pluginHostUi.prompts.length || pluginHostUi.diffs.length" />
    <AiAssistantDrawer v-if="assistantLoaded" @open-settings="openSettings('ai')" />
    <LinkedNotesDrawer v-if="linkedNotesDrawer.rendered" />
    <SettingsWorkspaceView
      v-if="settingsLoaded"
      v-model="settingsOpen"
      :initial-category="settingsInitialCategory"
      :initial-plugin-id="settingsInitialPluginId"
    />
  </div>
  <SshMfaDialog v-if="sshMfa.current" />
</template>
