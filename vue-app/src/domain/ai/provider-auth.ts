export interface AiProviderAuthInput {
  provider?: string;
  baseUrl: string;
  apiKey: string;
}

export type AiProviderAuthValidation =
  | { valid: true }
  | { valid: false; reason: "missing_api_key" | "unresolved_base_url" };

/** A {name} a plugin preset left in the base URL for the user to fill in. */
const UNRESOLVED_PLACEHOLDER_PATTERN = /\{[a-z_]{1,24}\}/;

function isLoopbackBaseUrl(baseUrl: string) {
  try {
    const hostname = new URL(baseUrl).hostname.toLowerCase();
    return (
      hostname === "localhost" ||
      hostname === "127.0.0.1" ||
      hostname === "::1" ||
      hostname === "[::1]"
    );
  } catch {
    return false;
  }
}

/** Remote built-in providers require credentials; local and custom endpoints may opt out. */
export function validateAiProviderAuth(
  input: AiProviderAuthInput,
): AiProviderAuthValidation {
  if (UNRESOLVED_PLACEHOLDER_PATTERN.test(input.baseUrl)) return { valid: false, reason: "unresolved_base_url" };
  if (input.apiKey.trim()) return { valid: true };
  if (input.provider === "ollama") return { valid: true };
  if (isLoopbackBaseUrl(input.baseUrl)) return { valid: true };
  return { valid: false, reason: "missing_api_key" };
}

/** The i18n key of the message for a failed validation. */
export function aiProviderAuthMessageKey(
  validation: Extract<AiProviderAuthValidation, { valid: false }>,
): "ai.unresolvedBaseUrl" | "ai.missingApiKey" {
  return validation.reason === "unresolved_base_url" ? "ai.unresolvedBaseUrl" : "ai.missingApiKey";
}
