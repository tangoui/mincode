import { createPinia, setActivePinia } from "pinia";
import { effectScope, type EffectScope } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";

const ui = vi.hoisted(() => ({
  confirmBox: vi.fn(async () => "confirm"),
  listeners: new Map<string, Set<(payload: unknown) => void>>(),
}));

/** Delivers a host event to the composable's listeners. */
function emitHostEvent(name: string, payload: unknown) {
  for (const handler of ui.listeners.get(name) ?? []) handler(payload);
}

vi.mock("element-plus", () => ({
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
import { useAcpAgentStart } from "@/composables/use-acp-agent-start";
import { useAiAssistantStore } from "@/stores/ai-assistant";

let scope: EffectScope | undefined;

function setup(selected: string, markServersDisconnected: (serverIds: string[]) => void = vi.fn()) {
  const addSystemMessage = vi.fn();
  const startNewConversation = vi.fn();
  const launchCommandAgent = vi.fn(async (_adapterId: string) => {});
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
      hasPreviousSession: false,
      launchCommandAgent,
      ...overrides,
    });
  return { start, addSystemMessage, startNewConversation, launchCommandAgent };
}

afterEach(() => {
  scope?.stop();
  scope = undefined;
  vi.clearAllMocks();
});

describe("an agent the host disconnects", () => {
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
      agent: running("codex"),
      otherAgent: running("opencode"),
      builtin: running("claude-code"),
    };
  }

  it("marks only the conversations on the listed server ids not ready, in every cached workspace", async () => {
    const { store, agent, otherAgent, builtin } = await storeWithConversations();
    await store.switchWorkspaceScope("workspace:two");
    const elsewhere = store.createConversation("agentic");
    store.setConversationAgentState(elsewhere.id, { acpSessionId: "session-x", acpAdapterId: "codex", agentReady: true });
    setup("codex", (ids) => void store.markAcpServersDisconnected(ids));
    emitHostEvent("acp:disconnected", { serverIds: [agent.acpServerId], pluginId: "acme.agents" });
    expect(store.conversationById(agent.id)?.agentReady).toBe(false);
    // The session id is kept so the restarted agent can load it.
    expect(store.conversationById(agent.id)?.acpSessionId).toBe(`session-${agent.id}`);
    expect(store.conversationById(otherAgent.id)?.agentReady).toBe(true);
    expect(store.conversationById(builtin.id)?.agentReady).toBe(true);
    expect(store.conversationById(elsewhere.id)?.agentReady).toBe(true);
    // A malformed payload changes nothing.
    emitHostEvent("acp:disconnected", { serverIds: "all" });
    emitHostEvent("acp:disconnected", null);
    expect(store.conversationById(otherAgent.id)?.agentReady).toBe(true);
  });

  it("restarts the agent on the next start of a disconnected conversation", async () => {
    const { store, agent } = await storeWithConversations();
    const { start, launchCommandAgent } = setup("codex", (ids) => void store.markAcpServersDisconnected(ids));
    emitHostEvent("acp:disconnected", { serverIds: [agent.acpServerId], pluginId: "acme.agents" });
    const conversation = store.conversationById(agent.id)!;
    // What startAgent checks before it starts anything.
    expect(conversation.agentReady && conversation.acpSessionId).toBeFalsy();
    await expect(
      start({ conversationId: agent.id, recordedAdapterId: conversation.acpAdapterId, hasPreviousSession: true }),
    ).resolves.toBe("codex");
    expect(launchCommandAgent).toHaveBeenCalledWith("codex");
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
