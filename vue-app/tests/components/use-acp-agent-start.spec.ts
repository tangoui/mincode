import { flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { effectScope, type EffectScope } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AcpMcpServerSet } from "@/domain/ai/acp-mcp-servers";

const ui = vi.hoisted(() => ({
  warning: vi.fn(),
  info: vi.fn(),
  error: vi.fn(),
  confirmBox: vi.fn(async () => "confirm"),
  confirmLaunch: vi.fn(async (_launch: unknown, _options?: { notices?: string[] }) => true),
  prepare: vi.fn(),
  trust: vi.fn(),
  launch: vi.fn(async () => ({})),
  listeners: new Map<string, Set<(payload: unknown) => void>>(),
}));

/** Delivers a host event to the composable's listeners. */
function emitHostEvent(name: string, payload: unknown) {
  for (const handler of ui.listeners.get(name) ?? []) handler(payload);
}

vi.mock("element-plus", () => ({
  ElMessage: { warning: ui.warning, info: ui.info, error: ui.error },
  ElMessageBox: { confirm: ui.confirmBox },
}));
vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/desktop-bridge")>()),
  listenAppEvent: (name: string, handler: (payload: unknown) => void) => {
    const handlers = ui.listeners.get(name) ?? new Set();
    handlers.add(handler);
    ui.listeners.set(name, handlers);
    return () => handlers.delete(handler);
  },
}));
vi.mock("@/services/plugin-launch-confirm", () => ({ confirmPluginLaunch: ui.confirmLaunch }));
vi.mock("@/services/plugin-acp-agent-service", () => ({
  prepareAcpAgent: ui.prepare,
  trustAcpAgent: ui.trust,
  launchPluginAcpAgent: ui.launch,
}));

import { pluginAcpAgentNotices, useAcpAgentStart } from "@/composables/use-acp-agent-start";
import { pluginAcpAgents, pluginAcpAgentsReady } from "@/services/contributions/acp-agents-state";
import { applySavedLocale } from "@/i18n/messages";
import { useAiAssistantStore } from "@/stores/ai-assistant";

const FINGERPRINT = "c".repeat(64);
const servers: AcpMcpServerSet = {
  servers: [
    { type: "stdio", name: "github", command: "gh-mcp" },
    { type: "http", name: "linear", url: "https://mcp.linear.app" },
  ],
  signInRequired: [],
  networkFailed: [],
};

function launchSpec(trusted = false) {
  return { pluginId: "acme.agents", agentId: "helper", trusted, previouslyTrusted: false, fingerprint: FINGERPRINT, argv: ["agent"] };
}

let scope: EffectScope | undefined;

function setup(selected: string, markServersDisconnected: (serverIds: string[]) => void = vi.fn()) {
  const addSystemMessage = vi.fn();
  const startNewConversation = vi.fn();
  const launchCommandAgent = vi.fn(async (_adapterId: string) => {});
  const mcpServers = vi.fn(async () => servers);
  scope = effectScope();
  const { startConversationAgent } = scope.run(() =>
    useAcpAgentStart({
      selectedAdapterId: () => selected,
      customAgents: () => [],
      addSystemMessage,
      startNewConversation,
      markServersDisconnected,
    }),
  )!;
  const start = (overrides: { conversationId?: string; recordedAdapterId?: string; hasPreviousSession?: boolean } = {}) =>
    startConversationAgent({
      conversationId: "conversation-1",
      serverId: "server-1",
      projectRoot: "/repo",
      hasPreviousSession: false,
      mcpServers,
      launchCommandAgent,
      ...overrides,
    });
  return { start, addSystemMessage, startNewConversation, launchCommandAgent, mcpServers };
}

afterEach(() => {
  scope?.stop();
  scope = undefined;
  vi.clearAllMocks();
  pluginAcpAgents.value = [];
  pluginAcpAgentsReady.value = false;
});

