<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from "vue";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal, type IDisposable } from "@xterm/xterm";
import { ElMessage } from "element-plus";
import { CopyDocument, Select, Tickets, Upload } from "@element-plus/icons-vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import PluginServiceMenuItems from "@/components/plugins/PluginServiceMenuItems.vue";
import type { TerminalSelectionServiceTarget } from "@/domain/plugins/service-surfaces";
import { menuPassContext, terminalHostContext } from "@/domain/plugins/menus";
import "@xterm/xterm/css/xterm.css";
import { terminalOverlayStyle, usePredictiveEcho } from "@/composables/use-predictive-echo";
import { guardTerminalPasteEvents, useTerminalKeybindings } from "@/composables/use-terminal-keybindings";
import { analyzeTerminalPaste } from "@/domain/terminal/context-menu";
import { isTerminalReport } from "@/domain/terminal/terminal-attention";
import { useVueI18n } from "@/i18n/messages";
import { shellOutputEventName, type ShellEndedEvent, type ShellOutputEvent } from "@/domain/ssh/types";
import {
  createTerminalInputBatcher,
  isImmediateTerminalInput,
  type TerminalInputBatcher,
} from "@/domain/terminal/input-batcher";
import {
  resizeSshDockerExec,
  startSshDockerExec,
  startSshDockerLogStream,
  stopSshDockerStream,
  writeToSshDockerExec,
  type SshDockerTerminalMode,
} from "@/services/ssh-docker-service";
import {
  resolveTerminalTheme,
  setTerminalTheme,
  TERMINAL_COLOR_SCHEME_CHANGED_EVENT,
  type TerminalColorScheme,
} from "@/services/terminal-theme";
import {
  attachTerminalWebglAddon,
  readTerminalPreferences,
} from "@/services/terminal-preferences";
import {
  clampTerminalDimensions,
  fitTerminalSize,
  hasTerminalLayout,
  waitForTerminalLayout,
} from "@/services/terminal-viewport";
import { UI_THEME_CHANGED_EVENT, type UiTheme } from "@/services/ui-theme";
import { copyText, listenAppEvent, readText } from "@/services/desktop-bridge";
import { attachPluginTerminalLinks } from "@/services/terminal-plugin-links";
import type { TerminalWebLinkFailure } from "@/services/terminal-web-links";

const TerminalPasteConfirmDialog = defineAsyncComponent(
  () => import("@/components/terminal/TerminalPasteConfirmDialog.vue")
);
const TerminalCommandSnippetsPanel = defineAsyncComponent(
  () => import("@/components/terminal/TerminalCommandSnippetsPanel.vue")
);

const props = withDefaults(
  defineProps<{
    parentSessionId: string;
    sessionId: string;
    containerId: string;
    mode: SshDockerTerminalMode;
    shell?: string;
    tail?: number;
  }>(),
  {
    shell: "sh",
    tail: 200,
  }
);

const emit = defineEmits<{
  ended: [];
  failed: [message: string];
}>();

const { t } = useVueI18n();
const terminalHost = ref<HTMLElement | null>(null);
const pasteDialogOpen = ref(false);
const pasteDialogMounted = ref(false);
const pendingPasteText = ref("");
watch(pasteDialogOpen, (open) => {
  if (open) pasteDialogMounted.value = true;
});
const snippetsOpen = ref(false);
const snippetsMounted = ref(false);
const streamReady = ref(false);
watch(snippetsOpen, (open) => {
  if (open) snippetsMounted.value = true;
});
let xterm: Terminal | null = null;
let pluginLinks: IDisposable | null = null;
let fitAddon: FitAddon | null = null;
let webglAddonHandle: IDisposable | null = null;
let resizeObserver: ResizeObserver | null = null;
let inputSubscription: IDisposable | null = null;
let resizeSubscription: IDisposable | null = null;
let stopOutputListener: (() => void) | null = null;
let stopEndedListener: (() => void) | null = null;
let stopPasteGuard: (() => void) | null = null;
let started = false;
let ended = false;
let closed = false;
let inputBatcher: TerminalInputBatcher | undefined;

