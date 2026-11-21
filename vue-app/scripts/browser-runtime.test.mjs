import { registerHooks } from "node:module";
import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import test from "node:test";

// desktop-bridge.ts imports the Vite-only dev-performance helper; stub it so the real
// invokeBackend/canInvoke run under node:test. Desktop mode is a stub window.tango runtime
// whose mincode.invoke returns the command's JSON like the Go host does.
const devPerformanceStub = `
  export const measureDevAsync = (_name, run) => run();
`;
const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/dev-performance") return { url: dataModule(devPerformanceStub), shortCircuit: true };
    return nextResolve(specifier, context);
  },
});

globalThis.window = {};

const { canInvoke, invokeBackend, isBrowserLocalMode } = await import("../src/services/desktop-bridge.ts");
const {
  __resetBrowserRuntimeForTests,
  browserLocalFeatures,
  hasBrowserHandler,
  registerBrowserHandler,
  registerBrowserLocalFeature,
} = await import("../src/services/browser-runtime/registry.ts");

function tangoStub() {
  return {
    async call(method, request) {
      const state = globalThis.__browserRuntimeTest;
      state.nativeCalls.push({ method, command: request.command, args: request.args });
      const raw = await state.nativeImpl(request.command, request.args);
      return raw ? JSON.parse(raw) : null;
    },
    stream() {
      throw new Error("stream not expected in this test");
    },
    socket() {
      throw new Error("socket not expected in this test");
    },
    emit() {},
    onRaw: () => () => {},
  };
}

function reset({ desktop = false } = {}) {
  __resetBrowserRuntimeForTests();
  globalThis.window = {};
  globalThis.tango = desktop ? tangoStub() : undefined;
  const state = {
    nativeCalls: [],
    nativeImpl: async () => {
      throw new Error("native call not expected");
    },
  };
  globalThis.__browserRuntimeTest = state;
  return state;
}

test("browser mode: registered command runs the handler with args and signal and returns its value as-is", async () => {
  const state = reset();
  const received = [];
  // Not JSON-serialisable on purpose: the handler result must not be round-tripped through JSON.
  const result = { bytes: new Uint8Array([1, 2, 3]), at: new Date(0) };
  registerBrowserHandler("http_send_request", async (args, signal) => {
    received.push({ args, signal });
    return result;
  });
  const controller = new AbortController();
  const args = { payload: { url: "https://example.test" } };

  const value = await invokeBackend("http_send_request", args, { signal: controller.signal });

  assert.equal(value, result);
  assert.equal(received.length, 1);
  assert.equal(received[0].args, args);
  assert.equal(received[0].signal, controller.signal);
  assert.equal(state.nativeCalls.length, 0);
  assert.equal(hasBrowserHandler("http_send_request"), true);
});

test("browser mode: handler receives an empty args object by default", async () => {
  reset();
  let seen;
  registerBrowserHandler("ai_store_list_workspaces", async (args) => {
    seen = args;
    return [];
  });
  assert.deepEqual(await invokeBackend("ai_store_list_workspaces"), []);
  assert.deepEqual(seen, {});
});

test("browser mode: an already-aborted signal rejects before the handler runs", async () => {
  reset();
  let calls = 0;
  registerBrowserHandler("local_fs_list_dir", async () => {
    calls += 1;
    return [];
  });
  const controller = new AbortController();
  controller.abort(new Error("stopped"));
  await assert.rejects(invokeBackend("local_fs_list_dir", {}, { signal: controller.signal }), /stopped/);
  assert.equal(calls, 0);
});

test("browser mode: handler errors propagate unchanged", async () => {
  reset();
  const failure = new TypeError("Failed to fetch");
  registerBrowserHandler("ai_list_models", async () => {
    throw failure;
  });
  await assert.rejects(invokeBackend("ai_list_models"), (error) => error === failure);
});

test("browser mode: unregistered command keeps the native path and fails without the desktop runtime", async () => {
  const state = reset();
  assert.equal(hasBrowserHandler("git_fetch"), false);
  await assert.rejects(invokeBackend("git_fetch", { path: "/tmp" }), /desktop runtime is unavailable/);
  assert.equal(state.nativeCalls.length, 0);
});

test("desktop runtime: registered browser handlers are never called", async () => {
  const state = reset({ desktop: true });
  let handlerCalls = 0;
  registerBrowserHandler("local_fs_read_text_file", async () => {
    handlerCalls += 1;
    return "browser";
  });
  state.nativeImpl = async () => JSON.stringify("native");

  assert.equal(await invokeBackend("local_fs_read_text_file", { path: "/a.txt" }), "native");
  assert.equal(handlerCalls, 0);
  assert.equal(state.nativeCalls.length, 1);
  assert.equal(state.nativeCalls[0].method, "mincode.invoke");
  assert.deepEqual(state.nativeCalls[0].args, { path: "/a.txt" });
});

