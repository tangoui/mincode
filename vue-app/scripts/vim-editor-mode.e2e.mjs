// Opt-in headless Chrome smoke test for the Vim editor mode (browser preview, no desktop runtime).
// Not part of run-tests.mjs (needs a Vite dev server and Chrome). Run from vue-app/:
//   node scripts/vim-editor-mode.e2e.mjs
// CHROME_PATH overrides the browser binary. Registers a test keymap preset with `editorMode: "vim"`
// (what the official keymaps.vim preset does), mounts a MonacoTextEditor, types `ihello<Esc>dd`, checks the text
// and the mode indicator, runs `:w`, composes IME text in insert mode, then switches back to the
// MinCode preset and checks that Vim detached. On a cold Vite cache the dev server may reload the
// page while it optimises dependencies; if the first run times out at "Vim attached", run it again.
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
const profile = await mkdtemp(join(tmpdir(), "mincode-vim-e2e-"));
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
  console.log("vim editor mode e2e: OK");
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
    } else if (message.method === "Runtime.consoleAPICalled" && message.params.type === "error") {
      errors.push(`console.error: ${message.params.args.map((arg) => arg.value ?? arg.description).join(" ")}`);
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
  const waitFor = async (expression, label, timeout = 60000) => {
    const start = Date.now();
    while (Date.now() - start < timeout) {
      if (await evaluate(expression)) return;
      await sleep(150);
    }
    throw new Error(`timeout: ${label}\nstate: ${JSON.stringify(await evaluate(state))}\nuncaught errors:\n${errors.join("\n")}`);
  };
  const step = (text) => console.log(`- ${text}`);

  const KEYS = {
    Escape: { key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 },
    Enter: { key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r" },
  };
  const charKey = (char) => {
    const upper = char.toUpperCase();
    if (char === ":") return { key: ":", code: "Semicolon", windowsVirtualKeyCode: 186, text: ":", modifiers: 8 };
    if (/[0-9]/.test(char)) return { key: char, code: `Digit${char}`, windowsVirtualKeyCode: char.charCodeAt(0), text: char };
    return { key: char, code: `Key${upper}`, windowsVirtualKeyCode: upper.charCodeAt(0), text: char };
  };
  const press = async (descriptor) => {
    const { modifiers = 0, ...key } = descriptor;
    await send("Input.dispatchKeyEvent", { type: "keyDown", modifiers, ...key });
    await send("Input.dispatchKeyEvent", { type: "keyUp", modifiers, ...key, text: undefined });
    await sleep(30);
  };
  const type = async (text) => {
    for (const char of text) await press(charKey(char));
  };

  const state = `({
    text: window.__vimE2E?.text(),
    editorMode: window.__vimE2E?.store.editorMode ?? null,
    provider: window.__vimE2E?.store.editorModeProvider?.id ?? null,
    monaco: !!document.querySelector('[data-e2e=vim-host] .monaco-editor'),
    saves: window.__vimE2E?.saves,
    mode: document.querySelector('.app-vim-status')?.dataset.vimMode ?? null,
    label: document.querySelector('.app-vim-status__mode')?.textContent ?? null,
    stripHidden: document.querySelector('[data-e2e=vim-host] .app-vim-status, [data-e2e=vim-host] [hidden]')?.hidden ?? null,
  })`;

  await send("Runtime.enable");
  await send("Page.enable");
  await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 860, deviceScaleFactor: 1, mobile: false });
  await send("Page.navigate", { url: `http://127.0.0.1:${vitePort}/` });
  await waitFor(`!!document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia && !!document.querySelector('.app-activity-button')`, "app shell");

  // Mount a MinCode text editor with the app's Pinia and Vue instance (same module URLs as the app).
  await evaluate(`(async () => {
    const main = await (await fetch('/src/main.ts')).text();
    const vueUrl = main.match(/from\\s+"(\\/node_modules\\/\\.vite\\/deps\\/vue\\.js[^"]*)"/)[1];
    const { createApp, h, ref } = await import(vueUrl);
    const { registerKeymapPreset } = await import('/src/domain/keybindings/preset.ts');
    const { useKeybindingsStore } = await import('/src/stores/keybindings.ts');
    const Editor = (await import('/src/components/projects/MonacoTextEditor.vue')).default;
    const pinia = document.querySelector('#app').__vue_app__.config.globalProperties.$pinia;
    registerKeymapPreset({ id: 'e2e.vim', title: { zh: 'E2E Vim', en: 'E2E Vim' }, extends: 'mincode', editorMode: 'vim', rules: [] }, { source: 'plugin', pluginId: 'e2e' });
    const store = useKeybindingsStore(pinia);
    store.setPreset('e2e.vim');
    const text = ref('one\\ntwo\\n');
    const api = { saves: 0, text: () => text.value, store };
    window.__vimE2E = api;
    const host = document.createElement('div');
    host.dataset.e2e = 'vim-host';
    host.style.cssText = 'position:fixed;left:40px;top:40px;width:640px;height:320px;z-index:99999;display:flex;background:#fff';
    document.body.appendChild(host);
    createApp({
      render: () => h(Editor, {
        modelValue: text.value,
        'onUpdate:modelValue': (value) => { text.value = value; },
        onSave: () => { api.saves += 1; },
        path: '/e2e/vim.txt',
        preferences: { fontSize: 13, tabSize: 2, wordWrap: 'off', minimap: false, lineNumbers: 'on' },
        kind: 'text',
      }),
    }).use(pinia).mount(host);
    return true;
  })()`);
  await waitFor(`document.querySelector('[data-e2e=vim-host] .app-vim-status')?.dataset.vimMode === 'normal'`, "Vim attached (lazy chunk loaded)");
  step(`Vim attached; indicator: ${await evaluate(`document.querySelector('.app-vim-status__mode').textContent`)}`);

  const clickEditor = async () => {
    // A real click, so Monaco focuses whichever input surface it uses (EditContext or textarea).
    for (const type of ["mousePressed", "mouseReleased"]) {
      await send("Input.dispatchMouseEvent", { type, x: 300, y: 200, button: "left", clickCount: 1 });
    }
    await sleep(100);
  };
  await clickEditor();
  await press(KEYS.Escape);
  await type("gg0");

  await press(charKey("i"));
  await waitFor(`document.querySelector('.app-vim-status')?.dataset.vimMode === 'insert'`, "insert mode");
  step(`i → ${await evaluate(`document.querySelector('.app-vim-status__mode').textContent`)}`);
  await type("hello");
  await waitFor(`window.__vimE2E.text() === 'helloone\\ntwo\\n'`, "typed hello");
  step("typed hello in insert mode");

  // IME composition in insert mode must insert the committed text and keep the mode.
  await send("Input.imeSetComposition", { text: "ni", selectionStart: 2, selectionEnd: 2 });
  await sleep(50);
  await send("Input.insertText", { text: "你" });
  await waitFor(`window.__vimE2E.text() === 'hello你one\\ntwo\\n'`, "IME commit");
  if ((await evaluate(`document.querySelector('.app-vim-status').dataset.vimMode`)) !== "insert") throw new Error("IME composition left insert mode");
  step("IME composition committed 你 in insert mode");

  await press(KEYS.Escape);
  await waitFor(`document.querySelector('.app-vim-status')?.dataset.vimMode === 'normal'`, "normal mode");
  await type("dd");
  await waitFor(`window.__vimE2E.text() === 'two\\n'`, "dd deleted the line");
  step(`<Esc>dd → text ${JSON.stringify(await evaluate(`window.__vimE2E.text()`))}, indicator ${await evaluate(`document.querySelector('.app-vim-status__mode').textContent`)}`);

  // `>>` runs Monaco's ShiftCommand (patched for monaco-editor 0.56's language-configuration service).
  await press({ key: ">", code: "Period", windowsVirtualKeyCode: 190, text: ">", modifiers: 8 });
  await press({ key: ">", code: "Period", windowsVirtualKeyCode: 190, text: ">", modifiers: 8 });
  await waitFor(`/^\\s+two\\n$/.test(window.__vimE2E.text())`, ">> indented the line");
  step(`>> → ${JSON.stringify(await evaluate(`window.__vimE2E.text()`))}`);

  await press(charKey(":"));
  await waitFor(`!!document.querySelector('.app-vim-status__prompt input')`, "ex prompt");
  await press(charKey("w"));
  await press(KEYS.Enter);
  await waitFor(`window.__vimE2E.saves === 1`, ":w saved");
  step(":w called the editor save handler");

  await evaluate(`window.__vimE2E.store.setPreset('mincode'), true`);
  await waitFor(`document.querySelector('[data-e2e=vim-host] .app-vim-status') === null || document.querySelector('[data-e2e=vim-host] [hidden]') !== null && !document.querySelector('[data-e2e=vim-host] [data-vim-mode]')`, "Vim detached");
  await clickEditor();
  await press(charKey("i"));
  await waitFor(`window.__vimE2E.text().includes('i')`, "plain typing after detach");
  step("switching back to the MinCode preset detached Vim (typing inserts text)");
  if (errors.length) throw new Error(`uncaught page errors:\n${errors.join("\n")}`);
}
