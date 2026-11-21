import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";
import { URI } from "monaco-editor/base/common/uri.js";
import {
  clearServerProblems,
  createProblemRegistry,
  problemsFor,
  setProblems,
  summarizeProblems,
} from "../src/domain/projects/lsp-problems.ts";

let harnessId = 0;
const tick = () => new Promise((resolve) => setImmediate(resolve));
const token = { onCancellationRequested: () => ({ dispose() {} }) };
const syncOptions = (change = 2, save = { includeText: true }) => ({ openClose: true, change, save });
const definition = (id, languages = ["typescript"], auxiliary = false) => ({ id, languages, auxiliary });
// Stub of Monaco's command registry: the editor commands this build registers.
const monacoCommandIds = new Set([
  "editor.action.peekLocations", "editor.action.showReferences", "editor.action.goToLocations", "editor.action.formatDocument",
]);

async function harness(t, servers, { initialize, notify, start, request, idleStopSeconds = 0, backgroundStopSeconds = 0 } = {}) {
  const id = `lsp-runtime-test-${++harnessId}`;
  const calls = [];
  const models = new Map();
  const hooks = {};
  const providers = new Map();
  const markers = new Map();
  const markerCalls = [];
  let eventHandler;
  let installedHandler;
  let restartRequiredHandler;
  const restartPrompts = [];
  let generation = 0;
  let controller;
  const monaco = {
    Uri: URI,
    Range: class {
      constructor(startLineNumber, startColumn, endLineNumber, endColumn) {
        Object.assign(this, { startLineNumber, startColumn, endLineNumber, endColumn });
      }
    },
    MarkerSeverity: { Error: 8, Warning: 4, Info: 2, Hint: 1 },
    Emitter: class {
      listeners = new Set();
      event = (fn, _thisArg, disposables) => {
        this.listeners.add(fn);
        const subscription = { dispose: () => this.listeners.delete(fn) };
        if (Array.isArray(disposables)) disposables.push(subscription);
        return subscription;
      };
      fire(value) { for (const fn of this.listeners) fn(value); }
      dispose() { this.listeners.clear(); }
    },
    editor: {
      getModels: () => [...models.values()],
      getModel: (uri) => models.get(uri.toString()),
      setModelMarkers(model, owner, items) {
        markers.set(`${model.uri.toString()}\0${owner}`, items);
        markerCalls.push({ model, owner, items });
      },
      onDidCreateModel: (fn) => { hooks.create = fn; },
      onDidChangeModelLanguage: (fn) => { hooks.language = fn; },
      onWillDisposeModel: (fn) => { hooks.dispose = fn; },
    },
    languages: new Proxy({}, {
      get: (_, name) => (language, provider) => {
        providers.set(`${name}:${language}`, provider);
        return { dispose() {} };
      },
    }),
  };
  const service = {
    listLspServers: async () => servers,
    onLspEvent: (handler) => { eventHandler = handler; return () => {}; },
    onLspServerInstalled: (handler) => { installedHandler = handler; return () => { installedHandler = undefined; }; },
    onLspRestartRequired: (handler) => { restartRequiredHandler = handler; return () => {}; },
    replyLspRequest: async (...args) => { calls.push({ method: "reply", args }); },
    startLspServer: async (root, serverId) => {
      calls.push({ method: "start", root, serverId });
      await start?.(serverId);
      return { ...servers.find((server) => server.id === serverId), running: true, generation: ++generation };
    },
    stopLspServer: async (root, serverId) => { calls.push({ method: "stop", root, serverId }); },
    sendLspRequest: async (root, serverId, gen, method, params, timeoutMs, signal) => {
      const call = { root, serverId, generation: gen, method, params, timeoutMs, signal };
      calls.push(call);
      if (method === "initialize") {
        return initialize ? initialize(serverId, params) : {
          capabilities: { hoverProvider: true, textDocumentSync: syncOptions() },
        };
      }
      if (request) return request(call);
      if (method === "textDocument/hover") return { contents: "hover" };
      return null;
    },
    sendLspNotify: async (root, serverId, gen, method, params) => {
      calls.push({ root, serverId, generation: gen, method, params });
      await notify?.(serverId, method, params);
    },
  };
  const runtimeStates = new Map();
  const watchLimited = new Set();
  const progressCalls = [];
  const commandHandlers = new Map();
  const feedback = [];
  const problemRegistry = createProblemRegistry();
  const problems = {
    set: (root, serverId, uri, diagnostics) => setProblems(problemRegistry, root, serverId, uri, diagnostics),
    clearServer: (root, serverId) => clearServerProblems(problemRegistry, root, serverId),
    get: (root, serverId, uri) => problemsFor(problemRegistry, root, serverId, uri),
    summary: (root) => summarizeProblems(problemRegistry, root),
  };
  const mocks = {
    vue: { watch: () => () => {} },
    "@/services/lsp-runtime-state": {
      lspRuntimeState: (root, id) => runtimeStates.get(`${root}\0${id}`),
      setLspRuntimeState: (root, id, state) => runtimeStates.set(`${root}\0${id}`, state),
      markLspWatchLimited: (root, id) => watchLimited.add(`${root}\0${id}`),
      setLspServerName: (root, id, name) => progressCalls.push(["name", root, id, name]),
      setLspWatchScanning: (root, id, active) => progressCalls.push(["scanning", root, id, active]),
      applyLspProgress: (root, id, params) => progressCalls.push(["progress", root, id, params]),
    },
    "@/services/monaco-runtime.js": monaco,
    "@/composables/project-file-uri": {
      projectFileUri: (root, path) => URI.file(path ? `${root}/${path}` : root),
    },
    "@/services/lsp-service": service,
    "@/services/monaco-commands": {
      registerMonacoCommand: (commandId, handler) => { commandHandlers.set(commandId, handler); return () => {}; },
      hasMonacoCommand: (commandId) => monacoCommandIds.has(commandId),
    },
    "@/services/lsp-feedback": {
      showLspMessage: (serverId, toast) => { feedback.push({ serverId, ...toast }); },
      showLspCommandFailure: (serverId, title, reason) => { feedback.push({ serverId, failed: title, reason }); },
    },
    "@/services/settings-persistence": { readSettingsSnapshot: () => ({}) },
    "@/domain/performance/runtime-limits": {
      runtimePerformanceSettingsFromSnapshot: () => ({
        lspBackgroundStopSeconds: backgroundStopSeconds, lspIdleStopSeconds: idleStopSeconds, lspAuxiliaryIdleStopSeconds: 0,
      }),
    },
    "@/stores/plugin-language-servers": {
      usePluginLanguageServersStore: () => ({ servers: [], revision: 0, syncRequests: 0 }),
    },
    "@/services/plugin-language-server-service": { startPluginLanguageServer() { throw new Error("unexpected plugin"); } },
    "@/stores/project-problems": { useProjectProblemsStore: () => problems },
    "@/services/jdtls-bundles": { handleJdtlsRestartRequired: (event, restart, options) => { restartPrompts.push({ event, restart, options }); } },
  };
  globalThis[id] = mocks;
  const runtimeUrl = new URL(`../src/composables/project-language-server-runtime.ts?${id}`, import.meta.url).href;
  const loader = registerHooks({
    resolve(specifier, context, nextResolve) {
      if (context.parentURL === runtimeUrl && mocks[specifier]) {
        const exports = Object.keys(mocks[specifier]).map((key) =>
          `export const ${key} = globalThis[${JSON.stringify(id)}][${JSON.stringify(specifier)}][${JSON.stringify(key)}];`,
        ).join("\n");
        return { url: `data:text/javascript,${encodeURIComponent(exports)}`, shortCircuit: true };
      }
      return nextResolve(specifier, context);
    },
  });
  const { createProjectLanguageServerController, restartBuiltinServer } = await import(runtimeUrl);
  t.after(async () => {
    for (const model of [...models.values()]) disposeModel(model);
    await controller?.dispose();
    // Kept until here: the runtime imports some modules lazily (the jdtls restart prompt).
    loader.deregister();
    delete globalThis[id];
  });

  function addModel(path, text = "old", language = "typescript", announce = false) {
    const listeners = new Set();
    let version = 1;
    const model = {
      uri: URI.file(path),
      getValue: () => text,
      getVersionId: () => version,
      getLanguageId: () => language,
      isDisposed: () => !models.has(model.uri.toString()),
      onDidChangeContent: (fn) => { listeners.add(fn); return { dispose: () => listeners.delete(fn) }; },
      change: (value) => { text = value; version++; for (const listener of listeners) listener(); },
      changeLanguage: (value) => { language = value; return hooks.language({ model }); },
    };
    models.set(model.uri.toString(), model);
    if (announce) void hooks.create(model);
    return model;
  }
  function disposeModel(model) {
    hooks.dispose?.(model);
    models.delete(model.uri.toString());
  }
  async function settle() { for (let i = 0; i < 12; i++) await tick(); }
  async function activate(root = "/repo", enabled = servers.map((s) => s.id), overrides = undefined) {
    controller = createProjectLanguageServerController({
      projectRoot: { value: root }, enabledServers: { value: enabled }, active: true,
      ...(overrides ? { pluginLanguageOverrides: { value: overrides } } : {}),
    });
    await settle();
    return controller;
  }
  return {
    calls, providers, addModel, disposeModel, settle, activate, runtimeStates, watchLimited, progressCalls, restartBuiltinServer, markers, markerCalls, problems, feedback,
    runCommand: (payload) => commandHandlers.get("mincode.lsp.executeCommand")(payload),
    advance: async (ms = 200) => { t.mock.timers.tick(ms); await settle(); },
    modelMarkers: (model, serverId = "json-ls") => markers.get(`${model.uri.toString()}\0project-lsp:${serverId}`) ?? [],
    emit: (event) => eventHandler(event),
    installed: async (serverId) => { await installedHandler?.(serverId); await settle(); },
    restartPrompts,
    restartRequired: (event) => restartRequiredHandler(event),
    hover: (model) => providers.get(`registerHoverProvider:${model.getLanguageId()}`)
      ?.provideHover(model, { lineNumber: 1, column: 1 }, token),
  };
}

