import assert from "node:assert/strict";
import { readdir } from "node:fs/promises";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import { URL } from "node:url";
import {
  DATABASE_DRIVER_CATEGORIES,
  DATABASE_DRIVER_CONNECTION_FIELDS,
  DATABASE_DRIVER_DESCRIPTION_MAX_RUNES,
  DATABASE_DRIVER_ENUM_MAX,
  DATABASE_DRIVER_FEATURES,
  DATABASE_DRIVER_FILE_EXTENSIONS_MAX,
  DATABASE_DRIVER_LOCALES_MAX,
  DATABASE_DRIVER_MAX_ENTRIES,
  DATABASE_DRIVER_OPTION_KEY_MAX_LENGTH,
  DATABASE_DRIVER_OPTION_STRING_MAX_LENGTH,
  DATABASE_DRIVER_OPTIONS_MAX,
  DATABASE_DRIVER_REPLACEABLE_TYPES,
  DATABASE_DRIVER_SECRET_FIELDS,
  DATABASE_DRIVER_SQL_DIALECTS,
  DATABASE_DRIVER_TITLE_MAX_RUNES,
  applyDatabaseDriverRebind,
  bindConnectionToPluginDriver,
  buildDatabaseDriverCatalog,
  createCredentialReleaseGate,
  databaseDriverReleaseAllowed,
  databaseDriverBuiltinCandidates,
  databaseDriverDialectType,
  databaseDriverErrorCode,
  databaseDriverOptionDefaults,
  databaseDriverDescription,
  databaseDriverOptionFields,
  databaseDriverPluginSearch,
  databaseDriverPreviewSummary,
  databaseDriverRebindCandidates,
  databaseDriverSecretFields,
  databaseDriverSupports,
  databaseDriverTitle,
  invalidDatabaseDriverOptions,
  missingDatabaseDriverFields,
  normalizeDatabaseDriverRef,
  parseDatabaseDriverGrantStatuses,
  parsePluginDatabaseDriverEntry,
  parsePluginDatabaseDriverKey,
  pluginDatabaseDriverKey,
  resolvePluginDatabaseDrivers,
  sanitizeDatabaseDriverOptions,
  unbindConnectionDriver,
} from "../src/domain/plugins/database-drivers.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");
const ICON = "b".repeat(64);
const PLUGIN = { pluginId: "acme.duck", pluginName: "Acme Duck", version: "1.2.0" };

function entry(overrides = {}) {
  return {
    id: "duck",
    title: "Duck Pro",
    titleLocalized: { zh: "鸭子" },
    description: "Fast engine",
    icon: ICON,
    category: "analytics",
    sqlDialect: "duckdb",
    replaces: "duckdb",
    connection: {
      fields: ["filePath", "password"],
      required: ["filePath"],
      fileExtensions: [".duckdb", ".db"],
      options: {
        readOnly: { type: "boolean", title: "Read only", default: false },
        threads: { type: "integer", minimum: 1, maximum: 64, default: 4 },
        mode: { type: "string", enum: ["fast", "safe"], default: "safe" },
        label: { type: "string", maxLength: 32 },
      },
    },
    features: ["cancel", "metadata.tables", "metadata.columns", "metadata.primaryKeys", "rows.update"],
    specFingerprint: "c".repeat(64),
    ...overrides,
  };
}

function contribution(pluginId, entries, overrides = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: "", entries, issues: [] },
    ...overrides,
  };
}

function connection(overrides = {}) {
  return {
    id: "c1",
    name: "Local",
    dbType: "duckdb",
    host: "",
    port: 0,
    username: "",
    password: "secret",
    database: "",
    filePath: "/tmp/a.duckdb",
    savePassword: true,
    createdAt: 0,
    updatedAt: 0,
    ...overrides,
  };
}

