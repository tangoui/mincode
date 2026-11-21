// Connector sign-in (mcpauth) error codes. The backend puts a stable
// "[MCP_AUTH_*]" code into the error text, but the text around it differs per
// path (connect, login, IPC wrapping), so codes are found anywhere in it.

const AUTH_CODE_PATTERN = /\[(MCP_AUTH_[A-Z_]+)\]/;

export type McpAuthErrorCode = `MCP_AUTH_${string}`;

function errorText(error: unknown) {
  if (error instanceof Error) return error.message;
  if (error && typeof error === "object" && typeof (error as { message?: unknown }).message === "string") {
    return (error as { message: string }).message;
  }
  return String(error);
}

/** The first "[MCP_AUTH_*]" code in the error text, or null. */
export function mcpAuthErrorCode(error: unknown): McpAuthErrorCode | null {
  return (AUTH_CODE_PATTERN.exec(errorText(error))?.[1] as McpAuthErrorCode | undefined) ?? null;
}

/** A connect or token request was refused for want of a (fresh) sign-in. */
export function isMcpAuthRequired(error: unknown) {
  return mcpAuthErrorCode(error) === "MCP_AUTH_REQUIRED";
}

const MESSAGE_KEYS: Record<string, string> = {
  MCP_AUTH_REQUIRED: "required",
  // An expired token whose refresh did not reach the server: a network
  // problem, not a reason to sign in again.
  MCP_AUTH_NETWORK: "network",
  MCP_AUTH_UNSUPPORTED: "unsupported",
  MCP_AUTH_REGISTER_REFUSED: "registerRefused",
  MCP_AUTH_CANCELLED: "cancelled",
  MCP_AUTH_TIMEOUT: "timeout",
  MCP_AUTH_DENIED: "denied",
  MCP_AUTH_BROWSER: "browser",
  MCP_AUTH_INSECURE_URL: "insecureUrl",
};

/** i18n key describing a code; "generic" takes the raw error as {message}. */
export function mcpAuthErrorMessageKey(code: McpAuthErrorCode | null) {
  return `settings.mcp.auth.error.${(code && MESSAGE_KEYS[code]) || "generic"}`;
}