test("rejected, malformed or failed initialized handshake never registers a fake active server", async (t) => {
  for (const failure of ["reject", "invalid", "notify", "encoding"]) {
    await t.test(failure, async (t) => {
      const h = await harness(t, [definition("ts-ls")], {
        initialize: () => {
          if (failure === "reject") throw new Error("initialize rejected");
          if (failure === "invalid") return null;
          return { capabilities: failure === "encoding" ? { positionEncoding: "utf-8" } : { hoverProvider: true } };
        },
        notify: (_, method) => { if (failure === "notify" && method === "initialized") throw new Error("notify rejected"); },
      });
      h.addModel("/repo/a.ts");
      await h.activate();
      assert.equal(h.calls.filter((c) => c.method === "stop").length, 1);
      assert.equal(h.providers.size, 0);
      assert.deepEqual(h.runtimeStates.get("/repo\0ts-ls"), { phase: "failed", failure: "initialize" });
      assert.equal(h.calls.some((c) => c.method === "textDocument/didOpen"), false);
    });
  }
});

test("save flushes before didSave; full/incremental/none and includeText honor each server", async (t) => {
  let release;
  const blocked = new Promise((resolve) => { release = resolve; });
  const h = await harness(t, [definition("ts-ls"), definition("tailwind-ls", ["typescript"], true), definition("json-ls")], {
    initialize: (id) => ({ capabilities: {
      hoverProvider: true,
      textDocumentSync: id === "ts-ls" ? syncOptions(2) : id === "tailwind-ls" ? syncOptions(1, true) : syncOptions(0, false),
    } }),
    notify: (id, method) => id === "ts-ls" && method === "textDocument/didChange" ? blocked : undefined,
  });
  const model = h.addModel("/repo/a.ts", "a\r\nb😀");
  const controller = await h.activate();
  model.change("new\nvalue");
  let saved = false;
  const saving = controller.notifyFileSaved("a.ts").then(() => { saved = true; });
  await h.settle();
  assert.equal(saved, false);
  assert.equal(h.calls.some((c) => c.method === "textDocument/didSave"), false);
  release();
  await saving;
  const changes = h.calls.filter((c) => c.method === "textDocument/didChange");
  assert.equal(changes.length, 2);
  assert.deepEqual(changes.find((c) => c.serverId === "ts-ls").params.contentChanges, [{
    range: { start: { line: 0, character: 0 }, end: { line: 1, character: 3 } }, text: "new\nvalue",
  }]);
  assert.deepEqual(changes.find((c) => c.serverId === "tailwind-ls").params.contentChanges, [{ text: "new\nvalue" }]);
  const saves = h.calls.filter((c) => c.method === "textDocument/didSave");
  assert.equal(saves.length, 2);
  assert.equal(saves.find((c) => c.serverId === "ts-ls").params.text, "new\nvalue");
  assert.equal("text" in saves.find((c) => c.serverId === "tailwind-ls").params, false);
  model.change("next");
  await controller.notifyFileSaved("a.ts");
  assert.deepEqual(h.calls.filter((c) => c.method === "textDocument/didChange" && c.serverId === "ts-ls")[1]
    .params.contentChanges[0].range.end, { line: 1, character: 5 });
});

test("new same-language model starts missing auxiliary even with a running primary", async (t) => {
  let auxiliaryStarts = 0;
  const h = await harness(t, [definition("ts-ls"), definition("tailwind-ls", ["typescript"], true)], {
    start: (id) => {
      if (id === "tailwind-ls" && ++auxiliaryStarts === 1) throw new Error("first launch failed");
    },
  });
  h.addModel("/repo/a.ts");
  await h.activate();
  assert.equal(auxiliaryStarts, 1);
  // Without any settings resync, onDidCreateModel sees primary running while
  // the still-enabled auxiliary is absent. The former early return broke this.
  const model = h.addModel("/repo/b.ts", "second", "typescript", true);
  await h.settle();
  assert.equal(auxiliaryStarts, 2);
  assert.ok(h.calls.some((c) => c.serverId === "tailwind-ls" && c.method === "textDocument/didOpen"
    && c.params.textDocument.uri === model.uri.toString()));
  // A model in a similarly prefixed sibling directory must never attach.
  const sibling = h.addModel("/repo-other/c.ts", "outside", "typescript", true);
  await h.settle();
  assert.equal(h.calls.some((c) => c.method === "textDocument/didOpen"
    && c.params.textDocument.uri === sibling.uri.toString()), false);
});

test("a late auxiliary uses its own didOpen baseline, not the primary's older text", async (t) => {
  let release;
  const blocked = new Promise((resolve) => { release = resolve; });
  const h = await harness(t, [definition("ts-ls"), definition("tailwind-ls", ["typescript"], true)], {
    initialize: async (id) => {
      if (id === "tailwind-ls") await blocked;
      return { capabilities: { textDocumentSync: syncOptions() } };
    },
  });
  const model = h.addModel("/repo/a.ts", "primary old\ntext");
  const controller = await h.activate();
  model.change("auxiliary baseline😀"); // Debounced, not yet sent to the primary.
  release();
  await h.settle();
  const auxiliaryOpen = h.calls.find((c) => c.serverId === "tailwind-ls" && c.method === "textDocument/didOpen");
  assert.equal(auxiliaryOpen.params.textDocument.text, "auxiliary baseline😀");
  model.change("final");
  await controller.notifyFileSaved("a.ts");
  const changes = h.calls.filter((c) => c.method === "textDocument/didChange");
  assert.deepEqual(changes.find((c) => c.serverId === "ts-ls").params.contentChanges[0].range.end,
    { line: 1, character: 4 });
  assert.deepEqual(changes.find((c) => c.serverId === "tailwind-ls").params.contentChanges[0].range.end,
    { line: 0, character: 20 });
});

test("language change closes and reopens even on a shared server; queries flush edits", async (t) => {
  const h = await harness(t, [definition("ts-ls", ["typescript", "javascript"])]);
  const model = h.addModel("/repo/a.ts");
  await h.activate();
  model.change("😀updated");
  model.changeLanguage("javascript");
  await h.settle();
  const documentCalls = h.calls.filter((c) => c.method.startsWith("textDocument/"));
  assert.deepEqual(documentCalls.map((c) => c.method), ["textDocument/didOpen", "textDocument/didClose", "textDocument/didOpen"]);
  assert.equal(documentCalls[2].params.textDocument.languageId, "javascript");
  assert.equal(documentCalls[2].params.textDocument.text, "😀updated");
  model.change("query snapshot");
  await h.hover(model);
  const queryCalls = h.calls.filter((c) => c.method.startsWith("textDocument/")).slice(-2);
  assert.deepEqual(queryCalls.map((c) => c.method), ["textDocument/didChange", "textDocument/hover"]);
  assert.deepEqual(queryCalls[0].params.contentChanges[0].range.end, { line: 0, character: 9 });
});

