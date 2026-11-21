import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import {
  CUSTOM_EDITOR_CHUNK_BYTES,
  CUSTOM_EDITOR_LOCALES_MAX,
  CUSTOM_EDITOR_MAX_ENTRIES,
  CUSTOM_EDITOR_OPEN_DOCUMENTS_PER_PLUGIN,
  CUSTOM_EDITOR_PATTERN_MAX_LENGTH,
  CUSTOM_EDITOR_PATTERNS_MAX,
  CUSTOM_EDITOR_READ_MAX_BYTES,
  CUSTOM_EDITOR_SENSITIVE_FILENAMES,
  CUSTOM_EDITOR_TITLE_MAX_RUNES,
  CUSTOM_EDITOR_WRITE_MAX_BYTES,
  associationForPath,
  associationPatternForPath,
  customEditorKey,
  customEditorPatternMatches,
  customEditorPreviewSummary,
  isSensitiveFilename,
  isValidCustomEditorPattern,
  normalizeEditorAssociations,
  parseCustomEditorEntry,
  parseCustomEditorKey,
  resolveCustomEditors,
  resolveEditorsForPath,
} from "../src/domain/plugins/custom-editors.ts";
import {
  base64ByteLength,
  customEditorErrorCode,
  isCustomEditorConflictError,
  parseCustomEditorDocumentInfo,
  parseCustomEditorReadChunk,
  parsePluginUIDocumentRequest,
  parsePluginUIDocumentSaveResult,
} from "../src/domain/plugin-ui/document-protocol.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");

function contribution(pluginId, entries, extra = {}) {
  return {
    pluginId,
    pluginName: `Plugin ${pluginId}`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: pluginId, entries, issues: [] },
    ...extra,
  };
}

const plugin = { pluginId: "acme.draw", pluginName: "Acme" };

test("limits and sensitive filenames match the contribution contract", async () => {
  const contract = JSON.parse(await readSource("../src/domain/plugins/contribution-points.json"));
  const spec = contract.points.customEditors;
  assert.deepEqual(spec.kinds, ["process"]);
  assert.equal(spec.maxEntries, CUSTOM_EDITOR_MAX_ENTRIES);
  assert.equal(spec.limits.patternsMax, CUSTOM_EDITOR_PATTERNS_MAX);
  assert.equal(spec.limits.patternMaxLength, CUSTOM_EDITOR_PATTERN_MAX_LENGTH);
  assert.equal(spec.limits.titleMaxRunes, CUSTOM_EDITOR_TITLE_MAX_RUNES);
  assert.equal(spec.limits.localesMax, CUSTOM_EDITOR_LOCALES_MAX);
  assert.equal(spec.limits.readMaxBytes, CUSTOM_EDITOR_READ_MAX_BYTES);
  assert.equal(spec.limits.writeMaxBytes, CUSTOM_EDITOR_WRITE_MAX_BYTES);
  assert.equal(spec.limits.chunkBytes, CUSTOM_EDITOR_CHUNK_BYTES);
  assert.equal(spec.limits.openDocumentsPerPlugin, CUSTOM_EDITOR_OPEN_DOCUMENTS_PER_PLUGIN);
  assert.deepEqual([...CUSTOM_EDITOR_SENSITIVE_FILENAMES].sort(), [...spec.sensitiveFilenames].sort());
});

test("pattern validation follows the contract rules", () => {
  for (const pattern of ["*.drawio", "*.acme.svg", "Acmefile", "docs/*.draw", "*.c++", "data@1.bin", "?.cfg"]) {
    assert.equal(isValidCustomEditorPattern(pattern), true, pattern);
  }
  for (const pattern of [
    "*", "**", "**/*", "*.*", "*.?", ".*", "", "/abs.draw", "../x.draw", "a/../b.draw", "*.{a,b}", "[ab].draw",
    "!x.draw", "a\\b.draw", "a b.draw", "*x", "*.", "x".repeat(CUSTOM_EDITOR_PATTERN_MAX_LENGTH + 1), 42,
  ]) {
    assert.equal(isValidCustomEditorPattern(pattern), false, String(pattern));
  }
});

