<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from "vue";
import {
  Connection,
  CopyDocument,
  Delete,
  Download,
  Edit,
  Folder,
  Key,
  Plus,
  Upload,
  MoreFilled,
  Notebook,
  Refresh,
} from "@element-plus/icons-vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { useRouter } from "vue-router";
import { registerAiWorkspaceActionHandler } from "@/services/ai-workspace-actions";
import { registerCommand } from "@/services/command-registry";
import SshConnectionDialog from "@/components/ssh/SshConnectionDialog.vue";
import SshKnownHostsDialog from "@/components/ssh/SshKnownHostsDialog.vue";
import SshConfigSyncDialog from "@/components/ssh/SshConfigSyncDialog.vue";
import SshTerminalPaneTree from "@/components/ssh/SshTerminalPaneTree.vue";
import SshServerOverview, { type SshOverviewSession } from "@/components/ssh/SshServerOverview.vue";
import ConnectionAvatar, { type ConnectionAvatarStatus } from "@/components/connections/ConnectionAvatar.vue";
import LinkedNotesBadge from "@/components/notes/LinkedNotesBadge.vue";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";
import {
  createSshProfileId,
  type SshConnectionDraft,
  type SshConnectionProfile,
  type SshTerminalStatus,
} from "@/domain/ssh/types";
import { parseSshProfileImportBundle } from "@/domain/ssh/import-profiles";
import { buildSshProfileExport, sshProfileImportHasSecrets } from "@/domain/ssh/profile-portability";
import {
  confirmSecretExport,
  confirmSecretImport,
} from "@/composables/use-secret-export-confirm";
import {
  MAX_SSH_SESSION_PANES,
  countSshWorkspacePanes,
  deserializeSshWorkspaceSessionTab,
  findSshWorkspacePane,
  removeSshWorkspacePane,
  resizeSshWorkspaceSplit,
  serializeSshWorkspaceSessionTab,
  splitSshWorkspacePane,
  sshWorkspacePaneIds,
  updateSshWorkspacePane,
  type SshWorkspaceSessionTab,
  type SshWorkspaceTerminalPane,
} from "@/domain/ssh/workspace-session-layout";
import type { TerminalSplitDirection } from "@/domain/terminal/types";
import { orderConnectionGroups } from "@/domain/connections/group-order";
import { avatarColor } from "@/domain/connections/overview";
import { useVueI18n } from "@/i18n/messages";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useSshRecentStore } from "@/stores/ssh-recent";
import AppChevron from "@/components/ui/AppChevron.vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { ROW_GROUP_ATTRIBUTE, useGroupedRowWindow } from "@/composables/use-grouped-row-window";
import AppWorkspaceTabs from "@/components/ui/AppWorkspaceTabs.vue";
import AppFilterButton from "@/components/ui/AppFilterButton.vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import { useResizablePane } from "@/composables/use-resizable-pane";
import { useToggleSidebarCommand } from "@/composables/use-active-view-commands";
import { deleteTerminalSnapshot } from "@/services/terminal-session-cache";
import { invokeBackend, isDesktopRuntime, copyText } from "@/services/desktop-bridge";
import { saveTextFileToDisk } from "@/services/file-download";
import { loadPersistedValue, savePersistedValue, deletePersistedValue } from "@/services/persistent-store";
import { isWorkspaceScopePermanentlyDeleted } from "@/services/workspace-deleted-scopes";
import { normalizeSshBatchHistoryForPersistence } from "@/domain/ssh/batch-history-budget";

const SSH_SESSIONS_STORAGE_KEY = "app_ssh_workspace_sessions_v1";
const BATCH_HISTORY_KEY = "app_ssh_batch_history_v1";
const MAX_BATCH_HISTORY = 20;
const PERSIST_DEBOUNCE_MS = 150;

defineOptions({ name: "SshWorkspaceView" });

const { t } = useVueI18n();
const router = useRouter();
const connections = useSshConnectionsStore();
const sshRecent = useSshRecentStore();
const workspaceStateScope = useWorkspaceStateScope();
const workspaceScopeId = workspaceStateScope.scopeId;
const workspaceElement = ref<HTMLElement | null>(null);
const dialogVisible = ref(false);
const knownHostsVisible = ref(false);
const configSyncVisible = ref(false);
const editingDraft = ref<SshConnectionDraft>();
/** `user@host[:port]` typed into the overview search; prefills the next new host. */
const pendingAddress = ref("");
const searchQuery = ref("");
const filterOpen = ref(false);
const favoritesOnly = ref(false);
const selectedGroupFilter = ref<string>();
const selectedTagFilter = ref<string>();
const openGroups = ref<Record<string, boolean>>({});
const ungroupedFilter = "__app_ssh_ungrouped__";

type SshSidebarDragPayload =
  | { type: "profile"; profileId: string }
  | { type: "group"; group: string };
type SshGroupDropPosition = "before" | "after";

const sshSidebarDrag = ref<SshSidebarDragPayload>();
const sshGroupDrop = ref<{ group: string; position: SshGroupDropPosition }>();
const sshDragMimeType = "application/x-mincode-ssh-connection-sidebar";
const {
  size: sidebarWidth,
  collapsed: sidebarCollapsed,
  hydrate: hydrateSidebarLayout,
  persist: persistSidebarLayout,
} = useResizablePane({
  sizeKey: "app_ssh_sidebar_width",
  collapsedKey: "app_ssh_sidebar_collapsed",
  defaultSize: 270,
  minSize: 200,
  maxSize: (workspaceWidth) => Math.min(480, workspaceWidth * 0.56),
  containerRef: workspaceElement,
});

function persistSidebarWidth(value: number) {
  sidebarWidth.value = value;
  void persistSidebarLayout();
}

function updateSidebarCollapsed(value: boolean) {
  sidebarCollapsed.value = value;
  void persistSidebarLayout();
}
useToggleSidebarCommand(() => sidebarCollapsed.value, updateSidebarCollapsed);

const SSH_OVERVIEW_TAB_ID = "__ssh-server-overview__";
const sshSessionTabs = ref<SshWorkspaceSessionTab[]>([]);
const activeSessionIds = ref<Record<string, string | undefined>>({});
const workspaceSessionTabs = computed(() =>
  sshSessionTabs.value.filter((session) => session.workspaceScopeId === workspaceScopeId.value)
);
const activeSessionId = computed<string>({
  get: () => {
    const selected = activeSessionIds.value[workspaceScopeId.value];
    if (selected === SSH_OVERVIEW_TAB_ID) return selected;
    if (selected && workspaceSessionTabs.value.some((session) => session.id === selected)) return selected;
    return SSH_OVERVIEW_TAB_ID;
  },
  set: (sessionId: string) => {
    activeSessionIds.value = {
      ...activeSessionIds.value,
      [workspaceScopeId.value]: sessionId,
    };
  },
});
const activeSession = computed(() =>
  workspaceSessionTabs.value.find((session) => session.id === activeSessionId.value)
);
function workspacePanes(tab: SshWorkspaceSessionTab) {
  return sshWorkspacePaneIds(tab.root)
    .map((paneId) => findSshWorkspacePane(tab.root, paneId))
    .filter((pane): pane is SshWorkspaceTerminalPane => Boolean(pane));
}
/** Profiles shown in the active session tab; the sidebar highlights them like the database navigator. */
const activeProfileIds = computed(
  () => new Set(activeSession.value ? workspacePanes(activeSession.value).map((pane) => pane.profile.id) : [])
);
const overviewSessions = computed<SshOverviewSession[]>(() =>
  workspaceSessionTabs.value.flatMap((tab) =>
    workspacePanes(tab).map((pane) => ({
      profileId: pane.profile.id,
      sessionId: pane.id,
      tabId: tab.id,
      status: pane.status,
      statusDetail: pane.statusDetail,
    }))
  )
);

/** Sidebar status dot per profile: any connected pane wins, otherwise any pane still connecting. */
const profileSessionStatus = computed(() => {
  const status = new Map<string, "ready" | "connecting">();
  for (const session of overviewSessions.value) {
    if (session.status === "ready") status.set(session.profileId, "ready");
    else if ((session.status === "connecting" || session.status === "reconnecting") && status.get(session.profileId) !== "ready") {
      status.set(session.profileId, "connecting");
    }
  }
  return status;
});

