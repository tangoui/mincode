<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onDeactivated, ref, shallowRef, watch } from "vue";
import { ElMessage, ElMessageBox, type ScrollbarInstance } from "element-plus";
import MonacoTextEditor from "@/components/ui/MonacoTextEditor.vue";
import SftpTransferPanel from "@/components/sftp/SftpTransferPanel.vue";
import { useSftpTransferQueue } from "@/composables/useSftpTransferQueue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import PluginServiceMenuItems from "@/components/plugins/PluginServiceMenuItems.vue";
import {
  Back,
  Close,
  CopyDocument,
  Delete,
  Document,
  Download,
  EditPen,
  Folder,
  FolderAdd,
  FolderOpened,
  DocumentAdd,
  FullScreen,
  Refresh,
  ScaleToOriginal,
  Upload,
  View,
} from "@element-plus/icons-vue";
import { useVirtualRows } from "@/components/sftp/use-virtual-rows";
import { usePluginMenuTarget } from "@/composables/use-plugin-menu-target";
import {
  sftpFileMenuTarget,
  sftpFileServiceTarget,
  sftpMenuConnection,
  type SftpMenuConnection,
} from "@/domain/sftp/menu-target";
import { isSafeSftpEntryName } from "@/domain/sftp/transfer-queue";
import type { SshConnectionProfile, SshRuntimeCredentials } from "@/domain/ssh/types";
import { useVueI18n } from "@/i18n/messages";
import {
  cancelSftpTransfer,
  chooseLocalSftpDirectory,
  connectSftp,
  createSftpDirectory,
  deleteSftpEntry,
  disconnectSftp,
  listSftpDirectory,
  isSftpImageFile,
  readSftpImagePreview,
  readSftpTextFile,
  renameSftpEntry,
  resolveSftpInitialPath,
  runSftpTransfer,
  writeSftpTextFile,
  type SftpEntry,
} from "@/services/sftp-service";
import { chooseNativeFile, chooseNativeSaveFile, copyText, isDesktopRuntime } from "@/services/desktop-bridge";
import { useSftpSessionsStore } from "@/stores/sftp-sessions";

const props = defineProps<{
  visible: boolean;
  maximized?: boolean;
  sshSessionId: string;
  profile: SshConnectionProfile;
  credentials?: SshRuntimeCredentials;
}>();

const emit = defineEmits<{ close: []; toggleMaximize: [] }>();
const { t } = useVueI18n();
const sftpSessions = useSftpSessionsStore();
const protocol = computed(() => props.profile.fileTransfer?.protocol ?? "sftp");
const protocolLabel = computed(() => {
  if (protocol.value === "ftp") return "FTP";
  if (protocol.value === "ftps-explicit") return t("ssh.fileTransfer.protocol.ftpsExplicit");
  return "SFTP";
});
const remoteTarget = computed(() => {
  const transfer = props.profile.fileTransfer;
  return transfer && transfer.protocol !== "sftp"
    ? `${transfer.username}@${transfer.host}`
    : `${props.profile.username}@${props.profile.host}`;
});
const sessionId = computed(() => `${props.sshSessionId}-files-${protocol.value}`);
const connected = ref(false);
const loading = ref(false);
const connectStarted = ref(false);
const error = ref("");
const remotePath = ref("/");
const remoteHome = ref("/");
const entries = ref<SftpEntry[]>([]);
const selected = ref<SftpEntry>();
const contextMenu = ref<{ item?: SftpEntry; x: number; y: number } | null>(null);
// The session that is open and the endpoint it was opened with: row menus describe
// this connection, never a profile prop edited after connecting.
const connectedSession = shallowRef<{ id: string; connection: SftpMenuConnection } | null>(null);
function openRowMenuTarget() {
  const item = contextMenu.value?.item;
  const session = connectedSession.value;
  return item && session && session.id === sessionId.value ? { item, session } : null;
}
// Plugin items (contributes.menus sftp/file/context) of the open row menu; the blank-area menu has no target.
const { host: pluginMenuHost, context: pluginMenuContext } = usePluginMenuTarget("sftp/file/context", () => {
  const open = openRowMenuTarget();
  return open ? sftpFileMenuTarget(open.session.connection, open.item) : null;
});
// Plugin services (surface sftpFile) on a file row: read through the panel's open session.
function serviceTarget() {
  const open = openRowMenuTarget();
  return open ? sftpFileServiceTarget(open.session.id, open.session.connection, open.item) : null;
}
// A row menu belongs to one session's listing: never pair its row with another endpoint.
watch([sessionId, () => props.profile.id], () => {
  contextMenu.value = null;
});
// Inside a KeepAlive'd workspace, leaving it must not leave a row menu (and its target) open.
onDeactivated(() => {
  contextMenu.value = null;
});
const editor = ref<{
  path: string;
  name: string;
  content: string;
  dirty: boolean;
  loading: boolean;
  saving: boolean;
  error?: string;
  imageUrl?: string;
  isImage: boolean;
}>();
let disposed = false;
let previewRequest = 0;
const listScroller = ref<ScrollbarInstance>();
const {
  rows: visibleEntries,
  paddingTop: listPaddingTop,
  paddingBottom: listPaddingBottom,
  onScroll: onListScroll,
  scrollToIndex: scrollListToIndex,
} = useVirtualRows({
  items: () => entries.value,
  scroller: listScroller,
  rowSelector: ".ssh-sftp-panel__row",
  estimatedRowHeight: 28,
});

