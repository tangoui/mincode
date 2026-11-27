import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { registerHooks } from "node:module";
import test from "node:test";
import { setImmediate as tick } from "node:timers/promises";
import { startAgentControlExecutor } from "../src/services/agent-control-executor.ts";
import { createCapabilityRegistry } from "../src/domain/ai/capability-registry.ts";
import { createResourceRegistry } from "../src/domain/ai/resource-registry.ts";

const hook = registerHooks({
  load(url, context, nextLoad) {
    if (url.endsWith("/SqlSafetyConfirmContent.vue")) {
      return { format: "module", shortCircuit: true, source: "export default {}" };
    }
    return nextLoad(url, context);
  },
});
const { startAiCapabilityHost } = await import("../src/services/ai-capability-host.ts");
const { registerAiConversationCapabilities } = await import("../src/services/ai-native-tools.ts");
hook.deregister();

test("native capabilities are available at startup without an AI conversation", () => {
  const capabilities = createCapabilityRegistry();
  const stop = startAiCapabilityHost({
    nativeRuntime: true,
    capabilities,
    registry: createResourceRegistry(),
    invoke: async () => undefined,
    listResources: () => [],
  });
  const modules = new Set(capabilities.listCapabilities().map(({ module }) => module));
  for (const module of ["database", "ssh", "http", "terminal"]) {
    assert.ok(modules.has(module), module);
  }
  const nativeTools = capabilities.listTools();
  assert.ok(nativeTools.some(({ name }) => name === "terminal.split_pane"));
  assert.equal(capabilities.get("ask_user"), undefined);

  const stopDrawer = registerAiConversationCapabilities({
    capabilities,
    askUser: async () => "",
    runSubagentPlan: async () => undefined,
  });
  assert.ok(capabilities.get("ask_user"));
  assert.ok(capabilities.get("agent.run_parallel"));
  stopDrawer();
  assert.deepEqual(capabilities.listTools(), nativeTools);
  stop();
  assert.deepEqual(capabilities.listTools(), []);
});

test("App owns startup registration and the drawer owns its conversation capabilities", async () => {
  const source = (path) => readFile(new URL(path, import.meta.url), "utf8");
  const [app, drawer] = await Promise.all([
    source("../src/App.vue"),
    source("../src/components/ai/AiAssistantDrawer.vue"),
  ]);
  assert.match(app, /startAiCapabilityHost\(/);
  assert.match(drawer, /registerAiConversationCapabilities\(/);
  assert.doesNotMatch(drawer, /registerNativeAiCapabilities\(/);
});

test("phase 2 tools are available to external agents before the drawer opens", async (t) => {
  const capabilities = createCapabilityRegistry();
  const expected = {
    notes: "list_tree get search create update move delete create_folder rename_folder delete_folder list_tags set_tags link_object unlink_object list_for_object",
    ai: "list_conversations list_local_agents create_conversation send_message get_messages wait_idle stop",
    ui: "list_views open_view open_resource get_active close_tab notify",
    settings: "list get set reset",
    dashboard: "get_overview list_widgets get_widget_data",
  };
  const names = Object.entries(expected).flatMap(([module, actions]) => actions.split(" ").map((action) => `${module}.${action}`));
  function stub(getters) {
    return new Proxy(getters, { get: (target, name) => target[name] ?? (() => { throw new Error("test boundary"); }) });
  }
  t.after(startAiCapabilityHost({
    nativeRuntime: true,
    capabilities,
    registry: createResourceRegistry(),
    invoke: async () => undefined,
    listResources: () => [],
    uiNavigation: stub({ listTabs: () => ({ activeView: "dashboard", tabs: [] }) }),
    configurableSettings: stub({ read: async () => ({ uiTheme: "light" }) }),
    dashboard: stub({ widgets: async () => [] }),
    aiSessions: stub({ listLocalAgents: async () => [] }),
  }));
  for (const name of names) assert.ok(capabilities.get(name), name);
  t.after(registerAiConversationCapabilities({
    capabilities,
    askUser: async () => "",
    runSubagentPlan: async () => undefined,
  }));

  const handlers = new Map();
  let published;
  const replies = new Map();
  t.after(startAgentControlExecutor({
    capabilities, operations: {},
    listen(event, listener) { handlers.set(event, listener); return () => handlers.delete(event); },
    async invoke(command, payload) {
      if (command === "agent_control_ready") published = payload.tools.map(({ name }) => name);
      if (command === "agent_control_result") replies.set(payload.requestId, payload);
    },
    resolveProjectRoot: () => "",
    resources: async () => createResourceRegistry(),
    beforeWrite: async () => {},
    afterWrite: async () => {},
    message: (key) => key,
  }));
  await tick();
  for (const name of names) assert.ok(published.includes(name), name);
  for (const name of ["ask_user", "agent.run_parallel", "agent.integrate_result"]) assert.ok(!published.includes(name), name);
  const probes = {
    "ai.list_local_agents": {},
    "ui.list_views": {},
    "settings.get": { key: "uiTheme" },
    "dashboard.list_widgets": {},
  };
  for (const name of names) {
    // Validate executor dispatch even for the tools whose native side effects
    // require the desktop. Use empty or invalid input for mutations.
    handlers.get("agent-control:call")({
      requestId: name, name, arguments: probes[name] ?? {}, origin: { kind: "pane" },
    });
  }
  await tick();
  for (const name of names) {
    const reply = replies.get(name);
    assert.ok(reply, name);
    assert.notEqual(reply.content[0].text, "unknownTool", name);
    assert.notEqual(reply.content[0].text, "excludedTool", name);
    if (Object.prototype.hasOwnProperty.call(probes, name)) assert.equal(reply.isError, false, name);
  }
});
