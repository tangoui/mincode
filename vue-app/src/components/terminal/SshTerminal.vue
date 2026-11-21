<script setup lang="ts">
import {
  computed,
  defineAsyncComponent,
  nextTick,
  onActivated,
  onBeforeUnmount,
  onDeactivated,
  onMounted,
  ref,
  watch,
  type Ref,
} from "vue";
import {
  ArrowDown,
  ArrowLeftBold,
  ArrowRightBold,
  ArrowUp,
  CircleCloseFilled,
  Clock,
  Close,
  Edit,
  FolderOpened,
  Monitor,
  RefreshRight,
  Search,
  Star,
  Tickets,
  VideoCamera,
  VideoCameraFilled,
  VideoPlay,
  WarningFilled,
} from "@element-plus/icons-vue";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { SerializeAddon } from "@xterm/addon-serialize";
import { Terminal, type IDisposable } from "@xterm/xterm";
import { ElMessage, ElMessageBox } from "element-plus";
import "@xterm/xterm/css/xterm.css";
import { terminalOverlayStyle, usePredictiveEcho } from "@/composables/use-predictive-echo";
import SshCommandHistory from "@/components/ssh/SshCommandHistory.vue";
import SshPortForwardControls from "@/components/ssh/SshPortForwardControls.vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import type { LocalTerminalActivity } from "@/components/terminal/LocalTerminal.vue";
import TerminalContextMenu from "@/components/terminal/TerminalContextMenu.vue";
import type {
  ShellEndedEvent,
  ShellOutputEvent,
  ShellRecoveredEvent,
  SshConnectionProfile,
  SshRuntimeCredentials,
  SshTerminalStatus,
} from "@/domain/ssh/types";
import { shellOutputEventName } from "@/domain/ssh/types";
import {
  analyzeTerminalPaste,
  canRevealRemotePath,
  terminalAiContextText,
  terminalTextAsMarkdown,
  type TerminalContextMenuAction,
  type TerminalTextContext,
} from "@/domain/terminal/context-menu";
import type { TerminalSplitDirection } from "@/domain/terminal/types";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import {
  deleteTerminalSnapshot,
  readTerminalSnapshot,
  saveTerminalSnapshot,
} from "@/services/terminal-session-cache";
import { resolveTerminalPointerContext } from "@/services/terminal-context-menu";
import { alternateScreenWheelInput } from "@/domain/terminal/alternate-screen-wheel";
import {
  attachTerminalWebglAddon,
  readTerminalPreferences,
} from "@/services/terminal-preferences";
import { createTerminalWebLinksAddon, type TerminalWebLinkFailure } from "@/services/terminal-web-links";
import { attachPluginTerminalLinks } from "@/services/terminal-plugin-links";
import { copyText, isDesktopRuntime, invokeBackend, listenAppEvent, readText } from "@/services/desktop-bridge";
import { useSshMfaStore } from "@/stores/ssh-mfa";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useTerminalAttentionStore } from "@/stores/terminal-attention";
import { isTerminalReport } from "@/domain/terminal/terminal-attention";
import TerminalPaneAttention from "@/components/terminal/TerminalPaneAttention.vue";
import {
  addTerminalRecording,
  createTerminalRecording,
  deleteTerminalRecording,
  ensureTerminalRecordingsLoaded,
  renameTerminalRecording,
  terminalRecordings as recordings,
  type TerminalRecordingFrame,
} from "@/services/terminal-tools";
import {
  resolveTerminalTheme,
  setTerminalTheme,
  TERMINAL_COLOR_SCHEME_CHANGED_EVENT,
  type TerminalColorScheme,
} from "@/services/terminal-theme";
import {
  createTerminalInputBatcher,
  isImmediateTerminalInput,
  type TerminalInputBatcher,
} from "@/domain/terminal/input-batcher";
import { createTerminalResizeSync } from "@/domain/terminal/resize-sync";
import {
  clampTerminalDimensions,
  fitTerminalSize,
  hasTerminalLayout,
  waitForTerminalLayout,
} from "@/services/terminal-viewport";
import { UI_THEME_CHANGED_EVENT, type UiTheme } from "@/services/ui-theme";
import { registerTerminalBufferReader } from "@/services/e2e-terminal-buffer-probe";
import { guardTerminalPasteEvents, useTerminalKeybindings } from "@/composables/use-terminal-keybindings";
import { openSystemBrowser } from "@/services/web-automation-service";
import { useResizablePane } from "@/composables/use-resizable-pane";

// 低频弹窗与侧栏面板按需加载，避免全部进入终端首屏 chunk。
const SshServerManagerPanel = defineAsyncComponent(
  () => import("@/components/ssh/SshServerManagerPanel.vue")
);
const SshSftpPanel = defineAsyncComponent(() => import("@/components/ssh/SshSftpPanel.vue"));
const TerminalAiActionsDialog = defineAsyncComponent(
  () => import("@/components/terminal/TerminalAiActionsDialog.vue")
);
const TerminalBookmarksDialog = defineAsyncComponent(
  () => import("@/components/terminal/TerminalBookmarksDialog.vue")
);
const TerminalPasteConfirmDialog = defineAsyncComponent(
  () => import("@/components/terminal/TerminalPasteConfirmDialog.vue")
);
const TerminalPlaybackDialog = defineAsyncComponent(
  () => import("@/components/terminal/TerminalPlaybackDialog.vue")
);
const TerminalCommandSnippetsPanel = defineAsyncComponent(
  () => import("@/components/terminal/TerminalCommandSnippetsPanel.vue")
);

const props = withDefaults(
  defineProps<{
    sessionId: string;
    profile: SshConnectionProfile;
    credentials?: SshRuntimeCredentials;
    active?: boolean;
    canClose?: boolean;
    /** Shown on the disabled close button (e.g. the last pane of a pinned workbench tab). */
    closeDisabledReason?: string;
    /** False while the pane's terminal tab is hidden: the WebGL renderer is released at once. */
    visible?: boolean;
    canSplit?: boolean;
    keepAliveOnUnmount?: boolean;
  }>(),
  {
    credentials: undefined,
    active: false,
    canClose: false,
    closeDisabledReason: undefined,
    visible: true,
    canSplit: true,
    keepAliveOnUnmount: true,
  }
);

const emit = defineEmits<{
  select: [];
  close: [];
  reload: [];
  configure: [];
  split: [direction: TerminalSplitDirection];
  statusChange: [status: SshTerminalStatus, detail?: string];
  activity: [snapshot: LocalTerminalActivity];
}>();

const { locale, t } = useVueI18n();
const closeTooltip = computed(() =>
  !props.canClose && props.closeDisabledReason ? props.closeDisabledReason : t("terminal.closePane")
);
const terminalAttention = useTerminalAttentionStore();
const sshMfa = useSshMfaStore();

