// languageFeatures definition / references / codeLens / inlayHints /
// documentSymbol: the frontend re-validation of host results
// (backend/plugin/language_feature_results.go), location targets
// under the document's project root, the passive failure gate, the
// inlayHints range window and the per-kind request shape.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import test from "node:test";
import { fileURLToPath } from "node:url";

// --- Stub: the provider invoker records the host arguments. -----------------
const invokeStub = `
  export async function invokeProviderCommand(command, args, parse) {
    const state = globalThis.__languageFeatureKindsTest;
    state.calls.push([command, JSON.parse(JSON.stringify(args)), Object.keys(args)]);
    return { ok: true, response: parse(state.response) };
  }
  export const providerErrorText = (error) => error.code;
`;
const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    const resolved = nextResolve(specifier, context);
    if (resolved.url.endsWith("/src/services/plugin-provider-invoke.ts")) return { url: dataModule(invokeStub), shortCircuit: true };
    return resolved;
  },
});

const results = await import("../src/domain/plugins/language-feature-results.ts");
const { LANGUAGE_FEATURE_LIMITS, languageFeatureRangeWindow } = await import("../src/domain/plugins/language-features.ts");
const service = await import("../src/services/plugin-language-feature-service.ts");
const {
  createPassiveFailureGate,
  locationTargetPath,
  parseCodeLensResponse,
  parseDefinitionResponse,
  parseDocumentSymbolResponse,
  parseInlayHintsResponse,
  parseReferencesResponse,
  projectRootForPath,
  LOCATION_SENSITIVE_FILENAMES,
} = results;

const contract = JSON.parse(readFileSync(new URL("../src/domain/plugins/contribution-points.json", import.meta.url), "utf8"));

const range = (startLine = 1, startColumn = 1, endLine = startLine, endColumn = startColumn + 4) => ({ startLine, startColumn, endLine, endColumn });
const repeat = (count, make) => Array.from({ length: count }, (_, index) => make(index));

