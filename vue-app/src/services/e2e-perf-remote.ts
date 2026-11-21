// Opt-in native performance probe for terminals and SSH/SFTP (-tags e2e builds only).
// Uses the harness's loopback e2e-sshd (real PTY shell and SFTP subsystem):
//  - four local terminal panes flooding output at the same time,
//  - an SSH terminal pane flooding output,
//  - the SFTP workspace listing and scrolling a remote directory with thousands of files.
import router from "@/router";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import type { SshConnectionProfile } from "@/domain/ssh/types";
import { resolveTerminalLayoutTarget } from "@/domain/workspace/terminal-layout-scope";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { invokeBackend, listenAppEvent } from "@/services/desktop-bridge";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";
import { createMeasure, nextFrame, until, type PerfLog } from "@/services/e2e-perf-kit";
import { measurePredictiveTyping, measureTyping, pressKey, typeChar, type PredictiveTypingRow } from "@/services/e2e-perf-typing";
import { enablePredictiveEchoProbe } from "@/services/e2e-predictive-echo-probe";
import { enableTerminalBufferProbe, readTerminalBuffer } from "@/services/e2e-terminal-buffer-probe";
import { hydrateSettingsSnapshot, persistSettingsSnapshot, readSettingsSnapshot } from "@/services/settings-persistence";
import type { PredictiveEchoMode } from "@/domain/terminal/predictive-echo/gate";

function visible<T extends HTMLElement>(selector: string) {
  return [...document.querySelectorAll<T>(selector)].find((element) => element.getBoundingClientRect().width > 0);
}

const pause = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

const visibleTextareas = () => [...document.querySelectorAll<HTMLTextAreaElement>(".xterm-helper-textarea")].filter((node) => node.closest(".xterm")?.getBoundingClientRect().width);

function check(condition: unknown, message: string, row?: PredictiveTypingRow): asserts condition {
  if (!condition) throw new Error(`predictive echo: ${message}${row ? ` (${row.label}: ${JSON.stringify(row)})` : ""}`);
}

/** Letters a..z cycled to `count` chars. */
const letters = (count: number, from = 0) => Array.from({ length: count }, (_, i) => String.fromCharCode(97 + ((from + i) % 26))).join("");

/**
 * Predictive echo against the laggy e2e-sshd (every PTY output chunk held back 100 ms). Each mode
 * gets a fresh SSH pane (the binding reads the setting on creation), written through the real
 * settings persistence. The first char of every line is never predicted by design: it waits for
 * the real echo, so key → overlay is measured for chars 2..N and key → echo for char 1. A fast run
 * (keys 60 ms apart, faster than the echo) checks that chars typed before the first echo are
 * predicted once it arrives.
 */