describe("pluginAcpAgentNotices", () => {
  it("discloses the environment, every forwarded connector and the workbench tools", async () => {
    await applySavedLocale("zh");
    const notices = pluginAcpAgentNotices(servers.servers);
    expect(notices).toHaveLength(3);
    expect(notices[0]).toContain("全部环境变量");
    expect(notices[0]).toContain("API 密钥");
    expect(notices[1]).toContain("github, linear");
    expect(notices[1]).toContain("登录凭据");
    expect(notices[2]).toContain("浏览器");
  });

  it("says when no connector is forwarded", async () => {
    await applySavedLocale("en");
    const notices = pluginAcpAgentNotices([]);
    expect(notices[0]).toContain("all of your environment variables");
    expect(notices[1]).toContain("No MCP connectors");
  });
});

describe("useAcpAgentStart", () => {
  it("shows the confirmation with both disclosures before launching an unconfirmed plugin agent", async () => {
    await applySavedLocale("zh");
    ui.prepare.mockResolvedValue(launchSpec());
    ui.trust.mockResolvedValue(launchSpec(true));
    const { start, mcpServers } = setup("plugin:acme.agents/helper");
    await expect(start()).resolves.toBe("plugin:acme.agents/helper");
    expect(ui.confirmLaunch).toHaveBeenCalledTimes(1);
    const notices = ui.confirmLaunch.mock.calls[0]![1]!.notices!;
    expect(notices.some((notice) => notice.includes("全部环境变量"))).toBe(true);
    expect(notices.some((notice) => notice.includes("github, linear"))).toBe(true);
    expect(mcpServers).toHaveBeenCalled();
    expect(ui.launch).toHaveBeenCalledWith({ serverId: "server-1", pluginId: "acme.agents", agentId: "helper", cwd: "/repo" });
  });

  it("does not launch when the user cancels, and says so", async () => {
    ui.prepare.mockResolvedValue(launchSpec());
    ui.confirmLaunch.mockResolvedValueOnce(false);
    const { start, launchCommandAgent } = setup("plugin:acme.agents/helper");
    await expect(start()).resolves.toBeUndefined();
    expect(ui.trust).not.toHaveBeenCalled();
    expect(ui.launch).not.toHaveBeenCalled();
    expect(launchCommandAgent).not.toHaveBeenCalled();
    expect(ui.info).toHaveBeenCalledTimes(1);
  });

  it("marks a gone plugin agent's conversation unavailable and offers a new conversation", async () => {
    await applySavedLocale("zh");
    ui.prepare.mockRejectedValue(new Error("unavailable: plugin is disabled"));
    const { start, launchCommandAgent, addSystemMessage, startNewConversation } = setup("plugin:acme.agents/helper");
    await expect(start({ recordedAdapterId: "plugin:acme.agents/helper", hasPreviousSession: true })).resolves.toBeUndefined();
    expect(launchCommandAgent).not.toHaveBeenCalled();
    expect(ui.launch).not.toHaveBeenCalled();
    expect(addSystemMessage).toHaveBeenCalledTimes(1);
    expect(addSystemMessage.mock.calls[0]![1]).toContain("智能体已不可用");
    await flushPromises();
    expect(ui.confirmBox).toHaveBeenCalledTimes(1);
    expect(startNewConversation).toHaveBeenCalledTimes(1);
  });

  it("falls back to the default built-in agent and says so once per agent", async () => {
    pluginAcpAgentsReady.value = true;
    const { start, launchCommandAgent } = setup("plugin:acme.agents/fallback-once");
    await expect(start()).resolves.toBe("claude-code");
    await expect(start()).resolves.toBe("claude-code");
    expect(launchCommandAgent.mock.calls).toEqual([["claude-code"], ["claude-code"]]);
    expect(ui.prepare).not.toHaveBeenCalled();
    expect(ui.warning).toHaveBeenCalledTimes(1);
  });

  it("opens one unavailable prompt per conversation at a time", async () => {
    let close: (() => void) | undefined;
    ui.confirmBox.mockImplementationOnce(
      () => new Promise<string>((_resolve, reject) => (close = () => reject(new Error("cancel")))),
    );
    const { start, addSystemMessage, startNewConversation } = setup("codex");
    const blocked = { conversationId: "conversation-prompt", recordedAdapterId: "plugin:acme.agents/helper", hasPreviousSession: true };
    await start(blocked);
    await start(blocked);
    await start(blocked);
    await flushPromises();
    expect(ui.confirmBox).toHaveBeenCalledTimes(1);
    expect(addSystemMessage).toHaveBeenCalledTimes(1);
    close!();
    await flushPromises();
    expect(startNewConversation).not.toHaveBeenCalled();
    // Closed: the next start may prompt again.
    await start(blocked);
    await flushPromises();
    expect(ui.confirmBox).toHaveBeenCalledTimes(2);
  });
});

