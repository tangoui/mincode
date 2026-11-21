import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  collectJsonSchemaRegexes,
  collectJsonValidationEntries,
  jsonSchemaPatternMatches,
  jsonValidationPatternIssue,
  jsonValidationPreviewSummary,
  matchJsonSchemas,
  pluginJsonSchemaUri,
} from "../src/domain/plugins/json-validation.ts";
import {
  attachMonacoJsonSchemas,
  flushMonacoJsonSchemas,
  jsonSchemaTargetForPath,
  monacoJsonSchemaIssues,
  setJsonSchemaFetcher,
  setMonacoJsonSchemas,
} from "../src/services/monaco-json-schemas.ts";
import { setRegexProbeRunner } from "../src/services/regex-probe.ts";

const repo = fileURLToPath(new URL("../../", import.meta.url));
const corpus = join(repo, "tools/verify/fixtures/contributions");
const KEY_A = "a".repeat(64);
const KEY_B = "b".repeat(64);
const encoder = new TextEncoder();

function contribution(pluginId, entries, extra = {}) {
  return {
    pluginId,
    pluginName: pluginId,
    version: "1.0.0",
    source: "local",
    runtime: "declarative",
    activeInWorkspace: true,
    point: { fingerprint: `${pluginId}:${entries.length}`, entries, issues: [] },
    ...extra,
  };
}

function uri(scheme, path) {
  return { scheme, path, toString: () => `${scheme}://${path}` };
}

function stubMonaco(initialSchemas = []) {
  const models = [];
  const listeners = { create: new Set(), dispose: new Set(), language: new Set() };
  const writes = [];
  let diagnosticsOptions = { validate: true, allowComments: true, schemas: initialSchemas };
  const on = (set) => (listener) => {
    set.add(listener);
    return { dispose: () => set.delete(listener) };
  };
  const monaco = {
    editor: {
      getModels: () => models,
      onDidCreateModel: on(listeners.create),
      onWillDisposeModel: on(listeners.dispose),
      onDidChangeModelLanguage: on(listeners.language),
    },
    mincodeJsonDefaults: {
      get diagnosticsOptions() {
        return diagnosticsOptions;
      },
      setDiagnosticsOptions(options) {
        diagnosticsOptions = options;
        writes.push(options);
      },
    },
  };
  return {
    monaco,
    writes,
    listeners,
    options: () => diagnosticsOptions,
    addModel(scheme, path, language = "json") {
      const model = { uri: uri(scheme, path), isDisposed: () => false, getLanguageId: () => language };
      models.push(model);
      return model;
    },
  };
}

test("jsonValidation: fileMatch patterns mirror the host rules", () => {
  assert.equal(jsonValidationPatternIssue(".mcp.json"), undefined);
  assert.equal(jsonValidationPatternIssue("**/.cursor/mcp.json"), undefined);
  assert.equal(jsonValidationPatternIssue("tsconfig.*.json"), undefined);
  assert.match(jsonValidationPatternIssue("*.json") ?? "", /too broad/);
  assert.match(jsonValidationPatternIssue("**/*.json") ?? "", /too broad/);
  assert.match(jsonValidationPatternIssue("a?.json") ?? "", /too broad/);
  assert.match(jsonValidationPatternIssue("/abs/app.json") ?? "", /empty segments/);
  assert.match(jsonValidationPatternIssue("../app.json") ?? "", /\.\. segments/);
  assert.match(jsonValidationPatternIssue("config.yaml") ?? "", /\.json/);
  assert.match(jsonValidationPatternIssue("con fig.json") ?? "", /letters/);
});

