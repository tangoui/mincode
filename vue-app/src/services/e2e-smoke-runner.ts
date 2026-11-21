/**
 * Desktop smoke scenarios. Each one drives a core chain through the real
 * WebView → desktop IPC → Go → native path and the backend events coming back,
 * exactly as the product UI does: local PTY terminal, SSH shell, SQLite
 * database session, local plugin install/run/uninstall, and AI streaming with
 * cancellation. Loaded only by services/e2e-smoke.ts in `-tags e2e` builds.
 */
import {
  echoProbe,
  parseSmokeConfig,
  plainTerminalText,
  type SmokeConfig,
  type SmokeReport,
  type SmokeResult,
  type SmokeScenario,
} from "@/domain/e2e/desktop-smoke";
import { shellOutputEventName, type ShellEndedEvent, type ShellOutputEvent } from "@/domain/ssh/types";
import { createTerminalInputBatcher, isImmediateTerminalInput } from "@/domain/terminal/input-batcher";
import type { DatabaseQueryResult } from "@/domain/database/types";
import type { SqlSafetyCheck } from "@/domain/database/sql-safety";
import { runWithSafety, sqlSafetyArgs } from "@/services/sql-safety-service";
import { invokePlugin, listPlugins, previewLocalPlugin, installLocalPlugin, setPluginEnabled, uninstallPlugin } from "@/services/plugin-service";
import { invokeBackend, listenAppEvent, nativeErrorMessage } from "@/services/desktop-bridge";

class StepLog {
  readonly steps: string[] = [];
  done(step: string) {
    this.steps.push(step);
  }
}

function delay(ms: number) {
  return new Promise<void>((resolve) => setTimeout(resolve, ms));
}

function withTimeout<T>(promise: Promise<T>, ms: number, what: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return Promise.race([
    promise.finally(() => clearTimeout(timer)),
    new Promise<never>((_, reject) => {
      timer = setTimeout(() => {
        // A hidden (minimised) WebView pauses rendering, so layout waits never finish.
        const hidden = document.visibilityState === "hidden" ? " (window hidden: rendering was paused, keep the smoke window visible)" : "";
        reject(new Error(`timed out after ${ms} ms: ${what}${hidden}`));
      }, ms);
    }),
  ]);
}

function randomId(prefix: string) {
  return `${prefix}-${Math.random().toString(36).slice(2, 10)}`;
}

/** Collects one shell session's output event stream for the life of a scenario. */
function captureShell(sessionId: string) {
  let output = "";
  let ended: ShellEndedEvent | undefined;
  const waiters = new Set<() => void>();
  const notify = () => {
    for (const waiter of [...waiters]) waiter();
  };
  const stopOutput = listenAppEvent<ShellOutputEvent>(shellOutputEventName(sessionId), (event) => {
    if (event?.sessionId && event.sessionId !== sessionId) return;
    output += typeof event?.data === "string" ? event.data : "";
    notify();
  });
  const stopEnded = listenAppEvent<ShellEndedEvent>("shell-session-ended", (event) => {
    if (event?.sessionId !== sessionId) return;
    ended = event;
    notify();
  });
  const until = (predicate: () => boolean, ms: number, what: string) =>
    withTimeout(new Promise<void>((resolve) => {
      const check = () => {
        if (!predicate()) return;
        waiters.delete(check);
        resolve();
      };
      waiters.add(check);
      check();
    }), ms, what);
  return {
    text: () => plainTerminalText(output),
    waitFor: (expected: string, ms: number) => until(() => plainTerminalText(output).includes(expected), ms, `terminal output "${expected}"`),
    waitEnded: (ms: number) => until(() => ended !== undefined, ms, "shell-session-ended event"),
    dispose() {
      stopOutput();
      stopEnded();
      waiters.clear();
    },
  };
}

async function writeLine(sessionId: string, line: string) {
  await invokeBackend<void>("write_to_shell", { payload: { sessionId, input: `${line}\r` } });
}

