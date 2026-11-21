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
import { settingsNavigationRequest, type SettingsNavigationCategory } from "@/services/settings-navigation";
import { enqueueDatabaseFiles } from "@/services/external-file-open";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import {
  loadAiAssistantStore,
  peekAiAssistantStore,
  useAiAssistantShellStore,
  type AiAssistantStore,
} from "@/stores/ai-assistant-shell";
import { useWorkbenchStore } from "@/stores/workbench";
import { useNotificationsStore } from "@/stores/notifications";
import { useSshMfaStore } from "@/stores/ssh-mfa";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useAppUpdateStore } from "@/stores/app-update";
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
import { preloadWorkspaceView } from "@/router/workspace-views";
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
import { createStartupIdleBridgeCoordinator } from "@/services/startup-idle-bridges";
import { useCommandPaletteStore } from "@/stores/command-palette";
import { useKeybindingsStore } from "@/stores/keybindings";
import {
  parseDesktopAiConversationTarget,
  type NotificationTarget,
} from "@/domain/notifications/target";
import { hostGlobalShortcutErrorNotice } from "@/domain/keybindings/global-shortcut-notices";
import { BACKUP_RUN_EVENT } from "@/domain/database/backup-events";
import { drainPendingDesktopNavigation } from "@/services/desktop-navigation";
import { listenNotificationNavigation } from "@/services/notification-navigation";
import { AI_ASSISTANT_DROP_TARGET_ID } from "@/domain/ai/dropped-files";

const AppCommandPaletteHost = defineAsyncComponent(
  () => import("@/components/layout/AppCommandPaletteHost.vue")
);
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
  if (targetId === "database-workspace-drop-target") {
    enqueueDatabaseFiles(normalized);
    workbench.openView("database");
    await router.push({ name: "database" });
    return;
  }
  const databaseFiles: string[] = [];
  for (const file of normalized.filter(databaseFileCandidate)) {
    try {
      await invokeBackend("db_detect_file_type", { filePath: file });
      databaseFiles.push(file);
    } catch {
      // The file is not a supported database.
    }
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
  // 弹出窗口不渲染命令面板与侧栏。
if (!popupWindow) {
  void commandPalette.hydrate();
}
// 快捷键在弹出窗口也需要，因此两类窗口都加载键位并安装唯一的键盘分发器。
void keybindings.hydrate();
const uninstallKeybindingDispatcher = installKeybindingDispatcher();
const stopStartupIdleBridges = createStartupIdleBridgeCoordinator([
  ...(!popupWindow ? [
    {
      // New terminals can then create their WebGL renderer at open instead of swapping it in later.
      name: "terminal-webgl",
      load: () => import("@/services/terminal-preferences").then(({ preloadTerminalWebgl }) => preloadTerminalWebgl()),
      timeout: 5000,
    },
  ] : []),
]);
onBeforeUnmount(stopStartupIdleBridges);
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
function openSettings(initialCategory?: SettingsNavigationCategory) {
  settingsInitialCategory.value = initialCategory;
  settingsOpen.value = true;
}
watch(settingsNavigationRequest, (request) => {
  if (request.seq > 0) openSettings(request.category);
});
const commandPaletteOpenRequest = ref<CommandPaletteOpenRequest>({ seq: 0 });

const commandPaletteShortcut = computed(() => keybindings.label("builtin:command-palette"));

function openCommandPalette(request: Omit<CommandPaletteOpenRequest, "seq">) {
  if (popupWindow) return;
  commandPaletteOpenRequest.value = {
    seq: commandPaletteOpenRequest.value.seq + 1,
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
      projectRoot: "",
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
        projectRoot: "",
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
    void preloadWorkspaceView(workbench.activeTab?.view ?? "dashboard");
    const scopeId = workspaceStatePreferences.scopeIdFor(workbench.activeWorkspaceId);
    switchDatabaseWorkspaceScope(scopeId);
    const restoredTab = workbench.activeTab;
    const restoredView = restoredTab?.view ?? "dashboard";
    if (route.name !== restoredView) {
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
let stopBeforeQuit: (() => void) | undefined;
let stopNativeFileDrop: (() => void) | undefined;
let stopDesktopNavigate: (() => void) | undefined;
let stopBackupRunNotify: (() => void) | undefined;
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
      () => workbench.workspaces.map((workspace) => [
        workspace.id,
        workspace.name,
      ]).join("\u0000"),
    ],
    ([workspaceId]) => {
      if (!workbench.hydrated || !workspaceStatePreferences.hydrated) return;
      const scopeId = workspaceStatePreferences.scopeIdFor(workspaceId);
      syncAiWorkspaceDirectory();
      switchDatabaseWorkspaceScope(scopeId);
      const aiStore = peekAiAssistantStore();
      if (aiStore?.hydrated) {
        void aiStore.followTopWorkspace({
          workspaceId,
          workspaceName: workbench.activeWorkspace?.name,
          scopeId,
          projectRoot: "",
        });
      }
    },
    { flush: "sync" }
  );
  window.addEventListener("pagehide", flushPersistentUiState);
  window.addEventListener("beforeunload", flushPersistentUiState);
  watch(
    () => route.name,
    (routeName) => {
      if (!routeSyncReady.value || !workbench.hydrated || typeof routeName !== "string") return;
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
      if (target?.kind === "ai") {
        void assistant.show("agentic");
        return;
      }
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
    // Scheduled database backups run in Go even when the database view is closed.
    // The backup service is loaded on the first run so it stays out of the entry chunk.
    stopBackupRunNotify = listenAppEvent<unknown>(BACKUP_RUN_EVENT, (payload) => {
      void import("@/services/database-backup-service")
        .then(({ notifyBackupRunPayload }) => notifyBackupRunPayload(payload))
        .catch(() => undefined);
    });
    stopBeforeQuit = listenAppEvent("app:before-quit", () => {
      void flushPersistentUiState();
    });
    stopPopupClose = listenAppEvent<string>(POPUP_TAB_WINDOW_CLOSING_EVENT, (label) => {
      if (workbench.reclaimPoppedOutTabByLabel(label)) viewResetVersion.value += 1;
    });
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
  stopBackupRunNotify?.();
  stopBeforeQuit?.();
  stopPopupClose?.();
  stopPopupState?.();
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
    }"
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
              :key="`${String(activeRoute.name)}-${routeStateScopeKey(activeRoute.name)}-${viewResetVersion}`"
            />
          </KeepAlive>
        </RouterView>
      </main>
      </div>
    </div>
    <AppCommandPaletteHost
      v-if="commandPaletteOpenRequest.seq > 0"
      :open-request="commandPaletteOpenRequest"
      :shortcut-label="commandPaletteShortcut"
      @open-settings="openSettings()"
    />
    <AiAssistantDrawer v-if="assistantLoaded" @open-settings="openSettings('ai')" />
    <LinkedNotesDrawer v-if="linkedNotesDrawer.rendered" />
    <SettingsWorkspaceView
      v-if="settingsLoaded"
      v-model="settingsOpen"
      :initial-category="settingsInitialCategory"
    />
  </div>
  <SshMfaDialog v-if="sshMfa.current" />
</template>
