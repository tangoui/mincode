import assert from "node:assert/strict";
import test from "node:test";
import {
  LINTER_LIMITS,
  PLUGIN_LINTERS_STORAGE_KEY,
  clampDiagnostic,
  collectLinters,
  linterMarkerOwner,
  linterPreviewSummary,
  linterRunsOn,
  normalizeLinterPreferences,
  parseLintResult,
  parseLinterRef,
  selectedLinter,
  updateLinterPreference,
} from "../src/domain/plugins/linters.ts";
import { CONFIG_SYNC_STORAGE_PREFIX } from "../src/domain/settings/config-sync.ts";
import { notifyEditorSaved } from "../src/services/editor-save-participants.ts";
import { lazyContributionApply } from "../src/services/contributions/lazy-consumer.ts";
import { createLintScheduler } from "../src/services/plugin-linter-queue.ts";
import {
  attachMonacoLinters,
  refreshMonacoLinters,
  setMonacoLinterRuntime,
  setMonacoLinters,
  setPluginLinterInvoker,
} from "../src/services/monaco-linters.ts";

function contribution(pluginId, entries, extra = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: `${pluginId}:${entries.length}`, entries, issues: [] },
    ...extra,
  };
}

const eslint = { id: "eslint", title: "ESLint", languages: ["typescript", "javascript"], action: "lint", triggers: ["open", "save", "type"], debounceMs: 300 };

test("linters: entries are re-validated; only active process plugins contribute", () => {
  const result = collectLinters([
    contribution("z.lint", [eslint]),
    contribution("a.lint", [
      { id: "ok", title: "OK", languages: ["go"], action: "lint.go", triggers: ["save"], debounceMs: 750 },
      { id: "Bad", title: "x", languages: ["go"], action: "a", triggers: ["save"], debounceMs: 750 },
      { id: "trig", title: "T", languages: ["go"], action: "a", triggers: ["open", "open"], debounceMs: 750 },
      { id: "none", title: "T", languages: ["go"], action: "a", triggers: [], debounceMs: 750 },
      { id: "slow", title: "T", languages: ["go"], action: "a", triggers: ["type"], debounceMs: 99 },
      { id: "ctl", title: "bad\u202etitle", languages: ["go"], action: "a", triggers: ["save"], debounceMs: 750 },
      { id: "ok", title: "Again", languages: ["go"], action: "a", triggers: ["save"], debounceMs: 750 },
    ]),
    contribution("decl.lint", [eslint], { runtime: "declarative" }),
    contribution("off.lint", [eslint], { activeInWorkspace: false }),
  ]);
  assert.deepEqual(result.linters.map((linter) => linter.ref), ["a.lint/ok", "z.lint/eslint"]);
  assert.equal(result.issues.length, 6);
  assert.ok(result.issues.every((issue) => issue.stage === "frontend" && issue.pluginId === "a.lint"));
  assert.equal(linterMarkerOwner("z.lint/eslint"), "mincode-plugin-linter:z.lint/eslint");
  assert.deepEqual(parseLinterRef("z.lint/eslint"), { pluginId: "z.lint", linterId: "eslint" });
  assert.equal(parseLinterRef("eslint"), undefined);
});

test("linters: preferences are machine-local, off by default and normalized", () => {
  assert.ok(!PLUGIN_LINTERS_STORAGE_KEY.startsWith(CONFIG_SYNC_STORAGE_PREFIX), "never synced or exported");
  const byLanguage = JSON.parse(
    '{"typescript":{"linter":"z.lint/eslint","onType":true},"go":{"onType":true},"Bad Lang":{"linter":"z.lint/eslint"},"__proto__":{"linter":"z.lint/eslint"}}',
  );
  const normalized = normalizeLinterPreferences({ version: 1, byLanguage });
  assert.deepEqual(Object.keys(normalized.byLanguage), ["typescript"]);
  assert.equal(Object.getPrototypeOf(normalized.byLanguage), null);
  assert.deepEqual(normalizeLinterPreferences({ version: 2, byLanguage }).byLanguage, Object.create(null));

  const many = { version: 1, byLanguage: {} };
  for (let index = 0; index < 300; index += 1) many.byLanguage[`l${index}`] = { linter: "p/l" };
  assert.equal(Object.keys(normalizeLinterPreferences(many).byLanguage).length, LINTER_LIMITS.preferencesMax);

  let preferences = updateLinterPreference(normalized, "go", { linter: "a.lint/ok" });
  assert.deepEqual({ ...preferences.byLanguage.go }, { linter: "a.lint/ok", onType: false });
  preferences = updateLinterPreference(preferences, "go", { linter: undefined });
  assert.equal(preferences.byLanguage.go, undefined);
  // Lint while typing alone cannot be enabled without a linter.
  preferences = updateLinterPreference(preferences, "rust", { onType: true });
  assert.equal(preferences.byLanguage.rust, undefined);

  const linters = collectLinters([contribution("z.lint", [eslint])]).linters;
  assert.equal(selectedLinter(preferences, linters, "typescript")?.ref, "z.lint/eslint");
  assert.equal(selectedLinter(preferences, linters, "javascript"), undefined, "no linter runs until selected");
  assert.equal(linterRunsOn(linters[0], { linter: "z.lint/eslint", onType: false }, "type"), false);
  assert.equal(linterRunsOn(linters[0], { linter: "z.lint/eslint", onType: true }, "type"), true);
  assert.equal(linterRunsOn({ triggers: ["save"] }, { onType: true }, "type"), false);
  assert.equal(linterRunsOn({ triggers: ["save"] }, undefined, "open"), false);
});

