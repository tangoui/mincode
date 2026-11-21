/**
 * Decides which assistant message a local Agent (ACP) text chunk belongs to. Text after a new tool
 * call starts a new message, so the thread shows "text → tool → text" in the order it happened;
 * without the break, later text was appended to the message before the tool. Agents may keep one
 * messageId across tool calls, so agent ids are mapped to segment ids per conversation.
 */
export function createAcpTextSegments(newId: () => string) {
  const pendingBreaks = new Set<string>();
  const segmentsByAgentId = new Map<string, Map<string, string>>();

  return {
    /** Forget the previous turn's segments when a new prompt starts. */
    reset(conversationId: string) {
      pendingBreaks.delete(conversationId);
      segmentsByAgentId.delete(conversationId);
    },
    /** A tool call that was not seen before ends the current text segment. */
    breakAfterTool(conversationId: string) {
      pendingBreaks.add(conversationId);
    },
    /** `current` is the message the conversation's text was last written to. */
    messageIdFor(conversationId: string, agentMessageId: string, current: string | undefined) {
      let segments = segmentsByAgentId.get(conversationId);
      if (!segments) {
        segments = new Map();
        segmentsByAgentId.set(conversationId, segments);
      }
      const broken = pendingBreaks.delete(conversationId);
      let messageId = broken
        ? undefined
        : agentMessageId
          ? segments.get(agentMessageId) ?? agentMessageId
          : current;
      messageId ??= newId();
      if (agentMessageId) segments.set(agentMessageId, messageId);
      return messageId;
    },
  };
}