function clearImagePreview() {

  if (editor.value) editor.value.imageUrl = undefined;
  previewRequest += 1;
}

function remoteJoin(name: string) {
  const base = remotePath.value.replace(/\/$/, "");
  return base ? `${base}/${name}` : `/${name}`;
}


function validateRemoteEntryName(value: string) {
  const name = value.trim();
  if (!isSafeSftpEntryName(name)) {
    ElMessage.warning(t("sftp.invalidNewEntryName"));
    return "";
  }
  return name;
}

let listSequence = 0;

async function listRemote() {
  if (!connected.value) return;
  const sequence = ++listSequence;
  loading.value = true;
  error.value = "";
  try {
    const nextEntries = await listSftpDirectory(sessionId.value, remotePath.value);
    if (sequence !== listSequence) return;
    entries.value = nextEntries;
    selected.value = undefined;
  } catch (cause) {
    if (sequence === listSequence) error.value = String(cause);
  } finally {
    if (sequence === listSequence) loading.value = false;
  }
}

async function connect() {
  if (connected.value || connectStarted.value || !props.visible || disposed) return;
  if (!isDesktopRuntime()) {
    error.value = t("common.desktopRequired");
    return;
  }
  const openedSessionId = sessionId.value;
  const openedConnection = sftpMenuConnection(props.profile);
  const sequence = ++listSequence;
  connectStarted.value = true;
  loading.value = true;
  error.value = "";
  try {
    const home = await connectSftp(openedSessionId, props.profile, props.credentials);
    remoteHome.value = home || "/";
    remotePath.value = resolveSftpInitialPath(remoteHome.value, props.profile.defaultPath);
    if (disposed) {
      await disconnectSftp(openedSessionId).catch(() => undefined);
      return;
    }
    connected.value = true;
    connectedSession.value = { id: openedSessionId, connection: openedConnection };
    sftpSessions.connected({
      id: openedSessionId,
      name: `${props.profile.name.trim()} ${protocolLabel.value}`,
      profileId: props.profile.id,
      protocol: protocol.value,
    });
    const initialEntries = await listSftpDirectory(openedSessionId, remotePath.value);
    if (sequence === listSequence) entries.value = initialEntries;
  } catch (cause) {
    connected.value = false;
    connectedSession.value = null;
    clearImagePreview();
    editor.value = undefined;
    sftpSessions.disconnected(openedSessionId);
    await disconnectSftp(openedSessionId).catch(() => undefined);
    if (!disposed) error.value = String(cause);
  } finally {
    connectStarted.value = false;
    if (sequence === listSequence) loading.value = false;
  }
}