test("canInvoke: desktop always true; browser only for registered commands", () => {
  reset({ desktop: true });
  registerBrowserHandler("local_fs_write_text_file", async () => undefined);
  assert.equal(canInvoke("local_fs_write_text_file"), true);
  assert.equal(canInvoke("git_push"), true);
  assert.equal(isBrowserLocalMode(), false);

  globalThis.tango = undefined;
  assert.equal(canInvoke("local_fs_write_text_file"), true);
  assert.equal(canInvoke("git_push"), false);
  assert.equal(isBrowserLocalMode(), true);
});

test("test reset clears registered handlers and features", () => {
  reset();
  registerBrowserHandler("http_send_request", async () => undefined);
  registerBrowserLocalFeature({
    id: "http",
    titleKey: "browserLocal.feature.http.title",
    limitKey: "browserLocal.feature.http.limit",
  });
  reset();
  assert.equal(hasBrowserHandler("http_send_request"), false);
  assert.equal(canInvoke("http_send_request"), false);
  assert.equal(browserLocalFeatures.length, 0);
});

test("registerBrowserLocalFeature appends in order and rejects duplicate ids", () => {
  reset();
  const http = { id: "http", titleKey: "browserLocal.feature.http.title", limitKey: "browserLocal.feature.http.limit" };
  const ai = { id: "ai", titleKey: "browserLocal.feature.ai.title", limitKey: "browserLocal.feature.ai.limit" };
  registerBrowserLocalFeature(http);
  registerBrowserLocalFeature(ai);
  assert.deepEqual(browserLocalFeatures, [http, ai]);
  assert.throws(() => registerBrowserLocalFeature({ ...http, titleKey: "browserLocal.title" }), /already registered/);
  assert.deepEqual(browserLocalFeatures.map((feature) => feature.id), ["http", "ai"]);
  // The badge gets a snapshot of each entry; mutating the caller's object later does not change it.
  http.id = "mutated";
  assert.equal(browserLocalFeatures[0].id, "http");
});

const sourceRoot = fileURLToPath(new URL("../src/", import.meta.url));
const browserRuntimeDir = `${sourceRoot}services/browser-runtime/`;

async function sourceFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true, recursive: true });
  return entries
    .filter((entry) => entry.isFile() && /\.(ts|vue|mts|js)$/.test(entry.name))
    .map((entry) => `${entry.parentPath ?? entry.path}/${entry.name}`.replace(/\/+/g, "/"));
}

test("browser-runtime feature modules are imported only from services/browser-runtime", async () => {
  // Static or dynamic imports of browser-runtime/<feature> elsewhere would pull handlers into the main
  // bundle; only registry (tiny, used by desktop-bridge) and the lazily loaded index are allowed outside.
  // Three exceptions, and only as a dynamic import(): the browser file picker (browser-runtime/files),
  // the browser AI stream (browser-runtime/ai, used by streamAiLoopTurn in services/ai-agent-loop.ts) and
  // the browser folder picker / permission calls (browser-runtime/project-fs, used by chooseNativeDirectory
  // in services/desktop-bridge.ts and by the browser-root access state in stores/local-project.ts).
  const specifier = /(from\s+|import\s*\(\s*|import\s+)["']([^"']*browser-runtime[^"']*)["']/g;
  const offenders = [];
  for (const file of await sourceFiles(sourceRoot)) {
    if (file.startsWith(browserRuntimeDir)) continue;
    const text = await readFile(file, "utf8");
    for (const match of text.matchAll(specifier)) {
      const target = match[2].replace(/\.ts$/, "").replace(/\/index$/, "");
      if (/(?:^|\/)browser-runtime(?:\/registry)?$/.test(target)) continue;
      const dynamicImport = match[1].includes("(");
      if (dynamicImport && /(?:^|\/)browser-runtime\/files$/.test(target)) continue;
      if (dynamicImport && /(?:^|\/)browser-runtime\/ai$/.test(target) && file.endsWith("/services/ai-agent-loop.ts")) continue;
      if (
        dynamicImport
        && /(?:^|\/)browser-runtime\/project-fs$/.test(target)
        && (file.endsWith("/services/desktop-bridge.ts") || file.endsWith("/stores/local-project.ts"))
      ) continue;
      offenders.push(`${file.slice(sourceRoot.length)} -> ${match[2]}`);
    }
  }
  assert.deepEqual(offenders, []);
});