/** Sidebar avatar badge: the session status mapped onto the shared avatar vocabulary. */
function sidebarAvatarStatus(profileId: string): ConnectionAvatarStatus {
  const status = profileSessionStatus.value.get(profileId);
  if (status === "ready") return "online";
  if (status === "connecting") return "pending";
  return "offline";
}

const onlineProfileCount = computed(
  () => connections.profiles.filter((profile) => profileSessionStatus.value.get(profile.id) === "ready").length
);

interface BatchExecResult {
  id: string;
  host: string;
  stdout: string;
  stderr: string;
  exitCode?: number;
  durationMs: number;
  error?: string;
}

const batchCommandVisible = ref(false);
const batchCommand = ref("");
const batchCredential = ref("");
const selectedBatchProfileIds = ref<string[]>([]);
const batchExecuting = ref(false);
const batchResults = ref<BatchExecResult[]>([]);

interface BatchHistoryEntry {
  id: string;
  command: string;
  serverHosts: string[];
  timestamp: number;
  results: BatchExecResult[];
}

const batchHistory = ref<BatchHistoryEntry[]>([]);
const batchHistoryExpanded = ref(false);
const batchProfiles = computed(() => connections.sortedProfiles);
const batchEligibleProfiles = computed(() =>
  batchProfiles.value.filter((profile) => profile.authType !== "keyboard-interactive")
);
const selectedBatchProfiles = computed(() =>
  batchEligibleProfiles.value.filter((profile) =>
    selectedBatchProfileIds.value.includes(profile.id)
  )
);
const allBatchProfilesSelected = computed(
  () =>
    batchEligibleProfiles.value.length > 0 &&
    batchEligibleProfiles.value.every((profile) =>
      selectedBatchProfileIds.value.includes(profile.id)
    )
);
const batchNeedsCredential = computed(() =>
  selectedBatchProfiles.value.some((profile) => {
    const credentials = runtimeCredentialsFor(profile.id);
    return (
      (profile.authType === "password" && !credentials?.password) ||
      (profile.jumpHost?.authType === "password" && !credentials?.jumpPassword) ||
      Boolean(profile.socksProxy?.username && !credentials?.socksPassword)
    );
  })
);

const groupSummary = computed(() => {
  const counts = new Map<string, number>();
  for (const profile of connections.profiles) {
    const group = profile.group?.trim() || "";
    counts.set(group, (counts.get(group) ?? 0) + 1);
  }
  return [...counts.entries()].sort(([left], [right]) => {
    if (!left) return 1;
    if (!right) return -1;
    return left.localeCompare(right, undefined, { numeric: true, sensitivity: "base" });
  });
});

const profileGroupNames = computed(() =>
  groupSummary.value.map(([group]) => group).filter(Boolean)
);

const tagSummary = computed(() => {
  const counts = new Map<string, number>();
  for (const profile of connections.profiles) {
    for (const tag of profile.tags) counts.set(tag, (counts.get(tag) ?? 0) + 1);
  }
  return [...counts.entries()].sort(([left], [right]) =>
    left.localeCompare(right, undefined, { numeric: true, sensitivity: "base" })
  );
});
const profileTagNames = computed(() => tagSummary.value.map(([tag]) => tag));

const favoriteCount = computed(
  () => connections.profiles.filter((profile) => profile.favorite).length
);
const sshFilterScope = computed<"all" | "favorites">({
  get: () => (favoritesOnly.value ? "favorites" : "all"),
  set: (scope) => {
    favoritesOnly.value = scope === "favorites";
  },
});
const sshFilterScopeOptions = computed(() => [
  { label: `${t("ssh.manager.all")} (${connections.profiles.length})`, value: "all" },
  { label: `${t("ssh.manager.favorite")} (${favoriteCount.value})`, value: "favorites" },
]);
const filtersActive = computed(
  () =>
    favoritesOnly.value || Boolean(selectedGroupFilter.value) || Boolean(selectedTagFilter.value)
);

const filteredProfiles = computed(() => {
  const query = searchQuery.value.trim().toLocaleLowerCase();
  return connections.sortedProfiles.filter((profile) => {
    if (favoritesOnly.value && !profile.favorite) return false;
    const group = profile.group?.trim() || "";
    if (selectedGroupFilter.value === ungroupedFilter && group) return false;
    if (
      selectedGroupFilter.value &&
      selectedGroupFilter.value !== ungroupedFilter &&
      group !== selectedGroupFilter.value
    ) {
      return false;
    }
    if (selectedTagFilter.value && !profile.tags.includes(selectedTagFilter.value)) return false;
    if (!query) return true;
    return [
      profile.name,
      profile.host,
      profile.username,
      profile.authType,
      profile.description ?? "",
      profile.defaultPath ?? "",
      group,
      ...profile.tags,
    ]
      .join(" ")
      .toLocaleLowerCase()
      .includes(query);
  });
});

const profileGroups = computed(() => {
  const groups = new Map<string, SshConnectionProfile[]>();
  for (const profile of filteredProfiles.value) {
    const key = profile.group?.trim() || "";
    groups.set(key, [...(groups.get(key) ?? []), profile]);
  }
  return orderConnectionGroups(
    [...groups.entries()].map(([key, profiles]) => ({
      key,
      label: key || t("ssh.manager.ungrouped"),
      items: profiles,
      profiles,
    })),
    connections.groupOrder
  );
});

const sshDragEnabled = computed(
  () => !searchQuery.value.trim() && !filtersActive.value
);

function setSshSidebarDrag(payload: SshSidebarDragPayload, event: DragEvent) {
  sshSidebarDrag.value = payload;
  event.dataTransfer?.setData(sshDragMimeType, JSON.stringify(payload));
  event.dataTransfer?.setData(
    "text/plain",
    payload.type === "profile" ? payload.profileId : payload.group
  );
  if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
}

function readSshSidebarDrag(event: DragEvent): SshSidebarDragPayload | undefined {
  if (sshSidebarDrag.value) return sshSidebarDrag.value;
  const value = event.dataTransfer?.getData(sshDragMimeType);
  if (!value) return undefined;
  try {
    const parsed = JSON.parse(value) as SshSidebarDragPayload;
    if (parsed.type === "profile" && typeof parsed.profileId === "string") return parsed;
    if (parsed.type === "group" && typeof parsed.group === "string") return parsed;
  } catch {
    // Drag data from another source is ignored.
  }
  return undefined;
}

function clearSshSidebarDrag() {
  sshSidebarDrag.value = undefined;
  sshGroupDrop.value = undefined;
}

function onSshProfileDragStart(profile: SshConnectionProfile, event: DragEvent) {
  if (!sshDragEnabled.value) {
    event.preventDefault();
    return;
  }
  setSshSidebarDrag({ type: "profile", profileId: profile.id }, event);
}

function onSshGroupDragStart(group: string, event: DragEvent) {
  if (!sshDragEnabled.value || !group) {
    event.preventDefault();
    return;
  }
  event.stopPropagation();
  setSshSidebarDrag({ type: "group", group }, event);
}

function onSshGroupDragOver(group: string, event: DragEvent) {
  const payload = readSshSidebarDrag(event);
  if (!payload || (payload.type === "group" && (!group || payload.group === group))) return;

  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
  if (payload.type === "group") {
    const bounds = (event.currentTarget as HTMLElement).getBoundingClientRect();
    sshGroupDrop.value = {
      group,
      position: event.clientY < bounds.top + bounds.height / 2 ? "before" : "after",
    };
  } else {
    sshGroupDrop.value = undefined;
  }
}

async function onSshGroupDrop(group: string, event: DragEvent) {
  const payload = readSshSidebarDrag(event);
  const dropPosition = sshGroupDrop.value;
  clearSshSidebarDrag();
  if (!payload) return;

  try {
    if (payload.type === "profile") {
      const profile = connections.findProfile(payload.profileId);
      const targetGroup = group || undefined;
      if (!profile || (profile.group?.trim() || undefined) === targetGroup) return;
      await connections.moveProfileToGroup(profile.id, targetGroup);
      openGroups.value = { ...openGroups.value, [group]: true };
      ElMessage.success(
        t("ssh.manager.profileMovedToGroup").replace(
          "{group}",
          group || t("ssh.manager.ungrouped")
        )
      );
      return;
    }

    if (!group || payload.group === group) return;
    const position = dropPosition?.group === group ? dropPosition.position : "before";
    const orderedGroups = profileGroups.value
      .map((item) => item.key)
      .filter(Boolean)
      .filter((name) => name !== payload.group);
    const targetIndex = orderedGroups.indexOf(group);
    if (targetIndex < 0) return;
    orderedGroups.splice(targetIndex + (position === "after" ? 1 : 0), 0, payload.group);
    await connections.reorderGroups(orderedGroups);
  } catch (cause) {
    ElMessage.error(cause instanceof Error ? cause.message : String(cause));
  }
}