test("contract constants mirror both contribution-points.json copies", async () => {
  for (const path of ["../../plugins/api/contribution-points.json", "../src/domain/plugins/contribution-points.json"]) {
    const point = JSON.parse(await readSource(path)).points.databaseDrivers;
    assert.deepEqual(point.kinds, ["process"]);
    assert.deepEqual(point.requiresCapabilities, ["database.driver"]);
    assert.deepEqual(point.assets.selectors, ["icon"]);
    assert.equal(point.maxEntries, DATABASE_DRIVER_MAX_ENTRIES);
    assert.equal(point.limits.titleMaxRunes, DATABASE_DRIVER_TITLE_MAX_RUNES);
    assert.equal(point.limits.descriptionMaxRunes, DATABASE_DRIVER_DESCRIPTION_MAX_RUNES);
    assert.equal(point.limits.localesMax, DATABASE_DRIVER_LOCALES_MAX);
    assert.equal(point.limits.optionsMax, DATABASE_DRIVER_OPTIONS_MAX);
    assert.equal(point.limits.optionKeyMaxLength, DATABASE_DRIVER_OPTION_KEY_MAX_LENGTH);
    assert.equal(point.limits.optionStringMaxLength, DATABASE_DRIVER_OPTION_STRING_MAX_LENGTH);
    assert.equal(point.limits.enumMax, DATABASE_DRIVER_ENUM_MAX);
    assert.equal(point.limits.fileExtensionsMax, DATABASE_DRIVER_FILE_EXTENSIONS_MAX);
    assert.deepEqual(point.lists.connectionFields, [...DATABASE_DRIVER_CONNECTION_FIELDS]);
    assert.deepEqual(point.lists.secretFields, [...DATABASE_DRIVER_SECRET_FIELDS]);
    assert.deepEqual(point.lists.features, [...DATABASE_DRIVER_FEATURES]);
    assert.deepEqual(point.lists.sqlDialects, [...DATABASE_DRIVER_SQL_DIALECTS]);
    assert.deepEqual(point.lists.categories, [...DATABASE_DRIVER_CATEGORIES]);
    assert.deepEqual(point.lists.replaceableTypes, [...DATABASE_DRIVER_REPLACEABLE_TYPES]);
  }
});

test("parses a valid entry into a flattened, re-validated descriptor", () => {
  const driver = parsePluginDatabaseDriverEntry(entry(), PLUGIN);
  assert.ok(driver);
  assert.equal(driver.key, "plugin:acme.duck/duck");
  assert.equal(driver.driverId, "duck");
  assert.equal(driver.pluginVersion, "1.2.0");
  assert.equal(driver.dbType, "duckdb");
  assert.equal(driver.dialectType, "duckdb");
  assert.deepEqual(driver.fields, ["filePath", "password"]);
  assert.deepEqual(driver.fileExtensions, [".duckdb", ".db"]);
  assert.equal(driver.icon, ICON);
  assert.equal(databaseDriverTitle(driver, ["zh-CN", "zh"]), "鸭子");
  assert.equal(databaseDriverTitle(driver, ["en"]), "Duck Pro");
  assert.ok(databaseDriverSupports(driver, "rows.update"));
  assert.ok(!databaseDriverSupports(driver, "destructive.dropTable"));
  assert.deepEqual(databaseDriverSecretFields(driver), ["password"]);
});

test("strips unsafe text from titles", () => {
  const driver = parsePluginDatabaseDriverEntry(entry({ title: "Duck‮\u0007 Pro" }), PLUGIN);
  assert.equal(driver.title, "Duck Pro");
});