const terminalPane = ref<HTMLElement | null>(null);
const terminalHost = ref<HTMLElement>();
const toolsExpanded = ref(false);
const searchOpen = ref(false);
const searchQuery = ref("");
const searchFound = ref<boolean | null>(null);
const searchInput = ref<{ focus: () => void }>();
const bookmarksOpen = ref(false);
const bookmarkInitialCommand = ref("");
const playbackOpen = ref(false);
const aiActionsOpen = ref(false);
const pasteDialogOpen = ref(false);
const commandHistoryOpen = ref(false);
function fillHistoryCommand(command: string) {
  if (status.value !== "ready" || !command || /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u.test(command)) return;
  predictiveEcho.paste(command);
  commandHistoryOpen.value = false;
  xterm?.focus();
}
const pendingPasteText = ref("");
const snippetsOpen = ref(false);
const contextMenu = ref({
  open: false,
  x: 0,
  y: 0,
  context: { text: "", line: "", token: "" } as TerminalTextContext,
});
const sftpVisible = ref(false);
const sshConnections = useSshConnectionsStore();
// 终端会话打开后用户可能又编辑了连接（例如刚配置 FTP），文件面板必须使用 store 里最新的配置与凭据，
// 而不是会话创建时的快照，否则会按旧的 SFTP 协议和缺失的凭据连接。
const fileTransferProfile = computed(() => sshConnections.findProfile(props.profile.id) ?? props.profile);
const fileTransferCredentials = computed<SshRuntimeCredentials | undefined>(() => {
  const saved = sshConnections.credentialsFor(props.profile.id);
  if (!saved && !props.credentials) return undefined;
  return { ...saved, ...props.credentials };
});
const fileTransferEnabled = computed(() => fileTransferProfile.value.fileTransfer?.enabled !== false);
const fileTransferProtocol = computed(() => fileTransferProfile.value.fileTransfer?.protocol ?? "sftp");
const filePanelLabel = computed(() => t(sftpVisible.value ? "sftp.hideFilePanel" : "sftp.showFilePanel"));
// 异步组件在 chunk 加载完成前没有实例，这里只按对外暴露的方法声明引用类型。
const sftpPanel = ref<{ revealPath(path: string): Promise<void> }>();
const pendingRevealPath = ref<string | null>(null);
const serverManagerVisible = ref(false);
// 弹窗/抽屉的异步 chunk 仅在首次打开时拉取；首次打开后保持挂载，以保留关闭过渡与内部状态。
function useOnceOpened(source: Ref<boolean>) {
  const opened = ref(false);
  watch(source, (value) => {
    if (value) opened.value = true;
  });
  return opened;
}
const serverManagerMounted = useOnceOpened(serverManagerVisible);
const pasteDialogMounted = useOnceOpened(pasteDialogOpen);
const aiActionsMounted = useOnceOpened(aiActionsOpen);
const bookmarksMounted = useOnceOpened(bookmarksOpen);
const playbackMounted = useOnceOpened(playbackOpen);
const snippetsMounted = useOnceOpened(snippetsOpen);
const sftpMaximized = ref(false);
const SFTP_MIN_WIDTH = 320;
const TERMINAL_MIN_WIDTH = 280;
const sftpMaximumWidth = (paneWidth: number) =>
  Math.max(SFTP_MIN_WIDTH, paneWidth - TERMINAL_MIN_WIDTH);
const {
  size: sftpWidth,
  hydrate: hydrateSftpWidth,
  persist: persistSftpWidth,
} = useResizablePane({
  sizeKey: "app_terminal_sftp_width",
  collapsedKey: "app_terminal_sftp_collapsed",
  defaultSize: 360,
  minSize: SFTP_MIN_WIDTH,
  maxSize: sftpMaximumWidth,
  containerRef: terminalPane,
});
function clampSftpWidth(value: number) {
  const paneWidth = terminalPane.value?.getBoundingClientRect().width ?? window.innerWidth;
  return Math.max(SFTP_MIN_WIDTH, Math.min(sftpMaximumWidth(paneWidth), Math.round(value)));
}

const displayedSftpWidth = computed(() => clampSftpWidth(sftpWidth.value));
const recording = ref(false);
const status = ref<SshTerminalStatus>("connecting");
const statusDetail = ref("");
const statusLabel = computed(() => t(`ssh.status.${status.value}` as MessageKey));
const statusMessage = computed(() => {
  if (status.value === "connecting") {
    return t("ssh.connectingHint")
      .replace("{user}", props.profile.username)
      .replace("{host}", props.profile.host)
      .replace("{port}", String(props.profile.port));
  }
  if (status.value === "reconnecting") return t("ssh.reconnecting");
  if (status.value === "credentials") return t("ssh.credentialsRequired");
  if (status.value === "unavailable") return t("terminal.onlyDesktop");
  if (status.value === "error") return t("ssh.startFailed");
  return "";
});
const terminalTitle = computed(
  () => props.profile.name || `${props.profile.username}@${props.profile.host}`
);

let xterm: Terminal | null = null;
let pluginLinks: IDisposable | null = null;
let fitAddon: FitAddon | null = null;
let serializeAddon: SerializeAddon | null = null;
let searchAddon: SearchAddon | null = null;
let webglAddonHandle: IDisposable | null = null;
/** A WebGL renderer is loading (see attachWebgl). */
let webglLoading = false;
let inactiveWebglTimer: ReturnType<typeof setTimeout> | undefined;
const TERMINAL_INACTIVE_WEBGL_DELAY_MS = 30_000;
let resizeObserver: ResizeObserver | null = null;
let deactivated = false;
let inputSubscription: IDisposable | null = null;
let stopOutput: (() => void) | null = null;
let stopEnded: (() => void) | null = null;
let stopPasteGuard: (() => void) | null = null;
let stopRecovered: (() => void) | null = null;
let stopBufferProbe: (() => void) | null = null;
let closed = false;
let backendReady = false;
// SSH 输入走有返回结果的 IPC 调用，回显固定走 shell-output 事件，避免 Stream
// 传输层在快速输入时只报告“已入队”而导致远端 PTY 回显缺字符。
let inputBatcher: TerminalInputBatcher | undefined;
let recordingFrames: TerminalRecordingFrame[] = [];
let recordingStartedAt = 0;
let lastActivityEmitAt = 0;
let quietActivityTimer: number | undefined;

// 预测回显：只在就绪、可见的窗格里预测；未聚焦、有选区等由 binding 自己判断。
const predictiveEcho = usePredictiveEcho({
  terminal: () => xterm ?? undefined,
  screen: () => terminalHost.value?.querySelector<HTMLElement>(".xterm-screen") ?? null,
  enabled: () => status.value === "ready" && !closed && props.visible && !deactivated,
  style: () => terminalOverlayStyle(xterm),
  probeId: () => props.sessionId,
});

