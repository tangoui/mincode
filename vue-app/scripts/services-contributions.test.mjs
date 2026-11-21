import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import {
  SERVICE_INPUT_KINDS,
  SERVICE_LIMITS,
  SERVICE_MIME_TYPES,
  SERVICE_OUTPUT_KINDS,
  SERVICE_SURFACES,
  normalizeServiceAccepts,
  parsePluginServiceEntry,
  resolveDeclaredPluginServices,
  resolvePluginServices,
  resolveServiceSurfaces,
  serviceAccepts,
  serviceEntryTitle,
  serviceNameError,
  servicesForFiles,
  servicesForPalette,
  servicesForSelection,
  servicePreviewSurfaces,
} from "../src/domain/plugins/services.ts";
import {
  buildDbResultFile,
  httpResponseFileName,
  isTextResponseType,
  normalizeTerminalServiceText,
  redactResponseHeaders,
  servicesForSurface,
  surfaceRunRequest,
  urlHasUserinfo,
} from "../src/domain/plugins/service-surfaces.ts";

const contract = JSON.parse(await readFile(new URL("../../plugins/api/contribution-points.json", import.meta.url), "utf8"));
const goMimeTable = await readFile(new URL("../../backend/plugin/service_mime.go", import.meta.url), "utf8");
const sharedHeaderVectors = JSON.parse(await readFile(new URL("../../backend/plugin/testdata/service-response-headers.json", import.meta.url), "utf8"));

function provider(overrides = {}) {
  return {
    pluginId: "acme-text",
    pluginName: "Acme Text",
    entryId: "reverse",
    service: "demo.text.reverse",
    title: "Reverse text",
    description: "Reverse the characters of a text",
    input: { kind: "text", maxBytes: 8192 },
    output: { kind: "text" },
    surfaces: ["selection", "palette"],
    risk: "read",
    timeoutMs: 30000,
    isDefault: false,
    ...overrides,
  };
}

function fileProvider(overrides = {}) {
  return provider({
    entryId: "ocr",
    service: "image.ocr",
    title: "Recognize text",
    input: { kind: "files", accepts: ["image/*"], maxBytes: 67108864, maxFiles: 2 },
    output: { kind: "json" },
    surfaces: ["fileMenu", "palette"],
    ...overrides,
  });
}

test("service limits and lists mirror the contract", () => {
  const spec = contract.points.services;
  assert.equal(spec.status, "enabled");
  assert.deepEqual(spec.kinds, ["process"]);
  assert.equal(SERVICE_LIMITS.maxEntries, spec.maxEntries);
  for (const [key, value] of Object.entries(SERVICE_LIMITS)) {
    if (key !== "maxEntries") assert.equal(value, spec.limits[key], key);
  }
  assert.deepEqual([...SERVICE_INPUT_KINDS], spec.lists.inputKinds);
  assert.deepEqual([...SERVICE_OUTPUT_KINDS], spec.lists.outputKinds);
  assert.deepEqual([...SERVICE_SURFACES], spec.lists.surfaces);
});