test("rejects out-of-contract entries as a whole", () => {
  const rejects = [
    entry({ id: "Duck" }),
    entry({ id: "a".repeat(33) }),
    entry({ sqlDialect: "cobol" }),
    entry({ category: "graph" }),
    entry({ replaces: "mysql" }),
    entry({ icon: "icons/duck.svg" }),
    entry({ features: ["rows.update"] }),
    entry({ features: ["destructive.dropTable"] }),
    entry({ features: ["teleport"] }),
    entry({ connection: { fields: [] } }),
    entry({ connection: { fields: ["host", "shell"] } }),
    entry({ connection: { fields: ["host"], required: ["port"] } }),
    entry({ connection: { fields: ["host"], defaultPort: 5432 } }),
    entry({ connection: { fields: ["host", "port"], defaultPort: 70000 } }),
    entry({ connection: { fields: ["host"], fileExtensions: [".db"] } }),
    entry({ connection: { fields: ["filePath"], fileExtensions: ["db"] } }),
    entry({ connection: { fields: ["host"], options: { apiKey: { type: "string", secret: true } } } }),
    entry({ connection: { fields: ["host"], options: { "bad-key": { type: "string" } } } }),
    entry({ connection: { fields: ["host"], options: { rx: { type: "string", pattern: ".*" } } } }),
    entry({ connection: { fields: ["host"], options: { big: { type: "string", maxLength: 4096 } } } }),
    entry({ connection: { fields: ["host"], options: { list: { type: "array" } } } }),
    entry({ connection: { fields: ["host"], options: { n: { type: "integer", default: "x" } } } }),
    entry({ connection: { fields: ["host"], options: { e: { type: "string", enum: ["a", "a"] } } } }),
  ];
  for (const value of rejects) assert.equal(parsePluginDatabaseDriverEntry(value, PLUGIN), undefined, JSON.stringify(value));
  const tooMany = Object.fromEntries(
    Array.from({ length: DATABASE_DRIVER_OPTIONS_MAX + 1 }, (_, index) => [`o${index}`, { type: "boolean" }]),
  );
  assert.equal(parsePluginDatabaseDriverEntry(entry({ connection: { fields: ["host"], options: tooMany } }), PLUGIN), undefined);
  assert.equal(parsePluginDatabaseDriverEntry(entry(), { ...PLUGIN, pluginId: "../x" }), undefined);
});

test("dialect representative: replaces first, generic maps to the neutral plugin type", () => {
  assert.equal(databaseDriverDialectType("postgres"), "postgres");
  assert.equal(databaseDriverDialectType("generic"), "plugin");
  assert.equal(databaseDriverDialectType("generic", "tdengine"), "tdengine");
  const generic = parsePluginDatabaseDriverEntry(entry({ replaces: undefined, sqlDialect: "generic" }), PLUGIN);
  assert.equal(generic.dbType, "plugin");
  assert.equal(generic.dialectType, "plugin");
  const pg = parsePluginDatabaseDriverEntry(entry({ replaces: undefined, sqlDialect: "postgres" }), PLUGIN);
  assert.equal(pg.dbType, "plugin");
  assert.equal(pg.dialectType, "postgres");
});

test("resolves only process contributions, deduplicated, capped and sorted", () => {
  const many = Array.from({ length: DATABASE_DRIVER_MAX_ENTRIES + 2 }, (_, index) => entry({ id: `d${index}` }));
  const { drivers, issues } = resolvePluginDatabaseDrivers([
    contribution("z.plugin", [entry(), entry(), entry({ id: "Bad" })]),
    contribution("a.plugin", many),
    contribution("declarative.plugin", [entry()], { runtime: "declarative" }),
  ]);
  assert.equal(drivers.filter((driver) => driver.pluginId === "a.plugin").length, DATABASE_DRIVER_MAX_ENTRIES);
  assert.equal(drivers.filter((driver) => driver.pluginId === "z.plugin").length, 1);
  assert.ok(!drivers.some((driver) => driver.pluginId === "declarative.plugin"));
  assert.equal(drivers[0].pluginId, "a.plugin");
  assert.deepEqual(
    issues.map((issue) => issue.reason).sort(),
    ["duplicate id", "invalid entry"],
  );
});

test("options: fields, defaults, sanitizing and validation", () => {
  const driver = parsePluginDatabaseDriverEntry(entry(), PLUGIN);
  assert.deepEqual(databaseDriverOptionDefaults(driver), { readOnly: false, threads: 4, mode: "safe" });
  const fields = databaseDriverOptionFields(driver, "en");
  assert.deepEqual(fields.map((field) => field.control).sort(), ["boolean", "enum", "integer", "string"]);
  assert.deepEqual(
    sanitizeDatabaseDriverOptions(driver, { threads: 8, mode: "wild", unknown: 1, label: "x", readOnly: "yes" }),
    { threads: 8, label: "x" },
  );
  assert.deepEqual(invalidDatabaseDriverOptions(driver, { threads: 100, mode: "safe", label: "" }), ["threads"]);
  assert.deepEqual(invalidDatabaseDriverOptions(driver, { threads: undefined }), []);
});

