/**
 * The AI assistant's agent start for a conversation (built-in, custom or
 * plugin ACP agents), around domain/ai/acp-agent-start.ts: the confirmation
 * of a plugin agent with its disclosures, and what the user is told when an
 * agent is cancelled, changed, gone or falls back. Only user actions reach
 * this (starting a session or sending a message); AI tools cannot start or
 * confirm a plugin agent.
 *
 * When the host closes a plugin's agents (plugin disabled, uninstalled,
 * reloaded, upgraded or failed) it emits `acp:disconnected` with their server
 * ids; the conversations on them become not ready, so their next send starts
 * the agent again (prepare, confirm when the launch changed, launch). An
 * agent that is gone by then gets the "agent unavailable" prompt.
 */
import { onScopeDispose } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  acpAdapterLabel,
  createOnceGate,
  DEFAULT_ACP_ADAPTER_ID,
  startAcpAgent,
  type AcpAgentStartOutcome,
} from "@/domain/ai/acp-agent-start";
import type { AcpMcpServer, AcpMcpServerSet } from "@/domain/ai/acp-mcp-servers";
import { useVueI18n } from "@/i18n/messages";
import { pluginAcpAgents, pluginAcpAgentsReady } from "@/services/contributions/acp-agents-state";
import { launchPluginAcpAgent, prepareAcpAgent, trustAcpAgent } from "@/services/plugin-acp-agent-service";
import { confirmPluginLaunch } from "@/services/plugin-launch-confirm";
import { listenAppEvent } from "@/services/desktop-bridge";

/** Emitted by the host with the ACP server ids it disconnected on its own. */
export const ACP_DISCONNECTED_EVENT = "acp:disconnected";

/** Module scope: the fallback is announced once per app session per agent. */
const fallbackNoticeGate = createOnceGate();
/** Module scope: a conversation gets the "agent unavailable" note once per app session. */
const unavailableNoteGate = createOnceGate();
/** Conversations whose "agent unavailable" prompt is open: one prompt at a time each. */
const openUnavailablePrompts = new Set<string>();

/**
 * What a plugin agent receives besides its launch, for the confirmation: the
 * whole environment, the connectors of this session (`servers`, as sent to
 * acp_new_session / acp_load_session) and the workbench tools the host adds
 * to every ACP session.
 */
export function pluginAcpAgentNotices(servers: readonly AcpMcpServer[]): string[] {
  const { t } = useVueI18n();
  const names = servers.map((server) => server.name);
  return [
    t("ai.pluginAgent.envNotice"),
    names.length ? t("ai.pluginAgent.mcpNotice", { names: names.join(", ") }) : t("ai.pluginAgent.mcpNoticeNone"),
    t("ai.pluginAgent.workbenchNotice"),
  ];
}

export interface ConversationAgentStart {
  conversationId: string;
  serverId: string;
  projectRoot: string;
  recordedAdapterId?: string;
  hasPreviousSession: boolean;
  /** The connectors this session receives (resolved once per start by the caller). */
  mcpServers: () => Promise<AcpMcpServerSet>;
  /** acp_launch for a built-in or custom adapter id. */
  launchCommandAgent: (adapterId: string) => Promise<void>;
}

export function useAcpAgentStart(options: {
  selectedAdapterId: () => string | undefined;
  customAgents: () => readonly { id: string; name: string }[] | undefined;
  addSystemMessage: (conversationId: string, content: string) => void;
  startNewConversation: () => unknown;
  /** Marks the conversations on these ACP server ids as not ready (agentReady false). */
  markServersDisconnected: (serverIds: string[]) => void;
}) {
  const { t, localeChain } = useVueI18n();
  const label = (adapterId: string) =>
    acpAdapterLabel(adapterId, {
      pluginAgents: pluginAcpAgents.value,
      customAgents: options.customAgents(),
      localeChain: localeChain.value,
    });

  async function reportConversationUnavailable(conversationId: string, outcome: Extract<AcpAgentStartOutcome, { kind: "conversation-unavailable" }>) {
    const message = t("ai.pluginAgent.conversationUnavailable", {
      name: label(outcome.recordedAdapterId),
      current: label(outcome.adapterId),
    });
    if (unavailableNoteGate(conversationId)) options.addSystemMessage(conversationId, message);
    if (openUnavailablePrompts.has(conversationId)) return;
    openUnavailablePrompts.add(conversationId);
    let confirmed = false;
    try {
      await ElMessageBox.confirm(message, t("ai.pluginAgent.conversationUnavailableTitle"), {
        type: "warning",
        confirmButtonText: t("ai.pluginAgent.newConversation"),
        cancelButtonText: t("common.cancel"),
      });
      confirmed = true;
    } catch {
      // Cancelled or closed.
    } finally {
      openUnavailablePrompts.delete(conversationId);
    }
    if (confirmed) await options.startNewConversation();
  }

  const stopDisconnected = listenAppEvent<{ serverIds?: unknown }>(ACP_DISCONNECTED_EVENT, (payload) => {
    const serverIds = Array.isArray(payload?.serverIds)
      ? payload.serverIds.filter((id): id is string => typeof id === "string" && id !== "")
      : [];
    if (serverIds.length) options.markServersDisconnected(serverIds);
  });
  onScopeDispose(() => stopDisconnected());

  /**
   * Starts the agent process for a conversation. Resolves the adapter id to
   * record with the session when the agent runs, or undefined when nothing
   * was started (the user was told why). Host errors other than `untrusted` /
   * `unavailable` are thrown.
   */
  async function startConversationAgent(input: ConversationAgentStart): Promise<string | undefined> {
    const outcome = await startAcpAgent(
      {
        selectedAdapterId: options.selectedAdapterId(),
        recordedAdapterId: input.recordedAdapterId,
        hasPreviousSession: input.hasPreviousSession,
        serverId: input.serverId,
        projectRoot: input.projectRoot,
        pluginAgents: { ready: pluginAcpAgentsReady.value, agents: pluginAcpAgents.value },
      },
      {
        launchCommandAgent: input.launchCommandAgent,
        prepare: prepareAcpAgent,
        trust: trustAcpAgent,
        launchPlugin: launchPluginAcpAgent,
        confirm: async (launch) =>
          confirmPluginLaunch(launch, { notices: pluginAcpAgentNotices((await input.mcpServers()).servers) }),
      },
    );
    switch (outcome.kind) {
      case "started":
        if (outcome.fallbackFrom && fallbackNoticeGate(outcome.fallbackFrom)) {
          ElMessage.warning(t("ai.pluginAgent.fallback", { name: label(outcome.fallbackFrom), fallback: label(DEFAULT_ACP_ADAPTER_ID) }));
        }
        return outcome.adapterId;
      case "cancelled":
        ElMessage.info(t("ai.pluginAgent.cancelled", { name: label(outcome.adapterId) }));
        return undefined;
      case "changed":
        ElMessage.error(t("ai.pluginAgent.changed", { name: label(outcome.adapterId) }));
        return undefined;
      case "conversation-unavailable":
        // Not awaited: the start (and the send waiting on it) ends while the prompt is open.
        void reportConversationUnavailable(input.conversationId, outcome);
        return undefined;
    }
  }

  return { startConversationAgent };
}
