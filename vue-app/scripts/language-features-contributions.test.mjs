import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test, { mock } from "node:test";
import {
  codeActionDiagnostics,
  collectLanguageFeatures,
  INLAY_HINT_KINDS,
  LANGUAGE_FEATURE_KINDS,
  PASSIVE_LANGUAGE_FEATURES,
  SYMBOL_KINDS,
  escapeHoverMarkdown,
  languageFeaturePreviewSummary,
  languageFeatureTitle,
  languageFeatureWindow,
  LANGUAGE_FEATURE_LIMITS,
  parseCompletionResponse,
  parseHoverResponse,
  validateCodeActionResult,
  validateLanguageFeatureEntry,
} from "../src/domain/plugins/language-features.ts";

const contractSpec = JSON.parse(readFileSync(new URL("../src/domain/plugins/contribution-points.json", import.meta.url), "utf8")).points.languageFeatures;
import {
  attachMonacoLanguageFeatures,
  languageFeatureSuspensions,
  PLUGIN_CODE_LENS_COMMAND,
  setLanguageFeatureCommandRunner,
  setLanguageFeatureInvoker,
  setMonacoLanguageFeatureRuntime,
  setMonacoLanguageFeatures,
} from "../src/services/monaco-language-features.ts";
import { codeLensCommandAccepts } from "../src/domain/plugins/language-feature-results.ts";
import { createCodeLensCommandRunner } from "../src/domain/plugins/code-lens-commands.ts";
import { CODE_LENS_TARGETS_MAX } from "../src/services/monaco-language-features.ts";

function contribution(pluginId, entries) {
  return {
    pluginId,
    pluginName: pluginId,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: "f", entries, issues: [] },
  };
}

const entry = {
  id: "words",
  title: "Words",
  titleLocalized: { zh: "单词" },
  languages: ["markdown", "plaintext"],
  features: ["hover", "completion"],
  triggerCharacters: ["@"],
  action: "lang.words",
};

test("languageFeatures: re-validates entries", () => {
  assert.equal(validateLanguageFeatureEntry(entry).ok, true);
  assert.equal(validateLanguageFeatureEntry({ ...entry, features: ["hover"] }).ok, false, "trigger characters need completion");
  assert.equal(validateLanguageFeatureEntry({ ...entry, features: ["hover"], triggerCharacters: [] }).ok, true);
  assert.equal(validateLanguageFeatureEntry({ ...entry, features: ["rename"] }).ok, false);
  assert.equal(validateLanguageFeatureEntry({ ...entry, languages: [] }).ok, false);
  assert.equal(validateLanguageFeatureEntry({ ...entry, languages: ["Markdown"] }).ok, false);
  assert.equal(validateLanguageFeatureEntry({ ...entry, triggerCharacters: ["ab"] }).ok, false);
  assert.equal(validateLanguageFeatureEntry({ ...entry, triggerCharacters: ["@", "@"] }).ok, false);
  const { providers, issues } = collectLanguageFeatures([contribution("acme.lang", [entry, entry])]);
  assert.equal(providers.length, 1);
  assert.equal(issues.length, 1);
  assert.equal(languageFeatureTitle(providers[0], ["zh-CN", "zh"]), "单词");
});

test("languageFeatures: frontend mirrors the contract", () => {
  assert.deepEqual([...LANGUAGE_FEATURE_KINDS], contractSpec.lists.features);
  assert.deepEqual([...SYMBOL_KINDS], contractSpec.lists.symbolKinds);
  assert.deepEqual([...INLAY_HINT_KINDS], contractSpec.lists.inlayHintKinds);
  assert.deepEqual([...PASSIVE_LANGUAGE_FEATURES].sort(), ["codeLens", "documentSymbol", "inlayHints"]);
  for (const name of [
    "definitionLocationsMax", "referencesLocationsMax", "referencesTimeoutMs", "codeLensesMax", "codeLensTitleMaxRunes",
    "codeLensArgsMaxBytes", "codeLensArgsMaxProperties", "inlayHintsMax", "inlayHintLabelMaxRunes", "documentSymbolsMax",
    "documentSymbolDepthMax", "symbolNameMaxRunes", "symbolDetailMaxRunes", "locationPathMaxBytes", "columnMax",
    "passiveConcurrentPerPlugin", "passiveDebounceMs", "passiveFailuresMax", "passiveSuspendMs",
  ]) {
    assert.equal(typeof contractSpec.limits[name], "number", `contract limit ${name}`);
    assert.equal(LANGUAGE_FEATURE_LIMITS[name], contractSpec.limits[name], `LANGUAGE_FEATURE_LIMITS.${name}`);
  }
  for (const [name, value] of Object.entries(LANGUAGE_FEATURE_LIMITS)) {
    if (name in contractSpec.limits) assert.equal(value, contractSpec.limits[name], `LANGUAGE_FEATURE_LIMITS.${name}`);
  }
  for (const feature of ["definition", "references", "codeLens", "inlayHints", "documentSymbol"]) {
    assert.equal(validateLanguageFeatureEntry({ ...entry, features: [feature], triggerCharacters: [] }).ok, true, feature);
  }
  assert.equal(validateLanguageFeatureEntry({ ...entry, features: ["definition"] }).ok, false, "trigger characters need completion");
});

test("languageFeatures: request window is the whole document or whole lines around the cursor", () => {
  assert.deepEqual(languageFeatureWindow(["a", "b", "c"], 2), { text: "a\nb\nc", startLine: 1 });
  assert.equal(languageFeatureWindow(["a"], 0), undefined);
  assert.equal(languageFeatureWindow(["a"], 2), undefined);
  const lines = Array.from({ length: 20 }, (_, i) => `${i}`.padStart(4, "x"));
  const window = languageFeatureWindow(lines, 10, 4 * 7 + 6);
  assert.equal(window.text.split("\n").length, 7);
  assert.ok(window.startLine <= 10 && window.startLine + 6 >= 10);
  assert.ok(10 - window.startLine >= window.startLine + 6 - 10, "prefers lines above the cursor");
  assert.ok(Buffer.byteLength(window.text) <= 34);
  assert.equal(languageFeatureWindow(["x".repeat(10), "y"], 1, 5), undefined);
});

test("languageFeatures: hover markdown is escaped and responses are re-validated", () => {
  assert.equal(escapeHoverMarkdown("[x](javascript:1) <b>*"), "\\[x\\]\\(javascript:1\\) \\<b\\>\\*");
  assert.equal(escapeHoverMarkdown("a\nb"), "a  \nb");
  const hover = parseHoverResponse({ contents: ["ok", 3, "  ", "x", "extra"], dropped: 1 });
  assert.deepEqual(hover.contents, ["ok", "x"]);
  assert.equal(hover.dropped, 3);
  assert.equal(hover.truncated, true);
  assert.deepEqual(parseHoverResponse({ contents: ["bad\u202e"] }).contents, []);

  const completion = parseCompletionResponse({
    items: [
      { label: "alpha", kind: "function", insertText: "alpha()", detail: "fn", documentation: "doc\r\nmore" },
      { label: "beta", kind: "lambda" },
      { label: "" },
      { label: "gamma", detail: "a\nb" },
    ],
    incomplete: true,
  });
  assert.deepEqual(completion.items, [{ label: "alpha", insertText: "alpha()", detail: "fn", documentation: "doc\nmore", kind: "function" }]);
  assert.equal(completion.dropped, 3);
  assert.equal(completion.incomplete, true);
  const many = parseCompletionResponse({ items: Array.from({ length: LANGUAGE_FEATURE_LIMITS.completionItemsMax + 1 }, (_, i) => ({ label: `i${i}` })) });
  assert.equal(many.items.length, LANGUAGE_FEATURE_LIMITS.completionItemsMax);
  assert.equal(many.truncated, true);
});