test("initialize uses encoded file URIs and honest configuration/watcher capabilities", async (t) => {
  const h = await harness(t, [definition("vue-ls", ["vue"])]);
  h.addModel("C:/My Project/日本語/a.vue", "<template />", "vue");
  await h.activate("C:/My Project/日本語");
  const init = h.calls.find((c) => c.method === "initialize").params;
  assert.equal(init.rootUri, URI.file("C:/My Project/日本語").toString());
  assert.equal(URI.parse(init.rootUri).path.toLowerCase(), "/c:/my project/日本語");
  assert.equal(init.workspaceFolders[0].name, "日本語");
  assert.equal(init.capabilities.workspace.configuration, true);
  // The Go session watches the root for the registrations it mirrors (backend/lsp/watch.go).
  assert.deepEqual(init.capabilities.workspace.didChangeWatchedFiles, { dynamicRegistration: true, relativePatternSupport: true });
  assert.deepEqual(init.capabilities.workspace.diagnostics, { refreshSupport: true });
  assert.deepEqual(init.capabilities.general.positionEncodings, ["utf-16"]);
  // SDK and standalone compatibility are enforced by the native adapter.
  assert.equal(init.initializationOptions, undefined);
  h.emit({ root: "C:/My Project/日本語", serverId: "vue-ls", generation: 1,
    method: "workspace/configuration", requestId: 7, params: { items: [{ section: "vue" }] } });
  await h.settle();
  assert.deepEqual(h.calls.find((c) => c.method === "reply").args[4], [null]);
});

test("language change lazily starts a different server and discards the old debounce", async (t) => {
  const h = await harness(t, [definition("ts-ls"), definition("json-ls", ["json"])]);
  const model = h.addModel("/repo/a.txt");
  await h.activate();
  model.change('{"ok":true}');
  model.changeLanguage("json");
  await h.settle();
  assert.equal(h.calls.some((c) => c.serverId === "ts-ls" && c.method === "textDocument/didClose"), true);
  const opened = h.calls.find((c) => c.serverId === "json-ls" && c.method === "textDocument/didOpen");
  assert.equal(opened.params.textDocument.languageId, "json");
  assert.equal(opened.params.textDocument.text, '{"ok":true}');
  assert.equal(h.calls.some((c) => c.method === "textDocument/didChange"), false);
});

test("pending didOpen serializes edits and saves; coalesced changes use last successfully sent text", async (t) => {
  let release;
  const blocked = new Promise((resolve) => { release = resolve; });
  const h = await harness(t, [definition("ts-ls")], {
    notify: (_, method) => method === "textDocument/didOpen" ? blocked : undefined,
  });
  const model = h.addModel("/repo/a.ts", "old\n😀");
  const controller = await h.activate();
  model.change("intermediate");
  model.change("final");
  const saving = controller.notifyFileSaved("a.ts");
  await h.settle();
  assert.equal(h.calls.some((c) => c.method === "textDocument/didChange"), false);
  release();
  await saving;
  const changes = h.calls.filter((c) => c.method.startsWith("textDocument/"));
  assert.deepEqual(changes.map((c) => c.method), ["textDocument/didOpen", "textDocument/didChange", "textDocument/didSave"]);
  assert.deepEqual(changes[1].params.contentChanges, [{
    range: { start: { line: 0, character: 0 }, end: { line: 1, character: 2 } }, text: "final",
  }]);
});

test("failed change retires the stale session instead of saving or querying it", async (t) => {
  const h = await harness(t, [definition("ts-ls")], {
    notify: (_, method) => { if (method === "textDocument/didChange") throw new Error("transport failed"); },
  });
  const model = h.addModel("/repo/a.ts");
  const controller = await h.activate();
  model.change("new");
  await controller.notifyFileSaved("a.ts");
  await h.settle();
  assert.equal(h.calls.some((c) => c.method === "textDocument/didSave"), false);
  assert.equal(h.calls.some((c) => c.method === "stop"), true);
  assert.equal(await h.hover(model), null);
});

test("a model disposed during handshake is never reopened", async (t) => {
  let release;
  const blocked = new Promise((resolve) => { release = resolve; });
  const h = await harness(t, [definition("ts-ls")], {
    initialize: async () => { await blocked; return { capabilities: { textDocumentSync: syncOptions() } }; },
  });
  const model = h.addModel("/repo/a.ts");
  await h.activate();
  h.disposeModel(model);
  release();
  await h.settle();
  assert.equal(h.calls.some((c) => c.method === "textDocument/didOpen"), false);
});

const diagnostic = (message = "JSON syntax error") => ({
  range: { start: { line: 0, character: 1 }, end: { line: 0, character: 2 } },
  severity: 1, message, source: "json",
});
const pullCapabilities = (identifier) => ({ capabilities: {
  hoverProvider: true, textDocumentSync: syncOptions(),
  diagnosticProvider: { documentSelector: null, interFileDependencies: false, workspaceDiagnostics: false,
    ...(identifier ? { identifier } : {}) },
} });
const pulls = (h) => h.calls.filter((c) => c.method === "textDocument/diagnostic");
const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

test("real runtime pulls after initialized/open, caches unchanged resultId, replaces and clears full markers", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const replies = [
    { kind: "full", items: [diagnostic()], resultId: "one" },
    { kind: "unchanged", resultId: "two" },
    { kind: "full", items: [], resultId: "three" },
    { kind: "full", items: [diagnostic("new error")] },
    { kind: "full", items: [] },
  ];
  const h = await harness(t, [definition("json-ls", ["json"])], {
    initialize: () => pullCapabilities("json"),
    request: (call) => call.method === "textDocument/diagnostic" ? replies.shift() : { contents: "hover" },
  });
  const model = h.addModel("/repo/a.json", "{", "json");
  const controller = await h.activate();
  assert.deepEqual(h.calls.find((c) => c.method === "initialize").params.capabilities.textDocument.diagnostic,
    { dynamicRegistration: false, relatedDocumentSupport: false });
  assert.equal(pulls(h).length, 0);
  await h.advance();
  assert.deepEqual(pulls(h)[0].params, { textDocument: { uri: model.uri.toString() }, identifier: "json" });
  assert.equal(pulls(h)[0].timeoutMs, 10000);
  assert.deepEqual(h.calls.filter((c) => ["initialized", "textDocument/didOpen", "textDocument/diagnostic"].includes(c.method))
    .map((c) => c.method), ["initialized", "textDocument/didOpen", "textDocument/diagnostic"]);
  assert.equal(h.modelMarkers(model)[0].message, "JSON syntax error");
  assert.equal(h.modelMarkers(model)[0].severity, 8);
  assert.equal(h.modelMarkers(model)[0].startColumn, 2);
  await controller.notifyFileSaved("a.json");
  await h.advance();
  assert.equal(pulls(h)[1].params.previousResultId, "one");
  assert.equal(h.modelMarkers(model)[0].message, "JSON syntax error");
  model.change("{}");
  await h.advance(300); // didChange debounce then diagnostic debounce.
  await h.advance();
  assert.equal(pulls(h)[2].params.previousResultId, "two");
  assert.deepEqual(h.modelMarkers(model), []);
  const changeAt = h.calls.findIndex((c) => c.method === "textDocument/didChange");
  assert.ok(changeAt < h.calls.indexOf(pulls(h)[2]));
  await controller.notifyFileSaved("a.json");
  await h.advance();
  assert.equal(pulls(h)[3].params.previousResultId, "three");
  assert.equal(h.modelMarkers(model)[0].message, "new error");
  await controller.notifyFileSaved("a.json");
  await h.advance();
  assert.equal("previousResultId" in pulls(h)[4].params, false); // Full without id resets cache.
  assert.equal(h.calls.some((c) => c.method === "workspace/diagnostic"), false);
});

test("pull waits for didOpen/didSave transport, and supports identifier-less providers", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const open = deferred();
  const save = deferred();
  const h = await harness(t, [definition("json-ls", ["json"])], {
    initialize: () => pullCapabilities(),
    notify: (_, method) => method === "textDocument/didOpen" ? open.promise : method === "textDocument/didSave" ? save.promise : undefined,
    request: () => ({ kind: "full", items: [] }),
  });
  h.addModel("/repo/a.json", "{}", "json");
  const controller = await h.activate();
  await h.advance();
  assert.equal(pulls(h).length, 0);
  open.resolve();
  await h.settle();
  assert.equal(pulls(h).length, 1);
  assert.equal("identifier" in pulls(h)[0].params, false);
  const saving = controller.notifyFileSaved("a.json");
  await h.settle();
  await h.advance(1000);
  assert.equal(pulls(h).length, 1);
  save.resolve();
  await saving;
  await h.advance();
  assert.equal(pulls(h).length, 2);
});

