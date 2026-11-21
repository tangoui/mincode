/** Credential header for Anthropic-format profiles: `x-api-key`, or `Authorization: Bearer` (Claude Code's ANTHROPIC_AUTH_TOKEN). */
export const AI_ANTHROPIC_AUTH_MODES = ["x-api-key", "bearer"] as const;

export type AiAnthropicAuth = (typeof AI_ANTHROPIC_AUTH_MODES)[number];

/** Keeps persisted or imported values on a supported mode; anything else falls back to `x-api-key`. */
export function normalizeAnthropicAuth(value: unknown): AiAnthropicAuth {
  return typeof value === "string" && AI_ANTHROPIC_AUTH_MODES.includes(value as AiAnthropicAuth)
    ? (value as AiAnthropicAuth)
    : "x-api-key";
}