function emitActivity(snapshot: LocalTerminalActivity) {
  emit("activity", snapshot);
}

function clearQuietActivityTimer() {
  if (quietActivityTimer === undefined) return;
  window.clearTimeout(quietActivityTimer);
  quietActivityTimer = undefined;
}

function scheduleQuietActivity(lastOutputAt?: number) {
  clearQuietActivityTimer();
  quietActivityTimer = window.setTimeout(() => {
    quietActivityTimer = undefined;
    if (closed || !backendReady) return;
    emitActivity({
      phase: "quiet",
      updatedAt: Date.now(),
      ...(lastOutputAt ? { lastOutputAt } : {}),
    });
  }, 3_000);
}

function emitOutputActivity() {
  const now = Date.now();
  if (now - lastActivityEmitAt >= 500) {
    lastActivityEmitAt = now;
    emitActivity({ phase: "active", updatedAt: now, lastOutputAt: now });
  }
  scheduleQuietActivity(now);
}

function setStatus(nextStatus: SshTerminalStatus, detail = "") {
  status.value = nextStatus;
  statusDetail.value = detail.trim();
  emit("statusChange", nextStatus, statusDetail.value || undefined);
}

function retryConnection() {
  emit("reload");
}

// 远端 PTY 尺寸只经由这里同步：每次发送都是一次 SSH window-change，拖动分栏时
// 按帧合并 fit，且仅在行列真正变化时发送。
const resizeSync = createTerminalResizeSync({
  measure() {
    if (!xterm || !fitAddon || closed || deactivated || !props.visible) return null;
    // 没有真实宽高时 FitAddon 会给出它自己的下限 2 列，clamp 后变成 20；这个假尺寸
    // 送进 PTY 就是永久的 20 列硬换行。跳过即可，ResizeObserver 稍后会重试。
    if (!hasTerminalLayout(terminalHost.value)) return null;
    // Fit with the WebGL renderer the PTY size was taken with (see ensureFitRenderer).
    ensureFitRenderer();
    if (webglLoading && backendReady) return null;
    try {
      // Screen and remote PTY get the same size; a sliver of a container changes neither.
      return fitTerminalSize(xterm, fitAddon);
    } catch {
      // 隐藏窗格尺寸尚未稳定时，等待下一次 ResizeObserver。
      return null;
    }
  },
  canSend: () => isDesktopRuntime() && backendReady && !closed,
  send: (dimensions) =>
    invokeBackend<void>("resize_local_shell", {
      payload: { sessionId: props.sessionId, ...dimensions },
    }),
});

function fitAndResize() {
  resizeSync.flush();
}

function focusTerminal() {
  if (!props.active) return;
  void nextTick(() => {
    if (!props.active || !props.visible || deactivated || closed) return;
    fitAndResize();
    xterm?.focus();
  });
}

function clearInactiveWebglTimer() {
  if (!inactiveWebglTimer) return;
  clearTimeout(inactiveWebglTimer);
  inactiveWebglTimer = undefined;
}

function releaseTerminalWebgl() {
  clearInactiveWebglTimer();
  webglAddonHandle?.dispose();
  webglAddonHandle = null;
  webglLoading = false;
}

// WebGL fits more columns than the DOM renderer in the same box (whole device pixels per
// cell). A live session shown again waits for the renderer it is drawn with before it
// fits, so its PTY is not resized to the DOM width and back.
function attachWebgl(terminal: Terminal) {
  webglLoading = true;
  const handle = attachTerminalWebglAddon(terminal, () => {
    webglLoading = false;
    // Renderer metrics changed: the overlay measures its cells again.
    predictiveEcho.reset();
    resizeSync.schedule();
  });
  if (!handle) webglLoading = false;
  return handle ?? null;
}

// WebGL and DOM renderers fit different column counts into the same box. An inactive pane
// whose WebGL was released (tab hidden, or the inactive delay) would otherwise fit with the
// DOM renderer when it is shown again or resized, resizing its PTY, and once more when it is
// active again; full-screen programs redraw after each resize and leave remnants. So every
// fit runs with WebGL: a released renderer is attached again for the fit and, on an inactive
// pane, released after the usual delay.
function ensureFitRenderer() {
  if (!xterm || webglAddonHandle) return;
  webglAddonHandle = attachWebgl(xterm);
  if (!webglAddonHandle || props.active) return;
  clearInactiveWebglTimer();
  inactiveWebglTimer = setTimeout(releaseTerminalWebgl, TERMINAL_INACTIVE_WEBGL_DELAY_MS);
}

function restoreTerminalWebgl() {
  clearInactiveWebglTimer();
  if (!xterm || webglAddonHandle) return;
  webglAddonHandle = attachWebgl(xterm);
}

// The window regaining focus (app switch, wake from sleep) re-sends the visible pane's size: the
// send dedupe would otherwise keep a screen/PTY grid mismatch until the next real size change.
function handleWindowFocus() {
  if (!props.active || !props.visible || deactivated || closed) return;
  resizeSync.resync();
}

function syncTerminalRenderActivity(active: boolean, immediate = false) {
  clearInactiveWebglTimer();
  if (immediate || !props.visible || deactivated) {
    predictiveEcho.reset();
    releaseTerminalWebgl();
    return;
  }
  // Shown again: send the size even if unchanged, so a PTY left on another size is corrected.
  resizeSync.reset();
  if (active) {
    restoreTerminalWebgl();
    focusTerminal();
    return;
  }
  // Showing the same-sized tab need not produce a ResizeObserver notification.
  // Restore and fit every visible pane, including the unfocused splits.
  void nextTick(fitAndResize);
  inactiveWebglTimer = setTimeout(releaseTerminalWebgl, TERMINAL_INACTIVE_WEBGL_DELAY_MS);
}


function updateSftpWidth(value: number) {
  if (sftpMaximized.value) return;
  sftpWidth.value = clampSftpWidth(value);
}

function persistUpdatedSftpWidth(value: number) {
  if (sftpMaximized.value) return;
  sftpWidth.value = clampSftpWidth(value);
  void persistSftpWidth();
}

function toggleSftpMaximized() {
  sftpMaximized.value = !sftpMaximized.value;
  void nextTick(() => {
    fitAndResize();
  });
}

function closeSftpPanel() {
  sftpVisible.value = false;
  sftpMaximized.value = false;
  pendingRevealPath.value = null;
}

// 异步面板在 chunk 解析并真正挂载后，模板 ref 才会出现；此时补做挂起的定位请求。
watch(sftpVisible, (visible) => {
  if (!visible) pendingRevealPath.value = null;
});
watch(sftpPanel, (panel) => {
  if (!panel || !sftpVisible.value) return;
  const pending = pendingRevealPath.value;
  if (!pending) return;
  pendingRevealPath.value = null;
  void panel.revealPath(pending).catch((cause) => {
    ElMessage.error({
      message:
        cause instanceof Error && cause.message
          ? cause.message
          : t("terminal.context.sftpRevealFailed"),
      grouping: true,
      showClose: true,
    });
  });
});