test("options and description follow the interface language", () => {
  const driver = parsePluginDatabaseDriverEntry(entry({
    description: "Embedded engine",
    descriptionLocalized: { zh: "嵌入式引擎" },
    connection: {
      fields: ["filePath"],
      options: {
        mode: {
          type: "string", title: "Mode", titleLocalized: { zh: "模式" },
          description: "How to open", descriptionLocalized: { zh: "打开方式" },
          enum: ["fast", "safe"], enumLabelsLocalized: { zh: ["快速", "安全"], en: ["Fast", "Safe"] }, default: "safe",
        },
      },
    },
  }), PLUGIN);
  assert.equal(databaseDriverDescription(driver, "zh-CN"), "嵌入式引擎");
  assert.equal(databaseDriverDescription(driver, "fr"), "Embedded engine");
  const [zh] = databaseDriverOptionFields(driver, ["zh-CN", "zh"]);
  assert.equal(zh.title, "模式");
  assert.equal(zh.description, "打开方式");
  assert.deepEqual(zh.options, [{ value: "fast", label: "快速" }, { value: "safe", label: "安全" }]);
  const [en] = databaseDriverOptionFields(driver, "en");
  assert.equal(en.description, "How to open");
  assert.deepEqual(en.options.map((option) => option.label), ["Fast", "Safe"]);
});

test("required fields and binding keep credentials and never auto-route", () => {
  const driver = parsePluginDatabaseDriverEntry(entry(), PLUGIN);
  assert.deepEqual(missingDatabaseDriverFields(driver, { filePath: " " }), ["filePath"]);
  assert.deepEqual(missingDatabaseDriverFields(driver, { filePath: "/a" }), []);
  const bound = bindConnectionToPluginDriver(connection(), driver);
  assert.deepEqual(bound.driver, { pluginId: "acme.duck", driverId: "duck" });
  assert.equal(bound.dbType, "duckdb");
  assert.equal(bound.password, "secret");
  assert.deepEqual(bound.options, { readOnly: false, threads: 4, mode: "safe" });
  const rebound = bindConnectionToPluginDriver({ ...bound, options: { threads: 16, mode: "bad" } }, driver);
  assert.deepEqual(rebound.options, { readOnly: false, threads: 16, mode: "safe" });
  const unbound = unbindConnectionDriver(bound);
  assert.equal(unbound.driver, undefined);
  assert.equal(unbound.options, undefined);
  assert.equal(unbindConnectionDriver({ ...bound, dbType: "plugin" }), undefined);
});

test("bulk rebinding is explicit and type-preserving", () => {
  const driver = parsePluginDatabaseDriverEntry(entry(), PLUGIN);
  const connections = [
    connection({ id: "a" }),
    connection({ id: "b" }),
    connection({ id: "m", dbType: "mysql" }),
    connection({ id: "p", driver: { pluginId: "acme.duck", driverId: "duck" } }),
  ];
  assert.deepEqual(databaseDriverRebindCandidates(connections, driver).map((item) => item.id), ["a", "b"]);
  const changed = applyDatabaseDriverRebind(connections, ["a", "m", "p"], { kind: "plugin", driver });
  assert.deepEqual(changed.map((item) => item.id), ["a"]);
  assert.deepEqual(databaseDriverBuiltinCandidates(connections, "duckdb").map((item) => item.id), ["p"]);
  const back = applyDatabaseDriverRebind(connections, ["p", "a"], { kind: "builtin" });
  assert.deepEqual(back.map((item) => [item.id, item.driver]), [["p", undefined]]);
  const pluginOnly = parsePluginDatabaseDriverEntry(entry({ replaces: undefined }), PLUGIN);
  assert.deepEqual(databaseDriverRebindCandidates(connections, pluginOnly), []);
});

test("catalog lists supported built-ins then plugin drivers", () => {
  const driver = parsePluginDatabaseDriverEntry(entry(), PLUGIN);
  const catalog = buildDatabaseDriverCatalog(["mysql", "duckdb", "plugin"], new Set(["mysql"]), [driver]);
  assert.deepEqual(catalog.map((item) => item.key), ["builtin:mysql", "plugin:acme.duck/duck"]);
  assert.equal(catalog[1].dialectType, "duckdb");
});

