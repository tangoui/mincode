// Loaded only by the native -tags e2e smoke runner. Checks, after each everyday layout change,
// that every visible local terminal shows the grid its PTY has: xterm cols/rows == `stty size`
// == what the container fits. A pane whose xterm grid differs from its PTY makes full-screen
// programs (Claude Code, vim) draw for the wrong width: hard wraps, remnants, overlapping lines.
import { Window } from "@/services/desktop-window";
import router from "@/router";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { plainTerminalText, type SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { shellOutputEventName, type ShellOutputEvent } from "@/domain/ssh/types";
import { enableTerminalBufferProbe, readTerminalSize } from "@/services/e2e-terminal-buffer-probe";
import { invokeBackend, listenAppEvent } from "@/services/desktop-bridge";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
const settle = () => sleep(600);

async function until<T>(read: () => T | undefined | false | null, label: string, timeoutMs: number): Promise<T> {
  const deadline = performance.now() + timeoutMs;
  while (performance.now() < deadline) {
    const value = read();
    if (value) return value;
    await sleep(50);
  }
  throw new Error(`terminal-size smoke timed out: ${label}`);
}

export async function terminalSizeScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const wait = config.timeoutMs;
  const nonce = config.nonce;
  const workbench = useWorkbenchStore();
  const preferences = useWorkspaceStatePreferencesStore();
  const terminals = useTerminalWorkspaceStore();
  enableTerminalBufferProbe();
  const outputs = new Map<string, string>();
  const stops: (() => void)[] = [];
  const sessions = new Set<string>();
  const failures: string[] = [];
  let queries = 0;

  function capture(sessionId: string) {
    if (sessions.has(sessionId)) return;
    sessions.add(sessionId);
    outputs.set(sessionId, "");
    stops.push(listenAppEvent<ShellOutputEvent>(shellOutputEventName(sessionId), (event) => {
      outputs.set(sessionId, (outputs.get(sessionId) ?? "") + (typeof event?.data === "string" ? event.data : ""));
    }));
  }
  async function alive(sessionId: string) {
    const end = performance.now() + wait;
    while (performance.now() < end) {
      if (await invokeBackend<boolean>("shell_session_alive", { payload: { sessionId } }).catch(() => false)) return;
      await sleep(100);
    }
    throw new Error(`session ${sessionId} did not start`);
  }
  async function ptySize(sessionId: string) {
    queries += 1;
    const label = `MCSZ${queries}_`;
    const pattern = new RegExp(`${label}(\\d+)x(\\d+)_${nonce}`);
    await invokeBackend<void>("write_to_shell", { payload: { sessionId, input: `echo ${label}$(stty size | tr ' ' x)_${nonce}\r` } });
    const match = await until(() => pattern.exec(plainTerminalText(outputs.get(sessionId) ?? "")), `stty size in ${sessionId}`, wait);
    return { rows: Number(match[1]), cols: Number(match[2]) };
  }
  /** Every pane of the active terminal tab: xterm grid == PTY == fit proposal. */
  async function check(step: string) {
    await settle();
    const tab = terminals.tabs.find((item) => item.id === terminals.activeTabId);
    const ids = terminals.paneIds.filter((id) => id.startsWith("terminal-") && tab && JSON.stringify(tab.root).includes(id));
    const rows: string[] = [];
    for (const id of ids) {
      capture(id);
      const view = readTerminalSize(id);
      const pty = await ptySize(id);
      const line = `${id.slice(-6)} xterm ${view?.cols}x${view?.rows} pty ${pty.cols}x${pty.rows} fit ${view?.proposed?.cols}x${view?.proposed?.rows}`;
      rows.push(line);
      const bad = !view || view.cols !== pty.cols || view.rows !== pty.rows || (view.proposed && (view.proposed.cols !== view.cols || view.proposed.rows !== view.rows));
      if (bad) failures.push(`${step}: ${line}`);
    }
    log.done(`${failures.some((item) => item.startsWith(`${step}:`)) ? "MISMATCH" : "ok"} · ${step} · ${rows.join(" | ")}`);
  }

  const tab = workbench.createTab("terminal", `Size ${nonce}`);
  if (!tab) throw new Error("Could not open terminal tab");
  try {
    const target = resolveTerminalLayoutTarget(workbench.tabs, tab, preferences.isolationEnabled, preferences.scopeIdFor(tab.workspaceId));
    terminals.activateTab(tab.id, { shellType: "system" }, target.layoutId, target.fallbackLayoutId);
    await router.push(workspaceTabRoute(tab));
    const first = await until(() => terminals.activePaneId, "first pane", wait);
    capture(first);
    await alive(first);
    const firstTab = terminals.activeTabId;
    await check("新开终端");

    // Split and close the split: the remaining pane moves in the pane tree.
    const split = terminals.splitPane(first, "horizontal");
    if (!split) throw new Error("could not split");
    capture(split.id);
    await alive(split.id);
    await check("左右分屏");
    const splitNode = terminals.tabs.find((item) => item.id === firstTab)?.root;
    if (splitNode && splitNode.type === "split") {
      for (let i = 0; i < 12; i++) terminals.resizeSplit(splitNode.id, i % 2 ? [30, 70] : [70, 30]);
      await check("连续拖动分隔条");
    }
    terminals.closePane(split.id);
    await invokeBackend<void>("close_local_shell", { payload: { sessionId: split.id } }).catch(() => undefined);
    await check("关闭分屏");
    const vertical = terminals.splitPane(first, "vertical");
    if (vertical) { capture(vertical.id); await alive(vertical.id); await check("上下分屏"); }

    // Drag the divider frame by frame: every frame fits and sends a resize while earlier ones may
    // still be in flight (each is its own IPC request, answered in any order).
    const dragRoot = () => terminals.tabs.find((item) => item.id === firstTab)?.root;
    for (let round = 1; round <= 5; round++) {
      const root = dragRoot();
      if (!root || root.type !== "split") break;
      for (let frame = 0; frame <= 40; frame++) {
        const left = round % 2 ? 80 - frame : 40 + frame;
        terminals.resizeSplit(root.id, [left, 100 - left]);
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      }
      await check(`逐帧拖动分隔条 ${round}`);
    }
    // Diagnostic: a burst of resizes without waiting. Does the PTY end at the last size sent?
    if (vertical) {
      const burst = Array.from({ length: 40 }, (_, i) => invokeBackend<void>("resize_local_shell", { payload: { sessionId: vertical.id, cols: 60 + i, rows: 20 } }));
      await Promise.allSettled(burst);
      const after = await ptySize(vertical.id);
      log.done(`${after.cols === 99 ? "ok" : "REORDERED"} · 连发 40 次 resize（60→99 列）后 PTY ${after.cols}x${after.rows}`);
      if (after.cols !== 99) failures.push(`IPC 连发 resize 乱序：最后发 99 列，PTY 停在 ${after.cols} 列`);
      // The PTY now disagrees with the screen (the user-reported state). Showing the pane again
      // and the window regaining focus must correct it without any size change.
      terminals.addLocalTab({ shellType: "system" });
      const healTab = terminals.activeTabId;
      capture(terminals.activePaneId);
      await settle();
      terminals.selectTab(firstTab);
      await check("PTY 被改错后切走再切回（自我修复）");
      await invokeBackend<void>("resize_local_shell", { payload: { sessionId: vertical.id, cols: 61, rows: 20 } });
      await ptySize(vertical.id);
      terminals.selectPane(vertical.id);
      window.dispatchEvent(new Event("focus"));
      await check("PTY 被改错后窗口重新获得焦点（自我修复）");
      terminals.closeTab(healTab);
    }

    // A second terminal tab; switch back and forth.
    terminals.addLocalTab({ shellType: "system" });
    const secondTab = terminals.activeTabId;
    const second = terminals.activePaneId;
    capture(second);
    await alive(second);
    await check("新开第二个终端标签");
    // Resize the first tab's split while it is hidden, then show it.
    const hiddenRoot = terminals.tabs.find((item) => item.id === firstTab)?.root;
    if (hiddenRoot && hiddenRoot.type === "split") terminals.resizeSplit(hiddenRoot.id, [25, 75]);
    terminals.selectTab(firstTab);
    await check("隐藏时调整过分屏后切回第一个标签");
    for (let i = 1; i <= 3; i++) {
      terminals.selectTab(secondTab);
      await check(`切到第二个标签 ${i}`);
      terminals.selectTab(firstTab);
      await check(`切回第一个标签 ${i}`);
    }

    // Other workspaces and back (the terminal view is kept alive, deactivated).
    for (const name of ["dashboard", "projects", "database"]) {
      await router.push({ name }).catch(() => undefined);
      await settle();
      workbench.selectTab(tab.id);
      await router.push(workspaceTabRoute(tab));
      await check(`去 ${name} 再回来`);
    }
    // Switch terminal tabs while the terminal workspace is in the background.
    await router.push({ name: "dashboard" }).catch(() => undefined);
    await settle();
    terminals.selectTab(secondTab);
    await settle();
    workbench.selectTab(tab.id);
    await router.push(workspaceTabRoute(tab));
    await check("后台切换终端标签后回来");
    terminals.selectTab(firstTab);
    await check("再切回第一个标签");

    // The native window: drag-resize frame by frame, maximise and restore, minimise and restore.
    const original = await Window.Size();
    for (let round = 1; round <= 3; round++) {
      for (let frame = 0; frame <= 30; frame++) {
        const shrink = round % 2 ? frame : 30 - frame;
        await Window.SetSize(original.width - shrink * 20, original.height - shrink * 8);
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      }
      await check(`拖动窗口边缘 ${round}`);
    }
    await Window.SetSize(original.width, original.height);
    await Window.ToggleMaximise();
    await sleep(1200);
    await check("最大化");
    await Window.ToggleMaximise();
    await sleep(1200);
    await check("取消最大化");
    await Window.SetSize(Math.round(original.width / 2), original.height);
    await sleep(300);
    await Window.SetSize(original.width, original.height);
    await check("窗口缩到一半再恢复");
    await Window.Minimise();
    await sleep(1200);
    await Window.UnMinimise();
    await Window.Show();
    await sleep(1200);
    await check("最小化再恢复");

    // Reload a pane (remount, re-attach).
    terminals.reloadPane(first);
    await check("重新加载窗格");
  } finally {
    for (const stop of stops) stop();
    workbench.closeTab(tab.id, true);
    for (const id of sessions) await invokeBackend<void>("close_local_shell", { payload: { sessionId: id } }).catch(() => undefined);
  }
  if (failures.length) throw new Error(`terminal grid differs from PTY:\n${failures.join("\n")}`);
}
