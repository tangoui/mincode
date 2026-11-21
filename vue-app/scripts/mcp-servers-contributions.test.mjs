import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  collectMcpServers,
  isPluginMcpServerId,
  localizedMcpServerText,
  mcpServerAvailable,
  mcpServerErrorCode,
  mcpServerLaunchRoot,
  mcpServerPlatform,
  mcpServerPreviewSummary,
  normalizeEnabledPluginMcpServers,
  parsePluginMcpServerId,
  pluginMcpServerId,
  validateMcpServerEntry,
} from "../src/domain/plugins/mcp-servers.ts";

const FINGERPRINT = "a".repeat(64);

function entry(overrides = {}) {
  return {
    id: "search",
    title: "Search",
    command: "node",
    args: ["server.js"],
    env: {},
    platforms: ["darwin", "linux", "windows"],
    specFingerprint: FINGERPRINT,
    ...overrides,
  };
}

function contribution(entries, overrides = {}) {
  return {
    pluginId: "acme.tools",
    pluginName: "Acme Tools",
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: "f", entries, issues: [] },
    ...overrides,
  };
}

test("plugin MCP session ids round-trip and reject other ids", () => {
  const id = pluginMcpServerId("acme.tools", "search");
  assert.equal(id, "plugin:acme.tools/search");
  assert.equal(isPluginMcpServerId(id), true);
  assert.deepEqual(parsePluginMcpServerId(id), { pluginId: "acme.tools", serverId: "search" });
  for (const bad of ["search", "plugin:acme.tools", "plugin:Acme/search", "plugin:acme.tools/Search", "plugin:acme.tools/a/b"]) {
    assert.equal(isPluginMcpServerId(bad), false, bad);
    assert.equal(parsePluginMcpServerId(bad), undefined, bad);
  }
});

test("mcpServerPlatform maps navigator platforms", () => {
  assert.equal(mcpServerPlatform("MacIntel"), "darwin");
  assert.equal(mcpServerPlatform("Win32"), "windows");
  assert.equal(mcpServerPlatform("Linux x86_64"), "linux");
  assert.equal(mcpServerPlatform("darwin"), "darwin");
});

test("validateMcpServerEntry keeps display fields only and rejects bad ones", () => {
  const ok = validateMcpServerEntry(entry({ description: " Find things ", titleLocalized: { "zh-CN": "搜索" } }));
  assert.equal(ok.ok, true);
  assert.equal(ok.server.description, "Find things");
  assert.deepEqual(ok.server.titleLocalized, { "zh-CN": "搜索" });
  assert.equal("command" in ok.server, false);
  assert.equal("env" in ok.server, false);

  for (const [bad, message] of [
    [entry({ id: "Bad" }), /id/],
    [entry({ title: "" }), /title/],
    [entry({ title: "a\u202eb" }), /title/],
    [entry({ title: "x".repeat(65) }), /title/],
    [entry({ description: "x".repeat(201) }), /description/],
    [entry({ titleLocalized: { "not a locale": "x" } }), /titleLocalized/],
    [entry({ platforms: [] }), /platforms/],
    [entry({ platforms: ["darwin", "darwin"] }), /platforms/],
    [entry({ platforms: ["beos"] }), /platforms/],
    [entry({ specFingerprint: "abc" }), /specFingerprint/],
  ]) {
    const result = validateMcpServerEntry(bad);
    assert.equal(result.ok, false);
    assert.match(result.error, message);
  }
});

test("servers need a project unless the host says otherwise", () => {
  assert.equal(validateMcpServerEntry(entry()).server.requiresProject, true);
  assert.equal(validateMcpServerEntry(entry({ requiresProject: true })).server.requiresProject, true);
  const free = validateMcpServerEntry(entry({ requiresProject: false })).server;
  assert.equal(free.requiresProject, false);
  assert.equal(mcpServerAvailable({ requiresProject: true }, " "), false);
  assert.equal(mcpServerAvailable({ requiresProject: true }, "/repo"), true);
  assert.equal(mcpServerAvailable(free, ""), true);
  assert.equal(mcpServerLaunchRoot({ requiresProject: true }, " /repo "), "/repo");
  assert.equal(mcpServerLaunchRoot(free, "/repo"), "", "a project-less server never receives the project");
});

test("collectMcpServers builds a sorted registry with issues", () => {
  const { servers, issues } = collectMcpServers([
    contribution([entry({ id: "zeta" }), entry({ id: "alpha" }), entry({ id: "alpha" }), entry({ id: "Bad" })]),
    contribution([entry()], { pluginId: "declarative.only", runtime: "declarative" }),
    contribution([entry()], { pluginId: "other.workspace", activeInWorkspace: false }),
  ]);
  assert.deepEqual(servers.map((server) => server.sessionId), ["plugin:acme.tools/alpha", "plugin:acme.tools/zeta"]);
  assert.equal(servers[0].pluginName, "Acme Tools");
  assert.deepEqual(issues.map((issue) => [issue.entry, issue.stage]), [[2, "frontend"], [3, "frontend"]]);
  assert.match(issues[0].message, /duplicate/);
});