test("languageFeatures: install preview lists titles", () => {
  assert.deepEqual(languageFeaturePreviewSummary({ languageFeatures: [entry, { id: "raw" }] }), ["Words", "raw"]);
});

function stubMonaco() {
  const hovers = [];
  const completions = [];
  const monaco = {
    languages: {
      CompletionItemKind: { Text: 18, Function: 1, TypeParameter: 24 },
      CompletionTriggerKind: { Invoke: 0, TriggerCharacter: 1, TriggerForIncompleteCompletions: 2 },
      registerHoverProvider(selector, provider) {
        const record = { selector, provider, disposed: false };
        hovers.push(record);
        return { dispose: () => { record.disposed = true; } };
      },
      registerCompletionItemProvider(selector, provider) {
        const record = { selector, provider, disposed: false };
        completions.push(record);
        return { dispose: () => { record.disposed = true; } };
      },
    },
    editor: { registerCommand: () => ({ dispose() {} }) },
  };
  return { monaco, hovers, completions };
}

function stubModel(path, { scheme = "file", lines = ["hello wor"], version = 1 } = {}) {
  const model = {
    uri: { scheme, path },
    version,
    getLinesContent: () => lines,
    getLanguageId: () => "markdown",
    getVersionId: () => model.version,
    isDisposed: () => false,
    getWordUntilPosition: () => ({ word: "wor", startColumn: 7, endColumn: 10 }),
  };
  return model;
}

const token = { isCancellationRequested: false, onCancellationRequested: () => ({ dispose() {} }) };

test("languageFeatures: Monaco providers call the plugin and map results safely", async () => {
  const { providers } = collectLanguageFeatures([contribution("acme.lang", [entry, { ...entry, id: "hover-only", features: ["hover"], triggerCharacters: undefined }])]);
  const calls = [];
  let bumpVersion;
  setLanguageFeatureInvoker({
    async hover(provider, request) {
      calls.push(["hover", provider.id, request]);
      return { ok: true, response: { contents: ["**bold** <img>"], dropped: 0, truncated: false } };
    },
    async completion(provider, request) {
      calls.push(["completion", provider.id, request]);
      bumpVersion?.();
      return {
        ok: true,
        response: { items: [{ label: "world", kind: "function" }, { label: "w", insertText: "with", kind: "type" }, { label: "x" }], incomplete: true, dropped: 0, truncated: false },
      };
    },
  });
  setMonacoLanguageFeatureRuntime({ locale: () => "zh", projectRoots: () => ["/repo"] });
  setMonacoLanguageFeatures(providers);
  const stub = stubMonaco();
  const detach = attachMonacoLanguageFeatures(stub.monaco);
  try {
    assert.equal(stub.hovers.length, 2);
    assert.equal(stub.completions.length, 1);
    assert.deepEqual(stub.hovers[0].selector, ["markdown", "plaintext"]);
    assert.deepEqual(stub.completions[0].provider.triggerCharacters, ["@"]);

    const model = stubModel("/repo/docs/a.md");
    const position = { lineNumber: 1, column: 10 };
    const hover = await stub.hovers[0].provider.provideHover(model, position, token);
    assert.deepEqual(hover.contents, [{ value: "\\*\\*bold\\*\\* \\<img\\>", isTrusted: false, supportHtml: false }]);
    assert.deepEqual(calls[0][2], {
      languageId: "markdown",
      fileName: "a.md",
      relativePath: "docs/a.md",
      text: "hello wor",
      startLine: 1,
      position: { line: 1, column: 10 },
      locale: "zh",
    });

    const list = await stub.completions[0].provider.provideCompletionItems(model, position, { triggerKind: 1, triggerCharacter: "@" }, token);
    assert.equal(list.incomplete, true);
    assert.deepEqual(list.suggestions.map((s) => [s.label, s.kind, s.insertText]), [["world", 1, "world"], ["w", 24, "with"], ["x", 18, "x"]]);
    assert.deepEqual(list.suggestions[0].range, { startLineNumber: 1, startColumn: 7, endLineNumber: 1, endColumn: 10 });
    assert.equal(calls[1][2].triggerCharacter, "@");

    await stub.completions[0].provider.provideCompletionItems(model, position, { triggerKind: 1, triggerCharacter: "." }, token);
    assert.equal("triggerCharacter" in calls[2][2], false, "undeclared trigger characters are not forwarded");

    bumpVersion = () => { model.version += 1; };
    const stale = await stub.completions[0].provider.provideCompletionItems(model, position, { triggerKind: 0 }, token);
    assert.deepEqual(stale.suggestions, [], "results for an edited model are dropped");
    bumpVersion = undefined;

    const outside = await stub.hovers[0].provider.provideHover(stubModel("/elsewhere/b.md"), position, token);
    assert.ok(outside);
    assert.equal("relativePath" in calls.at(-1)[2], false);
    assert.equal(calls.at(-1)[2].fileName, "b.md");

    const before = calls.length;
    assert.equal(await stub.hovers[0].provider.provideHover(stubModel("x", { scheme: "inmemory" }), position, token), null);
    assert.equal(calls.length, before, "non-file models are not sent");

    setMonacoLanguageFeatures([]);
    assert.ok([...stub.hovers, ...stub.completions].every((record) => record.disposed));
  } finally {
    detach();
    setMonacoLanguageFeatures([]);
    setMonacoLanguageFeatureRuntime(undefined);
    setLanguageFeatureInvoker();
  }
});

test("languageFeatures: failed or throwing calls yield no result", async () => {
  const { providers } = collectLanguageFeatures([contribution("acme.lang", [entry])]);
  setLanguageFeatureInvoker({
    async hover() {
      return { ok: false, error: "timeout" };
    },
    async completion() {
      throw new Error("boom");
    },
  });
  setMonacoLanguageFeatureRuntime({ locale: () => "en", projectRoots: () => [] });
  setMonacoLanguageFeatures(providers);
  const stub = stubMonaco();
  const detach = attachMonacoLanguageFeatures(stub.monaco);
  try {
    const model = stubModel("/a.md");
    const position = { lineNumber: 1, column: 3 };
    assert.equal(await stub.hovers[0].provider.provideHover(model, position, token), null);
    assert.deepEqual(await stub.completions[0].provider.provideCompletionItems(model, position, { triggerKind: 0 }, token), { suggestions: [] });
  } finally {
    detach();
    setMonacoLanguageFeatures([]);
    setMonacoLanguageFeatureRuntime(undefined);
    setLanguageFeatureInvoker();
  }
});