test("jsonValidation: entries are re-validated and ordered by plugin", () => {
  const result = collectJsonValidationEntries([
    contribution("z.plugin", [{ id: "schemas/z.json", fileMatch: ["zeta.json"], schema: KEY_B }]),
    contribution("a.plugin", [
      { id: "schemas/a.json", fileMatch: ["alpha.json"], schema: KEY_A, title: "Alpha", titleLocalized: { zh: "阿尔法" } },
      { id: "schemas/bad.json", fileMatch: ["*.json"], schema: KEY_A },
      { id: "schemas/nokey.json", fileMatch: ["alpha.json"], schema: "not-a-key" },
      { id: "schemas/a.json", fileMatch: ["alpha.json"], schema: KEY_A },
    ]),
    contribution("off.plugin", [{ id: "s.json", fileMatch: ["other.json"], schema: KEY_A }], { activeInWorkspace: false }),
  ]);
  assert.deepEqual(result.entries.map((entry) => `${entry.pluginId}:${entry.id}`), ["a.plugin:schemas/a.json", "z.plugin:schemas/z.json"]);
  assert.deepEqual(result.entries[0].titleLocalized, { zh: "阿尔法" });
  assert.equal(result.issues.length, 3);
  assert.ok(result.issues.every((issue) => issue.stage === "frontend" && issue.pluginId === "a.plugin"));
  assert.match(result.issues.map((issue) => issue.message).join("\n"), /too broad[\s\S]*asset key[\s\S]*duplicate/);
});

test("jsonValidation: matching uses project-relative paths and basenames", () => {
  const inProject = { relativePath: "sub/.cursor/mcp.json", basename: "mcp.json" };
  const atRoot = { relativePath: ".cursor/mcp.json", basename: "mcp.json" };
  const outside = { basename: "mcp.json" };
  assert.ok(jsonSchemaPatternMatches("**/.cursor/mcp.json", inProject));
  assert.ok(jsonSchemaPatternMatches("**/.cursor/mcp.json", atRoot));
  assert.ok(!jsonSchemaPatternMatches("**/.cursor/mcp.json", outside));
  assert.ok(jsonSchemaPatternMatches("mcp.json", outside));
  assert.ok(!jsonSchemaPatternMatches(".cursor/mcp.json", inProject));
  assert.deepEqual(jsonSchemaTargetForPath("/work/app/sub/.cursor/mcp.json", ["/work", "/work/app/"]), {
    relativePath: "sub/.cursor/mcp.json",
    basename: "mcp.json",
  });
  assert.deepEqual(jsonSchemaTargetForPath("C:\\Work\\App\\x.json", ["c:/work/app"]), { relativePath: "x.json", basename: "x.json" });
  assert.deepEqual(jsonSchemaTargetForPath("/elsewhere/x.json", ["/work"]), { basename: "x.json" });
  assert.deepEqual(jsonSchemaTargetForPath("/x.json", ["/"]), { basename: "x.json" });

  const entries = Array.from({ length: 6 }, (_, index) => ({
    pluginId: `p${index}`,
    pluginName: `p${index}`,
    entry: 0,
    id: "s.json",
    fileMatch: ["package.json"],
    schema: KEY_A,
  }));
  assert.equal(matchJsonSchemas({ basename: "package.json" }, entries).length, 4);
});

test("jsonValidation: schema regexes follow the JSON worker's compilation", () => {
  const regexes = collectJsonSchemaRegexes({
    properties: { a: { pattern: "(?i)^abc$" }, b: { pattern: "^\\p{L}+$" }, c: { pattern: "[" } },
    patternProperties: { "^x-": {} },
    enum: [{ pattern: 1 }],
  });
  assert.deepEqual(
    regexes.map((item) => `${item.source}/${item.flags}`).sort(),
    ["^\\p{L}+$/u", "^abc$/iu", "^x-/u"],
  );
  let deep = {};
  for (let index = 0; index < 80; index += 1) deep = { items: deep };
  assert.equal(collectJsonSchemaRegexes(deep), undefined);
});

test("jsonValidation: install preview lists every pattern", () => {
  assert.equal(jsonValidationPreviewSummary({}), null);
  assert.deepEqual(
    jsonValidationPreviewSummary({ jsonValidation: [{ fileMatch: [".mcp.json", "x.json"] }, { fileMatch: ["x.json"] }] }),
    { count: 2, patterns: [".mcp.json", "x.json"] },
  );
});