// 预测回显只用于 exec（logs 模式没有输入，组件不调用 input/write/reset）。
const predictiveEcho = usePredictiveEcho({
  terminal: () => xterm ?? undefined,
  screen: () => terminalHost.value?.querySelector<HTMLElement>(".xterm-screen") ?? null,
  enabled: () => props.mode === "exec" && streamReady.value && !closed && !ended,
  style: () => terminalOverlayStyle(xterm),
});

function resetPrediction() {
  if (props.mode === "exec") predictiveEcho.reset();
}

function fitTerminal() {
  if (!xterm || !fitAddon || closed) return;
  // 抽屉展开动画期间容器宽度为 0，此时 fit() 不会失败，只会把终端压到 2 列 ——
  // clamp 后变成 20，再送进 exec 会话就是永久的 20 列硬换行。等下一次
  // ResizeObserver 拿到真实宽度再量。
  if (!hasTerminalLayout(terminalHost.value)) return;
  try {
    // Never below the usable minimum: the exec session is sized from xterm.cols/rows.
    fitTerminalSize(xterm, fitAddon);
  } catch {
    // 尺寸尚未稳定，等待下一次 ResizeObserver。
  }
}

function handleThemeChange(event: Event) {
  if (!xterm) return;
  setTerminalTheme(xterm, resolveTerminalTheme((event as CustomEvent<UiTheme>).detail));
  resetPrediction();
}

function handleColorSchemeChange(event: Event) {
  if (xterm) {
    setTerminalTheme(
      xterm,
      resolveTerminalTheme(undefined, (event as CustomEvent<TerminalColorScheme>).detail)
    );
    resetPrediction();
  }
}

function writeError(message: string) {
  // The status line moves the cursor: predictions on screen are cleared first.
  resetPrediction();
  xterm?.writeln(`\r\n\x1b[31m[MinCode] ${message}\x1b[0m`);
}

function writeInputError(error: unknown) {
  writeError(error instanceof Error ? error.message : String(error));
}

async function writeExecInput(input: string) {
  if (!started || closed) return;
  await writeToSshDockerExec(props.sessionId, input);
}

function queueExecInput(input: string) {
  inputBatcher ??= createTerminalInputBatcher(writeExecInput, {
    delayMs: 8,
    maxChars: 256,
    onError: writeInputError,
  });
  return inputBatcher.push(input, isImmediateTerminalInput(input));
}

