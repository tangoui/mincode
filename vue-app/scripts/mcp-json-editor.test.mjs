import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { settingsIntegrations } from "./settings-integrations-module.mjs";

const { keepSyncMetadata, parseEditableMcpConfig, serializeEditableMcpConfig } = settingsIntegrations;

const settingsView = await readFile(
  new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url),
  "utf8",
);

const servers = [
  {
    id: "stdio-demo",
    transport: "stdio",
    command: "npx",
    args: "-y @example/mcp",
    url: "",
    docsUrl: "https://example.com/docs",
    enabled: true,
    env: { TOKEN: "secret" },
    headers: {},
  },
  {
    id: "http-demo",
    transport: "http",
    command: "",
    args: "",
    url: "https://example.com/mcp",
    enabled: false,
    env: {},
    headers: { Authorization: "Bearer token" },
  },
];

test("connector JSON editor round-trips every supported field", () => {
  const text = serializeEditableMcpConfig(servers);
  const parsed = parseEditableMcpConfig(text);
  assert.deepEqual(parsed, servers);
  const document = JSON.parse(text);
  assert.deepEqual(document.mcpServers["stdio-demo"].args, ["-y", "@example/mcp"]);
  assert.equal(document.mcpServers["http-demo"].type, "http");
});

test("strict connector JSON parser supports clearing all connectors", () => {
  assert.deepEqual(parseEditableMcpConfig('{"mcpServers":{}}'), []);
});

test("strict connector JSON parser rejects partial or invalid servers", () => {
  assert.throws(() => parseEditableMcpConfig("[]"), /invalid_mcp_config/);
  assert.throws(() => parseEditableMcpConfig('{"mcpServers":{"broken":{"args":[]}}}'), /invalid_mcp_server/);
  assert.throws(
    () => parseEditableMcpConfig('{"mcpServers":{"broken":{"command":"npx","env":{"TOKEN":1}}}}'),
    /invalid_mcp_env/,
  );
  assert.throws(
    () => parseEditableMcpConfig('{"mcpServers":{"broken":{"url":"https://example.com","enabled":"yes"}}}'),
    /invalid_mcp_enabled/,
  );
});

test("settings connector panel exposes an in-app Monaco JSON editor", () => {
  assert.match(settingsView, /@click="openMcpJsonEditor"/);
  assert.match(settingsView, /v-model="mcpJsonEditorOpen"[\s\S]*?<MonacoTextEditor/);
  assert.match(
    settingsView,
    /await persistSettingsSnapshot\(\{ \.\.\.settings, locale: locale\.value \}, \{ strict: true \}\);\s*ElMessage\.success\(t\("settings\.mcp\.jsonSaved"\)\)/
  );
  assert.match(settingsView, /parseEditableMcpConfig\(mcpJsonText\.value\)/);
});

test("saving the connector JSON keeps each connector's sync source", () => {
  const syncSource = { pluginId: "mincode.cc-switch-sync", kind: "mcp", sourceId: "fs", fingerprint: "0123456789abcdef" };
  const previous = [{ ...servers[0], syncSource }, servers[1]];
  const text = serializeEditableMcpConfig(previous);
  assert.ok(!text.includes("syncSource"), "sync metadata is not editable JSON");
  const edited = parseEditableMcpConfig(text.replace('"npx"', '"bunx"'));
  const saved = keepSyncMetadata(edited, previous);
  assert.deepEqual(saved[0], { ...servers[0], command: "bunx", syncSource });
  assert.equal("syncSource" in saved[1], false);
  // A connector renamed or added in the editor is a different connector.
  const renamed = keepSyncMetadata(parseEditableMcpConfig(text.replace('"stdio-demo"', '"renamed"')), previous);
  assert.equal(renamed.some((server) => "syncSource" in server), false);

  const save = settingsView.match(/async function saveMcpJson\(\) \{[\s\S]*?\n\}/)?.[0] ?? "";
  assert.match(save, /servers = keepSyncMetadata\(parseEditableMcpConfig\(mcpJsonText\.value\), settings\.mcpServers\)/);
});

test("a duplicated profile gets no sync source", () => {
  const { withoutSyncMetadata } = settingsIntegrations;
  const profile = { id: "p1", name: "gc2", model: "m1", syncSource: { pluginId: "x", kind: "profile", sourceId: "a", fingerprint: "f" } };
  assert.deepEqual(withoutSyncMetadata(profile), { id: "p1", name: "gc2", model: "m1" });
  assert.ok("syncSource" in profile, "the original keeps it");
  const duplicate = settingsView.match(/function duplicateAiProfile\([^)]*\) \{[\s\S]*?\n\}/)?.[0] ?? "";
  assert.match(duplicate, /\.\.\.withoutSyncMetadata\(source\)/);
  assert.ok(!/\.\.\.source,/.test(duplicate));
});
