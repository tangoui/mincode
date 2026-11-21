<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onDeactivated, onMounted, ref, shallowRef, watch, watchEffect } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { useRoute } from "vue-router";
import {
  Back,
  Close,
  Delete,
  Download,
  DocumentAdd,
  EditPen,
  FolderAdd,
  FolderOpened,
  Connection,
  Lock,
  Position,
  Refresh,
  View,
  Upload,
} from "@element-plus/icons-vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import PluginServiceMenuItems from "@/components/plugins/PluginServiceMenuItems.vue";
import { usePluginMenuTarget } from "@/composables/use-plugin-menu-target";
import {
  sftpFileMenuTarget,
  sftpFileServiceTarget,
  sftpMenuConnection,
  type SftpMenuConnection,
} from "@/domain/sftp/menu-target";
import SftpFileList from "@/components/sftp/SftpFileList.vue";
import SftpTransferPanel from "@/components/sftp/SftpTransferPanel.vue";
import { formatSftpTransferBytes, type SftpFileListCells } from "@/components/sftp/sftp-file-list";
import { useSftpTransferQueue } from "@/composables/useSftpTransferQueue";
import {
  isSafeSftpEntryName,
  type SftpConflictStrategy,
  type SftpTransferDraft,
  type SftpTransferItem,
} from "@/domain/sftp/transfer-queue";
import { useVueI18n } from "@/i18n/messages";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import {
  chooseLocalSftpDirectory,
  connectSftp,
  createSftpDirectory,
  deleteLocalFileEntry,
  deleteSftpEntry,
  disconnectSftp,
  cancelSftpTransfer,
  listLocalFiles,
  listSftpDirectory,
  moveLocalFileEntry,
  isSftpImageFile,
  readSftpImagePreview,
  readSftpTextFile,
  renameLocalFileEntry,
  renameSftpEntry,
  resolveSftpInitialPath,
  runSftpTransfer,
  writeSftpTextFile,
  type LocalFileEntry as LocalEntry,
  type SftpEntry as Entry,
} from "@/services/sftp-service";
import { getUserHomeDirectory } from "@/services/settings-service";
import { useSftpSessionsStore } from "@/stores/sftp-sessions";
import { useSshConnectionsStore } from "@/stores/ssh-connections";

// The remote file editor is opened on demand; loading it lazily keeps Monaco out of this view's startup chunk.
const MonacoTextEditor = defineAsyncComponent(() => import("@/components/projects/MonacoTextEditor.vue"));

defineOptions({ name: "SftpWorkspaceView" });

interface RemoteEditorTab {
  path: string;
  name: string;
  content: string;
  dirty: boolean;
  loading: boolean;
  saving: boolean;
  error?: string;
  isImage: boolean;
  imageUrl?: string;
}

const { t } = useVueI18n();
const route = useRoute();
const workspaceStateScope = useWorkspaceStateScope();
const workspaceScopeId = workspaceStateScope.capture().scopeId;
const ssh = useSshConnectionsStore();
const sftpSessions = useSftpSessionsStore();
const profileId = ref(ssh.sortedProfiles[0]?.id ?? "");
const sessionId = ref("");
const sshPassword = ref("");
const ftpPassword = ref("");
const remotePath = ref("/");
const entries = ref<Entry[]>([]);
const selected = ref<Entry>();
const renamingPath = ref("");
const renameValue = ref("");
const renamingLocalPath = ref("");
const localRenameValue = ref("");
const loading = ref(false);
const profile = computed(() => ssh.findProfile(profileId.value));
const localRoot = ref("");
const localPath = ref("");
const localInputText = ref("");
const localInputEditing = ref(false);
const localEntries = ref<LocalEntry[]>([]);
const selectedLocal = ref<LocalEntry>();
const editorTabs = ref<RemoteEditorTab[]>([]);
const activeEditorPath = ref("");
const activeEditor = computed(() =>
  editorTabs.value.find((tab) => tab.path === activeEditorPath.value)
);
const isReady = computed(() => Boolean(sessionId.value));
const protocol = computed(() => profile.value?.fileTransfer?.protocol ?? "sftp");
const protocolLabel = computed(() => {
  if (protocol.value === "ftp") return "FTP";
  if (protocol.value === "ftps-explicit") return t("ssh.fileTransfer.protocol.ftpsExplicit");
  return "SFTP";
});
const requiresFtpPassword = computed(() => protocol.value !== "sftp");
const transferPanelOpen = ref(false);
const remoteDropActive = ref(false);
const localDropActive = ref(false);
const conflictStrategy = ref<SftpConflictStrategy>("overwrite");
const LOCAL_DRAG_TYPE = "application/x-app-sftp-local-entry";
const REMOTE_DRAG_TYPE = "application/x-app-sftp-remote-entry";

interface RemoteContextMenuState { item?: Entry; x: number; y: number }
interface LocalContextMenuState { item: LocalEntry; x: number; y: number }
const remoteContextMenu = ref<RemoteContextMenuState | null>(null);
const localContextMenu = ref<LocalContextMenuState | null>(null);
// The session that is open and the endpoint it was opened with: row menus describe
// this connection, never a profile edited (or switched) after connecting.
const connectedSession = shallowRef<{ id: string; connection: SftpMenuConnection } | null>(null);
function openRowMenuTarget() {
  const item = remoteContextMenu.value?.item;
  const session = connectedSession.value;
  return item && session && session.id === sessionId.value ? { item, session } : null;
}
// Plugin items (contributes.menus sftp/file/context) of the open remote row menu;
// the blank-area menu has no item and so no target.
const { host: remotePluginMenuHost, context: remotePluginMenuContext } = usePluginMenuTarget("sftp/file/context", () => {
  const open = openRowMenuTarget();
  return open ? sftpFileMenuTarget(open.session.connection, open.item) : null;
});
// Plugin services (surface sftpFile) on a remote file row: read through the open session.
function remoteServiceTarget() {
  const open = openRowMenuTarget();
  return open ? sftpFileServiceTarget(open.session.id, open.session.connection, open.item) : null;
}
// A row menu belongs to one session's listing: never pair its row with another profile's endpoint.
watch([profileId, sessionId], () => {
  remoteContextMenu.value = null;
});
// This view is KeepAlive'd: leaving the route must not leave a row menu (and its target) open.
onDeactivated(() => {
  remoteContextMenu.value = null;
});
let disposed = false;
let previewGeneration = 0;
const localFileList = ref<{ scrollToPath: (path: string) => void }>();
const remoteFileList = ref<{ scrollToPath: (path: string) => void }>();

