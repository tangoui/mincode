import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { compile as compileMonarch } from "../node_modules/monaco-editor/esm/vs/editor/standalone/common/monarch/monarchCompile.js";
import { buildLanguageRegistry, languagePreviewSummary, resolvePluginLanguageAssociations } from "../src/domain/editor/language-contributions.ts";
import { collectMonarchRegexes, expandMonarchRegex } from "../src/domain/editor/monarch.ts";
import { regexProbeInputs, regexSafetyIssue, runRegexProbe } from "../src/domain/editor/regex-safety.ts";
import {
  PLUGIN_LANGUAGES_CHANGED_EVENT,
  attachMonacoLanguages,
  detectMonacoEditorLanguage,
  monacoLanguageIssues,
  monacoPluginLanguageIds,
  pluginLanguageAssociations,
  setMonacoLanguages,
} from "../src/services/monaco-languages.ts";
import { setRegexProbeRunner } from "../src/services/regex-probe.ts";

const repo = fileURLToPath(new URL("../../", import.meta.url));
const corpus = join(repo, "tools/verify/fixtures/contributions");
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

function contribution(pluginId, entries, issues = []) {
  return {
    pluginId,
    pluginName: pluginId,
    version: "1.0.0",
    source: "local",
    runtime: "declarative",
    activeInWorkspace: true,
    point: { fingerprint: `${pluginId}:${entries.length}`, entries, issues },
  };
}

const grammar = {
  keywords: ["let", "fn"],
  identifier: "[a-z_]\\w*",
  tokenizer: {
    root: [
      ["@identifier", { cases: { "@keywords": "keyword", "@default": "identifier" } }],
      { include: "@whitespace" },
      ['"', "string", "@string"],
    ],
    string: [['[^"\\\\]+', "string"], ['\\\\.', "string.escape"], ['"', "string", "@pop"]],
    whitespace: [["[ \\t\\r\\n]+", "white"], ["#.*$", "comment"]],
  },
};

function language(id, extra = {}) {
  return { id, aliases: [id.toUpperCase()], extensions: [], filenames: [], filenamePatterns: [], ...extra };
}

test("regex safety: accepts common grammar regexes", () => {
  for (const source of [
    "(_+\\d+)*",
    '"([^"\\\\]|\\\\.)*"',
    "[a-zA-Z_]\\w*",
    "\\d*\\.\\d+([eE][\\-+]?\\d+)?",
    "[a-z]\\w*(\\.[a-z]\\w*)*",
    "(?:[^\\\\\"]|\\\\.)*",
    "[=><!~?:&|+\\-*\\/\\^%]+",
    "0[xX][0-9a-fA-F']*[0-9a-fA-F]",
    "(?=a)b",
  ]) {
    assert.equal(regexSafetyIssue(source), undefined, source);
  }
  assert.equal(regexSafetyIssue("\\p{L}+", { unicode: true }), undefined);
});

test("regex safety: rejects catastrophic shapes and backreferences", () => {
  for (const source of ["(a+)+b", "(\\w+\\s?)*", "(a|aa)*", "(.*a)*", "((ab)*)*", "(?=(a+)+b)", "(A+)+", "([a-c]+)*c"]) {
    assert.match(regexSafetyIssue(source) ?? "", /catastrophic backtracking/, source);
  }
  assert.match(regexSafetyIssue("(a+)+", { ignoreCase: true }) ?? "", /catastrophic/);
  assert.match(regexSafetyIssue("(a)\\1") ?? "", /backreferences/);
  assert.match(regexSafetyIssue("(?<x>a)\\k<x>") ?? "", /backreferences/);
  assert.match(regexSafetyIssue("(") ?? "", /invalid regular expression/);
});

test("regex probe inputs are bounded and runnable", () => {
  const inputs = regexProbeInputs("^(?:[a-z]+=)");
  assert.ok(inputs.length > 0 && inputs.length < 64);
  assert.ok(inputs.every((input) => input.length <= 130 && input.endsWith("\u0001")));
  assert.ok(runRegexProbe([{ source: "^(?:[a-z]+=)", flags: "" }, { source: "(", flags: "" }]) > 0);
});