test("patterns match basenames case-insensitively, or relative paths when they contain /", () => {
  assert.equal(customEditorPatternMatches("*.drawio", "docs/Diagram.DRAWIO"), true);
  assert.equal(customEditorPatternMatches("*.drawio", "docs/diagram.drawio.bak"), false);
  assert.equal(customEditorPatternMatches("Acmefile", "sub/acmefile"), true);
  assert.equal(customEditorPatternMatches("docs/*.draw", "docs/a.draw"), true);
  assert.equal(customEditorPatternMatches("docs/*.draw", "other/a.draw"), false);
});

test("sensitive filenames include the contract list and defensive variants", () => {
  for (const name of CUSTOM_EDITOR_SENSITIVE_FILENAMES) assert.equal(isSensitiveFilename(`cfg/${name}`), true, name);
  for (const name of [".env.production", "deploy.pem", "id_rsa.pub", "ID_ED25519", "prod.tfstate", "my.key"]) {
    assert.equal(isSensitiveFilename(name), true, name);
  }
  for (const name of ["diagram.drawio", "keys.md", "environment.ts"]) assert.equal(isSensitiveFilename(name), false, name);
});

test("entries are re-validated; a default claiming sensitive names is dropped", () => {
  const valid = parseCustomEditorEntry({ id: "drawing", viewId: "editor", filenamePatterns: ["*.draw"] }, plugin);
  assert.equal(valid?.key, "acme.draw.drawing");
  assert.equal(valid?.priority, "option");
  assert.equal(valid?.access, "read");
  assert.equal(valid?.title, "drawing");
  assert.equal(parseCustomEditorEntry({ id: "Bad", viewId: "editor", filenamePatterns: ["*.draw"] }, plugin), undefined);
  assert.equal(parseCustomEditorEntry({ id: "x", viewId: "editor", filenamePatterns: [] }, plugin), undefined);
  assert.equal(parseCustomEditorEntry({ id: "x", viewId: "editor", filenamePatterns: ["*"] }, plugin), undefined);
  assert.equal(parseCustomEditorEntry({ id: "x", viewId: "editor", filenamePatterns: ["*.draw"], access: "write" }, plugin), undefined);
  assert.equal(parseCustomEditorEntry({ id: "x", viewId: "editor", filenamePatterns: ["*.pem"], priority: "default" }, plugin), undefined);
  assert.ok(parseCustomEditorEntry({ id: "x", viewId: "editor", filenamePatterns: ["*.pem"], priority: "option" }, plugin));
  const titled = parseCustomEditorEntry({ id: "x", viewId: "editor", filenamePatterns: ["*.draw"], title: "\u202eDraw\u0000" }, plugin);
  assert.equal(titled?.title, "Draw");
});

test("keys round-trip and split on the last dot", () => {
  assert.deepEqual(parseCustomEditorKey(customEditorKey("acme.draw", "drawing")), { pluginId: "acme.draw", editorId: "drawing" });
  assert.equal(parseCustomEditorKey("nodot"), undefined);
});

test("resolveCustomEditors keeps active process plugins sorted by plugin then id", () => {
  const editors = resolveCustomEditors([
    contribution("zeta", [{ id: "b", viewId: "v", filenamePatterns: ["*.draw"] }, { id: "a", viewId: "v", filenamePatterns: ["*.draw"] }]),
    contribution("alpha", [{ id: "z", viewId: "v", filenamePatterns: ["*.draw"] }]),
    contribution("inactive", [{ id: "a", viewId: "v", filenamePatterns: ["*.draw"] }], { activeInWorkspace: false }),
    contribution("declarative", [{ id: "a", viewId: "v", filenamePatterns: ["*.draw"] }], { runtime: "declarative" }),
  ]);
  assert.deepEqual(editors.map((editor) => editor.key), ["alpha.z", "zeta.a", "zeta.b"]);
});

