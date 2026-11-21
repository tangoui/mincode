<script setup lang="ts">
import { computed, defineAsyncComponent, h, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  Camera,
  ArrowRight,
  Check,
  CircleClose,
  Compass,
  Close,
  CopyDocument,
  DArrowLeft,
  DArrowRight,
  Delete,
  EditPen,
  FolderAdd,
  FolderOpened,
  Lock,
  MagicStick,
  Monitor,
  MoreFilled,
  Plus,
  RefreshRight,
  TopRight,
  Unlock,
} from "@element-plus/icons-vue";
import { ElMessage, ElMessageBox, type TabPaneName, type TabsPaneContext } from "element-plus";
import { useRoute, useRouter } from "vue-router";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { DEFAULT_WORKSPACE_ID, useWorkbenchStore } from "@/stores/workbench";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { canPopOutTab, closePopupWindow, popOutTab } from "@/services/popup-tab";
import { hydrateSettingsSnapshot } from "@/services/settings-persistence";
import { preloadWorkspaceView } from "@/router/workspace-views";
import { copyText, invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import type { LocalShellType, LocalTerminalPaneOptions } from "@/domain/terminal/types";
import { withWorkspaceCwd } from "@/domain/terminal/types";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { useTabAttention } from "@/composables/use-terminal-attention";
import { withCodexNotifications } from "@/domain/terminal/terminal-attention";
import TerminalAttentionMarker from "@/components/terminal/TerminalAttentionMarker.vue";
import MinCodeMark from "@/assets/brand/mincode-mark.svg";
import AppChevron from "@/components/ui/AppChevron.vue";
import AppWorkspaceTabs from "@/components/ui/AppWorkspaceTabs.vue";
import NotificationCenterButton from "@/components/layout/NotificationCenterButton.vue";
import BrowserLocalModeBadge from "@/components/layout/BrowserLocalModeBadge.vue";
import { useAiAssistantShellStore } from "@/stores/ai-assistant-shell";
import { recordClosedTabs } from "@/services/closed-tab-history";
import { useWorkbenchTabCommands } from "@/composables/use-workbench-tab-commands";
import { useWorkspaceManagement } from "@/composables/use-workspace-management";
import type { WorkspaceTab } from "@/types/workspace";
import { focusInlineInput } from "@/domain/ui/focus-inline-input";

// The AI task button stays lazy (it pulls the agent-run stores); the placeholder keeps its
// header slot so the top bar does not shift when the chunk arrives.
const AiTaskProgressButton = defineAsyncComponent({
  loader: () => import("@/components/layout/AiTaskProgressButton.vue"),
  loadingComponent: () =>
    h("span", { class: "app-header-button", style: "pointer-events: none", "aria-hidden": "true" }),
  delay: 0,
});

const route = useRoute();
const router = useRouter();
const workbench = useWorkbenchStore();
const terminalWorkspace = useTerminalWorkspaceStore();
const assistant = useAiAssistantShellStore();
const workspaceStateScope = useWorkspaceStateScope();
const workspaceStatePreferences = workspaceStateScope.preferences;
const workspaceStateScopeId = workspaceStateScope.scopeId;
const { t } = useVueI18n();
const { tabAttention, dismissTabAttention } = useTabAttention();
const tabsHost = ref<HTMLElement>();
const tabsHostWidth = ref(0);
const renamingTabId = ref("");
const renameValue = ref("");
let observer: ResizeObserver | undefined;
let stopTabPopoutDrag: (() => void) | undefined;

const tabPartition = computed(() => {
  const tabs = workbench.workspaceTabs;
  if (!tabs.length) return { visible: [], overflow: [] };

  const widths = tabs.map(workspaceTabWidth);
  const fitWidths = tabs.map(workspaceTabFitWidth);
  const hostWidth = tabsHostWidth.value;
  const totalWidth = widths.reduce((total, width) => total + width, 0);
  const totalFitWidth = fitWidths.reduce((total, width) => total + width, 0);
  if (!hostWidth || totalWidth <= hostWidth || totalFitWidth <= hostWidth) {
    return { visible: tabs, overflow: [] };
  }

  const availableWidth = Math.max(0, hostWidth - OVERFLOW_TRIGGER_WIDTH_PX);
  const activeIndex = Math.max(0, tabs.findIndex((tab) => tab.id === workbench.activeTabId));
  let start = 0;
  let end = 0;
  let usedWidth = 0;

  while (end < tabs.length) {
    const nextWidth = fitWidths[end] ?? TOP_TAB_MIN_WIDTH_PX;
    if (end > start && usedWidth + nextWidth > availableWidth) break;
    usedWidth += nextWidth;
    end += 1;
  }

  if (activeIndex >= end) {
    start = activeIndex;
    end = activeIndex + 1;
    usedWidth = fitWidths[activeIndex] ?? TOP_TAB_MIN_WIDTH_PX;

    while (start > 0) {
      const previousWidth = fitWidths[start - 1] ?? TOP_TAB_MIN_WIDTH_PX;
      if (usedWidth + previousWidth > availableWidth) break;
      start -= 1;
      usedWidth += previousWidth;
    }

    while (end < tabs.length) {
      const nextWidth = fitWidths[end] ?? TOP_TAB_MIN_WIDTH_PX;
      if (usedWidth + nextWidth > availableWidth) break;
      usedWidth += nextWidth;
      end += 1;
    }
  }

  const visible = tabs.slice(start, end);
  const visibleIds = new Set(visible.map((tab) => tab.id));
  return { visible, overflow: tabs.filter((tab) => !visibleIds.has(tab.id)) };
});
const visibleTabs = computed(() => tabPartition.value.visible);
const overflowTabs = computed(() => tabPartition.value.overflow);
// Tabs hidden in the overflow menu still need to show that a terminal wants the user.
const overflowAttention = computed(() => {
  for (const tab of overflowTabs.value) {
    const attention = tabAttention(tab);
    if (attention) return attention;
  }
  return undefined;
});

const SHELL_COMMAND_PREFIX = "shell:";
const AI_COMMAND_PREFIX = "ai:";
const PRESET_AI_COMMANDS = [
  "claude",
  "gemini",
  "codex",
  "kimi",
  "glm",
  "opencode",
  "qodercn",
  "qoder",
  "grok",
  "claude --model sonnet",
] as const;
const PRESET_SHELL_TYPES_UNIX: readonly LocalShellType[] = ["bash", "zsh", "fish"];
const PRESET_SHELL_TYPES_WINDOWS: readonly LocalShellType[] = ["cmd", "powershell", "pwsh", "wsl"];

const shellPresets = computed(() =>
  navigator.platform.startsWith("Win") ? PRESET_SHELL_TYPES_WINDOWS : PRESET_SHELL_TYPES_UNIX
);

function tabTitle(tab: (typeof workbench.tabs)[number]) {
  return tab.title || t(tab.titleKey as MessageKey);
}
function isWorkspaceTabClosable(tab: (typeof workbench.tabs)[number]) {
  return !tab.pinned;
}
function warmWorkspaceView(view: (typeof workbench.tabs)[number]["view"]) {
  void preloadWorkspaceView(view);
}
function currentRouteMatchesTab(tab: (typeof workbench.tabs)[number] | undefined) {
  return route.name === (tab?.view ?? "dashboard");
}
function navigateToTab(tab: (typeof workbench.tabs)[number] | undefined) {
  if (tab) warmWorkspaceView(tab.view);
  if (currentRouteMatchesTab(tab)) return;
  void router.push(workspaceTabRoute(tab));
}
function navigateToActiveTab() {
  navigateToTab(workbench.activeTab);
}
const {
  workspaceDialogVisible,
  workspaceEditingId,
  workspaceName,
  workspaceWorktreeEnabled,
  workspaceRepoPath,
  workspaceBranch,
  workspaceCreating,
  pendingWorktreePath,
  workspaceDialogReady,
  preloadWorkspaceConversation,
  switchWorkspace,
  openCreateWorkspace,
  openRenameWorkspace,
  saveWorkspaceRename,
  cancelWorkspaceRename,
  chooseWorkspaceRepo,
  saveWorkspaceDialog,
  deleteWorkspace,
  permanentlyDeleteWorkspace,
  restoreWorkspace,
} = useWorkspaceManagement({ navigateToTab, navigateToActiveTab });
function warmTab(id: string) {
  const tab = workbench.tabs.find((item) => item.id === id);
  if (tab) warmWorkspaceView(tab.view);
}
function selectTab(id: string) {
  const existing = workbench.tabs.find((item) => item.id === id);
  if (!existing || (id === workbench.activeTabId && currentRouteMatchesTab(existing))) return;
  const tab = workbench.selectTab(id);
  if (tab) navigateToTab(tab);
}
async function closeWorkspaceTab(id: string) {
  const tab = workbench.tabs.find((item) => item.id === id);
  if (!tab || !isWorkspaceTabClosable(tab)) return;

  const popupLabel = workbench.poppedOutTabLabels[id];
  if (popupLabel) {
    try {
      await closePopupWindow(popupLabel);
    } catch {
      ElMessage.warning(t("tab.popupUnavailable"));
    }
  }

  recordClosedTabs([tab]);
  workbench.closeTab(id, true);
  navigateToActiveTab();
}
// Use tab-click (not update:modelValue) so clicking the active tab still restores its route.
function handleWorkspaceTabClick(pane: TabsPaneContext) {
  if (pane.paneName === undefined) return;
  // Clicking a terminal tab, even the active one, acknowledges its bell.
  const tab = workbench.tabs.find((item) => item.id === String(pane.paneName));
  if (tab) dismissTabAttention(tab);
  selectTab(String(pane.paneName));
}
function removeWorkspaceTab(name: TabPaneName) {
  void closeWorkspaceTab(String(name));
}
function closeTab(event: Event, id: string) {
  event.preventDefault();
  event.stopPropagation();
  void closeWorkspaceTab(id);
}
function createTerminalTab(options: LocalTerminalPaneOptions, title?: string) {
  const terminalCount = workbench.workspaceTabs.filter((tab) => tab.view === "terminal").length;
  const tab = workbench.createTab(
    "terminal",
    title || (terminalCount ? `${t("nav.terminal")} ${terminalCount + 1}` : undefined)
  );
  if (!tab) return undefined;

  const target = resolveTerminalLayoutTarget(
    workbench.tabs,
    tab,
    workspaceStatePreferences.isolationEnabled,
    workspaceStateScopeId.value
  );
  terminalWorkspace.activateTab(
    tab.id,
    withWorkspaceCwd(options, workbench.activeWorktreePath),
    target.layoutId,
    target.fallbackLayoutId
  );
  navigateToTab(tab);
  return tab;
}
useWorkbenchTabCommands({
  selectTab,
  closeTab: closeWorkspaceTab,
  navigateTo: navigateToTab,
  newTerminal: (title) => {
    createTerminalTab({ shellType: "system" }, title);
  },
});
function openAiAssistant() {
  void assistant.show("agentic");
}
async function captureScreenshot() {
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  try {
    await invokeBackend<void>("capture_screenshot");
  } catch {
    ElMessage.error(t("common.screenshotFailed"));
  }
}
async function defaultAiCommand() {
  const settings = await hydrateSettingsSnapshot();
  if (!settings || typeof settings !== "object" || Array.isArray(settings)) return "claude";
  const command = (settings as { aiCommand?: unknown }).aiCommand;
  return typeof command === "string" && command.trim() ? command.trim() : "claude";
}

function openBrowser() {
  const tab = workbench.openView("browser");
  navigateToTab(tab);
}

async function createTerminal(command: string) {
  if (command === "browser") {
    openBrowser();
    return;
  }

  const aiCommand =
    command === `${AI_COMMAND_PREFIX}default`
      ? await defaultAiCommand()
      : command.startsWith(AI_COMMAND_PREFIX)
        ? command.slice(AI_COMMAND_PREFIX.length)
        : undefined;

  if (aiCommand) {
    createTerminalTab({ initialCommand: withCodexNotifications(aiCommand) });
    return;
  }

  const shellType = command.startsWith(SHELL_COMMAND_PREFIX)
    ? (command.slice(SHELL_COMMAND_PREFIX.length) as LocalShellType)
    : "system";
  createTerminalTab({ shellType });
}
async function openRename(event: Event, id: string) {
  event.preventDefault();
  event.stopPropagation();
  const tab = workbench.tabs.find((item) => item.id === id);
  if (!tab) return;
  renamingTabId.value = id;
  renameValue.value = tabTitle(tab);
  await nextTick();
  focusInlineInput(`[data-tab-rename-input="${id}"]`);
}
function saveRename() {
  if (!renamingTabId.value) return;
  const name = renameValue.value.trim();
  if (name) workbench.renameTab(renamingTabId.value, name);
  renamingTabId.value = "";
}
function cancelRename() {
  renamingTabId.value = "";
}
function duplicateTab(id: string) {
  const source = workbench.tabs.find((item) => item.id === id);
  if (!source) return;
  const sourceTarget =
    source.view === "terminal"
      ? resolveTerminalLayoutTarget(
          workbench.tabs,
          source,
          workspaceStatePreferences.isolationEnabled,
          workspaceStateScopeId.value
        )
      : undefined;
  const tab = workbench.duplicateTab(id, t("tab.copySuffix"));
  if (!tab) return;
  if (sourceTarget && tab.id !== source.id) {
    const target = resolveTerminalLayoutTarget(
      workbench.tabs,
      tab,
      workspaceStatePreferences.isolationEnabled,
      workspaceStateScopeId.value
    );
    terminalWorkspace.duplicateTab(sourceTarget.layoutId, target.layoutId);
  }
  navigateToTab(tab);
}
function moveTab(id: string, workspaceId: string) {
  const tab = workbench.moveTabToWorkspace(id, workspaceId);
  if (tab) navigateToTab(tab);
}
function closeTabGroup(action: "left" | "right" | "other", id: string) {
  const before = [...workbench.workspaceTabs];
  recordClosedTabsAfter(before, () => {
    if (action === "left") workbench.closeTabsToLeft(id);
    else if (action === "right") workbench.closeTabsToRight(id);
    else workbench.closeOtherTabs(id);
  });
  navigateToActiveTab();
}
/** Runs `close` and records the workbench tabs it removed for reopen-closed. */
function recordClosedTabsAfter(before: readonly WorkspaceTab[], close: () => void) {
  close();
  const remaining = new Set(workbench.tabs.map((tab) => tab.id));
  recordClosedTabs(before.filter((tab) => !remaining.has(tab.id)));
}
async function copyTabName(id: string) {
  const tab = workbench.tabs.find((item) => item.id === id);
  if (!tab) return;
  try {
    await copyText(tabTitle(tab));
  } catch {
    /* clipboard unavailable */
  }
}
async function popOutWorkspaceTab(id: string) {
  const tab = workbench.tabs.find((item) => item.id === id);
  if (!tab || workbench.poppedOutTabLabels[id]) return;
  if (!isDesktopRuntime()) {
    ElMessageBox.alert(t("tab.popoutDesktopOnly"), t("common.tip"));
    return;
  }
  if (!canPopOutTab(tab)) {
    ElMessageBox.alert(t("tab.popupUnsupported"), t("common.tip"));
    return;
  }
  try {
    const label = await popOutTab(tab, tabTitle(tab));
    workbench.markTabPoppedOut(id, label);
  } catch {
    ElMessageBox.alert(t("tab.popupUnavailable"), t("common.tip"));
  }
}

function handleTabPointerDown(event: PointerEvent, id: string) {
  warmTab(id);
  startPopoutDrag(event, id);
}

function startPopoutDrag(event: PointerEvent, id: string) {
  const tab = workbench.tabs.find((item) => item.id === id);
  if (!tab || event.button !== 0 || !isDesktopRuntime() || !canPopOutTab(tab)) return;
  const startX = event.clientX;
  const startY = event.clientY;
  let popped = false;
  const cleanup = () => {
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", cleanup);
    window.removeEventListener("pointercancel", cleanup);
    stopTabPopoutDrag = undefined;
  };
  const onMove = (move: PointerEvent) => {
    if (popped || workbench.poppedOutTabLabels[id]) return;
    const dx = move.clientX - startX;
    const dy = move.clientY - startY;
    if (dy < 48 || Math.abs(dy) < Math.abs(dx) * 1.15) return;
    popped = true;
    cleanup();
    void popOutWorkspaceTab(id);
  };
  stopTabPopoutDrag?.();
  stopTabPopoutDrag = cleanup;
  window.addEventListener("pointermove", onMove);
  window.addEventListener("pointerup", cleanup);
  window.addEventListener("pointercancel", cleanup);
}

type TabMenuHandle = { handleClose: () => void };
const tabMenuRefs = new Map<string, TabMenuHandle>();

function setTabMenuRef(tabId: string, menu: unknown) {
  if (menu) tabMenuRefs.set(tabId, menu as TabMenuHandle);
  else tabMenuRefs.delete(tabId);
}

// Context-menu triggers don't emit the click that closes other dropdowns, so keep only one open.
function handleTabMenuVisible(tabId: string, visible: boolean) {
  if (visible) {
    for (const [id, menu] of tabMenuRefs) if (id !== tabId) menu.handleClose();
  }
}

function handleTabCommand(tabId: string, command: string | number | object) {
  const value = String(command);
  if (value === "rename") openRename(new Event("contextmenu"), tabId);
  else if (value === "copy") void copyTabName(tabId);
  else if (value === "duplicate") duplicateTab(tabId);
  else if (value === "pin") workbench.togglePinTab(tabId);
  else if (value === "popout") void popOutWorkspaceTab(tabId);
  else if (value === "close-left") closeTabGroup("left", tabId);
  else if (value === "close-right") closeTabGroup("right", tabId);
  else if (value === "close-other") closeTabGroup("other", tabId);
  else if (value === "close") {
    void closeWorkspaceTab(tabId);
  } else if (value.startsWith("move:")) moveTab(tabId, value.slice(5));
}
const TOP_TAB_WIDTH_PX = 120;
const TOP_TAB_MIN_WIDTH_PX = 88;
const PINNED_TAB_MIN_WIDTH_PX = 76;
const PINNED_TAB_MAX_WIDTH_PX = 126;
const PINNED_TAB_LABEL_CHROME_PX = 28;
const OVERFLOW_TRIGGER_WIDTH_PX = 42;
const TOP_TAB_ITEM_CHROME_PX = 7;
const TOP_TAB_CLOSE_WIDTH_PX = 18;
let tabTextMeasureContext: CanvasRenderingContext2D | null | undefined;

function measureTabTitle(text: string) {
  if (tabTextMeasureContext === undefined) {
    const canvas = typeof document === "undefined" ? null : document.createElement("canvas");
    tabTextMeasureContext = canvas?.getContext("2d") ?? null;
    if (tabTextMeasureContext) {
      tabTextMeasureContext.font =
        '600 11px -apple-system, BlinkMacSystemFont, "SF Pro Text", "PingFang SC", sans-serif';
    }
  }
  return tabTextMeasureContext?.measureText(text).width ?? Array.from(text).length * 11;
}

function workspaceTabWidth(tab: (typeof workbench.tabs)[number]) {
  if (!tab.pinned) return TOP_TAB_WIDTH_PX;

  const titleWidth = Math.ceil(measureTabTitle(tabTitle(tab)));
  return Math.min(
    PINNED_TAB_MAX_WIDTH_PX,
    Math.max(PINNED_TAB_MIN_WIDTH_PX, titleWidth + PINNED_TAB_LABEL_CHROME_PX)
  );
}

function workspaceTabFitWidth(tab: (typeof workbench.tabs)[number]) {
  if (!tab.pinned) return TOP_TAB_MIN_WIDTH_PX;
  return workspaceTabWidth(tab);
}

function workspaceTabDisplayWidth(tab: (typeof workbench.tabs)[number]) {
  if (tab.pinned) return workspaceTabWidth(tab);

  const regularTabs = visibleTabs.value.filter((item) => !item.pinned);
  if (!regularTabs.length) return TOP_TAB_WIDTH_PX;
  const pinnedWidth = visibleTabs.value
    .filter((item) => item.pinned)
    .reduce((total, item) => total + workspaceTabWidth(item), 0);
  const overflowWidth = overflowTabs.value.length ? OVERFLOW_TRIGGER_WIDTH_PX : 0;
  const availableForRegularTabs = Math.max(
    0,
    tabsHostWidth.value - overflowWidth - pinnedWidth
  );
  return Math.min(
    TOP_TAB_WIDTH_PX,
    Math.max(TOP_TAB_MIN_WIDTH_PX, availableForRegularTabs / regularTabs.length)
  );
}

// el-tabs owns the item chrome (right padding, close icon, divider); size the label to fill the rest.
function workspaceTabLabelWidth(tab: (typeof workbench.tabs)[number]) {
  const closeWidth =
    isWorkspaceTabClosable(tab) && renamingTabId.value !== tab.id ? TOP_TAB_CLOSE_WIDTH_PX : 0;
  return Math.max(
    0,
    Math.floor(workspaceTabDisplayWidth(tab)) - TOP_TAB_ITEM_CHROME_PX - closeWidth
  );
}

function calculateVisibleTabs() {
  tabsHostWidth.value = tabsHost.value?.clientWidth ?? 0;
}

watch([() => workbench.workspaceTabs.length, () => workbench.activeWorkspaceId], async () => {
  await nextTick();
  calculateVisibleTabs();
});

onMounted(async () => {
  await nextTick();
  calculateVisibleTabs();
  if (tabsHost.value) {
    observer = new ResizeObserver(calculateVisibleTabs);
    observer.observe(tabsHost.value);
  }
});
onBeforeUnmount(() => {
  observer?.disconnect();
  stopTabPopoutDrag?.();
});
</script>

<template>
  <header class="app-top-tabs">
    <div class="app-brand-mark" aria-label="MinCode">
      <img :src="MinCodeMark" alt="" />
    </div>
    <div ref="tabsHost" class="app-top-tabs__scroll">
      <AppWorkspaceTabs
        class="app-top-tabs__workspace-tabs"
        :model-value="workbench.activeTabId"
        @tab-click="handleWorkspaceTabClick"
        @tab-remove="removeWorkspaceTab"
      >
        <el-tab-pane
          v-for="tab in visibleTabs"
          :key="tab.id"
          :name="tab.id"
          :closable="isWorkspaceTabClosable(tab) && renamingTabId !== tab.id"
        >
          <template #label>
            <el-dropdown
              :ref="(menu: unknown) => setTabMenuRef(tab.id, menu)"
              trigger="contextmenu"
              placement="bottom-start"
              popper-class="app-tab-context-popper"
              class="app-workspace-tab-menu"
              :style="{ width: `${workspaceTabLabelWidth(tab)}px` }"
              @command="handleTabCommand(tab.id, $event)"
              @visible-change="handleTabMenuVisible(tab.id, $event)"
            >
              <span
                class="app-workspace-tab"
                :class="{ 'is-pinned': tab.pinned }"
                @pointerenter="warmTab(tab.id)"
                @pointerdown="handleTabPointerDown($event, tab.id)"
              >
                <span
                  v-if="renamingTabId === tab.id"
                  class="app-workspace-tab__rename"
                  @click.stop
                  @pointerdown.stop
                  @mousedown.stop
                  @keydown.stop
                >
                  <el-input
                    v-model="renameValue"
                    class="app-inline-rename"
                    :data-tab-rename-input="tab.id"
                    maxlength="40"
                    @keydown.enter.prevent="saveRename"
                    @keydown.esc.prevent="cancelRename"
                    @blur="saveRename"
                  />
                </span>
                <template v-else>
                  <el-icon v-if="tab.pinned" class="app-workspace-tab__pin"><Lock /></el-icon>
                  <TerminalAttentionMarker v-if="tabAttention(tab)" :attention="tabAttention(tab)!" />
                  <span class="app-workspace-tab__label">{{ tabTitle(tab) }}</span>
                </template>
              </span>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="rename"
                    ><el-icon><EditPen /></el-icon>{{ t("tab.rename") }}</el-dropdown-item
                  >
                  <el-dropdown-item command="copy"
                    ><el-icon><CopyDocument /></el-icon>{{ t("tab.copyName") }}</el-dropdown-item
                  >
                  <el-dropdown-item command="duplicate"
                    ><el-icon><CopyDocument /></el-icon>{{ t("tab.duplicate") }}</el-dropdown-item
                  >
                  <el-dropdown-item command="pin"
                    ><el-icon><Unlock v-if="tab.pinned" /><Lock v-else /></el-icon
                    >{{ tab.pinned ? t("tab.unpin") : t("tab.pin") }}</el-dropdown-item
                  >
                  <el-dropdown-item
                    command="popout"
                    :disabled="Boolean(workbench.poppedOutTabLabels[tab.id]) || !canPopOutTab(tab)"
                    ><el-icon><TopRight /></el-icon
                    >{{
                      workbench.poppedOutTabLabels[tab.id] ? t("tab.poppedOut") : t("tab.popout")
                    }}</el-dropdown-item
                  >
                  <el-dropdown-item
                    v-for="workspace in workbench.workspaces.filter(
                      (item) => item.id !== tab.workspaceId
                    )"
                    :key="workspace.id"
                    :command="`move:${workspace.id}`"
                    :disabled="Boolean(workbench.poppedOutTabLabels[tab.id])"
                    divided
                  >
                    <el-icon><FolderOpened /></el-icon
                    >{{ t("tab.moveToWorkspace", { name: workspace.name }) }}
                  </el-dropdown-item>
                  <el-dropdown-item command="close-left" divided
                    ><el-icon><DArrowLeft /></el-icon>{{ t("tab.closeLeft") }}</el-dropdown-item
                  >
                  <el-dropdown-item command="close-right"
                    ><el-icon><DArrowRight /></el-icon>{{ t("tab.closeRight") }}</el-dropdown-item
                  >
                  <el-dropdown-item command="close-other"
                    ><el-icon><CircleClose /></el-icon>{{ t("tab.closeOther") }}</el-dropdown-item
                  >
                  <el-dropdown-item command="close" :disabled="Boolean(tab.pinned)" divided
                    ><el-icon><Close /></el-icon>{{ t("common.closeTab") }}</el-dropdown-item
                  >
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-tab-pane>
      </AppWorkspaceTabs>

      <el-dropdown
        v-if="overflowTabs.length"
        trigger="click"
        :hide-on-click="false"
        popper-class="app-tabs-overflow-popper"
      >
        <el-button
          text
          native-type="button"
          class="app-tabs-overflow"
          :aria-label="t('tab.allTabs')"
        >
          <el-icon><MoreFilled /></el-icon><b>{{ overflowTabs.length }}</b>
          <TerminalAttentionMarker v-if="overflowAttention" :attention="overflowAttention" />
        </el-button>
        <template #dropdown>
          <el-dropdown-menu class="app-tabs-overflow-menu">
            <el-scrollbar class="app-tabs-overflow-menu__scroll">
              <li v-for="tab in overflowTabs" :key="tab.id" role="none" class="app-overflow-tab-item">
                <el-input
                  v-if="renamingTabId === tab.id"
                  v-model="renameValue"
                  class="app-inline-rename app-overflow-tab-rename"
                  :data-tab-rename-input="tab.id"
                  maxlength="40"
                  @click.stop
                  @mousedown.stop
                  @keydown.enter.stop.prevent="saveRename"
                  @keydown.esc.stop.prevent="cancelRename"
                  @blur="saveRename"
                />
                <el-button
                  v-else
                  text
                  native-type="button"
                  class="app-overflow-tab-select"
                  @pointerenter="warmTab(tab.id)"
                  @focus="warmTab(tab.id)"
                  @click="selectTab(tab.id)"
                >
                  <el-icon v-if="tab.pinned"><Lock /></el-icon>
                  <TerminalAttentionMarker v-if="tabAttention(tab)" :attention="tabAttention(tab)!" />
                  <span class="app-overflow-tab-title">{{ tabTitle(tab) }}</span>
                </el-button>
                <el-button
                  text
                  circle
                  native-type="button"
                  class="app-overflow-tab-edit"
                  :aria-label="t('tab.rename')"
                  @click="openRename($event, tab.id)"
                >
                  <el-icon><EditPen /></el-icon>
                </el-button>
                <el-button
                  v-if="isWorkspaceTabClosable(tab)"
                  text
                  circle
                  native-type="button"
                  class="app-overflow-tab-close"
                  :aria-label="t('common.closeTab')"
                  @click="closeTab($event, tab.id)"
                >
                  <el-icon><Close /></el-icon>
                </el-button>
              </li>
            </el-scrollbar>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>

    <div class="app-window-actions">
      <BrowserLocalModeBadge />
      <el-dropdown
        trigger="click"
        placement="bottom-end"
        :hide-on-click="false"
        popper-class="app-workspace-switcher-popper"
      >
        <el-button
          text
          native-type="button"
          class="app-header-button app-header-selector"
          :aria-label="t('tab.switchWorkspace')"
        >
          <el-icon><FolderOpened /></el-icon
          ><span class="app-header-selector__name">{{
            workbench.activeWorkspace?.name ?? t("tab.defaultWorkspace")
          }}</span
          ><AppChevron class="app-header-chevron" direction="down" />
        </el-button>
        <template #dropdown>
          <el-dropdown-menu class="app-workspace-menu">
            <el-scrollbar class="app-workspace-menu__scroll">
            <li
              v-for="workspace in workbench.workspaces"
              :key="workspace.id"
              role="none"
              class="app-workspace-menu__item"
            >
              <el-input
                v-if="workspaceEditingId === workspace.id"
                v-model="workspaceName"
                class="app-inline-rename app-workspace-menu__rename"
                :data-workspace-rename-input="workspace.id"
                maxlength="40"
                @click.stop
                @mousedown.stop
                @keydown.enter.stop.prevent="saveWorkspaceRename"
                @keydown.esc.stop.prevent="cancelWorkspaceRename"
                @blur="saveWorkspaceRename"
              />
              <el-button
                v-else
                text
                native-type="button"
                class="app-workspace-menu__select"
                @pointerenter="preloadWorkspaceConversation(workspace.id)"
                @focus="preloadWorkspaceConversation(workspace.id)"
                @click="switchWorkspace(workspace.id)"
              >
                <el-icon
                  ><Check v-if="workspace.id === workbench.activeWorkspaceId" /><FolderOpened
                    v-else
                /></el-icon>
                <span class="app-workspace-menu__copy">
                  <strong>{{ workspace.name }}</strong>
                  <small
                    >{{ workbench.tabs.filter((tab) => tab.workspaceId === workspace.id).length }}
                    {{ t("tab.tabsCount") }}</small
                  >
                </span>
              </el-button>
              <el-button
                text
                circle
                native-type="button"
                class="app-workspace-menu__action"
                :aria-label="t('tab.renameWorkspace')"
                @click="openRenameWorkspace($event, workspace.id)"
              >
                <el-icon><EditPen /></el-icon>
              </el-button>
              <el-button
                v-if="workspace.id !== DEFAULT_WORKSPACE_ID"
                text
                circle
                native-type="button"
                class="app-workspace-menu__action is-danger"
                :aria-label="t('tab.deleteWorkspace')"
                @click="deleteWorkspace($event, workspace.id)"
              >
                <el-icon><Delete /></el-icon>
              </el-button>
            </li>
            <template v-if="workbench.deletedWorkspaces.length">
              <li role="presentation" class="app-workspace-menu__section-label">
                {{ t("tab.recentlyDeletedWorkspaces") }}
              </li>
              <li
                v-for="record in workbench.deletedWorkspaces"
                :key="record.workspace.id"
                role="none"
                class="app-workspace-menu__item app-workspace-menu__item--deleted"
              >
                <div class="app-workspace-menu__deleted-copy">
                  <strong>{{ record.workspace.name }}</strong>
                  <small>{{ t("tab.workspaceDataRetained") }}</small>
                </div>
                <el-button
                  text
                  native-type="button"
                  class="app-workspace-menu__restore"
                  :aria-label="t('tab.restoreWorkspace')"
                  @click="restoreWorkspace($event, record.workspace.id)"
                >
                  <el-icon><RefreshRight /></el-icon>{{ t("tab.restoreWorkspace") }}
                </el-button>
                <el-button
                  text
                  circle
                  native-type="button"
                  class="app-workspace-menu__action is-danger"
                  :aria-label="t('tab.permanentlyDeleteWorkspace')"
                  @click="permanentlyDeleteWorkspace($event, record.workspace.id)"
                >
                  <el-icon><Delete /></el-icon>
                </el-button>
              </li>
            </template>
            <li role="none" class="app-workspace-menu__create">
              <el-button text native-type="button" @click="openCreateWorkspace">
                <el-icon><FolderAdd /></el-icon>{{ t("tab.newWorkspace") }}
              </el-button>
            </li>
            </el-scrollbar>
          </el-dropdown-menu>
        </template>
      </el-dropdown>

      <el-dropdown
        trigger="click"
        placement="bottom-end"
        popper-class="app-new-tab-dropdown"
        @command="createTerminal"
      >
        <el-button
          text
          native-type="button"
          class="app-header-button app-header-button--new-tab"
          :aria-label="t('common.newTab')"
          @pointerenter="warmWorkspaceView('terminal')"
          @focus="warmWorkspaceView('terminal')"
        >
          <el-icon><Plus /></el-icon><AppChevron class="app-header-chevron" direction="down" />
        </el-button>
        <template #dropdown>
          <el-dropdown-menu class="app-new-tab-menu">
            <el-scrollbar class="app-new-tab-menu__scroll">
              <li class="app-new-tab-menu__label">{{ t("tab.newMenu.shellGroup") }}</li>
              <el-dropdown-item
                class="app-new-tab-menu__item"
                :command="`${SHELL_COMMAND_PREFIX}system`"
                @pointerenter="warmWorkspaceView('terminal')"
              >
                <el-icon><Monitor /></el-icon><span>{{ t("tab.newMenu.defaultShell") }}</span>
              </el-dropdown-item>
              <el-dropdown-item
                v-for="shell in shellPresets"
                :key="shell"
                class="app-new-tab-menu__item"
                :command="`${SHELL_COMMAND_PREFIX}${shell}`"
                @pointerenter="warmWorkspaceView('terminal')"
              >
                <el-icon class="app-new-tab-menu__command-icon"><ArrowRight /></el-icon>
                <span>{{ t(`tab.newMenu.shell.${shell}` as MessageKey) }}</span>
              </el-dropdown-item>

              <li class="app-new-tab-menu__separator" role="separator" />
              <li class="app-new-tab-menu__label">{{ t("tab.newMenu.browserGroup") }}</li>
              <el-dropdown-item
                class="app-new-tab-menu__item"
                command="browser"
                @pointerenter="warmWorkspaceView('browser')"
              >
                <el-icon><Compass /></el-icon><span>{{ t("tab.newMenu.browser") }}</span>
              </el-dropdown-item>

              <li class="app-new-tab-menu__separator" role="separator" />
              <li class="app-new-tab-menu__label">{{ t("tab.newMenu.aiGroup") }}</li>
              <el-dropdown-item
                class="app-new-tab-menu__item"
                :command="`${AI_COMMAND_PREFIX}default`"
              >
                <el-icon><MagicStick /></el-icon><span>{{ t("tab.newMenu.defaultAI") }}</span>
              </el-dropdown-item>
              <el-dropdown-item
                v-for="command in PRESET_AI_COMMANDS"
                :key="command"
                class="app-new-tab-menu__item"
                :command="`${AI_COMMAND_PREFIX}${command}`"
              >
                <el-icon class="app-new-tab-menu__command-icon"><ArrowRight /></el-icon><span>{{ command }}</span>
              </el-dropdown-item>
            </el-scrollbar>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <el-button
        text
        native-type="button"
        class="app-header-button app-header-button--agent"
        :aria-label="t('ai.agentic')"
        :title="t('ai.agentic')"
        @click="openAiAssistant"
      >
        <el-icon><MagicStick /></el-icon>
      </el-button>
      <el-button
        text
        native-type="button"
        class="app-header-button"
        :aria-label="t('common.screenshot')"
        :title="t('common.screenshot')"
        @click="void captureScreenshot()"
      >
        <el-icon><Camera /></el-icon>
      </el-button>
      <AiTaskProgressButton />
      <NotificationCenterButton />
    </div>
  </header>

  <el-dialog
    v-model="workspaceDialogVisible"
    :title="t('tab.newWorkspace')"
    width="420px"
    append-to-body
    @keyup.enter="saveWorkspaceDialog"
  >
    <el-input
      v-model="workspaceName"
      autofocus
      maxlength="40"
      :placeholder="t('tab.workspaceNamePlaceholder')"
    />
    <el-checkbox v-model="workspaceWorktreeEnabled" class="workspace-worktree-toggle">
      {{ t("tab.workspaceWorktree") }}
    </el-checkbox>
    <template v-if="workspaceWorktreeEnabled">
      <p class="workspace-worktree-hint">{{ t("tab.workspaceWorktreeHint") }}</p>
      <label class="workspace-worktree-field">
        <span>{{ t("tab.workspaceRepo") }}</span>
        <el-input
          v-model="workspaceRepoPath"
          size="small"
          :placeholder="t('tab.workspaceRepoPlaceholder')"
        >
          <template #append>
            <el-button :icon="FolderOpened" @click="chooseWorkspaceRepo" />
          </template>
        </el-input>
      </label>
      <label class="workspace-worktree-field">
        <span>{{ t("tab.workspaceBranch") }}</span>
        <el-input
          v-model="workspaceBranch"
          size="small"
          maxlength="120"
          :placeholder="t('tab.workspaceBranchPlaceholder')"
        />
      </label>
      <p v-if="pendingWorktreePath" class="workspace-worktree-hint">
        {{ t("tab.workspaceWorktreePath") }}: {{ pendingWorktreePath }}
      </p>
    </template>
    <template #footer
      ><el-button :disabled="workspaceCreating" @click="workspaceDialogVisible = false">{{
        t("common.cancel")
      }}</el-button
      ><el-button
        type="primary"
        :loading="workspaceCreating"
        :disabled="!workspaceDialogReady"
        @click="saveWorkspaceDialog"
        >{{ t("common.save") }}</el-button
      ></template
    >
  </el-dialog>
</template>