// ---- New kinds (definition, references, codeLens, inlayHints, documentSymbol) ----

const allKinds = {
  id: "tasks",
  title: "Tasks",
  titleLocalized: { zh: "任务" },
  languages: ["typescript", "sql"],
  features: ["hover", "completion", "definition", "references", "codeLens", "inlayHints", "documentSymbol"],
  action: "lang.tasks",
};

function stubMonacoAll() {
  const records = { hover: [], completion: [], definition: [], references: [], codeLens: [], inlayHints: [], documentSymbol: [] };
  const commands = [];
  const register = (bucket) => (selector, provider) => {
    const record = { selector, provider, disposed: false };
    records[bucket].push(record);
    return { dispose: () => { record.disposed = true; } };
  };
  const monaco = {
    languages: {
      CompletionItemKind: { Text: 18, Function: 1, TypeParameter: 24 },
      CompletionTriggerKind: { Invoke: 0, TriggerCharacter: 1, TriggerForIncompleteCompletions: 2 },
      registerHoverProvider: register("hover"),
      registerCompletionItemProvider: register("completion"),
      registerDefinitionProvider: register("definition"),
      registerReferenceProvider: register("references"),
      registerCodeLensProvider: register("codeLens"),
      registerInlayHintsProvider: register("inlayHints"),
      registerDocumentSymbolProvider: register("documentSymbol"),
    },
    editor: {
      registerCommand(id, handler) {
        const record = { id, handler, disposed: false };
        commands.push(record);
        return { dispose: () => { record.disposed = true; } };
      },
    },
    Uri: { file: (path) => ({ scheme: "file", path, external: `file://${path}` }) },
  };
  return { monaco, records, commands };
}

function projectModel(path, { scheme = "file", lines = ["task build", "  run tsc", "task test"], version = 1 } = {}) {
  const model = {
    uri: { scheme, path },
    version,
    disposed: false,
    getLinesContent: () => lines,
    getLineCount: () => lines.length,
    getLanguageId: () => "typescript",
    getVersionId: () => model.version,
    isDisposed: () => model.disposed,
    validateRange(range) {
      const endLineNumber = Math.min(range.endLineNumber, lines.length);
      const endColumn = endLineNumber < range.endLineNumber ? lines[endLineNumber - 1].length + 1 : Math.min(range.endColumn, lines[endLineNumber - 1].length + 1);
      return { startLineNumber: range.startLineNumber, startColumn: Math.min(range.startColumn, lines[range.startLineNumber - 1].length + 1), endLineNumber, endColumn };
    },
    getWordUntilPosition: () => ({ word: "", startColumn: 1, endColumn: 1 }),
  };
  return model;
}

function cancellableToken() {
  const listeners = [];
  return {
    isCancellationRequested: false,
    onCancellationRequested(listener) {
      listeners.push(listener);
      return { dispose() {} };
    },
    cancel() {
      this.isCancellationRequested = true;
      for (const listener of listeners) listener();
    },
  };
}

function okResponse(response) {
  return { ok: true, response: { dropped: 0, truncated: false, ...response } };
}

function emptyInvoker(calls, overrides = {}) {
  const record = (name, value) => async (provider, request) => {
    calls.push([name, provider.id, request]);
    return typeof value === "function" ? value(request) : value;
  };
  return {
    hover: record("hover", okResponse({ contents: ["h"] })),
    completion: record("completion", okResponse({ items: [], incomplete: false })),
    definition: record("definition", okResponse({ locations: [] })),
    references: record("references", okResponse({ locations: [] })),
    codeLens: record("codeLens", okResponse({ lenses: [] })),
    inlayHints: record("inlayHints", okResponse({ hints: [] })),
    documentSymbol: record("documentSymbol", okResponse({ symbols: [] })),
    ...Object.fromEntries(Object.entries(overrides).map(([name, value]) => [name, record(name, value)])),
  };
}

function setupAll(invoker, { locale = "zh", roots = ["/repo"], entries = [allKinds] } = {}) {
  const { providers } = collectLanguageFeatures([contribution("acme.lang", entries)]);
  setLanguageFeatureInvoker(invoker);
  setMonacoLanguageFeatureRuntime({ locale: () => locale, projectRoots: () => roots });
  setMonacoLanguageFeatures(providers);
  const stub = stubMonacoAll();
  const detach = attachMonacoLanguageFeatures(stub.monaco);
  return {
    ...stub,
    providers,
    cleanup() {
      detach();
      setMonacoLanguageFeatures([]);
      setMonacoLanguageFeatureRuntime(undefined);
      setLanguageFeatureInvoker();
      setLanguageFeatureCommandRunner();
    },
  };
}

/** Runs a passive provider call through the 300 ms debounce with mocked timers. */
async function passive(call) {
  const pending = call();
  mock.timers.tick(LANGUAGE_FEATURE_LIMITS.passiveDebounceMs);
  return pending;
}

function isEmptyResult(result) {
  if (result == null) return true;
  if (Array.isArray(result)) return result.length === 0;
  return (result.lenses ?? result.hints).length === 0;
}

test("monaco-language-features: registers each declared kind", () => {
  const env = setupAll(emptyInvoker([]), { entries: [allKinds, { ...entry, id: "hover-only", features: ["hover"], triggerCharacters: undefined }] });
  try {
    for (const kind of ["definition", "references", "codeLens", "inlayHints", "documentSymbol"]) {
      assert.equal(env.records[kind].length, 1, kind);
      assert.equal(env.records[kind][0].provider.displayName, "任务", `${kind} displayName`);
      assert.deepEqual(env.records[kind][0].selector, ["typescript", "sql"]);
    }
    assert.equal(env.records.hover.length, 2);
    assert.equal(PLUGIN_CODE_LENS_COMMAND, "mincode.pluginCodeLens");
    assert.deepEqual(env.commands.map((command) => command.id), ["mincode.pluginCodeLens"]);
    setMonacoLanguageFeatures(env.providers);
    assert.equal(env.commands.length, 1, "the CodeLens command is registered once per attach");
    assert.equal(env.records.codeLens.length, 2);
    assert.equal(env.records.codeLens[0].disposed, true);
  } finally {
    env.cleanup();
  }
  assert.equal(env.commands[0].disposed, true, "detach removes the command");
});

