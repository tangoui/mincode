// Opt-in headless Chrome smoke test for Settings -> Shortcuts (browser preview, no desktop runtime).
// Not part of run-tests.mjs (needs a Vite dev server and Chrome). Run from vue-app/:
//   node scripts/keybinding-settings.e2e.mjs
// CHROME_PATH overrides the browser binary. Records ⌘K/Ctrl+K for the AI assistant, checks the live
// conflict warning, checks that the dispatcher stays paused while recording, saves, then resets the row.
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const mac = process.platform === "darwin";
const chromePath =
  process.env.CHROME_PATH ??
  (mac
    ? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    : process.platform === "win32"
      ? "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe"
      : "google-chrome");

function freePort() {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

async function waitForHttp(url, timeout = 60000) {
  const start = Date.now();
  while (Date.now() - start < timeout) {
    try {
      if ((await fetch(url)).ok) return;
    } catch {
      // not up yet
    }
    await sleep(250);
  }
  throw new Error(`timeout waiting for ${url}`);
}

const vitePort = await freePort();
const cdpPort = await freePort();
const profile = await mkdtemp(join(tmpdir(), "mincode-kb-e2e-"));
const appRoot = fileURLToPath(new URL("..", import.meta.url));
const vite = spawn(process.execPath, [join(appRoot, "node_modules/vite/bin/vite.js"), "--host", "127.0.0.1", "--port", String(vitePort), "--strictPort"], {
  cwd: appRoot,
  stdio: "ignore",
});
const chrome = spawn(chromePath, ["--headless=new", `--remote-debugging-port=${cdpPort}`, `--user-data-dir=${profile}`, "--no-first-run", "--no-default-browser-check", "about:blank"], {
  stdio: "ignore",
});

let ws;
try {
  await waitForHttp(`http://127.0.0.1:${vitePort}/`);
  await waitForHttp(`http://127.0.0.1:${cdpPort}/json/version`);
  await run();
  console.log("keybinding settings e2e: OK");
} catch (error) {
  console.error(error);
  process.exitCode = 1;
} finally {
  ws?.close();
  chrome.kill();
  vite.kill();
  await sleep(300);
  await rm(profile, { recursive: true, force: true }).catch(() => undefined);
}

async function run() {
  const targets = await (await fetch(`http://127.0.0.1:${cdpPort}/json/list`)).json();
  const page = targets.find((target) => target.type === "page");
  ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve) => ws.addEventListener("open", resolve, { once: true }));
  let id = 0;
  const pending = new Map();
  const errors = [];
  ws.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (message.id && pending.has(message.id)) {
      pending.get(message.id)(message);
      pending.delete(message.id);
    } else if (message.method === "Runtime.exceptionThrown") {
      errors.push(message.params.exceptionDetails.exception?.description ?? message.params.exceptionDetails.text);
    }
  });
  const send = (method, params = {}) =>
    new Promise((resolve) => {
      const next = ++id;
      pending.set(next, resolve);
      ws.send(JSON.stringify({ id: next, method, params }));
    });
  const evaluate = async (expression) => {
    const response = await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
    if (response.result?.exceptionDetails) throw new Error(`${expression}\n${JSON.stringify(response.result.exceptionDetails)}`);
    return response.result?.result?.value;
  };
  // The first open compiles lazy chunks in the dev server, so waits are generous.
  const waitFor = async (expression, label, timeout = 60000) => {
    const start = Date.now();
    while (Date.now() - start < timeout) {
      if (await evaluate(expression)) return;
      await sleep(150);
    }
    throw new Error(`timeout: ${label}\nuncaught errors:\n${errors.join("\n")}`);
  };
  const step = (text) => console.log(`- ${text}`);
  const rowOf = (command) => `document.querySelector('[data-testid=keybinding-table] [data-command="${command}"]').closest('tr')`;
  const label = mac ? "⌘K" : "Ctrl+K";

  await send("Runtime.enable");
  await send("Page.enable");
  await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 860, deviceScaleFactor: 1, mobile: false });
  await send("Page.navigate", { url: `http://127.0.0.1:${vitePort}/` });
  const settingsButton = `[...document.querySelectorAll('.app-activity-button[aria-pressed]')].find((e) => /^(设置|Settings)$/.test(e.getAttribute('aria-label') ?? ''))`;
  await waitFor(`!!${settingsButton}`, "app shell");
  await evaluate(`${settingsButton}.click()`);
  const shortcutsNav = `[...document.querySelectorAll('.settings-dialog__nav-item')].find((e) => /^(快捷键|Shortcuts)$/.test(e.textContent.trim()))`;
  await waitFor(`!!${shortcutsNav}`, "settings navigation");
  await evaluate(`${shortcutsNav}.click()`);
  await waitFor(`!!document.querySelector('[data-testid=keybinding-table] [data-command="builtin:ai"]')`, "keybinding table");
  step(`panel loaded with ${await evaluate(`document.querySelectorAll('[data-testid=keybinding-table] [data-command]').length`)} command rows`);

  await evaluate(`${rowOf("builtin:ai")}.querySelector('[data-testid=keybinding-change]').click()`);
  await waitFor(`document.activeElement?.dataset?.testid === 'keybinding-recorder-capture'`, "recorder focus");
  step("recorder opened with the capture box focused");

  const modifiers = mac ? 4 : 2;
  const modifierKey = mac ? { key: "Meta", code: "MetaLeft", windowsVirtualKeyCode: 91 } : { key: "Control", code: "ControlLeft", windowsVirtualKeyCode: 17 };
  const keyK = { key: "k", code: "KeyK", windowsVirtualKeyCode: 75 };
  await send("Input.dispatchKeyEvent", { type: "rawKeyDown", modifiers, ...modifierKey });
  await send("Input.dispatchKeyEvent", { type: "rawKeyDown", modifiers, ...keyK });
  await send("Input.dispatchKeyEvent", { type: "keyUp", modifiers, ...keyK });
  await send("Input.dispatchKeyEvent", { type: "keyUp", modifiers: 0, ...modifierKey });
  await waitFor(`!!document.querySelector('[data-testid=keybinding-recorder-conflicts]')`, "conflict warning");
  const recorded = await evaluate(`document.querySelector('[data-testid=keybinding-recorder-capture]').textContent.trim()`);
  if (recorded !== label) throw new Error(`recorded ${recorded}, expected ${label}`);
  step(`recorded ${recorded}; conflict: ${await evaluate(`document.querySelector('[data-testid=keybinding-recorder-conflicts]').textContent.replace(/\\s+/g, ' ').trim()`)}`);
  if (await evaluate(`!!document.querySelector('.app-command-palette')`)) throw new Error("dispatcher opened the command palette while recording");
  step("dispatcher paused while recording");

  await evaluate(`document.querySelector('[data-testid=keybinding-recorder-save]').click()`);
  await waitFor(`[...${rowOf("builtin:ai")}.querySelectorAll('kbd')].some((k) => k.textContent.trim() === '${label}' && k.classList.contains('is-conflict'))`, "saved binding");
  step(`saved: row shows ${label} marked as a conflict`);

  await evaluate(`${rowOf("builtin:ai")}.querySelector('[data-testid=keybinding-reset]').click()`);
  await waitFor(`![...${rowOf("builtin:ai")}.querySelectorAll('kbd')].some((k) => k.textContent.trim() === '${label}')`, "reset");
  step("reset: row back to its default binding");
  if (errors.length) throw new Error(`uncaught page errors:\n${errors.join("\n")}`);
}