test("language-feature-results: parsers mirror the host caps", () => {
  // Every kind: one past the cap is cut in order and truncated.
  const definition = parseDefinitionResponse({ locations: repeat(LANGUAGE_FEATURE_LIMITS.definitionLocationsMax + 1, (i) => ({ range: range(i + 1) })), dropped: 0, truncated: false });
  assert.equal(definition.locations.length, 20);
  assert.equal(definition.truncated, true);
  assert.equal(definition.locations[19].range.startLine, 20);
  const references = parseReferencesResponse({ locations: repeat(501, (i) => ({ range: range(i + 1) })), dropped: 2, truncated: false });
  assert.equal(references.locations.length, 500);
  assert.equal(references.truncated, true);
  assert.equal(references.dropped, 2, "host counts are kept");
  const lenses = parseCodeLensResponse({ lenses: repeat(101, (i) => ({ range: range(i + 1), title: `Run ${i}` })), dropped: 0, truncated: false });
  assert.equal(lenses.lenses.length, 100);
  assert.equal(lenses.truncated, true);
  const hints = parseInlayHintsResponse({ hints: repeat(301, (i) => ({ position: { line: i + 1, column: 1 }, label: ": int" })), dropped: 0, truncated: false });
  assert.equal(hints.hints.length, 300);
  assert.equal(hints.truncated, true);
  const symbols = parseDocumentSymbolResponse({ symbols: repeat(501, (i) => ({ name: `s${i}`, kind: "function", range: range(i + 1), selectionRange: range(i + 1) })), dropped: 0, truncated: false });
  assert.equal(symbols.symbols.length, 500);
  assert.equal(symbols.truncated, true);

  // Lens titles: codicon syntax, multi-line and format characters drop the lens.
  const titled = parseCodeLensResponse({
    lenses: [
      { range: range(), title: "$(play) Run" },
      { range: range(), title: "Run\nnow" },
      { range: range(), title: "Run 👨\u200d👩" },
      { range: range(), title: "Run", command: "acme.run", args: { name: "build" } },
      { range: range(), title: "Run", args: { name: "build" } },
      { range: range(), title: "Run", command: "acme.run", args: { nested: { a: 1 } } },
      { range: range(), title: "Plain" },
    ],
    dropped: 0,
    truncated: false,
  });
  assert.deepEqual(titled.lenses.map((lens) => lens.title), ["Run", "Plain"]);
  assert.deepEqual(titled.lenses[0], { range: range(), title: "Run", command: "acme.run", args: { name: "build" } });
  assert.equal(titled.dropped, 5);

  // Inlay hints: unknown kind and multi-line labels drop the hint; kind and padding are kept.
  const kinds = parseInlayHintsResponse({
    hints: [
      { position: { line: 1, column: 3 }, label: ": int", kind: "type", paddingLeft: true },
      { position: { line: 1, column: 3 }, label: "a", kind: "widget" },
      { position: { line: 1, column: 3 }, label: "a\nb" },
      { position: { line: 0, column: 3 }, label: "a" },
      { position: { line: 2, column: 1 }, label: "name:", kind: "parameter", paddingRight: true },
    ],
    dropped: 1,
    truncated: false,
  });
  assert.deepEqual(kinds.hints, [
    { position: { line: 1, column: 3 }, label: ": int", kind: "type", paddingLeft: true },
    { position: { line: 2, column: 1 }, label: "name:", kind: "parameter", paddingRight: true },
  ]);
  assert.equal(kinds.dropped, 4);
  const inRange = parseInlayHintsResponse({ hints: [{ position: { line: 4, column: 1 }, label: "a" }, { position: { line: 9, column: 1 }, label: "b" }] }, { startLine: 3, endLine: 5 });
  assert.deepEqual(inRange.hints.map((hint) => hint.label), ["a"]);
  assert.equal(inRange.dropped, 1);

  // Locations: definition keeps a selectionRange inside range; references take none; bad ranges drop.
  const located = parseDefinitionResponse({
    locations: [
      { path: "src/b.ts", range: range(3, 1, 5, 2), selectionRange: range(3, 2, 3, 6) },
      { range: range(3, 1, 3, 4), selectionRange: range(4, 1, 4, 2) },
      { range: { startLine: 2, startColumn: 5, endLine: 2, endColumn: 1 } },
      { range: { startLine: 1, startColumn: 1, endLine: 1 } },
      { range: range(1, LANGUAGE_FEATURE_LIMITS.columnMax + 1) },
      { range: range(), uri: "file:///etc/passwd" },
    ],
  });
  assert.deepEqual(located.locations, [{ path: "src/b.ts", range: range(3, 1, 5, 2), selectionRange: range(3, 2, 3, 6) }]);
  assert.equal(located.dropped, 5);
  assert.equal(parseReferencesResponse({ locations: [{ range: range(), selectionRange: range() }, { range: range() }] }).locations.length, 1);
  // Lines and columns are JSON integers, as Go's int decode.
  for (const bad of [1.5, "1", true, Number.NaN, Number.POSITIVE_INFINITY, 2 ** 53, -0, 0]) {
    assert.equal(parseReferencesResponse({ locations: [{ range: { ...range(), startLine: bad } }] }).locations.length, 0, String(bad));
    assert.equal(parseInlayHintsResponse({ hints: [{ position: { line: 1, column: bad }, label: "a" }] }).hints.length, 0, String(bad));
  }

  // Location paths: the host rules (escapes, characters, sensitive and VCS files) drop the item.
  const escapes = [
    "/etc/passwd", "../x", "C:/x", "a\\b", "https://x", "a/./b", "a".repeat(1025), "src/", "src//a.ts", ".", "",
    "src/a%20b.ts", "src/a.ts?x", "src/a.ts#L1", "~/x", "$HOME/x", "\uff0e\uff0e/x", "src\uff0fa.ts", "src\u2215a.ts",
    "src/a\u200b.ts", "src/a\u2028.ts", "src/\u202egol.exe", "src/a\u00ad.ts", ".env", "config/.env.local", "home/ID_RSA",
    ".git/config", "sub/.GIT/HEAD", "x/.hg/store", "aws/credentials",
  ];
  const paths = parseReferencesResponse({ locations: [...escapes.map((path) => ({ path, range: range() })), ...["src/a.ts", "docs/读我.md", "src/~backup.ts", "pkg/$x.ts"].map((path) => ({ path, range: range() }))] });
  assert.deepEqual(paths.locations.map((location) => location.path), ["src/a.ts", "docs/读我.md", "src/~backup.ts", "pkg/$x.ts"]);
  assert.equal(paths.dropped, escapes.length);
  assert.deepEqual([...LOCATION_SENSITIVE_FILENAMES], contract.points.fileTemplates.sensitiveFilenames, "mirrors the contract");

  // Symbols: an invalid node drops with its children (dropped + 1); depth past the cap is truncated.
  const nested = (depth) => (depth === 0 ? [] : [{ name: `d${depth}`, kind: "module", range: range(1, 1, 50, 1), children: nested(depth - 1) }]);
  const deep = parseDocumentSymbolResponse({ symbols: nested(LANGUAGE_FEATURE_LIMITS.documentSymbolDepthMax + 1) });
  let depth = 0;
  for (let level = deep.symbols; level?.length; level = level[0].children) depth += 1;
  assert.equal(depth, 8);
  assert.equal(deep.truncated, true);
  const pruned = parseDocumentSymbolResponse({
    symbols: [
      { name: "bad", kind: "widget", range: range(1, 1, 9, 1), children: [{ name: "child", kind: "field", range: range(2) }] },
      { name: "ok", detail: "  ", kind: "class", range: range(10, 1, 20, 1), selectionRange: range(10, 7, 10, 9), children: [{ name: "m", detail: "()", kind: "method", range: range(11) }] },
      { name: "outside", kind: "class", range: range(30), selectionRange: range(31) },
      { name: "two\nlines", kind: "class", range: range(40) },
    ],
  });
  assert.deepEqual(pruned.symbols, [{
    name: "ok", kind: "class", range: range(10, 1, 20, 1), selectionRange: range(10, 7, 10, 9),
    children: [{ name: "m", detail: "()", kind: "method", range: range(11), selectionRange: range(11) }],
  }]);
  assert.equal(pruned.dropped, 3);

  // Not an object, or the list missing: empty results.
  for (const value of [undefined, null, "x", 7, [], { locations: "x" }]) {
    assert.deepEqual(parseDefinitionResponse(value), { locations: [], dropped: 0, truncated: false });
    assert.deepEqual(parseReferencesResponse(value), { locations: [], dropped: 0, truncated: false });
    assert.deepEqual(parseCodeLensResponse(value), { lenses: [], dropped: 0, truncated: false });
    assert.deepEqual(parseInlayHintsResponse(value), { hints: [], dropped: 0, truncated: false });
    assert.deepEqual(parseDocumentSymbolResponse(value), { symbols: [], dropped: 0, truncated: false });
  }
});

