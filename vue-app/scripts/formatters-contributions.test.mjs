import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  FORMATTER_LIMITS,
  PLUGIN_FORMATTERS_STORAGE_KEY,
  chooseFormatterProvider,
  collectFormatters,
  formatterPreviewSummary,
  minimalEdit,
  normalizeFormatterPreferences,
  parseFormatterRef,
  selectedFormatter,
  updateFormatterPreference,
  utf8ByteLength,
} from "../src/domain/plugins/formatters.ts";
import { runEditorSaveParticipants } from "../src/services/editor-save-participants.ts";
import {
  FORMAT_DOCUMENT_WITH_ACTION_ID,
  attachMonacoFormatters,
  monacoFormatterProviderRef,
  setMonacoFormatterRuntime,
  setMonacoFormatters,
  setPluginFormatterInvoker,
} from "../src/services/monaco-formatters.ts";

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

const prettier = { id: "prettier", title: "Prettier", languages: ["typescript", "json"], action: "format", range: true };

test("formatters: entries are re-validated; only active process plugins contribute", () => {
  const result = collectFormatters([
    contribution("z.fmt", [prettier]),
    contribution("a.fmt", [
      { id: "ok", title: "OK", languages: ["go"], action: "fmt.go", range: false },
      { id: "Bad", title: "x", languages: ["go"], action: "a" },
      { id: "dup", title: "Dup", languages: ["go", "go"], action: "a" },
      { id: "ctl", title: "bad\u202etitle", languages: ["go"], action: "a" },
      { id: "ok", title: "Again", languages: ["go"], action: "a" },
    ]),
    contribution("decl.fmt", [prettier], { runtime: "declarative" }),
    contribution("off.fmt", [prettier], { activeInWorkspace: false }),
  ]);
  assert.deepEqual(result.formatters.map((formatter) => formatter.ref), ["a.fmt/ok", "z.fmt/prettier"]);
  assert.equal(result.formatters[1].range, true);
  assert.equal(result.issues.length, 4);
  assert.ok(result.issues.every((issue) => issue.stage === "frontend" && issue.pluginId === "a.fmt"));
});

test("formatters: preferences are machine-local and normalized", () => {
  assert.ok(!PLUGIN_FORMATTERS_STORAGE_KEY.startsWith("app_"), "never synced or exported");
  const byLanguage = JSON.parse(
    '{"typescript":{"formatter":"z.fmt/prettier","formatOnSave":true},"go":{"formatOnSave":true},"Bad Lang":{"formatter":"z.fmt/prettier"},"json":{"formatter":"nope"},"__proto__":{"formatter":"z.fmt/prettier"}}',
  );
  const normalized = normalizeFormatterPreferences({ version: 1, byLanguage });
  assert.deepEqual(Object.keys(normalized.byLanguage), ["typescript"]);
  assert.equal(Object.getPrototypeOf(normalized.byLanguage), null);
  assert.deepEqual(normalizeFormatterPreferences({ version: 2, byLanguage }).byLanguage, Object.create(null));

  const many = { version: 1, byLanguage: {} };
  for (let index = 0; index < 300; index += 1) many.byLanguage[`l${index}`] = { formatter: "p/f" };
  assert.equal(Object.keys(normalizeFormatterPreferences(many).byLanguage).length, FORMATTER_LIMITS.preferencesMax);

  let preferences = updateFormatterPreference(normalized, "go", { formatter: "a.fmt/ok" });
  assert.deepEqual({ ...preferences.byLanguage.go }, { formatter: "a.fmt/ok", formatOnSave: false });
  preferences = updateFormatterPreference(preferences, "go", { formatOnSave: true });
  assert.equal(preferences.byLanguage.go.formatOnSave, true);
  // Back to built-in: the entry (and format on save) goes away.
  preferences = updateFormatterPreference(preferences, "go", { formatter: undefined });
  assert.equal(preferences.byLanguage.go, undefined);
  // Format on save alone cannot be enabled for the built-in formatter.
  preferences = updateFormatterPreference(preferences, "rust", { formatOnSave: true });
  assert.equal(preferences.byLanguage.rust, undefined);

  const formatters = collectFormatters([contribution("z.fmt", [prettier])]).formatters;
  assert.equal(selectedFormatter(preferences, formatters, "typescript")?.ref, "z.fmt/prettier");
  assert.equal(selectedFormatter(preferences, formatters, "go"), undefined);
  assert.deepEqual(parseFormatterRef("z.fmt/prettier"), { pluginId: "z.fmt", formatterId: "prettier" });
  assert.equal(parseFormatterRef("prettier"), undefined);
});

