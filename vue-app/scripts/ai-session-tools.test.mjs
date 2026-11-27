import assert from "node:assert/strict";
import test from "node:test";
import { createCapabilityRegistry } from "../src/domain/ai/capability-registry.ts";
import { selectAiCapabilityTools } from "../src/domain/ai/tool-selection.ts";
import { applySavedLocale } from "../src/i18n/messages.ts";

await applySavedLocale("en");

async function fixture() {
  const { aiSessionCapabilities } = await import("../src/services/ai-tools/ai-session-tools.ts");
  const store = [];
  const turns = new Map();
  let runnerLoads = 0;
  const registry = createCapabilityRegistry();
  const dependencies = {
    listLocalAgents: async () => [{ id: "codex", name: "Codex" }],
    listConversations: async () => store,
    getConversation: async (id) => store.find((conversation) => conversation.id === id),
    createConversation: async ({ mode, title, cwd, agentId, fullAccess }) => {
      const conversation = {
        id: `conversation-${store.length + 1}`, mode, title: title ?? "", projectRoot: cwd, acpAdapterId: agentId,
        fullAccess, status: "not_started", messages: [], agentEvents: [], updatedAt: store.length,
      };
      store.push(conversation);
      return conversation;
    },
    loadRunner: async () => {
      runnerLoads += 1;
      return {
        start(id, text) {
          const conversation = store.find((item) => item.id === id);
          const messageId = `message-${conversation.messages.length + 1}`;
          conversation.messages.push({ id: messageId, role: "user", content: text });
          const turn = Promise.withResolvers();
          turns.set(id, { ...turn, state: "running" });
          return { messageId, done: turn.promise, cancel: () => turns.get(id).resolve("idle") };
        },
        state: (id) => turns.get(id)?.state ?? "idle",
        stop: async (id) => {
          const turn = turns.get(id);
          if (!turn) return false;
          turn.state = "idle";
          turn.resolve("idle");
          return true;
        },
      };
    },
  };
  for (const capability of aiSessionCapabilities(dependencies)) registry.register(capability);
  const call = async (name, input = {}, options = {}) => (await registry.execute(`ai.${name}`, input, {
    authorization: { conversationId: "caller", consume: async () => true }, ...options,
  })).result;
  return { call, registry, store, turns, runnerLoads: () => runnerLoads };
}

test("session tool names, risks and required fields match the public contract", async () => {
  const { registry } = await fixture();
  assert.deepEqual(Object.fromEntries(registry.listCapabilities().map(({ tool, risk }) => [
    tool.name, [risk, tool.inputSchema.required ?? []],
  ])), {
    "ai.list_conversations": ["read", []], "ai.list_local_agents": ["read", []],
    "ai.create_conversation": ["write", ["mode"]], "ai.send_message": ["write", ["conversationId", "text"]],
    "ai.get_messages": ["read", ["conversationId"]], "ai.wait_idle": ["read", ["conversationId"]],
    "ai.stop": ["write", ["conversationId"]],
  });
});

test("validates input before starting a turn or creating a conversation", async () => {
  const { call, store, runnerLoads } = await fixture();
  await assert.rejects(call("create_conversation", { mode: "chat" }), /mode/);
  await assert.rejects(call("create_conversation", { mode: "local-agent" }), /agentId/);
  await assert.rejects(call("create_conversation", { mode: "local-agent", agentId: "missing" }), /missing/);
  await assert.rejects(call("list_conversations", { limit: 51 }), /limit/);
  await assert.rejects(call("get_messages", { conversationId: "missing", limit: 101 }), /limit/);
  await assert.rejects(call("send_message", { conversationId: "missing", text: " " }), /text/);
  await assert.rejects(call("send_message", { conversationId: "missing", text: "hello" }), /missing/);
  assert.equal(store.length, 0);
  assert.equal(runnerLoads(), 0);
});

test("the native assistant offers session tools when asked to start a Codex conversation", async () => {
  const { registry } = await fixture();
  const tools = selectAiCapabilityTools(registry.listCapabilities(), {
    text: "帮我新建一个 Codex 会话", availableModules: ["ai"],
  });
  assert.ok(tools.some(({ name }) => name === "ai.create_conversation"));
});