test("monaco-language-features: new kinds serve only project files", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const calls = [];
  const env = setupAll(emptyInvoker(calls));
  try {
    const position = { lineNumber: 1, column: 2 };
    for (const model of [projectModel("/q1.sql", { scheme: "app" }), projectModel("/outside/a.ts"), projectModel("/repo")]) {
      assert.ok(isEmptyResult(await env.records.definition[0].provider.provideDefinition(model, position, token)));
      assert.ok(isEmptyResult(await env.records.references[0].provider.provideReferences(model, position, { includeDeclaration: true }, token)));
      assert.ok(isEmptyResult(await passive(() => env.records.codeLens[0].provider.provideCodeLenses(model, token))));
      assert.ok(isEmptyResult(await passive(() => env.records.documentSymbol[0].provider.provideDocumentSymbols(model, token))));
      const range = { startLineNumber: 1, startColumn: 1, endLineNumber: 3, endColumn: 1 };
      assert.ok(isEmptyResult(await passive(() => env.records.inlayHints[0].provider.provideInlayHints(model, range, token))));
    }
    assert.deepEqual(calls, [], "no new-kind call outside project files");
    await env.records.hover[0].provider.provideHover(projectModel("/q1.sql", { scheme: "app" }), position, token);
    assert.deepEqual(calls.map((call) => call[0]), ["hover"], "hover still serves app: models");
  } finally {
    env.cleanup();
    mock.timers.reset();
  }
});

test("monaco-language-features: maps locations, symbols and hints", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const calls = [];
  let afterCall;
  const range = (startLine, startColumn, endLine, endColumn) => ({ startLine, startColumn, endLine, endColumn });
  const env = setupAll(emptyInvoker(calls, {
    definition: () => {
      afterCall?.();
      return okResponse({
        locations: [
          { range: range(3, 1, 3, 10), selectionRange: range(3, 6, 3, 10) },
          { range: range(9, 1, 9, 2) },
          { path: "lib/b.ts", range: range(40, 2, 41, 3), selectionRange: range(40, 2, 40, 5) },
          { path: "src/a.ts", range: range(1, 1, 1, 5) },
        ],
      });
    },
    references: okResponse({ locations: [{ range: range(1, 6, 1, 11) }, { path: "lib/c.ts", range: range(2, 1, 2, 4) }] }),
    documentSymbol: () => {
      afterCall?.();
      return okResponse({
        symbols: [
          { name: "build", kind: "enumMember", detail: "task", range: range(1, 1, 2, 10), selectionRange: range(1, 6, 1, 11), children: [{ name: "T", kind: "typeParameter", range: range(2, 3, 2, 6), selectionRange: range(2, 3, 2, 6) }] },
          { name: "gone", kind: "function", range: range(30, 1, 30, 2), selectionRange: range(30, 1, 30, 2) },
        ],
      });
    },
    inlayHints: okResponse({ hints: [{ position: { line: 2, column: 3 }, label: "cmd:", kind: "parameter", paddingRight: true }] }),
  }));
  try {
    const model = projectModel("/repo/src/a.ts");
    const position = { lineNumber: 3, column: 7 };
    const links = await env.records.definition[0].provider.provideDefinition(model, position, token);
    assert.equal(calls[0][2].projectRoot, "/repo");
    assert.equal(calls[0][2].relativePath, "src/a.ts");
    assert.deepEqual(calls[0][2].position, { line: 3, column: 7 });
    assert.equal(links.length, 3, "a same-document location past the last line is dropped");
    assert.equal(links[0].uri, model.uri);
    assert.deepEqual(links[0].range, { startLineNumber: 3, startColumn: 1, endLineNumber: 3, endColumn: 10 });
    assert.deepEqual(links[0].targetSelectionRange, { startLineNumber: 3, startColumn: 6, endLineNumber: 3, endColumn: 10 });
    assert.deepEqual(links[1].uri, { scheme: "file", path: "/repo/lib/b.ts", external: "file:///repo/lib/b.ts" });
    assert.deepEqual(links[1].range, { startLineNumber: 40, startColumn: 2, endLineNumber: 41, endColumn: 3 });
    assert.deepEqual(links[1].targetSelectionRange, { startLineNumber: 40, startColumn: 2, endLineNumber: 40, endColumn: 5 });
    assert.equal(links[2].uri, model.uri, "the document's own relative path is the model itself");

    const references = await env.records.references[0].provider.provideReferences(model, position, { includeDeclaration: false }, token);
    assert.equal(calls[1][2].includeDeclaration, false);
    assert.equal(calls[1][2].projectRoot, "/repo");
    assert.deepEqual(references.map((location) => location.uri.path), ["/repo/src/a.ts", "/repo/lib/c.ts"]);

    const symbols = await passive(() => env.records.documentSymbol[0].provider.provideDocumentSymbols(model, token));
    const symbolRequest = calls.at(-1)[2];
    assert.equal(symbolRequest.startLine, 1);
    assert.equal(symbolRequest.text, "task build\n  run tsc\ntask test");
    assert.equal("position" in symbolRequest, false);
    assert.equal("projectRoot" in symbolRequest, false);
    assert.equal(symbols.length, 1, "a symbol past the last line is dropped");
    assert.equal(symbols[0].kind, 21);
    assert.equal(symbols[0].detail, "task");
    assert.deepEqual(symbols[0].tags, []);
    assert.deepEqual(symbols[0].selectionRange, { startLineNumber: 1, startColumn: 6, endLineNumber: 1, endColumn: 11 });
    assert.equal(symbols[0].children[0].kind, 25);
    assert.equal(symbols[0].children[0].detail, "");

    const hints = await passive(() => env.records.inlayHints[0].provider.provideInlayHints(model, { startLineNumber: 2, startColumn: 1, endLineNumber: 3, endColumn: 1 }, token));
    assert.deepEqual(calls.at(-1)[2].range, { startLine: 2, endLine: 3 });
    assert.equal(calls.at(-1)[2].startLine, 1);
    assert.deepEqual(hints.hints, [{ label: "cmd:", position: { lineNumber: 2, column: 3 }, kind: 2, paddingRight: true }]);
    hints.dispose();

    afterCall = () => { model.version += 1; };
    assert.ok(isEmptyResult(await env.records.definition[0].provider.provideDefinition(model, position, token)), "stale definition");
    assert.ok(isEmptyResult(await passive(() => env.records.documentSymbol[0].provider.provideDocumentSymbols(model, token))), "stale symbols");
  } finally {
    env.cleanup();
    mock.timers.reset();
  }
});