test("language-feature-results: location paths stay under the document's project root", () => {
  assert.equal(projectRootForPath("/r/a/b.ts", ["/r", "/r/a"]), "/r/a");
  assert.equal(projectRootForPath("/r/a/b.ts", ["/r/a", "/r"]), "/r/a");
  assert.equal(projectRootForPath("/r/ab/c.ts", ["/r/a"]), undefined, "a sibling with the same prefix is not inside");
  assert.equal(projectRootForPath("/r", ["/r"]), undefined, "the root itself is not a document");
  assert.equal(projectRootForPath("/x/a.ts", ["/r"]), undefined);
  assert.equal(projectRootForPath("/x/a.ts", ["/"]), undefined, "the filesystem root is never a project root");
  assert.equal(projectRootForPath("c:/Work/App/src/a.ts", ["C:\\work\\app"]), "C:\\work\\app", "drive paths compare case-insensitively");
  assert.equal(projectRootForPath("/r/a/b.ts", ["/r/a/"]), "/r/a/");
  assert.equal(projectRootForPath("C:/a.ts", ["C:\\"]), undefined, "a drive root is never a project root");
  assert.equal(projectRootForPath("c:/a.ts", ["C:/"]), undefined);

  for (const escape of ["../x", "/etc/x", "C:/x", "a\\b", "https://x", "a/./b", ".git/config", ".env"]) {
    assert.equal(locationTargetPath("/r", "src/a.ts", escape), undefined, escape);
  }
  assert.deepEqual(locationTargetPath("/r", "src/a.ts", undefined), { sameDocument: true });
  assert.deepEqual(locationTargetPath("/r", "src/a.ts", "src/a.ts"), { sameDocument: true });
  assert.deepEqual(locationTargetPath("/r", "src/a.ts", "lib/b.ts"), { sameDocument: false, absolutePath: "/r/lib/b.ts" });
  assert.deepEqual(locationTargetPath("/r/", "src/a.ts", "lib/b.ts"), { sameDocument: false, absolutePath: "/r/lib/b.ts" });
  assert.deepEqual(locationTargetPath("C:\\r", "src/a.ts", "lib/b.ts"), { sameDocument: false, absolutePath: "C:/r/lib/b.ts" });
  assert.equal(locationTargetPath("relative", "src/a.ts", "lib/b.ts"), undefined, "the root must be absolute");
  assert.equal(locationTargetPath("/", "src/a.ts", "lib/b.ts"), undefined);
});