function clearImagePreviews(tabs: RemoteEditorTab[], invalidate = true) {
  if (invalidate) previewGeneration += 1;
  for (const tab of tabs) tab.imageUrl = undefined;
}

interface LocalDragPayload {
  root: string;
  entry: LocalEntry;
}

interface RemoteDragPayload {
  sessionId: string;
  entry: Entry;
}

function remoteJoin(name: string) {
  const base = remotePath.value.replace(/\/$/, "");
  return base ? `${base}/${name}` : `/${name}`;
}

function localAbsolute(path: string) {
  return `${localRoot.value.replace(/\/$/, "")}/${path}`;
}

function joinLocalRelative(base: string, name: string) {
  return [base, name].filter(Boolean).join("/");
}

function joinRemote(base: string, name: string) {
  const normalized = base.replace(/\/$/, "");
  return normalized ? `${normalized}/${name}` : `/${name}`;
}

function formatEntrySize(item: Entry) {
  return item.isDir ? "—" : formatSftpTransferBytes(item.size);
}

function formatModified(value?: number) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value * 1000));
}

function validateRemoteEntryName(value: string) {
  const name = value.trim();
  if (!isSafeSftpEntryName(name)) {
    ElMessage.warning(t("sftp.invalidNewEntryName"));
    return "";
  }
  return name;
}

function describeRemoteEntry(item: Entry): SftpFileListCells {
  return { detail: formatModified(item.modified), detailTitle: item.permissions, size: formatEntrySize(item) };
}

function describeLocalEntry(item: LocalEntry): SftpFileListCells {
  return { detail: item.path, size: String(item.size ?? "") };
}

let refreshTimer: ReturnType<typeof setTimeout> | undefined;
function scheduleTransferRefresh(direction: SftpTransferItem["direction"]) {
  if (refreshTimer) clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => {
    refreshTimer = undefined;
    if (direction === "upload") void listRemote();
    else void listLocal();
  }, 120);
}

const transferQueue = useSftpTransferQueue({
  async execute(item, reportProgress) {
    if (!sessionId.value || item.sessionId !== sessionId.value) {
      throw new Error(t("sftp.transferSessionClosed"));
    }
    if (item.operation === "create-remote-directory") {
      await createSftpDirectory(item.sessionId, item.remotePath);
      return { status: "completed" as const };
    }
    if (!item.backendJobId) throw new Error(t("sftp.transferJobUnavailable"));
    const snapshot = await runSftpTransfer(
      {
        jobId: item.backendJobId,
        sessionId: item.sessionId,
        direction: item.direction,
        localPath: item.localPath,
        remotePath: item.remotePath,
        resume: true,
        conflictStrategy: item.conflictStrategy,
      },
      (progress) => {
        reportProgress({
          backendJobId: progress.jobId,
          bytesTransferred: progress.bytesTransferred,
          totalBytes: progress.totalBytes,
          speedBytesPerSecond: progress.speedBytesPerSecond,
          resumedFrom: progress.resumedFrom,
        });
      }
    );
    return {
      status: snapshot.status === "cancelled"
        ? "cancelled" as const
        : snapshot.status === "failed"
          ? "failed" as const
          : "completed" as const,
      error: snapshot.error,
    };
  },
  async cancel(item) {
    if (item.backendJobId) await cancelSftpTransfer(item.backendJobId, item.sessionId);
  },
  onSettled(item) {
    if (item.status === "completed") scheduleTransferRefresh(item.direction);
  },
});
const transferCount = computed(() => transferQueue.items.value.length);
let remoteListSequence = 0;
let listedRemotePath = "";