test("monaco-language-features: passive providers debounce, cancel and suspend", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const calls = [];
  let lensResult = { ok: false, error: { code: "timeout", detail: "" } };
  const env = setupAll(emptyInvoker(calls, { codeLens: () => lensResult }));
  try {
    const model = projectModel("/repo/src/a.ts");
    const lenses = env.records.codeLens[0].provider;

    const cancelled = cancellableToken();
    const pending = lenses.provideCodeLenses(model, cancelled);
    mock.timers.tick(LANGUAGE_FEATURE_LIMITS.passiveDebounceMs - 1);
    cancelled.cancel();
    mock.timers.tick(10);
    assert.ok(isEmptyResult(await pending));
    assert.equal(calls.length, 0, "a request cancelled within the debounce never reaches the host");

    const early = lenses.provideCodeLenses(model, token);
    mock.timers.tick(LANGUAGE_FEATURE_LIMITS.passiveDebounceMs - 1);
    await Promise.resolve();
    assert.equal(calls.length, 0, "nothing is sent before the debounce ends");
    mock.timers.tick(1);
    await early;
    assert.equal(calls.length, 1);

    await passive(() => lenses.provideCodeLenses(model, token));
    await passive(() => lenses.provideCodeLenses(model, token));
    assert.equal(calls.length, 3);
    assert.deepEqual(languageFeatureSuspensions(), ["acme.lang\u{0}tasks\u{0}codeLens"]);
    assert.ok(isEmptyResult(await passive(() => lenses.provideCodeLenses(model, token))));
    assert.equal(calls.length, 3, "the fourth call after three timeouts is suspended");

    await passive(() => env.records.documentSymbol[0].provider.provideDocumentSymbols(model, token));
    assert.equal(calls.length, 4, "other kinds of the provider are not suspended");

    setMonacoLanguageFeatures(env.providers);
    assert.deepEqual(languageFeatureSuspensions(), []);
    lensResult = okResponse({ lenses: [] });
    await passive(() => env.records.codeLens.at(-1).provider.provideCodeLenses(model, token));
    assert.equal(calls.length, 5, "a new provider list lifts the suspension");

    const before = calls.length;
    await env.records.definition.at(-1).provider.provideDefinition(model, { lineNumber: 1, column: 1 }, token);
    assert.equal(calls.length, before + 1, "definition is not debounced");

    const dropped = env.records.codeLens.at(-1).provider.provideCodeLenses(model, cancellableToken());
    setMonacoLanguageFeatures([]);
    mock.timers.tick(LANGUAGE_FEATURE_LIMITS.passiveDebounceMs);
    assert.ok(isEmptyResult(await dropped), "removing the providers ends pending debounces");
    assert.equal(calls.length, before + 1);

    setMonacoLanguageFeatures(env.providers);
    const big = projectModel("/repo/src/big.ts", { lines: ["x".repeat(LANGUAGE_FEATURE_LIMITS.textMaxBytes + 1)] });
    assert.ok(isEmptyResult(await passive(() => env.records.codeLens.at(-1).provider.provideCodeLenses(big, token))));
    assert.equal(calls.length, before + 1, "documents over textMaxBytes are not sent");
  } finally {
    env.cleanup();
    mock.timers.reset();
  }
});

test("monaco-language-features: codeLens click dispatches through plugin command paths", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const range = { startLine: 1, startColumn: 1, endLine: 1, endColumn: 5 };
  const env = setupAll(emptyInvoker([], {
    codeLens: okResponse({
      lenses: [
        { range, title: "Run build", command: "run", args: { name: "build" } },
        { range, title: "Info" },
        { range, title: "Clean", command: "clean" },
      ],
    }),
  }));
  const runs = [];
  const failures = [];
  setLanguageFeatureCommandRunner({
    async withArgs(pluginId, command, args) {
      runs.push(["withArgs", pluginId, command, args]);
    },
    async plain(pluginId, command) {
      runs.push(["plain", pluginId, command]);
      if (command === "clean") throw new Error("Plugin command acme.lang:clean is not available");
    },
    failed(message) {
      failures.push(message);
    },
  });
  try {
    const model = projectModel("/repo/src/a.ts");
    const list = await passive(() => env.records.codeLens[0].provider.provideCodeLenses(model, token));
    assert.equal(list.lenses.length, 3);
    const [run, info, clean] = list.lenses;
    assert.deepEqual(run.range, { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 5 });
    assert.equal(run.command.id, PLUGIN_CODE_LENS_COMMAND);
    assert.equal(run.command.title, "Run build");
    assert.equal(run.command.arguments.length, 1);
    assert.equal(typeof run.command.arguments[0], "string", "Monaco only sees an opaque key");
    assert.equal(run.command.arguments[0].includes("build"), false);
    assert.deepEqual(info.command, { id: "", title: "Info" });

    const handler = env.commands[0].handler;
    await handler({}, run.command.arguments[0]);
    await handler({}, clean.command.arguments[0]);
    assert.deepEqual(runs, [["withArgs", "acme.lang", "run", { name: "build" }], ["plain", "acme.lang", "clean"]]);
    assert.equal(failures.length, 1);
    assert.match(failures[0], /not available/);

    await handler({}, { pluginId: "acme.lang", command: "run" });
    assert.equal(runs.length, 2, "only opaque keys are accepted");

    list.dispose();
    await handler({}, run.command.arguments[0]);
    assert.equal(runs.length, 2, "a disposed list's keys do not run");

    const again = await passive(() => env.records.codeLens[0].provider.provideCodeLenses(model, token));
    setMonacoLanguageFeatures(env.providers);
    await env.commands[0].handler({}, again.lenses[0].command.arguments[0]);
    assert.equal(runs.length, 2, "a provider list change clears the keys");
  } finally {
    env.cleanup();
    mock.timers.reset();
  }
});

test("language-feature-results: codeLens commands are re-checked at click time", () => {
  const schema = { type: "object", properties: { name: { type: "string", maxLength: 5 }, mode: { type: "string", enum: ["a", "b"] }, n: { type: "integer" } }, required: ["name"] };
  const read = { action: "run", risk: "read", effectiveRisk: "read", inputSchema: schema };
  assert.equal(codeLensCommandAccepts(read, { name: "x" }), true);
  assert.equal(codeLensCommandAccepts(read, { name: "x", mode: "b", n: 3 }), true);
  assert.equal(codeLensCommandAccepts(read, { mode: "a" }), false, "required");
  assert.equal(codeLensCommandAccepts(read, { name: "toolong" }), false, "maxLength");
  assert.equal(codeLensCommandAccepts(read, { name: "x", mode: "c" }), false, "enum");
  assert.equal(codeLensCommandAccepts(read, { name: "x", n: 1.5 }), false, "integer");
  assert.equal(codeLensCommandAccepts(read, { name: "x", other: 1 }), false, "undeclared");
  assert.equal(codeLensCommandAccepts(read, { name: "a\u{202e}b" }), false, "unsafe text");
  assert.equal(codeLensCommandAccepts(read, { name: "x", n: 2 ** 54 }), false, "out of range");
  assert.equal(codeLensCommandAccepts({ ...read, risk: "write", effectiveRisk: "write" }, { name: "x" }), false, "write commands take no args");
  assert.equal(codeLensCommandAccepts({ ...read, effectiveRisk: "dangerous" }, { name: "x" }), false, "a read command running a dangerous action takes no args");
  assert.equal(codeLensCommandAccepts({ ...read, effectiveRisk: "network" }, { name: "x" }), false, "network is not read");
  assert.equal(codeLensCommandAccepts({ ...read, inputSchema: undefined }, { name: "x" }), false, "no schema, no args");
  assert.equal(codeLensCommandAccepts({ ...read, target: { type: "view", viewId: "v" } }, { name: "x" }), false);
  assert.equal(codeLensCommandAccepts({ ...read, risk: "dangerous", effectiveRisk: "dangerous", inputSchema: undefined }), true, "no args: any action command");
  assert.equal(codeLensCommandAccepts({ risk: "read", effectiveRisk: "read", target: { type: "view", viewId: "v" } }), false, "view targets never");
  assert.equal(codeLensCommandAccepts(undefined), false);
});

