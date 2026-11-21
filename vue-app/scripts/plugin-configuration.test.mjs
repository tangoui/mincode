import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  PLUGIN_CONFIGURATION_SYNC_KEY,
  bridgeConfigurationValues,
  configurationFieldValue,
  configurationFields,
  isValidConfigurationValue,
  parseConfigurationState,
  parsePluginConfigurationSync,
  pluginConfigurationSyncEntry,
} from "../src/domain/plugins/configuration.ts";
import { parsePluginUIHostRequest } from "../src/domain/plugin-ui/protocol.ts";

const source = (path) => readFile(new URL(path, import.meta.url), "utf8");

const schema = {
  title: "Example",
  titleLocalized: { zh: "示例" },
  properties: {
    endpoint: { type: "string", title: "Endpoint", titleLocalized: { zh: "地址" }, format: "url", default: "https://example.com", order: 1 },
    retries: { type: "integer", title: "Retries", minimum: 0, maximum: 5, default: 3, order: 2 },
    mode: { type: "string", enum: ["fast", "safe"], enumLabelsLocalized: { zh: ["快", "稳"] }, scope: "workspace", default: "safe", order: 3 },
    verbose: { type: "boolean", scope: "workspace", order: 4 },
    tags: { type: "array", items: { type: "string" }, maxItems: 3, order: 5 },
    token: { type: "string", secret: true, order: 6 },
  },
};

function state(overrides = {}) {
  return parseConfigurationState({
    pluginId: "config.test",
    schema,
    workspaceId: "ws-1",
    global: { retries: 4, token: "leak", unknown: 1 },
    workspace: { mode: "fast", retries: 1 },
    effective: { endpoint: "https://example.com", retries: 4, mode: "fast", token: "leak" },
    secrets: { token: true, endpoint: true },
    orphanSecrets: ["old.key", "Bad Key"],
    ignored: [{ scope: "workspace", workspaceId: "ws-2", key: "gone", reason: "unknown setting" }],
    secretsAvailable: true,
    ...overrides,
  });
}

test("settings state is validated and never holds secret values", () => {
  const parsed = state();
  assert.ok(parsed);
  assert.deepEqual(parsed.global, { retries: 4 });
  assert.deepEqual(parsed.workspace, { mode: "fast" }, "global-only keys are dropped from workspace values");
  assert.equal("token" in parsed.effective, false);
  assert.deepEqual(parsed.secrets, { token: true }, "only declared secret settings report a set flag");
  assert.deepEqual(parsed.orphanSecrets, ["old.key"]);
  assert.deepEqual(parsed.ignored, [{ scope: "workspace", workspaceId: "ws-2", key: "gone", reason: "unknown setting" }]);
  assert.equal(parseConfigurationState({ pluginId: "x", schema: { properties: {} } }), undefined);
  assert.equal(parseConfigurationState(null), undefined);
});

test("form model orders fields, localizes labels and maps controls", () => {
  const parsed = state();
  const fields = configurationFields(parsed.schema, "zh-CN");
  assert.deepEqual(fields.map((field) => [field.key, field.control]), [
    ["endpoint", "string"],
    ["retries", "integer"],
    ["mode", "enum"],
    ["verbose", "boolean"],
    ["tags", "array"],
    ["token", "secret"],
  ]);
  assert.equal(fields[0].title, "地址");
  assert.equal(fields[1].title, "Retries");
  assert.equal(fields[5].title, "token", "untitled settings fall back to the key");
  assert.deepEqual(fields[2].options, [{ value: "fast", label: "快" }, { value: "safe", label: "稳" }]);
  assert.equal(fields[2].scope, "workspace");
  assert.deepEqual(configurationFields(parsed.schema, "en")[2].options.map((option) => option.label), ["fast", "safe"]);

  const byKey = Object.fromEntries(fields.map((field) => [field.key, field]));
  assert.equal(configurationFieldValue(byKey.retries, parsed.global, {}), 4);
  assert.equal(configurationFieldValue(byKey.mode, {}, { mode: "safe" }), "safe");
  assert.equal(configurationFieldValue(byKey.verbose, {}, {}), false);
  assert.deepEqual(configurationFieldValue(byKey.tags, {}, {}), []);
  assert.equal(configurationFieldValue(byKey.retries, {}, {}), 0);
});

test("client-side value checks mirror the host rules", () => {
  const { properties } = state().schema;
  assert.equal(isValidConfigurationValue(properties.retries, 5), true);
  assert.equal(isValidConfigurationValue(properties.retries, 6), false);
  assert.equal(isValidConfigurationValue(properties.retries, 1.5), false);
  assert.equal(isValidConfigurationValue(properties.mode, "other"), false);
  assert.equal(isValidConfigurationValue(properties.tags, ["a", "b", "c"]), true);
  assert.equal(isValidConfigurationValue(properties.tags, ["a", "b", "c", "d"]), false);
  assert.equal(isValidConfigurationValue(properties.tags, ["a", 1]), false);
  assert.equal(isValidConfigurationValue(properties.verbose, "true"), false);
});

test("iframe bridge values exclude secrets even if the host payload carried one", () => {
  const parsed = state();
  // Simulate a compromised/mistaken payload that slipped a secret into effective.
  const tampered = { ...parsed, effective: { ...parsed.effective, token: "leak" } };
  assert.deepEqual(bridgeConfigurationValues(tampered), { endpoint: "https://example.com", retries: 4, mode: "fast" });
  assert.deepEqual(bridgeConfigurationValues(tampered, ["token", "retries", "missing"]), { retries: 4 });
});