test("driver refs and keys are validated", () => {
  assert.equal(pluginDatabaseDriverKey({ pluginId: "a.b", driverId: "x" }), "plugin:a.b/x");
  assert.deepEqual(parsePluginDatabaseDriverKey("plugin:a.b/x"), { pluginId: "a.b", driverId: "x" });
  assert.equal(parsePluginDatabaseDriverKey("builtin:mysql"), undefined);
  assert.equal(parsePluginDatabaseDriverKey("plugin:a.b/X"), undefined);
  assert.equal(normalizeDatabaseDriverRef({ pluginId: "a/b", driverId: "x" }), undefined);
});

test("grant statuses parse tolerantly and fail closed", () => {
  const refs = [
    { connectionId: "c1", pluginId: "p", driverId: "d" },
    { connectionId: "c2", pluginId: "p", driverId: "d" },
  ];
  const items = {
    items: [
      { connectionId: "c2", pluginId: "p", driverId: "d", available: false, reason: "disabled", granted: true },
      { connectionId: "c1", pluginId: "p", driverId: "d", available: true, granted: true },
    ],
  };
  const parsed = parseDatabaseDriverGrantStatuses(items, refs);
  assert.deepEqual(parsed.map((item) => [item.connectionId, item.available, item.reason, item.granted]), [
    ["c1", true, undefined, true],
    ["c2", false, "disabled", true],
  ]);
  const positional = parseDatabaseDriverGrantStatuses([{ available: true, granted: false }], refs);
  assert.deepEqual(positional.map((item) => [item.available, item.reason]), [[true, undefined], [false, "notInstalled"]]);
  const garbage = parseDatabaseDriverGrantStatuses({ statuses: [{ available: "yes", reason: "<b>" }] }, refs.slice(0, 1));
  assert.deepEqual(garbage[0], { ...refs[0], available: false, reason: "notInstalled", granted: false, trusted: false });
});

test("host error codes", () => {
  assert.equal(databaseDriverErrorCode(new Error("untrusted: connection c1 is not bound")), "untrusted");
  assert.equal(databaseDriverErrorCode("RuntimeError: unavailable: plugin stopped"), "unavailable");
  assert.equal(databaseDriverErrorCode("plain failure"), undefined);
});

test("install preview summarizes each declared driver", () => {
  const summary = databaseDriverPreviewSummary({
    databaseDrivers: [
      entry(),
      entry({ id: "g", title: "", sqlDialect: "nope", replaces: "mysql", features: ["metadata.tables", "destructive.clearTable"], connection: { fields: ["host", "token"] } }),
      "junk",
      entry({ id: "s", title: "Snap", features: ["transaction.snapshotWrite"], connection: { fields: ["host"] } }),
    ],
  });
  assert.deepEqual(summary, [
    { title: "Duck Pro", dialect: "duckdb", replaces: "duckdb", secretFields: ["password"], writes: true, destructive: false },
    { title: "g", dialect: "generic", secretFields: ["token"], writes: false, destructive: true },
    { title: "Snap", dialect: "duckdb", replaces: "duckdb", secretFields: [], writes: true, destructive: false },
  ]);
  assert.deepEqual(databaseDriverPreviewSummary(undefined), []);
});