async function checkMacKeyboardInput(sessionId: string, shell: ReturnType<typeof captureShell>, timeoutMs: number, log: StepLog) {
  if (!/Mac/i.test(navigator.platform)) return;
  const { Terminal } = await import("@xterm/xterm");
  const terminal = new Terminal();
  const host = document.createElement("div");
  host.style.cssText = "position:fixed;left:-10000px;width:640px;height:400px";
  document.body.append(host);
  const batcher = createTerminalInputBatcher((input) => invokeBackend<void>("write_to_shell", { payload: { sessionId, input } }));
  const writes: Promise<void>[] = [];
  const subscription = terminal.onData((input) => writes.push(batcher.push(input, isImmediateTerminalInput(input))));
  try {
    terminal.open(host);
    const textarea = terminal.textarea!;
    const key = (type: string, value: string, keyCode: number) => {
      const event = new KeyboardEvent(type, { key: value, bubbles: true, cancelable: true });
      Object.defineProperty(event, "keyCode", { value: keyCode });
      textarea.dispatchEvent(event);
    };
    const insert = (data: string) => {
      textarea.value += data;
      textarea.dispatchEvent(new InputEvent("input", { data, inputType: "insertText", composed: true, bubbles: true }));
    };
    const marker = randomId("MINCODE_TYPED");
    await writeLine(sessionId, `printf '%s%s\\n' '${marker}_' ready; IFS= read -r typed; printf '%s%s:%s:END\\n' '${marker}_' result "$typed"`);
    await shell.waitFor(`${marker}_ready`, timeoutMs);
    for (let pair = 0; pair < 64; pair++) {
      insert("c");
      key("keydown", "c", 229);
      insert("d");
      key("keydown", "d", 229);
      key("keyup", "c", 67);
      key("keyup", "d", 68);
    }
    key("keydown", "Enter", 13);
    await Promise.all(writes);
    await shell.waitFor(`${marker}_result:${"cd".repeat(64)}:END`, timeoutMs);
    log.done("128 rapid IME input bytes reached the PTY in order");
  } finally {
    subscription.dispose();
    batcher.dispose();
    terminal.dispose();
    host.remove();
  }
}

async function closeShell(sessionId: string) {
  await invokeBackend<void>("close_local_shell", { payload: { sessionId } });
}

async function terminalScenario(config: SmokeConfig, log: StepLog) {
  const sessionId = randomId("e2e-local");
  const shell = captureShell(sessionId);
  try {
    await invokeBackend<string>("start_local_shell", { payload: { sessionId, cols: 100, rows: 30 } });
    log.done("start_local_shell");
    const probe = echoProbe(config.nonce, "LOCAL");
    await writeLine(sessionId, probe.command);
    await shell.waitFor(probe.expected, config.timeoutMs);
    log.done("command output streamed back over shell-output");
    await checkMacKeyboardInput(sessionId, shell, config.timeoutMs, log);
    await invokeBackend<void>("resize_local_shell", { payload: { sessionId, cols: 123, rows: 41 } });
    await writeLine(sessionId, "stty size");
    await shell.waitFor("41 123", config.timeoutMs);
    log.done("resize reached the PTY");
    await closeShell(sessionId);
    const alive = await invokeBackend<boolean>("shell_session_alive", { payload: { sessionId } });
    if (alive) throw new Error("local shell still alive after close_local_shell");
    log.done("close_local_shell retired the session");
  } finally {
    shell.dispose();
    await closeShell(sessionId).catch(() => undefined);
  }
}

async function sshScenario(config: SmokeConfig, log: StepLog) {
  const ssh = config.ssh!;
  const sessionId = randomId("e2e-ssh");
  const shell = captureShell(sessionId);
  try {
    await invokeBackend<string>("start_ssh_shell", {
      payload: {
        sessionId,
        profileId: sessionId,
        host: ssh.host,
        port: ssh.port,
        username: ssh.username,
        attachIfExists: false,
        authType: "password",
        password: ssh.password,
        postConnectCommands: [],
        algorithmPolicy: "modern",
        keepAliveIntervalSeconds: 30,
        keepAliveMaxMisses: 3,
        reconnectEnabled: false,
        reconnectMaxAttempts: 0,
        reconnectBaseDelayMs: 1000,
        reconnectMaxDelayMs: 1000,
        cols: 100,
        rows: 30,
      },
    });
    log.done("start_ssh_shell (password auth, TOFU host key)");
    const probe = echoProbe(config.nonce, "SSH");
    await writeLine(sessionId, probe.command);
    await shell.waitFor(probe.expected, config.timeoutMs);
    log.done("remote command output streamed back");
    await checkMacKeyboardInput(sessionId, shell, config.timeoutMs, log);
    await invokeBackend<void>("resize_local_shell", { payload: { sessionId, cols: 111, rows: 33 } });
    await writeLine(sessionId, "stty size");
    await shell.waitFor("33 111", config.timeoutMs);
    log.done("window-change reached the remote PTY");
    await closeShell(sessionId);
    const alive = await invokeBackend<boolean>("shell_session_alive", { payload: { sessionId } });
    if (alive) throw new Error("SSH session still alive after close");
    log.done("disconnect retired the session");
  } finally {
    shell.dispose();
    await closeShell(sessionId).catch(() => undefined);
  }
}