function groupIsOpen(group: string) {
  return openGroups.value[group] !== false;
}

function toggleGroup(group: string) {
  openGroups.value = { ...openGroups.value, [group]: !groupIsOpen(group) };
}

// One "⋯" menu for every sidebar connection, anchored at the clicked button. A full
// el-dropdown per row (500 connections) froze opening the page for ~0.4 s.
const profileMenu = ref<{ profileId: string; x: number; y: number }>();
const profileMenuProfile = computed(() => (profileMenu.value ? connections.findProfile(profileMenu.value.profileId) : undefined));

function openProfileMenu(event: MouseEvent, profileId: string) {
  const anchor = (event.currentTarget as HTMLElement | null)?.getBoundingClientRect();
  const menuWidth = 200;
  const x = anchor ? anchor.right - menuWidth : event.clientX;
  const y = anchor ? anchor.bottom + 4 : event.clientY;
  profileMenu.value = {
    profileId,
    x: Math.max(8, Math.min(x, window.innerWidth - menuWidth - 8)),
    y: Math.max(8, y),
  };
}

function closeProfileMenu() {
  profileMenu.value = undefined;
}

const linkedNotes = useLinkedNotesDrawer();

function openProfileNotes(profile: SshConnectionProfile, createNew = false) {
  return linkedNotes.open({ objectType: "ssh", objectId: profile.id, nameSnapshot: profile.name }, { createNew });
}

/** Runs a menu entry for the menu's profile after closing the menu. */
function runProfileMenu(action: (profile: SshConnectionProfile) => unknown) {
  const profile = profileMenuProfile.value;
  closeProfileMenu();
  if (profile) void action(profile);
}

// Many connections render only the sidebar rows near the viewport (see use-grouped-row-window).
const sidebarScroller = ref<{ wrapRef?: HTMLElement }>();
const sidebarRows = useGroupedRowWindow({
  scroller: () => sidebarScroller.value?.wrapRef,
  counts: () => Object.fromEntries(profileGroups.value.map((group) => [group.key, groupIsOpen(group.key) ? group.profiles.length : 0])),
  rowSelector: ".db-connection-entry",
  estimatedRowHeight: 44,
  fallbackRows: 12,
});

/** Profiles whose sidebar row (and so "⋯" menu) is rendered. */
const renderedProfileIds = computed(
  () =>
    new Set(
      profileGroups.value.filter((group) => groupIsOpen(group.key)).flatMap((group) => group.profiles.map((profile) => profile.id))
    )
);
// Filtering, collapsing a group or deleting the profile unmounts the row without a close event.
watch(renderedProfileIds, (ids) => {
  if (profileMenu.value && !ids.has(profileMenu.value.profileId)) profileMenu.value = undefined;
});
onDeactivated(() => {
  profileMenu.value = undefined;
});

function newConnection() {
  editingDraft.value = undefined;
  dialogVisible.value = true;
}

/** Overview search typed `user@host[:port]` with no matching host; opens the new-host dialog prefilled. */
function newConnectionForAddress(address: string) {
  editingDraft.value = undefined;
  pendingAddress.value = address;
  dialogVisible.value = true;
}

watch(dialogVisible, (visible) => {
  if (!visible) pendingAddress.value = "";
});

const unregisterNewSshConnectionCommand = registerCommand({
  id: "workspace:ssh:new-connection",
  source: "workspace",
  title: () => t("ssh.command.newConnection"),
  subtitle: () => t("commandPalette.source.workspace"),
  keywords: ["ssh", "connection", "profile"],
  priority: 3,
  execute: newConnection,
});

function editConnection(profile: SshConnectionProfile) {
  editingDraft.value = { profile };
  dialogVisible.value = true;
}

function deleteConnection(profile: SshConnectionProfile) {
  connections.removeProfile(profile.id);
}

async function handleSshProfileAction(command: string, profile: SshConnectionProfile) {
  if (command === "copy") return copySshConnectionInfo(profile);
  if (command === "edit") return editConnection(profile);
  if (command !== "delete") return;
  try {
    await ElMessageBox.confirm(t("ssh.manager.deleteConfirm").replace("{name}", profile.name), t("common.delete"), { type: "warning" });
    await deleteConnection(profile);
  } catch { /* cancelled */ }
}

async function copySshConnectionInfo(profile: SshConnectionProfile) {
  const creds = connections.credentialsFor(profile.id);
  const lines: string[] = [
    `${t("ssh.profileName")}: ${profile.name}`,
    `${t("ssh.host")}: ${profile.host}`,
    `${t("ssh.port")}: ${profile.port}`,
    `${t("ssh.username")}: ${profile.username}`,
    `${t("ssh.authType")}: ${profile.authType}`,
  ];
  if (creds?.password) lines.push(`${t("ssh.password")}: ${creds.password}`);
  if (profile.keyFile) lines.push(`${t("ssh.keyFile")}: ${profile.keyFile}`);
  if (creds?.passphrase) lines.push(`${t("ssh.passphrase")}: ${creds.passphrase}`);
  if (profile.description) lines.push(`${t("ssh.description")}: ${profile.description}`);
  try {
    await copyText(lines.join("\n"));
    ElMessage.success(t("ssh.manager.copyInfoSuccess"));
  } catch (e) {
    ElMessage.error(t("ssh.manager.copyInfoFailed").replace("{message}", String(e)));
  }
}

function savedConnection(profile: SshConnectionProfile) {
  const group = profile.group?.trim() || "";
  openGroups.value = { ...openGroups.value, [group]: true };
  dialogVisible.value = false;
  editingDraft.value = undefined;
}

/**
 * SSH 连接挂到当前工作区的右侧会话区，保持工作区内多会话模型，
 * 而不是跳转到全局“终端”页面。
 */
function createWorkspacePane(draft: SshConnectionDraft): SshWorkspaceTerminalPane {
  return {
    id: createSshProfileId("ssh-workspace"),
    type: "terminal",
    profile: draft.profile,
    credentials: draft.credentials,
    reloadVersion: 0,
    status: "connecting",
  };
}

function openWorkspaceSession(draft: SshConnectionDraft) {
  const root = createWorkspacePane(draft);
  const session: SshWorkspaceSessionTab = {
    id: root.id,
    workspaceScopeId: workspaceScopeId.value,
    root,
    activePaneId: root.id,
  };
  sshSessionTabs.value = [...sshSessionTabs.value, session];
  activeSessionId.value = session.id;
  dialogVisible.value = false;
  editingDraft.value = undefined;
}

function openSavedProfile(profile: SshConnectionProfile) {
  openWorkspaceSession({ profile, credentials: connections.credentialsFor(profile.id) });
}

function openSavedFileTransfer(profile: SshConnectionProfile) {
  if (profile.fileTransfer?.enabled === false) {
    ElMessage.warning(t("ssh.fileTransfer.disabledHint"));
    return;
  }
  void router.push({ name: "sftp", query: { profile: profile.id } });
}

async function handleSshWorkspaceAction(input: Record<string, unknown>, signal?: AbortSignal) {
  if (input.action === "active-resource") {
    const session = activeSession.value;
    return session ? findSshWorkspacePane(session.root, session.activePaneId)?.profile.id : null;
  }
  if (input.action !== "open-profile" || typeof input.profileId !== "string") {
    throw new Error("Unsupported SSH workspace action");
  }
  if (!connections.hydrated) await connections.hydrate();
  signal?.throwIfAborted();
  const profile = connections.findProfile(input.profileId);
  if (!profile) throw new Error(t("ai.navigation.resourceMissing", { id: input.profileId }));
  openSavedProfile(profile);
  return { profileId: profile.id, opened: true };
}
let unregisterSshWorkspaceActions = registerAiWorkspaceActionHandler("ssh", handleSshWorkspaceAction);
onActivated(() => {
  unregisterSshWorkspaceActions();
  unregisterSshWorkspaceActions = registerAiWorkspaceActionHandler("ssh", handleSshWorkspaceAction);
});
onDeactivated(() => unregisterSshWorkspaceActions());