test("monarch: attribute expansion mirrors Monaco", () => {
  const lexer = { a: "x@b", b: "y", empty: "", list: ["q"] };
  assert.equal(expandMonarchRegex(lexer, "@a+@@z"), "(?:x(?:y))+@z");
  assert.equal(expandMonarchRegex(lexer, "@empty"), "");
  assert.throws(() => expandMonarchRegex(lexer, "@missing"), /unknown attribute/);
  assert.throws(() => expandMonarchRegex(lexer, "@list"), /must be a string/);
  const loop = { a: "@a@a@a@a@a@a@a@a" };
  assert.throws(() => expandMonarchRegex(loop, "@a"), /after attribute expansion/);
  const regexes = collectMonarchRegexes(grammar);
  assert.ok(regexes.some((item) => item.source === "^(?:(?:[a-z_]\\w*))"));
  const guarded = collectMonarchRegexes({ tokenizer: { root: [["\\w+", { cases: { "~(a+)+b": "bad", "@default": "x" } }]] } });
  assert.ok(guarded.some((item) => item.source === "^(a+)+b$"));
});

test("registry: validates entries, drops unsafe grammars, resolves duplicate ids by pluginId", () => {
  const bad = { tokenizer: { root: [["(a+)+b", "bad"]] } };
  const registry = buildLanguageRegistry([
    contribution("z.plugin", [language("acme", { grammar, extensions: [".acme"] })]),
    contribution("a.plugin", [
      language("acme", { grammar, extensions: [".acm"] }),
      language("unsafe", { grammar: bad }),
      { id: "json", aliases: [], extensions: [".acmejson"], filenames: [], filenamePatterns: [] },
      { id: "Bad Id" },
      { id: "json", aliases: [], extensions: [], filenames: [], filenamePatterns: [], grammar },
    ], [{ entry: 9, message: "host skipped" }]),
  ]);
  const ids = registry.languages.map((item) => `${item.pluginId}:${item.id}`);
  assert.deepEqual(ids, ["a.plugin:acme", "a.plugin:unsafe", "a.plugin:json"]);
  assert.equal(registry.languages[1].grammar, undefined);
  assert.equal(registry.languages[2].associationOnly, true);
  const codes = registry.issues.map((item) => `${item.pluginId}:${item.code ?? item.stage}`);
  assert.deepEqual(codes.sort(), [
    "a.plugin:grammarRejected",
    "a.plugin:host",
    "a.plugin:malformed",
    "a.plugin:malformed",
    "z.plugin:duplicateId",
  ]);
});

test("associations: built-ins and earlier plugins win", () => {
  const registry = buildLanguageRegistry([
    contribution("b.plugin", [language("bee", { extensions: [".shared", ".bee"] })]),
    contribution("a.plugin", [language("ay", { extensions: [".ts", ".shared"], filenames: ["Dockerfile", "Ayfile"] })]),
  ]);
  const builtIns = [
    { id: "typescript", extensions: [".ts"] },
    { id: "dockerfile", filenames: ["Dockerfile"] },
    { id: "plaintext", extensions: [".txt"] },
  ];
  const { definitions, issues } = resolvePluginLanguageAssociations(
    registry.languages,
    builtIns,
    new Set(["typescript", "dockerfile", "plaintext", "ay", "bee"]),
  );
  assert.deepEqual(definitions.map((item) => [item.id, item.extensions, item.filenames]), [
    ["bee", [".bee"], []],
    ["ay", [".shared"], ["Ayfile"]],
  ]);
  assert.equal(issues.filter((item) => item.code === "associationConflict").length, 3);
});

test("install preview summary lists new languages and built-in associations", () => {
  assert.deepEqual(languagePreviewSummary({
    languages: [
      { id: "acme", aliases: ["Acme"], extensions: [".acme"], filenames: ["Acmefile"] },
      { id: "ini", extensions: [".acmerc"] },
      { id: "Bad" },
    ],
  }), { languages: ["acme (.acme, Acmefile)"], associations: ["ini (.acmerc)"] });
  assert.deepEqual(languagePreviewSummary(undefined), { languages: [], associations: [] });
});