test("language-feature-results: passive failure gate", () => {
  let now = 1_000;
  const gate = createPassiveFailureGate({ now: () => now });
  const key = "acme\u0000words\u0000codeLens";
  assert.equal(gate.allowed(key), true);
  gate.record(key, "timeout");
  gate.record(key, "timeout");
  assert.equal(gate.allowed(key), true);
  gate.record(key, "timeout");
  assert.equal(gate.allowed(key), false);
  assert.deepEqual(gate.suspensions(), [key]);
  assert.equal(gate.allowed("acme\u0000words\u0000inlayHints"), true, "keys are independent");
  now += 59_999;
  assert.equal(gate.allowed(key), false);
  now += 1;
  assert.equal(gate.allowed(key), true);
  assert.deepEqual(gate.suspensions(), []);
  // After a suspension, three more failures suspend again.
  gate.record(key, "plugin_error");
  gate.record(key, "invalid_result");
  assert.equal(gate.allowed(key), true);
  gate.record(key, "too_large");
  assert.equal(gate.allowed(key), false);
  gate.reset();
  assert.equal(gate.allowed(key), true);
  assert.deepEqual(gate.suspensions(), []);

  // busy / cancelled (and the other non-plugin codes) never count; ok clears the run.
  for (const code of ["busy", "cancelled", "unavailable", "not_found", "busy", "cancelled"]) gate.record(key, code);
  gate.record(key, "timeout");
  gate.record(key, "timeout");
  gate.record(key, "busy");
  gate.record(key, "ok");
  gate.record(key, "timeout");
  gate.record(key, "timeout");
  assert.equal(gate.allowed(key), true, "ok cleared the earlier failures");
  gate.record(key, "timeout");
  assert.equal(gate.allowed(key), false);

  const defaults = createPassiveFailureGate({ now: () => now });
  for (let i = 0; i < LANGUAGE_FEATURE_LIMITS.passiveFailuresMax; i++) defaults.record("k", "timeout");
  assert.equal(defaults.allowed("k"), false);
  // Calls in flight when the key was suspended neither extend nor end the suspension.
  now += 30_000;
  for (let i = 0; i < 5; i++) defaults.record("k", "timeout");
  defaults.record("k", "ok");
  assert.equal(defaults.allowed("k"), false);
  now += 30_000;
  assert.equal(defaults.allowed("k"), true, "suspended for passiveSuspendMs from the third failure only");
  defaults.record("k", "timeout");
  defaults.record("k", "timeout");
  assert.equal(defaults.allowed("k"), true, "the late failures did not count toward the next run");
  defaults.record("k", "timeout");
  assert.equal(defaults.allowed("k"), false);
  now += LANGUAGE_FEATURE_LIMITS.passiveSuspendMs;
  assert.equal(defaults.allowed("k"), true);
});

test("languageFeatures: range window covers the visible lines", () => {
  const small = ["a", "b", "c"];
  assert.deepEqual(languageFeatureRangeWindow(small, 2, 3), { text: "a\nb\nc", startLine: 1, range: { startLine: 2, endLine: 3 } });
  assert.equal(languageFeatureRangeWindow(small, 0, 2), undefined);
  assert.equal(languageFeatureRangeWindow(small, 3, 2), undefined);
  assert.equal(languageFeatureRangeWindow(small, 2, 4), undefined);

  // 200 KiB: 2 000 lines of 99 bytes plus newline.
  const big = repeat(2_000, (i) => `${String(i + 1).padStart(6, "0")} ${"x".repeat(92)}`);
  const window = languageFeatureRangeWindow(big, 1_000, 1_040);
  assert.ok(window);
  const windowLines = window.text.split("\n");
  const last = window.startLine + windowLines.length - 1;
  assert.ok(Buffer.byteLength(window.text) <= LANGUAGE_FEATURE_LIMITS.textMaxBytes);
  assert.ok(Buffer.byteLength(window.text) > LANGUAGE_FEATURE_LIMITS.textMaxBytes - 200, "the window is filled");
  assert.ok(window.startLine <= 1_000 && last >= 1_040);
  assert.deepEqual(window.range, { startLine: 1_000, endLine: 1_040 });
  assert.equal(windowLines[0], big[window.startLine - 1]);
  assert.ok(window.startLine < 1_000 && last > 1_040, "context on both sides");
  // At the top of the document the window extends downward.
  const top = languageFeatureRangeWindow(big, 1, 10);
  assert.equal(top.startLine, 1);
  assert.ok(Buffer.byteLength(top.text) <= LANGUAGE_FEATURE_LIMITS.textMaxBytes);

  // A visible region larger than the limit: the range is cut to the window.
  const huge = languageFeatureRangeWindow(big, 100, 1_900);
  const hugeLast = huge.startLine + huge.text.split("\n").length - 1;
  assert.equal(huge.startLine, 100);
  assert.ok(Buffer.byteLength(huge.text) <= LANGUAGE_FEATURE_LIMITS.textMaxBytes);
  assert.deepEqual(huge.range, { startLine: 100, endLine: hugeLast });
  assert.ok(hugeLast < 1_900);
  // The first visible line alone does not fit: no window.
  assert.equal(languageFeatureRangeWindow(["x".repeat(70_000), "y"], 1, 2), undefined);
  assert.deepEqual(languageFeatureRangeWindow(["a", "b", "c", "d"], 2, 3, 3), { text: "b\nc", startLine: 2, range: { startLine: 2, endLine: 3 } });
});

