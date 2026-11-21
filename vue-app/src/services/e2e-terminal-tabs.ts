// Loaded only by the native -tags e2e smoke runner. Drives terminal tabs inside one
// terminal workbench tab through the real tab strip:
//   1. "+" opens a second terminal tab with its own live shell; a command runs in it;
//   2. switching to the first tab and back keeps the second tab's shell (same PID, no
//      session end, same pane element, one "Shell ready" line), its screen (the command
//      output is still in the xterm buffer) and its PTY size: a hidden tab must not be
//      resized to a sliver (the 20-column clamp floor) while it is off screen;
//   3. × on the second tab ends its shell and leaves one tab;
//   4. × on the last tab closes the workbench tab and ends that shell too.
import { ref, watch } from "vue";
import router from "@/router";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { plainTerminalText, type SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { shellOutputEventName, type ShellEndedEvent, type ShellOutputEvent } from "@/domain/ssh/types";
import { enableTerminalBufferProbe, readTerminalBuffer } from "@/services/e2e-terminal-buffer-probe";
import { invokeBackend, listenAppEvent } from "@/services/desktop-bridge";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";
import { verifyTerminalRendererSwitch } from "../../tests/native/terminal-rendering";

// Event-driven waits for reactive state (a background WebView throttles timers).
function waitFor<T>(read: () => T | undefined | false | null, label: string, timeoutMs: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => {
      stop();
      reject(new Error(`terminal-tabs smoke timed out: ${label}`));
    }, timeoutMs);
    // The callback may run during watch() itself, so stopping waits for a microtask.
    const stop = watch(read, (value) => {
      if (!value) return;
      clearTimeout(timer);
      queueMicrotask(() => stop());
      resolve(value);
    }, { immediate: true, flush: "sync" });
  });
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/** DOM is not reactive: poll it. */
async function untilDom<T>(read: () => T | undefined | false | null, label: string, timeoutMs: number): Promise<T> {
  const deadline = performance.now() + timeoutMs;
  while (performance.now() < deadline) {
    const value = read();
    if (value) return value;
    await sleep(50);
  }
  throw new Error(`terminal-tabs smoke timed out: ${label}`);
}

/**
 * Lets the layout settle (ResizeObserver, fit, the frame-batched PTY resize). Not
 * requestAnimationFrame: an occluded macOS window stops delivering frames entirely.
 */
const settle = () => sleep(500);

/** One pane's shell output and end event for the whole scenario. */
function captureShell(sessionId: string) {
  const output = ref("");
  const ended = ref(false);
  const stops = [
    listenAppEvent<ShellOutputEvent>(shellOutputEventName(sessionId), (event) => {
      if (event?.sessionId && event.sessionId !== sessionId) return;
      output.value += typeof event?.data === "string" ? event.data : "";
    }),
    listenAppEvent<ShellEndedEvent>("shell-session-ended", (event) => {
      if (event?.sessionId === sessionId) ended.value = true;
    }),
  ];
  return {
    sessionId,
    output,
    ended,
    text: () => plainTerminalText(output.value),
    dispose: () => stops.forEach((stop) => stop()),
  };
}

type Shell = ReturnType<typeof captureShell>;

async function alive(sessionId: string) {
  return invokeBackend<boolean>("shell_session_alive", { payload: { sessionId } }).catch(() => false);
}

function write(shell: Shell, line: string) {
  return invokeBackend<void>("write_to_shell", { payload: { sessionId: shell.sessionId, input: `${line}\r` } })
    .catch((error: unknown) => { throw new Error(`write_to_shell: ${error instanceof Error ? error.message : JSON.stringify(error)}`); });
}

function tabStripItem(tabId: string) {
  return document.querySelector(".terminal-tab-strip")?.querySelector<HTMLElement>(`#${CSS.escape(`tab-${tabId}`)}`) ?? undefined;
}

function tabPanePanel(index: number) {
  return document.querySelectorAll<HTMLElement>(".terminal-workspace-layout .terminal-workspace-tab-panel")[index]?.querySelector<HTMLElement>(".terminal-pane") ?? undefined;
}

