import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";
import { settingsIntegrations } from "./settings-integrations-module.mjs";
import { buildAcpMcpServers } from "@/domain/ai/acp-mcp-servers";
import { mcpAuthErrorCode, mcpAuthErrorMessageKey } from "@/domain/ai/mcp-auth";
import { normalizeMcpAuth } from "@/domain/ai/mcp-servers";

async function importTypeScriptSource(path, transform) {
  const source = transform(await readFile(new URL(path, import.meta.url), "utf8"));
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);
}

// settings-persistence imports the secure store, which needs the desktop runtime.
const settingsPersistence = await importTypeScriptSource("../src/services/settings-persistence.ts", (source) =>
  source
    .replace('import { shallowRef } from "vue";', "const shallowRef = (value) => ({ value });")
    .replace(
      /import \{[\s\S]*?\} from "@\/domain\/performance\/runtime-limits";/,
      "const RUNTIME_PERFORMANCE_SETTING_KEYS = [];",
    )
    .replace(
      /import \{[\s\S]*?\} from "@\/services\/secure-persistent-store";/,
      "const loadSecurePersistedValue = async () => null; const saveSecurePersistedValue = async () => {};",
    )
    // Desktop semantics: browser-local AI keys (sessionStorage) are not involved.
    .replace('import { storeBrowserAiKeys, withBrowserAiKeys } from "@/services/browser-ai-keys";', "")
    .replace('import { isDesktopRuntime } from "@/services/desktop-bridge";', "const isDesktopRuntime = () => true;"),
);

const {
  mergeMcpServersById,
  normalizeStoredMcpServer,
  parseEditableMcpConfig,
  parseMcpConfig,
  serializeEditableMcpConfig,
} = settingsIntegrations;

const settingsView = await readFile(new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url), "utf8");
const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
const settingsService = await readFile(new URL("../src/services/settings-service.ts", import.meta.url), "utf8");

const github = {
  id: "github",
  transport: "http",
  command: "",
  args: "",
  url: "https://api.githubcopilot.com/mcp/",
  enabled: true,
  env: {},
  headers: {},
  auth: "oauth",
};

test("auth survives settings normalization and config sync sanitizing", () => {
  assert.deepEqual(normalizeStoredMcpServer(github), { ...github, docsUrl: "" });
  // Only the known marker is kept; anything else is dropped rather than passed to the backend.
  assert.equal("auth" in normalizeStoredMcpServer({ ...github, auth: "basic" }), false);
  assert.equal(normalizeMcpAuth("oauth"), "oauth");
  assert.equal(normalizeMcpAuth("OAuth"), undefined);
  assert.equal(normalizeMcpAuth(undefined), undefined);
  assert.match(settingsView, /mcpServers: Array\.isArray\(raw\.mcpServers\)\s*\? raw\.mcpServers\.map\(normalizeStoredMcpServer\)/);

  const withSecret = { ...github, headers: { "X-Trace": "secret" } };
  const synced = settingsPersistence.sanitizeSettingsForSync({ mcpServers: [withSecret] });
  assert.equal(synced.mcpServers[0].auth, "oauth", "the marker is not a secret");
  assert.deepEqual(synced.mcpServers[0].headers, { "X-Trace": "" });
  const merged = settingsPersistence.mergeSettingsWithLocalSecrets(synced, { mcpServers: [withSecret] });
  assert.equal(merged.mcpServers[0].auth, "oauth");
  assert.deepEqual(merged.mcpServers[0].headers, { "X-Trace": "secret" });
});

test("editable MCP JSON keeps auth", () => {
  const plain = { ...github, id: "plain", auth: undefined };
  delete plain.auth;
  const text = serializeEditableMcpConfig([github, plain]);
  const document = JSON.parse(text);
  assert.equal(document.mcpServers.github.auth, "oauth");
  assert.equal("auth" in document.mcpServers.plain, false);
  assert.deepEqual(parseEditableMcpConfig(text), [github, plain]);
  assert.throws(
    () => parseEditableMcpConfig('{"mcpServers":{"x":{"url":"https://example.com/mcp","auth":"basic"}}}'),
    /invalid_mcp_auth:x/,
  );
  // Imported mcp.json files carry the marker too; merging keeps the incoming server whole.
  const [imported] = parseMcpConfig('{"mcpServers":{"github":{"url":"https://api.githubcopilot.com/mcp/","auth":"oauth"}}}');
  assert.equal(imported.auth, "oauth");
  const mergedServers = mergeMcpServersById([{ ...plain, id: "github" }], [imported]);
  assert.equal(mergedServers[0].auth, "oauth");
});