test("plugin-language-feature-service: requests carry only kind fields", async () => {
  const state = { calls: [], response: {} };
  globalThis.__languageFeatureKindsTest = state;
  const provider = { pluginId: "acme", id: "words" };
  const base = { languageId: "markdown", fileName: "a.md", relativePath: "docs/a.md", text: "a\nb", startLine: 1, locale: "en" };
  const position = { line: 1, column: 1 };
  const everything = { ...base, position, range: { startLine: 1, endLine: 2 }, includeDeclaration: true, triggerCharacter: "@", projectRoot: "/r" };

  state.response = { contents: ["x"] };
  await service.requestHover(provider, everything);
  state.response = { items: [] };
  await service.requestCompletion(provider, everything);
  state.response = { locations: [{ path: "docs/b.md", range: range() }] };
  const definition = await service.requestDefinition(provider, everything);
  const references = await service.requestReferences(provider, { ...everything, includeDeclaration: false });
  await service.requestDefinition(provider, { ...everything, relativePath: undefined });
  state.response = { lenses: [{ range: range(), title: "Run" }] };
  const lenses = await service.requestCodeLens(provider, everything);
  state.response = { hints: [] };
  await service.requestInlayHints(provider, everything);
  state.response = { symbols: [] };
  await service.requestDocumentSymbols(provider, everything);

  assert.ok(state.calls.every(([command]) => command === "plugin_host_language_feature"));
  const sent = state.calls.map(([, args]) => args);
  const keys = sent.map((args) => [args.feature, Object.keys(args).filter((key) => !["pluginId", "providerId", "feature", ...Object.keys(base)].includes(key)).sort()]);
  assert.deepEqual(keys, [
    ["hover", ["position"]],
    ["completion", ["position", "triggerCharacter"]],
    ["definition", ["position", "projectRoot"]],
    ["references", ["includeDeclaration", "position", "projectRoot"]],
    ["definition", ["position"]],
    ["codeLens", []],
    ["inlayHints", ["range"]],
    ["documentSymbol", []],
  ]);
  assert.equal(sent[3].includeDeclaration, false);
  assert.equal("relativePath" in sent[4], false, "projectRoot is sent only with relativePath");
  for (const args of sent) assert.equal(args.pluginId, "acme");
  // hover keeps its pre-existing argument shape.
  assert.deepEqual([...state.calls[0][2]].sort(), ["pluginId", "providerId", "feature", ...Object.keys(base), "position"].sort());
  assert.equal(definition.ok && definition.response.locations[0].path, "docs/b.md");
  assert.equal(references.ok && references.response.locations.length, 1);
  assert.equal(lenses.ok && lenses.response.lenses[0].title, "Run");

  // Whole-document kinds refuse a window without reaching the host.
  const before = state.calls.length;
  const refused = await service.requestCodeLens(provider, { ...base, startLine: 5 });
  assert.equal(refused.ok, false);
  assert.equal(refused.ok || refused.error.code, "invalid_request");
  assert.equal(state.calls.length, before);
});