function codeLensRunnerEnv({ confirmAnswer = true, trusted = async () => false } = {}) {
  const commands = [
    { pluginId: "acme.lang", pluginName: "Acme Lang", id: "run", title: "Run task", titleLocalized: { zh: "运行任务" }, action: "run", risk: "read", effectiveRisk: "read", inputSchema: { type: "object", properties: { name: { type: "string" } } } },
    { pluginId: "acme.lang", pluginName: "Acme Lang", id: "write", title: "Rewrite file", action: "write", risk: "write", effectiveRisk: "write" },
    { pluginId: "acme.lang", pluginName: "Acme Lang", id: "purge", title: "Purge", action: "deleteAll", risk: "read", effectiveRisk: "dangerous" },
    { pluginId: "acme.lang", pluginName: "Acme Lang", id: "wipe", title: "Wipe cache", titleLocalized: { zh: "清空缓存" }, action: "wipe", risk: "dangerous", effectiveRisk: "dangerous" },
    { pluginId: "p.x", pluginName: "X", id: "fix", title: "Fix", action: "fix", risk: "write", effectiveRisk: "write", inputSchema: { type: "object", properties: { name: { type: "string" } } } },
    { pluginId: "p.x", pluginName: "X", id: "look", title: "Look", action: "look", risk: "read", effectiveRisk: "read", inputSchema: { type: "object", properties: { name: { type: "string" } } } },
  ];
  const confirms = [];
  const executed = [];
  const runner = createCodeLensCommandRunner({
    commands: () => commands,
    locale: () => ["zh"],
    t: (key, params) => (params ? `${key}|${params.plugin}|${params.command}` : key),
    async confirm(request) {
      confirms.push(request);
      return confirmAnswer;
    },
    trusted: (pluginId, command) => trusted(pluginId, command, commands),
    async execute(pluginId, command) {
      executed.push(["plain", pluginId, command]);
    },
    async executeWithArgs(pluginId, command, args) {
      executed.push(["withArgs", pluginId, command, args]);
    },
  });
  return { runner, confirms, executed, commands };
}

test("code-lens-commands: write and dangerous commands confirm with the manifest title first", async () => {
  const cancelled = codeLensRunnerEnv({ confirmAnswer: false });
  await cancelled.runner.plain("acme.lang", "write");
  assert.equal(cancelled.confirms.length, 1, "a write lens shows the confirmation");
  assert.deepEqual(cancelled.executed, [], "cancelling does not run the command");
  assert.equal(cancelled.confirms[0].dangerous, false);
  assert.equal(cancelled.confirms[0].title, "pluginContrib.languageFeatures.codeLensConfirmTitle");
  assert.equal(cancelled.confirms[0].message, "pluginContrib.languageFeatures.codeLensConfirmWrite|Acme Lang|Rewrite file");

  const accepted = codeLensRunnerEnv();
  await accepted.runner.plain("acme.lang", "wipe");
  assert.equal(accepted.confirms[0].dangerous, true);
  assert.equal(accepted.confirms[0].message, "pluginContrib.languageFeatures.codeLensConfirmDangerous|Acme Lang|清空缓存", "the localized manifest title, never the lens title");
  assert.deepEqual(accepted.executed, [["plain", "acme.lang", "wipe"]]);

  await assert.rejects(accepted.runner.withArgs("acme.lang", "write", { name: "x" }), /does not accept/);
  await assert.rejects(accepted.runner.plain("acme.lang", "gone"), /not available/);
  assert.equal(accepted.executed.length, 1);
});

test("code-lens-commands: the effective risk decides (a read command running a dangerous action confirms)", async () => {
  const env = codeLensRunnerEnv({ confirmAnswer: false });
  await env.runner.plain("acme.lang", "purge");
  assert.equal(env.confirms.length, 1);
  assert.equal(env.confirms[0].dangerous, true);
  assert.equal(env.confirms[0].message, "pluginContrib.languageFeatures.codeLensConfirmDangerous|Acme Lang|Purge");
  assert.deepEqual(env.executed, []);
});

test("code-lens-commands: a command that changed while the confirmation was open does not run", async () => {
  for (const change of [
    (commands) => { commands[1] = { ...commands[1], title: "Rewrite everything" }; },
    (commands) => { commands[1] = { ...commands[1], effectiveRisk: "dangerous" }; },
    (commands) => { commands[1] = { ...commands[1], action: "other" }; },
    (commands) => { commands.splice(1, 1); },
  ]) {
    const env = codeLensRunnerEnv();
    const confirm = env.runner.plain;
    let changed = false;
    const original = env.confirms.push.bind(env.confirms);
    env.confirms.push = (request) => {
      if (!changed) {
        changed = true;
        change(env.commands);
      }
      return original(request);
    };
    await assert.rejects(confirm("acme.lang", "write"), /out of date/);
    assert.deepEqual(env.executed, []);
  }
  const unchanged = codeLensRunnerEnv();
  await unchanged.runner.plain("acme.lang", "write");
  assert.deepEqual(unchanged.executed, [["plain", "acme.lang", "write"]]);
});

test("code-lens-commands: read commands run without a confirmation", async () => {
  const env = codeLensRunnerEnv({ confirmAnswer: false });
  await env.runner.withArgs("acme.lang", "run", { name: "build" });
  await env.runner.plain("acme.lang", "run");
  assert.deepEqual(env.confirms, []);
  assert.deepEqual(env.executed, [["withArgs", "acme.lang", "run", { name: "build" }], ["plain", "acme.lang", "run"]]);
  await assert.rejects(env.runner.withArgs("acme.lang", "run", { other: 1 }), /does not accept/, "args are re-validated against the current inputSchema");
});

test("monaco-language-features: CodeLens keys are bounded and a provider change asks Monaco again", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const range = { startLine: 1, startColumn: 1, endLine: 1, endColumn: 5 };
  const env = setupAll(emptyInvoker([], {
    codeLens: okResponse({ lenses: Array.from({ length: LANGUAGE_FEATURE_LIMITS.codeLensesMax }, () => ({ range, title: "Run", command: "run" })) }),
  }));
  const runs = [];
  setLanguageFeatureCommandRunner({
    async withArgs() {},
    async plain(pluginId, command) {
      runs.push([pluginId, command]);
    },
    failed() {},
  });
  try {
    const model = projectModel("/repo/src/a.ts");
    const provider = env.records.codeLens[0].provider;
    const lists = [];
    for (let index = 0; index <= CODE_LENS_TARGETS_MAX / LANGUAGE_FEATURE_LIMITS.codeLensesMax; index += 1) {
      lists.push(await passive(() => provider.provideCodeLenses(model, token)));
    }
    const handler = env.commands[0].handler;
    await handler({}, lists[0].lenses[0].command.arguments[0]);
    assert.equal(runs.length, 0, "the oldest keys of never-disposed lists are evicted");
    await handler({}, lists.at(-1).lenses[0].command.arguments[0]);
    assert.equal(runs.length, 1);

    let changes = 0;
    const subscription = env.records.codeLens.at(-1).provider.onDidChange(() => { changes += 1; });
    setMonacoLanguageFeatures(env.providers);
    assert.ok(changes >= 1, "codeLens onDidChange fires after the provider list changes");
    subscription.dispose();
  } finally {
    env.cleanup();
    mock.timers.reset();
  }
});