describe("a plugin agent the host disconnects", () => {
  async function storeWithConversations() {
    setActivePinia(createPinia());
    const store = useAiAssistantStore();
    await store.hydrate("workspace:one");
    const running = (adapterId: string) => {
      const conversation = store.createConversation("agentic");
      store.setConversationAgentState(conversation.id, { acpSessionId: `session-${conversation.id}`, acpAdapterId: adapterId, agentReady: true });
      return conversation;
    };
    return {
      store,
      plugin: running("plugin:acme.agents/helper"),
      otherPlugin: running("plugin:acme.other/helper"),
      builtin: running("claude-code"),
    };
  }

  it("marks only the conversations on the listed server ids not ready, in every cached workspace", async () => {
    const { store, plugin, otherPlugin, builtin } = await storeWithConversations();
    await store.switchWorkspaceScope("workspace:two");
    const elsewhere = store.createConversation("agentic");
    store.setConversationAgentState(elsewhere.id, { acpSessionId: "session-x", acpAdapterId: "codex", agentReady: true });
    setup("plugin:acme.agents/helper", (ids) => void store.markAcpServersDisconnected(ids));
    emitHostEvent("acp:disconnected", { serverIds: [plugin.acpServerId], pluginId: "acme.agents" });
    expect(store.conversationById(plugin.id)?.agentReady).toBe(false);
    // The session id is kept so the restarted agent can load it.
    expect(store.conversationById(plugin.id)?.acpSessionId).toBe(`session-${plugin.id}`);
    expect(store.conversationById(otherPlugin.id)?.agentReady).toBe(true);
    expect(store.conversationById(builtin.id)?.agentReady).toBe(true);
    expect(store.conversationById(elsewhere.id)?.agentReady).toBe(true);
    // A malformed payload changes nothing.
    emitHostEvent("acp:disconnected", { serverIds: "all" });
    emitHostEvent("acp:disconnected", null);
    expect(store.conversationById(otherPlugin.id)?.agentReady).toBe(true);
  });

  it("restarts the agent on the next start of a disconnected conversation", async () => {
    const { store, plugin } = await storeWithConversations();
    ui.prepare.mockResolvedValue(launchSpec(true));
    const { start } = setup("plugin:acme.agents/helper", (ids) => void store.markAcpServersDisconnected(ids));
    emitHostEvent("acp:disconnected", { serverIds: [plugin.acpServerId], pluginId: "acme.agents" });
    const conversation = store.conversationById(plugin.id)!;
    // What startAgent checks before it starts anything.
    expect(conversation.agentReady && conversation.acpSessionId).toBeFalsy();
    await expect(
      start({ conversationId: plugin.id, recordedAdapterId: conversation.acpAdapterId, hasPreviousSession: true }),
    ).resolves.toBe("plugin:acme.agents/helper");
    expect(ui.prepare).toHaveBeenCalledTimes(1);
    expect(ui.launch).toHaveBeenCalledWith(expect.objectContaining({ pluginId: "acme.agents", agentId: "helper" }));
  });

  it("stops listening when its scope ends", () => {
    const markServersDisconnected = vi.fn();
    setup("claude-code", markServersDisconnected);
    scope!.stop();
    scope = undefined;
    emitHostEvent("acp:disconnected", { serverIds: ["server-1"] });
    expect(markServersDisconnected).not.toHaveBeenCalled();
  });
});