test("wiring: consumer registered, dialog grants through the service, icons stay in image context", async () => {
  const index = await readSource("../src/services/contributions/index.ts");
  assert.match(index, /registerDatabaseDriverContributionConsumer,/);
  const dialog = await readSource("../src/components/database/DatabaseConnectionDialog.vue");
  assert.match(dialog, /grantDatabaseDriver\(grantRef\)/);
  assert.match(dialog, /confirmDatabaseDriverCredentialRelease\(/);
  assert.doesNotMatch(dialog, /v-html/);
  const icon = await readSource("../src/components/database/DatabaseTypeIcon.vue");
  assert.match(icon, /<img v-if="pluginIconUrl"/);
  assert.doesNotMatch(icon, /v-html/);
  const store = await readSource("../src/stores/database-drivers.ts");
  assert.match(store, /url\.startsWith\("blob:"\)/);
  const service = await readSource("../src/services/database-driver-service.ts");
  for (const command of ["grant", "revoke", "prune", "status", "trust_get", "trust_set"]) {
    assert.match(service, new RegExp(`"plugin_host_database_driver_${command}"`));
  }
  assert.match(service, /autofocus: false/);
  const view = await readSource("../src/views/PluginManagerWorkspaceView.vue");
  assert.match(view, /databaseDriverPreviewSummary\(preview\.value\?\.manifest\.contributes\)/);
});

test("locales and protected keys", async () => {
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  const keys = (source) => [...source.matchAll(/"(pluginContrib\.databaseDrivers\.[^"]+)"/g)].map((match) => match[1]).sort();
  assert.deepEqual(keys(zh), keys(en));
  for (const key of ["group", "grantTitle", "grantBody", "grantConfirm", "useBuiltin", "rebindTitle", "previewSummary"]) {
    assert.ok(keys(zh).includes(`pluginContrib.databaseDrivers.${key}`), key);
  }
  for (const key of ["grantAlwaysTrust", "trustLabel", "trustHint", "trustConfirmTitle", "trustConfirmBody", "trustConfirmButton"]) {
    assert.ok(keys(zh).includes(`pluginContrib.databaseDrivers.${key}`), key);
  }
  assert.match(zh, /"pluginContrib\.databaseDrivers\.trustLabel": "信任此插件（连接时直接发送凭据，不再逐个确认）"/);
  assert.match(zh, /"pluginContrib\.databaseDrivers\.grantAlwaysTrust": "始终信任此插件，不再询问"/);
  const protectedKeys = await readSource("../src/i18n/protected-keys.ts");
  assert.match(protectedKeys, /"pluginContrib\.databaseDrivers\.trust"/);
  assert.match(protectedKeys, /"pluginContrib\.databaseDrivers\.grant"/);
  assert.match(protectedKeys, /"pluginContrib\.databaseDrivers\.release"/);
});

test("lean build guidance: driver-id search and explicit rebinding wiring", async () => {
  // The host searches the plugin catalog by type/driver id and never names a plugin.
  assert.equal(databaseDriverPluginSearch("duckdb"), "duckdb");
  assert.equal(databaseDriverPluginSearch("hive"), "hive");
  const domain = await readSource("../src/domain/plugins/database-drivers.ts");
  assert.doesNotMatch(domain, /"[a-z0-9-]+-driver"/);
  const importDomain = await readSource("../src/domain/database/connection-import.ts");
  assert.doesNotMatch(importDomain, /"[a-z0-9-]+-driver"/);

  // Official driver plugins tag every driver id they contribute and every type
  // they replace, so that search finds them; a plugin with more drivers than
  // the 8 tags a manifest may carry (jdbc-bridge) fills all 8 instead. Every
  // type the lean (nocoredrivers) core leaves out is replaced by one of them.
  const replaced = new Set();
  const root = new URL("../../official-plugins/", import.meta.url);
  for (const entry of await readdir(root, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    let manifest;
    try {
      manifest = JSON.parse(await readFile(new URL(`${entry.name}/plugin.json`, root), "utf8"));
    } catch {
      continue;
    }
    const drivers = manifest.contributes?.databaseDrivers ?? [];
    const tags = (manifest.tags ?? []).map((tag) => tag.toLowerCase());
    if (drivers.length > 8) assert.equal(tags.length, 8, `${manifest.id} has more drivers than tags and should use all 8 tags`);
    for (const driver of drivers) {
      if (drivers.length <= 8) assert.ok(tags.includes(driver.id), `${manifest.id} tags lack driver id ${driver.id}`);
      if (driver.replaces) {
        assert.ok(tags.includes(driver.replaces), `${manifest.id} tags lack replaced type ${driver.replaces}`);
        replaced.add(driver.replaces);
      }
    }
  }
  for (const type of ["clickhouse", "duckdb", "oracle", "yashandb"]) assert.ok(replaced.has(type), `no official plugin replaces ${type}`);

  const view = await readSource("../src/views/DatabaseWorkspaceView.vue");
  assert.match(view, /defineAsyncComponent\(\(\) => import\("@\/components\/database\/DatabaseDriverRebindDialog\.vue"\)\)/);
  assert.match(view, /<DatabaseDriverRebindDialog[\s\S]*?@apply="applyDriverRebind"/);
  assert.match(view, /case "switch-driver":/);
  assert.match(view, /prepareFileDatabaseConnection\(detected, draft\)/);
  const sidebar = await readSource("../src/components/database/sidebar/DatabaseConnectionSidebar.vue");
  assert.match(sidebar, /v-if="canSwitchDriver\(connection\)" command="switch-driver"/);

  const runtime = await readSource("../src/composables/database/useDatabaseDriverRuntime.ts");
  // Missing built-in driver never installs or rebinds on its own: consent goes through the protected grant confirm.
  assert.match(runtime, /builtinDriver !== false/);
  assert.match(runtime, /ensureDatabaseDriverGrant\(ref,/);
  assert.match(runtime, /if \(!granted\) return undefined;/);
  assert.match(runtime, /openDriverPluginManager\(detected\.dbType\)/);
  assert.doesNotMatch(runtime, /installPlugin|plugin_install/);

  const dialog = await readSource("../src/components/database/DatabaseDriverRebindDialog.vue");
  assert.match(dialog, /initialConnectionIds\?: string\[\]/);
});

test("host-encoded null optional lists are accepted like omitted lists", () => {
  const driver = parsePluginDatabaseDriverEntry(
    entry({ replaces: "duckdb", connection: { fields: ["filePath"], required: null, fileExtensions: null, defaultPort: null }, features: null }),
    PLUGIN,
  );
  assert.ok(driver, "a file driver with null required/fileExtensions/features must not be dropped");
  assert.deepEqual(driver.required, []);
  assert.deepEqual(driver.fileExtensions, []);
  assert.deepEqual(driver.features, []);
  assert.equal(driver.dbType, "duckdb");
});

// ---------------------------------------------------------------------------
// Credential-release gate: per-plugin trust, "always trust" checkbox, and the
// repeat-prompt regression.

const REF = { connectionId: "conn-1", pluginId: "acme.driver", driverId: "acme" };

function fakeGate({ statuses = {}, decisions = [] } = {}) {
  const calls = { status: 0, confirm: 0, grant: [], trust: [] };
  const state = { granted: new Set(Object.entries(statuses).filter(([, s]) => s.granted).map(([id]) => id)), trusted: new Set(Object.entries(statuses).filter(([, s]) => s.trusted).map(([id]) => id)) };
  const confirms = [];
  const gate = createCredentialReleaseGate({
    async status(ref) {
      calls.status += 1;
      return { granted: state.granted.has(ref.connectionId), trusted: state.trusted.has(ref.pluginId) };
    },
    confirm() {
      calls.confirm += 1;
      const next = decisions.shift();
      if (next) return Promise.resolve(next);
      return new Promise((resolve) => confirms.push(resolve));
    },
    async grant(ref) {
      calls.grant.push(ref.connectionId);
      state.granted.add(ref.connectionId);
    },
    async setTrust(pluginId, trusted) {
      calls.trust.push([pluginId, trusted]);
      state.trusted.add(pluginId);
    },
  });
  return { gate, calls, confirms };
}

test("release gate: grant or trust skips the prompt", async () => {
  assert.equal(databaseDriverReleaseAllowed(undefined), false);
  assert.equal(databaseDriverReleaseAllowed({ granted: false, trusted: true }), true);
  assert.equal(databaseDriverReleaseAllowed({ granted: true, trusted: false }), true);
  const trusted = fakeGate({ statuses: { "acme.driver": { trusted: true } } });
  assert.equal(await trusted.gate(REF, {}), true);
  assert.equal(trusted.calls.confirm, 0, "a trusted plugin never prompts");
  assert.deepEqual(trusted.calls.grant, []);
  const granted = fakeGate({ statuses: { "conn-1": { granted: true } } });
  assert.equal(await granted.gate(REF, {}), true);
  assert.equal(granted.calls.confirm, 0, "an explicitly granted connection is never asked again");
  assert.deepEqual(
    parseDatabaseDriverGrantStatuses({ items: [{ ...REF, available: true, granted: false, trusted: true }] }, [REF])[0].trusted,
    true,
  );
});

test("release gate: the always-trust checkbox sets plugin trust", async () => {
  const { gate, calls } = fakeGate({ decisions: [{ confirmed: true, trust: true }] });
  assert.equal(await gate(REF, {}), true);
  assert.deepEqual(calls.grant, ["conn-1"]);
  assert.deepEqual(calls.trust, [["acme.driver", true]]);
  // Another connection of the same plugin no longer prompts.
  assert.equal(await gate({ ...REF, connectionId: "conn-2" }, {}), true);
  assert.equal(calls.confirm, 1);

  const plain = fakeGate({ decisions: [{ confirmed: true, trust: false }, { confirmed: false, trust: true }] });
  assert.equal(await plain.gate(REF, {}), true);
  assert.deepEqual(plain.calls.trust, [], "confirm without the checkbox does not trust the plugin");
  assert.equal(await plain.gate({ ...REF, connectionId: "conn-3" }, {}), false);
  assert.deepEqual(plain.calls.trust, [], "a cancelled confirm never trusts, even with the box ticked");
  assert.deepEqual(plain.calls.grant, ["conn-1"]);
});

test("release gate: concurrent connects share one prompt (repeat-prompt regression)", async () => {
  const { gate, calls, confirms } = fakeGate();
  // Two untrusted connects of the same connection (e.g. a double activation):
  // before the fix each opened its own stacked confirm, so after confirming
  // one the connection opened and loaded its tables behind the second one.
  const first = gate(REF, {});
  const second = gate(REF, {});
  // A different connection waits for the open prompt instead of stacking a second dialog.
  const other = gate({ ...REF, connectionId: "conn-2" }, {});
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(calls.confirm, 1);
  confirms.shift()({ confirmed: true, trust: false });
  assert.deepEqual(await Promise.all([first, second]), [true, true]);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(calls.confirm, 2, "the other connection is asked only after the first prompt closed");
  confirms.shift()({ confirmed: true, trust: false });
  assert.equal(await other, true);
  // Once granted, the connection is never asked again.
  assert.equal(await gate(REF, {}), true);
  assert.equal(calls.confirm, 2);
  assert.deepEqual(calls.grant, ["conn-1", "conn-2"]);
});

test("rebinding keeps the grant the user just confirmed (repeat-prompt regression)", async () => {
  const store = await readSource("../src/stores/database-workspace.ts");
  // saveConnection must only revoke the previous plugin's grant, never every
  // grant of the connection (that erased the grant recorded by the dialog).
  assert.match(store, /revokeDatabaseDriverGrant\(\{ connectionId: normalized\.id, pluginId: previousDriver\.pluginId \}\)/);
  assert.doesNotMatch(store, /revokeDatabaseDriverGrant\(\{ connectionId: normalized\.id \}\)/);
  const dialog = await readSource("../src/components/database/DatabaseConnectionDialog.vue");
  assert.doesNotMatch(dialog, /revokeReplacedGrant|revokeDatabaseDriverGrant/);
  assert.match(dialog, /databaseDriverReleaseAllowed\(status\)/);
  assert.match(dialog, /decision\.trust/);
  const view = await readSource("../src/views/DatabaseWorkspaceView.vue");
  assert.match(view, /await databaseStore\.initialize\(\);\n  for \(const filePath of files\)/);
  const service = await readSource("../src/services/database-driver-service.ts");
  assert.match(service, /grantAlwaysTrust/);
  assert.match(service, /createCredentialReleaseGate/);
  const manager = await readSource("../src/views/PluginManagerWorkspaceView.vue");
  assert.match(manager, /<PluginDatabaseDriverTrust v-if="plugin\.capabilities\.includes\('database\.driver'\)"/);
  const trust = await readSource("../src/components/plugins/PluginDatabaseDriverTrust.vue");
  assert.match(trust, /if \(value\) \{\s*try \{\s*await ElMessageBox\.confirm/);
  assert.match(trust, /trustConfirmBody/);
});