// ---- codeAction (quick fixes that edit only the current document) ----

const doc = (lines) => ({ lineCount: lines.length, maxColumn: (line) => lines[line - 1].length + 1 });
const range = (startLine, startColumn, endLine, endColumn) => ({ startLine, startColumn, endLine, endColumn });

test("languageFeatures: code actions edit only the current document", () => {
  const model = doc(["const a = 1", "const b = 2"]);
  const edit = (r, text) => ({ range: r, text });
  const actions = validateCodeActionResult([
    { title: "Use let", kind: "quickfix", isPreferred: true, edits: [edit(range(2, 1, 2, 6), "let"), edit(range(1, 1, 1, 6), "let")] },
    { title: "Delete", edits: [edit(range(1, 1, 2, 1), "")] },
    { title: "Past end", edits: [edit(range(1, 1, 1, 2), "x"), edit(range(3, 1, 3, 1), "y")] },
    { title: "Past column", edits: [edit(range(2, 1, 2, 40), "x")] },
    { title: "Overlap", edits: [edit(range(1, 1, 1, 6), "a"), edit(range(1, 3, 1, 8), "b")] },
    { title: "Other file", uri: "file:///etc/hosts", edits: [edit(range(1, 1, 1, 2), "x")] },
    { title: "Edit with uri", edits: [{ ...edit(range(1, 1, 1, 2), "x"), uri: "file:///etc/hosts" }] },
    { title: "Command", command: "rm", edits: [edit(range(1, 1, 1, 2), "x")] },
    { title: "Bad kind", kind: "shell", edits: [edit(range(1, 1, 1, 2), "x")] },
    { title: "No edits", edits: [] },
    { title: "Bidi", edits: [edit(range(1, 1, 1, 2), "x‮y")] },
    { title: "Too many", edits: Array.from({ length: LANGUAGE_FEATURE_LIMITS.codeActionEditsMax + 1 }, () => edit(range(1, 1, 1, 1), "x")) },
    { title: "Too big", edits: [edit(range(1, 1, 1, 1), "x".repeat(LANGUAGE_FEATURE_LIMITS.codeActionEditTextMaxBytes + 1))] },
  ], model);
  assert.deepEqual(actions, [
    { title: "Use let", kind: "quickfix", isPreferred: true, edits: [edit(range(1, 1, 1, 6), "let"), edit(range(2, 1, 2, 6), "let")] },
    { title: "Delete", edits: [edit(range(1, 1, 2, 1), "")] },
  ]);
  const many = Array.from({ length: LANGUAGE_FEATURE_LIMITS.codeActionItemsMax + 3 }, () => ({ title: "Fix", edits: [edit(range(1, 1, 1, 1), "x")] }));
  assert.equal(validateCodeActionResult(many, model).length, LANGUAGE_FEATURE_LIMITS.codeActionItemsMax);
});

test("languageFeatures: code action diagnostics are bounded plain text", () => {
  const model = doc(["abc", "def"]);
  const diagnostics = codeActionDiagnostics([
    { range: range(1, 1, 1, 3), message: "bad\r\nthing‮", severity: "error", source: " lint\u0007 " },
    { range: range(5, 1, 5, 2), message: "outside", severity: "warning" },
    { range: range(2, 1, 2, 2), message: "  ", severity: "info" },
    { range: range(2, 1, 2, 2), message: "m".repeat(5000), severity: "hint" },
  ], model);
  assert.equal(diagnostics.length, 2);
  assert.deepEqual(diagnostics[0], { range: range(1, 1, 1, 3), message: "bad\nthing", severity: "error", source: "lint" });
  assert.equal([...diagnostics[1].message].length, LANGUAGE_FEATURE_LIMITS.codeActionDiagnosticMessageMaxRunes);
  const many = Array.from({ length: 40 }, () => ({ range: range(1, 1, 1, 2), message: "m", severity: "error" }));
  assert.equal(codeActionDiagnostics(many, model).length, LANGUAGE_FEATURE_LIMITS.codeActionDiagnosticsMax);
});

function documentStubMonaco() {
  const lenses = [];
  const actions = [];
  const monaco = {
    languages: {
      CompletionItemKind: { Text: 18 },
      CompletionTriggerKind: { Invoke: 0, TriggerCharacter: 1 },
      registerHoverProvider: () => ({ dispose() {} }),
      registerCompletionItemProvider: () => ({ dispose() {} }),
      registerCodeLensProvider(selector, provider) {
        const record = { selector, provider, disposed: false };
        lenses.push(record);
        return { dispose: () => { record.disposed = true; } };
      },
      registerCodeActionProvider(selector, provider, metadata) {
        const record = { selector, provider, metadata, disposed: false };
        actions.push(record);
        return { dispose: () => { record.disposed = true; } };
      },
    },
    MarkerSeverity: { Hint: 1, Info: 2, Warning: 4, Error: 8 },
    editor: {
      EditorOption: { readOnly: 92 },
      getEditors: () => editors,
      registerCommand: () => ({ dispose() {} }),
    },
  };
  const editors = [];
  return { monaco, lenses, actions, editors };
}

function documentModel(path, lines) {
  const listeners = new Set();
  const model = {
    uri: { scheme: "file", path },
    version: 1,
    lines: [...lines],
    edits: [],
    getLinesContent: () => model.lines,
    getLineCount: () => model.lines.length,
    getLineMaxColumn: (line) => model.lines[line - 1].length + 1,
    getLanguageId: () => "go",
    getVersionId: () => model.version,
    isDisposed: () => false,
    onDidChangeContent(listener) {
      listeners.add(listener);
      return { dispose: () => listeners.delete(listener) };
    },
    onWillDispose: () => ({ dispose() {} }),
    pushStackElement: () => model.edits.push("stop"),
    pushEditOperations(_selections, operations) {
      model.edits.push(operations);
      model.version += 1;
      return null;
    },
    type() {
      model.version += 1;
      for (const listener of [...listeners]) listener({});
    },
  };
  return model;
}

const actionEntry = { id: "tests", title: "Tests", languages: ["go"], features: ["codeAction"], action: "tests.lens" };

function documentRuntime(overrides = {}) {
  const registered = new Map();
  return {
    registered,
    runtime: {
      locale: () => "en",
      projectRoots: () => ["/repo"],
      registerEditorCommand(id, handler) {
        registered.set(id, handler);
        return () => registered.delete(id);
      },
      ...overrides,
    },
  };
}

