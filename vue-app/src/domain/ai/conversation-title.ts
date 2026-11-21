export const AI_CONVERSATION_TITLE_MAX_LENGTH = 120;

export function normalizeAiConversationTitle(
  value: unknown,
  maxLength = AI_CONVERSATION_TITLE_MAX_LENGTH,
) {
  if (typeof value !== "string" || maxLength <= 0) return "";
  const normalized = value.trim().replace(/\s+/g, " ");
  return Array.from(normalized).slice(0, maxLength).join("");
}
