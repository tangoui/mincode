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
  const { projectSidebarScenario } = await import("@/services/e2e-project-sidebar");
  await projectSidebarScenario(log);
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

interface BrowserSmokePage {
  marker: string;
  input: string;
  url: string;
  width: number;
  height: number;
  scale: number;
  hasHostBridge: boolean;
  storage: string | null;
}

async function browserScenario(config: SmokeConfig, log: StepLog) {
  const fixture = config.browser;
  if (!fixture) throw new Error("browser fixture missing");
  if (!await invokeBackend<boolean>("embedded_browser_supported")) throw new Error("embedded browser is not supported in this runtime");
  // Windows keeps the WebView2/GDI assertions; macOS (WKWebView) skips the GDI
  // region capture and adds the browser-use chain (e2e-browser-use.ts).
  const windows = /Windows/i.test(navigator.userAgent);
  const macos = /Macintosh|Mac OS X/i.test(navigator.userAgent);
  if (!windows && !macos) throw new Error("browser smoke requires Windows WebView2/GDI or macOS WKWebView");
  if (macos && !fixture.use) throw new Error("macOS browser smoke needs the browser-use fixture pages");
  const inspectPNG = async (data: string) => {
    const bytes = Uint8Array.from(atob(data), (character) => character.charCodeAt(0));
    const signature = [137, 80, 78, 71, 13, 10, 26, 10];
    if (bytes.length < 33 || signature.some((byte, index) => bytes[index] !== byte)) throw new Error("capture did not return a PNG");
    const header = new DataView(bytes.buffer);
    const image = await createImageBitmap(new Blob([bytes], { type: "image/png" }));
    try {
      if (image.width !== header.getUint32(16) || image.height !== header.getUint32(20)) throw new Error("decoded PNG dimensions differ from IHDR");
      const canvas = document.createElement("canvas");
      canvas.width = image.width;
      canvas.height = image.height;
      const context = canvas.getContext("2d");
      if (!context) throw new Error("PNG pixel validation requires a canvas context");
      context.drawImage(image, 0, 0);
      const [red = 0, green = 0, blue = 0, alpha = 0] = context.getImageData(4, 4, 1, 1).data;
      if (Math.abs(red - 232) > 4 || Math.abs(green - 245) > 4 || Math.abs(blue - 255) > 4 || alpha !== 255) throw new Error(`PNG does not contain the fixture background pixel: rgba(${red},${green},${blue},${alpha}) ${image.width}x${image.height}`);
      return { width: image.width, height: image.height };
    } finally { image.close(); }
  };
  const first = `e2e-browser-first-${config.nonce}`;
  const second = `e2e-browser-second-${config.nonce}`;
  const bounds = { x: 48, y: 100, width: 640, height: 360 };
  const evaluate = <T>(tabId: string, script: string) => invokeBackend<T>("embedded_browser_eval", { payload: { tabId, script, timeoutMs: 3000 } });
  // chrome.webview.postMessage is a WebView2 JS stub even when messages are
  // disabled. Native smoke verifies IsWebMessageEnabled and no delivered host
  // event; here only the actual tango bridge symbols identify a leaked app bridge.
  const readPage = (tabId: string) => evaluate<BrowserSmokePage>(tabId, `({marker:document.querySelector('#marker')?.textContent,input:document.querySelector('#state')?.value,url:location.href,width:innerWidth,height:innerHeight,scale:devicePixelRatio,hasHostBridge:typeof window.__tango!=='undefined'||typeof window.tango!=='undefined'||typeof window.__tangoNative!=='undefined',storage:localStorage.getItem('e2e-browser')})`);
  const aiTool = <T>(toolName: string, tabId = first) => invokeBackend<T>("embedded_browser_ai_execute", { payload: { toolName, arguments: { tabId, timeoutMs: 10000 } } });
  const share = (enabled: boolean, tabId = first) => invokeBackend("embedded_browser_set_ai_access", { payload: { enabled, tabId } });
  const until = async <T>(read: () => Promise<T>, matches: (value: T) => boolean, what: string): Promise<T> => {
    const deadline = Date.now() + Math.min(config.timeoutMs, 15000);
    do {
      const value = await read();
      if (matches(value)) return value;
      await delay(100);
    } while (Date.now() < deadline);
    throw new Error(`browser did not report ${what}`);
  };
  const reject = async (operation: () => Promise<unknown>, pattern: RegExp, what: string) => {
    let message = "";
    try { await operation(); } catch (error) { message = nativeErrorMessage(error); }
    if (!pattern.test(message)) throw new Error(`${what}: expected a real rejection, got ${message || "success"}`);
  };
  // browser.* tool failures carry {"code","message","details"} JSON; anything else is plain text without a code.
  const rejectCode = async (operation: () => Promise<unknown>, code: string, what: string) => {
    let message = "";
    try { await operation(); } catch (error) { message = nativeErrorMessage(error); }
    let actual: string;
    try { actual = String((JSON.parse(message) as { code?: unknown }).code ?? ""); } catch { actual = ""; }
    if (actual !== code) throw new Error(`${what}: expected error code ${code}, got ${message || "success"}`);
  };
  try {
    await invokeBackend("embedded_browser_mount", { payload: { tabId: first, url: fixture.firstUrl, ...bounds } });
    if (!await invokeBackend<boolean>("embedded_browser_exists")) throw new Error("mount did not create an overlay");
    const ready = await invokeBackend<{ url: string; title: string }>("embedded_browser_wait_ready", {
      payload: { tabId: first, expectedUrl: fixture.firstUrl, timeoutMs: config.timeoutMs },
    });
    const initial = await readPage(first);
    if (ready.url !== fixture.firstUrl || ready.title !== `MinCode first ${config.nonce}` || initial.marker !== `first-${config.nonce}` || initial.hasHostBridge) {
      throw new Error(`first fixture DOM/URL or host bridge isolation mismatch: ${JSON.stringify({ ready, expected: fixture.firstUrl, initial })}`);
    }
    if (await invokeBackend<string>("embedded_browser_get_url", { payload: { tabId: first } }) !== fixture.firstUrl) throw new Error("native URL does not match the fixture");
    log.done("mount → exists → ready → real fixture DOM/URL; remote page has no desktop bridge");
    // WebView2 reports the page's exception text; WKWebView only says that one occurred.
    await reject(() => evaluate(first, `(()=>{throw new Error('e2e-eval-${config.nonce}')})()`), windows ? new RegExp(`e2e-eval-${config.nonce}`) : new RegExp(`e2e-eval-${config.nonce}|JavaScript exception`, "i"), "eval exception");
    await evaluate(first, `(()=>{document.querySelector('#state').value=${JSON.stringify(config.nonce)};localStorage.setItem('e2e-browser',${JSON.stringify(config.nonce)});return true})()`);
    for (const url of ["javascript:document.title='unexpected'", "file:///mincode-e2e-blocked.html"]) {
      await reject(() => invokeBackend("embedded_browser_navigate", { payload: { tabId: first, url } }), /协议|scheme|http|URL/i, "unsafe scheme navigation");
    }
    await invokeBackend("embedded_browser_mount", { payload: { tabId: second, url: fixture.secondUrl, ...bounds } });
    await invokeBackend("embedded_browser_wait_ready", { payload: { tabId: second, expectedUrl: fixture.secondUrl, timeoutMs: config.timeoutMs } });
    const other = await readPage(second);
    if (other.marker !== `second-${config.nonce}` || other.input !== "initial-second" || other.hasHostBridge || other.storage !== config.nonce) throw new Error("second tab DOM/isolation mismatch");
    await invokeBackend("embedded_browser_activate", { payload: { tabId: first } });
    if ((await readPage(first)).input !== config.nonce) throw new Error("tab switch recreated the first DOM");
    log.done("strict CSP eval works; two native tabs retain DOM/localStorage; JavaScript errors and unsafe schemes reject");

    await invokeBackend("embedded_browser_set_zoom", { payload: { tabId: first, scale: 1.25 } });
    await until(() => readPage(first), (page) => page.width < initial.width && page.scale > initial.scale, "zoomed viewport");
    await invokeBackend("embedded_browser_set_zoom", { payload: { tabId: first, scale: 1 } });
    await until(() => readPage(first), (page) => Math.abs(page.width - initial.width) <= 2, "restored zoom");
    const resized = { ...bounds, width: 560, height: 300 };
    await invokeBackend("embedded_browser_sync_bounds", { payload: { tabId: first, ...resized, visible: true } });
    await until(() => readPage(first), (page) => Math.abs(page.width - resized.width) <= 2 && Math.abs(page.height - resized.height) <= 2, "resized viewport");
    await invokeBackend("embedded_browser_hide");
    if (!await invokeBackend<boolean>("embedded_browser_exists")) throw new Error("hide destroyed the native tabs");
    await invokeBackend("embedded_browser_activate", { payload: { tabId: first } });
    if ((await readPage(first)).input !== config.nonce) throw new Error("hide/activate lost DOM state");
    log.done("zoom and bounds change real viewport; hide/activate preserves the native tab");

    await share(false);
    await rejectCode(() => aiTool("browser.capture_view"), "browser_tab_not_shared", "unshared capture");
    await share(true);
    await rejectCode(() => aiTool("browser.capture_view", second), "browser_tab_not_shared", "unshared tab capture");
    const capture = await aiTool<{ mimeType: string; data: string; width: number; height: number; tabId: string; coordinateFrameAvailable: boolean }>("browser.capture_view");
    const decoded = await inspectPNG(capture.data);
    if (capture.mimeType !== "image/png" || capture.tabId !== first || decoded.width !== capture.width || decoded.height !== capture.height || !capture.coordinateFrameAvailable) throw new Error("capture geometry does not match decoded PNG");
    log.done(`explicitly shared browser.capture_view returned fixture pixels in PNG ${capture.width}×${capture.height}; unshared/cross-tab requests rejected`);
    if (windows) {
      await invokeBackend("focus_main_window");
      // 只截取新测试宿主内的 20×20 fixture 背景，不捕获任意桌面区域。
      // DOM readiness and CapturePreview do not wait for the desktop compositor.
      // Require actual fixture pixels, retrying while Windows presents the frame.
      const regionSize = await until(async () => {
        const region = await invokeBackend<string>("embedded_browser_screenshot", { payload: { x: resized.x + 4, y: resized.y + 4, width: 20, height: 20 } });
        try { return await inspectPNG(region); } catch { return null; }
      }, (size) => size !== null, "visible GDI fixture pixels");
      if (!regionSize) throw new Error("visible fixture region unavailable");
      if (regionSize.width < 20 || regionSize.height < 20 || regionSize.width > 80 || regionSize.height > 80) throw new Error("unexpected fixture region PNG dimensions");
      log.done(`GDI captured only the 20×20 logical fixture region (${regionSize.width}×${regionSize.height} PNG) with matching background pixels`);
    }

    await invokeBackend("embedded_browser_close_tab", { payload: { tabId: second } });
    await reject(() => evaluate(second, "location.href"), /not mounted|not found|closed|不存在/i, "closed tab eval");
    if ((await readPage(first)).input !== config.nonce) throw new Error("closing the second tab retired the first tab");
    log.done("close_tab rejects further eval while the remaining native tab stays usable");
    await invokeBackend("embedded_browser_navigate", { payload: { tabId: first, url: fixture.errorUrl } });
    const errors = await until(() => aiTool<Array<{ message?: string; url?: string }>>("browser.get_navigation_errors"), (items) => items.some((item) => Boolean(item.message)), "native navigation failure");
    if (!errors.some((item) => item.url === fixture.errorUrl)) throw new Error("navigation diagnostics do not identify the failed fixture URL");
    await reject(() => invokeBackend("embedded_browser_wait_ready", { payload: { tabId: first, expectedUrl: fixture.errorUrl, previousUrl: fixture.firstUrl, timeoutMs: 2000 } }), windows ? /browser navigation failed:/i : /browser navigation failed:|did not become ready/i, "failed navigation readiness");
    // macOS keeps a cumulative failure list, so its readiness wait times out instead of failing fast.
    log.done("loopback connection reset surfaced as native navigation failure and rejected readiness");
    await share(false);
    await invokeBackend("embedded_browser_close");
    if (await invokeBackend<boolean>("embedded_browser_exists")) throw new Error("overlay still exists after close");
    log.done("close_tab rejects further eval; close retires all native overlays and exists=false");
  } finally {
    await share(false).catch(() => undefined);
    await invokeBackend("embedded_browser_close").catch(() => undefined);
  }
  if (macos) {
    const { browserUseScenario } = await import("@/services/e2e-browser-use");
    await browserUseScenario(config, log);
  }
}