async function retry() {
  if (connected.value) await listRemote();
  else await connect();
}

function normalizeRemotePath(path: string) {
  const trimmed = path.trim();
  const expanded = trimmed === "~"
    ? remoteHome.value
    : trimmed.startsWith("~/")
      ? `${remoteHome.value.replace(/\/$/, "")}/${trimmed.slice(2)}`
      : trimmed;
  const normalized = expanded.replace(/\/{2,}/g, "/");
  return normalized.length > 1 ? normalized.replace(/\/$/, "") : normalized;
}

async function waitForConnection() {
  if (!connected.value) await connect();
  if (connectStarted.value) {
    await new Promise<void>((resolve) => {
      const startedAt = Date.now();
      const timer = window.setInterval(() => {
        if (!connectStarted.value || disposed || Date.now() - startedAt >= 15_000) {
          window.clearInterval(timer);
          resolve();
        }
      }, 50);
    });
  }
  if (!connected.value) throw new Error(error.value || t("terminal.context.sftpRevealFailed"));
}

async function revealPath(path: string) {
  const requested = path.trim();
  if (!(requested.startsWith("/") || requested === "~" || requested.startsWith("~/"))) {
    throw new Error(t("terminal.context.sftpRelativePath"));
  }
  await waitForConnection();
  const target = normalizeRemotePath(requested);
  const sequence = ++listSequence;
  loading.value = true;
  error.value = "";
  try {
    try {
      const targetEntries = await listSftpDirectory(sessionId.value, target);
      if (sequence !== listSequence) return;
      remotePath.value = target;
      entries.value = targetEntries;
      selected.value = undefined;
      return;
    } catch {
      const separator = target.lastIndexOf("/");
      const parent = separator <= 0 ? "/" : target.slice(0, separator);
      const parentEntries = await listSftpDirectory(sessionId.value, parent);
      if (sequence !== listSequence) return;
      const match = parentEntries.find((item) => item.path === target || item.name === target.slice(separator + 1));
      if (!match) throw new Error(t("terminal.context.sftpRevealFailed"));
      remotePath.value = parent;
      entries.value = parentEntries;
      selected.value = match;
      void nextTick(() => scrollListToIndex(parentEntries.indexOf(match)));
    }
  } finally {
    if (sequence === listSequence) loading.value = false;
  }
}

defineExpose({ revealPath });

async function openEntry(item: SftpEntry) {
  selected.value = item;
  clearImagePreview();
  if (item.isDir) {
    remotePath.value = item.path;
    await listRemote();
    return;
  }

  const openedSession = sessionId.value;
  const request = previewRequest;
  editor.value = {
    path: item.path,
    name: item.name,
    content: "",
    dirty: false,
    loading: true,
    saving: false,
    isImage: isSftpImageFile(item.name),
  };
  try {
    if (editor.value.isImage) {
      const imageUrl = await readSftpImagePreview(openedSession, item.path);
      if (!disposed && request === previewRequest && sessionId.value === openedSession && editor.value?.path === item.path) {
        editor.value.imageUrl = imageUrl;
      }
    } else {
      const content = await readSftpTextFile(openedSession, item.path);
      if (!disposed && request === previewRequest && sessionId.value === openedSession && editor.value?.path === item.path) editor.value.content = content;
    }
  } catch (cause) {
    if (!disposed && request === previewRequest && sessionId.value === openedSession && editor.value?.path === item.path) editor.value.error = String(cause);
  } finally {
    if (!disposed && request === previewRequest && sessionId.value === openedSession && editor.value?.path === item.path) editor.value.loading = false;
  }
}

function updateEditorContent(content: string) {
  if (!editor.value) return;
  editor.value.content = content;
  editor.value.dirty = true;
}

