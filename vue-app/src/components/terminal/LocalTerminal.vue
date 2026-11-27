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
  Close,
  RefreshRight,
  Search,
  Star,
  VideoCamera,
  VideoCameraFilled,
  VideoPlay,
} from "@element-plus/icons-vue";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { SerializeAddon } from "@xterm/addon-serialize";
import { Terminal, type IDisposable } from "@xterm/xterm";
import { ElMessage, ElMessageBox } from "element-plus";
import "@xterm/xterm/css/xterm.css";
import { prepareAgentControlTerminalCommand } from "@/services/agent-control-panes";
import { pendingAgentControlTerminal, startAgentControlShell } from "@/services/agent-control-terminal-launch";
import TerminalContextMenu from "@/components/terminal/TerminalContextMenu.vue";
import {
  analyzeTerminalPaste,
  terminalAiContextText,
  terminalTextAsMarkdown,
  type TerminalContextMenuAction,
  type TerminalTextContext,
} from "@/domain/terminal/context-menu";
import type { LocalShellType, TerminalSplitDirection } from "@/domain/terminal/types";
import { useTerminalAttentionStore } from "@/stores/terminal-attention";
import { isTerminalReport } from "@/domain/terminal/terminal-attention";
import TerminalPaneAttention from "@/components/terminal/TerminalPaneAttention.vue";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import {
  deleteTerminalSnapshot,
  readTerminalSnapshot,
  saveTerminalSnapshot,
} from "@/services/terminal-session-cache";
import { resolveTerminalPointerContext } from "@/services/terminal-context-menu";
import { alternateScreenWheelInput } from "@/domain/terminal/alternate-screen-wheel";
import { registerTerminalBufferReader, registerTerminalSizeReader } from "@/services/e2e-terminal-buffer-probe";
import {
  attachTerminalWebglAddon,
  readTerminalPreferences,
} from "@/services/terminal-preferences";
import { createTerminalWebLinksAddon, type TerminalWebLinkFailure } from "@/services/terminal-web-links";
import { copyText, isDesktopRuntime, invokeBackend, listenAppEvent, readText } from "@/services/desktop-bridge";
import {
  createTerminalInputBatcher,
  isImmediateTerminalInput,
  type TerminalInputBatcher,
} from "@/domain/terminal/input-batcher";
import { createTerminalResizeSync } from "@/domain/terminal/resize-sync";
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
  clampTerminalDimensions,
  fitTerminalSize,
  hasTerminalLayout,
  waitForTerminalLayout,
} from "@/services/terminal-viewport";
import { UI_THEME_CHANGED_EVENT, type UiTheme } from "@/services/ui-theme";
import { guardTerminalPasteEvents, useTerminalKeybindings } from "@/composables/use-terminal-keybindings";
import { openSystemBrowser } from "@/services/web-automation-service";
import { shellOutputEventName, type ShellOutputEvent } from "@/domain/ssh/types";
import { getUserHomeDirectory } from "@/services/settings-service";

// 低频弹窗按需加载，避免进入终端首屏 chunk。
const TerminalBookmarksDialog = defineAsyncComponent(
  () => import("@/components/terminal/TerminalBookmarksDialog.vue")
);
const TerminalAiActionsDialog = defineAsyncComponent(
  () => import("@/components/terminal/TerminalAiActionsDialog.vue")
);
const TerminalPasteConfirmDialog = defineAsyncComponent(
  () => import("@/components/terminal/TerminalPasteConfirmDialog.vue")
);
const TerminalPlaybackDialog = defineAsyncComponent(
  () => import("@/components/terminal/TerminalPlaybackDialog.vue")
);

interface ShellEndedEvent {
  sessionId: string;
  reason: string;
  /** 后端标记为可恢复时会话还会重连，不算失败。 */
  recoverable: boolean;
}

export interface LocalTerminalActivity {
  phase: "connecting" | "active" | "quiet" | "completed" | "failed";
  summary?: string;
  updatedAt: number;
  lastOutputAt?: number;
  endedAt?: number;
}

