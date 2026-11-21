export const SESSION_PROVIDER_IDS = [
  "claude",
  "pi",
  "codex",
  "gemini",
  "opencode",
  "kimi",
  "qodercn",
  "qoder",
  "grok",
] as const;

export type SessionProviderId = (typeof SESSION_PROVIDER_IDS)[number];

export type SessionDeepLink = {
  provider: SessionProviderId;
  sessionId: string;
};

export type SessionDeepLinkParseResult =
  | { kind: "none" }
  | { kind: "invalid" }
  | ({ kind: "valid" } & SessionDeepLink);

const providerIds = new Set<string>(SESSION_PROVIDER_IDS);

function queryText(value: unknown): string {
  if (Array.isArray(value))
    return value.length === 1 ? queryText(value[0]) : "";
  return typeof value === "string" ? value.trim() : "";
}

export function parseSessionDeepLink(
  query: Record<string, unknown>,
): SessionDeepLinkParseResult {
  const hasProvider = query.provider !== undefined && query.provider !== null;
  const hasSessionId =
    query.sessionId !== undefined && query.sessionId !== null;
  if (!hasProvider && !hasSessionId) return { kind: "none" };

  const provider = queryText(query.provider);
  const sessionId = queryText(query.sessionId);
  if (!providerIds.has(provider) || !sessionId) return { kind: "invalid" };
  return { kind: "valid", provider: provider as SessionProviderId, sessionId };
}