test("stale model-version response is discarded; changes coalesce into one serialized new pull", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const old = deferred();
  const h = await harness(t, [definition("json-ls", ["json"])], {
    initialize: () => pullCapabilities(),
    request: () => pulls(h).length === 1 ? old.promise : { kind: "full", items: [diagnostic("current")], resultId: "current" },
  });
  const model = h.addModel("/repo/a.json", "{", "json");
  await h.activate();
  await h.advance();
  model.change("{x");
  model.change("{y");
  // Even before didChange is flushed, Monaco version invalidates the old response.
  old.resolve({ kind: "full", items: [diagnostic("stale")], resultId: "stale" });
  await h.settle();
  assert.deepEqual(h.modelMarkers(model), []);
  await h.advance(300);
  await h.advance();
  assert.equal(pulls(h).length, 2);
  assert.equal("previousResultId" in pulls(h)[1].params, false);
  assert.equal(h.modelMarkers(model)[0].message, "current");
});

test("an edit during awaited didChange never labels the old server snapshot with the new model version", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const change = deferred();
  let changes = 0;
  const h = await harness(t, [definition("json-ls", ["json"])], {
    initialize: () => pullCapabilities(),
    notify: (_, method) => method === "textDocument/didChange" && ++changes === 1 ? change.promise : undefined,
    request: () => ({ kind: "full", items: [diagnostic("synced")] }),
  });
  const model = h.addModel("/repo/a.json", "{", "json");
  await h.activate();
  model.change("first edit");
  await h.advance(); // Initial pull flushes the edit but is blocked in native didChange.
  assert.equal(pulls(h).length, 0);
  model.change("second edit");
  change.resolve();
  await h.settle();
  assert.equal(pulls(h).length, 0);
  assert.deepEqual(h.modelMarkers(model), []);
  await h.advance();
  assert.equal(pulls(h).length, 1);
  const changesSent = h.calls.filter((c) => c.method === "textDocument/didChange");
  assert.equal(changesSent.length, 2);
  assert.equal(changesSent[1].params.contentChanges[0].text, "second edit");
  assert.equal(h.modelMarkers(model)[0].message, "synced");
});

test("pulls serialize across documents and debounce edits while a request is running", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const first = deferred();
  const h = await harness(t, [definition("json-ls", ["json"])], {
    initialize: () => pullCapabilities(),
    request: () => pulls(h).length === 1 ? first.promise : { kind: "full", items: [] },
  });
  const model = h.addModel("/repo/a.json", "{", "json");
  h.addModel("/repo/b.json", "{}", "json");
  const controller = await h.activate();
  await h.advance();
  assert.equal(pulls(h).length, 1);
  model.change("{x");
  await controller.notifyFileSaved("a.json");
  await controller.notifyFileSaved("a.json");
  await h.advance(1000);
  assert.equal(pulls(h).length, 1);
  first.resolve({ kind: "full", items: [diagnostic("old")], resultId: "old" });
  await h.settle();
  assert.deepEqual(h.modelMarkers(model), []);
  assert.equal(pulls(h).length, 2); // Other document's queued pull.
  await h.advance();
  assert.equal(pulls(h).length, 3); // One coalesced new pull for the edited document.
});

test("late unchanged and malformed reports preserve the last valid marker/cache without spinning", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const late = deferred();
  const h = await harness(t, [definition("json-ls", ["json"])], {
    initialize: () => pullCapabilities(),
    request: () => {
      if (pulls(h).length === 1) return { kind: "full", items: [diagnostic("cached")], resultId: "cached" };
      if (pulls(h).length === 2) return late.promise;
      return { kind: "full", items: null, resultId: "invalid" };
    },
  });
  const model = h.addModel("/repo/a.json", "{", "json");
  const controller = await h.activate();
  await h.advance();
  await controller.notifyFileSaved("a.json");
  await h.advance();
  model.change("new text");
  late.resolve({ kind: "unchanged", resultId: "stale" });
  await h.settle();
  await h.advance(300);
  await h.advance();
  assert.equal(pulls(h)[2].params.previousResultId, "cached");
  assert.equal(h.modelMarkers(model)[0].message, "cached");
  await h.advance(60000);
  assert.equal(pulls(h).length, 3);
  await controller.notifyFileSaved("a.json");
  await h.advance();
  assert.equal(pulls(h)[3].params.previousResultId, "cached");
});

test("pull failure/cancellation never retires ready session or retries without a fresh event", async (t) => {
  for (const failure of [new Error("diagnostic failed"), new DOMException("Aborted", "AbortError")]) {
    await t.test(failure.name, async (t) => {
      t.mock.timers.enable({ apis: ["setTimeout"] });
      const h = await harness(t, [definition("json-ls", ["json"])], {
        initialize: () => pullCapabilities(),
        request: (call) => {
          if (call.method === "textDocument/hover") return { contents: "working" };
          if (pulls(h).length === 1) throw failure;
          return { kind: "full", items: [diagnostic()] };
        },
      });
      const model = h.addModel("/repo/a.json", "{", "json");
      const controller = await h.activate();
      await h.advance();
      await h.advance(60000);
      assert.equal(pulls(h).length, 1);
      assert.equal(h.calls.some((c) => c.method === "stop"), false);
      assert.equal(h.runtimeStates.get("/repo\0json-ls").phase, "ready");
      assert.equal((await h.hover(model)).contents[0].value, "working");
      await controller.notifyFileSaved("a.json");
      await h.advance();
      assert.equal(pulls(h).length, 2);
      assert.equal(h.modelMarkers(model)[0].message, "JSON syntax error");
    });
  }
});

test("stop clears markers, aborts pull, and old generation cannot overwrite a restarted session", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const late = deferred();
  const h = await harness(t, [definition("json-ls", ["json"])], {
    initialize: () => pullCapabilities(),
    request: () => pulls(h).length === 2 ? late.promise : { kind: "full", items: [diagnostic("live")], resultId: "live" },
  });
  const model = h.addModel("/repo/a.json", "{", "json");
  const controller = await h.activate();
  await h.advance();
  assert.equal(h.modelMarkers(model)[0].message, "live");
  await controller.notifyFileSaved("a.json");
  await h.advance();
  controller.sync("/repo", []);
  await h.settle();
  assert.deepEqual(h.modelMarkers(model), []);
  assert.equal(pulls(h)[1].signal.aborted, true);
  controller.sync("/repo", ["json-ls"]);
  await h.settle();
  await h.advance();
  assert.equal(pulls(h)[2].generation, 2);
  assert.equal("previousResultId" in pulls(h)[2].params, false);
  late.resolve({ kind: "full", items: [diagnostic("old generation")], resultId: "old" });
  await h.settle();
  assert.equal(h.modelMarkers(model)[0].message, "live");
  h.emit({ root: "/repo", serverId: "json-ls", generation: 2, method: "$/serverStopped", params: { final: true } });
  assert.deepEqual(h.modelMarkers(model), []);
  await h.advance(60000);
  assert.equal(pulls(h).length, 3);
});

test("disposed/replaced model and language reopen reject late reports even with matching version ids", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const late = deferred();
  const h = await harness(t, [definition("json-ls", ["json", "jsonc"])], {
    initialize: () => pullCapabilities(),
    request: () => pulls(h).length === 1 ? late.promise : { kind: "full", items: [diagnostic("fresh")] },
  });
  const old = h.addModel("/repo/a.json", "{", "json");
  await h.activate();
  await h.advance();
  h.disposeModel(old);
  const replacement = h.addModel("/repo/a.json", "{", "json", true);
  await h.settle();
  assert.equal(pulls(h)[0].signal.aborted, true);
  late.resolve({ kind: "full", items: [diagnostic("disposed")] });
  await h.settle();
  assert.deepEqual(h.modelMarkers(replacement), []);
  await h.advance();
  assert.equal(h.modelMarkers(replacement)[0].message, "fresh");
  replacement.changeLanguage("jsonc");
  await h.settle();
  assert.deepEqual(h.modelMarkers(replacement), []);
  await h.advance();
  assert.equal("previousResultId" in pulls(h)[2].params, false);
  assert.equal(h.modelMarkers(replacement)[0].message, "fresh");
});

