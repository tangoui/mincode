import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  MCP_LOAD_TOOL_NAME,
  mcpLoadSummary,
  mcpLoadToolDefinition,
  mcpServersFromHistory,
  mcpServersMentioned,
  parseMcpLoadRequest,
  pickMcpTools,
  planMcpToolLoading,
} from "../src/domain/ai/mcp-tool-loading.ts";

const servers = ["github", "jira", "my-db"];

test("a new conversation loads no connector up front and only offers the loader", () => {
  assert.deepEqual(
    planMcpToolLoading({ text: "帮我查一下最近的工单", serverIds: servers, historyServerIds: [] }),
    { offerLoader: true, preload: [] },
  );
});

test("no connectors, or a purely social turn, needs neither loader nor tools", () => {
  assert.deepEqual(planMcpToolLoading({ text: "list issues", serverIds: [], historyServerIds: [] }), { offerLoader: false, preload: [] });
  assert.deepEqual(planMcpToolLoading({ text: "谢谢你", serverIds: servers, historyServerIds: ["github"] }), { offerLoader: false, preload: [] });
});

test("connectors used earlier or named in the request are loaded up front, the rest stay behind the loader", () => {
  assert.deepEqual(
    planMcpToolLoading({ text: "use Jira to list my issues", serverIds: servers, historyServerIds: ["github"] }),
    { offerLoader: true, preload: ["github", "jira"] },
  );
  assert.deepEqual(
    planMcpToolLoading({ text: "continue", serverIds: ["github"], historyServerIds: ["github", "removed"] }),
    { offerLoader: false, preload: ["github"] },
  );
});

test("connector names match whole words only", () => {
  assert.deepEqual(mcpServersMentioned("用 github 查一下", servers), ["github"]);
  assert.deepEqual(mcpServersMentioned("open my-db please", servers), ["my-db"]);
  assert.deepEqual(mcpServersMentioned("githubusercontent and my-dbx", servers), []);
});

test("history yields the connectors whose tools or loader ran", () => {
  const history = [
    { role: "user", content: "x" },
    { role: "tool", content: "ok", toolResult: { source: { kind: "mcp", toolName: MCP_LOAD_TOOL_NAME, serverId: "jira" } } },
    { role: "tool", content: "ok", toolResult: { source: { kind: "mcp", toolName: "search", serverId: "github" } } },
    { role: "tool", content: "ok", toolResult: { source: { kind: "native", toolName: "git.status" } } },
    { role: "tool", content: "ok", toolResult: { source: { kind: "mcp", toolName: "search", serverId: "jira" } } },
  ];
  assert.deepEqual(mcpServersFromHistory(history), ["jira", "github"]);
});

test("the loader tool lists only enabled connector IDs and accepts nothing else", () => {
  const definition = mcpLoadToolDefinition(servers);
  assert.equal(definition.name, MCP_LOAD_TOOL_NAME);
  assert.deepEqual(definition.inputSchema.properties.serverId.enum, servers);
  assert.equal(definition.inputSchema.additionalProperties, false);
  assert.match(definition.description, /github, jira, my-db/);

  assert.deepEqual(parseMcpLoadRequest({ serverId: " jira ", toolNames: ["a", " a ", "", 3] }, servers), { serverId: "jira", toolNames: ["a"] });
  assert.throws(() => parseMcpLoadRequest({ serverId: "other" }, servers), /Unknown connector "other"/);
  assert.throws(() => parseMcpLoadRequest({}, servers), /Unknown connector/);
});

test("loading picks requested tools and reports unknown names", () => {
  const tools = [
    { name: "mcp.jira.search", toolName: "search", description: "[Connector:jira] Search issues" },
    { name: "mcp.jira.create", toolName: "create", description: "[Connector:jira] Create an issue" },
  ];
  assert.deepEqual(pickMcpTools(tools, []).picked.map((tool) => tool.toolName), ["search", "create"]);
  const { picked, unknown } = pickMcpTools(tools, ["create", "delete"]);
  assert.deepEqual(picked.map((tool) => tool.toolName), ["create"]);
  assert.deepEqual(unknown, ["delete"]);

  const summary = mcpLoadSummary("jira", picked, unknown);
  assert.match(summary, /Loaded 1 tool\(s\) from connector "jira"/);
  assert.match(summary, /- mcp\.jira\.create: Create an issue/);
  assert.doesNotMatch(summary, /\[Connector:/);
  assert.match(summary, /Not found on this connector: delete\./);
  assert.match(mcpLoadSummary("jira", []), /exposes no matching tools/);
});

test("long tool lists and descriptions are bounded", () => {
  const many = Array.from({ length: 130 }, (_, index) => ({
    name: `mcp.big.t${index}`,
    toolName: `t${index}`,
    description: "x".repeat(400),
  }));
  const summary = mcpLoadSummary("big", many);
  assert.match(summary, /- … 30 more/);
  assert.ok(summary.split("\n").every((line) => line.length < 220));
});

test("the drawer loads connectors on demand instead of every enabled server", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.doesNotMatch(drawer, /availableMcpTools|selectMcpTools/);
  assert.match(drawer, /historyServerIds: mcpServersFromHistory\(toolHistory\)/);
  assert.match(drawer, /const toolHistory = relayedTurnRun\?\.history === "none" \? \[\] : persistedHistory;/);
  assert.match(drawer, /const mcpLoader = mcpPlan\.offerLoader \? \[mcpLoadToolDefinition\(mcpServerIds\)\] : \[\];/);
  assert.match(drawer, /const currentTools = \(\) => \[\.\.\.nativeTools, \.\.\.mcpLoader, \.\.\.mcpTools\];/);
  assert.match(drawer, /currentTools,\s*compactHistory/);
  assert.match(drawer, /mcpLoader\.length && call\.name === MCP_LOAD_TOOL_NAME\s*\?\s*loadMcpToolsForModel\(/);
  // Loaded tools still run through executeAgentTool and its MCP approval.
  assert.match(drawer, /executeAgentTool\(\s*conversationId,\s*toolTriggerMessageIds\.get\(call\.id\),\s*call,\s*mcpTools,/);
});