test("formatters: the selector never picks an unselected plugin formatter", () => {
  const refOf = (provider) => provider.ref;
  const builtin = { ref: undefined, name: "builtin" };
  const lsp = { ref: undefined, name: "lsp" };
  const plugin = { ref: "p/f", name: "plugin" };
  assert.equal(chooseFormatterProvider([plugin, builtin, lsp], refOf, undefined), builtin);
  assert.equal(chooseFormatterProvider([builtin, plugin], refOf, "p/f"), plugin);
  assert.equal(chooseFormatterProvider([builtin, plugin], refOf, "p/other"), builtin);
  assert.equal(chooseFormatterProvider([plugin], refOf, undefined), undefined);
  assert.equal(chooseFormatterProvider([], refOf, "p/f"), undefined);
});

test("formatters: minimal edit and byte length", () => {
  assert.equal(minimalEdit("same", "same"), null);
  assert.deepEqual(minimalEdit("a  =  1;\n", "a = 1;\n"), { start: 2, end: 5, text: "=" });
  assert.deepEqual(minimalEdit("abc", "abXc"), { start: 2, end: 2, text: "X" });
  assert.deepEqual(minimalEdit("x", ""), { start: 0, end: 1, text: "" });
  // Surrogate pairs sharing a high surrogate are never split.
  const edit = minimalEdit("a😀b", "a😁b");
  assert.deepEqual(edit, { start: 1, end: 3, text: "😁" });
  assert.equal(utf8ByteLength("aé😀"), 1 + 2 + 4);
});

test("formatters: install preview lists titles and languages", () => {
  assert.deepEqual(formatterPreviewSummary(undefined), []);
  assert.deepEqual(formatterPreviewSummary({ formatters: [prettier, { id: "x", languages: ["go"] }, 3] }), [
    { title: "Prettier", languages: ["typescript", "json"] },
    { title: "x", languages: ["go"] },
  ]);
});

class Range {
  constructor(startLineNumber, startColumn, endLineNumber, endColumn) {
    Object.assign(this, { startLineNumber, startColumn, endLineNumber, endColumn });
  }
}

function positionAt(text, offset) {
  const before = text.slice(0, offset).split("\n");
  return { lineNumber: before.length, column: before[before.length - 1].length + 1 };
}

function offsetAt(text, lineNumber, column) {
  const lines = text.split("\n");
  let offset = 0;
  for (let index = 0; index < lineNumber - 1; index += 1) offset += lines[index].length + 1;
  return offset + column - 1;
}

function stubModel(scheme, path, language, text) {
  const model = {
    text,
    version: 1,
    stack: 0,
    uri: { scheme, path, toString: () => `${scheme}://${path}` },
    isDisposed: () => false,
    getValue: () => model.text,
    getVersionId: () => model.version,
    getEOL: () => "\n",
    getLanguageId: () => language,
    getOptions: () => ({ tabSize: 2, insertSpaces: true }),
    getPositionAt: (offset) => positionAt(model.text, offset),
    pushStackElement: () => {
      model.stack += 1;
    },
    pushEditOperations: (_selections, edits) => {
      for (const edit of edits) {
        const start = offsetAt(model.text, edit.range.startLineNumber, edit.range.startColumn);
        const end = offsetAt(model.text, edit.range.endLineNumber, edit.range.endColumn);
        model.text = model.text.slice(0, start) + edit.text + model.text.slice(end);
      }
      model.version += 1;
      return null;
    },
  };
  return model;
}

function stubMonaco() {
  const providers = [];
  const rangeProviders = [];
  const actions = [];
  const selectors = [];
  const models = [];
  const monaco = {
    Range,
    editor: {
      getModels: () => models,
      addEditorAction: (descriptor) => {
        actions.push(descriptor);
        return { dispose: () => actions.splice(actions.indexOf(descriptor), 1) };
      },
    },
    languages: {
      registerDocumentFormattingEditProvider: (language, provider) => {
        const record = { language, provider };
        providers.push(record);
        return { dispose: () => providers.splice(providers.indexOf(record), 1) };
      },
      registerDocumentRangeFormattingEditProvider: (language, provider) => {
        const record = { language, provider };
        rangeProviders.push(record);
        return { dispose: () => rangeProviders.splice(rangeProviders.indexOf(record), 1) };
      },
    },
    mincodeFormattingConflicts: {
      setFormatterSelector: (selector) => {
        selectors.unshift(selector);
        return { dispose: () => selectors.splice(selectors.indexOf(selector), 1) };
      },
    },
  };
  return { monaco, providers, rangeProviders, actions, selectors, models };
}