export async function terminalTabsScenario(config: SmokeConfig, log: { done(step: string): void }) {
  await verifyTerminalRendererSwitch(log);
  const wait = config.timeoutMs;
  const nonce = config.nonce;
  const workbench = useWorkbenchStore();
  const preferences = useWorkspaceStatePreferencesStore();
  const terminalWorkspace = useTerminalWorkspaceStore();
  const shells: Shell[] = [];
  let queries = 0;
  // What the terminal shows: its xterm buffer (the WebGL renderer leaves no DOM text).
  enableTerminalBufferProbe();
  const screen = (shell: Shell) => readTerminalBuffer(shell.sessionId);
  const shellReadyLines = (text: string) => text.split("\n").filter((line) => line.includes("[MinCode] Shell ready")).length;

  /** Prints `expr` in the shell between unique markers; the typed command line itself never matches. */
  async function query(shell: Shell, expr: string, what: string) {
    queries += 1;
    const label = `MINCODE_Q${queries}_`;
    const pattern = new RegExp(`${label}(\\d+(?:x\\d+)?)_${nonce}`);
    await write(shell, `echo ${label}${expr}_${nonce}`);
    await waitFor(() => pattern.test(shell.text()), `${what} in ${shell.sessionId}`, wait);
    return pattern.exec(shell.text())![1];
  }
  const pid = (shell: Shell) => query(shell, "$$", "shell PID");
  const size = (shell: Shell) => query(shell, "$(stty size | tr ' ' x)", "PTY size (rows x cols)");

  /** The pane's PTY is up: it printed (its prompt) or is already alive. */
  async function ready(shell: Shell) {
    if (await alive(shell.sessionId)) return;
    await waitFor(() => shell.output.value.length > 0, `first output of ${shell.sessionId}`, wait);
  }

  async function selectByClick(tabId: string) {
    const item = await untilDom(() => tabStripItem(tabId), `tab strip item ${tabId}`, wait);
    item.click();
    await waitFor(() => terminalWorkspace.activeTabId === tabId, `terminal tab ${tabId} to become active`, wait);
    await settle();
  }

  async function closeByClick(tabId: string) {
    const close = await untilDom(() => tabStripItem(tabId)?.querySelector<HTMLElement>(".is-icon-close"), `close button of tab ${tabId}`, wait);
    close.click();
  }

  const tab = workbench.createTab("terminal", `Tabs ${nonce}`);
  if (!tab) throw new Error("Could not create a terminal workbench tab");
  try {
    const target = resolveTerminalLayoutTarget(workbench.tabs, tab, preferences.isolationEnabled, preferences.scopeIdFor(tab.workspaceId));
    // A fresh single-tab layout (as "new terminal" does), not whatever the store held last.
    terminalWorkspace.activateTab(tab.id, { shellType: "system" }, target.layoutId, target.fallbackLayoutId);
    const first = captureShell(terminalWorkspace.activePaneId);
    shells.push(first);
    const firstTabId = terminalWorkspace.activeTabId;
    await router.push(workspaceTabRoute(tab));
    await ready(first);
    await untilDom(() => tabStripItem(firstTabId), "the tab strip to show the first terminal tab", wait);
    if (terminalWorkspace.tabs.length !== 1) throw new Error(`expected 1 terminal tab, got ${terminalWorkspace.tabs.length}`);
    log.done(`terminal workbench tab open with one terminal tab and a live shell (${first.sessionId})`);

    // "+" in the tab strip: a new terminal tab with its own shell, made active.
    const add = await untilDom(() => document.querySelector<HTMLButtonElement>(".terminal-tab-strip .terminal-tab-strip__add"), "the + button", wait);
    add.click();
    await waitFor(() => terminalWorkspace.tabs.length === 2, "+ to add a second terminal tab", wait);
    const secondTabId = terminalWorkspace.activeTabId;
    if (secondTabId === firstTabId) throw new Error("the new terminal tab is not active");
    const second = captureShell(terminalWorkspace.activePaneId);
    shells.push(second);
    await ready(second);
    const probe = { command: `echo MINCODE_TABS_$((40+2))_${nonce}`, expected: `MINCODE_TABS_42_${nonce}` };
    await write(second, probe.command);
    await waitFor(() => second.text().includes(probe.expected), "command output in the second terminal tab", wait);
    if (first.text().includes(probe.expected)) throw new Error("the second tab's command output reached the first tab's shell");
    await settle();
    const secondPid = await pid(second);
    const firstPid = await pid(first);
    if (firstPid === secondPid) throw new Error(`both terminal tabs talk to the same shell (pid ${firstPid})`);
    const secondSize = await size(second);
    const secondPane = await untilDom(() => tabPanePanel(1), "the second tab's terminal pane", wait);
    // Baseline: the probe reads this terminal, which shows the output and one start line.
    const shown = await untilDom(() => {
      const text = screen(second);
      return text?.includes(probe.expected) && shellReadyLines(text) > 0 ? text : undefined;
    }, `"${probe.expected}" and "[MinCode] Shell ready" on the second tab's screen (buffer: ${JSON.stringify(screen(second)?.slice(-400))})`, wait);
    if (shellReadyLines(shown) !== 1) throw new Error(`expected one "[MinCode] Shell ready" line, got ${shellReadyLines(shown)}`);
    log.done(`"+" opened a second terminal tab with its own shell (pid ${secondPid}, ${secondSize}); "${probe.expected}" came back`);

    // Away and back: the hidden tab keeps its shell, its pane element and its PTY size.
    await selectByClick(firstTabId);
    if (!(await alive(second.sessionId))) throw new Error("the background tab's shell died");
    const hiddenSize = await size(second);
    if (hiddenSize !== secondSize) throw new Error(`the hidden tab's PTY was resized from ${secondSize} to ${hiddenSize}`);
    await pid(first); // the first tab is on screen and still answers
    log.done(`switched to the first tab; the hidden second tab kept running at ${hiddenSize}`);

    await selectByClick(secondTabId);
    const backPid = await pid(second);
    const backSize = await size(second);
    if (backPid !== secondPid) throw new Error(`the second tab's shell restarted: pid ${secondPid} -> ${backPid}`);
    if (second.ended.value) throw new Error("the second tab's shell session ended while switching tabs");
    if (backSize !== secondSize) throw new Error(`switching back changed the PTY size from ${secondSize} to ${backSize}`);
    const cols = Number(backSize.split("x")[1]);
    if (!(cols > 20)) throw new Error(`PTY clamped to ${cols} columns`);
    if (tabPanePanel(1) !== secondPane || !secondPane.isConnected) throw new Error("the second tab's terminal pane was remounted");
    const backScreen = screen(second);
    if (backScreen === undefined) throw new Error("no terminal is mounted for the second tab after switching back");
    if (!backScreen.includes(probe.expected)) throw new Error(`switching back lost the earlier output "${probe.expected}"; screen: ${JSON.stringify(backScreen.slice(-400))}`);
    if (shellReadyLines(backScreen) !== 1) throw new Error(`switching back started the shell again: ${shellReadyLines(backScreen)} "[MinCode] Shell ready" lines`);
    log.done(`switched back: same shell (pid ${backPid}), same pane element, "${probe.expected}" still on screen, one "Shell ready" line, PTY still ${backSize}`);

    // The unfocused split must restore its renderer after either level of tabs
    // returns. A final stty query alone misses a pane left on DOM cell metrics.
    const split = terminalWorkspace.splitPane(second.sessionId, "horizontal");
    if (!split) throw new Error("could not split the second terminal tab");
    const third = captureShell(split.id);
    shells.push(third);
    await ready(third);
    const bothRenderers = () => {
      const screens = document.querySelectorAll(
        ".terminal-workspace-tab-panel:not([style*='display: none']) .xterm-screen"
      );
      return screens.length === 2 && [...screens].every((screen) => screen.querySelector("canvas"));
    };
    await untilDom(bothRenderers, "both split WebGL renderers", wait);
    await settle();
    const splitSizes = [await size(second), await size(third)];
    for (let iteration = 0; iteration < 3; iteration += 1) {
      await selectByClick(firstTabId);
      await selectByClick(secondTabId);
      await untilDom(bothRenderers, "both renderers after terminal tab switch", wait);
      await router.push({ name: "dashboard" });
      await settle();
      workbench.selectTab(tab.id);
      await router.push(workspaceTabRoute(tab));
      await untilDom(bothRenderers, "both renderers after workbench activation", wait);
      await settle();
      const restoredSizes = [await size(second), await size(third)];
      if (restoredSizes.some((value, index) => value !== splitSizes[index])) {
        throw new Error(`split PTY sizes changed: ${splitSizes.join(", ")} -> ${restoredSizes.join(", ")}`);
      }
    }
    log.done(`three terminal/workbench tab cycles restored both split renderers and kept PTY sizes ${splitSizes.join(", ")}`);

    // × on the second tab ends its shell.
    await closeByClick(secondTabId);
    await waitFor(() => terminalWorkspace.tabs.length === 1, "× to close the second terminal tab", wait);
    await waitFor(() => second.ended.value, "the closed tab's shell to end", wait);
    if (await alive(second.sessionId)) throw new Error("the closed terminal tab's shell is still alive");
    if (terminalWorkspace.activeTabId !== firstTabId) throw new Error("closing the second tab did not activate the first");
    if (!(await alive(first.sessionId))) throw new Error("closing the second tab ended the first tab's shell");
    log.done("× closed the second terminal tab: its shell session ended and shell_session_alive is false; one tab left");

    // × on the last tab closes the workbench tab.
    await closeByClick(firstTabId);
    await waitFor(() => !workbench.tabs.some((item) => item.id === tab.id), "× on the last terminal tab to close the workbench tab", wait);
    await waitFor(() => first.ended.value, "the last tab's shell to end", wait);
    if (await alive(first.sessionId)) throw new Error("the last terminal tab's shell is still alive");
    log.done("× on the last terminal tab closed the workbench tab and ended its shell");
  } finally {
    for (const shell of shells) shell.dispose();
    if (workbench.tabs.some((item) => item.id === tab.id)) workbench.closeTab(tab.id, true);
    for (const shell of shells) await invokeBackend<void>("close_local_shell", { payload: { sessionId: shell.sessionId } }).catch(() => undefined);
  }
}
