import { onScopeDispose } from "vue";
import { ElMessageBox } from "element-plus";
import {
  acpAdapterLabel,
  createOnceGate,
  startAcpAgent,
  type AcpAgentStartOutcome,
} from "@/domain/ai/acp-agent-start";
import { useVueI18n } from "@/i18n/messages";
import { listenAppEvent } from "@/services/desktop-bridge";

/** Emitted by the host with the ACP server ids it disconnected on its own. */
export const ACP_DISCONNECTED_EVENT = "acp:disconnected";

/** Module scope: a conversation gets the "agent unavailable" note once per app session. */
const unavailableNoteGate = createOnceGate();
/** Conversations whose "agent unavailable" prompt is open: one prompt at a time each. */
const openUnavailablePrompts = new Set<string>();

export interface ConversationAgentStart {
  conversationId: string;
  recordedAdapterId?: string;
  hasPreviousSession: boolean;
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
  const { t } = useVueI18n();
  const label = (adapterId: string) => acpAdapterLabel(adapterId, options.customAgents());

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

  async function startConversationAgent(input: ConversationAgentStart): Promise<string | undefined> {
    const outcome = await startAcpAgent({
      selectedAdapterId: options.selectedAdapterId(),
      recordedAdapterId: input.recordedAdapterId,
      hasPreviousSession: input.hasPreviousSession,
    }, input.launchCommandAgent);
    if (outcome.kind === "started") return outcome.adapterId;
    void reportConversationUnavailable(input.conversationId, outcome);
    return undefined;
  }

  return { startConversationAgent };
}