function focusSearchInput() {
  void nextTick(() => searchInput.value?.focus());
}

function openSearch() {
  searchOpen.value = true;
  focusSearchInput();
  if (searchQuery.value) findInTerminal(true);
}

function closeSearch() {
  searchOpen.value = false;
  searchFound.value = null;
  xterm?.focus();
}

function findInTerminal(next: boolean) {
  const query = searchQuery.value;
  searchFound.value = query
    ? next
      ? searchAddon?.findNext(query) === true
      : searchAddon?.findPrevious(query) === true
    : null;
}

function handleSearchKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    closeSearch();
    return;
  }
  if (event.key === "Enter") {
    event.preventDefault();
    findInTerminal(!event.shiftKey);
  }
}

function startRecording() {
  recordingFrames = [];
  recordingStartedAt = Date.now();
  recording.value = true;
}

async function stopRecording() {
  recording.value = false;
  if (!recordingFrames.length) return;
  const recorded = createTerminalRecording(recordingFrames, terminalTitle.value);
  recordingFrames = [];
  await addTerminalRecording(recorded);
  playbackOpen.value = true;
}

function appendRecordingFrame(data: string) {
  if (!recording.value || !data || recordingFrames.length >= 50_000) return;
  recordingFrames.push({ t: Date.now() - recordingStartedAt, d: data });
}

async function deleteRecording(id: string) {
  await deleteTerminalRecording(id);
}

async function renameRecording(id: string, name: string) {
  await renameTerminalRecording(id, name);
}

// SSH 输入必须由有确认的 IPC 调用提交。Stream 的 socket.send 只能表示
// 浏览器接受了帧，不能证明后端已将字节放入 SSH channel；快速逐字输入时会造成静默丢字。
async function writeShellInput(input: string) {
  if (!input || closed || !isDesktopRuntime()) return;
  await invokeBackend<void>("write_to_shell", {
    payload: { sessionId: props.sessionId, input },
  });
}

/** The status line moves the cursor: predictions on screen are cleared first. */
function writeInputFailure() {
  predictiveEcho.reset();
  xterm?.writeln(`\r\n\x1b[31m[MinCode] ${t("terminal.inputFailed")}\x1b[0m`);
}

function queueShellInput(input: string, immediate = false) {
  if (!inputBatcher) {
    inputBatcher = createTerminalInputBatcher(writeShellInput, {
      delayMs: 8,
      maxChars: 256,
      onError: () => writeInputFailure(),
    });
  }
  return inputBatcher.push(input, immediate);
}

async function sendTerminalControl(input: string) {
  if (closed || status.value !== "ready" || !isDesktopRuntime()) return;
  try {
    await queueShellInput(input, true);
  } catch {
    writeInputFailure();
  }
}

async function runBookmarkedCommand(command: string) {
  if (!command.trim() || closed || !isDesktopRuntime() || status.value !== "ready") return;
  try {
    await queueShellInput(`${command.trim()}\r`, true);
    focusTerminal();
  } catch {
    writeInputFailure();
  }
}

function backendPayload() {
  return {
    sessionId: props.sessionId,
    profileId: props.profile.id,
    host: props.profile.host,
    port: props.profile.port,
    username: props.profile.username,
    attachIfExists: true,
    authType: props.profile.authType,
    password: props.credentials?.password,
    keyFile: props.profile.keyFile,
    passphrase: props.credentials?.passphrase,
    totpUri: props.credentials?.totpUri,
    portForwards: props.profile.portForwards,
    initialDirectory: props.profile.defaultPath,
    postConnectCommands: props.profile.postConnectCommands ?? [],
    algorithmPolicy: props.profile.algorithmPolicy ?? "modern",
    keepAliveIntervalSeconds: props.profile.keepAlive?.intervalSeconds ?? 30,
    keepAliveMaxMisses: props.profile.keepAlive?.maxMisses ?? 3,
    reconnectEnabled: props.profile.reconnect?.enabled !== false,
    reconnectMaxAttempts: props.profile.reconnect?.maxAttempts ?? 5,
    reconnectBaseDelayMs: props.profile.reconnect?.baseDelayMs ?? 1000,
    reconnectMaxDelayMs: props.profile.reconnect?.maxDelayMs ?? 15_000,
    jumpHost: props.profile.jumpHost
      ? {
          ...props.profile.jumpHost,
          algorithmPolicy: props.profile.algorithmPolicy ?? "modern",
          password: props.credentials?.jumpPassword,
          passphrase: props.credentials?.jumpPassphrase,
        }
      : undefined,
    socksProxy: props.profile.socksProxy
      ? {
          host: props.profile.socksProxy.host,
          port: props.profile.socksProxy.port,
          username: props.profile.socksProxy.username,
          password: props.credentials?.socksPassword,
        }
      : undefined,
  };
}

function credentialsDefinitelyMissing() {
  if (props.profile.authType === "password" && !props.credentials?.password) return true;
  if (props.profile.jumpHost?.authType === "password" && !props.credentials?.jumpPassword) {
    return true;
  }
  // 配置了代理用户名就必须提供代理密码，避免服务端认证失败被误判为网络问题。
  return Boolean(props.profile.socksProxy?.username && !props.credentials?.socksPassword);
}

function renderUnavailableMessage() {
  if (!xterm || isDesktopRuntime() || status.value !== "unavailable") return;
  xterm.reset();
  xterm.writeln(`\x1b[33m[MinCode] ${t("terminal.onlyDesktop")}\x1b[0m`);
}

function handleThemeChange(event: Event) {
  if (!xterm) return;
  setTerminalTheme(xterm, resolveTerminalTheme((event as CustomEvent<UiTheme>).detail));
  predictiveEcho.reset();
}

function handleColorSchemeChange(event: Event) {
  if (xterm) {
    setTerminalTheme(
      xterm,
      resolveTerminalTheme(undefined, (event as CustomEvent<TerminalColorScheme>).detail)
    );
    predictiveEcho.reset();
  }
}

function handleWebLinkFailure(failure: TerminalWebLinkFailure) {
  const message =
    failure === "desktop-required"
      ? t("terminal.linkDesktopOnly")
      : t("terminal.openLinkFailed");
  ElMessage.error({ message, grouping: true, showClose: true });
}

function openTerminalContextMenu(event: MouseEvent) {
  event.preventDefault();
  emit("select");
  contextMenu.value = {
    open: true,
    x: event.clientX,
    y: event.clientY,
    context: resolveTerminalPointerContext(xterm, terminalHost.value, event.clientX, event.clientY),
  };
}

function closeTerminalContextMenu() {
  contextMenu.value.open = false;
}