function removeSshSessionTab(name: string | number) {
  if (typeof name === "string") void closeWorkspaceSession(name);
}

const sessionTabContextMenu = ref<{ id: string; x: number; y: number }>();
const sessionTabContextIndex = computed(() =>
  workspaceSessionTabs.value.findIndex((session) => session.id === sessionTabContextMenu.value?.id)
);
const sessionTabContextIsOverview = computed(
  () => sessionTabContextMenu.value?.id === SSH_OVERVIEW_TAB_ID
);
const sessionTabContextHasLeft = computed(() => sessionTabContextIndex.value > 0);
const sessionTabContextHasRight = computed(
  () =>
    sessionTabContextIndex.value >= 0 &&
    sessionTabContextIndex.value < workspaceSessionTabs.value.length - 1
);

function openSessionTabContextMenu(event: MouseEvent) {
  const item = (event.target as HTMLElement | null)?.closest?.(".el-tabs__item");
  if (!item || !item.id.startsWith("tab-")) return;
  const id = item.id.slice("tab-".length);
  if (id !== SSH_OVERVIEW_TAB_ID && !workspaceSessionTabs.value.some((session) => session.id === id)) {
    return;
  }
  event.preventDefault();
  sessionTabContextMenu.value = { id, x: event.clientX, y: event.clientY };
}

function closeSessionTabContextMenu() {
  sessionTabContextMenu.value = undefined;
}

async function closeWorkspaceSessions(sessionIds: string[], activeId?: string) {
  const ordered = sessionIds.filter((id) => id !== activeId);
  if (activeId && sessionIds.includes(activeId)) ordered.push(activeId);
  for (const sessionId of ordered) await closeWorkspaceSession(sessionId);
}

async function runSessionTabContextCommand(command: string) {
  const menu = sessionTabContextMenu.value;
  if (!menu) return;
  sessionTabContextMenu.value = undefined;
  const sessionIds = workspaceSessionTabs.value.map((session) => session.id);
  const index = sessionIds.indexOf(menu.id);
  switch (command) {
    case "close":
      if (index >= 0) await closeWorkspaceSession(menu.id);
      return;
    case "close-left":
      if (index > 0) await closeWorkspaceSessions(sessionIds.slice(0, index), activeSessionId.value);
      return;
    case "close-right":
      if (index >= 0) {
        await closeWorkspaceSessions(sessionIds.slice(index + 1), activeSessionId.value);
      }
      return;
    case "close-other":
      if (index >= 0) {
        await closeWorkspaceSessions(
          sessionIds.filter((id) => id !== menu.id),
          activeSessionId.value
        );
      }
      return;
    case "close-all":
      await closeWorkspaceSessions(sessionIds, activeSessionId.value);
  }
}

function updateWorkspaceSessionTab(
  tabId: string,
  update: (tab: SshWorkspaceSessionTab) => SshWorkspaceSessionTab
) {
  sshSessionTabs.value = sshSessionTabs.value.map((tab) => (tab.id === tabId ? update(tab) : tab));
}

function selectWorkspacePane(tabId: string, paneId: string) {
  updateWorkspaceSessionTab(tabId, (tab) => ({ ...tab, activePaneId: paneId }));
}

function updateWorkspaceSessionStatus(
  tabId: string,
  paneId: string,
  status: SshTerminalStatus,
  statusDetail?: string
) {
  const currentTab = sshSessionTabs.value.find((item) => item.id === tabId);
  const previousPane = currentTab ? findSshWorkspacePane(currentTab.root, paneId) : undefined;
  // Record only the transition into "ready" so status refreshes don't rewrite it.
  if (previousPane && previousPane.status !== "ready" && status === "ready") {
    sshRecent.markOpened(previousPane.profile.id);
  }
  updateWorkspaceSessionTab(tabId, (tab) => ({
    ...tab,
    root: updateSshWorkspacePane(tab.root, paneId, (pane) => ({ ...pane, status, statusDetail })),
  }));
}

function sessionStatusLabel(status: SshTerminalStatus) {
  return t(`ssh.status.${status}`);
}

async function closeNativeSession(sessionId: string) {
  deleteTerminalSnapshot(sessionId);
  if (!isDesktopRuntime()) return;
  await invokeBackend<void>("close_local_shell", { payload: { sessionId } }).catch(() => {});
}

async function closeWorkspaceSession(sessionId: string) {
  const tab = sshSessionTabs.value.find((item) => item.id === sessionId);
  if (!tab) return;
  await Promise.all(workspacePanes(tab).map((pane) => closeNativeSession(pane.id)));

  const workspaceIndex = sshSessionTabs.value
    .filter((item) => item.workspaceScopeId === tab.workspaceScopeId)
    .findIndex((item) => item.id === tab.id);
  const nextTabs = sshSessionTabs.value.filter((item) => item.id !== tab.id);
  sshSessionTabs.value = nextTabs;
  if (activeSessionIds.value[tab.workspaceScopeId] === tab.id) {
    const remainingWorkspaceTabs = nextTabs.filter((item) => item.workspaceScopeId === tab.workspaceScopeId);
    activeSessionIds.value = {
      ...activeSessionIds.value,
      [tab.workspaceScopeId]:
        remainingWorkspaceTabs[
          Math.min(Math.max(workspaceIndex, 0), remainingWorkspaceTabs.length - 1)
        ]?.id ?? SSH_OVERVIEW_TAB_ID,
    };
  }
}

async function closeWorkspacePane(tabId: string, paneId: string) {
  const tab = sshSessionTabs.value.find((item) => item.id === tabId);
  if (!tab || !findSshWorkspacePane(tab.root, paneId)) return;
  if (countSshWorkspacePanes(tab.root) === 1) {
    await closeWorkspaceSession(tabId);
    return;
  }

  const root = removeSshWorkspacePane(tab.root, paneId);
  if (!root) return;
  const remainingPaneIds = sshWorkspacePaneIds(root);
  updateWorkspaceSessionTab(tabId, (current) => ({
    ...current,
    root,
    activePaneId:
      current.activePaneId === paneId ? remainingPaneIds[0] ?? current.activePaneId : current.activePaneId,
  }));
  void closeNativeSession(paneId);
}

async function reloadWorkspacePane(tabId: string, paneId: string) {
  await closeNativeSession(paneId);
  updateWorkspaceSessionTab(tabId, (tab) => ({
    ...tab,
    root: updateSshWorkspacePane(tab.root, paneId, (pane) => ({
      ...pane,
      reloadVersion: pane.reloadVersion + 1,
      status: "connecting",
      statusDetail: undefined,
    })),
  }));
}

function splitWorkspacePane(tabId: string, paneId: string, direction: TerminalSplitDirection) {
  const tab = sshSessionTabs.value.find((item) => item.id === tabId);
  const pane = tab ? findSshWorkspacePane(tab.root, paneId) : undefined;
  if (!tab || !pane) return;
  if (countSshWorkspacePanes(tab.root) >= MAX_SSH_SESSION_PANES) {
    ElMessage.warning(t("terminal.maxPanes"));
    return;
  }

  const newPane = createWorkspacePane({ profile: pane.profile, credentials: pane.credentials });
  const root = splitSshWorkspacePane(
    tab.root,
    paneId,
    direction,
    newPane,
    createSshProfileId("ssh-workspace-split")
  );
  if (!root) return;
  updateWorkspaceSessionTab(tabId, (current) => ({ ...current, root, activePaneId: newPane.id }));
}

function resizeWorkspaceSplit(tabId: string, splitId: string, sizes: number[]) {
  updateWorkspaceSessionTab(tabId, (tab) => ({
    ...tab,
    root: resizeSshWorkspaceSplit(tab.root, splitId, sizes),
  }));
}

function configureWorkspaceSession(tabId: string, paneId: string) {
  const tab = sshSessionTabs.value.find((item) => item.id === tabId);
  const pane = tab ? findSshWorkspacePane(tab.root, paneId) : undefined;
  if (!pane) return;
  editingDraft.value = { profile: pane.profile, credentials: pane.credentials };
  dialogVisible.value = true;
}