test("jsonValidation: Monaco stage registers matching schemas for exact model URIs", async () => {
  const fetched = [];
  setJsonSchemaFetcher(async (pluginId, keys) => {
    fetched.push(`${pluginId}:${keys.join(",")}`);
    return new Map(keys.map((key) => [key, encoder.encode(JSON.stringify({ type: "object", properties: { name: { pattern: "^[a-z]+$" } } }))]));
  });
  setRegexProbeRunner(async () => "ok");
  const stub = stubMonaco([{ uri: "https://example.test/own.json", fileMatch: ["own.json"] }]);
  const detach = attachMonacoJsonSchemas(stub.monaco);
  try {
    stub.addModel("file", "/work/app/.mcp.json");
    stub.addModel("file", "/work/app/other.json");
    stub.addModel("app", "/Users/me/.mcp.json");
    stub.addModel("file", "/work/app/.mcp.json.txt", "plaintext");
    const entries = collectJsonValidationEntries([
      contribution("fixture.jsonschema", [{ id: "schemas/mcp.schema.json", fileMatch: [".mcp.json"], schema: KEY_A }]),
    ]).entries;
    setMonacoJsonSchemas(entries, () => ["/work/app"]);
    await flushMonacoJsonSchemas();
    const options = stub.options();
    assert.equal(options.enableSchemaRequest, false);
    assert.equal(options.schemaRequest, "ignore");
    assert.equal(options.allowComments, true, "other diagnostics options are preserved");
    assert.deepEqual(options.schemas.map((schema) => schema.uri), [
      "https://example.test/own.json",
      pluginJsonSchemaUri("fixture.jsonschema", KEY_A),
    ]);
    assert.deepEqual(options.schemas[1].fileMatch, ["app:///Users/me/.mcp.json", "file:///work/app/.mcp.json"]);
    assert.deepEqual(fetched, [`fixture.jsonschema:${KEY_A}`]);

    // An unchanged match set does not rewrite the defaults or refetch.
    const writes = stub.writes.length;
    await flushMonacoJsonSchemas();
    assert.equal(stub.writes.length, writes);
    assert.equal(fetched.length, 1);

    // Removing every entry removes plugin schemas and keeps the others.
    setMonacoJsonSchemas([]);
    await flushMonacoJsonSchemas();
    assert.deepEqual(stub.options().schemas.map((schema) => schema.uri), ["https://example.test/own.json"]);

    setMonacoJsonSchemas(entries);
    await flushMonacoJsonSchemas();
    assert.equal(stub.options().schemas.length, 2);
    detach();
    assert.deepEqual(stub.options().schemas.map((schema) => schema.uri), ["https://example.test/own.json"]);
    assert.equal(stub.listeners.create.size, 0);
  } finally {
    detach();
    setMonacoJsonSchemas([]);
    setJsonSchemaFetcher();
    setRegexProbeRunner();
  }
});

test("jsonValidation: unsafe, invalid or slow schemas are not registered", async () => {
  const schemas = {
    ["1".repeat(64)]: encoder.encode(JSON.stringify({ properties: { a: { pattern: "^(a+)+$" } } })),
    ["2".repeat(64)]: encoder.encode("{ not json"),
    ["3".repeat(64)]: encoder.encode(JSON.stringify({ properties: { a: { pattern: "^slow$" } } })),
    ["4".repeat(64)]: encoder.encode(JSON.stringify([1, 2])),
  };
  setJsonSchemaFetcher(async (_pluginId, keys) => new Map(keys.map((key) => [key, schemas[key]])));
  setRegexProbeRunner(async (cases) => (cases.some((item) => item.source.includes("slow")) ? "timeout" : "ok"));
  const stub = stubMonaco();
  const detach = attachMonacoJsonSchemas(stub.monaco);
  try {
    stub.addModel("app", "/x/settings.json");
    const entries = Object.keys(schemas).map((key, index) => ({
      pluginId: "fixture.bad",
      pluginName: "Bad",
      entry: index,
      id: `s${index}.json`,
      fileMatch: ["settings.json"],
      schema: key,
    }));
    setMonacoJsonSchemas(entries);
    await flushMonacoJsonSchemas();
    assert.deepEqual(stub.options().schemas ?? [], []);
    const messages = monacoJsonSchemaIssues().map((issue) => issue.message).join("\n");
    assert.match(messages, /rejected/);
    assert.match(messages, /not valid JSON/);
    assert.match(messages, /timed out/);
    assert.match(messages, /root must be an object/);
    assert.ok(monacoJsonSchemaIssues().every((issue) => issue.stage === "frontend"));
  } finally {
    detach();
    setMonacoJsonSchemas([]);
    setJsonSchemaFetcher();
    setRegexProbeRunner();
  }
});