function stubMonaco() {
  const languages = [
    { id: "plaintext", extensions: [".txt"] },
    { id: "typescript", extensions: [".ts"] },
    { id: "json", extensions: [".json"] },
    { id: "ini", extensions: [".ini"] },
  ];
  const tokenizers = new Map();
  const configurations = new Map();
  const monaco = {
    languages: {
      getLanguages: () => languages.map((item) => ({ ...item })),
      register: (definition) => languages.push(definition),
      setMonarchTokensProvider: (id, definition) => {
        // Compile with the real Monarch compiler so the grammar is checked like in the app.
        compileMonarch(id, definition);
        const entry = { definition, disposed: false };
        tokenizers.set(id, entry);
        return { dispose: () => { entry.disposed = true; tokenizers.delete(id); } };
      },
      setLanguageConfiguration: (id, configuration) => {
        const entry = { configuration, disposed: false };
        configurations.set(id, entry);
        return { dispose: () => { entry.disposed = true; configurations.delete(id); } };
      },
    },
  };
  return { monaco, languages, tokenizers, configurations };
}

test("monaco stage: registers ids, tokenizers after the probe, associations after built-ins", async () => {
  const events = [];
  globalThis.window = new EventTarget();
  globalThis.window.addEventListener(PLUGIN_LANGUAGES_CHANGED_EVENT, () => events.push(1));
  const probed = [];
  setRegexProbeRunner(async (cases) => {
    probed.push(cases.length);
    return cases.some((item) => item.source.includes("slow")) ? "timeout" : "ok";
  });
  try {
    const stub = stubMonaco();
    const detach = attachMonacoLanguages(stub.monaco);
    const configuration = { comments: { lineComment: "#" }, autoClosingPairs: [["(", ")"], { open: '"', close: '"', notIn: ["string"] }], wordPattern: "[a-z]+", indentationRules: { increaseIndentPattern: "\\{$", decreaseIndentPattern: "^\\}" } };
    const slow = { tokenizer: { root: [["slow", "x"]] } };
    const registry = buildLanguageRegistry([
      contribution("acme", [
        language("acme", { grammar, configuration, extensions: [".acme", ".ts"], filenamePatterns: ["**/conf/*.acme.d", "*.acme.ts", "*.acme.txt"] }),
        language("slowlang", { grammar: slow, extensions: [".slow"] }),
        { id: "ini", aliases: [], extensions: [".acmerc"], filenames: [], filenamePatterns: [] },
        language("typescript", {}),
      ]),
    ]);
    setMonacoLanguages(registry.languages);
    await flush();
    await flush();
    assert.ok(stub.languages.some((item) => item.id === "acme" && item.aliases[0] === "ACME" && !item.extensions));
    assert.ok(stub.tokenizers.has("acme"));
    assert.ok(stub.configurations.get("acme").configuration.wordPattern instanceof RegExp);
    assert.deepEqual(stub.configurations.get("acme").configuration.autoClosingPairs[0], { open: "(", close: ")" });
    assert.ok(!stub.tokenizers.has("slowlang"), "timed-out grammar has no tokenizer");
    assert.deepEqual(monacoPluginLanguageIds(), ["acme", "slowlang"]);
    const issues = monacoLanguageIssues().map((item) => item.code).sort();
    assert.deepEqual(issues, ["associationConflict", "probeTimeout", "registerFailed"]);
    assert.ok(events.length >= 1);

    assert.equal(detectMonacoEditorLanguage(stub.monaco, "/p/a.acme", ""), "acme");
    assert.equal(detectMonacoEditorLanguage(stub.monaco, "/p/a.ts", ""), "typescript");
    assert.equal(detectMonacoEditorLanguage(stub.monaco, "/p/.acmerc", ""), "ini");
    assert.equal(detectMonacoEditorLanguage(stub.monaco, "/p/conf/x.acme.d", ""), "acme");
    assert.equal(detectMonacoEditorLanguage(stub.monaco, "/p/notes.txt", ""), "plaintext");
    assert.equal(detectMonacoEditorLanguage(stub.monaco, "/p/unknown.zzz", ""), "plaintext");
    // A plugin compound suffix beats the shorter built-in extension; the plain extension stays built-in.
    assert.equal(detectMonacoEditorLanguage(stub.monaco, "/p/a.acme.ts", ""), "acme");
    assert.equal(detectMonacoEditorLanguage(stub.monaco, "/p/b.ts", ""), "typescript");
    assert.equal(detectMonacoEditorLanguage(stub.monaco, "/p/a.acme.txt", ""), "plaintext");

    // Dispose-then-apply in one tick: unchanged grammar keeps its tokenizer.
    const before = stub.tokenizers.get("acme");
    setMonacoLanguages([]);
    setMonacoLanguages(registry.languages);
    await flush();
    assert.equal(stub.tokenizers.get("acme"), before);
    assert.equal(before.disposed, false);

    // Removal disposes tokenizers/configurations and associations; the id stays registered.
    setMonacoLanguages([]);
    await flush();
    assert.equal(before.disposed, true);
    assert.equal(stub.configurations.size, 0);
    assert.deepEqual(pluginLanguageAssociations(), []);
    assert.equal(detectMonacoEditorLanguage(stub.monaco, "/p/a.acme", ""), "plaintext");
    assert.ok(stub.languages.some((item) => item.id === "acme"));

    // Re-adding reuses the registered id without registering it twice.
    setMonacoLanguages(registry.languages);
    await flush();
    await flush();
    assert.equal(stub.languages.filter((item) => item.id === "acme").length, 1);
    assert.ok(stub.tokenizers.has("acme"));
    detach();
    assert.equal(stub.tokenizers.size, 0);
    setMonacoLanguages([]);
    await flush();
  } finally {
    setRegexProbeRunner();
    delete globalThis.window;
  }
});