function runtimeCredentialsFor(profileId: string) {
  const runtimeCredentials = [...workspaceSessionTabs.value]
    .flatMap(workspacePanes)
    .reverse()
    .find((pane) => pane.profile.id === profileId)?.credentials;
  const savedCredentials = connections.credentialsFor(profileId);
  if (!runtimeCredentials) return savedCredentials;
  return { ...savedCredentials, ...runtimeCredentials };
}

function openBatchCommand() {
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("terminal.onlyDesktop"));
    return;
  }
  selectedBatchProfileIds.value = [];
  batchCommand.value = "";
  batchCredential.value = "";
  batchResults.value = [];
  batchCommandVisible.value = true;
}

function closeBatchCommand() {
  if (batchExecuting.value) return;
  batchCommandVisible.value = false;
}

function toggleBatchProfile(profileId: string) {
  selectedBatchProfileIds.value = selectedBatchProfileIds.value.includes(profileId)
    ? selectedBatchProfileIds.value.filter((id) => id !== profileId)
    : [...selectedBatchProfileIds.value, profileId];
}

function toggleAllBatchProfiles() {
  selectedBatchProfileIds.value = allBatchProfilesSelected.value
    ? []
    : batchEligibleProfiles.value.map((profile) => profile.id);
}

function batchServerPayload(profile: SshConnectionProfile) {
  const credentials = runtimeCredentialsFor(profile.id);
  const sharedCredential = batchCredential.value || undefined;
  return {
    id: profile.id,
    host: profile.host,
    port: profile.port,
    username: profile.username,
    authType: profile.authType,
    algorithmPolicy: profile.algorithmPolicy ?? "modern",
    password:
      credentials?.password ?? (profile.authType === "password" ? sharedCredential : undefined),
    keyFile: profile.keyFile,
    passphrase:
      credentials?.passphrase ?? (profile.authType === "key" ? sharedCredential : undefined),
    jumpHost: profile.jumpHost
      ? {
          ...profile.jumpHost,
          algorithmPolicy: profile.algorithmPolicy ?? "modern",
          password:
            credentials?.jumpPassword ??
            (profile.jumpHost.authType === "password" ? sharedCredential : undefined),
          passphrase:
            credentials?.jumpPassphrase ??
            (profile.jumpHost.authType === "key" ? sharedCredential : undefined),
        }
      : undefined,
    socksProxy: profile.socksProxy
      ? {
          host: profile.socksProxy.host,
          port: profile.socksProxy.port,
          username: profile.socksProxy.username,
          password: credentials?.socksPassword,
        }
      : undefined,
  };
}

function batchResultSucceeded(result: BatchExecResult) {
  return result.exitCode === 0 && !result.error;
}

function batchResultText(result: BatchExecResult) {
  const output = [
    result.stdout ? `stdout\n${result.stdout.trim()}` : "",
    result.stderr ? `stderr\n${result.stderr.trim()}` : "",
    result.error ?? "",
  ].filter(Boolean);
  return output.join("\n\n") || t("ssh.manager.batchCommandNoOutput");
}

async function sendBatchCommand() {
  const command = batchCommand.value.trim();
  if (!selectedBatchProfiles.value.length) {
    ElMessage.warning(t("ssh.manager.batchCommandNoSelection"));
    return;
  }
  if (!command) {
    ElMessage.warning(t("ssh.manager.batchCommandEmpty"));
    return;
  }
  if (batchNeedsCredential.value && !batchCredential.value) {
    ElMessage.warning(t("ssh.manager.batchCommandCredentialsRequired"));
    return;
  }

  try {
    await ElMessageBox.confirm(
      t("ssh.manager.batchCommandConfirm").replace(
        "{count}",
        String(selectedBatchProfiles.value.length)
      ),
      t("ssh.manager.batchCommandTitle"),
      {
        type: "warning",
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return;
  }

  batchExecuting.value = true;
  batchResults.value = [];
  try {
    batchResults.value = await invokeBackend<BatchExecResult[]>("ssh_batch_exec", {
      payload: {
        servers: selectedBatchProfiles.value.map(batchServerPayload),
        command,
      },
    });
    const successCount = batchResults.value.filter(batchResultSucceeded).length;
    ElMessage.success(
      t("ssh.manager.batchCommandSent")
        .replace("{success}", String(successCount))
        .replace("{count}", String(batchResults.value.length))
    );
    batchHistory.value = [
      {
        id: String(Date.now()),
        command,
        serverHosts: selectedBatchProfiles.value.map((p) => p.host),
        timestamp: Date.now(),
        results: batchResults.value,
      },
      ...batchHistory.value,
    ].slice(0, MAX_BATCH_HISTORY);
    void savePersistedValue(
      BATCH_HISTORY_KEY,
      normalizeSshBatchHistoryForPersistence(batchHistory.value),
    );
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : t("ssh.manager.batchCommandFailed"));
  } finally {
    batchExecuting.value = false;
  }
}

async function exportBatchResults() {
  const data = {
    command: batchCommand.value,
    timestamp: new Date().toISOString(),
    results: batchResults.value,
  };
  try {
    await saveTextFileToDisk({
      title: t("ssh.manager.batchCommandExport"),
      filename: `batch-results-${new Date().toISOString().slice(0, 10)}.json`,
      content: JSON.stringify(data, null, 2),
      filters: [{ DisplayName: "JSON", Pattern: "*.json" }],
      browserMimeType: "application/json",
    });
  } catch (cause) {
    ElMessage.error(cause instanceof Error ? cause.message : String(cause));
  }
}

function restoreFromHistory(entry: BatchHistoryEntry) {
  batchCommand.value = entry.command;
  batchHistoryExpanded.value = false;
}

function clearBatchHistory() {
  batchHistory.value = [];
  void deletePersistedValue(BATCH_HISTORY_KEY);
}

async function exportProfiles() {
  const choice = await confirmSecretExport();
  if (!choice) return;
  const payload = buildSshProfileExport(
    connections.profiles,
    (profileId) => connections.credentialsFor(profileId),
    { includeSecrets: choice === "with-secrets", exportedAt: new Date().toISOString() }
  );
  try {
    const saved = await saveTextFileToDisk({
      title: t("common.export"),
      filename: "mincode-ssh-connections.json",
      content: JSON.stringify(payload, null, 2),
      filters: [{ DisplayName: "JSON", Pattern: "*.json" }],
      browserMimeType: "application/json",
    });
    if (saved === false) return;
    ElMessage.success(
      payload.includesSecrets
        ? t("security.secretExport.doneWithSecrets")
        : t("security.secretExport.doneWithoutSecrets")
    );
  } catch (cause) {
    ElMessage.error(cause instanceof Error ? cause.message : String(cause));
  }
}

function chooseImport() {
  document.querySelector<HTMLInputElement>("#ssh-profile-import")?.click();
}

async function importProfiles(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  try {
    const parsed: unknown = JSON.parse(await file.text());
    const entries = parseSshProfileImportBundle(parsed);
    if (!entries.length) throw new Error(t("ssh.manager.invalidImport"));
    const withSecrets = sshProfileImportHasSecrets(parsed);
    if (withSecrets && !(await confirmSecretImport())) return;
    for (const entry of entries) {
      const saved = connections.saveProfile(entry.profile);
      if (entry.credentials) await connections.saveCredentials(saved.id, entry.credentials);
    }
    await connections.persistProfiles();
    ElMessage.success(t("ssh.manager.imported").replace("{count}", String(entries.length)));
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : t("ssh.manager.invalidImport"));
  } finally {
    input.value = "";
  }
}

let persistTimer: ReturnType<typeof setTimeout> | undefined;

function persistedSshSessions() {
  return sshSessionTabs.value
    .filter((session) => !isWorkspaceScopePermanentlyDeleted(session.workspaceScopeId))
    .map(serializeSshWorkspaceSessionTab);
}

function schedulePersistSessions() {
  if (persistTimer) clearTimeout(persistTimer);
  persistTimer = setTimeout(() => {
    persistTimer = undefined;
    void savePersistedValue(SSH_SESSIONS_STORAGE_KEY, persistedSshSessions());
  }, PERSIST_DEBOUNCE_MS);
}

// Watch only the serialized layout: pane status, credentials and profile
// details churn while connecting but are not persisted.
watch(() => JSON.stringify(persistedSshSessions()), schedulePersistSessions);

