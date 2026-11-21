// Loaded only by the native -tags e2e smoke runner. Opens a real terminal tab and
// drives the PTY: an OSC 9 notification (with the tab on screen), then from another
// tab a long command that goes quiet, a bell and the shell exiting must each mark
// the terminal tab; closing the tab drops the marker.
import { nextTick, ref, watch } from "vue";
import router from "@/router";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import type { TerminalAttentionReason } from "@/domain/terminal/terminal-attention";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { readSettingsSnapshot, updateSettingsRuntimeSnapshot } from "@/services/settings-persistence";
import { invokeBackend, listenAppEvent } from "@/services/desktop-bridge";
import { useTerminalAttentionStore } from "@/stores/terminal-attention";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";

// Event-driven waits: a background WebView throttles timers, so polling with
// setTimeout can stall for minutes while reactive state is already there.
function waitFor<T>(read: () => T | undefined | false | null, label: string, timeoutMs: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => {
      stop();
      reject(new Error(`Timed out waiting for ${label}`));
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

export async function terminalAttentionScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const wait = config.timeoutMs;
  const workbench = useWorkbenchStore();
  const preferences = useWorkspaceStatePreferencesStore();
  const terminalWorkspace = useTerminalWorkspaceStore();
  const attention = useTerminalAttentionStore();
  const previousSettings = readSettingsSnapshot<Record<string, unknown>>();
  // No system notification permission prompt in the isolated test profile.
  updateSettingsRuntimeSnapshot({ ...previousSettings, terminalAttention: true, terminalAttentionSystemNotify: false });

  const tab = workbench.createTab("terminal", `Attention ${config.nonce}`);
  if (!tab) throw new Error("Could not create a terminal tab");
  try {
    const target = resolveTerminalLayoutTarget(workbench.tabs, tab, preferences.isolationEnabled, preferences.scopeIdFor(tab.workspaceId));
    terminalWorkspace.activateTab(tab.id, {}, target.layoutId, target.fallbackLayoutId);
    await router.push(workspaceTabRoute(tab));
    const paneId = terminalWorkspace.activePaneId;
    // The shell's first output (its prompt) proves the PTY is up.
    const output = ref(false);
    const stopOutput = listenAppEvent(`shell-output:${paneId}`, () => { output.value = true; });
    try {
      if (!(await invokeBackend<boolean>("shell_session_alive", { payload: { sessionId: paneId } }).catch(() => false))) {
        await waitFor(() => output.value, "terminal PTY output", wait);
      }
    } finally {
      stopOutput();
    }
    log.done(`terminal tab opened with a live PTY (${paneId})`);

    const write = (line: string) => invokeBackend<void>("write_to_shell", { payload: { sessionId: paneId, input: `${line}\r` } })
      .catch((error: unknown) => { throw new Error(`write_to_shell: ${error instanceof Error ? error.message : JSON.stringify(error)}`); });
    const reason = () => attention.items.get(paneId)?.reason;
    const expectReason = (expected: TerminalAttentionReason, label: string, ms = wait) => waitFor(() => reason() === expected, label, ms);
    const marker = () => document.querySelector(".app-workspace-tab .app-terminal-attention, .app-tabs-overflow .app-terminal-attention");
    const leave = async () => {
      workbench.openView("dashboard"); // the dashboard is "no active tab"
      await router.push({ name: "dashboard" }).catch(() => undefined);
      await waitFor(() => !attention.visiblePanes.has(paneId), "terminal to leave the screen", wait);
    };
    const reset = () => attention.dismiss([paneId]);

    // The tab is on screen: the user still gets a marker (and a notification).
    await write(String.raw`printf '\033]9;Agent turn complete\007'`);
    await expectReason("completed", "OSC 9 notification to mark the on-screen tab");
    await nextTick();
    if (!marker()) throw new Error("no bell marker on the terminal tab");
    const chip = document.querySelector(".terminal-pane-toolbar .terminal-pane-attention");
    if (!chip?.textContent?.includes("已完成") && !chip?.textContent?.includes("Done")) throw new Error(`pane toolbar chip: ${chip?.textContent ?? "missing"}`);
    if (attention.count < 1) throw new Error("attention not counted for the Dock badge");
    log.done("OSC 9 \"Agent turn complete\" marked the on-screen terminal tab and its pane toolbar");

    await leave();
    reset();
    // Runs ~10 s, which also clears the backend's 3 s limit between bell/notify signals.
    await write("for i in 1 2 3 4 5 6; do echo tick-$i; sleep 1; done");
    await expectReason("quiet", "long output that stops to mark the tab", wait + 15_000);
    log.done("six seconds of output followed by silence marked the tab as stopped");

    reset();
    await write(String.raw`sleep 1.5; printf 'ring\a\n'`); // a bell right after a keystroke is the shell's own feedback
    await expectReason("bell", "bell to mark the tab");
    log.done("a bell marked the tab; the BEL ending shell title sequences did not");

    reset();
    await write("exit");
    await expectReason("exited", "shell exit to mark the tab");
    log.done("the shell exiting marked the tab");

    // Closing a terminal tab ends its shell instead of leaving it running hidden.
    const other = workbench.createTab("terminal", `Closed ${config.nonce}`);
    if (!other) throw new Error("Could not create a second terminal tab");
    const otherTarget = resolveTerminalLayoutTarget(workbench.tabs, other, preferences.isolationEnabled, preferences.scopeIdFor(other.workspaceId));
    terminalWorkspace.activateTab(other.id, {}, otherTarget.layoutId, otherTarget.fallbackLayoutId);
    await router.push(workspaceTabRoute(other));
    const otherPane = terminalWorkspace.activePaneId;
    const otherOutput = ref(false);
    const otherEnded = ref(false);
    const stops = [
      listenAppEvent(`shell-output:${otherPane}`, () => { otherOutput.value = true; }),
      listenAppEvent<{ sessionId?: string }>("shell-session-ended", (event) => { if (event?.sessionId === otherPane) otherEnded.value = true; }),
    ];
    try {
      await waitFor(() => otherOutput.value, "second terminal output", wait);
      workbench.closeTab(other.id, true);
      await waitFor(() => otherEnded.value, "closed tab's shell to end", wait);
      if (await invokeBackend<boolean>("shell_session_alive", { payload: { sessionId: otherPane } })) throw new Error("closed tab's shell is still alive");
    } finally {
      for (const stop of stops) stop();
    }
    log.done("closing a terminal tab ended its shell");
  } finally {
    const paneIds = [...terminalWorkspace.paneIdsForLayout(resolveTerminalLayoutTarget(workbench.tabs, tab, preferences.isolationEnabled, preferences.scopeIdFor(tab.workspaceId)).layoutId)];
    workbench.closeTab(tab.id, true);
    for (const id of paneIds) await invokeBackend<void>("close_local_shell", { payload: { sessionId: id } }).catch(() => undefined);
    updateSettingsRuntimeSnapshot(previousSettings);
  }
  await waitFor(() => attention.count === 0, "closing the tab to drop its marker", wait);
  log.done("closing the tab dropped its marker");
}