test("stop cancels a not-yet-fired diagnostic timer", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = await harness(t, [definition("json-ls", ["json"])], { initialize: () => pullCapabilities() });
  h.addModel("/repo/a.json", "{", "json");
  const controller = await h.activate();
  controller.sync("/repo", []);
  await h.settle();
  await h.advance(60000);
  assert.equal(pulls(h).length, 0);
});

test("openClose false and missing capabilities do not send unsupported methods", async (t) => {
  const h = await harness(t, [definition("ts-ls")], {
    initialize: () => ({ capabilities: { textDocumentSync: { change: 0, save: false, openClose: false } } }),
  });
  const model = h.addModel("/repo/a.ts");
  const controller = await h.activate();
  model.change("new");
  await controller.notifyFileSaved("a.ts");
  assert.equal(await h.hover(model), null);
  h.disposeModel(model);
  await h.settle();
  assert.equal(h.calls.some((c) => c.method.startsWith("textDocument/")), false);
});

test("push diagnostics for unopened project files are kept, shown on open and dropped on stop", async (t) => {
  const h = await harness(t, [definition("gopls", ["go"])]);
  h.addModel("/repo/main.go", "package main", "go");
  await h.activate();
  const closedUri = URI.file("/repo/util/util.go").toString();
  h.emit({ root: "/repo", serverId: "gopls", generation: 1, method: "textDocument/publishDiagnostics",
    params: { uri: closedUri, diagnostics: [diagnostic("undefined: x"), { message: "no range" }] } });
  h.emit({ root: "/repo", serverId: "gopls", generation: 1, method: "textDocument/publishDiagnostics",
    params: { uri: URI.file("/usr/local/go/src/fmt/print.go").toString(), diagnostics: [diagnostic()] } });
  await h.settle();
  assert.deepEqual(h.problems.get("/repo", "gopls", closedUri).map((item) => item.message), ["undefined: x"]);
  assert.deepEqual(h.problems.summary("/repo").files.map((file) => file.path), ["util/util.go"],
    "files outside the root stay editor-only");

  const later = h.addModel("/repo/util/util.go", "package util", "go", true);
  await h.settle();
  assert.deepEqual(h.modelMarkers(later, "gopls").map((marker) => marker.message), ["undefined: x"]);

  // Switching the project stops the server; its problems go with it.
  await h.activate("/other", []);
  assert.equal(h.problems.summary("/repo").files.length, 0);
});

test("workspace pulls report unopened files, resend result ids, honour refresh and skip open documents", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const closedUri = URI.file("/repo/b.json").toString();
  const replies = [
    { items: [
      { kind: "full", uri: closedUri, version: null, items: [diagnostic("closed file")], resultId: "w1" },
      { kind: "full", uri: URI.file("/repo/a.json").toString(), version: 1, items: [diagnostic("stale open report")] },
    ] },
    { items: [{ kind: "unchanged", uri: closedUri, version: null, resultId: "w2" }] },
  ];
  const h = await harness(t, [definition("json-ls", ["json"])], {
    initialize: () => ({ capabilities: { textDocumentSync: syncOptions(),
      diagnosticProvider: { identifier: "json", interFileDependencies: true, workspaceDiagnostics: true } } }),
    request: (call) => call.method === "workspace/diagnostic" ? replies.shift() ?? { items: [] }
      : call.method === "textDocument/diagnostic" ? { kind: "full", items: [] } : null,
  });
  const model = h.addModel("/repo/a.json", "{}", "json");
  await h.activate();
  await h.advance(0);
  const workspacePulls = () => h.calls.filter((c) => c.method === "workspace/diagnostic");
  assert.equal(workspacePulls().length, 1);
  assert.deepEqual(workspacePulls()[0].params, { identifier: "json", previousResultIds: [] });
  assert.deepEqual(h.problems.get("/repo", "json-ls", closedUri).map((item) => item.message), ["closed file"]);
  assert.deepEqual(h.problems.get("/repo", "json-ls", model.uri.toString()), [], "open documents belong to the document pull");

  // Idle: no further pulls without an event.
  await h.advance(5000);
  assert.equal(workspacePulls().length, 1);

  h.emit({ root: "/repo", serverId: "json-ls", generation: 1, method: "workspace/diagnostic/refresh", requestId: 9, params: null });
  await h.settle();
  await h.advance(0);
  assert.equal(workspacePulls().length, 2);
  assert.deepEqual(workspacePulls()[1].params.previousResultIds, [{ uri: closedUri, value: "w1" }]);
  assert.deepEqual(h.problems.get("/repo", "json-ls", closedUri).map((item) => item.message), ["closed file"],
    "unchanged keeps the previous report");
  assert.equal(h.calls.some((c) => c.method === "reply" && c.args[3] === 9), true);

  model.change('{"edited": true}');
  await h.advance(300);
  await h.advance(1500);
  assert.equal(workspacePulls().length, 3);
  assert.deepEqual(workspacePulls()[2].params.previousResultIds, [{ uri: closedUri, value: "w2" }]);
});

test("Vue 3 hybrid starts the vue-ts companion and bridges tsserver requests through it", async (t) => {
  const h = await harness(t, [
    { ...definition("vue-ls", ["vue"]), companions: ["vue-ts"] },
    definition("vue-ts", ["vue"], true),
  ], {
    request: (call) => call.method === "workspace/executeCommand"
      ? { seq: 0, type: "response", success: true, body: { configFileName: "/repo/tsconfig.json" } }
      : null,
  });
  h.addModel("/repo/App.vue", "<template />", "vue");
  await h.activate("/repo", ["vue-ls"]);
  assert.deepEqual(h.calls.filter((c) => c.method === "start").map((c) => c.serverId).sort(), ["vue-ls", "vue-ts"]);

  const vueGeneration = h.calls.find((c) => c.method === "initialize" && c.serverId === "vue-ls").generation;
  h.emit({ root: "/repo", serverId: "vue-ls", generation: vueGeneration, method: "tsserver/request",
    params: [[5, "_vue:projectInfo", { file: "/repo/App.vue", needFileNameList: false }]] });
  await h.settle();
  const forwarded = h.calls.find((c) => c.method === "workspace/executeCommand");
  assert.equal(forwarded.serverId, "vue-ts");
  assert.deepEqual(forwarded.params, { command: "typescript.tsserverRequest",
    arguments: ["_vue:projectInfo", { file: "/repo/App.vue", needFileNameList: false }, { isAsync: true, lowPriority: true }] });
  const response = h.calls.find((c) => c.method === "tsserver/response");
  assert.equal(response.serverId, "vue-ls");
  assert.deepEqual(response.params, [[5, { configFileName: "/repo/tsconfig.json" }]]);

  // Malformed requests are ignored; servers without a bridge are never answered.
  h.emit({ root: "/repo", serverId: "vue-ls", generation: vueGeneration, method: "tsserver/request", params: [["x"]] });
  h.emit({ root: "/repo", serverId: "ts-ls", generation: 1, method: "tsserver/request", params: [[6, "quickinfo", {}]] });
  await h.settle();
  assert.equal(h.calls.filter((c) => c.method === "tsserver/response").length, 1);
});

test("reinstall retries an enabled failed Vue companion and refreshes both Vue sessions", async (t) => {
  let repaired = false;
  const h = await harness(t, [
    { ...definition("vue-ls", ["vue"]), companions: ["vue-ts"] },
    definition("vue-ts", ["vue"], true),
    definition("json-ls", ["json"]),
  ], {
    initialize: (id) => {
      if (id === "vue-ts" && !repaired) throw new Error("incompatible SDK");
      return { capabilities: { hoverProvider: true, textDocumentSync: syncOptions() } };
    },
  });
  const model = h.addModel("/repo/App.vue", "<template />", "vue");
  h.addModel("/repo/data.json", "{}", "json");
  const controller = await h.activate("/repo", ["vue-ls", "json-ls"]);
  assert.equal(h.runtimeStates.get("/repo\0vue-ts").phase, "failed");
  repaired = true;
  await h.installed("vue-ts");
  assert.equal(h.runtimeStates.get("/repo\0vue-ts").phase, "ready");
  assert.equal(h.runtimeStates.get("/repo\0vue-ls").phase, "ready");
  for (const id of ["vue-ls", "vue-ts"]) {
    assert.equal(h.calls.filter((c) => c.method === "start" && c.serverId === id).length, 2);
    assert.ok(h.calls.some((c) => c.method === "textDocument/didOpen" && c.serverId === id && c.params.textDocument.uri === model.uri.toString()));
  }
  assert.equal(h.calls.filter((c) => c.method === "start" && c.serverId === "json-ls").length, 1);
  assert.equal(h.calls.filter((c) => c.method === "stop" && c.serverId === "json-ls").length, 0);
  await controller.dispose();
  const count = h.calls.length;
  await h.installed("vue-ts");
  assert.equal(h.calls.length, count, "disposed controller must unsubscribe");
});