async function predictiveEchoBlock(config: SmokeConfig, log: PerfLog, laggyPort: number, alive: (sessionId: string) => Promise<void>) {
  const ssh = config.ssh!;
  const wait = config.timeoutMs;
  const workbench = useWorkbenchStore();
  const terminals = useTerminalWorkspaceStore();
  const preferences = useWorkspaceStatePreferencesStore();
  enableTerminalBufferProbe();
  enablePredictiveEchoProbe();
  const previousSettings = await hydrateSettingsSnapshot<Record<string, unknown>>();
  const profile: SshConnectionProfile = {
    id: `perf-laggy-ssh-${config.nonce}`, name: `perf-laggy-ssh-${config.nonce}`, host: ssh.host, port: laggyPort, username: ssh.username,
    authType: "password", tags: [], favorite: false, saveCredentials: false, portForwards: [], algorithmPolicy: "modern",
    createdAt: Date.now(), updatedAt: Date.now(),
  };
  const PROMPT = "pe$";
  /** `pe$ ` + 40 typed chars + the cursor cell */
  const MIN_COLS = 46;
  /** Columns from the xterm's own sizing: `.xterm-screen` width ÷ one cell (`.xterm-char-measure-element`, if present). */
  const paneCols = (area: HTMLTextAreaElement) => {
    const xterm = area.closest(".xterm");
    const screen = xterm?.querySelector<HTMLElement>(".xterm-screen");
    const measure = xterm?.querySelector<HTMLElement>(".xterm-char-measure-element");
    const cell = measure?.getBoundingClientRect().width ?? 0;
    const length = measure?.textContent?.length ?? 0;
    if (!screen || !cell || !length) return null;
    return Math.floor(screen.getBoundingClientRect().width / (cell / length));
  };
  /** Last non-blank line of the pane's active buffer (alternate screen while vi runs). */
  const lastLine = (sessionId: string) => {
    const lines = (readTerminalBuffer(sessionId) ?? "").split("\n").map((line) => line.trimEnd()).filter(Boolean);
    return lines[lines.length - 1] ?? "";
  };
  const shell = (sessionId: string, input: string) => invokeBackend("write_to_shell", { payload: { sessionId, input } });
  const tabs: string[] = [];

  /** A terminal tab with a local pane and the laggy SSH pane next to it, running bash with readline. */
  const open = async (mode: PredictiveEchoMode) => {
    // One tab at a time: textareas are picked by DOM order.
    for (const id of tabs.splice(0)) workbench.closeTab(id, true);
    await pause(300);
    await persistSettingsSnapshot({ ...readSettingsSnapshot<Record<string, unknown>>(), predictiveEcho: mode });
    const tab = workbench.createTab("terminal", `Predictive ${mode} ${config.nonce}`);
    if (!tab) throw new Error("Could not open predictive echo terminal tab");
    tabs.push(tab.id);
    const target = resolveTerminalLayoutTarget(workbench.tabs, tab, preferences.isolationEnabled, preferences.scopeIdFor(tab.workspaceId));
    terminals.activateTab(tab.id, {}, target.layoutId, target.fallbackLayoutId);
    await router.push(workspaceTabRoute(tab));
    const localId = await until(() => terminals.activePaneId, "predictive local pane", wait);
    await alive(localId);
    const pane = terminals.splitSshPane(localId, "horizontal", { profile, credentials: { password: ssh.password } });
    if (!pane) throw new Error("could not split the laggy SSH pane");
    const sessionId = pane.id;
    await alive(sessionId);
    await until(() => lastLine(sessionId) === "e2e$", "laggy sh prompt", wait * 2);
    await shell(sessionId, `exec env BASH_SILENCE_DEPRECATION_WARNING=1 PS1='${PROMPT} ' /bin/bash --norc --noprofile -i\r`);
    await until(() => lastLine(sessionId) === PROMPT, "laggy bash prompt", wait);
    // Read the textarea only now (panes may remount on split/connect); a horizontal split puts the new pane second.
    const area = await until(() => (visibleTextareas().length >= 2 ? visibleTextareas()[1] : undefined), "laggy ssh textarea", wait);
    const cols = paneCols(area);
    if (cols !== null && cols < MIN_COLS) throw new Error(`predictive echo: laggy SSH pane is ${cols} columns, needs >= ${MIN_COLS} so a typed line never wraps`);
    area.focus();
    await pause(500);
    return { sessionId, area };
  };
  /** Ctrl+U through xterm (so the binding sees a control key) and wait for the bare prompt. */
  const clearLine = async (sessionId: string, area: HTMLTextAreaElement) => {
    pressKey(area, "CtrlU");
    await until(() => lastLine(sessionId) === PROMPT, "line cleared", wait);
    await pause(300);
  };
  const typedLine = (sessionId: string, text: string, row: PredictiveTypingRow) => {
    const lines = (readTerminalBuffer(sessionId) ?? "").split("\n").map((line) => line.trimEnd()).filter(Boolean);
    const line = lines[lines.length - 1] ?? "";
    const expected = `${PROMPT} ${text}`;
    // The buffer reader does not mark wrapped rows: name a wrap instead of a bogus mismatch.
    const wrapped = lines.length >= 2 && `${lines[lines.length - 2]}${line}` === expected;
    check(!wrapped, `typed line wrapped: the laggy SSH pane is narrower than ${MIN_COLS} columns`, row);
    check(line === expected, `buffer line ${JSON.stringify(line)} != typed ${JSON.stringify(text)}`, row);
  };
  const echoedLine = (row: PredictiveTypingRow) => {
    check(row.lost === 0, "keys lost", row);
    check(row.finalCells === 0, "overlay not empty after the echoes arrived", row);
    check(row.misses === 0, `${row.misses} predictions missed or timed out`, row);
    check(!row.predicted.includes(1), "the first char of the line was predicted", row);
  };

  try {
    // always: chars 2..N predicted within a frame of the key.
    {
      const { sessionId, area } = await open("always");
      const text = letters(40);
      const row = await measurePredictiveTyping(log, "SSH(100ms)·预测打字 always", sessionId, area, text, 120);
      echoedLine(row);
      const unpredicted = text.length - 1 - row.predicted.length;
      // Char 2 may still race char 1's echo (100 ms delay vs 120 ms interval).
      check(unpredicted <= 2, `${unpredicted} of chars 2..N not predicted`, row);
      // The draw runs inside the key handler (cost check); the spec target is the frame after it.
      check(row.overlay.p90 <= 2, `key → overlay draw p90 ${row.overlay.p90} ms > 2 ms`, row);
      check(row.overlayPaint.n > 0 && row.overlayPaint.p90 <= 20, `key → frame after overlay draw p90 ${row.overlayPaint.p90} ms > 20 ms (n=${row.overlayPaint.n})`, row);
      typedLine(sessionId, text, row);
      await clearLine(sessionId, area);

      // Password: read -s never echoes, so nothing may ever be drawn.
      // read -s does not echo the Enter either: `; echo` moves the next prompt to its own line.
      await shell(sessionId, `read -s -p "Token " t; echo\r`);
      await until(() => lastLine(sessionId) === "Token", "Token prompt", wait);
      await pause(300);
      const secret = await measurePredictiveTyping(log, "SSH(100ms)·密码", sessionId, area, letters(10, 7), 120, { expectEcho: false });
      check(secret.draws === 0, `${secret.draws} overlay draws at the password prompt`, secret);
      check(secret.echoed === 0, "the password prompt echoed", secret);
      pressKey(area, "Enter");
      await until(() => lastLine(sessionId) === PROMPT, "prompt after password", wait);
      await pause(300);

      // vi: insert-mode typing predicted and confirmed; normal-mode motions never drawn.
      const file = `pe-${config.nonce}.txt`;
      await shell(sessionId, `/usr/bin/vi ${file}\r`);
      await until(() => {
        const screen = readTerminalBuffer(sessionId) ?? "";
        return screen.includes(file) && screen.split("\n").some((line) => line.trimEnd() === "~");
      }, "vi screen", wait);
      await pause(500);
      typeChar(area, "i");
      // 'i' is never echoed: let its latency sample expire before measuring.
      await pause(1500);
      const insertText = letters(20, 3);
      const insert = await measurePredictiveTyping(log, "SSH(100ms)·vi 插入", sessionId, area, insertText, 120);
      check(insert.lost === 0, "vi keys lost", insert);
      check(insert.predicted.length > 0, "nothing predicted in vi insert mode", insert);
      check(insert.finalCells === 0, "vi overlay not empty after the echoes arrived", insert);
      check(insert.misses === 0, `${insert.misses} vi insert predictions missed or timed out`, insert);
      check((readTerminalBuffer(sessionId) ?? "").includes(insertText), "vi buffer is missing the typed text", insert);
      pressKey(area, "Escape");
      await pause(500);
      const motions = await measurePredictiveTyping(log, "SSH(100ms)·vi 普通模式", sessionId, area, "jjkk", 120, { expectEcho: false });
      check(motions.draws === 0, `${motions.draws} overlay draws for vi normal-mode keys`, motions);
      await shell(sessionId, ":q!\r");
      await until(() => lastLine(sessionId) === PROMPT, "prompt after vi", wait);
    }

    // always, typing faster than the 100 ms echo (60 ms apart): the sample queue never empties, so
    // chars typed before the line's first echo are drawn when it arrives and later chars at their key.
    {
      const { sessionId, area } = await open("always");
      const text = letters(40);
      const row = await measurePredictiveTyping(log, "SSH(100ms)·预测打字 always 快打 60ms", sessionId, area, text, 60);
      echoedLine(row);
      check(row.unpredictedAfterFirstEcho === 0, `${row.unpredictedAfterFirstEcho} chars echoed after the line's first echo were never predicted`, row);
      check(row.predicted.length >= text.length - 3, `only ${row.predicted.length} of ${text.length} chars predicted`, row);
      check(row.overlayPaint.n > 0 && row.overlayPaint.p90 <= 20, `key → frame after overlay draw p90 ${row.overlayPaint.p90} ms > 20 ms (n=${row.overlayPaint.n})`, row);
      typedLine(sessionId, text, row);
      await clearLine(sessionId, area);
    }

    // auto: off until the first echo samples, then predicting at 100 ms latency.
    {
      const { sessionId, area } = await open("auto");
      const text = letters(40);
      const row = await measurePredictiveTyping(log, "SSH(100ms)·预测打字 auto", sessionId, area, text, 120);
      echoedLine(row);
      const first = row.predicted[0];
      check(first !== undefined && first <= 10, `auto mode started predicting at char ${first ?? "never"}`, row);
      const unpredicted = text.length - first + 1 - row.predicted.length;
      check(unpredicted <= 2, `${unpredicted} chars not predicted after auto mode started`, row);
      typedLine(sessionId, text, row);
    }

    // Control: off, key → echo carries the full delay.
    {
      const { sessionId, area } = await open("off");
      const text = letters(40);
      const row = await measurePredictiveTyping(log, "SSH(100ms)·不预测", sessionId, area, text, 120);
      check(row.lost === 0, "keys lost", row);
      check(row.draws === 0, `${row.draws} overlay draws with predictive echo off`, row);
      check(row.echo.p50 >= 100, `key → echo p50 ${row.echo.p50} ms < 100 ms: is the sshd delay on?`, row);
      typedLine(sessionId, text, row);
    }
  } finally {
    for (const id of tabs) workbench.closeTab(id, true);
    await persistSettingsSnapshot(previousSettings).catch(() => undefined);
  }
}