test("resolution order: user association > plugin default > text; sensitive stays text", () => {
  const editors = resolveCustomEditors([
    contribution("beta", [{ id: "view", viewId: "v", filenamePatterns: ["*.draw"], priority: "default", access: "readWrite" }]),
    contribution("alpha", [
      { id: "opt", viewId: "v", filenamePatterns: ["*.draw", "*.pem"] },
      { id: "def", viewId: "v", filenamePatterns: ["*.draw"], priority: "default" },
    ]),
  ]);
  const first = resolveEditorsForPath(editors, "a/b.draw");
  assert.equal(first.default?.key, "alpha.def");
  assert.equal(first.source, "plugin");
  assert.equal(first.needsConfirmation, true);
  assert.deepEqual(first.options.map((editor) => editor.key), ["alpha.def", "alpha.opt", "beta.view"]);

  const confirmed = resolveEditorsForPath(editors, "b.draw", { byPattern: {}, confirmedDefaults: ["alpha.def"] });
  assert.equal(confirmed.needsConfirmation, false);

  const user = resolveEditorsForPath(editors, "b.DRAW", { byPattern: { "*.draw": "beta.view" }, confirmedDefaults: [] });
  assert.equal(user.default?.key, "beta.view");
  assert.equal(user.source, "user");
  assert.equal(user.needsConfirmation, false);

  const text = resolveEditorsForPath(editors, "b.draw", { byPattern: { "*.draw": "text" }, confirmedDefaults: [] });
  assert.equal(text.default, undefined);
  assert.equal(text.source, "user");

  const exact = resolveEditorsForPath(editors, "x/special.draw", {
    byPattern: { "*.draw": "text", "special.draw": "alpha.opt" },
    confirmedDefaults: [],
  });
  assert.equal(exact.default?.key, "alpha.opt");

  const inert = resolveEditorsForPath(editors, "b.draw", { byPattern: { "*.draw": "gone.editor" }, confirmedDefaults: [] });
  assert.equal(inert.default?.key, "alpha.def");
  assert.equal(inert.source, "plugin");

  const sensitive = resolveEditorsForPath(editors, "certs/server.pem", { byPattern: { "*.pem": "alpha.opt" }, confirmedDefaults: [] });
  assert.equal(sensitive.default, undefined);
  assert.equal(sensitive.sensitive, true);
  assert.deepEqual(sensitive.options.map((editor) => editor.key), ["alpha.opt"]);
});

test("associations normalize and look up exact name before extension", () => {
  const associations = normalizeEditorAssociations({
    byPattern: { "*.DRAW": "alpha.def", "Makefile": "text", "../x": "text", "*.ok": "noDot", "*.bad": 3 },
    confirmedDefaults: ["alpha.def", "alpha.def", "text", 7],
  });
  assert.deepEqual(associations.byPattern, { "*.draw": "alpha.def", makefile: "text" });
  assert.deepEqual(associations.confirmedDefaults, ["alpha.def"]);
  assert.deepEqual(normalizeEditorAssociations(null), { byPattern: {}, confirmedDefaults: [] });
  assert.deepEqual(associationForPath(associations, "src/Makefile"), { pattern: "makefile", value: "text" });
  assert.deepEqual(associationForPath(associations, "a/B.Draw"), { pattern: "*.draw", value: "alpha.def" });
  assert.equal(associationPatternForPath("docs/Diagram.Acme.SVG"), "*.svg");
  assert.equal(associationPatternForPath("Acmefile"), "acmefile");
});

test("install preview summarizes valid entries only", () => {
  const summary = customEditorPreviewSummary({
    customEditors: [
      { id: "drawing", viewId: "v", title: "Drawing", filenamePatterns: ["*.draw"], priority: "default", access: "readWrite" },
      { id: "bad", viewId: "v", filenamePatterns: ["*"] },
    ],
  }, "acme");
  assert.deepEqual(summary, [{ title: "Drawing", patterns: ["*.draw"], priority: "default", access: "readWrite" }]);
  assert.deepEqual(customEditorPreviewSummary(undefined), []);
});

