import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import {
  collectLanguageServers,
  languageServerErrorCode,
  languageServerPreviewSummary,
  validateLanguageServerEntry,
} from "../src/domain/plugins/language-servers.ts";
import {
  isPluginLspServerId,
  parsePluginLspServerId,
  pluginApplyEditAllowed,
  pluginLspServerId,
  PLUGIN_APPLY_EDIT_WINDOW_MS,
  resolveLanguageRouting,
  routedServerLanguages,
  sanitizeLspSettings,
} from "../src/domain/projects/lsp.ts";

const root = new URL("../../", import.meta.url);
const read = (path) => readFile(new URL(path, root), "utf8");
const FP = "a".repeat(64);

test("plugin server ids round-trip and reject malformed ids", () => {
  assert.equal(pluginLspServerId("acme.go", "demo"), "plugin:acme.go/demo");
  assert.deepEqual(parsePluginLspServerId("plugin:acme.go/demo"), { pluginId: "acme.go", serverId: "demo" });
  for (const bad of ["plugin:../x/demo", "plugin:acme/Demo", "plugin:acme", "gopls", "plugin:acme/demo/x"]) {
    assert.equal(isPluginLspServerId(bad), false, bad);
    assert.equal(parsePluginLspServerId(bad), undefined, bad);
  }
});

test("sanitizeLspSettings keeps built-in and plugin ids and valid overrides only", () => {
  const settings = sanitizeLspSettings({
    enabledServers: ["gopls", "gopls", "unknown", "plugin:acme.go/demo", "plugin:../x/y", 3],
    pluginLanguageOverrides: { go: "plugin:acme.go/demo", Go: "plugin:acme.go/demo", rust: "gopls", yaml: 1 },
  });
  assert.deepEqual(settings, {
    enabledServers: ["gopls", "plugin:acme.go/demo"],
    pluginLanguageOverrides: { go: "plugin:acme.go/demo" },
  });
  assert.deepEqual(sanitizeLspSettings({ enabledServers: [], pluginLanguageOverrides: { x: "gopls" } }), { enabledServers: [] });
  assert.deepEqual(sanitizeLspSettings(null), { enabledServers: [] });
});

function context({ enabled, overrides = {} }) {
  return {
    enabled: new Set(enabled),
    overrides,
    builtinLanguages: new Map([["gopls", ["go"]], ["ts-ls", ["typescript", "javascript"]]]),
    pluginLanguages: new Map([
      ["plugin:zeta/go", ["go", "gomod"]],
      ["plugin:alpha/go", ["go", "gomod", "gotmpl"]],
    ]),
  };
}

test("a built-in server wins unless an explicit override names an enabled plugin server", () => {
  const all = ["gopls", "plugin:zeta/go", "plugin:alpha/go"];
  assert.deepEqual(resolveLanguageRouting("go", context({ enabled: all })), { kind: "builtin" });
  // Plugin servers are ordered by id for languages no built-in covers.
  assert.deepEqual(resolveLanguageRouting("gomod", context({ enabled: all })), { kind: "plugin", serverId: "plugin:alpha/go" });
  const overridden = context({ enabled: all, overrides: { go: "plugin:zeta/go" } });
  assert.deepEqual(resolveLanguageRouting("go", overridden), { kind: "plugin", serverId: "plugin:zeta/go" });
  // An override of a disabled or non-declaring server is inert.
  assert.deepEqual(resolveLanguageRouting("go", context({ enabled: ["gopls"], overrides: { go: "plugin:zeta/go" } })), { kind: "builtin" });
  assert.deepEqual(resolveLanguageRouting("typescript", context({ enabled: all, overrides: { typescript: "plugin:zeta/go" } })), undefined);
  // A disabled built-in leaves its language to an enabled plugin server.
  assert.deepEqual(resolveLanguageRouting("go", context({ enabled: ["plugin:zeta/go"] })), { kind: "plugin", serverId: "plugin:zeta/go" });
  assert.equal(resolveLanguageRouting("rust", context({ enabled: all })), undefined);
  // Prototype keys never act as overrides.
  assert.deepEqual(resolveLanguageRouting("constructor", context({ enabled: all })), undefined);
});