test("reinstall discovers a newly installed Vue hybrid companion without reopening the editor", async (t) => {
  const servers = [definition("vue-ls", ["vue"]), definition("vue-ts", ["vue"], true)];
  const h = await harness(t, servers);
  h.addModel("/repo/App.vue", "<template />", "vue");
  await h.activate("/repo", ["vue-ls"]);
  assert.equal(h.calls.filter((c) => c.method === "start" && c.serverId === "vue-ts").length, 0);
  servers[0].companions = ["vue-ts"];
  await h.installed("vue-ts");
  assert.equal(h.runtimeStates.get("/repo\0vue-ts").phase, "ready");
  assert.equal(h.calls.filter((c) => c.method === "start" && c.serverId === "vue-ls").length, 2);
});

test("reinstall does not launch a disabled server or revive an inactive workspace", async (t) => {
  const h = await harness(t, [definition("vue-ls", ["vue"]), definition("vue-ts", ["vue"], true)]);
  h.addModel("/repo/App.vue", "<template />", "vue");
  const controller = await h.activate("/repo", []);
  await h.installed("vue-ts");
  assert.equal(h.calls.filter((c) => c.method === "start").length, 0);
  controller.deactivate();
  await h.installed("vue-ts");
  assert.equal(h.calls.filter((c) => c.method === "start").length, 0);
});

test("restartBuiltinServer restarts a running server and clears its crash count", async (t) => {
  const h = await harness(t, [definition("jdtls", ["java"]), definition("json-ls", ["json"])]);
  const model = h.addModel("/repo/Main.java", "class Main {}", "java");
  h.addModel("/repo/data.json", "{}", "json");
  await h.activate("/repo", ["jdtls", "json-ls"]);
  assert.equal(await h.restartBuiltinServer("/repo", "jdtls"), true);
  assert.equal(h.calls.filter((c) => c.method === "stop" && c.serverId === "jdtls").length, 1);
  assert.equal(h.calls.filter((c) => c.method === "start" && c.serverId === "jdtls").length, 2);
  assert.equal(h.calls.filter((c) => c.serverId === "json-ls" && (c.method === "stop" || c.method === "start")).length, 1);
  assert.equal(h.runtimeStates.get("/repo\0jdtls").phase, "ready");
  const opens = h.calls.filter((c) => c.method === "textDocument/didOpen" && c.serverId === "jdtls");
  assert.equal(opens.at(-1).params.textDocument.uri, model.uri.toString());
  // Another root is not this controller's.
  assert.equal(await h.restartBuiltinServer("/other", "jdtls"), false);
});

test("restartBuiltinServer starts an enabled server without an open file, never a disabled one", async (t) => {
  const h = await harness(t, [definition("jdtls", ["java"]), definition("json-ls", ["json"])]);
  const controller = await h.activate("/repo", ["jdtls"]);
  assert.equal(h.calls.filter((c) => c.method === "start").length, 0);
  assert.equal(await controller.builtinServerAvailability("jdtls", "java"), "available");
  assert.equal(await h.restartBuiltinServer("/repo", "jdtls"), true);
  assert.equal(h.calls.filter((c) => c.method === "start" && c.serverId === "jdtls").length, 1);
  assert.equal(h.calls.filter((c) => c.method === "stop").length, 0);
  assert.equal(await controller.builtinServerAvailability("json-ls", "json"), "disabled");
  assert.equal(await h.restartBuiltinServer("/repo", "json-ls"), false);
  assert.equal(h.calls.filter((c) => c.method === "start" && c.serverId === "json-ls").length, 0);
  controller.deactivate();
  assert.equal(await controller.builtinServerAvailability("jdtls", "java"), "unavailable");
  assert.equal(await h.restartBuiltinServer("/repo", "jdtls"), false);
});

test("a held server is never idle-stopped; after the release it can be", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { holdLspServer } = await import("../src/services/lsp-server-holds.ts");
  const h = await harness(t, [definition("jdtls", ["java"])], { idleStopSeconds: 60 });
  await h.activate("/repo", ["jdtls"]);
  // No Java file is open: Java debugging started jdtls directly and holds it.
  const release = holdLspServer("/repo/", "jdtls");
  assert.equal(await h.restartBuiltinServer("/repo", "jdtls"), true);
  await h.advance(120_000);
  assert.equal(h.calls.filter((c) => c.method === "stop").length, 0);
  assert.equal(h.runtimeStates.get("/repo\0jdtls").phase, "ready");
  release();
  release(); // idempotent
  await h.advance(59_000);
  assert.equal(h.calls.filter((c) => c.method === "stop").length, 0);
  await h.advance(2_000);
  assert.equal(h.calls.filter((c) => c.method === "stop" && c.serverId === "jdtls").length, 1);
});

test("restart-required prompts for running sessions, never while a Java launch restarts jdtls itself", async (t) => {
  const { beginBuiltinServerLaunch } = await import("../src/services/lsp-server-holds.ts");
  const h = await harness(t, [definition("jdtls", ["java"])]);
  h.addModel("/repo/Main.java", "class Main {}", "java");
  await h.activate("/repo", ["jdtls"]);
  const event = { serverId: "jdtls", reason: "bundles" };

  // In flight when the event arrives: decided before the import, so ending
  // the launch right away does not bring the prompt back.
  const end = beginBuiltinServerLaunch("jdtls");
  h.restartRequired(event);
  end();
  await h.settle();
  assert.equal(h.restartPrompts.length, 0);

  // Started while the prompt module loads: checked again after the import.
  h.restartRequired(event);
  const endLater = beginBuiltinServerLaunch("jdtls");
  await h.settle();
  endLater();
  assert.equal(h.restartPrompts.length, 0);

  // No launch: one prompt, whose Restart restarts the running jdtls.
  h.restartRequired(event);
  await h.settle();
  assert.equal(h.restartPrompts.length, 1);
  await h.restartPrompts[0].restart();
  await h.settle();
  assert.equal(h.calls.filter((c) => c.method === "start" && c.serverId === "jdtls").length, 2);
});

test("a held server survives the background stop and is stopped once released", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { holdLspServer } = await import("../src/services/lsp-server-holds.ts");
  const h = await harness(t, [definition("jdtls", ["java"]), definition("json-ls", ["json"])], { backgroundStopSeconds: 30 });
  h.addModel("/repo/Main.java", "class Main {}", "java");
  h.addModel("/repo/data.json", "{}", "json");
  const controller = await h.activate("/repo", ["jdtls", "json-ls"]);
  const release = holdLspServer("/repo", "jdtls");
  controller.deactivate();
  await h.advance(31_000);
  const stops = (id) => h.calls.filter((c) => c.method === "stop" && c.serverId === id).length;
  assert.equal(stops("json-ls"), 1);
  assert.equal(stops("jdtls"), 0);
  release();
  await h.settle();
  assert.equal(stops("jdtls"), 1);
});

test("switching to another project keeps a held server of the old one until it is released", async (t) => {
  const { holdLspServer } = await import("../src/services/lsp-server-holds.ts");
  const h = await harness(t, [definition("jdtls", ["java"])]);
  h.addModel("/repo/Main.java", "class Main {}", "java");
  const first = await h.activate("/repo", ["jdtls"]);
  const release = holdLspServer("/repo", "jdtls");
  first.deactivate();
  await h.activate("/other", ["jdtls"]);
  assert.equal(h.calls.filter((c) => c.method === "stop" && c.root === "/repo").length, 0);
  release();
  await h.settle();
  assert.equal(h.calls.filter((c) => c.method === "stop" && c.root === "/repo" && c.serverId === "jdtls").length, 1);
  await first.dispose();
});