test("acp servers include a bearer header for signed-in oauth servers and drop signed-out ones", async () => {
  const signedOut = { ...github, id: "figma", url: "https://mcp.figma.com/mcp" };
  const tokenHeader = { ...github, id: "pat", auth: undefined, headers: { Authorization: "Bearer pat" } };
  delete tokenHeader.auth;
  const stdio = {
    id: "fs", transport: "stdio", command: "npx", args: "-y server <path>", url: "",
    enabled: true, env: { ROOT: "/tmp" }, headers: {},
  };
  const disabled = { ...github, id: "off", enabled: false };
  const asked = [];
  const result = await buildAcpMcpServers(
    [{ ...github, headers: { authorization: "Bearer stale", "X-Extra": "1" } }, signedOut, tokenHeader, stdio, disabled],
    "/repo",
    async (server) => {
      asked.push(server.id);
      return server.id === "github" ? { Authorization: "Bearer gho_token" } : null;
    },
  );
  assert.deepEqual(asked, ["github", "figma"], "only enabled oauth servers ask for a token");
  assert.deepEqual(result.servers, [
    {
      type: "http",
      name: "github",
      url: "https://api.githubcopilot.com/mcp/",
      // The host token replaces a configured Authorization header, as in the gateway.
      headers: [{ name: "X-Extra", value: "1" }, { name: "Authorization", value: "Bearer gho_token" }],
    },
    { type: "http", name: "pat", url: "https://api.githubcopilot.com/mcp/", headers: [{ name: "Authorization", value: "Bearer pat" }] },
    { type: "stdio", name: "fs", command: "npx", args: ["-y", "server", "/repo"], env: [{ name: "ROOT", value: "/tmp" }] },
  ]);
  assert.deepEqual(result.signInRequired, ["figma"]);
  assert.deepEqual(result.networkFailed, []);
});

test("acp hand-off separates connectors that need a sign-in from token refreshes that hit the network", async () => {
  const servers = ["required", "network", "timeout", "store", "missing"].map((id) => ({ ...github, id, url: `https://${id}.example.com/mcp` }));
  // A failed lookup never fails the session; the connector is left out and sorted by why.
  const result = await buildAcpMcpServers(servers, "/repo", async (server) => {
    if (server.id === "required") throw new Error("connect: [MCP_AUTH_REQUIRED] 连接器需要登录");
    if (server.id === "network") throw new Error("RuntimeError: [MCP_AUTH_NETWORK] refresh failed");
    if (server.id === "timeout") throw new Error("[MCP_AUTH_TIMEOUT] 登录超时");
    if (server.id === "store") throw new Error("[MCP_AUTH_STORE] 打开令牌库失败");
    return null;
  });
  assert.deepEqual(result, {
    servers: [],
    signInRequired: ["required", "store", "missing"],
    networkFailed: ["network", "timeout"],
  });

  // The drawer posts a separate network notice; the sign-in notice stays for the rest.
  assert.match(drawer, /t\("ai\.mcp\.oauthNetworkFailedForAcp", \{ names: skipped\.networkFailed\.join/);
  assert.match(drawer, /t\("ai\.mcp\.oauthSkippedForAcp", \{ names: skipped\.signInRequired\.join/);
  assert.match(drawer, /noteAcpSkippedConnectors\(id, acpMcpServers\)/);
});

test("auth error codes are found anywhere in the error text", () => {
  assert.equal(mcpAuthErrorCode(new Error("connect github: [MCP_AUTH_REQUIRED] 连接器需要登录")), "MCP_AUTH_REQUIRED");
  assert.equal(mcpAuthErrorCode("RuntimeError: [MCP_AUTH_NETWORK] refresh failed"), "MCP_AUTH_NETWORK");
  assert.equal(mcpAuthErrorCode({ message: "[MCP_AUTH_UNSUPPORTED] no client" }), "MCP_AUTH_UNSUPPORTED");
  assert.equal(mcpAuthErrorCode(new Error("plain failure")), null);
  assert.equal(mcpAuthErrorCode(new Error("[MCP_AUTH_SOMETHING_NEW] x")), "MCP_AUTH_SOMETHING_NEW");
  assert.equal(mcpAuthErrorMessageKey("MCP_AUTH_REQUIRED"), "settings.mcp.auth.error.required");
  assert.equal(mcpAuthErrorMessageKey("MCP_AUTH_NETWORK"), "settings.mcp.auth.error.network");
  assert.equal(mcpAuthErrorMessageKey("MCP_AUTH_UNSUPPORTED"), "settings.mcp.auth.error.unsupported");
  assert.equal(mcpAuthErrorMessageKey("MCP_AUTH_SOMETHING_NEW"), "settings.mcp.auth.error.generic");
});

test("connect calls pass the auth marker and ACP sessions await the token hand-off", () => {
  assert.match(settingsService, /invokeBackend\("mcp_connect", \{[^}]*auth: runtimeServer\.auth/);
  const connects = drawer.match(/invokeBackend<McpConnectResult>\("mcp_connect", \{[\s\S]*?\}\)/g) ?? [];
  assert.equal(connects.length, 2);
  for (const call of connects) assert.match(call, /auth: server\.auth/);
  assert.match(drawer, /async function configuredAcpMcpServers\(/);
  assert.match(drawer, /const acpMcpServers = await mcpServers\(\);[\s\S]*?const sessionArgs = \{ serverId, cwd, mcpServers: acpMcpServers\.servers \}/);
  assert.match(drawer, /"acp_load_session", \{\s*\.\.\.sessionArgs, sessionId: previousSessionId/);
  assert.match(drawer, /"acp_new_session", sessionArgs\)/);
  assert.match(drawer, /ai\.mcp\.oauthSkippedForAcp/);
});