test("linters: diagnostics are clamped defensively", () => {
  assert.deepEqual(clampDiagnostic({ startLineNumber: 3, startColumn: 5, endLineNumber: 1, endColumn: 2, severity: "bogus", message: " a\r\nb\u202e ", tags: ["unnecessary", "x", "unnecessary"] }), {
    startLineNumber: 1,
    startColumn: 2,
    endLineNumber: 3,
    endColumn: 5,
    severity: "warning",
    message: "a\nb",
    tags: ["unnecessary"],
  });
  assert.equal(clampDiagnostic({ startLineNumber: 0, startColumn: 1, endLineNumber: 1, endColumn: 1, message: "x" }), undefined);
  assert.equal(clampDiagnostic({ startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1, message: "  " }), undefined);
  const long = clampDiagnostic({ startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1, message: "x".repeat(5000), code: "c\u0000", source: "s" });
  assert.equal([...long.message].length, LINTER_LIMITS.messageMaxRunes);
  assert.equal(long.code, undefined);
  assert.equal(long.source, "s");

  const parsed = parseLintResult({ diagnostics: [{ startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 2, severity: "error", message: "m" }, 3], dropped: 2 });
  assert.equal(parsed.diagnostics.length, 1);
  assert.equal(parsed.dropped, 3);
  assert.deepEqual(parseLintResult("nope"), { diagnostics: [], dropped: 0, truncated: false });
});

test("linters: install preview lists titles, languages and lint while typing", () => {
  assert.deepEqual(linterPreviewSummary(undefined), []);
  assert.deepEqual(linterPreviewSummary({ linters: [eslint, { id: "x", languages: ["go"], triggers: ["save"] }, 3] }), [
    { title: "ESLint", languages: ["typescript", "javascript"], onType: true },
    { title: "x", languages: ["go"], onType: false },
  ]);
});