test("the restart prompt defers a jdtls that hosts a debug session until the session ends", async (t) => {
  const { holdLspServer } = await import("../src/services/lsp-server-holds.ts");
  const h = await harness(t, [definition("jdtls", ["java"])]);
  h.addModel("/repo/Main.java", "class Main {}", "java");
  await h.activate("/repo", ["jdtls"]);
  const release = holdLspServer("/repo", "jdtls", "debug-session");
  h.restartRequired({ serverId: "jdtls", reason: "bundles" });
  await h.settle();
  assert.equal(h.restartPrompts.length, 1);
  assert.deepEqual(h.restartPrompts[0].options, { deferred: true });
  const starts = () => h.calls.filter((c) => c.method === "start" && c.serverId === "jdtls").length;
  await h.restartPrompts[0].restart();
  await h.settle();
  assert.equal(starts(), 1, "the debug session's jdtls keeps running");
  assert.equal(h.calls.filter((c) => c.method === "stop").length, 0);
  release();
  await h.settle();
  assert.equal(starts(), 2, "restarted once the session ended");
  assert.equal(h.calls.filter((c) => c.method === "stop" && c.serverId === "jdtls").length, 1);
});

const lspRange = (line) => ({ start: { line, character: 0 }, end: { line, character: 4 } });
const codeLensCapabilities = (codeLensProvider) => ({ capabilities: {
  textDocumentSync: syncOptions(), ...(codeLensProvider ? { codeLensProvider } : {}),
} });
const codeLens = (h, model) => h.providers.get(`registerCodeLensProvider:${model.getLanguageId()}`);

test("initialize declares CodeLens support and refresh", async (t) => {
  const h = await harness(t, [definition("gopls", ["go"])]);
  h.addModel("/repo/main.go", "package main", "go");
  await h.activate();
  const init = h.calls.find((c) => c.method === "initialize").params;
  assert.deepEqual(init.capabilities.textDocument.codeLens, {});
  assert.deepEqual(init.capabilities.workspace.codeLens, { refreshSupport: true });
});

test("CodeLens requests the server and routes commands through workspace/executeCommand", async (t) => {
  const h = await harness(t, [definition("gopls", ["go"])], {
    initialize: () => codeLensCapabilities({ resolveProvider: false }),
    request: (call) => call.method === "textDocument/codeLens" ? [
      { range: lspRange(2), command: { title: "run test", command: "gopls.run_tests", arguments: [{ uri: "x" }] } },
      { range: lspRange(4), command: { title: "3 references", command: "" } },
      { range: lspRange(6) },
      null,
      { command: { title: "no range", command: "gopls.tidy" } },
    ] : null,
  });
  const model = h.addModel("/repo/main.go", "package main", "go");
  await h.activate();
  const result = await codeLens(h, model).provideCodeLenses(model, token);
  const requests = h.calls.filter((c) => c.method === "textDocument/codeLens");
  assert.equal(requests.length, 1);
  assert.deepEqual(requests[0].params, { textDocument: { uri: model.uri.toString() } });
  assert.equal(result.lenses.length, 2, "lenses without a range, or without a command and no resolve, are dropped");
  const [run, label] = result.lenses;
  assert.deepEqual({ ...run.range }, { startLineNumber: 3, startColumn: 1, endLineNumber: 3, endColumn: 5 });
  assert.equal(run.command.id, "mincode.lsp.executeCommand");
  assert.equal(run.command.title, "run test");
  assert.deepEqual(run.command.arguments, [{
    root: "/repo", serverId: "gopls", generation: 1, title: "run test",
    command: "gopls.run_tests", arguments: [{ uri: "x" }],
  }]);
  assert.deepEqual(label.command, { id: "", title: "3 references" }, "a command-less title is a plain label");
});

test("CodeLens resolve is asked only for lenses without a command", async (t) => {
  const h = await harness(t, [definition("ts-ls")], {
    initialize: () => codeLensCapabilities({ resolveProvider: true }),
    request: (call) => {
      if (call.method === "textDocument/codeLens") return [
        { range: lspRange(1), data: { id: 7 } },
        { range: lspRange(3), command: { title: "ready", command: "ts.ready" } },
      ];
      if (call.method === "codeLens/resolve") return { ...call.params, command: { title: "2 implementations", command: "ts.impl", arguments: [1] } };
      return null;
    },
  });
  const model = h.addModel("/repo/a.ts");
  await h.activate();
  const provider = codeLens(h, model);
  const { lenses } = await provider.provideCodeLenses(model, token);
  assert.equal(lenses.length, 2);
  assert.equal(lenses[0].command, undefined);
  const resolved = await provider.resolveCodeLens(model, lenses[0], token);
  const resolves = h.calls.filter((c) => c.method === "codeLens/resolve");
  assert.equal(resolves.length, 1);
  assert.deepEqual(resolves[0].params, { range: lspRange(1), data: { id: 7 } }, "the raw server lens round-trips");
  assert.equal(resolved.command.title, "2 implementations");
  assert.deepEqual(resolved.command.arguments, [{
    root: "/repo", serverId: "ts-ls", generation: 1, title: "2 implementations", command: "ts.impl", arguments: [1],
  }]);
  assert.equal(await provider.resolveCodeLens(model, lenses[1], token), lenses[1]);
  assert.equal(h.calls.filter((c) => c.method === "codeLens/resolve").length, 1);
});

test("a server without codeLensProvider is never asked for CodeLens", async (t) => {
  const h = await harness(t, [definition("ts-ls")], {
    initialize: () => codeLensCapabilities(undefined),
    request: () => [{ range: lspRange(0), command: { title: "x", command: "x" } }],
  });
  const model = h.addModel("/repo/a.ts");
  await h.activate();
  assert.equal(await codeLens(h, model).provideCodeLenses(model, token), null);
  assert.equal(h.calls.some((c) => c.method === "textDocument/codeLens" || c.method === "codeLens/resolve"), false);
});

test("workspace/codeLens/refresh and session stop ask Monaco to refetch CodeLens", async (t) => {
  const h = await harness(t, [definition("gopls", ["go"])], {
    initialize: () => codeLensCapabilities({}),
  });
  const model = h.addModel("/repo/main.go", "package main", "go");
  await h.activate();
  let refreshes = 0;
  const listener = codeLens(h, model).onDidChange(() => { refreshes++; });
  t.after(() => listener.dispose());
  h.emit({ root: "/repo", serverId: "gopls", generation: 1, method: "workspace/codeLens/refresh", requestId: 11, params: null });
  await h.settle();
  assert.equal(refreshes, 1);
  const reply = h.calls.find((c) => c.method === "reply" && c.args[3] === 11);
  assert.equal(reply.args[4], null);
  // Stopping the session refetches so its lenses disappear.
  await h.activate("/other", []);
  assert.equal(refreshes, 2);
});

test("CodeLens onDidChange passes Monaco's disposables argument through", async (t) => {
  const h = await harness(t, [definition("gopls", ["go"])], { initialize: () => codeLensCapabilities({}) });
  const model = h.addModel("/repo/main.go", "package main", "go");
  await h.activate();
  const provider = codeLens(h, model);
  const disposables = [];
  let event;
  const subscription = provider.onDidChange((value) => { event = value; }, undefined, disposables);
  assert.deepEqual(disposables, [subscription]);
  h.emit({ root: "/repo", serverId: "gopls", generation: 1, method: "workspace/codeLens/refresh", requestId: 12, params: null });
  await h.settle();
  assert.equal(event, provider, "listeners receive the provider, as Monaco's IEvent<this> declares");
  subscription.dispose();
});