test("languageFeatures: a quick fix edits only its model, as one undo step, when picked", async (t) => {
  t.mock.method(console, "warn", () => {});
  const { providers } = collectLanguageFeatures([contribution("acme.tests", [actionEntry])]);
  const calls = [];
  setLanguageFeatureInvoker({
    async codeAction(provider, request) {
      calls.push(request);
      return {
        ok: true,
        response: {
          actions: [
            { title: "Use let", kind: "quickfix", isPreferred: true, edits: [{ range: range(1, 1, 1, 6), text: "let" }, { range: range(2, 1, 2, 6), text: "let" }] },
            { title: "Elsewhere", edits: [{ range: range(7, 1, 7, 2), text: "x" }] },
          ],
          dropped: 0,
          truncated: false,
        },
      };
    },
  });
  const readOnlyNotices = { count: 0 };
  const { runtime, registered } = documentRuntime({ notifyReadOnly: () => { readOnlyNotices.count += 1; } });
  setMonacoLanguageFeatureRuntime(runtime);
  setMonacoLanguageFeatures(providers);
  const stub = documentStubMonaco();
  const detach = attachMonacoLanguageFeatures(stub.monaco);
  try {
    assert.equal(stub.actions.length, 1);
    const model = documentModel("/repo/a.go", ["const a = 1", "const b = 2"]);
    const marker = { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 6, message: "use let", severity: 4, source: "vet" };
    const list = await stub.actions[0].provider.provideCodeActions(
      model,
      { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 },
      { markers: [marker], trigger: 1 },
      cancellableToken(),
    );
    assert.deepEqual(calls[0].range, range(1, 1, 1, 1));
    assert.deepEqual(calls[0].diagnostics, [{ range: range(1, 1, 1, 6), message: "use let", severity: "warning", source: "vet" }]);
    assert.equal("position" in calls[0], false);
    assert.equal(list.actions.length, 1, "an action with an edit outside the document is dropped");
    const [action] = list.actions;
    assert.equal(action.title, "Use let");
    assert.equal(action.kind, "quickfix");
    assert.equal(action.isPreferred, true);
    assert.equal(action.edit, undefined, "no workspace edit: Monaco never applies it by itself");
    assert.deepEqual(model.edits, [], "nothing is applied before the user picks the action");

    registered.get(action.command.id)(...action.command.arguments);
    assert.deepEqual(model.edits, [
      "stop",
      [
        { range: { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 6 }, text: "let", forceMoveMarkers: true },
        { range: { startLineNumber: 2, startColumn: 1, endLineNumber: 2, endColumn: 6 }, text: "let", forceMoveMarkers: true },
      ],
      "stop",
    ], "one undo stop around a single edit operation");

    registered.get(action.command.id)(...action.command.arguments);
    assert.equal(model.edits.length, 3, "a stale action (model changed) is not applied again");
    list.dispose();

    // A read-only editor refuses a picked quick fix and tells the user.
    const readOnlyEditor = { getModel: () => model, getOption: (id) => id === 92 };
    stub.editors.push({ getModel: () => documentModel("/repo/other.go", ["x"]), getOption: () => false }, readOnlyEditor);
    const again = await stub.actions[0].provider.provideCodeActions(
      model, { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 }, { markers: [] }, cancellableToken(),
    );
    const before = model.edits.length;
    registered.get(again.actions[0].command.id)(...again.actions[0].command.arguments);
    assert.equal(model.edits.length, before, "nothing is applied in a read-only editor");
    assert.equal(readOnlyNotices.count, 1);
    stub.editors.pop();
    const writable = await stub.actions[0].provider.provideCodeActions(
      model, { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 }, { markers: [] }, cancellableToken(),
    );
    registered.get(writable.actions[0].command.id)(...writable.actions[0].command.arguments);
    assert.equal(model.edits.length, before + 3, "a writable editor applies it");
  } finally {
    detach();
    setMonacoLanguageFeatures([]);
    setMonacoLanguageFeatureRuntime(undefined);
    setLanguageFeatureInvoker();
  }
});

function trustedCodeLensEnv({ trustedAnswer = true, confirmAnswer = false, onTrusted } = {}) {
  const trustCalls = [];
  const env = codeLensRunnerEnv({
    confirmAnswer,
    async trusted(pluginId, command, commands) {
      trustCalls.push([pluginId, command]);
      onTrusted?.(commands);
      if (trustedAnswer instanceof Error) throw trustedAnswer;
      return trustedAnswer;
    },
  });
  return { ...env, trustCalls };
}

test("code-lens: trusted write runs without args", async () => {
  const env = trustedCodeLensEnv();
  await env.runner.plain("p.x", "fix");
  assert.deepEqual(env.confirms, [], "the dialog is skipped");
  assert.deepEqual(env.executed, [["plain", "p.x", "fix"]]);
  assert.equal(env.trustCalls.length, 1);
  assert.equal(env.trustCalls[0][0], "p.x");
  assert.equal(env.trustCalls[0][1].id, "fix");
  assert.equal(env.trustCalls[0][1].effectiveRisk, "write");

  // A lens that carries args still goes through the read-only args rule.
  await assert.rejects(env.runner.withArgs("p.x", "fix", { name: "x" }), /does not accept/);
  assert.equal(env.trustCalls.length, 1, "args never ask trust");
  assert.ok(env.executed.every(([kind]) => kind === "plain"), "executeWithArgs is never called");
  assert.equal(env.executed.length, 1);
});

test("code-lens: untrusted write still confirms", async () => {
  for (const trustedAnswer of [false, new Error("authorize failed"), null, 1]) {
    const env = trustedCodeLensEnv({ trustedAnswer });
    await env.runner.plain("p.x", "fix");
    assert.equal(env.trustCalls.length, 1);
    assert.equal(env.confirms.length, 1, `${String(trustedAnswer)} falls back to the dialog`);
    assert.deepEqual(env.executed, []);
  }
});

test("code-lens: trusted command still re-checked", async () => {
  for (const change of [
    (commands) => { const i = commands.findIndex((c) => c.pluginId === "p.x" && c.id === "fix"); commands[i] = { ...commands[i], effectiveRisk: "dangerous" }; },
    (commands) => { commands.splice(commands.findIndex((c) => c.pluginId === "p.x" && c.id === "fix"), 1); },
  ]) {
    const env = trustedCodeLensEnv({ onTrusted: change });
    await assert.rejects(env.runner.plain("p.x", "fix"), /out of date/);
    assert.deepEqual(env.confirms, []);
    assert.deepEqual(env.executed, []);
  }
});

test("code-lens: read never asks trust", async () => {
  const env = trustedCodeLensEnv();
  await env.runner.plain("p.x", "look");
  await env.runner.withArgs("p.x", "look", { name: "x" });
  assert.deepEqual(env.trustCalls, []);
  assert.deepEqual(env.confirms, []);
  assert.deepEqual(env.executed, [["plain", "p.x", "look"], ["withArgs", "p.x", "look", { name: "x" }]]);
  await assert.rejects(env.runner.withArgs("p.x", "look", { other: 1 }), /does not accept/, "trusted plugins' args are still validated");
  assert.equal(env.executed.length, 2);
});