async function databaseScenario(config: SmokeConfig, log: StepLog) {
  const sessionId = await invokeBackend<string>("db_connect", {
    config: { dbType: "sqlite", host: "", port: 0, filePath: config.database!.filePath },
  });
  log.done("db_connect sqlite");
  // The isolated harness cannot click dialogs: it confirms each SQL safety check itself.
  const confirmInHarness = async (check: SqlSafetyCheck) => (check.risk === "high" ? { confirmText: check.confirmTarget } : {});
  const query = async (sql: string) => {
    const result = await runWithSafety({
      sessionId, query: sql, source: "e2e", origin: "preconfirmed", confirm: confirmInHarness,
      execute: (token) => invokeBackend<DatabaseQueryResult>("db_execute_query", {
        sessionId, query: sql, queryId: randomId("e2e-query"), source: "e2e", ...sqlSafetyArgs(token),
      }),
    });
    if (!result) throw new Error("e2e SQL safety confirmation declined");
    return result;
  };
  try {
    await query("CREATE TABLE e2e_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)");
    await query(`INSERT INTO e2e_items (name) VALUES ('alpha'), ('${config.nonce}')`);
    log.done("DDL and insert executed");
    const result = await query("SELECT id, name FROM e2e_items ORDER BY id");
    const names = result.rows.map((row) => row[1]);
    if (result.rowCount !== 2 || names[1] !== config.nonce || !result.columns.includes("name")) {
      throw new Error(`unexpected query result: ${JSON.stringify({ columns: result.columns, rows: result.rows })}`);
    }
    log.done("select returned the inserted rows");
    let rejected = "";
    try {
      await query("SELECT * FROM e2e_missing_table");
    } catch (error) {
      rejected = nativeErrorMessage(error);
    }
    if (!/no such table/i.test(rejected)) throw new Error(`invalid SQL was not reported as a real error: "${rejected}"`);
    log.done("backend error surfaced to the WebView");
    const { databaseTransfersScenario } = await import("@/services/e2e-database-transfers");
    await databaseTransfersScenario(config.database!.filePath, sessionId, log);
  } finally {
    await invokeBackend<void>("db_disconnect", { sessionId }).catch(() => undefined);
  }
  log.done("db_disconnect");
  const { databaseSplitScenario } = await import("@/services/e2e-database-split");
  await databaseSplitScenario(config.database!.filePath, log);
  const { databaseObjectToolbarScenario } = await import("@/services/e2e-database-object-toolbar");
  await databaseObjectToolbarScenario(config.database!.filePath, log);
}

async function pluginScenario(config: SmokeConfig, log: StepLog, fixture = config.plugin!) {
  const { packagePath, pluginId } = fixture;
  const preview = await previewLocalPlugin(packagePath);
  if (preview.manifest.id !== pluginId) throw new Error(`preview reported ${preview.manifest.id}`);
  if ((await listPlugins()).some((plugin) => plugin.id === pluginId)) throw new Error("preview installed the plugin");
  log.done("read-only preflight");
  await installLocalPlugin(packagePath);
  try {
    if (!(await listPlugins()).some((plugin) => plugin.id === pluginId)) throw new Error("installed plugin is not listed");
    log.done("install");
    await setPluginEnabled(pluginId, true);
    log.done("enable");
    const result = await invokePlugin(pluginId, "hello", JSON.stringify({ name: config.nonce }));
    const message = (result as { message?: unknown } | null)?.message;
    if (message !== `Hello, ${config.nonce}!`) throw new Error(`unexpected plugin result: ${JSON.stringify(result)}`);
    log.done("process plugin answered over JSON-RPC");
    await setPluginEnabled(pluginId, false);
    log.done("disable");
  } finally {
    await uninstallPlugin(pluginId, true).catch(() => undefined);
  }
  if ((await listPlugins()).some((plugin) => plugin.id === pluginId)) throw new Error("plugin still listed after uninstall");
  log.done("uninstall");
}