test("collectMcpServers forwards host issues and filters by platform", () => {
  const withHostIssue = contribution([entry({ id: "mac", platforms: ["darwin"] }), entry({ id: "all" })]);
  withHostIssue.point.issues = [{ entry: 5, id: "broken", message: "requires shell" }];
  const { servers, issues } = collectMcpServers([withHostIssue], "linux");
  assert.deepEqual(servers.map((server) => server.id), ["all"]);
  assert.deepEqual(issues, [{ pluginId: "acme.tools", stage: "host", entry: 5, message: "requires shell", server: "broken" }]);
});

test("normalizeEnabledPluginMcpServers keeps unique well-formed ids", () => {
  assert.deepEqual(normalizeEnabledPluginMcpServers(undefined), []);
  assert.deepEqual(normalizeEnabledPluginMcpServers("plugin:a.b/c"), []);
  assert.deepEqual(
    normalizeEnabledPluginMcpServers(["plugin:acme.tools/search", 1, "search", "plugin:acme.tools/search", "plugin:acme.tools/x"]),
    ["plugin:acme.tools/search", "plugin:acme.tools/x"],
  );
  const many = Array.from({ length: 80 }, (_, index) => `plugin:acme.tools/s${index}`);
  assert.equal(normalizeEnabledPluginMcpServers(many).length, 64);
});

test("localizedMcpServerText follows the locale chain", () => {
  const localized = { zh: "搜索", "pt-BR": "Pesquisa" };
  assert.equal(localizedMcpServerText("Search", localized, "zh-CN"), "搜索");
  assert.equal(localizedMcpServerText("Search", localized, ["pt-BR", "en"]), "Pesquisa");
  assert.equal(localizedMcpServerText("Search", localized, "fr"), "Search");
  assert.equal(localizedMcpServerText("Search", undefined, "zh"), "Search");
});

test("install preview and host error codes", () => {
  assert.deepEqual(
    mcpServerPreviewSummary({ mcpServers: [entry(), { title: "" }, entry({ title: " Docs " })] }),
    ["Search", "Docs"],
  );
  assert.deepEqual(mcpServerPreviewSummary({}), []);
  assert.deepEqual(mcpServerPreviewSummary(null), []);
  assert.equal(mcpServerErrorCode(new Error("not_found: missing")), "not_found");
  assert.equal(mcpServerErrorCode("changed: spec changed"), "changed");
  assert.equal(mcpServerErrorCode(new Error("boom")), "");
});

test("the AI drawer and settings wire plugin MCP servers through the host service only", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(drawer, /startPluginMcpServer\(server\.sessionId, mcpServerLaunchRoot\(server, projectRoot\)\)/);
  assert.match(drawer, /\.filter\(\(server\) => mcpServerAvailable\(server, runProjectRoot\)\)/);
  assert.doesNotMatch(drawer, /runProjectRoot\.trim\(\) \? pluginMcpServers/);
  assert.doesNotMatch(drawer, /ensurePluginMcpServerConfirmed|trustPluginMcpServer/);
  const service = await readFile(new URL("../src/services/plugin-mcp-server-service.ts", import.meta.url), "utf8");
  assert.doesNotMatch(service, /"mcp_connect/);
  const section = await readFile(new URL("../src/components/settings/PluginMcpServersSection.vue", import.meta.url), "utf8");
  assert.match(section, /ensurePluginMcpServerConfirmed\(server\.sessionId, mcpServerLaunchRoot\(server, projectRoot\(\)\)\)/);
});

test("host steps behind the connector switch time out with the step name", async () => {
  const { withMcpStepTimeout, mcpServerErrorCode } = await import("../src/domain/plugins/mcp-servers.ts");
  assert.equal(await withMcpStepTimeout(Promise.resolve(7), "prepare", 50), 7);
  await assert.rejects(withMcpStepTimeout(Promise.reject(new Error("forbidden: no")), "prepare", 50), /forbidden: no/);
  const stuck = new Promise(() => {});
  await assert.rejects(withMcpStepTimeout(stuck, "trust", 10), (error) => {
    assert.equal(mcpServerErrorCode(error), "timeout");
    assert.match(error.message, /^timeout: trust\b/);
    return true;
  });
  const service = await readFile(new URL("../src/services/plugin-mcp-server-service.ts", import.meta.url), "utf8");
  assert.match(service, /withMcpStepTimeout\(preparePluginMcpServer\(sessionId, projectRoot\), "prepare"\)/);
  assert.match(service, /withMcpStepTimeout\(trustPluginMcpServer\(sessionId, projectRoot, launch\.fingerprint\), "trust"\)/);
  const section = await readFile(new URL("../src/components/settings/PluginMcpServersSection.vue", import.meta.url), "utf8");
  assert.match(section, /withMcpStepTimeout\(store\.setEnabled\(server\.sessionId, true\), "save"\)/);
  assert.match(section, /code === "timeout"/);
});