async function copyContextText(text: string) {
  if (!text) return;
  try {
    await copyText(text);
    ElMessage.success({ message: t("terminal.context.copied"), grouping: true });
  } catch {
    ElMessage.error({ message: t("terminal.context.clipboardFailed"), grouping: true });
  }
}

/** Guarded paste: multi-line or risky text needs confirmation before it reaches the shell. */
function pasteGuarded(text: string) {
  if (!text) return;
  const analysis = analyzeTerminalPaste(text);
  if (analysis.requiresConfirmation) {
    pendingPasteText.value = text;
    pasteDialogOpen.value = true;
    return;
  }
  predictiveEcho.paste(analysis.text);
}

async function requestPaste() {
  let text: string;
  try {
    text = await readText();
  } catch {
    ElMessage.error({ message: t("terminal.context.clipboardReadFailed"), grouping: true });
    return;
  }
  pasteGuarded(text);
}

/** Keyboard copy: silent on success, like a native terminal. */
async function copyTerminalSelection() {
  const text = xterm?.getSelection() ?? "";
  if (!text) return;
  try {
    await copyText(text);
  } catch {
    ElMessage.error({ message: t("terminal.context.clipboardFailed"), grouping: true });
  }
}

const handleTerminalKey = useTerminalKeybindings({
  xterm: () => xterm,
  copy: copyTerminalSelection,
  paste: requestPaste,
  find: openSearch,
  clear: () => xterm?.clear(),
  selectAll: () => xterm?.selectAll(),
});

function pasteConfirmed(text: string) {
  predictiveEcho.paste(text);
  focusTerminal();
}

function bracketedPasteMode() {
  return xterm?.modes.bracketedPasteMode === true;
}

/** Command snippet from the drawer: already planned (no "\r", no trailing newline; multi-line only in bracketed paste). */
function insertCommandSnippet(text: string) {
  if (status.value !== "ready" || !text) return;
  predictiveEcho.paste(text);
  focusTerminal();
}