test("iframe bridge requests are bounded", () => {
  assert.deepEqual(parsePluginUIHostRequest({ type: "mincode.plugin.configuration.get", id: "r1" }), { type: "mincode.plugin.configuration.get", id: "r1" });
  assert.deepEqual(parsePluginUIHostRequest({ type: "mincode.plugin.configuration.get", id: "r2", keys: ["a"] }), { type: "mincode.plugin.configuration.get", id: "r2", keys: ["a"] });
  assert.equal(parsePluginUIHostRequest({ type: "mincode.plugin.configuration.get", id: "r3", keys: [1] }), undefined);
  assert.equal(parsePluginUIHostRequest({ type: "mincode.plugin.configuration.get", id: "r4", keys: Array(257).fill("a") }), undefined);
  assert.deepEqual(parsePluginUIHostRequest({ type: "mincode.plugin.configuration.open", id: "r5" }), { type: "mincode.plugin.configuration.open", id: "r5" });
});

test("PluginViewFrame answers and pushes configuration only through the secret-free filter", async () => {
  const frame = await source("../src/components/plugins/PluginViewFrame.vue");
  // Every value the frame reads from the host passes through bridgeConfigurationValues.
  const reads = frame.match(/await getPluginConfiguration\(/g) ?? [];
  const filtered = frame.match(/bridgeConfigurationValues\(state(, request\.keys)?\)/g) ?? [];
  assert.equal(reads.length, 2);
  assert.equal(filtered.length, 2);
  assert.doesNotMatch(frame, /state\.effective|state\.global|state\.workspace|setPluginConfigurationSecret|plugin_host_configuration/);
  // Inside the features object (which now holds conditional spreads like `...(x ? {} : {...})`):
  // no line closing that object (4-space indent) between its opening and the flag.
  assert.match(frame, /features: \{(?:(?!\n {4}\})[\s\S])*?\n\s+configuration: 1,/);
  // The settings service has no command that reads a secret value back.
  const service = await source("../src/services/plugin-configuration-service.ts");
  assert.doesNotMatch(service, /secret_get|secrets_get|host\.secrets/);
  assert.equal((service.match(/invokeBackend/g) ?? []).length, 8);
});

test("configuration sync entry carries only global plugin values", () => {
  assert.equal(PLUGIN_CONFIGURATION_SYNC_KEY.startsWith("app_"), true);
  const data = parsePluginConfigurationSync({
    version: 1,
    plugins: {
      "config.test": { global: { retries: 2, tags: ["a"], "Bad Key": 1, nested: { a: 1 } }, workspaces: { "ws-1": { mode: "fast" } } },
      "__host__": { global: { a: 1 } },
      "bad:id": { global: { a: 1 } },
      "empty.plugin": { global: {} },
    },
  });
  assert.deepEqual(data, { version: 1, plugins: { "config.test": { global: { retries: 2, tags: ["a"] } } } });
  assert.equal(parsePluginConfigurationSync({ version: 2, plugins: {} }), undefined);
  assert.deepEqual(pluginConfigurationSyncEntry({ version: 1, plugins: { "config.test": { global: { retries: 2 } } } }), {
    storage: "plain",
    value: { version: 1, plugins: { "config.test": { global: { retries: 2 } } } },
  });
  assert.equal(pluginConfigurationSyncEntry({ version: 1, plugins: {} }), undefined);
  assert.equal(pluginConfigurationSyncEntry(undefined), undefined);
});

test("config sync routes the synthetic entry through the Go host", async () => {
  const persistence = await source("../src/services/config-sync-persistence.ts");
  assert.match(persistence, /pluginConfigurationSyncEntry\(await exportPluginConfigurationSync\(\)\)/);
  assert.match(persistence, /key === PLUGIN_CONFIGURATION_SYNC_KEY[\s\S]*importPluginConfigurationSync\(data\)[\s\S]*continue;/);
});

test("settings fields resolve plugin-owned text over the locale chain", () => {
  const chained = {
    properties: {
      mode: {
        type: "string", scope: "global", enum: ["fast", "safe"], title: "Mode",
        titleLocalized: { zh: "模式", "pt-BR": "Modo" },
        description: "Speed", descriptionLocalized: { zh: "速度" },
        enumLabelsLocalized: { zh: ["快", "稳"], pt: ["Rápido", "Seguro"] },
      },
    },
  };
  // Language pack active (pt-BR over base zh): the plugin's own pt-BR/pt text wins, then the base.
  const [pack] = configurationFields(chained, ["pt-BR", "zh"]);
  assert.equal(pack.title, "Modo");
  assert.equal(pack.description, "速度");
  assert.deepEqual(pack.options.map((option) => option.label), ["Rápido", "Seguro"]);
  // Unknown pack tag falls back along the chain to the base language.
  const [fallback] = configurationFields(chained, ["ja", "zh"]);
  assert.equal(fallback.title, "模式");
  assert.deepEqual(fallback.options.map((option) => option.label), ["快", "稳"]);
  // A plain tag keeps working.
  assert.equal(configurationFields(chained, "en")[0].title, "Mode");
});