test("routedServerLanguages splits languages between built-in and plugin servers", () => {
  const ctx = context({ enabled: ["gopls", "plugin:zeta/go", "plugin:alpha/go"], overrides: { go: "plugin:zeta/go" } });
  assert.deepEqual(routedServerLanguages("gopls", ["go"], ctx), []);
  assert.deepEqual(routedServerLanguages("gopls", ["go"], ctx, true), ["go"]);
  assert.deepEqual(routedServerLanguages("plugin:zeta/go", ["go", "gomod"], ctx), ["go"]);
  assert.deepEqual(routedServerLanguages("plugin:alpha/go", ["go", "gomod", "gotmpl"], ctx), ["gomod", "gotmpl"]);
  assert.deepEqual(routedServerLanguages("ts-ls", ["typescript", "javascript"], ctx), ["typescript", "javascript"]);
});

test("an unsolicited applyEdit is allowed only shortly after a user action", () => {
  const now = 1_000_000;
  assert.equal(pluginApplyEditAllowed(undefined, now), false);
  assert.equal(pluginApplyEditAllowed(now - 1000, now), true);
  assert.equal(pluginApplyEditAllowed(now - PLUGIN_APPLY_EDIT_WINDOW_MS, now), true);
  assert.equal(pluginApplyEditAllowed(now - PLUGIN_APPLY_EDIT_WINDOW_MS - 1, now), false);
  assert.equal(pluginApplyEditAllowed(now + 1, now), false);
});

test("entries are re-validated and only active process plugins contribute", () => {
  const good = { id: "demo", title: "Demo", titleLocalized: { zh: "\u6f14\u793a" }, languages: ["mcdemo"], specFingerprint: FP, command: "ignored" };
  const result = validateLanguageServerEntry(good);
  assert.equal(result.ok, true);
  assert.deepEqual(result.server, { id: "demo", title: "Demo", titleLocalized: { zh: "\u6f14\u793a" }, languages: ["mcdemo"], specFingerprint: FP });
  for (const bad of [
    { ...good, id: "Demo" },
    { ...good, title: "a\u202eb" },
    { ...good, title: "a\u2028b" },
    { ...good, languages: [] },
    { ...good, languages: ["go", "go"] },
    { ...good, languages: ["Go"] },
    { ...good, specFingerprint: "x" },
    { ...good, titleLocalized: { "bad locale!": "x" } },
  ]) {
    assert.equal(validateLanguageServerEntry(bad).ok, false, JSON.stringify(bad));
  }
  const plugin = (pluginId, runtime, activeInWorkspace, entries) => ({
    pluginId,
    pluginName: pluginId,
    version: "1.0.0",
    source: "local",
    runtime,
    activeInWorkspace,
    point: { fingerprint: "f", entries, issues: [] },
  });
  const collected = collectLanguageServers([
    plugin("zeta", "process", true, [good, { ...good }, { ...good, id: "bad", languages: [] }]),
    plugin("alpha", "process", true, [{ ...good, id: "other" }]),
    plugin("decl", "declarative", true, [good]),
    plugin("away", "process", false, [good]),
  ]);
  assert.deepEqual(collected.servers.map((server) => server.sessionId), ["plugin:alpha/other", "plugin:zeta/demo"]);
  assert.deepEqual(collected.issues.map((issue) => issue.message), ['duplicate language server id "demo"', "languages are invalid"]);
});

test("install preview and error codes", () => {
  assert.deepEqual(
    languageServerPreviewSummary({ languageServers: [{ title: "Demo", languages: ["go", "Bad"] }, { title: "" }, { title: "Bare" }] }),
    ["Demo (go)", "Bare"],
  );
  assert.deepEqual(languageServerPreviewSummary(undefined), []);
  assert.equal(languageServerErrorCode(new Error("limit: too many restarts")), "limit");
  assert.equal(languageServerErrorCode("untrusted: x"), "untrusted");
  assert.equal(languageServerErrorCode(new Error("boom")), "");
});