test("languages: consumers load Monaco lazily (type-only imports)", async () => {
  for (const file of [
    "src/services/contributions/languages.ts",
    "src/services/monaco-languages.ts",
    "src/services/regex-probe.ts",
    "src/services/workers/regex-probe.worker.ts",
    "src/domain/editor/language-contributions.ts",
    "src/domain/editor/monarch.ts",
    "src/domain/editor/regex-safety.ts",
  ]) {
    const source = await readFile(fileURLToPath(new URL(`../${file}`, import.meta.url)), "utf8");
    const eager = [...source.matchAll(/^\s*import\s+(?!type\s)[^;]*?from\s+["']([^"']+)["']/gm)]
      .map((match) => match[1])
      .filter((specifier) => /monaco-runtime|monaco-editor|monaco-keymap/.test(specifier));
    assert.deepEqual(eager, [], file);
    assert.doesNotMatch(source, /import\(\s*["']@\/services\/monaco-runtime/, file);
  }
});

// The host inlines grammar/configuration files into the entry; mirror that.
async function corpusContribution(name) {
  const dir = join(corpus, name);
  const manifest = JSON.parse(await readFile(join(dir, "plugin.json"), "utf8"));
  const entries = [];
  for (const entry of manifest.contributes.languages) {
    const value = {
      id: entry.id,
      aliases: entry.aliases ?? [],
      extensions: entry.extensions ?? [],
      filenames: entry.filenames ?? [],
      filenamePatterns: entry.filenamePatterns ?? [],
    };
    for (const field of ["grammar", "configuration"]) {
      if (typeof entry[field] !== "string") continue;
      value[`${field}Path`] = entry[field];
      value[field] = JSON.parse(await readFile(join(dir, entry[field]), "utf8"));
    }
    entries.push(value);
  }
  return contribution(manifest.id, entries);
}

test("fixture corpus: frontend layer for languages", async () => {
  const expectations = JSON.parse(await readFile(join(corpus, "expectations.json"), "utf8"));
  let checked = 0;
  for (const [name, expected] of Object.entries(expectations.cases)) {
    if (!name.includes("/languages-")) continue;
    const valid = name.startsWith("valid/");
    if (!valid && !(expected.rejectedBy ?? []).includes("frontend")) continue;
    const contributionValue = await corpusContribution(name);
    const registry = buildLanguageRegistry([contributionValue]);
    if (valid) {
      assert.deepEqual(registry.issues, [], name);
      for (const item of registry.languages) {
        if (item.grammar) compileMonarch(item.id, structuredClone(item.grammar));
      }
    } else {
      assert.ok(registry.issues.some((issue) => issue.stage === "frontend"), `${name}: frontend accepted`);
    }
    checked += 1;
  }
  assert.ok(checked >= 3, `checked ${checked} language fixtures`);
});