test("plugin-language-feature-service: a root the host would refuse is left out", async () => {
  const state = { calls: [], response: { locations: [{ range: range() }] } };
  globalThis.__languageFeatureKindsTest = state;
  const provider = { pluginId: "acme", id: "words" };
  const request = { languageId: "go", fileName: "a.go", relativePath: "src/a.go", text: "a", startLine: 1, position: { line: 1, column: 1 }, locale: "en" };
  const zwj = String.fromCodePoint(0x200d);
  const refused = [
    `/Users/me/${String.fromCodePoint(0x1f469)}${zwj}${String.fromCodePoint(0x1f4bb)} work`,
    `/work/a${String.fromCodePoint(0x202e)}b`,
    `/work/a${String.fromCodePoint(0x2028)}b`,
    "/work/a\nb",
    `/work/${String.fromCharCode(0xd800)}`,
    `/${"x".repeat(4096)}`,
    "relative/root",
    "C:relative",
    "",
  ];
  for (const projectRoot of refused) {
    const result = await service.requestDefinition(provider, { ...request, projectRoot });
    await service.requestReferences(provider, { ...request, projectRoot, includeDeclaration: true });
    assert.equal(result.ok && result.response.locations.length, 1, "same-document locations still arrive");
  }
  assert.equal(state.calls.length, refused.length * 2);
  for (const [, args] of state.calls) assert.equal("projectRoot" in args, false, JSON.stringify(args).slice(0, 80));

  state.calls = [];
  const accepted = ["/work/app", `/${"x".repeat(4095)}`, "C:\\work\\app", "D:/work", "/Users/me/读我 work", `/Users/me/${String.fromCodePoint(0x1f469)} work`];
  for (const projectRoot of accepted) await service.requestDefinition(provider, { ...request, projectRoot });
  assert.deepEqual(state.calls.map(([, args]) => args.projectRoot), accepted);
  assert.equal(results.isHostProjectRoot("/work/app"), true);
  assert.equal(results.isHostProjectRoot("work/app"), false);
});

test("language-feature-results: stays pure and off the entry chunk's static graph", async () => {
  const { dirname, join, resolve } = await import("node:path");
  const { statSync } = await import("node:fs");
  const srcRoot = fileURLToPath(new URL("../src/", import.meta.url));
  const source = await readFile(join(srcRoot, "domain/plugins/language-feature-results.ts"), "utf8");
  // Value imports only from pure domain modules (no Vue, Monaco, IPC or services).
  const valueImports = [...source.matchAll(/^import\s+(?!type[\s{])[^;]*?from\s+"([^"]+)"/gm)].map((match) => match[1]);
  assert.ok(valueImports.length > 0);
  for (const specifier of valueImports) assert.match(specifier, /^@\/domain\//, specifier);
  const staticImport = /(?:^|[\n;])\s*(?:import|export)\s+(?!type[\s{])(?:[^'"`;]*?\sfrom\s*)?["']([^"']+)["']/g;
  const resolveSpecifier = (specifier, from) => {
    let base;
    if (specifier.startsWith("@/")) base = join(srcRoot, specifier.slice(2));
    else if (specifier.startsWith(".")) base = resolve(dirname(from), specifier);
    else return null;
    for (const suffix of ["", ".ts", ".vue", ".js", ".mjs", "/index.ts"]) {
      const candidate = base + suffix;
      if (statSync(candidate, { throwIfNoEntry: false })?.isFile()) return candidate;
    }
    return null;
  };
  const seen = new Set();
  const queue = [join(srcRoot, "main.ts")];
  while (queue.length) {
    const file = queue.pop();
    if (seen.has(file)) continue;
    seen.add(file);
    if (!/\.(ts|vue|js|mjs)$/.test(file)) continue;
    let text = await readFile(file, "utf8");
    if (file.endsWith(".vue")) text = [...text.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)].map((match) => match[1]).join("\n");
    for (const match of text.matchAll(staticImport)) {
      const target = resolveSpecifier(match[1], file);
      if (target && !seen.has(target)) queue.push(target);
    }
  }
  const reached = [...seen].map((file) => file.slice(srcRoot.length));
  assert.ok(reached.includes("App.vue") && reached.length > 50, `entry graph too small (${reached.length})`);
  for (const lazy of [
    "domain/plugins/language-feature-results.ts",
    "domain/plugins/language-features.ts",
    "services/plugin-language-feature-service.ts",
    "services/monaco-language-features.ts",
    "services/plugin-code-lens-commands.ts",
    "domain/plugins/code-lens-commands.ts",
  ]) {
    assert.ok(!reached.includes(lazy), `${lazy} is statically reachable from main.ts`);
  }
});
