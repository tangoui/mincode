// Connectors handed to an external ACP agent (Claude Code and others) when a
// session starts. Signed-in remote connectors are handed over with the host's
// token as an Authorization: Bearer header (spec §7.2); ones that still need a
// sign-in, or whose token could not be refreshed for a network failure, are
// left out and reported so the session can say so once.
import { mcpAuthErrorCode } from "@/domain/ai/mcp-auth";
import { normalizeMcpServerForRuntime, type StoredMcpServer } from "@/domain/ai/mcp-servers";

export interface AcpMcpServer {
  type: "stdio" | "http";
  name: string;
  command?: string;
  args?: string[];
  env?: Array<{ name: string; value: string }>;
  url?: string;
  headers?: Array<{ name: string; value: string }>;
}

export interface AcpMcpServerSet {
  servers: AcpMcpServer[];
  /** Ids of enabled oauth connectors left out because they have no usable sign-in. */
  signInRequired: string[];
  /**
   * Ids left out because refreshing an expired token did not reach the
   * authorization server: a network problem, not a reason to sign in again.
   */
  networkFailed: string[];
}

/** Error codes of a token lookup that failed on the network, not on the sign-in. */
const NETWORK_CODES = new Set(["MCP_AUTH_NETWORK", "MCP_AUTH_TIMEOUT"]);

/**
 * Resolves the Authorization header of a signed-in connector, or null when it
 * has no sign-in; a network failure is thrown so it is not mistaken for one.
 */
export type AcpAuthHeaders = (server: StoredMcpServer) => Promise<Record<string, string> | null>;

function stringRecord(value: unknown) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {} as Record<string, string>;
  return Object.fromEntries(
    Object.entries(value).filter((entry): entry is [string, string] => typeof entry[1] === "string"),
  );
}

function pairs(record: Record<string, string>) {
  return Object.entries(record).map(([name, value]) => ({ name, value }));
}

export async function buildAcpMcpServers(
  stored: readonly unknown[],
  projectRoot: string,
  authHeaders: AcpAuthHeaders,
): Promise<AcpMcpServerSet> {
  const entries = stored.filter(
    (server): server is StoredMcpServer =>
      Boolean(server && typeof server === "object" && (server as StoredMcpServer).enabled !== false && (server as StoredMcpServer).id?.trim()),
  );
  // Token lookups are independent; resolve them together and keep list order.
  type Resolved = { server: AcpMcpServer } | { skipped: string; reason: "sign-in" | "network" } | null;
  const resolved = await Promise.all(entries.map(async (storedServer): Promise<Resolved> => {
    const server = normalizeMcpServerForRuntime(storedServer, projectRoot);
    const name = server.id.trim();
    if (server.transport === "http") {
      if (!server.url?.trim()) return null;
      let headers = stringRecord(server.headers);
      if (server.auth === "oauth") {
        let auth: Record<string, string> | null;
        try {
          auth = await authHeaders(server);
        } catch (error) {
          // Any other failure leaves no usable token: signing in again is the remedy.
          return { skipped: name, reason: NETWORK_CODES.has(mcpAuthErrorCode(error) ?? "") ? "network" : "sign-in" };
        }
        if (!auth) return { skipped: name, reason: "sign-in" };
        // The host token replaces a configured Authorization header, as the MCP gateway does.
        headers = Object.fromEntries(Object.entries(headers).filter(([key]) => key.toLowerCase() !== "authorization"));
        headers = { ...headers, ...auth };
      }
      return { server: { type: "http", name, url: server.url.trim(), headers: pairs(headers) } satisfies AcpMcpServer };
    }
    if (!server.command?.trim()) return null;
    return {
      server: {
        type: "stdio",
        name,
        command: server.command.trim(),
        args: server.args?.trim() ? server.args.trim().split(/\s+/) : [],
        env: pairs(stringRecord(server.env)),
      } satisfies AcpMcpServer,
    };
  }));
  const result: AcpMcpServerSet = { servers: [], signInRequired: [], networkFailed: [] };
  for (const item of resolved) {
    if (!item) continue;
    if ("skipped" in item) (item.reason === "network" ? result.networkFailed : result.signInRequired).push(item.skipped);
    else result.servers.push(item.server);
  }
  return result;
}