const SCENARIOS: Record<SmokeScenario, (config: SmokeConfig, log: StepLog) => Promise<void>> = {
  "file-templates": async(config,log)=>{const {fileTemplatesScenario}=await import("@/services/e2e-file-templates");await fileTemplatesScenario(config,log);},
  "ai-prompts": async (config, log) => { const { aiPromptsScenario } = await import("@/services/e2e-ai-prompts"); await aiPromptsScenario(config, log); },
  "rust-plugin": (config, log) => pluginScenario(config, log, config.rustPlugin!),
  "ui-runtime": async (config, log) => { const { uiRuntimeScenario } = await import("@/services/e2e-ui-runtime"); await uiRuntimeScenario(config, log); },
  "ts-plugin": async (config, log) => { const { tsPluginScenario } = await import("@/services/e2e-ts-plugin"); await tsPluginScenario(config, log); },
  "port-manager": async (config, log) => { const { portManagerScenario } = await import("@/services/e2e-port-manager"); await portManagerScenario(config, log); },
  services: async (config, log) => { const { servicesScenario } = await import("@/services/e2e-services"); await servicesScenario(config, log); },
  "terminal-attention": async (config, log) => { const { terminalAttentionScenario } = await import("@/services/e2e-terminal-attention"); await terminalAttentionScenario(config, log); },
  "terminal-tabs": async (config, log) => { const { terminalTabsScenario } = await import("@/services/e2e-terminal-tabs"); await terminalTabsScenario(config, log); },
  "terminal-size": async (config, log) => { const { terminalSizeScenario } = await import("@/services/e2e-terminal-size"); await terminalSizeScenario(config, log); },
  "large-text": async (config, log) => { const { largeTextScenario } = await import("@/services/e2e-large-text"); await largeTextScenario(config, log); },
  perf: async (config, log) => { const { perfScenario } = await import("@/services/e2e-perf"); await perfScenario(config, log); },
  "perf-database": async (config, log) => { const { perfDatabaseScenario } = await import("@/services/e2e-perf-database"); await perfDatabaseScenario(config, log); },
  "perf-http": async (config, log) => { const { perfHttpScenario } = await import("@/services/e2e-perf-http"); await perfHttpScenario(config, log); },
  "perf-ai": async (config, log) => { const { perfAiScenario } = await import("@/services/e2e-perf-ai"); await perfAiScenario(config, log); },
  "perf-remote": async (config, log) => { const { perfRemoteScenario } = await import("@/services/e2e-perf-remote"); await perfRemoteScenario(config, log); },
  "perf-sessions": async (config, log) => { const { perfSessionsScenario } = await import("@/services/e2e-perf-sessions"); await perfSessionsScenario(config, log); },
  "perf-git": async (config, log) => { const { perfGitScenario } = await import("@/services/e2e-perf-git"); await perfGitScenario(config, log); },
  "perf-pages": async (config, log) => { const { perfPagesScenario } = await import("@/services/e2e-perf-pages"); await perfPagesScenario(config, log); },
  "perf-plugins": async (config, log) => { const { perfPluginsScenario } = await import("@/services/e2e-perf-plugins"); await perfPluginsScenario(config, log); },
  "perf-plugin-startup": async (config, log) => { const { perfPluginStartupScenario } = await import("@/services/e2e-perf-plugin-startup"); await perfPluginStartupScenario(config, log); },
  lsp: async (config, log) => { const { lspScenario } = await import("@/services/e2e-lsp"); await lspScenario(config, log); },
  laravel: async (config, log) => { const { laravelScenario } = await import("@/services/e2e-laravel"); await laravelScenario(config, log); },
  "git-ai-commit": async (config, log) => { const { gitAiCommitScenario } = await import("@/services/e2e-git-ai-commit"); await gitAiCommitScenario(config, log); },
  linters: async (config, log) => { const { lintersScenario } = await import("@/services/e2e-linters"); await lintersScenario(config, log); },
  "ai-sidebar": async (_config, log) => { const { aiSidebarScenario } = await import("@/services/e2e-ai-sidebar"); await aiSidebarScenario(log); },
  "mail-client": async(config,log)=>{const {mailClientScenario}=await import("@/services/e2e-mail-client");await mailClientScenario(config,log);},
  formatter: async (config, log) => { const { formatterScenario } = await import("@/services/e2e-formatter"); await formatterScenario(config, log); },
  browser: browserScenario,
  "ai-catalogs": async (config, log) => { const { aiCatalogsScenario } = await import("@/services/e2e-ai-catalogs"); await aiCatalogsScenario(config, log); },
  ebook: async (config, log) => { const { ebookScenario } = await import("@/services/e2e-ebook"); await ebookScenario(config, log); },
  debugger: async (config, log) => { const { debuggerScenario } = await import("@/services/e2e-release-checks"); await debuggerScenario(config, log); },
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
