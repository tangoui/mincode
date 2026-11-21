// Opt-in headless Chrome smoke test for plugin editor snippets (browser preview, no desktop runtime).
// Not part of run-tests.mjs (needs a Vite dev server and Chrome). Run from vue-app/:
//   node scripts/snippets.e2e.mjs
// CHROME_PATH overrides the browser binary. Injects a `snippets` contribution snapshot through the
// frontend stage (buildSnippetRegistry → setMonacoSnippets, exactly what the consumer does), mounts a
// MonacoTextEditor for a .ts file, types the prefix `clo`, checks the suggest widget offers the plugin
// snippet with its detail, accepts it and checks the expanded text; then checks that a read-only
// editor gets no plugin snippet. On a cold Vite cache the dev server may reload the page while it
// optimises dependencies; if the first run times out, run it again.
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
const profile = await mkdtemp(join(tmpdir(), "mincode-snippets-e2e-"));
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
  console.log("snippets e2e: OK");
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
  const charKey = (char) => ({ key: char, code: `Key${char.toUpperCase()}`, windowsVirtualKeyCode: char.toUpperCase().charCodeAt(0), text: char });
  const press = async (descriptor) => {
    const { modifiers = 0, ...key } = descriptor;
    await send("Input.dispatchKeyEvent", { type: "keyDown", modifiers, ...key });
    await send("Input.dispatchKeyEvent", { type: "keyUp", modifiers, ...key, text: undefined });
    await sleep(40);
  };
  const type = async (text) => {
    for (const char of text) await press(charKey(char));
  };
  const state = `({
    text: window.__snippetsE2E?.text(),
    languages: window.__snippetsE2E?.languages(),
    suggest: [...document.querySelectorAll('.suggest-widget.visible .monaco-list-row')].map((row) => row.textContent),
  })`;

  await send("Runtime.enable");
  await send("Page.enable");
  await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 860, deviceScaleFactor: 1, mobile: false });
  await send("Page.navigate", { url: `http://127.0.0.1:${vitePort}/` });
  await waitFor(`!!document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia && !!document.querySelector('.app-activity-button')`, "app shell");

  await evaluate(`(async () => {
    const main = await (await fetch('/src/main.ts')).text();
    const vueUrl = main.match(/from\\s+"(\\/node_modules\\/\\.vite\\/deps\\/vue\\.js[^"]*)"/)[1];
    const { createApp, h, ref } = await import(vueUrl);
    const { buildSnippetRegistry } = await import('/src/domain/editor/snippets.ts');
    const { setMonacoSnippets, monacoSnippetLanguages } = await import('/src/services/monaco-snippets.ts');
    const Editor = (await import('/src/components/projects/MonacoTextEditor.vue')).default;
    const pinia = document.querySelector('#app').__vue_app__.config.globalProperties.$pinia;
    const snippets = [{ name: 'Console log', prefixes: ['clog'], body: 'console.log(\${1:value});$0', description: 'Log a value' }];
    const registry = buildSnippetRegistry([{ pluginId: 'e2e.snippets', pluginName: 'E2E Snippets', version: '1.0.0', source: 'local',
      runtime: 'declarative', activeInWorkspace: true,
      point: { fingerprint: 'e2e', issues: [], entries: [{ language: 'typescript', path: 'snippets/ts.json', snippets }] } }]);
    setMonacoSnippets(registry.byLanguage);
    const text = ref('');
    const readonlyText = ref('');
    window.__snippetsE2E = { text: () => text.value, languages: monacoSnippetLanguages };
    const mount = (name, top, readonly, model) => {
      const host = document.createElement('div');
      host.dataset.e2e = name;
      host.style.cssText = 'position:fixed;left:40px;top:' + top + 'px;width:640px;height:300px;z-index:99999;display:flex;background:#fff';
      document.body.appendChild(host);
      createApp({
        render: () => h(Editor, {
          modelValue: model.value,
          'onUpdate:modelValue': (value) => { model.value = value; },
          path: '/e2e/' + name + '.ts',
          readonly,
          preferences: { fontSize: 13, tabSize: 2, wordWrap: 'off', minimap: false, lineNumbers: 'on' },
          kind: 'text',
        }),
      }).use(pinia).mount(host);
    };
    mount('snippets-host', 40, false, text);
    mount('snippets-readonly', 400, true, readonlyText);
    return true;
  })()`);
  await waitFor(`!!document.querySelector('[data-e2e=snippets-host] .monaco-editor') && (window.__snippetsE2E.languages() ?? []).includes('typescript')`, "editor mounted and provider registered");
  step(`providers: ${JSON.stringify(await evaluate(`window.__snippetsE2E.languages()`))}`);

  const click = async (y) => {
    for (const kind of ["mousePressed", "mouseReleased"]) {
      await send("Input.dispatchMouseEvent", { type: kind, x: 300, y, button: "left", clickCount: 1 });
    }
    await sleep(100);
  };
  await click(120);
  await type("clo");
  await waitFor(`[...document.querySelectorAll('.suggest-widget.visible .monaco-list-row')].some((row) => row.textContent.includes('clog'))`, "snippet suggested");
  step(`suggestions: ${JSON.stringify((await evaluate(state)).suggest)}`);
  await press({ key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r" });
  await waitFor(`window.__snippetsE2E.text() === 'console.log(value);'`, "snippet inserted");
  step(`accepted → ${JSON.stringify(await evaluate(`window.__snippetsE2E.text()`))}`);

  // Read-only editor: explicitly trigger suggest; no plugin snippet may appear. Suggest widgets render
  // into the shared fixed overflow host, so the query is document-wide (only one is visible at a time).
  await click(480);
  await press({ key: " ", code: "Space", windowsVirtualKeyCode: 32, modifiers: 2 });
  await sleep(800);
  const readonlyRows = await evaluate(`[...document.querySelectorAll('.suggest-widget.visible .monaco-list-row')].map((row) => row.textContent)`);
  if (readonlyRows.some((row) => row.includes("clog"))) throw new Error(`read-only editor offered a snippet: ${JSON.stringify(readonlyRows)}`);
  step("read-only editor offers no plugin snippet");
  if (errors.length) throw new Error(`uncaught page errors:\n${errors.join("\n")}`);
}