test("only navigation lens commands run in Monaco; showReferences opens the references peek", async (t) => {
  const uri = URI.file("/repo/main.go").toString();
  const other = URI.file("/repo/b.go").toString();
  const navigationArgs = (extra = []) => [uri, { line: 0, character: 5 },
    [{ uri, range: lspRange(3) }, { uri: other, range: lspRange(7) }, { uri, range: null }], ...extra];
  const h = await harness(t, [definition("gopls", ["go"])], {
    initialize: () => codeLensCapabilities({}),
    request: (call) => call.method === "textDocument/codeLens" ? [
      { range: lspRange(0), command: { title: "2 references", command: "editor.action.showReferences", arguments: navigationArgs() } },
      { range: lspRange(1), command: { title: "peek", command: "editor.action.peekLocations", arguments: navigationArgs(["goto"]) } },
      { range: lspRange(2), command: { title: "go", command: "editor.action.goToLocations", arguments: navigationArgs(["bogus", "msg", true]) } },
      { range: lspRange(3), command: { title: "run tests", command: "editor.action.formatDocument", arguments: [] } },
      { range: lspRange(4), command: { title: "broken", command: "editor.action.showReferences", arguments: ["x"] } },
      { range: lspRange(5), command: { title: "spoof", command: "mincode.lsp.executeCommand", arguments: [] } },
    ] : null,
  });
  const model = h.addModel("/repo/main.go", "package main", "go");
  await h.activate();
  const { lenses } = await codeLens(h, model).provideCodeLenses(model, token);
  const [references, peek, goTo, format, broken, spoof] = lenses.map((lens) => lens.command);
  const locationsOf = (command) => command.arguments[2].map((location) => [location.uri.toString(), location.range.startLineNumber]);

  assert.equal(references.id, "editor.action.peekLocations", "showReferences opens the references peek");
  assert.equal(references.title, "2 references");
  assert.equal(references.arguments[0].toString(), uri);
  assert.deepEqual(references.arguments[1], { lineNumber: 1, column: 6 });
  assert.deepEqual(locationsOf(references), [[uri, 4], [other, 8]], "malformed locations are dropped");
  assert.equal(references.arguments.length, 3);

  assert.equal(peek.id, "editor.action.peekLocations");
  assert.deepEqual(locationsOf(peek), [[uri, 4], [other, 8]]);
  assert.equal(peek.arguments[3], "goto");

  assert.equal(goTo.id, "editor.action.goToLocations");
  assert.deepEqual(locationsOf(goTo), [[uri, 4], [other, 8]]);
  assert.equal(goTo.arguments.length, 3, "unknown multiple values and extra args are dropped");

  assert.equal(format.id, "mincode.lsp.executeCommand", "a registered but non-navigation editor command is not run client-side");
  assert.equal(format.arguments[0].command, "editor.action.formatDocument");
  assert.deepEqual(broken, { id: "", title: "broken" }, "malformed navigation args are a plain label");
  assert.equal(spoof.id, "mincode.lsp.executeCommand");
  assert.equal(spoof.arguments[0].command, "mincode.lsp.executeCommand", "app commands never run in the client");
});

test("codeLens/resolve without a range keeps the original lens range", async (t) => {
  const h = await harness(t, [definition("ts-ls")], {
    initialize: () => codeLensCapabilities({ resolveProvider: true }),
    request: (call) => call.method === "textDocument/codeLens" ? [{ range: lspRange(5), data: 1 }]
      : call.method === "codeLens/resolve" ? { command: { title: "resolved", command: "ts.x" } } : null,
  });
  const model = h.addModel("/repo/a.ts");
  await h.activate();
  const provider = codeLens(h, model);
  const { lenses } = await provider.provideCodeLenses(model, token);
  const resolved = await provider.resolveCodeLens(model, lenses[0], token);
  assert.equal(resolved.command.title, "resolved");
  assert.deepEqual({ ...resolved.range }, { startLineNumber: 6, startColumn: 1, endLineNumber: 6, endColumn: 5 });
});

test("window/showMessage: errors always, info only as feedback to a command the user ran", async (t) => {
  const h = await harness(t, [definition("gopls", ["go"])], { initialize: () => codeLensCapabilities({}) });
  h.addModel("/repo/main.go", "package main", "go");
  await h.activate();
  const message = (type, text) => h.emit({ root: "/repo", serverId: "gopls", generation: 1, method: "window/showMessage", params: { type, message: text } });
  message(3, "Finished loading packages.");
  message(4, "Running...");
  message(1, "go: command not found");
  await h.settle();
  assert.deepEqual(h.feedback, [{ serverId: "gopls", kind: "error", message: "go: command not found" }]);

  await h.runCommand({ root: "/repo", serverId: "gopls", generation: 1, title: "run go generate", command: "gopls.generate", arguments: [] });
  message(3, "completed");
  await h.settle();
  assert.deepEqual(h.feedback.at(-1), { serverId: "gopls", kind: "info", message: "completed" });
  assert.equal(h.feedback.length, 2);
});

test("a failed server command is reported instead of swallowed", async (t) => {
  const h = await harness(t, [definition("gopls", ["go"])], {
    initialize: () => codeLensCapabilities({}),
    request: (call) => {
      if (call.method === "workspace/executeCommand") throw new Error("exit status 1:\ntouch: not found");
      return null;
    },
  });
  h.addModel("/repo/main.go", "package main", "go");
  await h.activate();
  await h.runCommand({ root: "/repo", serverId: "gopls", generation: 1, title: "run go generate", command: "gopls.generate", arguments: [] });
  assert.deepEqual(h.feedback, [{ serverId: "gopls", failed: "run go generate", reason: "exit status 1: touch: not found" }]);

  // A lens from a session that has since restarted fails the same visible way.
  await h.runCommand({ root: "/repo", serverId: "gopls", generation: 99, title: "run go generate", command: "gopls.generate", arguments: [] });
  assert.equal(h.feedback.length, 2);
  assert.equal(h.feedback[1].failed, "run go generate");
});

test("an over-limit file-watch notice marks the session without touching its state", async (t) => {
  const h = await harness(t, [definition("intelephense", ["php"])]);
  h.addModel("/repo/index.php", "<?php", "php");
  await h.activate("/repo", ["intelephense"]);
  assert.equal(h.runtimeStates.get("/repo\0intelephense").phase, "ready");
  h.emit({ root: "/repo", serverId: "intelephense", generation: 1, method: "$/mincode/fileWatchLimited", params: {} });
  assert.ok(h.watchLimited.has("/repo\0intelephense"), "the dialog is told external changes are not watched");
  assert.equal(h.runtimeStates.get("/repo\0intelephense").phase, "ready", "the server keeps serving");
  assert.equal(h.calls.filter((c) => c.method === "stop").length, 0);
});

test("work-done progress and the file-watch scan reach the status-bar state", async (t) => {
  const h = await harness(t, [{ ...definition("intelephense", ["php"]), name: "PHP / Intelephense" }]);
  h.addModel("/repo/index.php", "<?php", "php");
  await h.activate("/repo", ["intelephense"]);
  const init = h.calls.find((c) => c.method === "initialize").params;
  assert.deepEqual(init.capabilities.window, { workDoneProgress: true }, "servers only report progress to clients that declare it");
  assert.deepEqual(h.progressCalls.find((call) => call[0] === "name"), ["name", "/repo", "intelephense", "PHP / Intelephense"]);
  // window/workDoneProgress/create is a request the server waits on.
  h.emit({ root: "/repo", serverId: "intelephense", generation: 1, method: "window/workDoneProgress/create", requestId: 3, params: { token: "t" } });
  await h.settle();
  assert.deepEqual(h.calls.find((c) => c.method === "reply" && c.args[3] === 3)?.args[4], null);
  const progress = { token: "t", value: { kind: "report", percentage: 45, message: "5305 of 11633 files" } };
  h.emit({ root: "/repo", serverId: "intelephense", generation: 1, method: "$/progress", params: progress });
  h.emit({ root: "/repo", serverId: "intelephense", generation: 1, method: "$/mincode/fileWatchScan", params: { state: "begin" } });
  h.emit({ root: "/repo", serverId: "intelephense", generation: 1, method: "$/mincode/fileWatchScan", params: { state: "end" } });
  assert.deepEqual(h.progressCalls.filter((call) => call[0] !== "name"), [
    ["progress", "/repo", "intelephense", progress],
    ["scanning", "/repo", "intelephense", true],
    ["scanning", "/repo", "intelephense", false],
  ]);
});

// A client that declares window.workDoneProgress gets command results as
// progress (gopls: "completed" in the end of "running go generate") instead of
// window/showMessage; the user must still see that their command finished.
test("a work-done progress end message is command feedback, like showMessage", async (t) => {
  const h = await harness(t, [definition("gopls", ["go"])], { initialize: () => codeLensCapabilities({}) });
  h.addModel("/repo/main.go", "package main", "go");
  await h.activate();
  const progress = (token, value) => h.emit({ root: "/repo", serverId: "gopls", generation: 1, method: "$/progress", params: { token, value } });
  // Background work (indexing) ends without the user having run anything: no toast.
  progress("load", { kind: "begin", title: "Loading packages" });
  progress("load", { kind: "end", message: "Finished loading packages." });
  await h.settle();
  assert.deepEqual(h.feedback, []);

  await h.runCommand({ root: "/repo", serverId: "gopls", generation: 1, title: "run go generate", command: "gopls.generate", arguments: [] });
  progress("gen", { kind: "begin", title: "running go generate" });
  progress("gen", { kind: "end", message: "completed" });
  await h.settle();
  assert.deepEqual(h.feedback, [{ serverId: "gopls", kind: "info", message: "completed" }]);
});
