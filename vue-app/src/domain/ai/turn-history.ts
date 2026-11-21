/**
 * Which earlier messages one agentic turn sends to the model, and whether the
 * turn's loop history may replace the conversation's saved loop history.
 *
 * - historyNone (a plugin turn with history "none"): only this turn's message;
 *   the prompt carries its own context. Nothing earlier is read, and the saved
 *   history is left as it is, so later default turns are unaffected.
 * - directConversation (a greeting or other social turn): only this message;
 *   an earlier saved task stays saved so the next actionable request resumes it.
 * - otherwise: the saved loop history plus this message, or — before any loop
 *   history exists — the visible conversation messages (which already include it).
 *
 * The turn's own tool calls and results are added by the agent loop on top of
 * the returned messages, so they always see each other.
 */
export function selectAiTurnHistory<M>(input: {
  historyNone: boolean;
  directConversation: boolean;
  persistedHistory: readonly M[];
  currentMessage: M;
  continueHistory: (persistedHistory: M[], currentMessage: M) => M[];
  messageHistory: () => M[];
}): { messages: M[]; persist: boolean } {
  if (input.historyNone) return { messages: [input.currentMessage], persist: false };
  const hasPersisted = input.persistedHistory.length > 0;
  if (input.directConversation) return { messages: [input.currentMessage], persist: !hasPersisted };
  return {
    messages: hasPersisted
      ? input.continueHistory([...input.persistedHistory], input.currentMessage)
      : input.messageHistory(),
    persist: true,
  };
}