async function confirmClosePane() {
  if (!props.canClose) return;
  try {
    await ElMessageBox.confirm(t("terminal.context.closeConfirm"), t("terminal.closePane"), {
      type: "warning",
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    emit("close");
  } catch {
    // 用户取消关闭。
  }
}

function shellQuote(value: string) {
  return `'${value.replace(/'/g, `'"'"'`)}'`;
}

async function revealContextPathInSftp(path?: string) {
  if (!canRevealRemotePath(path)) {
    ElMessage.warning({ message: t("terminal.context.sftpRelativePath"), grouping: true });
    return;
  }
  sftpVisible.value = true;
  await nextTick();
  // chunk 已加载、面板已挂载时直接调用；否则挂起，等模板 ref 出现后补执行。
  if (sftpPanel.value) {
    try {
      await sftpPanel.value.revealPath(path!);
    } catch (cause) {
      ElMessage.error({
        message:
          cause instanceof Error && cause.message
            ? cause.message
            : t("terminal.context.sftpRevealFailed"),
        grouping: true,
        showClose: true,
      });
    }
    return;
  }
  pendingRevealPath.value = path ?? null;
}

async function handleContextMenuAction(action: TerminalContextMenuAction) {
  const context = contextMenu.value.context;
  switch (action) {
    case "copy":
      await copyContextText(context.text);
      return;
    case "paste":
      await requestPaste();
      return;
    case "insert-command-snippet":
      snippetsOpen.value = true;
      return;
    case "select-all":
      xterm?.selectAll();
      return;
    case "search-selection":
      searchQuery.value = context.text.trim();
      openSearch();
      return;
    case "clear":
      xterm?.clear();
      return;
    case "copy-markdown":
      await copyContextText(terminalTextAsMarkdown(context.text));
      return;
    case "copy-url":
      await copyContextText(context.url ?? "");
      return;
    case "open-url":
      if (!context.url) return;
      try {
        await openSystemBrowser(context.url);
      } catch {
        handleWebLinkFailure(isDesktopRuntime() ? "open-failed" : "desktop-required");
      }
      return;
    case "copy-path":
      await copyContextText(context.path ?? "");
      return;
    case "reveal-sftp":
      await revealContextPathInSftp(context.path);
      return;
    case "bookmark":
      bookmarkInitialCommand.value = context.text.trim();
      bookmarksOpen.value = true;
      return;
    case "copy-ssh-command":
      await copyContextText(`ssh -p ${props.profile.port} ${shellQuote(`${props.profile.username}@${props.profile.host}`)}`);
      return;
    case "ai-assistant":
      aiActionsOpen.value = true;
      return;
    case "send-interrupt":
      await sendTerminalControl("\x03");
      return;
    case "send-eof":
      await sendTerminalControl("\x04");
      return;
    case "split-horizontal":
      emit("split", "horizontal");
      return;
    case "split-vertical":
      emit("split", "vertical");
      return;
    case "reload":
      emit("reload");
      return;
    case "close":
      await confirmClosePane();
      return;
    default:
      return;
  }
}

// A hidden terminal tab releases WebGL at once; a pane switch inside a shown tab keeps the delay.
watch([() => props.active, () => props.visible], ([active, visible]) => syncTerminalRenderActivity(active, !visible));
watch(playbackOpen, (visible) => {
  if (visible) void ensureTerminalRecordingsLoaded();
});
watch(bookmarksOpen, (visible) => {
  if (!visible) bookmarkInitialCommand.value = "";
});
watch(
  () => props.credentials,
  (credentials) => sshMfa.registerCredentials(props.sessionId, credentials),
  { deep: true }
);
watch(locale, renderUnavailableMessage);
onActivated(() => {
  deactivated = false;
  syncTerminalRenderActivity(props.active);
});
onDeactivated(() => {
  deactivated = true;
  syncTerminalRenderActivity(false, true);
});

onMounted(async () => {
  void hydrateSftpWidth();
  sshMfa.ensureListening();
  sshMfa.registerCredentials(props.sessionId, props.credentials);
  if (!terminalHost.value) return;
  emitActivity({ phase: "connecting", updatedAt: Date.now() });

  xterm = new Terminal({
    cursorBlink: true,
    cursorStyle: "bar",
    fontFamily: '"SFMono-Regular", Menlo, Monaco, Consolas, "Liberation Mono", monospace',
    fontSize: 13,
    lineHeight: 1.25,
    scrollback: readTerminalPreferences().scrollback,
  });
  fitAddon = new FitAddon();
  serializeAddon = new SerializeAddon();
  searchAddon = new SearchAddon();
  xterm.loadAddon(fitAddon);
  xterm.loadAddon(serializeAddon);
  xterm.loadAddon(searchAddon);
  xterm.loadAddon(createTerminalWebLinksAddon({ onFailure: handleWebLinkFailure }));
  // Before open: with the WebGL module preloaded, open creates the WebGL renderer directly
  // instead of building, fitting and painting a DOM renderer that WebGL then replaces. Every
  // visible pane fits with WebGL (an inactive one releases it after the usual delay).
  if (props.active || props.visible) ensureFitRenderer();
  xterm.open(terminalHost.value);
  setTerminalTheme(xterm, resolveTerminalTheme());
  pluginLinks = attachPluginTerminalLinks(xterm, {
    kind: "ssh",
    host: terminalHost.value,
    onWebLinkFailure: handleWebLinkFailure,
  });
  const bufferTerminal = xterm;
  stopBufferProbe = registerTerminalBufferReader(props.sessionId, () => {
    const buffer = bufferTerminal.buffer.active;
    const lines: string[] = [];
    for (let index = 0; index < buffer.length; index += 1) lines.push(buffer.getLine(index)?.translateToString(true) ?? "");
    return lines.join("\n");
  });
  window.addEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  window.addEventListener("focus", handleWindowFocus);
  window.addEventListener(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, handleColorSchemeChange);

  const snapshot = readTerminalSnapshot(props.sessionId);
  if (snapshot) {
    xterm.write(snapshot);
    predictiveEcho.reset();
  }
  fitAndResize();

  if (!isDesktopRuntime()) {
    setStatus("unavailable");
    emitActivity({ phase: "failed", updatedAt: Date.now(), endedAt: Date.now() });
    renderUnavailableMessage();
    return;
  }

  // 输出统一入口：事件通道与流通道共用，避免录制逻辑写两份。
  const handleShellOutput = (data: string) => {
    if (closed) return;
    appendRecordingFrame(data);
    emitOutputActivity();
    predictiveEcho.write(data);
  };

  stopOutput = listenAppEvent<ShellOutputEvent>(shellOutputEventName(props.sessionId), (event) => {
    handleShellOutput(event.data);
  });
  stopRecovered = listenAppEvent<ShellRecoveredEvent>("shell-session-recovered", (event) => {
    if (event.sessionId !== props.sessionId || closed) return;
    setStatus("ready");
    emitActivity({ phase: "active", updatedAt: Date.now() });
    scheduleQuietActivity();
    predictiveEcho.reset();
    xterm?.writeln(`\r\n\x1b[32m[MinCode] ${t("ssh.recovered")}\x1b[0m`);
    resizeSync.reset();
    fitAndResize();
  });
  stopEnded = listenAppEvent<ShellEndedEvent>("shell-session-ended", (event) => {
    if (event.sessionId !== props.sessionId || closed) return;
    clearQuietActivityTimer();
    predictiveEcho.reset();
    const endedAt = Date.now();
    if (event.recoverable) {
      setStatus("reconnecting");
      emitActivity({ phase: "connecting", summary: event.reason, updatedAt: endedAt });
      xterm?.writeln(`\r\n\x1b[33m[MinCode] ${t("ssh.reconnecting")}\x1b[0m`);
      return;
    }
    setStatus("error", t("ssh.sessionEnded"));
    emitActivity({ phase: "completed", summary: event.reason, updatedAt: endedAt, endedAt });
    deleteTerminalSnapshot(props.sessionId);
    xterm?.writeln(`\r\n\x1b[31m[MinCode] ${t("ssh.sessionEnded")}\x1b[0m`);
  });

  resizeObserver = new ResizeObserver(() => resizeSync.schedule());
  resizeObserver.observe(terminalHost.value);

  // Keymap-driven terminal keys: copy/paste/find/clear/select-all run here,
  // skip-shell app bindings bubble to the dispatcher, the rest reach the shell.
  xterm.attachCustomKeyEventHandler(handleTerminalKey);
  xterm.attachCustomWheelEventHandler((event) => {
    const input = alternateScreenWheelInput(
      xterm?.buffer.active.type ?? "normal",
      xterm?.modes.mouseTrackingMode ?? "none",
      event.deltaY,
      event.ctrlKey || event.metaKey || event.altKey,
    );
    if (!input) return true;
    void queueShellInput(input, true).catch(() => undefined);
    return false;
  });
  stopPasteGuard = guardTerminalPasteEvents(terminalHost.value, pasteGuarded);
  inputSubscription = xterm.onData((input) => {
    if (closed || status.value !== "ready") return;
    // Typing into the pane acknowledges its terminal attention (tab bell).
    if (!isTerminalReport(input) && terminalAttention.items.has(props.sessionId)) terminalAttention.dismiss([props.sessionId]);
    // 终端自动应答（CPR、DA、焦点、鼠标报告）不是按键，不能清空预测和首字标记
    if (!isTerminalReport(input)) predictiveEcho.input(input);
    void queueShellInput(input, isImmediateTerminalInput(input)).catch(() => {
      writeInputFailure();
    });
  });

  try {
    const alive = await invokeBackend<boolean>("shell_session_alive", {
      payload: { sessionId: props.sessionId },
    }).catch(() => false);
    if (!alive && credentialsDefinitelyMissing()) {
      setStatus("credentials");
      // generic PTY 阶段里没有“等待凭据”，未建连即视为失败，避免伪装成仍在连接。
      emitActivity({ phase: "failed", updatedAt: Date.now(), endedAt: Date.now() });
      xterm.writeln(`\x1b[33m[MinCode] ${t("ssh.credentialsRequired")}\x1b[0m`);
      return;
    }
    if (!alive) {
      setStatus("connecting");
      xterm.writeln(`\x1b[36m[MinCode] ${statusMessage.value}\x1b[0m`);
    }

    // 分栏面板首帧的 flex-basis 是 0，而子组件的 onMounted 早于父组件的首次测量。
    // 先等真实布局再量一次，否则 PTY 会以 20 列诞生，远端 CLI 的输出就永久按 20
    // 列硬换行 —— 已进 scrollback 的硬换行不会随后续 SIGWINCH 回流。
    await waitForTerminalLayout(terminalHost.value);
    if (closed) return;
    fitAndResize();

    const dimensions = clampTerminalDimensions(xterm.cols, xterm.rows);
    const message = await invokeBackend<string>("start_ssh_shell", {
      payload: { ...backendPayload(), cols: dimensions.cols, rows: dimensions.rows },
    });
    if (closed) return;
    resizeSync.reset();
    backendReady = true;
    setStatus("ready");
    emitActivity({ phase: "active", updatedAt: Date.now() });
    scheduleQuietActivity();
    // SSH 输入已经通过有确认的 IPC 调用提交；回显固定走 shell-output 事件。
    // 不启用 Streams 数据面：Stream.Send 的成功只表示帧进入传输层，不能证明
    // WebView 已消费。在快速输入的远端 PTY 回显场景中，这可能造成单字符回显丢失。
    if (message !== "SSH shell attached" && message !== "SSH shell recovered") {
      xterm.writeln(`\x1b[32m[MinCode] ${message}\x1b[0m`);
    }
    fitAndResize();
    focusTerminal();
  } catch (error) {
    const missingCredentials = credentialsDefinitelyMissing();
    const detail = error instanceof Error && error.message ? error.message : "";
    setStatus(missingCredentials ? "credentials" : "error", detail);
    clearQuietActivityTimer();
    const endedAt = Date.now();
    emitActivity({ phase: "failed", updatedAt: endedAt, endedAt });
    const suffix = detail ? ` ${detail}` : "";
    xterm.writeln(`\x1b[31m[MinCode] ${t("ssh.startFailed")}${suffix}\x1b[0m`);
  }
});

onBeforeUnmount(() => {
  sshMfa.unregisterSession(props.sessionId);
  clearQuietActivityTimer();
  closed = true;
  backendReady = false;
  inputBatcher?.dispose();
  inputBatcher = undefined;

  if (serializeAddon && xterm && isDesktopRuntime()) {
    try {
      saveTerminalSnapshot(props.sessionId, serializeAddon.serialize({ scrollback: 2000 }));
    } catch {
      // 终端已销毁时无需保留快照。
    }
  }
  resizeObserver?.disconnect();
  resizeSync.dispose();
  inputSubscription?.dispose();
  stopPasteGuard?.();
  stopPasteGuard = null;
  stopOutput?.();
  stopEnded?.();
  stopRecovered?.();
  window.removeEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  window.removeEventListener("focus", handleWindowFocus);
  window.removeEventListener(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, handleColorSchemeChange);
  stopBufferProbe?.();
  stopBufferProbe = null;
  predictiveEcho.dispose();
  pluginLinks?.dispose();
  pluginLinks = null;
  // Destroy xterm first to avoid rebuilding its DOM renderer during WebGL addon disposal.
  try {
    xterm?.dispose();
  } finally {
    releaseTerminalWebgl();
  }
  if (!props.keepAliveOnUnmount && isDesktopRuntime()) {
    deleteTerminalSnapshot(props.sessionId);
    void invokeBackend<void>("close_local_shell", {
      payload: { sessionId: props.sessionId },
    }).catch(() => {});
  }
});
</script>

<template>
  <section ref="terminalPane" class="terminal-pane" :class="{ 'is-active': active }" @mousedown="emit('select')">
    <el-drawer v-model="commandHistoryOpen" :title="t('ssh.history.title')" size="560px" append-to-body destroy-on-close>
      <SshCommandHistory v-if="commandHistoryOpen" :key="sessionId" :session-id="sessionId" :profile="profile" :ready="status === 'ready'" @fill="fillHistoryCommand" />
    </el-drawer>
    <header class="terminal-pane-toolbar">
      <div class="terminal-pane-toolbar__identity">
        <span class="terminal-pane-status" :class="`is-${status}`" />
        <strong>{{ terminalTitle }}</strong>
        <span>{{ statusLabel }}</span>
        <em v-if="profile.portForwards.length">
          {{ t("ssh.portForward.short") }} {{ profile.portForwards.length }}
        </em>
      </div>
      <div class="terminal-pane-toolbar__actions">
        <TerminalPaneAttention :pane-id="sessionId" />

        <el-tooltip
          :content="t(toolsExpanded ? 'terminal.collapseTools' : 'terminal.expandTools')"
          placement="bottom"
          :teleported="false"
        >
          <el-button text
            native-type="button"
            :aria-expanded="toolsExpanded"
            :aria-label="t(toolsExpanded ? 'terminal.collapseTools' : 'terminal.expandTools')"
            @click.stop="toolsExpanded = !toolsExpanded"
          >
            <el-icon><ArrowUp v-if="toolsExpanded" /><ArrowDown v-else /></el-icon>
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('terminal.splitRight')" placement="bottom" :teleported="false">
          <el-button text
            native-type="button"
            :disabled="!canSplit"
            :aria-label="t('terminal.splitRight')"
            @click.stop="emit('split', 'horizontal')"
          >
            ⇆
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('terminal.splitDown')" placement="bottom" :teleported="false">
          <el-button text
            native-type="button"
            :disabled="!canSplit"
            :aria-label="t('terminal.splitDown')"
            @click.stop="emit('split', 'vertical')"
          >
            ⇅
          </el-button>
        </el-tooltip>
        <el-tooltip
          :content="filePanelLabel"
          placement="bottom"
          :teleported="false"
        >
          <el-button text
            native-type="button"
            :disabled="
              !fileTransferEnabled ||
              (fileTransferProtocol === 'sftp' && status !== 'ready')
            "
            :class="{ 'is-active': sftpVisible }"
            :aria-pressed="sftpVisible"
            :aria-label="filePanelLabel"
            @click.stop="sftpVisible = !sftpVisible"
          >
            <el-icon><FolderOpened /></el-icon>
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('ssh.server.title')" placement="bottom" :teleported="false">
          <el-button
            text
            native-type="button"
            :disabled="status !== 'ready'"
            :class="{ 'is-active': serverManagerVisible }"
            :aria-pressed="serverManagerVisible"
            :aria-label="t('ssh.server.title')"
            @click.stop="serverManagerVisible = true"
          >
            <el-icon><Monitor /></el-icon>
          </el-button>
        </el-tooltip>
        <SshPortForwardControls
          :session-id="sessionId"
          :rules="profile.portForwards"
          :connection-ready="status === 'ready'"
          @configure="emit('configure')"
        />
        <template v-if="toolsExpanded">
          <el-tooltip :content="t('ssh.history.button')" placement="bottom" :teleported="false">
            <el-button
              text
              native-type="button"
              :aria-label="t('ssh.history.button')"
              :aria-expanded="commandHistoryOpen"
              @click.stop="commandHistoryOpen = true"
            >
              <el-icon><Clock /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('terminal.search')" placement="bottom" :teleported="false">
            <el-button text
              native-type="button"
              :class="{ 'is-active': searchOpen }"
              :aria-label="t('terminal.search')"
              @click.stop="openSearch"
            >
              <el-icon><Search /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip
            :content="t(recording ? 'terminal.stopRecording' : 'terminal.startRecording')"
            placement="bottom"
            :teleported="false"
          >
            <el-button text
              native-type="button"
              :class="{ 'is-recording': recording }"
              :aria-label="t(recording ? 'terminal.stopRecording' : 'terminal.startRecording')"
              @click.stop="recording ? void stopRecording() : startRecording()"
            >
              <el-icon><VideoCameraFilled v-if="recording" /><VideoCamera v-else /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('terminal.playback')" placement="bottom" :teleported="false">
            <el-button text
              native-type="button"
              :aria-label="t('terminal.playback')"
              @click.stop="playbackOpen = true"
            >
              <el-icon><VideoPlay /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('terminal.bookmarks')" placement="bottom" :teleported="false">
            <el-button text
              native-type="button"
              :aria-label="t('terminal.bookmarks')"
              @click.stop="bookmarksOpen = true"
            >
              <el-icon><Star /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('pluginContrib.commandSnippets.open')" placement="bottom" :teleported="false">
            <el-button text
              native-type="button"
              :aria-label="t('pluginContrib.commandSnippets.open')"
              @click.stop="snippetsOpen = true"
            >
              <el-icon><Tickets /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('ssh.editConnection')" placement="bottom" :teleported="false">
            <el-button text
              native-type="button"
              :aria-label="t('ssh.editConnection')"
              @click.stop="emit('configure')"
            >
              <el-icon><Edit /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('terminal.reload')" placement="bottom" :teleported="false">
            <el-button text native-type="button" :aria-label="t('terminal.reload')" @click.stop="emit('reload')">
              <el-icon><RefreshRight /></el-icon>
            </el-button>
          </el-tooltip>
        </template>
        <el-tooltip :content="closeTooltip" placement="bottom" :teleported="false">
          <!-- The wrapper keeps the tooltip on a disabled button. -->
          <span class="terminal-pane-close-wrap">
            <el-button text
              native-type="button"
              :disabled="!canClose"
              :aria-label="t('terminal.closePane')"
              :aria-description="!canClose && closeDisabledReason ? closeDisabledReason : undefined"
              @click.stop="emit('close')"
            >
              <el-icon><Close /></el-icon>
            </el-button>
          </span>
        </el-tooltip>
      </div>
    </header>
    <div
      v-if="status === 'error' || status === 'credentials' || status === 'unavailable'"
      class="ssh-terminal-state"
      :class="`is-${status}`"
      role="status"
      aria-live="polite"
      @mousedown.stop
    >
      <el-icon class="ssh-terminal-state__icon">
        <CircleCloseFilled v-if="status === 'error'" />
        <WarningFilled v-else />
      </el-icon>
      <strong>{{ statusLabel }}</strong>
      <p>{{ statusMessage }}</p>
      <code v-if="statusDetail">{{ statusDetail }}</code>
      <div
        v-if="status === 'error' || status === 'credentials'"
        class="ssh-terminal-state__actions"
      >
        <el-button
          v-if="status === 'error'"
          size="small"
          type="primary"
          @click="retryConnection"
        >
          {{ t("ssh.retryConnection") }}
        </el-button>
        <el-button
          size="small"
          :type="status === 'credentials' ? 'primary' : 'default'"
          @click="emit('configure')"
        >
          {{ t("common.edit") }}
        </el-button>
      </div>
    </div>
    <div v-if="searchOpen" class="terminal-search-bar" @mousedown.stop>
      <el-input
        ref="searchInput"
        v-model="searchQuery"
        :placeholder="t('terminal.searchPlaceholder')"
        @input="findInTerminal(true)"
        @keydown="handleSearchKeydown"
      />
      <span v-if="searchFound === false" class="terminal-search-bar__status">{{
        t("terminal.searchNoResults")
      }}</span>
      <el-button text
        native-type="button"
        :aria-label="t('terminal.searchPrevious')"
        @click="findInTerminal(false)"
      >
        <el-icon><ArrowLeftBold /></el-icon>
      </el-button>
      <el-button text native-type="button" :aria-label="t('terminal.searchNext')" @click="findInTerminal(true)">
        <el-icon><ArrowRightBold /></el-icon>
      </el-button>
      <el-button text native-type="button" :aria-label="t('terminal.closeSearch')" @click="closeSearch">
        <el-icon><Close /></el-icon>
      </el-button>
    </div>
    <AppSplitPane
      class="terminal-sftp-splitter"
      side="second"
      :model-value="sftpMaximized ? 100 : displayedSftpWidth"
      :unit="sftpMaximized ? 'percent' : 'px'"
      :collapsed="!sftpVisible"
      :resizable="sftpVisible && !sftpMaximized"
      :min="sftpMaximized ? 100 : SFTP_MIN_WIDTH"
      :max="sftpMaximized ? 100 : sftpMaximumWidth"
      :second-min="sftpMaximized ? 0 : TERMINAL_MIN_WIDTH"
      :aria-label="t('sftp.resizePanel')"
      @resize="updateSftpWidth"
      @resize-end="persistUpdatedSftpWidth"
    >
      <template #first>
        <div
          ref="terminalHost"
          class="terminal-pane-host"
          @contextmenu="openTerminalContextMenu"
        />
      </template>
      <template #second>
        <SshSftpPanel
          ref="sftpPanel"
          :visible="sftpVisible"
          :maximized="sftpMaximized"
          :class="{ 'is-maximized': sftpMaximized }"
          :ssh-session-id="sessionId"
          :profile="fileTransferProfile"
          :credentials="fileTransferCredentials"
          @close="closeSftpPanel"
          @toggle-maximize="toggleSftpMaximized"
        />
      </template>
    </AppSplitPane>
    <SshServerManagerPanel
      v-if="serverManagerMounted"
      v-model="serverManagerVisible"
      :session-id="sessionId"
    />
    <TerminalContextMenu
      :open="contextMenu.open"
      :x="contextMenu.x"
      :y="contextMenu.y"
      :selection="contextMenu.context.text"
      :token="contextMenu.context.token"
      :url="contextMenu.context.url"
      :path="contextMenu.context.path"
      :ready="status === 'ready'"
      :can-close="canClose"
      :can-split="canSplit"
      :can-reveal-in-sftp="canRevealRemotePath(contextMenu.context.path)"
      terminal-kind="ssh"
      @close="closeTerminalContextMenu"
      @action="handleContextMenuAction"
    />
    <TerminalPasteConfirmDialog
      v-if="pasteDialogMounted"
      v-model="pasteDialogOpen"
      :text="pendingPasteText"
      @confirm="pasteConfirmed"
    />
    <TerminalAiActionsDialog
      v-if="aiActionsMounted"
      v-model="aiActionsOpen"
      :context-text="terminalAiContextText(contextMenu.context)"
      terminal-kind="ssh"
    />
    <TerminalCommandSnippetsPanel
      v-if="snippetsMounted"
      v-model="snippetsOpen"
      terminal-kind="ssh"
      :ready="status === 'ready'"
      :bracketed-paste-mode="bracketedPasteMode"
      @insert="insertCommandSnippet"
    />
    <TerminalBookmarksDialog
      v-if="bookmarksMounted"
      v-model="bookmarksOpen"
      :initial-command="bookmarkInitialCommand"
      @run="runBookmarkedCommand"
    />
    <TerminalPlaybackDialog
      v-if="playbackMounted"
      v-model="playbackOpen"
      :recordings="recordings"
      @delete="deleteRecording"
      @rename="renameRecording"
    />
  </section>
</template>
