export const AI_API_FORMATS = ["openai", "openai-responses", "anthropic"] as const;

export type AiApiFormat = (typeof AI_API_FORMATS)[number];

/** Keeps persisted or imported profile values on one of the supported request protocols. */
export function normalizeAiApiFormat(
  value: unknown,
  fallback: AiApiFormat = "openai",
): AiApiFormat {
  return typeof value === "string" && AI_API_FORMATS.includes(value as AiApiFormat)
    ? (value as AiApiFormat)
    : fallback;
}