const props = withDefaults(
  defineProps<{
    sessionId: string;
    shellType?: LocalShellType;
    customPath?: string;
    initialCwd?: string;
    initialCommand?: string;
    extraEnv?: Record<string, string>;
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
    shellType: "system",
    customPath: undefined,
    initialCwd: undefined,
    initialCommand: undefined,
    extraEnv: undefined,
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
  split: [direction: TerminalSplitDirection];
  activity: [snapshot: LocalTerminalActivity];
}>();

const { locale, t } = useVueI18n();
const closeTooltip = computed(() =>
  !props.canClose && props.closeDisabledReason ? props.closeDisabledReason : t("terminal.closePane")
);
const terminalAttention = useTerminalAttentionStore();
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
const pendingPasteText = ref("");

// 低频弹窗只在首次打开时拉取 chunk；之后保持挂载以保留关闭过渡与内部状态。
function useOnceOpened(source: Ref<boolean>) {
  const opened = ref(false);
  watch(source, (value) => {
    if (value) opened.value = true;
  });
  return opened;
}
const pasteDialogMounted = useOnceOpened(pasteDialogOpen);
const aiActionsMounted = useOnceOpened(aiActionsOpen);
const bookmarksMounted = useOnceOpened(bookmarksOpen);
const playbackMounted = useOnceOpened(playbackOpen);
const contextMenu = ref({
  open: false,
  x: 0,
  y: 0,
  context: { text: "", line: "", token: "" } as TerminalTextContext,
});
const recording = ref(false);
const status = ref<"connecting" | "ready" | "unavailable" | "ended" | "error">("connecting");
const statusLabel = computed(() => t(`terminal.status.${status.value}` as MessageKey));


let xterm: Terminal | null = null;
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
/** E2E-only buffer reader (a no-op outside `-tags e2e` smoke runs). */
let stopBufferProbe: (() => void) | null = null;
let closed = false;
let backendReady = false;
// 本地终端的输入和回显都走有确认的 IPC/事件通道，避免 Stream 传输层
// 在快速输入时只报告“已入队”而导致 PTY 回显缺字符。
let inputBatcher: TerminalInputBatcher | undefined;
let recordingFrames: TerminalRecordingFrame[] = [];
let recordingStartedAt = 0;
let lastActivityEmitAt = 0;
let quietActivityTimer: number | undefined;

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

// PTY 尺寸只经由这里同步：拖动分栏时按帧合并 fit，且仅在行列真正变化时发送。
const resizeSync = createTerminalResizeSync({
  measure() {
    if (!xterm || !fitAddon || closed || deactivated || !props.visible) return null;
    // 容器还没有宽高时 FitAddon 不会失败，只会返回它自己的下限 2 列，clamp 后变成
    // 20 —— 那个假尺寸一旦送进 PTY，输出就按 20 列硬换行写死在 scrollback 里。
    // 宁可跳过：容器拿到宽度后 ResizeObserver 会再来一次。
    if (!hasTerminalLayout(terminalHost.value)) return null;
    // Fit with the WebGL renderer the PTY size was taken with (see ensureFitRenderer).
    ensureFitRenderer();
    if (webglLoading && backendReady) return null;
    try {
      // Screen and PTY get the same size; a sliver of a container changes neither.
      return fitTerminalSize(xterm, fitAddon);
    } catch {
      // 容器隐藏或尺寸尚未稳定时忽略，下一次 ResizeObserver 会重试。
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
  const recorded = createTerminalRecording(
    recordingFrames,
    t("terminal.localShell")
  );
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

// 终端输入必须由有返回结果的 IPC 调用提交。Stream 的 socket.send 只能表示
// 浏览器接受了帧，不能证明后端已将字节写入 PTY；快速输入时可能静默丢字。
async function writeShellInput(input: string) {
  if (!input || closed || !isDesktopRuntime()) return;
  await invokeBackend<void>("write_to_shell", {
    payload: { sessionId: props.sessionId, input },
  });
}

function queueShellInput(input: string, immediate = false) {
  if (!inputBatcher) {
    inputBatcher = createTerminalInputBatcher(writeShellInput, {
      delayMs: 8,
      maxChars: 256,
      onError: () => xterm?.writeln(`\r\n\x1b[31m[MinCode] ${t("terminal.inputFailed")}\x1b[0m`),
    });
  }
  return inputBatcher.push(input, immediate);
}

async function sendTerminalControl(input: string) {
  if (closed || status.value !== "ready" || !isDesktopRuntime()) return;
  try {
    await queueShellInput(input, true);
  } catch {
    xterm?.writeln(`\r\n\x1b[31m[MinCode] ${t("terminal.inputFailed")}\x1b[0m`);
  }
}

async function runBookmarkedCommand(command: string) {
  if (!command.trim() || closed || !isDesktopRuntime()) return;
  try {
    await queueShellInput(`${command.trim()}\r`, true);
    focusTerminal();
  } catch {
    xterm?.writeln(`\r\n\x1b[31m[MinCode] ${t("terminal.inputFailed")}\x1b[0m`);
  }
}

function renderUnavailableMessage() {
  if (!xterm || isDesktopRuntime() || status.value !== "unavailable") return;
  xterm.reset();
  xterm.writeln(`\x1b[33m[MinCode] ${t("terminal.onlyDesktop")}\x1b[0m`);
}

function handleThemeChange(event: Event) {
  if (xterm) setTerminalTheme(xterm, resolveTerminalTheme((event as CustomEvent<UiTheme>).detail));
}

function handleColorSchemeChange(event: Event) {
  if (xterm) {
    setTerminalTheme(
      xterm,
      resolveTerminalTheme(undefined, (event as CustomEvent<TerminalColorScheme>).detail)
    );
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
  xterm?.paste(analysis.text);
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
  xterm?.paste(text);
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

async function handleContextMenuAction(action: TerminalContextMenuAction) {
  const context = contextMenu.value.context;
  switch (action) {
    case "copy":
      await copyContextText(context.text);
      return;
    case "paste":
      await requestPaste();
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
    case "bookmark":
      bookmarkInitialCommand.value = context.text.trim();
      bookmarksOpen.value = true;
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
      reloadPane();
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
watch(locale, renderUnavailableMessage);
onActivated(() => {
  deactivated = false;
  syncTerminalRenderActivity(props.active);
});
onDeactivated(() => {
  deactivated = true;
  syncTerminalRenderActivity(false, true);
});

// 输出统一入口：事件通道与流通道共用，避免录制/尾部截取逻辑写两份。
function handleShellOutput(data: string) {
  if (closed) return;
  appendRecordingFrame(data);
  xterm?.write(data);
  emitOutputActivity();
}

function applyShellEnded(event: ShellEndedEvent) {
  status.value = event.recoverable ? "connecting" : "ended";
  clearQuietActivityTimer();
  const endedAt = Date.now();
  emitActivity({
    phase: event.recoverable ? "active" : "completed",
    summary: event.reason,
    updatedAt: endedAt,
    ...(event.recoverable ? {} : { endedAt }),
  });
  if (!event.recoverable) deleteTerminalSnapshot(props.sessionId);
  xterm?.writeln(`\r\n\x1b[33m[MinCode] ${t("terminal.sessionEnded")}\x1b[0m`);
}

function reloadPane() {
  emit("reload");
}

onMounted(async () => {
  emitActivity({ phase: "connecting", updatedAt: Date.now() });
  if (!terminalHost.value) return;

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
  const bufferTerminal = xterm;
  stopBufferProbe = registerTerminalBufferReader(props.sessionId, () => {
    const buffer = bufferTerminal.buffer.active;
    const lines: string[] = [];
    for (let index = 0; index < buffer.length; index += 1) lines.push(buffer.getLine(index)?.translateToString(true) ?? "");
    return lines.join("\n");
  });
  const bufferFit = fitAddon;
  const stopSizeProbe = registerTerminalSizeReader(props.sessionId, () => {
    let proposed: { cols: number; rows: number } | null;
    try {
      const value = bufferFit.proposeDimensions();
      proposed = value && Number.isFinite(value.cols) ? { cols: value.cols, rows: value.rows } : null;
    } catch {
      proposed = null;
    }
    return { cols: bufferTerminal.cols, rows: bufferTerminal.rows, proposed };
  });
  const stopTextProbe = stopBufferProbe;
  stopBufferProbe = () => {
    stopTextProbe();
    stopSizeProbe();
  };
  window.addEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  window.addEventListener("focus", handleWindowFocus);
  window.addEventListener(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, handleColorSchemeChange);

  const snapshot = readTerminalSnapshot(props.sessionId);
  if (snapshot) xterm.write(snapshot);
  fitAndResize();

  if (!isDesktopRuntime()) {
    status.value = "unavailable";
    renderUnavailableMessage();
    return;
  }


  // 必须先订阅输出，再创建或重新附加 PTY，避免首个 shell prompt 丢失。
  stopOutput = listenAppEvent<ShellOutputEvent>(shellOutputEventName(props.sessionId), (event) => {
    handleShellOutput(event.data);
  });
  stopEnded = listenAppEvent<ShellEndedEvent>("shell-session-ended", (event) => {
    if (event.sessionId !== props.sessionId || closed) return;
    applyShellEnded(event);
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
    if (closed) return;
    // Typing into the pane acknowledges its terminal attention (tab bell).
    if (!isTerminalReport(input) && terminalAttention.items.has(props.sessionId)) terminalAttention.dismiss([props.sessionId]);
    void queueShellInput(input, isImmediateTerminalInput(input)).catch(() => {
      xterm?.writeln(`\r\n\x1b[31m[MinCode] ${t("terminal.inputFailed")}\x1b[0m`);
    });
  });

  // 分栏面板首帧的 flex-basis 是 0，而子组件的 onMounted 早于父组件的首次测量。
  // 必须等到真实布局再量一次，否则 PTY 会以 20 列诞生，initialCommand 的输出就
  // 永久按 20 列换行。这一步必须早于下面的 initialCommand 写入。
  await waitForTerminalLayout(terminalHost.value);
  if (closed) return;
  fitAndResize();

  await connectShell();
});

async function startShellSession(cols: number, rows: number, extraEnv = props.extraEnv) {
  const cwd = props.initialCwd || await getUserHomeDirectory();
  if (closed) return "";
  return invokeBackend<string>("start_local_shell", {
    payload: {
      sessionId: props.sessionId,
      shellType: props.shellType,
      customPath: props.customPath,
      cwd,
      extraEnv,
      attachIfExists: true,
      cols,
      rows,
    },
  });
}

async function connectShell() {
  if (!xterm || closed) return;
  const controlled = pendingAgentControlTerminal(props.sessionId);
  try {
    const dimensions = clampTerminalDimensions(xterm.cols, xterm.rows);
    const prepared = controlled ? await prepareAgentControlTerminalCommand(props.sessionId, controlled.command, controlled.signal) : undefined;
    const extraEnv = prepared?.extraEnv ? { ...props.extraEnv, ...prepared.extraEnv } : props.extraEnv;
    controlled?.signal.throwIfAborted();
    const start = () => startShellSession(dimensions.cols, dimensions.rows, extraEnv);
    const message = controlled
      ? await startAgentControlShell(controlled.signal, start, () => invokeBackend("close_local_shell", {
          payload: { sessionId: props.sessionId },
        }), () => closed)
      : await start();
    if (closed || !xterm) return;
    resizeSync.reset();
    backendReady = true;
    status.value = "ready";
    emitActivity({ phase: "quiet", updatedAt: Date.now() });
    // 本地终端输入通过有确认的 IPC 调用提交，回显固定走 shell-output 事件。
    // 不启用 Streams 数据面，避免传输层排队成功但 WebView 回显丢帧。
    const attached = message === "Shell attached";
    if (!attached) {
      xterm.writeln(`\x1b[32m[MinCode] ${message}\x1b[0m`);
      const command = controlled ? prepared?.command : props.initialCommand;
      if (command?.trim()) {
        await invokeBackend<void>("write_to_shell", {
          payload: { sessionId: props.sessionId, input: `${command}\r` },
        }, controlled ? { signal: controlled.signal } : undefined);
      }
    }
    controlled?.started();
    fitAndResize();
    focusTerminal();
  } catch (error) {
    controlled?.failed(error);
    status.value = "error";
    const endedAt = Date.now();
    emitActivity({ phase: "failed", updatedAt: endedAt, endedAt });
    xterm?.writeln(`\x1b[31m[MinCode] ${t("terminal.startFailed")}\x1b[0m`);
  }
}

onBeforeUnmount(() => {
  pendingAgentControlTerminal(props.sessionId)?.failed(new Error(t("settings.agentControl.paneStartFailed")));
  closed = true;
  backendReady = false;
  clearQuietActivityTimer();
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
  stopBufferProbe?.();
  stopBufferProbe = null;
  stopOutput?.();
  stopEnded?.();
  window.removeEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  window.removeEventListener("focus", handleWindowFocus);
  window.removeEventListener(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, handleColorSchemeChange);
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
  <section class="terminal-pane" :class="{ 'is-active': active }" @mousedown="emit('select')">
    <header class="terminal-pane-toolbar">
      <div class="terminal-pane-toolbar__identity">
        <span class="terminal-pane-status" :class="`is-${status}`" />
        <strong>{{ shellType === "system" ? t("terminal.localShell") : shellType }}</strong>
        <span>{{ statusLabel }}</span>
      </div>
      <div class="terminal-pane-toolbar__actions">
        <TerminalPaneAttention :pane-id="sessionId" />
        <el-tooltip
          :content="t(toolsExpanded ? 'terminal.collapseTools' : 'terminal.expandTools')"
          placement="bottom"
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
        <el-tooltip :content="t('terminal.splitRight')" placement="bottom">
          <el-button text
            native-type="button"
            :disabled="!canSplit"
            :aria-label="t('terminal.splitRight')"
            @click.stop="emit('split', 'horizontal')"
          >
            ⇆
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('terminal.splitDown')" placement="bottom">
          <el-button text
            native-type="button"
            :disabled="!canSplit"
            :aria-label="t('terminal.splitDown')"
            @click.stop="emit('split', 'vertical')"
          >
            ⇅
          </el-button>
        </el-tooltip>
        <template v-if="toolsExpanded">
          <el-tooltip :content="t('terminal.search')" placement="bottom">
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
          <el-tooltip :content="t('terminal.playback')" placement="bottom">
            <el-button text
              native-type="button"
              :aria-label="t('terminal.playback')"
              @click.stop="playbackOpen = true"
            >
              <el-icon><VideoPlay /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('terminal.bookmarks')" placement="bottom">
            <el-button text
              native-type="button"
              :aria-label="t('terminal.bookmarks')"
              @click.stop="bookmarksOpen = true"
            >
              <el-icon><Star /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('terminal.reload')" placement="bottom">
            <el-button text native-type="button" :aria-label="t('terminal.reload')" @click.stop="reloadPane">
              <el-icon><RefreshRight /></el-icon>
            </el-button>
          </el-tooltip>
        </template>
        <el-tooltip :content="closeTooltip" placement="bottom">
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
    <div
      ref="terminalHost"
      class="terminal-pane-host"
      @contextmenu="openTerminalContextMenu"
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
      terminal-kind="local"
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
      terminal-kind="local"
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

<style scoped>
.terminal-pane-profile-badge {
  flex-shrink: 0;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.terminal-pane-profile-block {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 12px;
  border-bottom: 1px solid var(--app-border);
  background: var(--app-surface);
}

.terminal-pane-profile-block__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.terminal-pane-profile-block__actions .el-button + .el-button {
  margin-left: 0;
}
</style>