async function saveEditor() {
  const target = editor.value;
  if (!target || target.isImage || target.loading || target.error || target.saving) return;
  const savedContent = target.content;
  target.saving = true;
  try {
    await writeSftpTextFile(sessionId.value, target.path, savedContent);
    // Edits typed while the write was in flight are not on the server yet; keep them marked unsaved.
    if (target.content === savedContent) target.dirty = false;
    ElMessage.success(t("common.saved"));
  } catch (cause) {
    ElMessage.error(String(cause));
  } finally {
    target.saving = false;
  }
}

async function closeEditor() {
  if (editor.value?.dirty) {
    try {
      await ElMessageBox.confirm(t("sftp.unsavedConfirm"), t("sftp.closeEditor"), {
        type: "warning",
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
      });
    } catch {
      return;
    }
  }
  clearImagePreview();
  editor.value = undefined;
}

function goUp() {
  remotePath.value = remotePath.value.replace(/\/[^/]+\/?$/, "") || "/";
  void listRemote();
}

async function createFile() {
  try {
    const result = await ElMessageBox.prompt(t("sftp.fileName"), t("sftp.newFile"), {
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    const name = validateRemoteEntryName(result.value);
    if (!name) return;
    await writeSftpTextFile(sessionId.value, remoteJoin(name), "");
    await listRemote();
  } catch (cause) {
    if (cause === "cancel" || cause === "close") return;
    ElMessage.error(String(cause));
  }
}

async function createDirectory() {
  try {
    const result = await ElMessageBox.prompt(t("sftp.folderName"), t("sftp.newFolder"), {
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    const name = validateRemoteEntryName(result.value);
    if (!name) return;
    await createSftpDirectory(sessionId.value, remoteJoin(name));
    await listRemote();
  } catch (cause) {
    if (cause === "cancel" || cause === "close") return;
    ElMessage.error(String(cause));
  }
}

async function renameSelected() {
  if (!selected.value) return;
  try {
    const result = await ElMessageBox.prompt(t("sftp.fileName"), t("sftp.rename"), {
      inputValue: selected.value.name,
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    const name = validateRemoteEntryName(result.value);
    if (!name || name === selected.value.name) return;
    await renameSftpEntry(sessionId.value, selected.value.path, remoteJoin(name));
    await listRemote();
  } catch (cause) {
    if (cause === "cancel" || cause === "close") return;
    ElMessage.error(String(cause));
  }
}

async function removeSelected() {
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
    return;
  }
  try {
    await deleteSftpEntry(sessionId.value, selected.value.path, selected.value.isDir);
    selected.value = undefined;
    await listRemote();
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function downloadSelected() {
  // Selection can change while a native dialog is open. Keep the chosen
  // source and session fixed, just like the upload destination.
  const entry = selected.value;
  const targetSession = connectedSession.value?.id;
  if (!entry || !targetSession || !connected.value) return;
  if (!isSafeSftpEntryName(entry.name)) {
    ElMessage.warning(t("sftp.invalidEntryName").replace("{name}", entry.name));
    return;
  }
  try {
    let localPath: string | null;
    if (entry.isDir) {
      const parent = await chooseLocalSftpDirectory(t("sftp.chooseLocalDownloadFolder"));
      localPath = parent ? `${parent.replace(/[\\/]$/, "")}/${entry.name}` : null;
    } else {
      localPath = await chooseNativeSaveFile(t("sftp.download"), entry.name);
    }
    if (!localPath || disposed || !props.visible || !connected.value
      || connectedSession.value?.id !== targetSession || sessionId.value !== targetSession) return;
    transferPanelOpen.value = true;
    transferQueue.enqueue([{
      sessionId: targetSession,
      direction: "download",
      operation: "download-entry",
      name: entry.name,
      localPath,
      remotePath: entry.path,
      isDirectory: entry.isDir,
    }]);
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

const transferPanelOpen = ref(false);
const transferQueue = useSftpTransferQueue({
  async execute(item, reportProgress) {
    if (!connected.value || connectedSession.value?.id !== item.sessionId || sessionId.value !== item.sessionId) {
      throw new Error(t("sftp.transferSessionClosed"));
    }
    if (!item.backendJobId) throw new Error(t("sftp.transferJobUnavailable"));
    const snapshot = await runSftpTransfer({
      jobId: item.backendJobId,
      sessionId: item.sessionId,
      direction: item.direction,
      localPath: item.localPath,
      remotePath: item.remotePath,
      // Always start afresh: same-sized remote files or stale local .part
      // files are not proof of matching content and must not be resumed.
      resume: false,
      conflictStrategy: "overwrite",
    }, (progress) => reportProgress({
      bytesTransferred: progress.bytesTransferred,
      totalBytes: progress.totalBytes,
      speedBytesPerSecond: progress.speedBytesPerSecond,
      resumedFrom: progress.resumedFrom,
    }));
    return {
      status: snapshot.status === "completed" ? "completed" as const
        : snapshot.status === "cancelled" ? "cancelled" as const : "failed" as const,
      error: snapshot.error,
    };
  },
  async cancel(item) {
    if (item.backendJobId) await cancelSftpTransfer(item.backendJobId, item.sessionId);
  },
  async onSettled(item) {
    if (disposed || item.sessionId !== connectedSession.value?.id) return;
    if (item.status === "completed") {
      if (item.direction === "upload") {
        await listRemote();
        ElMessage.success(t("sftp.uploaded"));
      } else {
        ElMessage.success(t("sftp.downloadedTo").replace("{path}", item.localPath));
      }
    } else if (item.status === "failed") {
      ElMessage.error(item.error || t("sftp.transferStatus.failed"));
    }
  },
});
const transferCount = computed(() => transferQueue.items.value.length);

async function upload() {
  // Capture the destination before opening the native picker, so navigation
  // while it is open cannot silently redirect the chosen file to another path.
  const targetSession = connectedSession.value?.id;
  const targetPath = remotePath.value;
  try {
    const localPath = await chooseNativeFile(t("sftp.chooseUploadFile"));
    if (!localPath || disposed || !props.visible || !connected.value || !targetSession
      || connectedSession.value?.id !== targetSession || sessionId.value !== targetSession) return;
    const name = localPath.split(/[\\/]/).pop();
    if (!name) return;
    transferPanelOpen.value = true;
    transferQueue.enqueue([{
      sessionId: targetSession,
      direction: "upload",
      operation: "upload-file",
      name,
      localPath,
      remotePath: `${targetPath.replace(/\/$/, "")}/${name}`,
    }]);
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

function onRowContextMenu(event: MouseEvent, item: SftpEntry) {
  event.stopPropagation();
  selected.value = item;
  contextMenu.value = { item, x: event.clientX, y: event.clientY };
}

function onListContextMenu(event: MouseEvent) {
  if (!connected.value) return;
  selected.value = undefined;
  contextMenu.value = { x: event.clientX, y: event.clientY };
}

function runContextAction(action: () => unknown) {
  contextMenu.value = null;
  void action();
}

function openContextItem() {
  const item = contextMenu.value?.item;
  contextMenu.value = null;
  if (item) void openEntry(item);
}

async function copySelectedPath() {
  if (!selected.value) return;
  try {
    await copyText(selected.value.path);
    ElMessage.success(t("sftp.pathCopied"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

watch(
  () => props.visible,
  (visible) => {
    contextMenu.value = null;
    if (visible) void connect();
    else {
      clearImagePreview();
      editor.value = undefined;
    }
  },
  { immediate: true }
);

onBeforeUnmount(() => {
  disposed = true;
  transferQueue.dispose();
  contextMenu.value = null;
  connectedSession.value = null;
  clearImagePreview();
  sftpSessions.disconnected(sessionId.value);
  if (connected.value) void disconnectSftp(sessionId.value).catch(() => undefined);
});
</script>

<template>
  <aside v-show="visible" class="ssh-sftp-panel" :aria-label="protocolLabel">
    <header class="ssh-sftp-panel__header">
      <div>
        <strong>{{ protocolLabel }}</strong>
        <small>{{ remoteTarget }}</small>
      </div>
      <div class="ssh-sftp-panel__header-actions">
        <el-button
          text
          :icon="maximized ? ScaleToOriginal : FullScreen"
          :aria-label="t(maximized ? 'common.minimize' : 'common.maximize')"
          :title="t(maximized ? 'common.minimize' : 'common.maximize')"
          @click="emit('toggleMaximize')"
        />
        <el-button text :icon="Close" :aria-label="t('sftp.hideFilePanel')" @click="emit('close')" />
      </div>
    </header>

    <div class="ssh-sftp-panel__pathbar">
      <el-button
        text
        :icon="Back"
        :disabled="!connected || remotePath === '/'"
        :aria-label="t('sftp.parentDirectory')"
        @click="goUp"
      />
      <el-input v-model="remotePath" :disabled="!connected" :aria-label="t('sftp.path')" @keyup.enter="listRemote" />
      <el-button text :icon="Refresh" :disabled="!connected" :aria-label="t('common.refresh')" @click="listRemote" />
    </div>

    <el-scrollbar class="ssh-sftp-panel__actions">
      <div class="ssh-sftp-panel__actions-content">
        <el-button :icon="Upload" :disabled="!connected" @click="upload">{{ t("sftp.upload") }}</el-button>
        <el-button :icon="Download" :disabled="!selected" @click="downloadSelected">{{ t("sftp.download") }}</el-button>
        <el-button data-testid="sftp-create-file" :icon="DocumentAdd" :disabled="!connected" @click="createFile">{{ t("sftp.newFile") }}</el-button>
        <el-button :icon="FolderAdd" :disabled="!connected" @click="createDirectory">{{ t("sftp.newFolder") }}</el-button>
        <el-button text :icon="EditPen" :disabled="!selected" :aria-label="t('sftp.rename')" @click="renameSelected" />
        <el-popconfirm :title="t('common.delete')" @confirm="removeSelected">
          <template #reference>
            <el-button text type="danger" :icon="Delete" :disabled="!selected" :aria-label="t('common.delete')" />
          </template>
        </el-popconfirm>
      </div>
    </el-scrollbar>

    <div v-if="loading" class="ssh-sftp-panel__state">
      <el-icon class="is-loading"><Refresh /></el-icon><span>{{ t("sftp.loading") }}</span>
    </div>
    <div v-else-if="error" class="ssh-sftp-panel__state is-error">
      <span>{{ error }}</span><el-button text @click="retry">{{ t("common.refresh") }}</el-button>
    </div>
    <section v-else-if="editor" class="ssh-sftp-panel__editor">
      <div class="ssh-sftp-panel__editor-toolbar">
        <strong :title="editor.path">{{ editor.name }}{{ editor.dirty ? " •" : "" }}</strong>
        <el-button
          v-if="!editor.isImage"
          text
          :disabled="!editor.dirty || editor.loading || editor.saving || Boolean(editor.error)"
          :loading="editor.saving"
          @click="saveEditor"
        >
          {{ t("common.save") }}
        </el-button>
        <el-button text @click="closeEditor">{{ t("common.close") }}</el-button>
      </div>
      <div v-if="editor.loading" class="ssh-sftp-panel__state">
        <el-icon class="is-loading"><Refresh /></el-icon><span>{{ t("sftp.loading") }}</span>
      </div>
      <div v-else-if="editor.error" class="ssh-sftp-panel__state is-error">
        <span>{{ editor.error }}</span>
        <el-button text @click="closeEditor">{{ t("common.close") }}</el-button>
      </div>
      <el-image
        v-else-if="editor.imageUrl"
        class="ssh-sftp-panel__image-preview"
        :src="editor.imageUrl"
        :alt="editor.name"
        fit="contain"

        :preview-src-list="[editor.imageUrl]"
        preview-teleported
        hide-on-click-modal
        @error="editor.error = t('sftp.imageDecodeFailed')"
      />
      <MonacoTextEditor
        v-else-if="!editor.isImage"
        kind="sftp"
        :model-value="editor.content"
        :path="editor.path"
        :preferences="{
          fontSize: 13,
          tabSize: 2,
          wordWrap: 'on',
          minimap: false,
          lineNumbers: 'on',
        }"
        @update:model-value="updateEditorContent"
        @save="saveEditor"
      />
    </section>
    <el-scrollbar
      v-else
      ref="listScroller"
      class="ssh-sftp-panel__list"
      @scroll="onListScroll"
      @contextmenu.prevent="onListContextMenu"
    >
      <div v-if="listPaddingTop" aria-hidden="true" :style="{ height: `${listPaddingTop}px` }" />
      <button
        v-for="item in visibleEntries"
        :key="item.path"
        type="button"
        class="ssh-sftp-panel__row"
        :class="{ 'is-active': selected?.path === item.path }"
        @click="selected = item"
        @dblclick="openEntry(item)"
        @contextmenu.prevent="onRowContextMenu($event, item)"
      >
        <el-icon><Folder v-if="item.isDir" /><Document v-else /></el-icon>
        <span>{{ item.name }}</span>
        <small>{{ item.isDir ? "" : item.size }}</small>
      </button>
      <div v-if="listPaddingBottom" aria-hidden="true" :style="{ height: `${listPaddingBottom}px` }" />
      <div v-if="connected && entries.length === 0" class="ssh-sftp-panel__state">{{ t("sftp.emptyDirectory") }}</div>
    </el-scrollbar>

    <SftpTransferPanel v-if="transferCount" v-model:open="transferPanelOpen" :queue="transferQueue" />

    <AppContextMenu
      v-if="contextMenu"
      :open="true"
      :x="contextMenu.x"
      :y="contextMenu.y"
      @close="contextMenu = null"
    >
      <template v-if="contextMenu.item">
        <el-dropdown-item
          :icon="contextMenu.item.isDir ? FolderOpened : View"
          @click="openContextItem"
        >
          {{ contextMenu.item.isDir ? t("sftp.openFolder") : t("sftp.preview") }}
        </el-dropdown-item>
        <el-dropdown-item v-if="!contextMenu.item.isDir" :icon="Download" @click="runContextAction(downloadSelected)">
          {{ t("sftp.download") }}
        </el-dropdown-item>
        <el-dropdown-item :icon="CopyDocument" @click="runContextAction(copySelectedPath)">
          {{ t("sftp.copyPath") }}
        </el-dropdown-item>
        <el-dropdown-item :icon="EditPen" @click="runContextAction(renameSelected)">
          {{ t("sftp.rename") }}
        </el-dropdown-item>
        <el-dropdown-item class="is-danger" :icon="Delete" @click="runContextAction(removeSelected)">
          {{ t("common.delete") }}
        </el-dropdown-item>
      </template>
      <el-dropdown-item :divided="Boolean(contextMenu.item)" :icon="Upload" @click="runContextAction(upload)">
        {{ t("sftp.upload") }}
      </el-dropdown-item>
      <el-dropdown-item :icon="DocumentAdd" @click="runContextAction(createFile)">
        {{ t("sftp.newFile") }}
      </el-dropdown-item>
      <el-dropdown-item :icon="FolderAdd" @click="runContextAction(createDirectory)">
        {{ t("sftp.newFolder") }}
      </el-dropdown-item>
      <el-dropdown-item :icon="Refresh" @click="runContextAction(listRemote)">
        {{ t("common.refresh") }}
      </el-dropdown-item>
      <PluginMenuItems
        v-if="contextMenu.item"
        menu="sftp/file/context"
        :host="pluginMenuHost"
        :context="pluginMenuContext"
        @run="contextMenu = null"
      />
      <PluginServiceMenuItems :target="serviceTarget" @run="contextMenu = null" />
    </AppContextMenu>
  </aside>
</template>