test("fixture corpus: frontend layer for jsonValidation", async () => {
  const expectations = JSON.parse(await readFile(join(corpus, "expectations.json"), "utf8"));
  let checked = 0;
  for (const [name, expected] of Object.entries(expectations.cases)) {
    if (!name.includes("/jsonValidation-")) continue;
    const valid = name.startsWith("valid/");
    if (!valid && !(expected.rejectedBy ?? []).includes("frontend")) continue;
    const dir = join(corpus, name);
    const manifest = JSON.parse(await readFile(join(dir, "plugin.json"), "utf8"));
    const keys = new Map();
    const entries = manifest.contributes.jsonValidation.map((entry, index) => {
      const key = String(index + 1).repeat(64).slice(0, 64);
      keys.set(key, entry.schema);
      return { id: entry.schema, fileMatch: entry.fileMatch, schema: key, ...(entry.title ? { title: entry.title } : {}) };
    });
    const collected = collectJsonValidationEntries([contribution(manifest.id, entries)]);
    assert.deepEqual(collected.issues, [], name);
    setJsonSchemaFetcher(async (_pluginId, requested) => {
      const bytes = new Map();
      for (const key of requested) bytes.set(key, new Uint8Array(await readFile(join(dir, keys.get(key)))));
      return bytes;
    });
    setRegexProbeRunner(async () => "ok");
    const stub = stubMonaco();
    const detach = attachMonacoJsonSchemas(stub.monaco);
    try {
      for (const entry of collected.entries) {
        for (const pattern of entry.fileMatch) stub.addModel("file", `/work/${pattern.replace(/^\*\*\//, "")}`);
      }
      setMonacoJsonSchemas(collected.entries, () => ["/work"]);
      await flushMonacoJsonSchemas();
      if (valid) {
        assert.deepEqual(monacoJsonSchemaIssues(), [], name);
        assert.equal(stub.options().schemas.length, collected.entries.length, name);
      } else {
        assert.ok(monacoJsonSchemaIssues().some((issue) => issue.stage === "frontend"), `${name}: frontend accepted`);
        assert.deepEqual(stub.options().schemas ?? [], [], name);
      }
    } finally {
      detach();
      setMonacoJsonSchemas([]);
      setJsonSchemaFetcher();
      setRegexProbeRunner();
    }
    checked += 1;
  }
  assert.ok(checked >= 3, `checked ${checked} jsonValidation fixtures`);
});

test("jsonValidation: consumers load Monaco lazily (type-only imports)", async () => {
  for (const file of [
    "src/services/contributions/json-validation.ts",
    "src/services/contributions/json-validation-runtime.ts",
    "src/services/monaco-json-schemas.ts",
    "src/domain/plugins/json-validation.ts",
  ]) {
    const source = await readFile(fileURLToPath(new URL(`../${file}`, import.meta.url)), "utf8");
    const eager = [...source.matchAll(/^\s*import\s+(?!type\s)[^;]*?from\s+["']([^"']+)["']/gm)]
      .map((match) => match[1])
      .filter((specifier) => /monaco-runtime|monaco-editor|monaco-keymap|project-file-uri/.test(specifier));
    assert.deepEqual(eager, [], file);
    assert.doesNotMatch(source, /import\(\s*["']@\/services\/monaco-runtime/, file);
    assert.doesNotMatch(source, /releasePluginAssets/, file);
  }
});