function testRuntime(overrides = {}) {
  const reports = [];
  const picks = [];
  const remembered = [];
  const runtime = {
    preferredRef: () => undefined,
    formatOnSave: () => undefined,
    projectRoots: () => ["/work"],
    displayName: (formatter) => `${formatter.title} (${formatter.pluginId})`,
    formatWithLabel: () => "Format Document With…",
    pick: async (options) => {
      picks.push(options);
      return undefined;
    },
    remember: (language, ref) => remembered.push([language, ref]),
    report: (failure) => reports.push(failure),
    ...overrides,
  };
  return { runtime, reports, picks, remembered };
}

const token = { isCancellationRequested: false, onCancellationRequested: () => ({ dispose() {} }) };

test("formatters: Monaco stage registers tagged providers, the action and a deterministic selector", async () => {
  const formatters = collectFormatters([contribution("z.fmt", [prettier])]).formatters;
  const requests = [];
  setPluginFormatterInvoker(async (formatter, request) => {
    requests.push({ ref: formatter.ref, ...request });
    return { ok: true, text: request.text.replace(/\s*=\s*/g, " = ") };
  });
  const { runtime, picks, remembered } = testRuntime({
    pick: async (options) => {
      picks.push(options);
      return { kind: "plugin", ref: "z.fmt/prettier", remember: true };
    },
  });
  const stub = stubMonaco();
  setMonacoFormatterRuntime(runtime);
  setMonacoFormatters(formatters);
  const detach = attachMonacoFormatters(stub.monaco);
  try {
    assert.deepEqual(stub.providers.map((item) => item.language), ["typescript", "json"]);
    assert.deepEqual(stub.rangeProviders.map((item) => item.language), ["typescript", "json"]);
    assert.equal(stub.actions.length, 1);
    assert.equal(stub.actions[0].id, FORMAT_DOCUMENT_WITH_ACTION_ID);
    assert.equal(stub.actions[0].precondition, "!editorReadonly && (editorLangId == 'json' || editorLangId == 'typescript')");
    const plugin = stub.providers[0].provider;
    assert.equal(monacoFormatterProviderRef(plugin), "z.fmt/prettier");
    // Range providers reach the selector as display-name-only wrappers.
    assert.equal(monacoFormatterProviderRef({ displayName: plugin.displayName }), "z.fmt/prettier");

    const builtin = { displayName: "TypeScript" };
    const selector = stub.selectors[0];
    const model = stubModel("file", "/work/src/a.ts", "typescript", "const a  =  1;\n");
    assert.equal(await selector([plugin, builtin], model, 1, 1), builtin, "built-in stays the default");
    assert.equal(await selector([plugin], model, 2, 1), undefined, "silent formatting never runs an unselected plugin");
    assert.equal(picks.length, 0);
    assert.equal(await selector([plugin], model, 1, 1), plugin, "explicit formatting asks once");
    assert.equal(picks.length, 1);
    assert.deepEqual(remembered, [["typescript", "z.fmt/prettier"]]);

    const edits = await plugin.provideDocumentFormattingEdits(model, { tabSize: 2, insertSpaces: true }, token);
    assert.equal(edits.length, 1);
    assert.equal(edits[0].text, "=");
    assert.deepEqual({ ...edits[0].range }, { startLineNumber: 1, startColumn: 9, endLineNumber: 1, endColumn: 12 });
    assert.equal(requests[0].mode, "explicit");
    assert.equal(requests[0].fileName, "a.ts");
    assert.equal(requests[0].relativePath, "src/a.ts");
    assert.equal(requests[0].eol, "\n");

    setMonacoFormatters([]);
    assert.equal(stub.providers.length, 0);
    assert.equal(stub.actions.length, 0);
  } finally {
    detach();
    assert.equal(stub.selectors.length, 0);
    setMonacoFormatters([]);
    setMonacoFormatterRuntime(undefined);
    setPluginFormatterInvoker();
  }
});