// Hosts can disappear from many places (sidebar, sync, import overwrite), so
// prune "recently opened" against the profile list instead of each delete
// action. Before hydration the list may be empty, which would wipe every record;
// waiting for the recent store too keeps stale records it loads from surviving.
watch(
  () => (connections.hydrated && sshRecent.hydrated ? connections.profiles.map((profile) => profile.id) : null),
  (ids) => {
    if (ids) sshRecent.prune(new Set(ids));
  },
  { immediate: true }
);

onMounted(async () => {
  void sshRecent.hydrate();
  await Promise.all([hydrateSidebarLayout(), connections.hydrate()]);
  const [storedSessions, storedHistory] = await Promise.all([
    loadPersistedValue<unknown[]>(SSH_SESSIONS_STORAGE_KEY),
    loadPersistedValue<BatchHistoryEntry[]>(BATCH_HISTORY_KEY),
  ]);
  if (Array.isArray(storedSessions)) {
    const restored = storedSessions
      .map((raw) =>
        deserializeSshWorkspaceSessionTab(
          raw,
          (id) => connections.findProfile(id),
          (id) => connections.credentialsFor(id)
        )
      )
      .filter((tab): tab is SshWorkspaceSessionTab => tab !== null);
    if (restored.length > 0) sshSessionTabs.value = restored;
  }
  if (Array.isArray(storedHistory)) {
    batchHistory.value = normalizeSshBatchHistoryForPersistence(storedHistory);
  }
});
onBeforeUnmount(() => {
  if (persistTimer) {
    clearTimeout(persistTimer);
    void savePersistedValue(
      SSH_SESSIONS_STORAGE_KEY,
      sshSessionTabs.value
        .filter((session) => !isWorkspaceScopePermanentlyDeleted(session.workspaceScopeId))
        .map(serializeSshWorkspaceSessionTab)
    );
  }
  unregisterSshWorkspaceActions();
  unregisterNewSshConnectionCommand();
  // Keep native SSH sessions alive when KeepAlive evicts this view. Restored panes
  // reuse their stable session IDs and attach to the existing backend shells.
});
</script>

