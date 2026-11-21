import type { AiAssistantEvent, AiAssistantMessage } from "@/stores/ai-assistant";
import type { AiLoopMessage } from "@/services/ai-agent-loop";

export interface RewindableConversation {
  messages: readonly AiAssistantMessage[];
  agentEvents: readonly AiAssistantEvent[];
  agentHistory: readonly AiLoopMessage[];
}

export interface RewoundConversation {
  messages: AiAssistantMessage[];
  agentEvents: AiAssistantEvent[];
  agentHistory: AiLoopMessage[];
  /** The user message the conversation was rewound to (it is removed too). */
  removed: AiAssistantMessage;
}

/**
 * Drops a user message and everything after it, so it can be sent again
 * (regenerate, retry) or replaced (edit and resend).
 *
 * The model history is cut at the matching user turn: the same position counted
 * from the end, holding the same text. Turns that never entered the history
 * (compacted, or small talk kept out of it) make that match fail; the history is
 * then cleared so the next send rebuilds context from the visible messages
 * instead of replaying turns the user just removed.
 */
export function rewindConversation(
  conversation: RewindableConversation,
  messageId: string,
): RewoundConversation | undefined {
  const index = conversation.messages.findIndex(
    (message) => message.id === messageId && message.role === "user",
  );
  const removed = conversation.messages[index];
  if (index < 0 || !removed) return undefined;

  const removedIds = new Set(
    conversation.messages.slice(index).map((message) => message.id).filter(Boolean),
  );
  const agentEvents = conversation.agentEvents.filter((event) => {
    if (event.triggerMessageId) return !removedIds.has(event.triggerMessageId);
    return event.afterMessageIndex === undefined || event.afterMessageIndex < index;
  });

  const userTurnsFromTarget = conversation.messages
    .slice(index)
    .filter((message) => message.role === "user").length;
  let historyCut = -1;
  let seen = 0;
  for (let position = conversation.agentHistory.length - 1; position >= 0; position -= 1) {
    if (conversation.agentHistory[position]?.role !== "user") continue;
    seen += 1;
    if (seen === userTurnsFromTarget) {
      historyCut = position;
      break;
    }
  }
  const matches = historyCut >= 0 && conversation.agentHistory[historyCut]?.content === removed.content;

  return {
    messages: conversation.messages.slice(0, index),
    agentEvents,
    agentHistory: matches ? conversation.agentHistory.slice(0, historyCut) : [],
    removed,
  };
}

/** The user turn that produced an assistant (or error) message. */
export function precedingUserMessage(
  messages: readonly AiAssistantMessage[],
  messageId: string,
): AiAssistantMessage | undefined {
  const index = messages.findIndex((message) => message.id === messageId);
  for (let position = index - 1; position >= 0; position -= 1) {
    if (messages[position]?.role === "user") return messages[position];
  }
  return undefined;
}