test("formatters: the preferred plugin formatter wins the selector", async () => {
  const formatters = collectFormatters([contribution("z.fmt", [prettier])]).formatters;
  const { runtime } = testRuntime({ preferredRef: (language) => (language === "typescript" ? "z.fmt/prettier" : undefined) });
  const stub = stubMonaco();
  setMonacoFormatterRuntime(runtime);
  setMonacoFormatters(formatters);
  const detach = attachMonacoFormatters(stub.monaco);
  try {
    const plugin = stub.providers[0].provider;
    const builtin = { displayName: "TypeScript" };
    assert.equal(await stub.selectors[0]([builtin, plugin], stubModel("file", "/w/a.ts", "typescript", ""), 2, 1), plugin);
    assert.equal(await stub.selectors[0]([builtin, plugin], stubModel("file", "/w/a.json", "json", ""), 2, 1), builtin);
  } finally {
    detach();
    setMonacoFormatters([]);
    setMonacoFormatterRuntime(undefined);
  }
});

test("formatters: format on save is opt-in and applies only to an unchanged document", async () => {
  const formatters = collectFormatters([contribution("z.fmt", [prettier])]).formatters;
  const calls = [];
  let mutate;
  setPluginFormatterInvoker(async (_formatter, request) => {
    calls.push(request.mode);
    mutate?.();
    return { ok: true, text: request.text.replace("  ", " ") };
  });
  let optIn = false;
  const { runtime, reports } = testRuntime({ formatOnSave: () => (optIn ? formatters[0] : undefined) });
  const stub = stubMonaco();
  setMonacoFormatterRuntime(runtime);
  setMonacoFormatters(formatters);
  const detach = attachMonacoFormatters(stub.monaco);
  try {
    const model = stubModel("file", "/work/a.ts", "typescript", "let  x = 1;\n");
    stub.models.push(model);
    await runEditorSaveParticipants({ path: "/work/a.ts", projectRoot: "/work", external: false });
    assert.deepEqual(calls, [], "no plugin runs on save without the opt-in");

    optIn = true;
    await runEditorSaveParticipants({ path: "/work/a.ts", projectRoot: "/work", external: false });
    assert.deepEqual(calls, ["save"]);
    assert.equal(model.text, "let x = 1;\n");
    assert.equal(model.stack, 2, "one undo step");

    model.text = "let  y = 2;\n";
    mutate = () => {
      model.version += 1;
    };
    await runEditorSaveParticipants({ path: "/work/a.ts", projectRoot: "/work", external: false });
    assert.equal(model.text, "let  y = 2;\n", "a document changed during formatting is left alone");
    assert.equal(reports.at(-1)?.code, "changed");
    mutate = undefined;

    const external = stubModel("app", "/Users/me/notes.ts", "typescript", "a  b\n");
    stub.models.push(external);
    await runEditorSaveParticipants({ path: "/Users/me/notes.ts", external: true });
    assert.equal(external.text, "a b\n");

    setPluginFormatterInvoker(async () => ({ ok: false, error: { code: "timeout", detail: "" } }));
    model.text = "let  z;\n";
    await runEditorSaveParticipants({ path: "/work/a.ts", projectRoot: "/work", external: false });
    assert.equal(model.text, "let  z;\n");
    assert.equal(reports.at(-1)?.code, "timeout");
  } finally {
    detach();
    setMonacoFormatters([]);
    setMonacoFormatterRuntime(undefined);
    setPluginFormatterInvoker();
  }
  // Detached: the save participant is gone.
  calls.length = 0;
  await runEditorSaveParticipants({ path: "/work/a.ts", projectRoot: "/work", external: false });
  assert.deepEqual(calls, []);
});

test("formatters: oversized documents are not sent", async () => {
  const formatters = collectFormatters([contribution("z.fmt", [prettier])]).formatters;
  let called = false;
  setPluginFormatterInvoker(async () => {
    called = true;
    return { ok: true };
  });
  const { runtime, reports } = testRuntime();
  const stub = stubMonaco();
  setMonacoFormatterRuntime(runtime);
  setMonacoFormatters(formatters);
  const detach = attachMonacoFormatters(stub.monaco);
  try {
    const model = stubModel("file", "/work/big.json", "json", "x".repeat(FORMATTER_LIMITS.payloadMaxBytes));
    const edits = await stub.providers[1].provider.provideDocumentFormattingEdits(model, { tabSize: 2, insertSpaces: true }, token);
    assert.deepEqual(edits, []);
    assert.equal(called, false);
    assert.equal(reports[0].code, "too_large");
  } finally {
    detach();
    setMonacoFormatters([]);
    setMonacoFormatterRuntime(undefined);
    setPluginFormatterInvoker();
  }
});