interface StreamEvent {
  type?: string;
  text?: string;
  message?: string;
}

async function aiScenario(config: SmokeConfig, log: StepLog) {
  const ai = config.ai!;
  const providerConfig = { baseUrl: ai.baseUrl, apiKey: ai.apiKey, model: ai.model, apiFormat: "openai" };

  const channelId = randomId("e2e-ai");
  const chunks: string[] = [];
  let done = false;
  let streamError = "";
  const stop = listenAppEvent<StreamEvent>(`stream:${channelId}`, (event) => {
    if (event?.type === "chunk" && typeof event.text === "string") chunks.push(event.text);
    else if (event?.type === "done") done = true;
    else if (event?.type === "error") streamError = String(event.message ?? "stream error");
  });
  try {
    await withTimeout(invokeBackend("ai_stream_chat", {
      channelId, config: providerConfig, messages: [{ role: "user", content: `smoke ${config.nonce}` }],
    }), config.timeoutMs, "ai_stream_chat");
    await delay(100);
  } finally {
    stop();
  }
  if (streamError) throw new Error(`stream error event: ${streamError}`);
  if (!done) throw new Error("no done event");
  if (chunks.join("") !== ai.expectedText) throw new Error(`streamed text mismatch: ${JSON.stringify(chunks.join(""))}`);
  if (chunks.length < 2) throw new Error("response arrived as a single event; streaming was not incremental");
  log.done(`streamed ${chunks.length} chunk events and done`);

  const cancelChannel = randomId("e2e-ai-cancel");
  let cancelChunks = 0;
  let firstChunk!: () => void;
  const started = new Promise<void>((resolve) => {
    firstChunk = resolve;
  });
  const stopCancel = listenAppEvent<StreamEvent>(`stream:${cancelChannel}`, (event) => {
    if (event?.type !== "chunk") return;
    cancelChunks++;
    firstChunk();
  });
  try {
    const pending = invokeBackend("ai_stream_chat", {
      channelId: cancelChannel, config: providerConfig, messages: [{ role: "user", content: ai.slowPrompt }],
    }).catch(() => undefined);
    await withTimeout(started, config.timeoutMs, "first chunk of the slow stream");
    const cancelledAt = Date.now();
    await invokeBackend("ai_stream_cancel", { channelId: cancelChannel });
    await withTimeout(pending, 5000, "ai_stream_chat to settle after cancel");
    const settleMs = Date.now() - cancelledAt;
    const seen = cancelChunks;
    await delay(500);
    if (cancelChunks !== seen) throw new Error("chunks kept arriving after cancellation");
    log.done(`cancel settled in ${settleMs} ms after ${seen} chunk events`);
  } finally {
    stopCancel();
  }
}

