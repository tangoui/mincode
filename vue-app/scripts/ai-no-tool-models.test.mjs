import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { toolsForModel } from "../src/domain/ai/model-tool-support.ts";

const tools = [{ name: "read_file", description: "Read", inputSchema: { type: "object" } }];

test("toolsForModel drops tools for a model that cannot take them", () => {
  assert.deepEqual(toolsForModel("lite", ["lite"], tools), []);
});

test("toolsForModel keeps tools for other models and without a list", () => {
  assert.equal(toolsForModel("other", ["lite"], tools), tools);
  assert.equal(toolsForModel("lite", undefined, tools), tools);
});

test("streamAiLoopTurn filters by the request's own model and keeps the list out of the payload", async () => {
  const source = await readFile(new URL("../src/services/ai-agent-loop.ts", import.meta.url), "utf8");
  const start = source.indexOf("export async function streamAiLoopTurn(");
  const body = source.slice(start, source.indexOf("\n}\n", start));
  // Failover calls streamAiLoopTurn with { ...config, model }, so checking config.model here covers it.
  assert.match(body, /createAiToolTransport\(toolsForModel\(config\.model, config\.noToolModels, tools\)\)/);
  assert.match(body, /const \{ noToolModels: _noToolModels, \.\.\.requestConfig \} = config;/);
  assert.match(body, /\.\.\.requestConfig,/);
});

test("every AI request builder passes the profile's noToolModels", async () => {
  for (const path of [
    "../src/components/ai/AiAssistantDrawer.vue",
    "../src/components/database/query/DatabaseAiQueryDialog.vue",
  ]) {
    const source = await readFile(new URL(path, import.meta.url), "utf8");
    assert.match(source, /noToolModels: [^\n]*profile[^\n]*noToolModels/, path);
  }
});

test("validateAiProviderAuth refuses a base URL with an unfilled placeholder", async () => {
  const { validateAiProviderAuth, aiProviderAuthMessageKey } = await import("../src/domain/ai/provider-auth.ts");
  const result = validateAiProviderAuth({
    provider: "plugin",
    baseUrl: "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1",
    apiKey: "token",
  });
  assert.deepEqual(result, { valid: false, reason: "unresolved_base_url" });
  assert.equal(aiProviderAuthMessageKey(result), "ai.unresolvedBaseUrl");
  assert.equal(aiProviderAuthMessageKey({ valid: false, reason: "missing_api_key" }), "ai.missingApiKey");
});

test("every caller shows the reason-specific auth message", async () => {
  for (const path of [
    "../src/components/ai/AiAssistantDrawer.vue",
    "../src/components/database/query/DatabaseAiQueryDialog.vue",
  ]) {
    const source = await readFile(new URL(path, import.meta.url), "utf8");
    assert.doesNotMatch(source, /t\("ai\.missingApiKey"\)/, path);
    assert.match(source, /aiProviderAuthMessageKey\(auth\)/, path);
  }
});

test("the assistant tells the user once per model that tools are off", async () => {
  const source = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(source, /function notifyNoToolModel\(config: \{ model: string; noToolModels\?: string\[\] \}\)/);
  assert.match(source, /ElMessage\.info\(t\("ai\.noToolModel", \{ model: config\.model \}\)\)/);
  assert.match(source, /notifyNoToolModel\(requestConfig\);/);
});

test("plainChatHistory folds tool calls and results into plain assistant text", async () => {
  const { plainChatHistory } = await import("../src/domain/ai/model-tool-support.ts");
  const history = [
    { role: "system", content: "sys" },
    { role: "user", content: "list files" },
    { role: "assistant", content: "Let me look.", toolCalls: [{ id: "c1", name: "list_dir", arguments: "{}" }], thinkingBlocks: [{ type: "thinking", thinking: "x", signature: "s" }] },
    { role: "tool", content: "a.txt\nb.txt", toolCallId: "c1", toolResult: { status: "success", source: "native" } },
    { role: "assistant", content: "Two files." },
    { role: "user", content: "thanks" },
  ];
  const plain = plainChatHistory(history);
  assert.deepEqual(plain.map((message) => message.role), ["system", "user", "assistant", "user"]);
  assert.ok(plain.every((message) => !message.toolCalls && !message.toolCallId && !message.toolResult && !message.thinkingBlocks));
  assert.match(plain[2].content, /Let me look\.[\s\S]*list_dir[\s\S]*a\.txt\nb\.txt[\s\S]*Two files\./);
  assert.deepEqual(history[2].toolCalls.length, 1, "input is not mutated");
});

test("streamAiLoopTurn sends plain history when it drops the tools", async () => {
  const source = await readFile(new URL("../src/services/ai-agent-loop.ts", import.meta.url), "utf8");
  const start = source.indexOf("export async function streamAiLoopTurn(");
  const body = source.slice(start, source.indexOf("\n}\n", start));
  assert.match(body, /const chatOnly = config\.noToolModels\?\.includes\(config\.model\) === true;/);
  assert.match(body, /\(chatOnly \? plainChatHistory\(messages\) : messages\)\.map\(/);
});