test("formatters: consumers load Monaco lazily (type-only imports)", async () => {
  for (const file of [
    "src/services/contributions/formatters.ts",
    "src/services/contributions/formatters-runtime.ts",
    "src/services/monaco-formatters.ts",
    "src/services/plugin-formatter-service.ts",
    "src/stores/plugin-formatters.ts",
    "src/domain/plugins/formatters.ts",
  ]) {
    const source = await readFile(fileURLToPath(new URL(`../${file}`, import.meta.url)), "utf8");
    const eager = [...source.matchAll(/^\s*import\s+(?!type\s)[^;]*?from\s+["']([^"']+)["']/gm)]
      .map((match) => match[1])
      .filter((specifier) => /monaco-runtime|monaco-editor|monaco-keymap|project-file-uri|FormatterPickerDialog/.test(specifier));
    assert.deepEqual(eager, [], file);
    assert.doesNotMatch(source, /import\(\s*["']@\/services\/monaco-runtime/, file);
  }
});

async function runSaveFormat(options = {}) {
  const { formatProjectModelOnSave } = await import("../src/services/monaco-formatters.ts");
  const stub = stubMonaco();
  const model = stubModel("file", "/work/a.json", "json", '{"a":1}');
  model.onWillDispose = () => ({ dispose() {} });
  model.onDidChangeContent = () => ({ dispose() {} });
  const builtin = { displayName: "JSON" };
  const used = [];
  stub.monaco.mincodeSaveFormatting = {
    async run(target, choose, signal) {
      const selected = choose([...(stub.providers.map((item) => item.provider)), builtin]);
      if (selected && !signal.aborted) used.push(selected);
    },
  };
  const formatters = collectFormatters([contribution("z.fmt", [prettier])]).formatters;
  const { runtime, picks } = testRuntime(options.runtime);
  setMonacoFormatterRuntime(runtime);
  setMonacoFormatters(formatters);
  const detach = attachMonacoFormatters(stub.monaco);
  const invoked = [];
  setPluginFormatterInvoker(async (_, request, signal) => {
    invoked.push(request);
    if (options.invoke) return options.invoke(model, signal);
    return { ok: true, text: '{"a": 1}' };
  });
  try {
    await formatProjectModelOnSave(stub.monaco, model, { path: "/work/a.json", external: false,
      formatOnSave: options.enabled ?? true, signal: options.signal, isCurrent: options.isCurrent });
    return { used, invoked, picks, text: model.text, builtin };
  } finally { detach(); setMonacoFormatterRuntime(undefined); setMonacoFormatters([]); setPluginFormatterInvoker(); }
}

test("save format: builtin is selected silently; disabled setting never formats", async () => {
  const result = await runSaveFormat();
  assert.deepEqual(result.used, [result.builtin]);
  assert.equal(result.picks.length, 0);
  assert.equal(result.invoked.length, 0);
  const disabled = await runSaveFormat({ enabled: false });
  assert.equal(disabled.used.length, 0);
});

test("save format: selected plugin uses save mode exactly once; legacy opt-in survives", async () => {
  const formatter = collectFormatters([contribution("z.fmt", [prettier])]).formatters[0];
  for (const enabled of [true, false]) {
    const result = await runSaveFormat({ enabled, runtime: {
      preferredRef: () => formatter.ref, formatOnSave: () => formatter,
    } });
    assert.equal(result.invoked.length, 1);
    assert.equal(result.invoked[0].mode, "save");
    assert.equal(result.used.length, 0);
    assert.equal(result.text, '{"a": 1}');
  }
});

test("save format: unavailable plugin, aborted scope, and changed model never apply stale results", async () => {
  await assert.rejects(() => runSaveFormat({ runtime: { savePreferredRef: () => "gone/fmt" } }), /unavailable/);
  const abort = new AbortController(); abort.abort();
  assert.equal((await runSaveFormat({ signal: abort.signal })).used.length, 0);
  const changed = await runSaveFormat({ runtime: { preferredRef: () => "z.fmt/prettier" },
    invoke: async (model) => { model.version++; model.text = "newer"; return { ok: true, text: "old" }; } });
  assert.equal(changed.text, "newer");
  assert.equal((await runSaveFormat({ isCurrent: () => false })).used.length, 0);
});

test("save format: coordinator's formatting seam suppresses the legacy plugin participant", async () => {
  const { runtime } = testRuntime();
  setMonacoFormatterRuntime(runtime);
  const stub = stubMonaco(); const detach = attachMonacoFormatters(stub.monaco);
  let calls = 0;
  try {
    await runEditorSaveParticipants({ path: "/work/a.json", external: false, formatterHandled: true,
      format: async () => { calls++; } });
    assert.equal(calls, 1);
  } finally { detach(); setMonacoFormatterRuntime(undefined); }
});