/** Guarded paste: multi-line or risky text needs confirmation before it reaches the container shell. */
function pasteGuarded(text: string) {
  if (!text || props.mode !== "exec") return;
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

function pasteConfirmed(text: string) {
  predictiveEcho.paste(text);
  xterm?.focus();
}

function bracketedPasteMode() {
  return xterm?.modes.bracketedPasteMode === true;
}

/** Command snippet from the drawer: already planned (no "\r", no trailing newline; multi-line only in bracketed paste). */
function insertCommandSnippet(text: string) {
  if (props.mode !== "exec" || !started || closed || !text) return;
  predictiveEcho.paste(text);
  xterm?.focus();
}

function handleWebLinkFailure(failure: TerminalWebLinkFailure) {
  const message = failure === "desktop-required" ? t("terminal.linkDesktopOnly") : t("terminal.openLinkFailed");
  ElMessage.error({ message, grouping: true, showClose: true });
}

async function copyTerminalSelection() {
  const text = xterm?.getSelection() ?? "";
  if (!text) return;
  try {
    await copyText(text);
  } catch {
    ElMessage.error({ message: t("terminal.context.clipboardFailed"), grouping: true });
  }
}

// Minimal context menu: copy / paste (exec only) / select all, plus plugin
// items (contributes.menus terminal/context with terminal.kind "docker") and
// plugin services on the selection.
const contextMenu = ref<{ x: number; y: number; selection: string } | null>(null);
const pluginMenuHost = computed(() =>
  contextMenu.value ? terminalHostContext({ kind: "docker", selection: contextMenu.value.selection }) : null,
);

function pluginMenuContext() {
  return menuPassContext("terminal/context", { kind: "docker", selection: contextMenu.value?.selection ?? "" });
}

// Plugin services (surface terminalSelection): the explicit selection only.
function serviceTarget(): TerminalSelectionServiceTarget | null {
  const selection = contextMenu.value?.selection;
  return selection ? { surface: "terminalSelection", kind: "docker", selection } : null;
}

function openContextMenu(event: MouseEvent) {
  if (!xterm) return;
  event.preventDefault();
  contextMenu.value = { x: event.clientX, y: event.clientY, selection: xterm.getSelection() };
}

function closeContextMenu() {
  contextMenu.value = null;
}

// A KeepAlive'd view leaving must not keep the menu (and its targets) open.
onDeactivated(closeContextMenu);

function contextMenuCopy() {
  closeContextMenu();
  void copyTerminalSelection();
}

function contextMenuPaste() {
  closeContextMenu();
  void requestPaste();
}

function contextMenuSnippets() {
  closeContextMenu();
  snippetsOpen.value = true;
}

function contextMenuSelectAll() {
  closeContextMenu();
  xterm?.selectAll();
}

// No search UI here, so the find key reaches the shell; logs mode has no stdin to paste into.
const handleTerminalKey = useTerminalKeybindings({
  xterm: () => xterm,
  copy: copyTerminalSelection,
  ...(props.mode === "exec" ? { paste: requestPaste } : {}),
  clear: () => xterm?.clear(),
  selectAll: () => xterm?.selectAll(),
});

async function startStream() {
  if (!xterm) return;
  const dimensions = clampTerminalDimensions(xterm.cols, xterm.rows);
  if (props.mode === "exec") {
    await startSshDockerExec({
      parentSessionId: props.parentSessionId,
      sessionId: props.sessionId,
      containerId: props.containerId,
      shell: props.shell,
      ...dimensions,
    });
  } else {
    await startSshDockerLogStream({
      parentSessionId: props.parentSessionId,
      sessionId: props.sessionId,
      containerId: props.containerId,
      tail: props.tail,
    });
  }
  if (closed) {
    if (!ended) await stopSshDockerStream(props.sessionId).catch(() => {});
    return;
  }
  if (!ended) {
    started = true;
    streamReady.value = true;
  }
}

onMounted(async () => {
  if (!terminalHost.value) return;
  xterm = new Terminal({
    cursorBlink: props.mode === "exec",
    cursorStyle: "bar",
    disableStdin: props.mode === "logs",
    fontFamily: '"SFMono-Regular", Consolas, "Liberation Mono", monospace',
    fontSize: 13,
    lineHeight: 1.25,
    scrollback: readTerminalPreferences().scrollback,
  });
  fitAddon = new FitAddon();
  xterm.loadAddon(fitAddon);
  xterm.open(terminalHost.value);
  setTerminalTheme(xterm, resolveTerminalTheme());
  pluginLinks = attachPluginTerminalLinks(xterm, {
    kind: "docker",
    host: terminalHost.value,
    onWebLinkFailure: handleWebLinkFailure,
  });
  xterm.attachCustomKeyEventHandler(handleTerminalKey);
  stopPasteGuard = guardTerminalPasteEvents(terminalHost.value, pasteGuarded);
  // Renderer metrics changed once WebGL settles: the overlay measures its cells again.
  webglAddonHandle = attachTerminalWebglAddon(xterm, resetPrediction) ?? null;
  window.addEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  window.addEventListener(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, handleColorSchemeChange);

  stopOutputListener = listenAppEvent<ShellOutputEvent>(shellOutputEventName(props.sessionId), (payload) => {
    if (props.mode === "exec") predictiveEcho.write(payload.data);
    else xterm?.write(payload.data);
  });
  stopEndedListener = listenAppEvent<ShellEndedEvent>("shell-session-ended", (payload) => {
    if (payload.sessionId !== props.sessionId || closed) return;
    ended = true;
    started = false;
    streamReady.value = false;
    resetPrediction();
    xterm?.writeln(`\r\n\x1b[90m[MinCode] ${t("ssh.server.dockerStreamEnded")}\x1b[0m`);
    emit("ended");
  });

  resizeObserver = new ResizeObserver(fitTerminal);
  resizeObserver.observe(terminalHost.value);
  if (props.mode === "exec") {
    inputSubscription = xterm.onData((data) => {
      if (!started || closed) return;
      // 终端自动应答（CPR、DA、焦点、鼠标报告）不是按键，不能清空预测和首字标记
      if (!isTerminalReport(data)) predictiveEcho.input(data);
      void queueExecInput(data).catch(writeInputError);
    });
    resizeSubscription = xterm.onResize(({ cols, rows }) => {
      if (!started || closed) return;
      if (!hasTerminalLayout(terminalHost.value)) return;
      const dimensions = clampTerminalDimensions(cols, rows);
      void resizeSshDockerExec(props.sessionId, dimensions.cols, dimensions.rows).catch(() => {});
    });
  }

  await nextTick();
  // nextTick 只等到微任务，抽屉此刻仍在展开、容器仍是 0 宽。必须等到真实布局再量，
  // 否则 exec 会话会以 20 列诞生，输出的硬换行再也回不去。
  await waitForTerminalLayout(terminalHost.value);
  if (closed) return;
  fitTerminal();
  try {
    await startStream();
    if (props.mode === "exec") xterm.focus();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    writeError(message);
    emit("failed", message);
  }
});

onBeforeUnmount(() => {
  closed = true;
  contextMenu.value = null;
  inputBatcher?.dispose();
  inputBatcher = undefined;
  if (started) void stopSshDockerStream(props.sessionId).catch(() => {});
  resizeObserver?.disconnect();
  resizeObserver = null;
  stopPasteGuard?.();
  stopPasteGuard = null;
  inputSubscription?.dispose();
  inputSubscription = null;
  resizeSubscription?.dispose();
  resizeSubscription = null;
  stopOutputListener?.();
  stopOutputListener = null;
  stopEndedListener?.();
  stopEndedListener = null;
  window.removeEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  window.removeEventListener(TERMINAL_COLOR_SCHEME_CHANGED_EVENT, handleColorSchemeChange);
  predictiveEcho.dispose();
  webglAddonHandle?.dispose();
  webglAddonHandle = null;
  pluginLinks?.dispose();
  pluginLinks = null;
  xterm?.dispose();
  xterm = null;
  fitAddon = null;
});
</script>

<template>
  <div ref="terminalHost" class="ssh-docker-terminal" @contextmenu="openContextMenu">
    <AppContextMenu
      v-if="contextMenu"
      :open="true"
      :x="contextMenu.x"
      :y="contextMenu.y"
      :width="218"
      @close="closeContextMenu"
    >
      <el-dropdown-item :disabled="!contextMenu.selection" @click="contextMenuCopy">
        <el-icon><CopyDocument /></el-icon>{{ t("terminal.context.copy") }}
      </el-dropdown-item>
      <el-dropdown-item v-if="props.mode === 'exec'" @click="contextMenuPaste">
        <el-icon><Upload /></el-icon>{{ t("terminal.context.paste") }}
      </el-dropdown-item>
      <el-dropdown-item v-if="props.mode === 'exec'" :disabled="!streamReady" @click="contextMenuSnippets">
        <el-icon><Tickets /></el-icon>{{ t("pluginContrib.commandSnippets.menu") }}
      </el-dropdown-item>
      <el-dropdown-item @click="contextMenuSelectAll">
        <el-icon><Select /></el-icon>{{ t("terminal.context.selectAll") }}
      </el-dropdown-item>
      <PluginMenuItems
        menu="terminal/context"
        :host="pluginMenuHost"
        :context="pluginMenuContext"
        @run="closeContextMenu"
      />
      <PluginServiceMenuItems :target="serviceTarget" @run="closeContextMenu" />
    </AppContextMenu>
    <TerminalPasteConfirmDialog
      v-if="pasteDialogMounted"
      v-model="pasteDialogOpen"
      :text="pendingPasteText"
      @confirm="pasteConfirmed"
    />
    <TerminalCommandSnippetsPanel
      v-if="snippetsMounted && props.mode === 'exec'"
      v-model="snippetsOpen"
      terminal-kind="docker"
      :ready="streamReady"
      :bracketed-paste-mode="bracketedPasteMode"
      @insert="insertCommandSnippet"
    />
  </div>
</template>