function deferred() {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

test("linters: the scheduler keeps one call in flight per plugin and only the newest job per model", async () => {
  const calls = [];
  const scheduler = createLintScheduler((request, signal) => {
    const gate = deferred();
    calls.push({ request, signal, gate });
    signal.addEventListener("abort", () => gate.resolve({ ok: false, error: { code: "cancelled", detail: "" } }));
    return gate.promise;
  });
  const ok = (id) => ({ ok: true, result: { diagnostics: [], dropped: 0, truncated: false, id } });
  const first = scheduler.schedule("p", "m1", "m1@1");
  const other = scheduler.schedule("q", "m1", "q-m1@1");
  const second = scheduler.schedule("p", "m2", "m2@1");
  const stale = scheduler.schedule("p", "m2", "m2@2");
  // One call per plugin; the two plugins run independently.
  assert.deepEqual(calls.map((call) => call.request), ["m1@1", "q-m1@1"]);
  assert.equal((await second).error.code, "cancelled", "an older pending job is replaced");
  assert.equal(scheduler.pendingCount(), 1);

  // A newer version of the running model aborts the stale in-flight call.
  const newer = scheduler.schedule("p", "m1", "m1@2");
  assert.equal(calls[0].signal.aborted, true);
  assert.equal((await first).error.code, "cancelled");
  await new Promise((resolve) => setImmediate(resolve));
  // FIFO: m2 was queued before the newer m1.
  assert.deepEqual(calls.map((call) => call.request), ["m1@1", "q-m1@1", "m2@2"]);
  calls[2].gate.resolve(ok("m2"));
  assert.equal((await stale).ok, true);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(calls[3].request, "m1@2");
  calls[3].gate.resolve(ok("m1"));
  assert.equal((await newer).ok, true);

  scheduler.cancelPlugin("q");
  assert.equal((await other).error.code, "cancelled");
  assert.equal(scheduler.pendingCount(), 0);
});

function emitter() {
  const listeners = new Set();
  return {
    on(listener) {
      listeners.add(listener);
      return { dispose: () => listeners.delete(listener) };
    },
    fire(value) {
      for (const listener of [...listeners]) listener(value);
    },
    get size() {
      return listeners.size;
    },
  };
}

function stubModel(scheme, path, language, text) {
  const content = emitter();
  const dispose = emitter();
  const model = {
    text,
    version: 1,
    language,
    disposed: false,
    uri: { scheme, path, toString: () => `${scheme}://${path}` },
    isDisposed: () => model.disposed,
    getValue: () => model.text,
    getVersionId: () => model.version,
    getLanguageId: () => model.language,
    validateRange: (range) => ({ ...range, endLineNumber: Math.min(range.endLineNumber, model.text.split("\n").length) }),
    onDidChangeContent: (listener) => content.on(listener),
    onWillDispose: (listener) => dispose.on(listener),
    edit(next) {
      model.text = next;
      model.version += 1;
      content.fire({});
    },
    dispose() {
      dispose.fire();
      model.disposed = true;
    },
  };
  return model;
}

function stubMonaco() {
  const models = [];
  const markers = new Map();
  const created = emitter();
  const language = emitter();
  const monaco = {
    MarkerSeverity: { Hint: 1, Info: 2, Warning: 4, Error: 8 },
    MarkerTag: { Unnecessary: 1, Deprecated: 2 },
    editor: {
      getModels: () => models,
      onDidCreateModel: (listener) => created.on(listener),
      onDidChangeModelLanguage: (listener) => language.on(listener),
      setModelMarkers: (model, owner, data) => {
        const key = `${model.uri.toString()}|${owner}`;
        if (data.length) markers.set(key, data);
        else markers.delete(key);
      },
    },
  };
  return { monaco, models, markers, created, language };
}

const flush = () => new Promise((resolve) => setImmediate(resolve));

test("linters: Monaco stage publishes clamped markers per linter owner and honours the triggers", async () => {
  const linters = collectLinters([contribution("z.lint", [eslint])]).linters;
  const preference = { typescript: { linter: "z.lint/eslint", onType: false } };
  const reports = [];
  const requests = [];
  let respond = () => ({ ok: true, result: { diagnostics: [{ startLineNumber: 1, startColumn: 1, endLineNumber: 9, endColumn: 4, severity: "error", message: "bad", code: "no-x", tags: ["deprecated"] }], dropped: 0, truncated: false } });
  setPluginLinterInvoker({
    lint: async (linter, modelKey, request) => {
      requests.push({ ref: linter.ref, modelKey, ...request });
      return respond(request);
    },
    cancelModel: () => undefined,
  });
  setMonacoLinterRuntime({
    selected: (language) => (preference[language]?.linter ? linters.find((linter) => linter.ref === preference[language].linter) : undefined),
    preference: (language) => preference[language],
    projectRoots: () => ["/work"],
    sourceLabel: (linter) => `${linter.title} (${linter.pluginName})`,
    report: (failure) => reports.push(failure),
  });
  setMonacoLinters(linters);
  const stub = stubMonaco();
  const ts = stubModel("file", "/work/src/a.ts", "typescript", "let x = 1;\nx;");
  const js = stubModel("file", "/work/b.js", "javascript", "x");
  const inmemory = stubModel("inmemory", "/1", "typescript", "x");
  stub.models.push(ts, js, inmemory);
  const detach = attachMonacoLinters(stub.monaco);
  const owner = linterMarkerOwner("z.lint/eslint");
  try {
    await flush();
    // Only the selected language of a file model is linted, on open.
    assert.deepEqual(requests.map((request) => [request.modelKey, request.trigger, request.fileName, request.relativePath]), [
      ["file:///work/src/a.ts", "open", "a.ts", "src/a.ts"],
    ]);
    const marker = stub.markers.get(`file:///work/src/a.ts|${owner}`)[0];
    assert.deepEqual(marker, {
      severity: 8,
      message: "bad",
      source: "ESLint (z.lint name)",
      startLineNumber: 1,
      startColumn: 1,
      endLineNumber: 2,
      endColumn: 4,
      code: "no-x",
      tags: [2],
    });

    // Typing does not lint until the user opts in.
    ts.edit("let y = 2;");
    await new Promise((resolve) => setTimeout(resolve, 350));
    assert.equal(requests.length, 1);
    preference.typescript = { linter: "z.lint/eslint", onType: true };
    refreshMonacoLinters();
    ts.edit("let z = 3;");
    await new Promise((resolve) => setTimeout(resolve, 350));
    assert.deepEqual(requests.map((request) => request.trigger), ["open", "type"]);

    // Save runs through onEditorSaved, matched by path.
    notifyEditorSaved({ path: "/work/src/a.ts", projectRoot: "/work", external: false });
    await flush();
    assert.equal(requests.at(-1).trigger, "save");

    // A result for an older model version is discarded.
    stub.markers.clear();
    let release;
    respond = () => new Promise((resolve) => {
      release = () => resolve({ ok: true, result: { diagnostics: [{ startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 2, severity: "info", message: "old" }], dropped: 0, truncated: false } });
    });
    notifyEditorSaved({ path: "/work/src/a.ts", external: false });
    await flush();
    ts.version += 1;
    release();
    await flush();
    assert.equal(stub.markers.size, 0);

    // Failures are reported once per model until a success.
    respond = () => ({ ok: false, error: { code: "timeout", detail: "" } });
    notifyEditorSaved({ path: "/work/src/a.ts", external: false });
    notifyEditorSaved({ path: "/work/src/a.ts", external: false });
    await flush();
    assert.deepEqual(reports.map((report) => report.code), ["timeout"]);

    // A new model is linted on creation; deselecting the linter clears its markers.
    respond = () => ({ ok: true, result: { diagnostics: [{ startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 2, severity: "hint", message: "h" }], dropped: 0, truncated: false } });
    const created = stubModel("file", "/work/c.ts", "typescript", "c");
    stub.models.push(created);
    stub.created.fire(created);
    await flush();
    assert.ok(stub.markers.has(`file:///work/c.ts|${owner}`));
    delete preference.typescript;
    refreshMonacoLinters();
    assert.equal(stub.markers.size, 0);

    // Plugin removal clears markers too.
    preference.typescript = { linter: "z.lint/eslint", onType: false };
    refreshMonacoLinters();
    await flush();
    assert.ok(stub.markers.size > 0);
    setMonacoLinters([]);
    assert.equal(stub.markers.size, 0);
  } finally {
    detach();
    setMonacoLinters([]);
    setMonacoLinterRuntime(undefined);
    setPluginLinterInvoker(undefined);
  }
});

test("linters: an oversized document is never sent and the notice is shown once", async () => {
  const linters = collectLinters([contribution("z.lint", [eslint])]).linters;
  const reports = [];
  let sent = 0;
  setPluginLinterInvoker({ lint: async () => ((sent += 1), { ok: true, result: { diagnostics: [], dropped: 0, truncated: false } }), cancelModel: () => undefined });
  setMonacoLinterRuntime({
    selected: () => linters[0],
    preference: () => ({ linter: "z.lint/eslint", onType: false }),
    projectRoots: () => [],
    sourceLabel: (linter) => linter.title,
    report: (failure) => reports.push(failure),
  });
  setMonacoLinters(linters);
  const stub = stubMonaco();
  const big = stubModel("file", "/big.ts", "typescript", "x".repeat(LINTER_LIMITS.payloadMaxBytes));
  stub.models.push(big);
  const detach = attachMonacoLinters(stub.monaco);
  try {
    await flush();
    notifyEditorSaved({ path: "/big.ts", external: true });
    await flush();
    assert.equal(sent, 0);
    assert.deepEqual(reports.map((report) => report.code), ["too_large"]);
  } finally {
    detach();
    setMonacoLinters([]);
    setMonacoLinterRuntime(undefined);
    setPluginLinterInvoker(undefined);
  }
});

test("linters: consumers load their runtime lazily and never apply a superseded snapshot", async () => {
  const log = [];
  const errors = [];
  let loads = 0;
  let release;
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  const apply = lazyContributionApply(async () => {
    loads += 1;
    await gate;
    return (contributions) => {
      log.push(`apply ${contributions.length}`);
      return () => log.push(`cleanup ${contributions.length}`);
    };
  }, (message) => errors.push(message));
  assert.equal(apply([]), undefined, "an empty snapshot loads nothing");
  assert.equal(loads, 0);
  const first = apply([contribution("a", [eslint])]);
  first();
  const second = apply([contribution("a", [eslint]), contribution("b", [eslint])]);
  release();
  await flush();
  assert.deepEqual(log, ["apply 2"], "the snapshot cleaned up before loading is skipped");
  second();
  assert.deepEqual(log, ["apply 2", "cleanup 2"]);
  const failing = lazyContributionApply(async () => {
    throw new Error("offline");
  }, (message) => errors.push(message));
  failing([contribution("a", [eslint])]);
  await flush();
  assert.deepEqual(errors, ["load consumer: offline"]);
});
