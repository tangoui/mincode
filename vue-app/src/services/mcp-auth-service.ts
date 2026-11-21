// Connector sign-in (backend package mcpauth). Tokens never reach the UI
// except through acpAuthHeaders, which hands a signed-in connector to an
// external ACP agent as an Authorization header (spec §7.2).
import { isMcpAuthRequired, mcpAuthErrorCode } from "@/domain/ai/mcp-auth";
import type { StoredMcpServer } from "@/domain/ai/mcp-servers";
import { invokeBackend, listenAppEvent } from "@/services/desktop-bridge";

export type McpAuthStatus = "signed-out" | "signed-in" | "needs-login";
export type McpSignInResult = "signed-in" | "cancelled" | "failed";
export type McpAuthTarget = Pick<StoredMcpServer, "id" | "url">;

export interface McpDeviceCode {
  userCode: string;
  verificationUri: string;
}

export interface McpSignInOptions {
  /** Aborting cancels the login in the backend (its call context). */
  signal?: AbortSignal;
  /** Receives the error of a failed sign-in (not of a cancelled one). */
  onError?: (error: unknown) => void;
}

const DEVICE_CODE_EVENT = "mcp-oauth-device-code";
const STATUSES: readonly McpAuthStatus[] = ["signed-out", "signed-in", "needs-login"];
const MAX_USER_CODE_LENGTH = 64;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function connectorArgs(server: McpAuthTarget) {
  return { serverId: server.id.trim(), url: server.url.trim() };
}

export async function mcpAuthStatus(server: McpAuthTarget): Promise<McpAuthStatus> {
  const result = await invokeBackend<unknown>("mcp_oauth_status", connectorArgs(server));
  const status = isRecord(result) ? result.status : undefined;
  if (!STATUSES.includes(status as McpAuthStatus)) throw new Error("Invalid mcp_oauth_status response");
  return status as McpAuthStatus;
}

/** Runs the browser (or device) sign-in; resolves when it ends, never rejects. */
export async function signInMcpServer(server: McpAuthTarget, options: McpSignInOptions = {}): Promise<McpSignInResult> {
  try {
    const result = await invokeBackend<unknown>("mcp_oauth_login", connectorArgs(server), { signal: options.signal });
    const status = isRecord(result) ? result.status : undefined;
    if (status === "signed-in" || status === "cancelled") return status;
    throw new Error("Invalid mcp_oauth_login response");
  } catch (error) {
    if (options.signal?.aborted || mcpAuthErrorCode(error) === "MCP_AUTH_CANCELLED") return "cancelled";
    options.onError?.(error);
    return "failed";
  }
}

/** Deletes this device's tokens for the connector (its settings stay). */
export async function signOutMcpServer(server: Pick<StoredMcpServer, "id">) {
  await invokeBackend("mcp_oauth_logout", { serverId: server.id.trim() });
}

/**
 * The Authorization header for handing a signed-in connector to an external
 * ACP agent, refreshed first when it is about to expire; null when the
 * connector has no usable sign-in ([MCP_AUTH_REQUIRED]). Other failures, such
 * as [MCP_AUTH_NETWORK] when a refresh could not reach the server, are thrown
 * so the caller does not ask for a new sign-in. The token is never logged.
 */
export async function acpAuthHeaders(server: McpAuthTarget): Promise<Record<string, string> | null> {
  let result: unknown;
  try {
    result = await invokeBackend<unknown>("mcp_oauth_access_token", connectorArgs(server));
  } catch (error) {
    if (isMcpAuthRequired(error)) return null;
    throw error;
  }
  const token = isRecord(result) ? result.token : undefined;
  return typeof token === "string" && token ? { Authorization: `Bearer ${token}` } : null;
}

function deviceCodeFor(serverId: string, payload: unknown): McpDeviceCode | null {
  if (!isRecord(payload) || payload.serverId !== serverId) return null;
  const { userCode, verificationUri } = payload;
  if (typeof userCode !== "string" || !userCode.trim() || userCode.length > MAX_USER_CODE_LENGTH) return null;
  if (typeof verificationUri !== "string") return null;
  try {
    // Only a web page is opened; anything else is not a verification page.
    if (new URL(verificationUri).protocol !== "https:") return null;
  } catch {
    return null;
  }
  return { userCode: userCode.trim(), verificationUri };
}

/** Calls handler with the device-flow code the backend emits for serverId. */
export function onMcpDeviceCode(serverId: string, handler: (code: McpDeviceCode) => void): () => void {
  const id = serverId.trim();
  return listenAppEvent<unknown>(DEVICE_CODE_EVENT, (payload) => {
    const code = deviceCodeFor(id, payload);
    if (code) handler(code);
  });
}