test("the MIME table mirrors service_mime.go", () => {
  const goTable = Object.fromEntries([...goMimeTable.matchAll(/"(\.[a-z0-9]+)":\s*"([^"]+)"/g)].map((match) => [match[1], match[2]]));
  assert.ok(Object.keys(goTable).length > 40);
  assert.deepEqual({ ...SERVICE_MIME_TYPES }, goTable);
});

test("serviceAccepts matches extensions and MIME types through the table", () => {
  assert.equal(serviceAccepts(["image/*"], "A.PNG"), true);
  assert.equal(serviceAccepts([".pdf"], "x.pdf.png"), false);
  assert.equal(serviceAccepts(["application/pdf"], "x.PDF"), true);
  assert.equal(serviceAccepts([".tar.gz"], "backup.TAR.GZ"), true);
  assert.equal(serviceAccepts(["application/x-acme"], "a.acme"), false);
  assert.equal(serviceAccepts(["image/*"], "notes.unknownext"), false);
  assert.equal(serviceAccepts(["text/*"], "dir.d/README"), false);
  assert.equal(serviceAccepts([".kml"], "map.\u212aML"), true, "Kelvin sign lower-cases to k like Go");
});

test("entry rules reach the host's verdicts", () => {
  assert.equal(serviceNameError("demo.text.reverse"), undefined);
  for (const name of ["Demo.x", "demo", "a.b.c.d.e", `a.${"b".repeat(63)}`, "mincode.x", "host.x", "plugin.x"]) {
    assert.ok(serviceNameError(name), name);
  }
  assert.equal(normalizeServiceAccepts(undefined), undefined);
  assert.equal(normalizeServiceAccepts([]), undefined);
  assert.equal(normalizeServiceAccepts(["png"]), undefined);
  assert.equal(normalizeServiceAccepts([".png", ".PNG"]), undefined);
  assert.equal(normalizeServiceAccepts(Array.from({ length: 33 }, (_, index) => `.x${index}`)), undefined);
  assert.deepEqual(normalizeServiceAccepts([".PNG", "Image/*", "application/x-acme"]), [".png", "image/*", "application/x-acme"]);
  assert.deepEqual(normalizeServiceAccepts([".\u0130ni"]), [".ini"], "U+0130 lower-cases to i like Go");

  // fileMenu/selection need no workspace.read: the default is every compatible surface.
  assert.deepEqual(resolveServiceSurfaces(undefined, "text"), ["selection", "palette"]);
  assert.deepEqual(resolveServiceSurfaces(undefined, "file"), ["fileMenu", "palette"]);
  assert.deepEqual(resolveServiceSurfaces(undefined, "files"), ["fileMenu", "palette"]);
  assert.deepEqual(resolveServiceSurfaces(undefined, "none"), ["palette"]);
  assert.deepEqual(resolveServiceSurfaces([], "text"), []);
  assert.deepEqual(resolveServiceSurfaces(["palette", "selection"], "text"), ["selection", "palette"]);
  assert.deepEqual(resolveServiceSurfaces(["fileMenu"], "file"), ["fileMenu"]);
  assert.equal(resolveServiceSurfaces(["fileMenu"], "text"), undefined);
  assert.equal(resolveServiceSurfaces(["palette", "palette"], "text"), undefined);
  assert.equal(resolveServiceSurfaces(["toolbar"], "text"), undefined);
});

test("listed entries are revalidated and normalized", () => {
  const parsed = parsePluginServiceEntry(provider({ input: { kind: "text" }, timeoutMs: undefined, pluginName: "" }));
  assert.equal(parsed.input.maxBytes, 8192);
  assert.equal(parsed.timeoutMs, 30000);
  assert.equal(parsed.pluginName, "acme-text");
  const files = parsePluginServiceEntry(fileProvider({ input: { kind: "files", accepts: [".PNG"] } }));
  assert.deepEqual(files.input, { kind: "files", accepts: [".png"], maxBytes: 67108864, maxFiles: 32 });
  for (const [label, entry] of Object.entries({
    "bad service": provider({ service: "Demo.x" }),
    "reserved prefix": provider({ service: "host.x" }),
    "file without accepts": fileProvider({ input: { kind: "file" } }),
    "accepts on text": provider({ input: { kind: "text", accepts: [".txt"] } }),
    "text above the maximum": provider({ input: { kind: "text", maxBytes: 196609 } }),
    "101 files": fileProvider({ input: { kind: "files", accepts: [".txt"], maxFiles: 101 } }),
    "mime on json": provider({ output: { kind: "json", mime: "application/json" } }),
    "text on fileMenu": provider({ surfaces: ["fileMenu"] }),
    "blank title": provider({ title: " " }),
    "long title": provider({ title: "t".repeat(49) }),
    "zero-width title": provider({ title: "a\u200bb" }),
    "bad risk": provider({ risk: "harmless" }),
    "timeout too long": provider({ timeoutMs: 300001 }),
    "object params": provider({ params: { type: "object", properties: { options: { type: "object" } } } }),
  })) {
    assert.equal(parsePluginServiceEntry(entry), undefined, label);
  }
  const { surfaces: _omitted, ...withoutSurfaces } = provider();
  assert.deepEqual(parsePluginServiceEntry(withoutSurfaces).surfaces, ["selection", "palette"]);
  const listed = resolvePluginServices([provider(), provider(), provider({ service: "Bad" }), "junk"]);
  assert.equal(listed.length, 1);
});

test("resolveDeclaredPluginServices keeps the plugin's entries with a valid state", () => {
  const pluginId = provider().pluginId;
  const declared = resolveDeclaredPluginServices([
    provider({ active: true, isDefault: true }),
    provider({ entryId: "off", active: false, inactiveReason: "inactiveWorkspace", isDefault: true }),
    provider({ entryId: "off", active: true }),
    provider({ entryId: "bad-reason", active: false, inactiveReason: "sleepy" }),
    provider({ entryId: "no-flag" }),
    provider({ entryId: "other", pluginId: "other.plugin", active: true }),
    provider({ entryId: "invalid", service: "Bad", active: true }),
  ], pluginId);
  assert.deepEqual(declared.map((entry) => [entry.entryId, entry.active, entry.inactiveReason, entry.isDefault]), [
    [provider().entryId, true, undefined, true],
    ["off", false, "inactiveWorkspace", false],
  ]);
  assert.deepEqual(resolveDeclaredPluginServices("junk", pluginId), []);
});

test("servicesForFiles needs every file to match", () => {
  const entries = [fileProvider(), provider()];
  assert.deepEqual(servicesForFiles(entries, ["a.png", "b.JPG"]).map((group) => group.service), ["image.ocr"]);
  assert.deepEqual(servicesForFiles(entries, ["a.png", "b.txt"]), []);
  assert.deepEqual(servicesForFiles(entries, ["a.png", "b.png", "c.png"]), [], "more than maxFiles");
  assert.deepEqual(servicesForFiles(entries, []), []);
  const single = fileProvider({ entryId: "one", service: "image.one", title: "One", input: { kind: "file", accepts: [".png"] } });
  assert.deepEqual(servicesForFiles([single], ["a.png"]).map((group) => group.service), ["image.one"]);
  assert.deepEqual(servicesForFiles([single], ["a.png", "b.png"]), []);
  const paletteOnly = fileProvider({ surfaces: ["palette"] });
  assert.deepEqual(servicesForFiles([paletteOnly], ["a.png"]), []);
});

test("servicesForSelection reports whether the text fits", () => {
  const text = "汉".repeat(2731);
  assert.equal(new TextEncoder().encode(text).length, 8193);
  const [group] = servicesForSelection([provider()], text);
  assert.equal(group.fits, false);
  assert.equal(servicesForSelection([provider()], "汉".repeat(2730))[0].fits, true);
  const roomy = provider({ pluginId: "acme-big", pluginName: "Acme Big", input: { kind: "text", maxBytes: 65536 } });
  const [mixed] = servicesForSelection([provider(), roomy], text);
  assert.equal(mixed.fits, true);
  assert.deepEqual(mixed.providers.map((entry) => entry.pluginId), ["acme-big"]);
  assert.deepEqual(servicesForSelection([fileProvider(), provider({ surfaces: ["palette"] })], "x"), []);
});

test("providers of one service merge into one group sorted by title", () => {
  const other = provider({ pluginId: "zeta-text", pluginName: "Zeta", title: "Reverse (Zeta)", isDefault: true });
  const upper = provider({ entryId: "upper", service: "demo.text.upper", title: "Analyze text" });
  const groups = servicesForPalette([provider(), other, upper, fileProvider({ surfaces: ["fileMenu"] })]);
  assert.deepEqual(groups.map((group) => group.service), ["demo.text.upper", "demo.text.reverse"]);
  const reverse = groups[1];
  assert.deepEqual(reverse.providers.map((entry) => entry.pluginId), ["zeta-text", "acme-text"]);
  assert.equal(reverse.title, "Reverse (Zeta)");
  assert.equal(reverse.fits, true);
});

test("localized titles are revalidated and name groups in the UI language", () => {
  const localized = provider({ titleLocalized: { "zh-CN": " 反转文字 ", en: "Reverse text" } });
  const [entry] = resolvePluginServices([localized]);
  assert.deepEqual(entry.titleLocalized, { "zh-CN": "反转文字", en: "Reverse text" });
  assert.equal(serviceEntryTitle(entry, ["zh-CN", "en"]), "反转文字");
  assert.equal(serviceEntryTitle(entry, "fr"), "Reverse text");
  for (const titleLocalized of [{ "zh-CN": "" }, { "": "x" }, { "zh-CN": "a‮b" }, { zh: "x".repeat(49) }, "x",
    Object.fromEntries(Array.from({ length: 17 }, (_, index) => [`l${index}`, "x"]))]) {
    assert.deepEqual(resolvePluginServices([provider({ titleLocalized })]), [], JSON.stringify(titleLocalized));
  }
  const upper = provider({ pluginId: "acme-upper", entryId: "upper", service: "demo.text.upper", title: "Upper case", titleLocalized: { zh: "大写" } });
  const entries = resolvePluginServices([localized, upper]);
  assert.deepEqual(servicesForPalette(entries, ["zh-CN", "zh"]).map((group) => group.title), ["反转文字", "大写"].sort((a, b) => a.localeCompare(b)));
  assert.deepEqual(servicesForSelection(entries, "abc", "zh").map((group) => group.title).sort(), ["Reverse text", "大写"].sort());
  assert.deepEqual(servicesForPalette(entries).map((group) => group.title), ["Reverse text", "Upper case"]);
  const file = fileProvider({ titleLocalized: { zh: "识别文字" } });
  assert.deepEqual(servicesForFiles(resolvePluginServices([file]), ["a.png"], "zh").map((group) => group.title), ["识别文字"]);
});

// ---------------------------------------------------------------------------
// Newer surfaces (sftpFile, dbResult, httpResponse, terminalSelection).

test("services: surfaces and limits mirror the contract", () => {
  const spec = contract.points.services;
  assert.deepEqual([...SERVICE_SURFACES], spec.lists.surfaces);
  for (const key of ["dbResultMaxRows", "dbResultMaxBytes", "httpResponseMaxBytes", "httpResponseHeadersMax", "httpHeaderValueMaxBytes",
    "surfaceContextMaxBytes", "remoteContextPathMaxBytes", "remoteInputStallMs"]) {
    assert.equal(SERVICE_LIMITS[key], spec.limits[key], key);
  }
  assert.deepEqual(Object.keys(spec.valueSinceHost.surfaces).sort(), ["dbResult", "httpResponse", "sftpFile", "terminalSelection"]);
  // Compatibility table; the default (surfaces omitted) never includes the newer ones.
  assert.deepEqual(resolveServiceSurfaces(["terminalSelection", "selection"], "text"), ["selection", "terminalSelection"]);
  assert.deepEqual(resolveServiceSurfaces(["sftpFile", "dbResult", "httpResponse"], "file"), ["sftpFile", "dbResult", "httpResponse"]);
  assert.deepEqual(resolveServiceSurfaces(["sftpFile"], "files"), ["sftpFile"]);
  assert.equal(resolveServiceSurfaces(["terminalSelection"], "file"), undefined);
  assert.equal(resolveServiceSurfaces(["dbResult"], "text"), undefined);
  assert.equal(resolveServiceSurfaces(["httpResponse"], "none"), undefined);
  assert.deepEqual(resolveServiceSurfaces(undefined, "text"), ["selection", "palette"]);
  assert.deepEqual(resolveServiceSurfaces(undefined, "file"), ["fileMenu", "palette"]);
  // An entry with a newer surface is kept (it used to be dropped as unknown).
  assert.deepEqual(resolvePluginServices([provider({ surfaces: ["terminalSelection"] })])[0]?.surfaces, ["terminalSelection"]);
  assert.deepEqual(servicePreviewSurfaces({ services: [{ surfaces: ["fileMenu", "httpResponse"] }, { surfaces: ["sftpFile"] }, {}] }), ["sftpFile", "httpResponse"]);
  assert.deepEqual(servicePreviewSurfaces({ services: [{ input: { kind: "file" } }] }), []);
});

const sftpConnection = { id: "c1", name: "prod", protocol: "sftp", host: "files.example.com", port: 22, username: "deploy" };

function sftpTarget(overrides = {}) {
  return {
    surface: "sftpFile",
    sessionId: "sess-1",
    file: { name: "app.log", size: 1000 },
    remote: { connection: { ...sftpConnection }, path: "/var/log/app.log", name: "app.log", kind: "file", size: 1000 },
    ...overrides,
  };
}

function dbTarget(overrides = {}) {
  return {
    surface: "dbResult",
    columns: [{ name: "id", type: "bigint" }, { name: "email", type: null }],
    rows: () => [[1, "a@example.com"], [2, "b@example.com"]],
    formatCell: (value) => `fmt:${String(value)}`,
    hasMore: false,
    running: false,
    pendingChanges: false,
    view: { filtered: false, sorted: false, hiddenColumns: 0 },
    ...overrides,
  };
}

function httpTarget(overrides = {}, response = {}) {
  return {
    surface: "httpResponse",
    response: { status: 200, statusText: "OK", contentType: "application/json", headers: [["Content-Type", "application/json"]], body: "{}", sizeBytes: 2, elapsedMs: 12.5, ...response },
    secrets: [],
    request: { method: "get", url: "https://api.example.com/v1/orders?token=abc" },
    ...overrides,
  };
}

test("service-surfaces: servicesForSurface", () => {
  const logReader = fileProvider({ entryId: "log", service: "log.analyze", title: "Analyze log", input: { kind: "file", accepts: [".log"], maxBytes: 2048 }, surfaces: ["sftpFile"] });
  const legacy = fileProvider({ entryId: "legacy", service: "log.legacy", title: "Legacy", input: { kind: "file", accepts: [".log"] }, surfaces: undefined });
  const entries = resolvePluginServices([logReader, legacy]);
  // Only explicit sftpFile entries; accepts match the remote name.
  assert.deepEqual(servicesForSurface(entries, sftpTarget()).map((group) => [group.service, group.disabledReason]), [["log.analyze", undefined]]);
  assert.deepEqual(servicesForSurface(entries, sftpTarget({ file: { name: "app.txt", size: 10 } })), []);
  assert.deepEqual(servicesForSurface(entries, sftpTarget({ remote: { ...sftpTarget().remote, kind: "directory" } })), []);
  // Larger than the group's largest maxBytes: greyed out.
  const [tooLarge] = servicesForSurface(entries, sftpTarget({ file: { name: "app.log", size: 2049 } }));
  assert.equal(tooLarge.disabledReason, "tooLargeFile");
  const roomy = fileProvider({ pluginId: "acme-big", pluginName: "Big", entryId: "log", service: "log.analyze", title: "Analyze log", input: { kind: "file", accepts: [".log"], maxBytes: 4096 }, surfaces: ["sftpFile"] });
  const [mixed] = servicesForSurface(resolvePluginServices([logReader, roomy]), sftpTarget({ file: { name: "app.log", size: 2049 } }));
  assert.equal(mixed.disabledReason, undefined);
  assert.deepEqual(mixed.providers.map((entry) => entry.pluginId), ["acme-big"]);

  // dbResult: accepts must match result.json; running and pending edits grey out.
  const chart = fileProvider({ entryId: "chart", service: "data.chart", title: "Chart", input: { kind: "file", accepts: ["application/json"] }, surfaces: ["dbResult"] });
  const csvOnly = fileProvider({ entryId: "csv", service: "data.csv", title: "CSV", input: { kind: "file", accepts: [".csv"] }, surfaces: ["dbResult"] });
  const dbEntries = resolvePluginServices([chart, csvOnly, legacy]);
  assert.deepEqual(servicesForSurface(dbEntries, dbTarget()).map((group) => [group.service, group.disabledReason]), [["data.chart", undefined]]);
  assert.equal(servicesForSurface(dbEntries, dbTarget({ running: true, pendingChanges: true }))[0].disabledReason, "queryRunning");
  assert.equal(servicesForSurface(dbEntries, dbTarget({ pendingChanges: true }))[0].disabledReason, "pendingChanges");
  assert.deepEqual(servicesForSurface(dbEntries, dbTarget({ rows: () => [] })), []);
  assert.deepEqual(servicesForSurface(dbEntries, dbTarget({ columns: [] })), []);

  // httpResponse: text types only; over 10 MiB the response was cut.
  const types = fileProvider({ entryId: "types", service: "json.types", title: "Types", input: { kind: "file", accepts: [".json"] }, surfaces: ["httpResponse"] });
  const anyText = fileProvider({ entryId: "text", service: "text.any", title: "Any text", input: { kind: "file", accepts: ["text/*", ".json", ".txt"] }, surfaces: ["httpResponse"] });
  const httpEntries = resolvePluginServices([types, anyText]);
  assert.deepEqual(servicesForSurface(httpEntries, httpTarget()).map((group) => group.service), ["text.any", "json.types"]);
  assert.deepEqual(servicesForSurface(httpEntries, httpTarget({}, { contentType: "image/png" })), []);
  assert.deepEqual(servicesForSurface(httpEntries, httpTarget({}, { contentType: "text/html; charset=utf-8" })).map((group) => group.service), ["text.any"]);
  const truncated = servicesForSurface(httpEntries, httpTarget({}, { sizeBytes: 10485761 }));
  assert.deepEqual(truncated.map((group) => group.disabledReason), ["responseTruncated", "responseTruncated"]);
  assert.equal(servicesForSurface(httpEntries, httpTarget({}, { sizeBytes: 10485760 }))[0].disabledReason, undefined);

  // fileMenu keeps servicesForFiles' answer.
  const ocr = resolvePluginServices([fileProvider()]);
  assert.deepEqual(servicesForSurface(ocr, { surface: "fileMenu", files: [{ name: "a.png", absolutePath: "/a.png" }] }).map((group) => group.service), ["image.ocr"]);
  assert.deepEqual(servicesForSurface(ocr, sftpTarget({ file: { name: "a.png", size: 1 } })), [], "fileMenu entries stay off the new surfaces");
});

test("service-surfaces: buildDbResultFile", () => {
  const rows = [[1, "a", undefined, 10n, new Date(0), Number.NaN], [2, null, true, -1n, { x: 1 }, 1.5]];
  const target = dbTarget({
    columns: [{ name: "id", type: "bigint" }, { name: "name", type: "text" }, { name: "flag", type: null }, { name: "big", type: "int8" }, { name: "at", type: null }, { name: "n", type: null }],
    rows: () => rows,
    hasMore: true,
    view: { filtered: true, sorted: false, hiddenColumns: 2 },
  });
  const file = buildDbResultFile(target, 16777216);
  const parsed = JSON.parse(file.text);
  assert.deepEqual(Object.keys(parsed), ["format", "version", "columns", "rows", "rowCount", "truncated", "hasMore", "view"]);
  assert.equal(parsed.format, "mincode.dbResult");
  assert.equal(parsed.version, 1);
  assert.deepEqual(parsed.columns.map((column) => [column.name, column.type]), [["id", "bigint"], ["name", "text"], ["flag", null], ["big", "int8"], ["at", null], ["n", null]]);
  assert.deepEqual(parsed.rows, [
    [1, "a", null, "10", `fmt:${String(new Date(0))}`, "fmt:NaN"],
    [2, null, true, "-1", "fmt:[object Object]", 1.5],
  ]);
  assert.deepEqual([parsed.rowCount, parsed.truncated, parsed.hasMore], [2, false, true]);
  assert.deepEqual(parsed.view, { filtered: true, sorted: false, hiddenColumns: 2 });
  assert.deepEqual([file.rowCount, file.truncated], [2, false]);

  // Cells beyond the visible columns are not handed over.
  assert.deepEqual(JSON.parse(buildDbResultFile(dbTarget({ rows: () => [[1, "a", "hidden"]] }), 4096).text).rows, [[1, "a"]]);

  // Byte limit: whole rows only, the result stays within maxBytes and parses.
  const many = Array.from({ length: 500 }, (_, index) => [index, `user-${index}@example.com`]);
  const cut = buildDbResultFile(dbTarget({ rows: () => many }), 4096);
  assert.ok(new TextEncoder().encode(cut.text).length <= 4096);
  const cutParsed = JSON.parse(cut.text);
  assert.equal(cut.truncated, true);
  assert.equal(cutParsed.truncated, true);
  assert.equal(cutParsed.rowCount, cut.rowCount);
  assert.equal(cutParsed.rows.length, cut.rowCount);
  assert.ok(cut.rowCount > 10 && cut.rowCount < 500);
  assert.deepEqual(cutParsed.rows.at(-1), many[cut.rowCount - 1]);

  // Row limit.
  const tooMany = Array.from({ length: SERVICE_LIMITS.dbResultMaxRows + 1 }, (_, index) => [index]);
  const capped = buildDbResultFile(dbTarget({ columns: [{ name: "n", type: null }], rows: () => tooMany }), 16777216);
  assert.deepEqual([capped.rowCount, capped.truncated], [SERVICE_LIMITS.dbResultMaxRows, true]);
  assert.equal(JSON.parse(capped.text).rows.length, SERVICE_LIMITS.dbResultMaxRows);

  // Columns alone over the limit, or not one row fitting: too large, no file at all.
  const wideColumns = Array.from({ length: 40 }, (_, index) => ({ name: `column_${index}_${"x".repeat(100)}`, type: null }));
  assert.deepEqual(buildDbResultFile(dbTarget({ columns: wideColumns, rows: () => [[1]] }), 4096), { text: "", rowCount: 0, truncated: true, tooLarge: true });
  const hugeRow = buildDbResultFile(dbTarget({ rows: () => [[1, "y".repeat(5000)], [2, "z"]] }), 4096);
  assert.deepEqual(hugeRow, { text: "", rowCount: 0, truncated: true, tooLarge: true });
  const refused = surfaceRunRequest(dbTarget({ rows: () => [[1, "y".repeat(5000)]] }), { service: "s.x", title: "x", fits: true, providers: [{ input: { kind: "file", accepts: [".json"], maxBytes: 4096 } }] });
  assert.deepEqual(refused, { service: "s.x", surface: "dbResult", refused: { key: "plugins.services.dbResultTooLarge" } });
});

test("service-surfaces: httpResponseFileName", () => {
  for (const [contentType, want] of Object.entries({
    "": "response.txt",
    "application/json": "response.json",
    "application/vnd.api+json; charset=utf-8": "response.json",
    "Application/JSON; charset=UTF-8": "response.json",
    "text/json": "response.json",
    "application/x-ndjson": "response.ndjson",
    "application/xml": "response.xml",
    "text/xml": "response.xml",
    "application/atom+xml": "response.xml",
    "text/html; charset=utf-8": "response.html",
    "application/javascript": "response.js",
    "application/ecmascript": "response.js",
    "text/javascript": "response.js",
    "text/csv": "response.csv",
    "application/csv": "response.csv",
    "application/yaml": "response.yaml",
    "application/x-yaml": "response.yaml",
    "text/markdown": "response.md",
    "text/plain": "response.txt",
    "application/toml": "response.txt",
    "application/sql": "response.txt",
    "application/graphql": "response.txt",
    "application/x-www-form-urlencoded": "response.txt",
  })) {
    assert.equal(httpResponseFileName(contentType), want, contentType);
    assert.equal(isTextResponseType(contentType), true, contentType);
  }
  for (const contentType of ["image/png", "application/octet-stream", "application/pdf", "video/mp4", "garbage", "application/json\x00", "multipart/form-data; boundary=x", `text/${"x".repeat(260)}`]) {
    assert.equal(httpResponseFileName(contentType), null, contentType);
    assert.equal(isTextResponseType(contentType), false, contentType);
  }
});

function sharedText(value) {
  return typeof value === "string" ? value : value.repeat.repeat(value.count);
}

test("service-surfaces: redactResponseHeaders", () => {
  // The vectors the host's filterHeaders is tested with (TestSurfaceContextSharedHeaderVectors).
  assert.ok(sharedHeaderVectors.cases.length >= 3);
  for (const vector of sharedHeaderVectors.cases) {
    const headers = vector.headers.map(([name, value]) => [sharedText(name), sharedText(value)]);
    const got = redactResponseHeaders(headers, vector.secrets ?? []);
    assert.deepEqual(got.headers, vector.kept.map(([name, value]) => [sharedText(name), sharedText(value)]), vector.name);
    assert.equal(got.redacted, vector.redacted, vector.name);
    assert.equal(got.truncated, vector.truncated, vector.name);
  }

  // Secrets shorter than 6 characters are not compared; 6 are.
  assert.equal(redactResponseHeaders([["X-A", "abc123"]], ["abc123"]).redacted, 1);
  assert.equal(redactResponseHeaders([["X-A", "abc12"]], ["abc12"]).redacted, 0);
  // At most 32 headers; the rest set truncated and are not counted.
  const many = Array.from({ length: 40 }, (_, index) => [`X-H${String(index).padStart(2, "0")}`, "v"]);
  const capped = redactResponseHeaders(many, []);
  assert.deepEqual([capped.headers.length, capped.redacted, capped.truncated, capped.headers[0][0]], [32, 0, true, "X-H00"]);
  // Characters the host refuses are removed instead of failing the call.
  assert.deepEqual(redactResponseHeaders([["X-\u2066A", "a\u200bb\u2029c\td"]], []).headers, [["X-A", "abc\td"]]);
  assert.deepEqual(redactResponseHeaders([["\u200b", "v"]], []), { headers: [], redacted: 0, truncated: false });
  // The userinfo detector agrees with the host's backstop.
  for (const url of ["https://u:p@h/x", "https:/u:p@h/x", "https:\\\\u:p@h/x", "//u@h.example/x", "u:p@h.example/x", "HTTPS://U%40H.example/x"]) {
    assert.equal(urlHasUserinfo(url), true, url);
  }
  for (const url of ["https://api.example.com/users/@me", "/v1/users/@me", "{{baseUrl}}/users/@me", "https://h/x@"]) {
    assert.equal(urlHasUserinfo(url), false, url);
  }
});

test("service-surfaces: terminal needs an explicit selection", () => {
  const explain = provider({ entryId: "explain", service: "term.explain", title: "Explain", input: { kind: "text", maxBytes: 16 }, surfaces: ["terminalSelection"] });
  const entries = resolvePluginServices([explain, provider()]);
  const target = (selection) => ({ surface: "terminalSelection", kind: "ssh", selection });
  assert.deepEqual(servicesForSurface(entries, target("")), []);
  assert.deepEqual(servicesForSurface(entries, target("\u001b[31m\u001b[0m")), [], "nothing left after normalizing");
  assert.deepEqual(servicesForSurface(entries, target("error: boom")).map((group) => [group.service, group.disabledReason]), [["term.explain", undefined]]);
  assert.equal(servicesForSurface(entries, target("x".repeat(17)))[0].disabledReason, "tooLarge");

  assert.equal(normalizeTerminalServiceText("\u001b[31mred\u001b[0m\r\nnext\rline"), "red\nnext\nline");
  assert.equal(normalizeTerminalServiceText("a\u001b]0;title\u0007b\u001b]8;;https://x\u001b\\c"), "abc");
  assert.equal(normalizeTerminalServiceText("a\u0000b\u0085c\u009b1md\u007fe"), "abcde");
  assert.equal(normalizeTerminalServiceText("col1\tcol2\n"), "col1\tcol2\n");
  assert.equal(normalizeTerminalServiceText("bell\u0007 back\u0008"), "bell back");
});

test("service-surfaces: surfaceRunRequest", () => {
  const group = (overrides = {}) => ({ service: "demo.svc", title: "Demo", fits: true, providers: [], ...overrides });

  // sftpFile: the session and path only, never a credential.
  const remote = { ...sftpTarget().remote, connection: { ...sftpConnection, password: "hunter2-secret", privateKey: "-----BEGIN KEY-----" } };
  const sftp = surfaceRunRequest(sftpTarget({ remote }), group());
  assert.deepEqual(sftp, {
    service: "demo.svc",
    surface: "sftpFile",
    remoteFile: { sessionId: "sess-1", path: "/var/log/app.log" },
    surfaceContext: { remote: { connection: sftpConnection, path: "/var/log/app.log" } },
  });
  assert.ok(!JSON.stringify(sftp).includes("hunter2") && !JSON.stringify(sftp).includes("BEGIN"));
  const longPath = `/${"p".repeat(2048)}`;
  assert.deepEqual(surfaceRunRequest(sftpTarget({ remote: { ...sftpTarget().remote, path: longPath } }), group()).surfaceContext, { remoteOmitted: true });
  assert.deepEqual(surfaceRunRequest(sftpTarget({ remote: { ...sftpTarget().remote, path: "/a\u202egol.exe" } }), group()).surfaceContext, { remoteOmitted: true });
  const oddHost = surfaceRunRequest(sftpTarget({ remote: { ...sftpTarget().remote, connection: { ...sftpConnection, host: "h\u200b.example" } } }), group());
  assert.equal(oddHost.surfaceContext.remote.connection.host, "h.example");
  // A path within 2048 bytes that JSON escaping doubles past the context limit: remoteOmitted, not a refused call.
  const quoted = `/${'"'.repeat(2040)}`;
  const escaped = surfaceRunRequest(sftpTarget({ remote: { ...sftpTarget().remote, path: quoted } }), group());
  assert.deepEqual(escaped.surfaceContext, { remoteOmitted: true });
  assert.deepEqual(escaped.remoteFile, { sessionId: "sess-1", path: quoted });
  // Long names in the database context: the table, then the database name go; the context always fits.
  const longName = '"'.repeat(1000);
  const tree = (overrides) => ({ connection: { id: "d1", name: "orders", kind: "mysql" }, connected: true, objectType: "table", database: "shop", table: { name: "t", kind: "table" }, ...overrides });
  const fitsContext = (value) => new TextEncoder().encode(JSON.stringify(value)).length <= SERVICE_LIMITS.surfaceContextMaxBytes;
  const wide = group({ providers: [{ input: { kind: "file", accepts: [".json"], maxBytes: 67108864 } }] });
  const longTable = surfaceRunRequest(dbTarget({ database: tree({ database: longName, table: { name: longName, kind: "table" } }) }), wide);
  assert.ok(fitsContext(longTable.surfaceContext));
  assert.equal(longTable.surfaceContext.database.table, undefined);
  assert.equal(longTable.surfaceContext.database.database, longName);
  const allLong = surfaceRunRequest(dbTarget({ database: tree({ connection: { id: longName, name: longName, kind: longName }, database: longName, table: { name: longName, kind: "t" } }) }), wide);
  assert.equal(allLong.surfaceContext, undefined);
  assert.ok(allLong.content.text.length > 0);

  // dbResult: content only now; database context from the tree target's fields.
  const rows = [];
  for (let index = 0; index < 400; index++) rows.push([index, `user-${index}@example.com`]);
  const database = { connection: { id: "d1", name: "orders", kind: "postgresql", password: "x" }, connected: true, objectType: "table", database: "shop", table: { name: "orders", kind: "TABLE" } };
  const small = group({ providers: [{ input: { kind: "file", accepts: [".json"], maxBytes: 4096 } }] });
  const db = surfaceRunRequest(dbTarget({ rows: () => rows, database }), small);
  assert.equal(db.surface, "dbResult");
  assert.ok(new TextEncoder().encode(db.content.text).length <= 4096);
  assert.equal(db.content.contentType, undefined);
  assert.deepEqual(db.surfaceContext, { database: { connection: { id: "d1", name: "orders", kind: "postgresql" }, database: "shop", table: { name: "orders", kind: "table" } } });
  assert.deepEqual(db.notice, { key: "plugins.services.dbResultTruncated", count: JSON.parse(db.content.text).rowCount });
  const whole = surfaceRunRequest(dbTarget(), group({ providers: [{ input: { kind: "file", accepts: [".json"], maxBytes: 67108864 } }] }));
  assert.equal(whole.notice, undefined);
  assert.equal(whole.surfaceContext, undefined);

  // httpResponse: body as received, request URL sanitized, headers filtered.
  const http = surfaceRunRequest(httpTarget({
    secrets: ["tok-123456"],
    request: { method: "post", url: "  https://user:pw@api.example.com/v1/orders/abcdef0123456789abcdef0123?token=abc#frag  " },
  }, {
    headers: [["Content-Type", "application/json"], ["Set-Cookie", "sid=1"], ["X-Echo", "tok-123456"], ["Location", "https://app.example/cb?code=1"]],
    body: "{\"a\":1}",
  }), group());
  assert.deepEqual(http.content, { text: "{\"a\":1}", contentType: "application/json" });
  assert.deepEqual(http.surfaceContext.request, { method: "POST", url: "https://api.example.com/v1/orders/{redacted}", redacted: true });
  assert.deepEqual(http.surfaceContext.response, {
    status: 200, statusText: "OK", contentType: "application/json", elapsedMs: 12.5,
    headers: [["Content-Type", "application/json"], ["Location", "https://app.example/cb"]],
    headersRedacted: 3,
  });
  // Large headers are dropped from the end so the context fits.
  const big = Array.from({ length: 20 }, (_, index) => [`X-L${index}`, "v".repeat(500)]);
  const fitted = surfaceRunRequest(httpTarget({}, { headers: big }), group());
  assert.ok(new TextEncoder().encode(JSON.stringify(fitted.surfaceContext)).length <= SERVICE_LIMITS.surfaceContextMaxBytes);
  assert.equal(fitted.surfaceContext.response.headersTruncated, true);
  // An address the host would refuse is left out, not sent.
  const tooLong = surfaceRunRequest(httpTarget({ request: { method: "GET", url: `https://a.example/${"x".repeat(2100)}` } }), group());
  assert.equal(tooLong.surfaceContext.request, undefined);

  // terminalSelection: normalized text and the terminal kind.
  assert.deepEqual(surfaceRunRequest({ surface: "terminalSelection", kind: "docker", selection: "\u001b[1mok\u001b[0m\r\n" }, group()), {
    service: "demo.svc", surface: "terminalSelection", input: "ok\n", surfaceContext: { terminal: { kind: "docker" } },
  });
  // fileMenu: the paths.
  assert.deepEqual(surfaceRunRequest({ surface: "fileMenu", files: [{ name: "a.png", absolutePath: "/w/a.png" }] }, group()), { service: "demo.svc", surface: "fileMenu", paths: ["/w/a.png"] });
});

test("service-surfaces: HTTP provider choice follows the host, never a silent switch", () => {
  const MiB = 1048576;
  const types = (pluginId, maxBytes, isDefault = false) => fileProvider({
    pluginId, pluginName: pluginId, entryId: "types", service: "json.types", title: "Types",
    input: { kind: "file", accepts: [".json"], maxBytes }, surfaces: ["httpResponse"], isDefault,
  });
  const body = "x".repeat(5 * MiB);
  const target = httpTarget({}, { body, sizeBytes: 5 * MiB });
  const run = (entries) => {
    const [group] = servicesForSurface(resolvePluginServices(entries), target);
    return { group, request: surfaceRunRequest(target, group) };
  };

  // The default D is too small: the group still holds every provider (as the host's input.fits), the item is
  // live, and the click refuses with a message instead of pinning E or F.
  const tooSmallDefault = run([types("acme-d", MiB, true), types("acme-e", 16 * MiB), types("acme-f", 16 * MiB)]);
  assert.deepEqual(tooSmallDefault.group.providers.map((entry) => entry.pluginId), ["acme-d", "acme-e", "acme-f"]);
  assert.equal(tooSmallDefault.group.disabledReason, undefined);
  assert.deepEqual(tooSmallDefault.request, { service: "json.types", surface: "httpResponse", refused: { key: "plugins.services.responseTooLarge" } });

  // Only E fits and there is no default: the host would ask among all, so the smallest limit applies.
  const asked = run([types("acme-d", MiB), types("acme-e", 16 * MiB)]);
  assert.equal(asked.request.provider, undefined);
  assert.deepEqual(asked.request.refused, { key: "plugins.services.responseTooLarge" });
  const askedSmall = surfaceRunRequest(httpTarget({}, { body: "{}", sizeBytes: 2 }), servicesForSurface(resolvePluginServices([types("acme-d", MiB), types("acme-e", 16 * MiB)]), httpTarget({}, { body: "{}", sizeBytes: 2 }))[0]);
  assert.equal(askedSmall.provider, undefined);
  assert.equal(askedSmall.content.text, "{}");

  // A default that fits is pinned.
  const fits = run([types("acme-d", 16 * MiB, true), types("acme-e", MiB)]);
  assert.deepEqual(fits.request.provider, { pluginId: "acme-d", entryId: "types" });
  assert.equal(fits.request.content.text, body);

  // Over every provider's limit: greyed out at menu time.
  const none = run([types("acme-d", MiB, true), types("acme-e", 2 * MiB)]);
  assert.equal(none.group.disabledReason, "tooLargeFile");
});

test("service-surfaces: no SFTP entry for a path the host refuses", async () => {
  const { sftpFileServiceTarget } = await import("../src/domain/sftp/menu-target.ts");
  const row = (path) => ({ name: path.slice(path.lastIndexOf("/") + 1), path, isDir: false, size: 10, permissions: "-rw-r--r--" });
  assert.equal(sftpFileServiceTarget("s1", sftpConnection, row("/var/log/app.log"))?.surface, "sftpFile");
  for (const path of ["/var/log/a\u0000b.log", "/var/log/a\rb.log", "/var/log/a\nb.log"]) {
    assert.equal(sftpFileServiceTarget("s1", sftpConnection, row(path)), null, JSON.stringify(path));
  }
  assert.equal(sftpFileServiceTarget("s1", sftpConnection, { ...row("/x/link"), permissions: "lrwxrwxrwx" }), null);
});

test("service-surfaces: content is sized to the provider that will run", () => {
  const MiB = 1048576;
  const chart = (overrides) => fileProvider({ entryId: "chart", service: "data.chart", title: "Chart", input: { kind: "file", accepts: [".json"], maxBytes: MiB }, surfaces: ["dbResult", "httpResponse"], ...overrides });
  const small = chart({ pluginId: "acme-small", pluginName: "Small", input: { kind: "file", accepts: [".json"], maxBytes: MiB } });
  const big = chart({ pluginId: "acme-big", pluginName: "Big", input: { kind: "file", accepts: [".json"], maxBytes: 16 * MiB } });
  // About 5 MiB of rows.
  const rows = Array.from({ length: 5000 }, (_, index) => [index, `${"r".repeat(1040)}`]);
  const target = dbTarget({ rows: () => rows });
  const bytes = (request) => new TextEncoder().encode(request.content.text).length;
  const groupFor = (entries) => servicesForSurface(resolvePluginServices(entries), target)[0];

  // The default (1 MiB) runs although another provider takes 16 MiB: sized to and pinned on the default.
  const withDefault = surfaceRunRequest(target, groupFor([{ ...small, isDefault: true }, big]));
  assert.deepEqual(withDefault.provider, { pluginId: "acme-small", entryId: "chart" });
  assert.ok(bytes(withDefault) <= MiB);
  assert.equal(withDefault.notice?.key, "plugins.services.dbResultTruncated");
  // The only provider: its own limit, pinned.
  const only = surfaceRunRequest(target, groupFor([big]));
  assert.deepEqual(only.provider, { pluginId: "acme-big", entryId: "chart" });
  assert.ok(bytes(only) > 4 * MiB && bytes(only) <= 16 * MiB);
  assert.equal(only.notice, undefined);
  // Several and no default: the host asks the user, so the smallest limit, nothing pinned.
  const asked = surfaceRunRequest(target, groupFor([small, big]));
  assert.equal(asked.provider, undefined);
  assert.ok(bytes(asked) <= MiB);
  assert.ok(asked.notice);

  // httpResponse: a body over the running provider's limit (UTF-8, not the reported size) is refused, never cut.
  const body = "汉".repeat(400000); // 1.2 MB of UTF-8
  const httpGroup = (entries) => servicesForSurface(resolvePluginServices(entries), httpTarget({}, { body, sizeBytes: 1000 }))[0];
  assert.deepEqual(surfaceRunRequest(httpTarget({}, { body, sizeBytes: 1000 }), httpGroup([{ ...small, isDefault: true }, big])), {
    service: "data.chart", surface: "httpResponse", refused: { key: "plugins.services.responseTooLarge" },
  });
  const fits = surfaceRunRequest(httpTarget({}, { body, sizeBytes: 1000 }), httpGroup([{ ...big, isDefault: true }, small]));
  assert.deepEqual(fits.provider, { pluginId: "acme-big", entryId: "chart" });
  assert.equal(fits.content.text, body);
});