async function connect() {
  const selectedProfile = profile.value;
  if (!selectedProfile || !isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  if (sessionId.value && !(await requestDisconnect())) return;
  const openedSessionId = `sftp-${encodeURIComponent(workspaceScopeId)}-${selectedProfile.id}`;
  const openedConnection = sftpMenuConnection(selectedProfile);
  sessionId.value = openedSessionId;
  loading.value = true;
  try {
    const credentials = ssh.credentialsFor(selectedProfile.id);
    const remoteHome = await connectSftp(openedSessionId, selectedProfile, {
      ...credentials,
      ...(requiresFtpPassword.value
        ? { ftpPassword: ftpPassword.value || credentials?.ftpPassword || undefined }
        : { password: sshPassword.value || credentials?.password || undefined }),
    });
    remotePath.value = resolveSftpInitialPath(remoteHome, selectedProfile.defaultPath);
    listedRemotePath = remotePath.value;
    if (disposed || sessionId.value !== openedSessionId) {
      await disconnectSftp(openedSessionId).catch(() => undefined);
      return;
    }
    sftpSessions.connected({
      id: openedSessionId,
      name: `${selectedProfile.name.trim()} ${protocolLabel.value}`,
      profileId: selectedProfile.id,
      protocol: protocol.value,
    });
    connectedSession.value = { id: openedSessionId, connection: openedConnection };
    await loadRemoteEntries();
  } catch (error) {
    sftpSessions.disconnected(openedSessionId);
    if (connectedSession.value?.id === openedSessionId) connectedSession.value = null;
    if (sessionId.value === openedSessionId) sessionId.value = "";
    await disconnectSftp(openedSessionId).catch(() => undefined);
    ElMessage.error(String(error));
  } finally {
    loading.value = false;
  }
}

async function disconnect() {
  if (!sessionId.value) return;
  const closingSession = sessionId.value;
  transferQueue.cancelQueued();
  remoteListSequence++;
  sessionId.value = "";
  connectedSession.value = null;
  sftpSessions.disconnected(closingSession);
  entries.value = [];
  selected.value = undefined;
  clearImagePreviews(editorTabs.value);
  editorTabs.value = [];
  activeEditorPath.value = "";
  await disconnectSftp(closingSession).catch(() => undefined);
}

/** Resolves true when there is no unsaved remote edit or the user agreed to discard them. */
async function confirmDiscardDirtyEditors(message: string, title: string) {
  if (!editorTabs.value.some((tab) => tab.dirty)) return true;
  try {
    await ElMessageBox.confirm(message, title, { type: "warning" });
    return true;
  } catch {
    return false;
  }
}

/** User-initiated disconnect: unsaved remote edits are only dropped after confirmation. */
async function requestDisconnect() {
  if (!sessionId.value) return true;
  if (!(await confirmDiscardDirtyEditors(t("sftp.unsavedDisconnectConfirm"), t("sftp.disconnect")))) {
    return false;
  }
  await disconnect();
  return true;
}

async function selectProfile(nextProfileId: string) {
  if (nextProfileId === profileId.value) return;
  // The select is controlled: cancelling keeps profileId (and the select) on the original value.
  if (!(await requestDisconnect())) return;
  profileId.value = nextProfileId;
}

/**
 * Lists `remotePath`. Resolves false when a newer listing or session superseded this one;
 * throws when the current listing fails, after putting `remotePath` back on the directory
 * that is still shown so mkdir/upload/drop keep targeting it.
 */
async function loadRemoteEntries() {
  if (!sessionId.value) return false;
  const sequence = ++remoteListSequence;
  const openedSession = sessionId.value;
  const path = remotePath.value;
  const selectedPath = selected.value?.path;
  const isCurrent = () => sequence === remoteListSequence && sessionId.value === openedSession;
  let nextEntries: Entry[];
  try {
    nextEntries = await listSftpDirectory(openedSession, path);
  } catch (error) {
    if (!isCurrent()) return false;
    if (remotePath.value === path) remotePath.value = listedRemotePath;
    throw error;
  }
  if (!isCurrent()) return false;
  entries.value = nextEntries;
  listedRemotePath = path;
  selected.value = selectedPath
    ? entries.value.find((item) => item.path === selectedPath)
    : undefined;
  return true;
}

async function listRemote() {
  try {
    return await loadRemoteEntries();
  } catch (error) {
    ElMessage.error(String(error));
    return false;
  }
}

async function openRemote(item: Entry) {
  selected.value = item;
  if (item.isDir) {
    remotePath.value = item.path;
    await listRemote();
    return;
  }

  const existing = editorTabs.value.find((tab) => tab.path === item.path);
  if (existing) {
    activeEditorPath.value = existing.path;
    return;
  }

  const tab: RemoteEditorTab = {
    path: item.path,
    name: item.name,
    content: "",
    dirty: false,
    loading: true,
    saving: false,
    isImage: isSftpImageFile(item.name),
  };
  editorTabs.value.push(tab);
  activeEditorPath.value = item.path;
  const editorTab = editorTabs.value.find((candidate) => candidate.path === item.path);
  if (!editorTab) return;
  const openedSession = sessionId.value;
  const generation = previewGeneration;
  const isCurrent = () => !disposed && sessionId.value === openedSession &&
    generation === previewGeneration && editorTabs.value.includes(editorTab);
  try {
    if (editorTab.isImage) {
      const imageUrl = await readSftpImagePreview(openedSession, item.path);
      if (isCurrent()) editorTab.imageUrl = imageUrl;
    } else {
      const content = await readSftpTextFile(openedSession, item.path);
      if (isCurrent()) editorTab.content = content;
    }
  } catch (error) {
    if (isCurrent()) editorTab.error = String(error);
  } finally {
    if (isCurrent()) editorTab.loading = false;
  }
}

function updateEditorContent(value: string) {
  if (!activeEditor.value) return;
  activeEditor.value.content = value;
  activeEditor.value.dirty = true;
}

async function save(tab = activeEditor.value) {
  if (!tab || tab.isImage || tab.saving) return false;
  const savedContent = tab.content;
  tab.saving = true;
  try {
    await writeSftpTextFile(sessionId.value, tab.path, savedContent);
  } catch (error) {
    ElMessage.error(String(error));
    return false;
  } finally {
    tab.saving = false;
  }
  // Edits typed while the write was in flight are not on the server yet; keep them marked unsaved.
  if (tab.content === savedContent) tab.dirty = false;
  ElMessage.success(t("common.saved"));
  return true;
}

async function closeEditor(path: string) {
  const tab = editorTabs.value.find((item) => item.path === path);
  if (tab?.dirty) {
    try {
      await ElMessageBox.confirm(
        t("sftp.unsavedFileConfirm").replace("{name}", tab.name),
        t("sftp.closeEditor"),
        { type: "warning" }
      );
    } catch {
      return;
    }
  }
  const index = editorTabs.value.findIndex((item) => item.path === path);
  if (index < 0) return;
  clearImagePreviews([editorTabs.value[index]], false);
  editorTabs.value.splice(index, 1);
  if (activeEditorPath.value === path) {
    activeEditorPath.value = editorTabs.value[Math.max(0, index - 1)]?.path ?? "";
  }
}

async function closeAllEditors() {
  if (!(await confirmDiscardDirtyEditors(t("sftp.unsavedFilesConfirm"), t("sftp.closeEditor")))) return;
  clearImagePreviews(editorTabs.value);
  editorTabs.value = [];
  activeEditorPath.value = "";
}

async function mkdir() {
  try {
    const result = await ElMessageBox.prompt(t("sftp.folderName"), t("sftp.newFolder"), {
      confirmButtonText: t("common.create"),
      cancelButtonText: t("common.cancel"),
    });
    const name = validateRemoteEntryName(result.value);
    if (!name) return;
    await createSftpDirectory(sessionId.value, remoteJoin(name));
    await listRemote();
    ElMessage.success(t("sftp.createdEntry").replace("{name}", name));
  } catch (error) {
    if (error === "cancel" || error === "close") return;
    ElMessage.error(String(error));
  }
}

async function createRemoteFile() {
  try {
    const result = await ElMessageBox.prompt(t("sftp.fileName"), t("sftp.newFile"), {
      confirmButtonText: t("common.create"),
      cancelButtonText: t("common.cancel"),
    });
    const name = validateRemoteEntryName(result.value);
    if (!name) return;
    await writeSftpTextFile(sessionId.value, remoteJoin(name), "");
    await listRemote();
    ElMessage.success(t("sftp.createdEntry").replace("{name}", name));
  } catch (error) {
    if (error === "cancel" || error === "close") return;
    ElMessage.error(String(error));
  }
}

async function openSelectedRemote() {
  if (selected.value) await openRemote(selected.value);
}

async function remove() {
  if (!selected.value) return;
  try {
    await ElMessageBox.confirm(
      t("sftp.deleteConfirm").replace("{name}", selected.value.name),
      t("common.delete"),
      {
        type: "warning",
        confirmButtonText: t("common.delete"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return; // User cancelled the confirm dialog.
  }
  try {
    await deleteSftpEntry(sessionId.value, selected.value.path, selected.value.isDir);
    selected.value = undefined;
    await listRemote();
  } catch (error) {
    ElMessage.error(String(error));
  }
}

async function rename() {
  if (!selected.value) return;
  renamingPath.value = selected.value.path;
  renameValue.value = selected.value.name;
  remoteFileList.value?.scrollToPath(selected.value.path);
  await nextTick();
  document.querySelector<HTMLInputElement>(".sftp-file-rename input")?.select();
}

function cancelRename() {
  renamingPath.value = "";
}

async function saveRename() {
  const item = selected.value;
  if (!item || item.path !== renamingPath.value) return;
  const name = renameValue.value.trim();
  const oldPath = item.path;
  renamingPath.value = "";
  if (name === item.name) return;
  if (!validateRemoteEntryName(name)) return;
  try {
    await renameSftpEntry(sessionId.value, oldPath, remoteJoin(name));
    await listRemote();
  } catch (error) {
    ElMessage.error(String(error));
  }
}

// 远端跨目录移动：SFTP 的 rename 支持任意目标路径，目标目录 + 原名即移动。
async function moveRemoteEntryTo(item: Entry, targetDirectory: string) {
  if (!sessionId.value) return;
  const normalized = targetDirectory.replace(/\/+$/, "") || "/";
  const newPath = joinRemote(normalized, item.name);
  if (newPath === item.path) return;
  if (item.isDir && (normalized === item.path || normalized.startsWith(`${item.path}/`))) {
    ElMessage.warning(t("sftp.moveIntoSelf"));
    return;
  }
  try {
    await renameSftpEntry(sessionId.value, item.path, newPath);
    if (selected.value?.path === item.path) selected.value = undefined;
    await listRemote();
  } catch (error) {
    ElMessage.error(String(error));
  }
}

async function moveRemote() {
  const item = selected.value;
  if (!item) return;
  try {
    const result = await ElMessageBox.prompt(t("sftp.moveTargetPath"), t("sftp.move"), {
      inputValue: remotePath.value,
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    const target = result.value.trim();
    if (!target) return;
    await moveRemoteEntryTo(item, target);
  } catch (error) {
    if (error === "cancel" || error === "close") return;
    ElMessage.error(String(error));
  }
}

async function collectLocalUploadDrafts(
  item: LocalEntry,
  targetDirectory: string
): Promise<SftpTransferDraft[]> {
  if (!sessionId.value || !localRoot.value) return [];
  const drafts: SftpTransferDraft[] = [];
  const queue: Array<{ entry: LocalEntry; remoteDirectory: string }> = [
    { entry: item, remoteDirectory: targetDirectory },
  ];
  while (queue.length > 0) {
    const current = queue.shift();
    if (!current) continue;
    if (!isSafeSftpEntryName(current.entry.name)) {
      throw new Error(t("sftp.invalidEntryName").replace("{name}", current.entry.name));
    }
    const remoteTarget = joinRemote(current.remoteDirectory, current.entry.name);
    if (!current.entry.isDir) {
      drafts.push({
        sessionId: sessionId.value,
        direction: "upload",
        operation: "upload-file",
        name: current.entry.name,
        localPath: localAbsolute(current.entry.path),
        remotePath: remoteTarget,
        conflictStrategy: conflictStrategy.value,
      });
      continue;
    }
    drafts.push({
      sessionId: sessionId.value,
      direction: "upload",
      operation: "create-remote-directory",
      name: current.entry.name,
      localPath: localAbsolute(current.entry.path),
      remotePath: remoteTarget,
      isDirectory: true,
    });
    const children = await listLocalFiles(localRoot.value, current.entry.path);
    queue.push(
      ...children.map((child) => ({
        entry: child,
        remoteDirectory: remoteTarget,
      }))
    );
  }
  return drafts;
}

async function enqueueLocalEntry(item: LocalEntry, targetDirectory = remotePath.value) {
  if (!sessionId.value) return;
  try {
    const drafts = await collectLocalUploadDrafts(item, targetDirectory);
    transferQueue.enqueue(drafts);
    transferPanelOpen.value = true;
  } catch (error) {
    ElMessage.error(String(error));
  }
}

function enqueueRemoteEntry(item: Entry, targetLocalDirectory = localPath.value) {
  if (!isSafeSftpEntryName(item.name)) {
    ElMessage.error(t("sftp.invalidEntryName").replace("{name}", item.name));
    return;
  }
  if (!sessionId.value || !localRoot.value) {
    ElMessage.warning(t("sftp.chooseLocalDownloadFolder"));
    return;
  }
  transferQueue.enqueue([
    {
      sessionId: sessionId.value,
      direction: "download",
      operation: "download-entry",
      name: item.name,
      localPath: localAbsolute(joinLocalRelative(targetLocalDirectory, item.name)),
      remotePath: item.path,
      isDirectory: item.isDir,
    },
  ]);
  transferPanelOpen.value = true;
}

function download() {
  if (selected.value) enqueueRemoteEntry(selected.value);
}

function upload() {
  if (selectedLocal.value) void enqueueLocalEntry(selectedLocal.value);
}

function onRemoteContextMenu(event: MouseEvent, item: Entry) {
  event.preventDefault();
  event.stopPropagation();
  selected.value = item;
  remoteContextMenu.value = { item, x: event.clientX, y: event.clientY };
}

function onRemotePaneContextMenu(event: MouseEvent) {
  event.preventDefault();
  selected.value = undefined;
  remoteContextMenu.value = { x: event.clientX, y: event.clientY };
}

function onLocalContextMenu(event: MouseEvent, item: LocalEntry) {
  event.preventDefault();
  selectedLocal.value = item;
  localContextMenu.value = { item, x: event.clientX, y: event.clientY };
}

function startLocalDrag(event: DragEvent, item: LocalEntry) {
  if (!event.dataTransfer || !localRoot.value) return;
  const payload: LocalDragPayload = { root: localRoot.value, entry: item };
  event.dataTransfer.effectAllowed = "copy";
  event.dataTransfer.setData(LOCAL_DRAG_TYPE, JSON.stringify(payload));
}

function startRemoteDrag(event: DragEvent, item: Entry) {
  if (!event.dataTransfer || !sessionId.value) return;
  const payload: RemoteDragPayload = { sessionId: sessionId.value, entry: item };
  event.dataTransfer.effectAllowed = "copy";
  event.dataTransfer.setData(REMOTE_DRAG_TYPE, JSON.stringify(payload));
}

function dragHasType(event: DragEvent, type: string) {
  return Array.from(event.dataTransfer?.types ?? []).includes(type);
}

function parseDragPayload<T>(event: DragEvent, type: string): T | undefined {
  const raw = event.dataTransfer?.getData(type);
  if (!raw) return undefined;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return undefined;
  }
}

function onRemoteDragOver(event: DragEvent) {
  if (!dragHasType(event, LOCAL_DRAG_TYPE) && !dragHasType(event, REMOTE_DRAG_TYPE)) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
  remoteDropActive.value = true;
}

function onLocalDragOver(event: DragEvent) {
  if (!dragHasType(event, REMOTE_DRAG_TYPE) && !dragHasType(event, LOCAL_DRAG_TYPE)) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
  localDropActive.value = true;
}

function leaveDropZone(event: DragEvent, side: "local" | "remote") {
  const current = event.currentTarget as HTMLElement | null;
  const related = event.relatedTarget as Node | null;
  if (current && related && current.contains(related)) return;
  if (side === "local") localDropActive.value = false;
  else remoteDropActive.value = false;
}

function dropOnRemote(event: DragEvent, targetDirectory = remotePath.value) {
  event.preventDefault();
  event.stopPropagation();
  remoteDropActive.value = false;
  const payload = parseDragPayload<LocalDragPayload>(event, LOCAL_DRAG_TYPE);
  if (payload) {
    if (payload.root !== localRoot.value) {
      ElMessage.error(t("sftp.invalidDragSource"));
      return;
    }
    void enqueueLocalEntry(payload.entry, targetDirectory);
    return;
  }
  // 远端条目拖入远端目录：同面板内移动。
  const remotePayload = parseDragPayload<RemoteDragPayload>(event, REMOTE_DRAG_TYPE);
  if (!remotePayload || remotePayload.sessionId !== sessionId.value) {
    ElMessage.error(t("sftp.invalidDragSource"));
    return;
  }
  void moveRemoteEntryTo(remotePayload.entry, targetDirectory);
}

function dropOnLocal(event: DragEvent, targetLocalDirectory = localPath.value) {
  event.preventDefault();
  event.stopPropagation();
  localDropActive.value = false;
  const payload = parseDragPayload<RemoteDragPayload>(event, REMOTE_DRAG_TYPE);
  if (payload) {
    if (payload.sessionId !== sessionId.value) {
      ElMessage.error(t("sftp.invalidDragSource"));
      return;
    }
    enqueueRemoteEntry(payload.entry, targetLocalDirectory);
    return;
  }
  // 本地条目拖入本地目录：同面板内移动。
  const localPayload = parseDragPayload<LocalDragPayload>(event, LOCAL_DRAG_TYPE);
  if (!localPayload || localPayload.root !== localRoot.value) {
    ElMessage.error(t("sftp.invalidDragSource"));
    return;
  }
  void moveLocalEntryTo(localPayload.entry, targetLocalDirectory);
}

function dropOnRemoteDirectory(event: DragEvent, item: Entry) {
  dropOnRemote(event, item.path);
}

function dropOnLocalDirectory(event: DragEvent, item: LocalEntry) {
  dropOnLocal(event, item.path);
}

function remoteUp() {
  remotePath.value = remotePath.value.replace(/\/[^/]+\/?$/, "") || "/";
  void listRemote();
}

async function chooseLocal() {
  const path = await chooseLocalSftpDirectory(t("common.openFolder"));
  if (!path) return;
  localRoot.value = path;
  localPath.value = "";
  await listLocal();
}

let localListSequence = 0;
let listedLocal = { root: "", path: "" };

/** Local counterpart of loadRemoteEntries: restores root/path to the shown directory and throws on failure. */
async function loadLocalEntries() {
  if (!localRoot.value) return false;
  const sequence = ++localListSequence;
  const root = localRoot.value;
  const path = localPath.value;
  let nextEntries: LocalEntry[];
  try {
    nextEntries = await listLocalFiles(root, path);
  } catch (error) {
    if (sequence !== localListSequence) return false;
    if (localRoot.value === root && localPath.value === path) {
      localRoot.value = listedLocal.root;
      localPath.value = listedLocal.path;
    }
    throw error;
  }
  if (sequence !== localListSequence) return false;
  localEntries.value = nextEntries;
  listedLocal = { root, path };
  return true;
}

async function listLocal() {
  try {
    return await loadLocalEntries();
  } catch (error) {
    ElMessage.error(String(error));
    return false;
  }
}

async function openLocal(item: LocalEntry) {
  selectedLocal.value = item;
  if (!item.isDir) return;
  localPath.value = item.path;
  await listLocal();
}

function localUp() {
  const parts = localPath.value.split("/").filter(Boolean);
  parts.pop();
  localPath.value = parts.join("/");
  void listLocal();
}

async function removeLocal() {
  const item = selectedLocal.value;
  if (!item || !localRoot.value) return;
  try {
    await ElMessageBox.confirm(item.name, t("common.delete"), { type: "warning" });
  } catch {
    return; // 用户取消确认。
  }
  try {
    await deleteLocalFileEntry(localRoot.value, item.path);
    selectedLocal.value = undefined;
    await listLocal();
  } catch (error) {
    ElMessage.error(String(error));
  }
}

async function renameLocal() {
  const item = selectedLocal.value;
  if (!item) return;
  renamingLocalPath.value = item.path;
  localRenameValue.value = item.name;
  localFileList.value?.scrollToPath(item.path);
  await nextTick();
  document.querySelector<HTMLInputElement>(".sftp-local-file-rename input")?.select();
}

function cancelLocalRename() {
  renamingLocalPath.value = "";
}

async function saveLocalRename() {
  const item = selectedLocal.value;
  if (!item || item.path !== renamingLocalPath.value || !localRoot.value) return;
  const name = localRenameValue.value.trim();
  renamingLocalPath.value = "";
  if (!name || name === item.name || /[/\\]/.test(name)) return;
  try {
    await renameLocalFileEntry(localRoot.value, item.path, name);
    await listLocal();
  } catch (error) {
    ElMessage.error(String(error));
  }
}

// 本地跨目录移动：目标目录为相对本地根目录的路径。
async function moveLocalEntryTo(item: LocalEntry, targetDirectory: string) {
  if (!localRoot.value) return;
  const destDir = targetDirectory.replace(/\/+$/, "");
  if (destDir === localPath.value) return;
  if (item.isDir && (destDir === item.path || destDir.startsWith(`${item.path}/`))) {
    ElMessage.warning(t("sftp.moveIntoSelf"));
    return;
  }
  try {
    await moveLocalFileEntry(localRoot.value, item.path, destDir);
    if (selectedLocal.value?.path === item.path) selectedLocal.value = undefined;
    await listLocal();
  } catch (error) {
    ElMessage.error(String(error));
  }
}

async function moveLocal() {
  const item = selectedLocal.value;
  if (!item) return;
  try {
    const result = await ElMessageBox.prompt(t("sftp.moveTargetPath"), t("sftp.move"), {
      inputValue: localPath.value,
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    const target = result.value.trim();
    if (!target) return;
    await moveLocalEntryTo(item, target);
  } catch (error) {
    if (error === "cancel" || error === "close") return;
    ElMessage.error(String(error));
  }
}

function getLocalAbsPath() {
  if (!localRoot.value) return "";
  if (!localPath.value) return localRoot.value;
  return `${localRoot.value.replace(/\/$/, "")}/${localPath.value}`;
}

watchEffect(() => {
  if (!localInputEditing.value) localInputText.value = getLocalAbsPath();
});

async function navigateLocalInput() {
  const target = localInputText.value.trim();
  localInputEditing.value = false;
  if (!target) {
    localInputText.value = getLocalAbsPath();
    return;
  }
  const prevRoot = localRoot.value;
  const prevPath = localPath.value;
  localRoot.value = target;
  localPath.value = "";
  try {
    await loadLocalEntries();
  } catch (err) {
    localRoot.value = prevRoot;
    localPath.value = prevPath;
    ElMessage.error(String(err));
  }
  localInputText.value = getLocalAbsPath();
}

function onLocalInputFocus() {
  localInputEditing.value = true;
  localInputText.value = getLocalAbsPath();
}

function onLocalInputBlur() {
  localInputEditing.value = false;
  localInputText.value = getLocalAbsPath();
}

// 默认将本地目录设为当前用户家目录，失败时保持为空由用户手动选择。
async function initDefaultLocalRoot() {
  if (localRoot.value || !isDesktopRuntime()) return;
  try {
    const home = await getUserHomeDirectory();
    if (!home) return;
    localRoot.value = home;
    localPath.value = "";
    await loadLocalEntries();
  } catch {
    // 忽略：家目录不可用时不影响其余功能。
  }
}

async function applyProfileFromRoute(value: unknown) {
  if (typeof value !== "string" || !ssh.findProfile(value)) return;
  if (sessionId.value && value === profileId.value) return;
  if (sessionId.value && !(await requestDisconnect())) return;
  profileId.value = value;
  await nextTick();
  const selectedProfile = profile.value;
  if (!selectedProfile || !isDesktopRuntime()) return;
  const credentials = ssh.credentialsFor(selectedProfile.id);
  const canConnect = requiresFtpPassword.value
    ? Boolean(credentials?.ftpPassword) || Boolean(ftpPassword.value)
    : selectedProfile.authType !== "password" ||
      Boolean(credentials?.password) ||
      Boolean(sshPassword.value);
  if (canConnect) await connect();
}

onMounted(async () => {
  if (!ssh.hydrated) await ssh.hydrate();
  await applyProfileFromRoute(route.query.profile);
  void initDefaultLocalRoot();
});

watch(
  () => route.query.profile,
  (profileIdFromRoute) => void applyProfileFromRoute(profileIdFromRoute)
);

watch(profileId, (nextProfileId, previousProfileId) => {
  if (nextProfileId === previousProfileId) return;
  sshPassword.value = "";
  ftpPassword.value = "";
});

// Route leaves only deactivate this KeepAlive'd view, so edits survive; unmount (cache eviction,
// workspace close) cannot be vetoed, so at least tell the user which unsaved edits were dropped.
onBeforeUnmount(() => {
  disposed = true;
  remoteContextMenu.value = null;
  const discarded = editorTabs.value.filter((tab) => tab.dirty).map((tab) => tab.name);
  if (discarded.length) {
    ElMessage.warning(t("sftp.unsavedDiscardedOnClose").replace("{names}", discarded.join(", ")));
  }
  clearImagePreviews(editorTabs.value);
  transferQueue.dispose();
  if (refreshTimer) clearTimeout(refreshTimer);
  if (sessionId.value) void disconnect();
});
</script>

<template>
  <section class="sftp-workspace">
    <header class="sftp-header">
      <div class="sftp-header__identity">
        <span class="sftp-header__icon"
          ><el-icon><FolderOpened /></el-icon
        ></span>
        <div>
          <strong>{{ protocolLabel }}</strong>
          <small v-if="profile">{{ profile.fileTransfer?.username || profile.username }}@{{ profile.fileTransfer?.host || profile.host }}:{{ profile.fileTransfer?.port || profile.port }}</small>
          <small v-else>{{ t("sftp.selectConnection") }}</small>
        </div>
      </div>
      <div class="sftp-header__connect">
        <div class="sftp-connect-fields">
          <el-select
            filterable
            :model-value="profileId"
            size="small"
            class="sftp-profile-select"
            :placeholder="t('workspace.connections')"
            @update:model-value="selectProfile"
          >
            <template #prefix>
              <el-icon><FolderOpened /></el-icon>
            </template>
            <el-option
              v-for="item in ssh.sortedProfiles"
              :key="item.id"
              :label="item.name"
              :value="item.id"
            />
          </el-select>
          <span v-if="!isReady" class="sftp-connect-fields__divider" aria-hidden="true" />
          <el-input
            v-if="!isReady && requiresFtpPassword"
            v-model="ftpPassword"
            size="small"
            type="password"
            show-password
            :prefix-icon="Lock"
            class="sftp-password"
            :placeholder="t('ssh.fileTransfer.password')"
            :aria-label="t('ssh.fileTransfer.password')"
            autocomplete="current-password"
          />
          <el-input
            v-else-if="!isReady"
            v-model="sshPassword"
            size="small"
            type="password"
            show-password
            :prefix-icon="Lock"
            class="sftp-password"
            :placeholder="t('ssh.password')"
            :aria-label="t('ssh.password')"
            autocomplete="current-password"
          />
        </div>
        <el-button
          v-if="!isReady"
          type="primary"
          size="small"
          class="sftp-connect-button"
          :icon="Connection"
          :loading="loading"
          @click="connect"
          >{{ t("sftp.connectProtocol").replace("{protocol}", protocolLabel) }}</el-button
        >
        <el-button v-else size="small" class="sftp-disconnect-button" @click="requestDisconnect">{{
          t("sftp.disconnect")
        }}</el-button>
      </div>
    </header>

    <div v-if="loading" class="sftp-state">
      <el-icon class="is-loading" :size="28"><Refresh /></el-icon>
      <span>{{ t("sftp.connectingProtocol").replace("{protocol}", protocolLabel) }}</span>
    </div>
    <div v-else-if="!isReady" class="sftp-state">
      <el-icon :size="34"><FolderOpened /></el-icon>
      <strong>{{ t("sftp.selectConnection") }}</strong>
      <span>{{ t("sftp.connectionHint") }}</span>
    </div>

    <div v-else class="sftp-content" :class="{ 'sftp-content--editing': editorTabs.length > 0 }">
      <section
        v-if="editorTabs.length === 0"
        class="sftp-file-pane sftp-file-pane--local"
        :class="{ 'is-drop-active': localDropActive }"
        @dragover="onLocalDragOver"
        @dragleave="leaveDropZone($event, 'local')"
        @drop="dropOnLocal"
      >
        <div class="sftp-pane-title">
          <strong>{{ t("sftp.local") }}</strong>
          <div class="sftp-pane-title__actions">
            <el-button text native-type="button" @click="chooseLocal">{{
              t("common.openFolder")
            }}</el-button>
            <el-select
              v-model="conflictStrategy"
              size="small"
              class="sftp-conflict-select"
              :title="t('sftp.conflictStrategy')"
            >
              <el-option value="overwrite" :label="t('sftp.conflict.overwrite')" />
              <el-option value="skip" :label="t('sftp.conflict.skip')" />
              <el-option value="auto-rename" :label="t('sftp.conflict.autoRename')" />
            </el-select>
          </div>
        </div>
        <div class="sftp-pathbar">
          <el-button text :icon="Back" @click="localUp" />
          <el-input
            v-model="localInputText"
            size="small"
            @keyup.enter="navigateLocalInput"
            @keyup.escape="onLocalInputBlur"
            @focus="onLocalInputFocus"
            @blur="onLocalInputBlur"
          />
          <el-button text :icon="Refresh" @click="listLocal" />
          <el-button
            text
            :icon="Upload"
            :disabled="!selectedLocal"
            :aria-label="t('sftp.upload')"
            :title="t('sftp.upload')"
            @click="upload"
          />
        </div>
        <SftpFileList
          ref="localFileList"
          v-model:rename-value="localRenameValue"
          :entries="localEntries"
          :selected-path="selectedLocal?.path"
          :renaming-path="renamingLocalPath"
          rename-input-class="sftp-local-file-rename"
          :describe="describeLocalEntry"
          @select="selectedLocal = $event"
          @open="openLocal"
          @row-dragstart="startLocalDrag"
          @directory-dragover="onLocalDragOver"
          @directory-drop="dropOnLocalDirectory"
          @row-contextmenu="onLocalContextMenu"
          @save-rename="saveLocalRename"
          @cancel-rename="cancelLocalRename"
        >
          <div v-if="!localRoot" class="sftp-pane-empty">
            {{ t("sftp.chooseLocalUploadFolder") }}
          </div>
          <div v-else-if="localEntries.length === 0" class="sftp-pane-empty">
            {{ t("sftp.emptyDirectory") }}
          </div>
          <div v-if="localDropActive" class="sftp-drop-overlay">
            <el-icon><Download /></el-icon>
            <span>{{ t("sftp.dropToDownload") }}</span>
          </div>
        </SftpFileList>
      </section>

      <section
        class="sftp-file-pane sftp-file-pane--remote"
        :class="{ 'is-drop-active': remoteDropActive }"
        @dragover="onRemoteDragOver"
        @dragleave="leaveDropZone($event, 'remote')"
        @drop="dropOnRemote"
        @contextmenu.prevent="onRemotePaneContextMenu"
      >
        <div class="sftp-pane-title">
          <strong
            >{{ t("sftp.remote") }} <small>({{ profile?.host }})</small></strong
          >
          <span>{{ t("sftp.itemCount").replace("{count}", String(entries.length)) }}</span>
        </div>
        <div class="sftp-pathbar">
          <el-button text :icon="Back" @click="remoteUp" />
          <el-input v-model="remotePath" size="small" @keyup.enter="listRemote" />
          <el-button text :icon="Refresh" @click="listRemote" />
          <el-button text :icon="FolderAdd" :title="t('sftp.newFolder')" @click="mkdir" />
          <el-button text :icon="DocumentAdd" :title="t('sftp.newFile')" @click="createRemoteFile" />
          <el-button
            v-if="editorTabs.length === 0"
            text
            :icon="Download"
            :disabled="!selected"
            @click="download"
          />
          <el-button text :icon="EditPen" :disabled="!selected" @click="rename" />
          <el-button text :icon="Delete" :disabled="!selected" @click="remove" />
        </div>
        <SftpFileList
          ref="remoteFileList"
          v-model:rename-value="renameValue"
          :entries="entries"
          :selected-path="selected?.path"
          :renaming-path="renamingPath"
          :describe="describeRemoteEntry"
          @select="selected = $event"
          @open="openRemote"
          @row-dragstart="startRemoteDrag"
          @directory-dragover="onRemoteDragOver"
          @directory-drop="dropOnRemoteDirectory"
          @row-contextmenu="onRemoteContextMenu"
          @save-rename="saveRename"
          @cancel-rename="cancelRename"
        >
          <div v-if="entries.length === 0" class="sftp-pane-empty">
            {{ t("sftp.emptyDirectory") }}
          </div>
          <div v-if="remoteDropActive" class="sftp-drop-overlay">
            <el-icon><Upload /></el-icon>
            <span>{{ t("sftp.dropToUpload") }}</span>
          </div>
        </SftpFileList>
      </section>

      <section v-if="editorTabs.length > 0" class="sftp-editor-pane">
        <el-scrollbar class="sftp-editor-tabs">
          <div class="sftp-editor-tabs__content">
            <div
              v-for="tab in editorTabs"
              :key="tab.path"
              class="sftp-editor-tab"
              :class="{ 'is-active': activeEditorPath === tab.path }"
            >
              <button type="button" class="sftp-editor-tab__select" @click="activeEditorPath = tab.path">
                <span>{{ tab.name }}{{ tab.dirty ? " •" : "" }}</span>
              </button>
              <el-button
                text
                circle
                native-type="button"
                class="sftp-editor-tab__close"
                :aria-label="t('terminal.closePane')"
                :title="t('terminal.closePane')"
                @click="closeEditor(tab.path)"
              >
                <el-icon><Close /></el-icon>
              </el-button>
            </div>
            <div class="sftp-editor-actions">
              <el-button
                v-if="!activeEditor?.isImage"
                text
                native-type="button"
                :disabled="!activeEditor?.dirty || activeEditor.saving"
                :loading="activeEditor?.saving"
                @click="save()"
              >
                {{ t("sftp.saveRemoteFile") }}
              </el-button>
              <el-button text native-type="button" @click="closeAllEditors">
                {{ t("sftp.closeAllEditors") }}
              </el-button>
            </div>
          </div>
        </el-scrollbar>
        <div v-if="activeEditor?.loading" class="sftp-state">
          <el-icon class="is-loading"><Refresh /></el-icon>
        </div>
        <div v-else-if="activeEditor?.error" class="sftp-state sftp-state--error">
          {{ activeEditor.error }}
        </div>
        <el-image
          v-else-if="activeEditor?.imageUrl"
          class="sftp-editor-pane__image-preview"
          :src="activeEditor.imageUrl"
          :alt="activeEditor.name"
          fit="contain"
          :preview-src-list="[activeEditor.imageUrl]"
          preview-teleported
          hide-on-click-modal
          @error="activeEditor.error = t('sftp.imageDecodeFailed')"
        />
        <MonacoTextEditor
          v-else-if="activeEditor && !activeEditor.isImage"
          kind="sftp"
          :model-value="activeEditor.content"
          :path="activeEditor.path"
          :preferences="{
            fontSize: 13,
            tabSize: 2,
            wordWrap: 'on',
            minimap: false,
            lineNumbers: 'on',
          }"
          @update:model-value="updateEditorContent"
          @save="save()"
        />
      </section>
    </div>

    <SftpTransferPanel v-if="isReady && transferCount" v-model:open="transferPanelOpen" :queue="transferQueue" />
  </section>

  <AppContextMenu
    v-if="remoteContextMenu"
    :open="true"
    :x="remoteContextMenu.x"
    :y="remoteContextMenu.y"
    @close="remoteContextMenu = null"
  >
    <el-dropdown-item :icon="Refresh" @click="listRemote(); remoteContextMenu = null">
      {{ t("common.refresh") }}
    </el-dropdown-item>
    <el-dropdown-item :icon="FolderAdd" @click="mkdir(); remoteContextMenu = null">
      {{ t("sftp.newFolder") }}
    </el-dropdown-item>
    <el-dropdown-item :icon="DocumentAdd" @click="createRemoteFile(); remoteContextMenu = null">
      {{ t("sftp.newFile") }}
    </el-dropdown-item>
    <template v-if="remoteContextMenu.item">
      <el-dropdown-item :icon="remoteContextMenu.item.isDir ? FolderOpened : View" @click="openSelectedRemote(); remoteContextMenu = null">
        {{ remoteContextMenu.item.isDir ? t("sftp.openFolder") : t("sftp.preview") }}
      </el-dropdown-item>
      <el-dropdown-item :icon="Download" @click="download(); remoteContextMenu = null">
        {{ t("sftp.download") }}
      </el-dropdown-item>
      <el-dropdown-item :icon="EditPen" @click="rename(); remoteContextMenu = null">
        {{ t("sftp.rename") }}
      </el-dropdown-item>
      <el-dropdown-item :icon="Position" @click="moveRemote(); remoteContextMenu = null">
        {{ t("sftp.move") }}
      </el-dropdown-item>
      <el-dropdown-item class="is-danger" :icon="Delete" @click="remove(); remoteContextMenu = null">
        {{ t("common.delete") }}
      </el-dropdown-item>
      <PluginMenuItems
        menu="sftp/file/context"
        :host="remotePluginMenuHost"
        :context="remotePluginMenuContext"
        @run="remoteContextMenu = null"
      />
      <PluginServiceMenuItems :target="remoteServiceTarget" @run="remoteContextMenu = null" />
    </template>
  </AppContextMenu>

  <AppContextMenu
    v-if="localContextMenu"
    :open="true"
    :x="localContextMenu.x"
    :y="localContextMenu.y"
    @close="localContextMenu = null"
  >
    <el-dropdown-item :icon="Upload" @click="upload(); localContextMenu = null">
      {{ t("sftp.upload") }}
    </el-dropdown-item>
    <el-dropdown-item :icon="EditPen" @click="renameLocal(); localContextMenu = null">
      {{ t("sftp.rename") }}
    </el-dropdown-item>
    <el-dropdown-item :icon="Position" @click="moveLocal(); localContextMenu = null">
      {{ t("sftp.move") }}
    </el-dropdown-item>
    <el-dropdown-item class="is-danger" :icon="Delete" @click="removeLocal(); localContextMenu = null">
      {{ t("common.delete") }}
    </el-dropdown-item>
  </AppContextMenu>
</template>