export async function perfRemoteScenario(config: SmokeConfig, log: PerfLog) {
  const ssh = config.ssh!;
  const fixture = config.perfRemote!;
  const wait = config.timeoutMs;
  const measure = createMeasure(log);
  const workbench = useWorkbenchStore();
  const terminals = useTerminalWorkspaceStore();
  const preferences = useWorkspaceStatePreferencesStore();
  const sshStore = useSshConnectionsStore();
  await sshStore.hydrate();

  /** Waits for a session to be alive (pane ids are the PTY session ids). */
  const alive = async (sessionId: string) => {
    const end = performance.now() + wait;
    while (performance.now() < end) {
      if (await invokeBackend<boolean>("shell_session_alive", { payload: { sessionId } }).catch(() => false)) return;
      await pause(100);
    }
    throw new Error(`session ${sessionId} did not start`);
  };
  /** Prints `lines` lines in each session and waits until every one printed its end marker. */
  const flood = async (sessionIds: string[], lines: number) => {
    const done = new Set<string>();
    const stops = sessionIds.map((id) => listenAppEvent(`shell-output:${id}`, (payload: unknown) => {
      if (JSON.stringify(payload).includes(`PERF_DONE_${config.nonce}`)) done.add(id);
    }));
    try {
      for (const id of sessionIds) {
        // The echoed command line shows PERF_DONE_""<nonce>; only real output joins the marker.
        await invokeBackend("write_to_shell", { payload: { sessionId: id, input: `seq 1 ${lines} | awk '{ print "line " $1 " some padding text to make the line longer than a short number" }'; echo PERF_DONE_""${config.nonce}\r` } });
      }
      await until(() => done.size === sessionIds.length, "all floods to finish", wait * 4);
    } finally {
      for (const stop of stops) stop();
    }
  };

  const profile: SshConnectionProfile = {
    id: `perf-ssh-${config.nonce}`, name: `perf-ssh-${config.nonce}`, host: ssh.host, port: ssh.port, username: ssh.username,
    authType: "password", tags: [], favorite: false, saveCredentials: false, portForwards: [], algorithmPolicy: "modern",
    defaultPath: fixture.remoteDir, createdAt: Date.now(), updatedAt: Date.now(),
  };

  // Keystroke latency: a local pane alone, then a local pane next to an SSH pane.
  const typingTab = workbench.createTab("terminal", `Typing ${config.nonce}`);
  if (!typingTab) throw new Error("Could not open typing terminal tab");
  try {
    const target = resolveTerminalLayoutTarget(workbench.tabs, typingTab, preferences.isolationEnabled, preferences.scopeIdFor(typingTab.workspaceId));
    terminals.activateTab(typingTab.id, {}, target.layoutId, target.fallbackLayoutId);
    await router.push(workspaceTabRoute(typingTab));
    const localId = await until(() => terminals.activePaneId, "typing pane", wait);
    await alive(localId);
    await pause(1500);
    const textareas = () => [...document.querySelectorAll<HTMLTextAreaElement>(".xterm-helper-textarea")].filter((node) => node.closest(".xterm")?.getBoundingClientRect().width);
    const localArea = await until(() => textareas()[0], "local textarea", wait);
    for (let round = 1; round <= 2; round++) {
      await measureTyping(log, `本地·正常打字 ${round}`, localId, localArea, 40, 120);
      await measureTyping(log, `本地·快速打字 ${round}`, localId, localArea, 60, 35);
    }
    localArea.dispatchEvent(new KeyboardEvent("keydown", { key: "c", ctrlKey: true, bubbles: true }));
    const sshPane = terminals.splitSshPane(localId, "horizontal", { profile, credentials: { password: ssh.password } });
    if (!sshPane) throw new Error("could not split an SSH pane");
    await alive(sshPane.id);
    await pause(1500);
    // Panes may remount on split: re-read both; a horizontal split puts the new pane second.
    const [localAfter, sshArea] = await until(() => (textareas().length >= 2 ? textareas() : undefined), "ssh textarea", wait);
    for (let round = 1; round <= 2; round++) {
      await measureTyping(log, `SSH·正常打字 ${round}`, sshPane.id, sshArea, 40, 120);
      await measureTyping(log, `SSH·快速打字 ${round}`, sshPane.id, sshArea, 60, 35);
    }
    await measureTyping(log, "本地·有 SSH 窗格时正常打字", localId, localAfter, 40, 120);
  } finally {
    workbench.closeTab(typingTab.id, true);
  }
  if (fixture.laggySsh) await predictiveEchoBlock(config, log, fixture.laggySsh.port, alive);
  if (fixture.only === "typing") return;

  const tab = workbench.createTab("terminal", `Perf ${config.nonce}`);
  if (!tab) throw new Error("Could not open terminal tab");
  try {
    let paneIds: string[] = [];
    await measure("打开终端并分成 4 个窗格", async () => {
      const target = resolveTerminalLayoutTarget(workbench.tabs, tab, preferences.isolationEnabled, preferences.scopeIdFor(tab.workspaceId));
      terminals.activateTab(tab.id, {}, target.layoutId, target.fallbackLayoutId);
      await router.push(workspaceTabRoute(tab));
      const first = await until(() => terminals.activePaneId, "first pane", wait);
      const second = terminals.splitPane(first, "horizontal");
      if (second) terminals.splitPane(second.id, "vertical");
      terminals.splitPane(first, "vertical");
      await until(() => document.querySelectorAll(".xterm-screen").length >= 4, "four xterm panes", wait);
      paneIds = terminals.paneIds.filter((id) => id.startsWith("terminal-"));
      if (paneIds.length !== 4) throw new Error(`expected 4 local panes, got ${paneIds.join(",")}`);
      for (const id of paneIds) await alive(id);
    });
    await measure(`4 个终端同时输出各 ${fixture.localLines} 行`, () => flood(paneIds, fixture.localLines));
    await measure("4 个终端输出后空闲 3 秒", () => pause(3000));

    let sshPaneId = "";
    await measure("在终端中新开 SSH 窗格并连上", async () => {
      const pane = terminals.splitSshPane(terminals.activePaneId, "horizontal", { profile, credentials: { password: ssh.password } });
      if (!pane) throw new Error("could not split an SSH pane");
      sshPaneId = pane.id;
      await alive(sshPaneId);
    });
    if (sshPaneId) {
      await measure(`SSH 终端输出 ${fixture.sshLines} 行`, () => flood([sshPaneId], fixture.sshLines));
    }
  } finally {
    workbench.closeTab(tab.id, true);
  }

  // SFTP: the profile's default path is a directory with thousands of files.
  sshStore.saveProfile(profile);
  await sshStore.saveCredentials(profile.id, { password: ssh.password });
  const sftpTab = workbench.openView("sftp");
  try {
    await measure(`SFTP 连接并列出 ${fixture.remoteFiles} 个文件的目录`, async () => {
      await router.push({ name: "sftp", query: { profile: profile.id } });
      await until(() => document.querySelectorAll(".sftp-file-pane--remote .sftp-file-row").length > 10, "remote rows", wait * 2);
    });
    await measure("远程文件列表滚动到底再回顶部", async () => {
      const element = await until(() => visible<HTMLElement>(".sftp-file-pane--remote .sftp-file-list .el-scrollbar__wrap"), "remote list scroller", wait);
      const span = element.scrollHeight - element.clientHeight;
      for (let i = 0; i <= 40; i++) { element.scrollTop = (span * i) / 40; await nextFrame(); }
      for (let i = 40; i >= 0; i--) { element.scrollTop = (span * i) / 40; await nextFrame(); }
    });
    if (fixture.uploadFolder && fixture.uploadFiles) {
      const folder = fixture.uploadFolder;
      const localRow = () => [...document.querySelectorAll<HTMLElement>(".sftp-file-pane--local .sftp-file-row")].find((row) => row.textContent?.includes(folder));
      const statuses = () => [...document.querySelectorAll<HTMLElement>(".sftp-transfer-row__status")].map((node) => [...node.classList].find((name) => name.startsWith("is-")) ?? "");
      await measure(`上传含 ${fixture.uploadFiles} 个文件的文件夹到 ${fixture.remoteFiles} 个文件的目录`, async () => {
        (await until(localRow, "local upload folder", wait)).click();
        await nextFrame();
        (await until(() => visible<HTMLButtonElement>(".sftp-file-pane--local .sftp-pathbar button[aria-label='上传']") ?? undefined, "upload button", wait)).click();
        await until(() => statuses().length > 0, "transfer rows", wait);
        await until(() => statuses().length > 0 && statuses().every((status) => status === "is-completed"), "uploads completed", wait * 6);
        await until(() => [...document.querySelectorAll<HTMLElement>(".sftp-file-pane--remote .sftp-file-row")].some((row) => row.textContent?.includes(folder)), "uploaded folder listed", wait);
      });
    }
    await measure("SFTP 页空闲 5 秒", () => pause(5000));
  } finally {
    if (sftpTab) workbench.closeTab(sftpTab.id, true);
    await router.push({ name: "dashboard" }).catch(() => undefined);
    sshStore.removeProfile(profile.id);
  }
  await measure("离开 SFTP 后空闲 5 秒", () => pause(5000));
}
