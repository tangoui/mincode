<script setup lang="ts">
import { computed, nextTick, onActivated, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { storeToRefs } from "pinia";
import { useRoute, useRouter } from "vue-router";
import SshConnectionDialog from "@/components/ssh/SshConnectionDialog.vue";
import TerminalPaneLayout from "@/components/terminal/TerminalPaneLayout.vue";
import TerminalTabStrip, { type TerminalTabAddMenuCommand } from "@/components/terminal/TerminalTabStrip.vue";
import ShellConfigurationDialog from "@/components/terminal/ShellConfigurationDialog.vue";
import type { SshConnectionDraft } from "@/domain/ssh/types";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import type {
  LocalShellType,
  LocalTerminalPaneOptions,
  TerminalLeafPane,
  TerminalSplitDirection,
} from "@/domain/terminal/types";
import {
  terminalTabForPane,
  terminalTabLabel,
  terminalTabPaneIds,
  type TerminalTab,
} from "@/domain/terminal/terminal-tabs";
import { strongestAttention } from "@/domain/terminal/terminal-attention";
import { terminalTabUsage } from "@/domain/terminal/resource-usage";
import { useTerminalResourceUsage } from "@/composables/use-terminal-resource-usage";
import { endLocalShellProcess } from "@/services/terminal-resource-usage";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { useVueI18n } from "@/i18n/messages";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { useActiveViewCommands } from "@/composables/use-active-view-commands";
import { registerCommand } from "@/services/command-registry";
import { deleteTerminalSnapshot } from "@/services/terminal-session-cache";
import { loadLocalShellCwds } from "@/services/terminal-service";
import { chooseNativeDirectory, emitAppEvent, invokeBackend, isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";
import { isPopupWindow, popupWindowLabel, POPUP_TAB_STATE_EVENT } from "@/services/popup-tab";
import { recordClosedTabs } from "@/services/closed-tab-history";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useTerminalAttentionStore } from "@/stores/terminal-attention";

defineOptions({ name: "TerminalWorkspaceView" });

const terminalWorkspace = useTerminalWorkspaceStore();
const workspaceStateScope = useWorkspaceStateScope();
const workbench = workspaceStateScope.workbench;
const workspaceStatePreferences = workspaceStateScope.preferences;
const workspaceStateScopeId = workspaceStateScope.scopeId;
const { tabs, activeTabId, activeTab, canClose, canSplit } = storeToRefs(terminalWorkspace);
const { t } = useVueI18n();
const router = useRouter();
const route = useRoute();
const terminalAttention = useTerminalAttentionStore();
const shellConfigurationVisible = ref(false);
const sshConnectionVisible = ref(false);
const sshConnections = useSshConnectionsStore();
const savedSshProfileId = ref("");
const editingSshPaneId = ref<string>();
const popupLabel = isPopupWindow() ? popupWindowLabel() : "";

// Memory of the local terminals of every tab (hidden tabs keep running their programs).
const { usages: terminalUsages, refresh: refreshTerminalUsage } = useTerminalResourceUsage(() =>
  tabs.value.flatMap((tab) => terminalTabPaneIds(tab))
);

function tabUsage(tab: TerminalTab) {
  return terminalTabUsage(terminalTabPaneIds(tab), terminalUsages.value);
}

async function endTerminalProcess(sessionId: string, pid: number, name: string) {
  try {
    await ElMessageBox.confirm(t("terminal.usage.endConfirm", { name, pid }), t("terminal.usage.endTitle"), {
      type: "warning",
      confirmButtonText: t("terminal.usage.end"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  try {
    await endLocalShellProcess(sessionId, pid);
  } catch (error) {
    ElMessage.error(t("terminal.usage.endFailed", { error: error instanceof Error ? error.message : String(error) }));
    return;
  }
  // The process gets a moment to exit after the hangup before it is killed.
  window.setTimeout(() => void refreshTerminalUsage(), 2500);
}

const editingSshDraft = computed<SshConnectionDraft | undefined>(() => {
  if (!editingSshPaneId.value) return undefined;
  const pane = terminalWorkspace.getPane(editingSshPaneId.value);
  if (!pane || pane.type !== "ssh") return undefined;
  return { profile: pane.profile, credentials: pane.credentials };
});

async function splitPane(paneId: string, direction: TerminalSplitDirection) {
  // 分屏前先实时查询源 Pane 当前目录，让新终端直接落在同一目录下；
  // 查询失败时回退到 store 中最近记录的 cwd。
  const liveCwd = (await loadLocalShellCwds([paneId]))[paneId];
  if (!terminalWorkspace.splitPane(paneId, direction, liveCwd ? { initialCwd: liveCwd } : {})) {
    ElMessage.warning(t("terminal.maxPanes"));
  }
}

async function closeNativeSession(paneId: string) {
  deleteTerminalSnapshot(paneId);
  if (!isDesktopRuntime()) return;
  await invokeBackend<void>("close_local_shell", { payload: { sessionId: paneId } }).catch(() => {});
}

function paneLabel(pane: TerminalLeafPane) {
  if (pane.type === "ssh") return `SSH: ${pane.profile.name || `${pane.profile.username}@${pane.profile.host}`}`;
  return (
    (pane.shellType === "system" ? t("terminal.localShell") : pane.shellType)
  );
}

// The workbench tab owning these terminal tabs (a popup holds its own copy). When it is
// pinned, its last terminal tab cannot close: that would force-close the pinned tab.
const lastTabCloseLocked = computed(
  () => workbench.activeTab?.view === "terminal" && workbench.activeTab.pinned === true && tabs.value.length <= 1
);
const closeLockedReason = computed(() => (lastTabCloseLocked.value ? t("tab.pinnedCannotClose") : undefined));

function tabLabel(tab: TerminalTab) {
  return terminalTabLabel(tab, paneLabel);
}

// Same source as the workbench tab marker: any pane of the terminal tab with attention.
function tabNeedsAttention(tab: TerminalTab) {
  return terminalAttention.count > 0 && strongestAttention(terminalAttention.items, terminalTabPaneIds(tab)) !== undefined;
}

/** Closing the last terminal tab closes the workbench tab (or this popup window). */
async function closeWorkbenchTerminalTab() {
  if (popupLabel) {
    // The main window owns the workbench tab: it closes it like the tab bar's × (its
    // cleanup ends the shells) and then this window. Closing only this window would
    // hand the tab and its live shells back to the main window.
    try {
      await emitAppEvent(POPUP_TAB_STATE_EVENT, { type: "close-workbench-tab", windowLabel: popupLabel });
    } catch {
      ElMessage.warning(t("tab.popupUnavailable"));
    }
    return;
  }
  const tab = workbench.activeTab;
  if (tab?.view !== "terminal" || tab.pinned) return;
  recordClosedTabs([tab]);
  workbench.closeTab(tab.id, true);
  await router.push(workspaceTabRoute(workbench.activeTab)).catch(() => undefined);
}

async function closeTerminalTab(tabId: string) {
  if (!tabs.value.some((tab) => tab.id === tabId)) return;
  if (lastTabCloseLocked.value) {
    ElMessage.warning(t("tab.pinnedCannotClose"));
    return;
  }
  const paneIds = terminalWorkspace.closeTab(tabId);
  if (!paneIds) {
    await closeWorkbenchTerminalTab();
    return;
  }
  // After the closed tab's terminals unmounted (they save their screen on unmount).
  await nextTick();
  await Promise.all(paneIds.map((paneId) => closeNativeSession(paneId)));
}

// "Close others / left / right" from the tab menu: one at a time, each ends its own shells.
async function closeTerminalTabs(tabIds: string[]) {
  for (const tabId of tabIds) await closeTerminalTab(tabId);
}

// Closing the last pane of a terminal tab closes that tab.
async function closePane(paneId: string) {
  const tab = terminalTabForPane({ tabs: tabs.value, activeTabId: activeTabId.value }, paneId);
  if (!tab) return;
  if (terminalTabPaneIds(tab).length <= 1) {
    await closeTerminalTab(tab.id);
    return;
  }
  await closeNativeSession(paneId);
  terminalWorkspace.closePane(paneId);
}

async function reloadPane(paneId: string) {
  await closeNativeSession(paneId);
  terminalWorkspace.reloadPane(paneId);
}

function addShellTab(options: LocalTerminalPaneOptions) {
  terminalWorkspace.addLocalTab(options);
}

async function addShellTabInFolder() {
  if (!isDesktopRuntime()) return;
  try {
    const path = await chooseNativeDirectory(t("project.chooseFolder"));
    if (path) addShellTab({ initialCwd: path });
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  }
}

function openNewSsh() {
  editingSshPaneId.value = undefined;
  savedSshProfileId.value = "";
  void sshConnections.hydrate().catch(() => undefined);
  sshConnectionVisible.value = true;
}

// Picking a saved connection opens it directly, the same way the SSH workspace's list does.
function connectSavedSsh(profileId: string) {
  const profile = sshConnections.findProfile(profileId);
  if (!profile) return;
  sshConnectionVisible.value = false;
  void connectSsh({ profile, credentials: sshConnections.credentialsFor(profile.id) });
}

function editSsh(paneId: string) {
  editingSshPaneId.value = paneId;
  sshConnectionVisible.value = true;
}

async function connectSsh(draft: SshConnectionDraft) {
  if (editingSshPaneId.value) {
    const paneId = editingSshPaneId.value;
    await closeNativeSession(paneId);
    terminalWorkspace.updateSshPane(paneId, draft);
    editingSshPaneId.value = undefined;
    return;
  }
  terminalWorkspace.addSshTab(draft);
}

function terminalLayoutTarget(tabId: string) {
  const tab = workbench.tabs.find((candidate) => candidate.id === tabId);
  if (!tab || popupLabel) return { layoutId: tabId };
  return resolveTerminalLayoutTarget(
    workbench.tabs,
    tab,
    workspaceStatePreferences.isolationEnabled,
    workspaceStateScopeId.value
  );
}

function activateCurrentTerminalTab() {
  const tab = workbench.activeTab;
  if (tab?.view !== "terminal") return;
  const target = terminalLayoutTarget(tab.id);
  terminalWorkspace.activateTab(tab.id, undefined, target.layoutId, target.fallbackLayoutId);
}

function handleTerminalCreate(event: Event) {
  const detail = (
    event as CustomEvent<{
      tabId?: string;
      kind?: string;
      shellType?: LocalShellType;
      initialCommand?: string;
    }>
  ).detail;
  if (detail?.tabId) {
    const initialPane =
      detail.kind === "ai"
        ? { initialCommand: detail.initialCommand?.trim() || "claude" }
        : { shellType: detail.shellType ?? "system" };
    const target = terminalLayoutTarget(detail.tabId);
    terminalWorkspace.activateTab(
      detail.tabId,
      initialPane,
      target.layoutId,
      target.fallbackLayoutId
    );
    return;
  }
  if (detail?.kind === "ssh") {
    openNewSsh();
    return;
  }
  if (detail?.kind === "configured") {
    openShellConfiguration();
    return;
  }
  if (detail?.kind === "ai") {
    void addShellTab({ initialCommand: detail.initialCommand?.trim() || "claude" });
    return;
  }
  void addShellTab({ shellType: detail?.shellType ?? "system" });
}

function openShellConfiguration() {
  shellConfigurationVisible.value = true;
}

function handleAddMenu(kind: TerminalTabAddMenuCommand) {
  if (kind === "configured") openShellConfiguration();
  else if (kind === "ssh") openNewSsh();
  else if (kind === "folder") void addShellTabInFolder();
}

// Terminal tab shortcuts (⌘T / Ctrl+Alt+T, ⇧⌘[ / Ctrl+PageUp, ⇧⌘] / Ctrl+PageDown).
// Registered only while this view is active (KeepAlive deactivation disposes them) and
// run only while it is the shown route, so a project-page terminal never opens tabs here.
function whileTerminalViewShown(action: () => void) {
  return () => {
    if (route.name === "terminal") action();
  };
}

useActiveViewCommands(() => [
  registerCommand({
    id: "terminal:new-tab",
    source: "builtin",
    title: () => t("keybindings.command.terminal:new-tab"),
    subtitle: () => t("commandPalette.source.builtin"),
    keywords: ["terminal", "tab", "终端", "标签"],
    // Same path as the strip’s "+" button.
    execute: whileTerminalViewShown(() => void addShellTab({ shellType: "system" })),
  }),
  registerCommand({
    id: "terminal:next-tab",
    source: "builtin",
    title: () => t("keybindings.command.terminal:next-tab"),
    showInPalette: false,
    execute: whileTerminalViewShown(() => terminalWorkspace.selectAdjacentTab(1)),
  }),
  registerCommand({
    id: "terminal:prev-tab",
    source: "builtin",
    title: () => t("keybindings.command.terminal:prev-tab"),
    showInPalette: false,
    execute: whileTerminalViewShown(() => terminalWorkspace.selectAdjacentTab(-1)),
  }),
]);

watch(
  [
    () => workbench.activeTabId,
    () => workspaceStatePreferences.isolationEnabled,
    () => workspaceStatePreferences.sharedWorkspaceId,
  ],
  () => activateCurrentTerminalTab(),
  { immediate: true }
);

watch(
  [tabs, activeTabId],
  () => {
    if (!popupLabel || !isDesktopRuntime()) return;
    void emitAppEvent(POPUP_TAB_STATE_EVENT, {
      type: "terminal-layout",
      windowLabel: popupLabel,
      snapshot: terminalWorkspace.layoutSnapshot(),
    });
  },
  { deep: true }
);

onMounted(() => {
  activateCurrentTerminalTab();
  window.addEventListener("app:terminal-create", handleTerminalCreate);
});
onActivated(activateCurrentTerminalTab);
onBeforeUnmount(() => {
  terminalWorkspace.persistLayout();
  if (popupLabel && isDesktopRuntime()) {
    void emitAppEvent(POPUP_TAB_STATE_EVENT, {
      type: "terminal-layout",
      windowLabel: popupLabel,
      snapshot: terminalWorkspace.layoutSnapshot(),
    });
  }
  window.removeEventListener("app:terminal-create", handleTerminalCreate);
});
</script>

<template>
  <section class="terminal-workspace-layout">
    <TerminalTabStrip
      :tabs="tabs"
      :active-tab-id="activeTab.id"
      :label-for="tabLabel"
      :attention-for="tabNeedsAttention"
      :usage-for="tabUsage"
      :close-disabled-reason="closeLockedReason"
      @select="terminalWorkspace.selectTab"
      @close="closeTerminalTab"
      @close-tabs="closeTerminalTabs"
      @end-process="endTerminalProcess"
      @rename="terminalWorkspace.renameTab"
      @add="addShellTab({ shellType: 'system' })"
      @add-menu="handleAddMenu"
    />
    <div class="terminal-workspace-tab-panels">
      <!-- Hidden terminal tabs stay mounted (live shells, instant switch); their panes are inactive. -->
      <div
        v-for="tab in tabs"
        v-show="tab.id === activeTab.id"
        :key="tab.id"
        class="terminal-workspace-tab-panel"
      >
        <TerminalPaneLayout
          :node="tab.root"
          :active-pane-id="tab.activePaneId"
          :can-close="canClose"
          :can-split="canSplit"
          :visible="tab.id === activeTab.id"
          :close-last-pane="!lastTabCloseLocked"
          :close-disabled-reason="closeLockedReason"
          @select="terminalWorkspace.selectPane"
          @close="closePane"
          @reload="reloadPane"
          @configure-ssh="editSsh"
          @split="splitPane"
          @resize="terminalWorkspace.resizeSplit"
          @resize-end="terminalWorkspace.persistLayout"
        />
      </div>
    </div>
  </section>

  <ShellConfigurationDialog v-model="shellConfigurationVisible" @create="addShellTab" />
  <!-- SshConnectionDialog is an inline form (the SSH workspace embeds it in its detail
       pane); here it needs a real dialog, or it renders below the full-height terminal. -->
  <el-dialog
    v-model="sshConnectionVisible"
    append-to-body
    destroy-on-close
    class="terminal-ssh-dialog"
    width="min(720px, calc(100vw - 48px))"
    :title="t(editingSshPaneId ? 'ssh.editConnection' : 'ssh.newConnection')"
  >
    <div
      v-if="!editingSshPaneId && sshConnections.sortedProfiles.length"
      class="terminal-ssh-dialog__saved"
    >
      <span class="terminal-ssh-dialog__saved-label">{{ t("terminal.ssh.savedConnection") }}</span>
      <el-select
        v-model="savedSshProfileId"
        filterable
        class="terminal-ssh-dialog__saved-select"
        :placeholder="t('terminal.ssh.savedConnectionPlaceholder')"
        :aria-label="t('terminal.ssh.savedConnection')"
        @change="connectSavedSsh"
      >
        <el-option
          v-for="profile in sshConnections.sortedProfiles"
          :key="profile.id"
          :value="profile.id"
          :label="profile.name || profile.host"
        >
          <span class="terminal-ssh-dialog__saved-name" dir="auto">{{ profile.name || profile.host }}</span>
          <span class="terminal-ssh-dialog__saved-address" dir="auto"
            >{{ profile.username }}@{{ profile.host }}:{{ profile.port }}</span
          >
        </el-option>
      </el-select>
    </div>
    <SshConnectionDialog
      v-model="sshConnectionVisible"
      :initial="editingSshDraft"
      @connect="connectSsh"
    />
  </el-dialog>
</template>