test("iframe document requests reject paths, force, invalid base64, and oversize chunks", () => {
  assert.deepEqual(
    parsePluginUIDocumentRequest({ type: "mincode.plugin.document.read", id: "1", offset: 0, length: 10 }),
    { type: "mincode.plugin.document.read", id: "1", offset: 0, length: 10 },
  );
  assert.equal(parsePluginUIDocumentRequest({ type: "mincode.plugin.document.read", id: "1", offset: 0, length: 0 }), undefined);
  assert.equal(parsePluginUIDocumentRequest({ type: "mincode.plugin.document.read", id: "1", offset: 0, length: 11 }, 10), undefined);
  assert.equal(parsePluginUIDocumentRequest({ type: "mincode.plugin.document.read", id: "1", offset: -1, length: 1 }), undefined);

  const save = { type: "mincode.plugin.document.save", id: "2", data: "aGVsbG8=", last: true };
  assert.deepEqual(parsePluginUIDocumentRequest(save), { ...save, uploadId: undefined });
  assert.equal(parsePluginUIDocumentRequest({ ...save, force: true }), undefined);
  assert.equal(parsePluginUIDocumentRequest({ ...save, force: false }), undefined);
  assert.equal(parsePluginUIDocumentRequest({ ...save, path: "/etc/passwd" }), undefined);
  assert.equal(parsePluginUIDocumentRequest({ ...save, relativePath: "other.txt" }), undefined);
  assert.equal(parsePluginUIDocumentRequest({ ...save, data: "not base64!" }), undefined);
  assert.equal(parsePluginUIDocumentRequest({ ...save, data: "aGVsbG8=" }, 4), undefined);
  assert.equal(parsePluginUIDocumentRequest({ ...save, last: "yes" }), undefined);
  assert.equal(parsePluginUIDocumentRequest({ ...save, uploadId: "../x" }), undefined);
  assert.deepEqual(parsePluginUIDocumentRequest({ type: "mincode.plugin.document.dirty", id: "3", dirty: true }), {
    type: "mincode.plugin.document.dirty", id: "3", dirty: true,
  });
  assert.equal(parsePluginUIDocumentRequest({ type: "mincode.plugin.document.dirty", id: "3", dirty: 1 }), undefined);
  assert.equal(parsePluginUIDocumentRequest({ type: "mincode.plugin.document.delete", id: "4" }), undefined);

  assert.deepEqual(parsePluginUIDocumentSaveResult({ type: "mincode.plugin.document.save-result", requestId: "save-1", ok: false, error: "x" }), {
    type: "mincode.plugin.document.save-result", requestId: "save-1", ok: false, error: "x",
  });
  assert.equal(parsePluginUIDocumentSaveResult({ type: "mincode.plugin.document.save-result", requestId: "save-1" }), undefined);
});

test("base64 sizes and host responses are validated", () => {
  assert.equal(base64ByteLength(""), 0);
  assert.equal(base64ByteLength("aGVsbG8="), 5);
  assert.equal(base64ByteLength("aGk="), 2);
  assert.equal(base64ByteLength("aGVs"), 3);

  const info = parseCustomEditorDocumentInfo({
    name: "a.draw", relativePath: "docs/a.draw", size: 10, modifiedAt: 1700000000000, access: "read",
    maxReadBytes: CUSTOM_EDITOR_READ_MAX_BYTES * 2, maxWriteBytes: 99, chunkBytes: CUSTOM_EDITOR_CHUNK_BYTES * 2,
  }, "docs/a.draw");
  assert.equal(info.maxReadBytes, CUSTOM_EDITOR_READ_MAX_BYTES);
  assert.equal(info.maxWriteBytes, 0);
  assert.equal(info.chunkBytes, CUSTOM_EDITOR_CHUNK_BYTES);
  assert.equal("path" in info, false);
  assert.throws(() => parseCustomEditorDocumentInfo({ name: "a", relativePath: "other", size: 1, access: "read" }, "docs/a.draw"));
  assert.throws(() => parseCustomEditorDocumentInfo({ name: "a", relativePath: "docs/a.draw", size: 1, access: "write" }, "docs/a.draw"));

  assert.deepEqual(parseCustomEditorReadChunk({ data: "aGk=", eof: true, size: 2, modifiedAt: 5 }, 2), {
    data: "aGk=", eof: true, size: 2, modifiedAt: 5,
  });
  assert.deepEqual(parseCustomEditorReadChunk({ data: null, eof: true, size: 0 }, 2).data, "");
  assert.throws(() => parseCustomEditorReadChunk({ data: "aGVsbG8=", eof: true, size: 5 }, 2));
});

test("host error codes are parsed from Go error prefixes", () => {
  assert.equal(customEditorErrorCode(new Error("conflict: file changed on disk")), "conflict");
  assert.equal(customEditorErrorCode("plugin host: too_large: upload exceeds limit"), "too_large");
  assert.equal(customEditorErrorCode(new Error("stale_session: unknown session")), "stale_session");
  assert.equal(customEditorErrorCode(new Error("something else")), undefined);
  assert.equal(isCustomEditorConflictError(new Error("conflict: x")), true);
  assert.equal(isCustomEditorConflictError(new Error("not_found: x")), false);
});