test("creates native and selected local-agent conversations without starting the runner", async () => {
  const { call, store, runnerLoads } = await fixture();
  assert.deepEqual(await call("list_local_agents"), { agents: [{ id: "codex", name: "Codex" }] });
  const native = await call("create_conversation", { mode: "native", title: "Research" });
  const agent = await call("create_conversation", { mode: "local-agent", agentId: "codex", cwd: "/work", approval: "full-access" });
  assert.equal(native.mode, "native");
  assert.equal(native.title, "Research");
  assert.equal(store[0].fullAccess, false);
  assert.equal(agent.mode, "local-agent");
  assert.equal(agent.agentName, "Codex");
  assert.equal(store[1].projectRoot, "/work");
  assert.equal(store[1].fullAccess, true);
  assert.deepEqual((await call("list_conversations", { limit: 1 })).conversations.map(({ id }) => id), [agent.id]);
  assert.equal(runnerLoads(), 0);
});

test("waited send loads the runner and returns only its last assistant reply", async () => {
  const { call, store, turns, runnerLoads } = await fixture();
  const { id } = await call("create_conversation", { mode: "native" });
  store[0].messages.push({ role: "assistant", content: "Earlier reply" });
  const reply = call("send_message", { conversationId: id, text: "Hello", wait: true });
  await new Promise(setImmediate);
  assert.equal(runnerLoads(), 1);
  assert.equal(store[0].messages[1].content, "Hello");
  store[0].messages.push({ role: "assistant", content: "Working" }, { role: "assistant", content: "Done" }, { role: "user", content: "Next turn" });
  store[0].messages.push({ role: "assistant", content: "Unrelated reply" });
  turns.get(id).resolve("idle");
  assert.equal((await reply).reply.content, "Done");
});

test("background send, approval, question, timeout, and stop expose distinct outcomes", async () => {
  const { call, turns } = await fixture();
  const { id } = await call("create_conversation", { mode: "local-agent", agentId: "codex" });
  const sent = await call("send_message", { conversationId: id, text: "Begin" });
  assert.equal(sent.messageId, "message-1");
  assert.equal((await call("wait_idle", { conversationId: id, timeoutSecs: 0 })).state, "timeout");
  turns.get(id).state = "waiting_approval";
  assert.equal((await call("wait_idle", { conversationId: id })).state, "waiting_approval");
  turns.get(id).state = "waiting_input";
  assert.equal((await call("wait_idle", { conversationId: id })).state, "waiting_input");
  assert.deepEqual(await call("stop", { conversationId: id }), { conversationId: id, stopped: true });
  assert.equal((await call("wait_idle", { conversationId: id })).state, "idle");
});

test("waited send returns waiting status without ending the turn", async () => {
  const { call, turns } = await fixture();
  const { id } = await call("create_conversation", { mode: "native" });
  const reply = call("send_message", { conversationId: id, text: "Begin", wait: true });
  await new Promise(setImmediate);
  turns.get(id).state = "waiting_approval";
  assert.equal((await reply).state, "waiting_approval");
  assert.equal(turns.get(id).state, "waiting_approval");
});

test("message pagination returns transcript indexes and redacted tool summaries", async () => {
  const { call, store } = await fixture();
  const { id } = await call("create_conversation", { mode: "native" });
  store[0].messages.push({ id: "a", role: "user", content: "Ask" }, { id: "b", role: "assistant", content: "Answer" });
  store[0].agentEvents.push({ id: "tool", kind: "tool", triggerMessageId: "b", toolName: "example.read", title: "Looked up token=example-secret", status: "completed" });
  const result = await call("get_messages", { conversationId: id, sinceIndex: 1, limit: 1 });
  assert.equal(result.nextIndex, 2);
  assert.equal(result.messages[0].role, "assistant");
  assert.equal(result.messages[0].content, "Answer");
  assert.equal(result.messages[0].toolCalls[0].name, "example.read");
  assert.doesNotMatch(JSON.stringify(result), /example-secret/);
});