const SCENARIOS: Record<SmokeScenario, (config: SmokeConfig, log: StepLog) => Promise<void>> = {
  "ai-prompts": async (config, log) => { const { aiPromptsScenario } = await import("@/services/e2e-ai-prompts"); await aiPromptsScenario(config, log); },
  "rust-plugin": (config, log) => pluginScenario(config, log, config.rustPlugin!),
  "ui-runtime": async (config, log) => { const { uiRuntimeScenario } = await import("@/services/e2e-ui-runtime"); await uiRuntimeScenario(config, log); },
  "ts-plugin": async (config, log) => { const { tsPluginScenario } = await import("@/services/e2e-ts-plugin"); await tsPluginScenario(config, log); },
  "port-manager": async (config, log) => { const { portManagerScenario } = await import("@/services/e2e-port-manager"); await portManagerScenario(config, log); },
  services: async (config, log) => { const { servicesScenario } = await import("@/services/e2e-services"); await servicesScenario(config, log); },
  "terminal-attention": async (config, log) => { const { terminalAttentionScenario } = await import("@/services/e2e-terminal-attention"); await terminalAttentionScenario(config, log); },
  "terminal-tabs": async (config, log) => { const { terminalTabsScenario } = await import("@/services/e2e-terminal-tabs"); await terminalTabsScenario(config, log); },
  "terminal-size": async (config, log) => { const { terminalSizeScenario } = await import("@/services/e2e-terminal-size"); await terminalSizeScenario(config, log); },
  "perf-database": async (config, log) => { const { perfDatabaseScenario } = await import("@/services/e2e-perf-database"); await perfDatabaseScenario(config, log); },
  "perf-http": async (config, log) => { const { perfHttpScenario } = await import("@/services/e2e-perf-http"); await perfHttpScenario(config, log); },
  "perf-ai": async (config, log) => { const { perfAiScenario } = await import("@/services/e2e-perf-ai"); await perfAiScenario(config, log); },
  "perf-remote": async (config, log) => { const { perfRemoteScenario } = await import("@/services/e2e-perf-remote"); await perfRemoteScenario(config, log); },
  "perf-sessions": async (config, log) => { const { perfSessionsScenario } = await import("@/services/e2e-perf-sessions"); await perfSessionsScenario(config, log); },
  "perf-pages": async (config, log) => { const { perfPagesScenario } = await import("@/services/e2e-perf-pages"); await perfPagesScenario(config, log); },
  "perf-plugins": async (config, log) => { const { perfPluginsScenario } = await import("@/services/e2e-perf-plugins"); await perfPluginsScenario(config, log); },
  "perf-plugin-startup": async (config, log) => { const { perfPluginStartupScenario } = await import("@/services/e2e-perf-plugin-startup"); await perfPluginStartupScenario(config, log); },
  "ai-sidebar": async (_config, log) => { const { aiSidebarScenario } = await import("@/services/e2e-ai-sidebar"); await aiSidebarScenario(log); },
  "mail-client": async(config,log)=>{const {mailClientScenario}=await import("@/services/e2e-mail-client");await mailClientScenario(config,log);},
  "ai-catalogs": async (config, log) => { const { aiCatalogsScenario } = await import("@/services/e2e-ai-catalogs"); await aiCatalogsScenario(config, log); },
  ebook: async (config, log) => { const { ebookScenario } = await import("@/services/e2e-ebook"); await ebookScenario(config, log); },
  "sql-safety": async (config, log) => { const { sqlSafetyScenario } = await import("@/services/e2e-release-checks"); await sqlSafetyScenario(config, log); },
  "plugin-providers": async (config, log) => { const { pluginProvidersScenario } = await import("@/services/e2e-release-checks"); await pluginProvidersScenario(config, log); },
  "sftp-transfers": async (config, log) => { const { sftpTransfersScenario } = await import("@/services/e2e-release-checks"); await sftpTransfersScenario(config, log); },
  terminal: terminalScenario,
  ssh: sshScenario,
  database: databaseScenario,
  plugin: pluginScenario,
  ai: aiScenario,
  associations: async (config, log) => {
    const { associationsScenario } = await import("@/services/e2e-associations");
    await associationsScenario(config, log);
  },
};

export async function runDesktopSmoke(rawConfig: unknown): Promise<SmokeReport> {
  const startedAt = new Date().toISOString();
  let config: SmokeConfig;
  try {
    config = parseSmokeConfig(rawConfig);
  } catch (error) {
    return {
      nonce: "", startedAt, finishedAt: new Date().toISOString(), userAgent: navigator.userAgent,
      results: [{ scenario: "terminal", status: "failed", durationMs: 0, steps: [], error: nativeErrorMessage(error) }],
    };
  }
  const results: SmokeResult[] = [];
  // Sequential on purpose: failures stay attributable and scenarios do not
  // compete for the same native domains.
  for (const scenario of config.scenarios) {
    const log = new StepLog();
    const began = performance.now();
    try {
      await withTimeout(SCENARIOS[scenario](config, log), config.timeoutMs * 4, `${scenario} scenario`);
      results.push({ scenario, status: "passed", durationMs: Math.round(performance.now() - began), steps: log.steps });
    } catch (error) {
      results.push({
        scenario, status: "failed", durationMs: Math.round(performance.now() - began), steps: log.steps,
        error: nativeErrorMessage(error),
      });
    }
  }
  return { nonce: config.nonce, startedAt, finishedAt: new Date().toISOString(), userAgent: navigator.userAgent, results };
}
