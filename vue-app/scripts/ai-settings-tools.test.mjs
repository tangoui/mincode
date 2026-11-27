import assert from "node:assert/strict";
import test from "node:test";
import { settingsCapabilities } from "../src/services/ai-tools/settings-tools.ts";

function fixture() {
  const values = {
    uiTheme: "dark",
    aiProfiles: [{ id: "default", model: "example", apiKey: "sensitive-key" }],
    mcpServers: [{ id: "mcp", env: { COMPANY: "private-env" }, headers: { "X-Custom": "private-header" }, args: "--auth private-arg" }],
    "ai.apiKey": "sensitive-key",
    "agentControl.trusted": true,
  };
  const writes = [];
  const tools = settingsCapabilities({
    invoke: async () => undefined,
    configurableSettings: {
      read: async () => ({ ...values }),
      write: async (key, value) => { writes.push([key, value]); values[key] = value; },
    },
  });
  return {
    tools, writes,
    run: (name, input = {}, externalAgent = false) => tools.find((item) => item.tool.name === `settings.${name}`).execute({ input, externalAgent }),
  };
}

test("settings tools preserve existing tools and publish the added permissions and schemas", () => {
  const { tools } = fixture();
  for (const [name, risk, required] of [
    ["get_summary", "read", []], ["list", "read", []], ["get", "read", ["key"]],
    ["set", "write", ["key", "value"]], ["reset", "write", ["key"]],
  ]) {
    const capability = tools.find((entry) => entry.tool.name === `settings.${name}`);
    assert.deepEqual([capability?.risk, capability?.tool.inputSchema.required ?? []], [risk, required]);
  }
});

test("settings.list and settings.get redact profile keys and arbitrary MCP credentials", async () => {
  const { run } = fixture();
  const entries = await run("list");
  assert.deepEqual(await run("get", { key: "ai.apiKey" }), entries.find((item) => item.key === "ai.apiKey"));
  assert.equal(entries.find((item) => item.key === "ai.apiKey").value, "[REDACTED]");
  assert.deepEqual((await run("get", { key: "aiProfiles" })).value, [{ id: "default", model: "example", apiKey: "[REDACTED]" }]);
  assert.doesNotMatch(JSON.stringify(entries), /sensitive-key|private-env|private-header|private-arg/);
  assert.equal((await run("get", { key: "agentControl.trusted" }, true)).value, true);
});

test("settings.set validates values and settings.reset writes the default", async () => {
  const { run, writes } = fixture();
  await assert.rejects(async () => run("get", { key: "missing" }), /missing/);
  await assert.rejects(async () => run("set", { key: "uiTheme", value: 12 }), /uiTheme/);
  await assert.rejects(async () => run("set", { key: "uiTheme", value: "invalid-theme" }), /uiTheme/);
  await assert.rejects(async () => run("set", { key: "uiScale", value: 1000 }), /uiScale/);
  await run("set", { key: "uiTheme", value: "light" });
  assert.equal((await run("get", { key: "uiTheme" })).value, "light");
  await run("reset", { key: "uiTheme" });
  await run("set", { key: "ai.apiKey", value: "new-secret" });
  assert.equal((await run("get", { key: "ai.apiKey" })).value, "[REDACTED]");
  assert.deepEqual(writes, [["uiTheme", "light"], ["uiTheme", "light"], ["ai.apiKey", "new-secret"]]);
});

test("external Agents cannot toggle or reset their own trust, even with a false value", async () => {
  const { run, writes } = fixture();
  for (const value of [true, false]) {
    await assert.rejects(async () => run("set", { key: "agentControl.trusted", value }, true), /agentControl\.trusted/);
  }
  await assert.rejects(async () => run("reset", { key: "agentControl.trusted" }, true), /agentControl\.trusted/);
  assert.deepEqual(writes, []);
});

test("settings rejects incomplete connector, skill, and rule records before storage", async () => {
  const { run, writes } = fixture();
  for (const [key, value] of [
    ["mcpServers", [{ id: "broken", transport: "stdio" }]],
    ["aiSkills", [{ id: "broken", enabled: true }]],
    ["aiRules", [{ id: "broken", scope: "workspace", title: "test", content: "test", enabled: true }]],
  ]) {
    await assert.rejects(async () => run("set", { key, value }), new RegExp(key));
  }
  assert.deepEqual(writes, []);
});