<template>
  <section ref="workspaceElement" class="ssh-workspace">
    <AppSplitPane
      :model-value="sidebarWidth"
      :collapsed="sidebarCollapsed"
      layout="horizontal"
      side="first"
      :min="200"
      :max="(workspaceWidth) => Math.min(480, workspaceWidth * 0.56)"
      :second-min="320"
      collapsible
      :collapse-label="t('ssh.collapseSidebar')"
      :expand-label="t('ssh.expandSidebar')"
      :aria-label="t('ssh.resizeSidebar')"
      @resize-end="persistSidebarWidth"
      @update:collapsed="updateSidebarCollapsed"
    >
      <template #first>
        <aside v-if="!sidebarCollapsed" class="ssh-manager-sidebar">
      <div class="db-explorer__toolbar">
        <strong class="db-explorer__title">{{ t("ssh.manager.title") }}</strong>
        <el-dropdown trigger="click" placement="bottom-start">
          <el-button
            text
            native-type="button"
            class="db-square-action"
            :title="t('common.import')"
            :aria-label="t('common.import')"
          >
            <el-icon><Upload /></el-icon>
          </el-button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item @click="chooseImport">{{ t("ssh.manager.importJson") }}</el-dropdown-item>
              <el-dropdown-item @click="configSyncVisible = true">{{ t("ssh.manager.importConfig") }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <el-button
          text
          native-type="button"
          class="db-square-action db-explorer__secondary"
          :title="t('common.export')"
          :aria-label="t('common.export')"
          :disabled="!connections.profiles.length"
          @click="exportProfiles"
        >
          <el-icon><Download /></el-icon>
        </el-button>
        <el-button
          text
          native-type="button"
          class="db-square-action"
          :title="t('ssh.manager.knownHosts')"
          :aria-label="t('ssh.manager.knownHosts')"
          @click="knownHostsVisible = true"
        >
          <el-icon><Key /></el-icon>
        </el-button>
        <el-button
          text
          native-type="button"
          class="db-square-action db-explorer__secondary"
          :title="t('ssh.manager.batchCommand')"
          :aria-label="t('ssh.manager.batchCommand')"
          :disabled="!batchEligibleProfiles.length"
          @click="openBatchCommand"
        >
          <el-icon><Connection /></el-icon>
        </el-button>
        <!-- Shown only when the sidebar is too narrow for the secondary buttons (see navigator.css). -->
        <el-dropdown class="db-explorer__more" trigger="click" placement="bottom-end">
          <el-button
            text
            native-type="button"
            class="db-square-action"
            :title="t('common.more')"
            :aria-label="t('common.more')"
          >
            <el-icon><MoreFilled /></el-icon>
          </el-button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item :disabled="!connections.profiles.length" @click="exportProfiles"
                ><el-icon><Download /></el-icon>{{ t("common.export") }}</el-dropdown-item
              >
              <el-dropdown-item :disabled="!batchEligibleProfiles.length" @click="openBatchCommand"
                ><el-icon><Connection /></el-icon>{{ t("ssh.manager.batchCommand") }}</el-dropdown-item
              >
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <el-button
          text
          native-type="button"
          class="db-square-action is-primary"
          :title="t('ssh.newConnection')"
          :aria-label="t('ssh.newConnection')"
          @click="newConnection"
        >
          <el-icon><Plus /></el-icon>
        </el-button>
        <input
          id="ssh-profile-import"
          hidden
          type="file"
          accept="application/json,.json"
          @change="importProfiles"
        />
      </div>

      <div class="db-search-row">
        <AppSearchField
          v-model="searchQuery"
          class="db-search-box"
          clearable
          :clear-label="t('common.clear')"
          :placeholder="t('ssh.manager.searchPlaceholder')"
        />
        <AppFilterButton
          class="db-search-filter-trigger"
          :active="filterOpen || filtersActive"
          :label="t('ssh.manager.filter')"
          :aria-expanded="filterOpen"
          @click="filterOpen = !filterOpen"
        />
      </div>

      <section v-if="filterOpen" class="db-connection-filter-panel">
        <el-segmented
          v-model="sshFilterScope"
          class="db-connection-filter-scopes"
          size="small"
          :options="sshFilterScopeOptions"
          :aria-label="t('ssh.manager.filter')"
        />
        <div class="db-connection-filter-fields">
          <el-select
            filterable
            v-model="selectedGroupFilter"
            class="db-connection-filter-select"
            size="small"
            clearable
            :placeholder="t('ssh.manager.group')"
          >
            <el-option
              v-if="groupSummary.some(([group]) => !group)"
              :label="t('ssh.manager.ungrouped')"
              :value="ungroupedFilter"
            />
            <el-option
              v-for="[group, count] in groupSummary.filter(([group]) => Boolean(group))"
              :key="group"
              :label="`${group} (${count})`"
              :value="group"
            />
          </el-select>
          <el-select
            filterable
            v-model="selectedTagFilter"
            class="db-connection-filter-select"
            size="small"
            clearable
            :placeholder="t('ssh.manager.tag')"
          >
            <el-option
              v-for="[tag, count] in tagSummary"
              :key="tag"
              :label="`${tag} (${count})`"
              :value="tag"
            />
          </el-select>
        </div>
      </section>

      <el-scrollbar ref="sidebarScroller" class="db-tree-scroll">
        <section class="db-connection-navigator ssh-connection-navigator">
          <div v-if="!filteredProfiles.length" class="db-connection-empty">
            {{ connections.profiles.length ? t("ssh.manager.noMatch") : t("ssh.manager.empty") }}
          </div>
          <div
            v-for="group in profileGroups"
            :key="group.key || ungroupedFilter"
            class="db-connection-group"
            :class="{
              'is-drop-before': sshGroupDrop?.group === group.key && sshGroupDrop.position === 'before',
              'is-drop-after': sshGroupDrop?.group === group.key && sshGroupDrop.position === 'after',
              'is-drop-target': sshSidebarDrag?.type === 'profile',
            }"
            @dragover="onSshGroupDragOver(group.key, $event)"
            @drop="onSshGroupDrop(group.key, $event)"
          >
            <div class="db-connection-group__header">
              <button
                type="button"
                class="db-connection-group__title"
                :draggable="Boolean(group.key && sshDragEnabled)"
                :title="group.key ? t('ssh.manager.reorderGroups') : undefined"
                :aria-expanded="groupIsOpen(group.key)"
                @dragstart="onSshGroupDragStart(group.key, $event)"
                @dragend="clearSshSidebarDrag"
                @click="toggleGroup(group.key)"
              >
                <i
                  class="db-connection-group__bar"
                  aria-hidden="true"
                  :style="{ background: avatarColor(group.key || 'ungrouped') }"
                />
                <span>{{ group.label }}</span
                ><small>{{ group.profiles.length }}</small>
                <AppChevron class="db-tree-chevron" :direction="groupIsOpen(group.key) ? 'down' : 'right'" />
              </button>
            </div>
            <div v-if="groupIsOpen(group.key)" class="db-connection-group__children" v-bind="{ [ROW_GROUP_ATTRIBUTE]: group.key }">
              <div v-if="sidebarRows.windowOf(group.key, group.profiles).paddingTop" aria-hidden="true" :style="{ height: `${sidebarRows.windowOf(group.key, group.profiles).paddingTop}px` }" />
              <div
                v-for="profile in sidebarRows.windowOf(group.key, group.profiles).rows"
                :key="profile.id"
                class="db-connection-entry"
                :class="{
                  'is-active': activeProfileIds.has(profile.id),
                  'is-dragging': sshSidebarDrag?.type === 'profile' && sshSidebarDrag.profileId === profile.id,
                }"
                @dblclick="openSavedProfile(profile)"
              >
                <div class="db-connection-node">
                  <button
                    type="button"
                    class="db-connection-node__main"
                    :draggable="sshDragEnabled"
                    :title="t('ssh.manager.dragProfileToGroup')"
                    @dragstart="onSshProfileDragStart(profile, $event)"
                    @dragend="clearSshSidebarDrag"
                    @keydown.enter.prevent="openSavedProfile(profile)"
                    @keydown.space.prevent="openSavedProfile(profile)"
                  >
                    <ConnectionAvatar
                      :name="profile.name"
                      :seed="profile.id"
                      :size="26"
                      :status="sidebarAvatarStatus(profile.id)"
                    />
                    <span class="db-connection-node__meta"
                      ><strong>{{ profile.name }}</strong
                      ><em>{{ profile.username }}@{{ profile.host }}:{{ profile.port }}</em></span
                    >
                    <span
                      class="db-connection-node__status"
                      :class="{
                        'is-connected': profileSessionStatus.get(profile.id) === 'ready',
                        'is-connecting': profileSessionStatus.get(profile.id) === 'connecting',
                      }"
                      :title="
                        profileSessionStatus.get(profile.id) === 'ready'
                          ? t('ssh.status.ready')
                          : profileSessionStatus.get(profile.id) === 'connecting'
                            ? t('ssh.status.connecting')
                            : undefined
                      "
                    />
                  </button>
                  <LinkedNotesBadge type="ssh" :id="profile.id" :name="profile.name" />
                  <button
                    type="button"
                    class="db-connection-node__menu"
                    :title="t('common.more')"
                    :aria-label="t('common.more')"
                    aria-haspopup="menu"
                    @click.stop="openProfileMenu($event, profile.id)"
                    @dblclick.stop
                  >
                    <el-icon><MoreFilled /></el-icon>
                  </button>
                </div>
              </div>
              <div v-if="sidebarRows.windowOf(group.key, group.profiles).paddingBottom" aria-hidden="true" :style="{ height: `${sidebarRows.windowOf(group.key, group.profiles).paddingBottom}px` }" />
            </div>
          </div>
        </section>
      </el-scrollbar>

      <footer class="db-explorer__footer">
        <span class="db-explorer__footer-summary">{{
          t("ssh.manager.footerSummary", { total: connections.profiles.length, online: onlineProfileCount })
        }}</span>
        <el-button
          text
          size="small"
          native-type="button"
          :title="t('ssh.manager.importConfig')"
          @click="configSyncVisible = true"
        >
          <el-icon><Refresh /></el-icon>
          <span>{{ t("ssh.manager.sync") }}</span>
        </el-button>
      </footer>
        </aside>
      </template>
      <template #second>

    <el-dialog
      append-to-body
      v-model="batchCommandVisible"
      class="ssh-batch-command-dialog"
      width="640px"
      align-center
      :close-on-click-modal="!batchExecuting"
      :close-on-press-escape="!batchExecuting"
      :show-close="!batchExecuting"
      @closed="closeBatchCommand"
    >
      <template #header>
        <div class="ssh-batch-command-dialog__heading">
          <span class="ssh-batch-command-dialog__heading-icon" aria-hidden="true">
            <el-icon><Connection /></el-icon>
          </span>
          <strong>{{ t("ssh.manager.batchCommandTitle") }}</strong>
        </div>
      </template>

      <el-scrollbar class="ssh-batch-command-dialog__scrollbar">
        <div class="ssh-batch-command">
          <div class="ssh-batch-command__section">
            <div class="ssh-batch-command__section-header">
              <div>
                <strong>{{ t("ssh.manager.batchCommandServers") }}</strong>
                <small>{{
                  t("ssh.manager.batchCommandTargets").replace(
                    "{count}",
                    String(selectedBatchProfiles.length)
                  )
                }}</small>
              </div>
              <el-button
                text
                native-type="button"
                class="ssh-batch-command__select-all"
                :disabled="!batchEligibleProfiles.length || batchExecuting"
                @click="toggleAllBatchProfiles"
              >
                {{
                  allBatchProfilesSelected
                    ? t("common.clear")
                    : t("ssh.manager.batchCommandSelectAll")
                }}
              </el-button>
            </div>
            <el-scrollbar class="ssh-batch-command__servers">
              <el-checkbox
                v-for="profile in batchProfiles"
                :key="profile.id"
                class="ssh-batch-command__server"
                :class="{ 'is-disabled': profile.authType === 'keyboard-interactive' }"
                :model-value="selectedBatchProfileIds.includes(profile.id)"
                :disabled="profile.authType === 'keyboard-interactive' || batchExecuting"
                @change="toggleBatchProfile(profile.id)"
              >
                <span>
                  <strong>{{ profile.name }}</strong>
                  <small>{{ profile.username }}@{{ profile.host }}:{{ profile.port }}</small>
                </span>
                <em v-if="profile.authType === 'keyboard-interactive'">
                  {{ t("ssh.manager.batchCommandUnsupportedAuth") }}
                </em>
              </el-checkbox>
            </el-scrollbar>
          </div>

          <label class="ssh-batch-command__field">
            <span>{{ t("ssh.manager.batchCommandInput") }}</span>
            <el-input
              v-model="batchCommand"
              type="textarea"
              :rows="4"
              :disabled="batchExecuting"
              :placeholder="t('ssh.manager.batchCommandPlaceholder')"
            />
          </label>

          <div v-if="batchHistory.length" class="ssh-batch-command__history">
            <button
              type="button"
              class="ssh-batch-command__history-toggle"
              @click="batchHistoryExpanded = !batchHistoryExpanded"
            >
              {{ t("ssh.manager.batchCommandHistory") }}
              <AppChevron :open="batchHistoryExpanded" />
            </button>
            <div v-if="batchHistoryExpanded" class="ssh-batch-command__history-list">
              <button
                v-for="entry in batchHistory"
                :key="entry.id"
                type="button"
                class="ssh-batch-command__history-entry"
                @click="restoreFromHistory(entry)"
              >
                <code>{{ entry.command }}</code>
                <small>{{ entry.serverHosts.join(", ") }} · {{ new Date(entry.timestamp).toLocaleString() }}</small>
              </button>
              <button
                type="button"
                class="ssh-batch-command__history-clear"
                @click="clearBatchHistory"
              >
                {{ t("ssh.manager.batchCommandClearHistory") }}
              </button>
            </div>
          </div>

          <label class="ssh-batch-command__field">
            <span>{{ t("ssh.manager.batchCommandCredential") }}</span>
            <el-input
              v-model="batchCredential"
              type="password"
              show-password
              :disabled="batchExecuting"
              :placeholder="t('ssh.manager.batchCommandCredentialPlaceholder')"
            />
            <small>{{ t("ssh.manager.batchCommandCredentialHint") }}</small>
          </label>

          <div v-if="batchResults.length" class="ssh-batch-command__results">
            <div class="ssh-batch-command__results-header">
              <strong>{{ t("ssh.manager.batchCommandResults") }}</strong>
              <el-button text size="small" :icon="Download" @click="exportBatchResults">
                {{ t("ssh.manager.batchCommandExport") }}
              </el-button>
            </div>
            <article
              v-for="result in batchResults"
              :key="result.id"
              class="ssh-batch-command__result"
              :class="{
                'is-success': batchResultSucceeded(result),
                'is-error': !batchResultSucceeded(result),
              }"
            >
              <header>
                <span>{{ result.host }}</span>
                <small
                  >{{
                    batchResultSucceeded(result)
                      ? t("ssh.manager.batchCommandResultSuccess")
                      : t("ssh.manager.batchCommandResultFailed")
                  }}
                  · {{ result.durationMs }} ms</small
                >
              </header>
              <pre>{{ batchResultText(result) }}</pre>
            </article>
          </div>
        </div>
      </el-scrollbar>
      <template #footer>
        <el-button size="small" :disabled="batchExecuting" @click="closeBatchCommand">
          {{ t("common.cancel") }}
        </el-button>
        <el-button size="small" type="primary" :loading="batchExecuting" @click="sendBatchCommand">
          {{ t("common.send") }}
        </el-button>
      </template>
    </el-dialog>

    <SshKnownHostsDialog v-model="knownHostsVisible" />
    <SshConfigSyncDialog v-model="configSyncVisible" />

    <main class="ssh-manager-detail">
      <SshConnectionDialog
        v-if="dialogVisible"
        v-model="dialogVisible"
        :initial="editingDraft"
        :initial-address="pendingAddress"
        :group-options="profileGroupNames"
        :tag-options="profileTagNames"
        :connect-after-save="false"
        @saved="savedConnection"
      />
      <template v-else>
        <AppWorkspaceTabs
          v-model="activeSessionId"
          class="ssh-session-tabs"
          :aria-label="t('nav.ssh')"
          @tab-remove="removeSshSessionTab"
          @contextmenu="openSessionTabContextMenu"
        >
          <el-tab-pane :name="SSH_OVERVIEW_TAB_ID">
            <template #label>
              <span class="ssh-session-tab__label">{{ t("ssh.overview.title") }}</span>
            </template>
          </el-tab-pane>
          <el-tab-pane
            v-for="session in workspaceSessionTabs"
            :key="session.id"
            :name="session.id"
            closable
          >
            <template #label>
              <span
                class="ssh-session-tab__status"
                :class="`is-${findSshWorkspacePane(session.root, session.activePaneId)?.status ?? 'connecting'}`"
                :title="
                  findSshWorkspacePane(session.root, session.activePaneId)?.statusDetail ||
                  sessionStatusLabel(findSshWorkspacePane(session.root, session.activePaneId)?.status ?? 'connecting')
                "
                :aria-label="
                  findSshWorkspacePane(session.root, session.activePaneId)?.statusDetail ||
                  sessionStatusLabel(findSshWorkspacePane(session.root, session.activePaneId)?.status ?? 'connecting')
                "
                role="img"
              />
              <span class="ssh-session-tab__label">{{ findSshWorkspacePane(session.root, session.activePaneId)?.profile.name }}</span>
            </template>
          </el-tab-pane>
        </AppWorkspaceTabs>
        <AppContextMenu
          v-if="profileMenu && profileMenuProfile"
          :open="true"
          :x="profileMenu.x"
          :y="profileMenu.y"
          @close="closeProfileMenu"
        >
          <el-dropdown-item command="file-transfer" @click="runProfileMenu(openSavedFileTransfer)"><el-icon><Folder /></el-icon>{{ t('ssh.fileTransfer.open') }}</el-dropdown-item>
          <el-dropdown-item command="copy" @click="runProfileMenu((profile) => handleSshProfileAction('copy', profile))"><el-icon><CopyDocument /></el-icon>{{ t('ssh.manager.copyInfo') }}</el-dropdown-item>
          <el-dropdown-item command="edit" @click="runProfileMenu((profile) => handleSshProfileAction('edit', profile))"><el-icon><Edit /></el-icon>{{ t('common.edit') }}</el-dropdown-item>
          <el-dropdown-item divided command="notes-new" @click="runProfileMenu((profile) => openProfileNotes(profile, true))"><el-icon><Plus /></el-icon>{{ t('notes.link.newLinked') }}</el-dropdown-item>
          <el-dropdown-item command="notes-view" @click="runProfileMenu((profile) => openProfileNotes(profile))"><el-icon><Notebook /></el-icon>{{ t('notes.link.viewLinked', { count: linkedNotes.countOf('ssh', profileMenu.profileId) }) }}</el-dropdown-item>
          <el-dropdown-item divided command="delete" class="is-danger" @click="runProfileMenu((profile) => handleSshProfileAction('delete', profile))"><el-icon><Delete /></el-icon>{{ t('common.delete') }}</el-dropdown-item>

        </AppContextMenu>
        <AppContextMenu
          v-if="sessionTabContextMenu"
          :open="true"
          :x="sessionTabContextMenu.x"
          :y="sessionTabContextMenu.y"
          :width="210"
          popper-class="ssh-session-tab-context-menu"
          @close="closeSessionTabContextMenu"
        >
          <template v-if="!sessionTabContextIsOverview">
            <el-dropdown-item @click="runSessionTabContextCommand('close')">
              {{ t("common.closeTab") }}
            </el-dropdown-item>
            <el-dropdown-item
              :disabled="!sessionTabContextHasLeft"
              @click="runSessionTabContextCommand('close-left')"
            >
              {{ t("tab.closeLeft") }}
            </el-dropdown-item>
            <el-dropdown-item
              :disabled="!sessionTabContextHasRight"
              @click="runSessionTabContextCommand('close-right')"
            >
              {{ t("tab.closeRight") }}
            </el-dropdown-item>
            <el-dropdown-item
              divided
              :disabled="workspaceSessionTabs.length <= 1"
              @click="runSessionTabContextCommand('close-other')"
            >
              {{ t("tab.closeOther") }}
            </el-dropdown-item>
          </template>
          <el-dropdown-item
            :divided="!sessionTabContextIsOverview"
            :disabled="workspaceSessionTabs.length === 0"
            @click="runSessionTabContextCommand('close-all')"
          >
            {{ t("tab.closeAll") }}
          </el-dropdown-item>
        </AppContextMenu>
        <SshServerOverview
          v-if="activeSessionId === SSH_OVERVIEW_TAB_ID"
          :profiles="connections.sortedProfiles"
          :sessions="overviewSessions"
          :opened-at="sshRecent.openedAt"
          :recent-cleared-at="sshRecent.clearedAt"
          :group-order="connections.groupOrder"
          @connect="openSavedProfile"
          @open-session="activeSessionId = $event"
          @new-connection="newConnection"
          @new-connection-for-address="newConnectionForAddress"
          @clear-recent="sshRecent.clearRecent()"
          @import="chooseImport"
          @known-hosts="knownHostsVisible = true"
          @file-transfer="openSavedFileTransfer"
        />
        <div v-else-if="activeSession" class="ssh-session-content">
          <SshTerminalPaneTree
            v-for="session in sshSessionTabs"
            v-show="session.workspaceScopeId === workspaceScopeId && session.id === activeSessionId"
            :key="session.id"
            :node="session.root"
            :active-pane-id="session.activePaneId"
            :can-split="countSshWorkspacePanes(session.root) < MAX_SSH_SESSION_PANES"
            @select="selectWorkspacePane(session.id, $event)"
            @close="closeWorkspacePane(session.id, $event)"
            @reload="reloadWorkspacePane(session.id, $event)"
            @configure="configureWorkspaceSession(session.id, $event)"
            @split="(paneId, direction) => splitWorkspacePane(session.id, paneId, direction)"
            @status-change="
              (paneId, status, detail) =>
                updateWorkspaceSessionStatus(session.id, paneId, status, detail)
            "
            @resize="(splitId, sizes) => resizeWorkspaceSplit(session.id, splitId, sizes)"
          />
        </div>
      </template>
        </main>
      </template>
    </AppSplitPane>
  </section>
</template>
