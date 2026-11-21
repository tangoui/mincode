// Opt-in headless Chrome smoke test for the workbench tab commands (browser preview, no IPC
// runtime). Not part of run-tests.mjs (needs a Vite dev server and Chrome). Run from vue-app/:
//   node scripts/workbench-tabs.e2e.mjs
// CHROME_PATH overrides the browser binary. Opens two workbench tabs, then under the MinCode preset
// presses Ctrl+Tab (next tab) and ⌘W/Ctrl+W (close tab).
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
const profile = await mkdtemp(join(tmpdir(), "mincode-tabs-e2e-"));
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
  console.log("workbench tabs e2e: OK");
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
  const state = `({ ids: window.__tabsE2E?.ids(), active: window.__tabsE2E?.active(), route: window.__tabsE2E?.route(), focus: document.activeElement?.tagName })`;
  const waitFor = async (expression, label, timeout = 60000) => {
    const start = Date.now();
    while (Date.now() - start < timeout) {
      if (await evaluate(expression)) return;
      await sleep(150);
    }
    throw new Error(`timeout: ${label}\nstate: ${JSON.stringify(await evaluate(state))}\nuncaught errors:\n${errors.join("\n")}`);
  };
  const step = (text) => console.log(`- ${text}`);

  // CDP modifier bits: Alt 1, Ctrl 2, Meta 4, Shift 8.
  const MOD = mac ? 4 : 2;
  const KEYS = {
    Tab: { key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 },
    W: { key: "w", code: "KeyW", windowsVirtualKeyCode: 87 },
  };
  const press = async (key, modifiers) => {
    // Keys go to the document body: blur whatever the app focused so the workbench (not an editor) is the target.
    await evaluate(`document.activeElement?.blur?.(), true`);
    await send("Input.dispatchKeyEvent", { type: "rawKeyDown", modifiers, ...key });
    await send("Input.dispatchKeyEvent", { type: "keyUp", modifiers, ...key });
    await sleep(80);
  };
  const snapshot = async () => evaluate(state);
  const nextOf = (ids, active) => ids[(ids.indexOf(active) + 1) % ids.length];

  await send("Runtime.enable");
  await send("Page.enable");
  await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 860, deviceScaleFactor: 1, mobile: false });
  await send("Page.navigate", { url: `http://127.0.0.1:${vitePort}/` });
  await waitFor(`!!document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia && !!document.querySelector('.app-activity-button')`, "app shell");

  // On a cold Vite cache the dev server may reload the page once while optimizing deps: retry the setup.
  const setup = () => evaluate(`(async () => {
    const { useWorkbenchStore } = await import('/src/stores/workbench.ts');
    const { useKeybindingsStore } = await import('/src/stores/keybindings.ts');
    const app = document.querySelector('#app').__vue_app__;
    const pinia = app.config.globalProperties.$pinia;
    const router = app.config.globalProperties.$router;
    const workbench = useWorkbenchStore(pinia);
    window.__tabsE2E = {
      workbench,
      keybindings: useKeybindingsStore(pinia),
      ids: () => workbench.workspaceTabs.map((tab) => tab.id),
      active: () => workbench.activeTabId,
      route: () => String(router.currentRoute.value.name ?? ''),
    };
    workbench.openView('http');
    await router.push({ name: 'http' });
    workbench.openView('notes');
    await router.push({ name: 'notes' });
    return true;
  })()`);
  for (let attempt = 0; ; attempt += 1) {
    await waitFor(`!!document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia && !!document.querySelector('.app-activity-button')`, "app shell");
    await setup().catch(() => undefined);
    await sleep(2000);
    if (await evaluate(`!!window.__tabsE2E`)) break;
    if (attempt >= 4) throw new Error("page kept reloading during setup");
  }
  await waitFor(`window.__tabsE2E.ids().length >= 2 && window.__tabsE2E.route() === 'notes'`, "two workbench tabs");
  const { ids, active } = await snapshot();
  step(`opened tabs: ${ids.join(", ")} (active ${active})`);

  // MinCode preset (default).
  await waitFor(`window.__tabsE2E.keybindings.presetId === 'mincode'`, "MinCode preset active");
  const expected = nextOf(ids, active);
  await press(KEYS.Tab, 2);
  await waitFor(`window.__tabsE2E.active() === ${JSON.stringify(expected)}`, "MinCode Ctrl+Tab → next tab");
  step(`MinCode Ctrl+Tab selected ${expected}`);

  const closing = expected;
  await press(KEYS.W, MOD);
  await waitFor(`!window.__tabsE2E.ids().includes(${JSON.stringify(closing)})`, `MinCode ${mac ? "⌘W" : "Ctrl+W"} → close tab`);
  step(`MinCode ${mac ? "⌘W" : "Ctrl+W"} closed ${closing}`);

  if (errors.length) throw new Error(`uncaught page errors:\n${errors.join("\n")}`);
}
