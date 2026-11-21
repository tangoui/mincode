// ai.config login (spec §7.2, ruling R4): a plugin page asks the host to sign
// in to a connector the plugin installed. The host finds that connector by its
// syncSource, runs the sign-in itself and answers with a status only; tokens,
// error text and the connector's settings never go back to the page.
import { findSynced } from "@/domain/ai/sync-source";
import { mcpAuthErrorCode } from "@/domain/ai/mcp-auth";

/** Stable reasons a page may show; never the raw error text. */
export type AiConfigLoginFailure = "not-installed" | "unsupported" | "network" | "timeout" | "denied" | "failed";

export type AiConfigLoginResult =
  | { status: "signed-in" }
  | { status: "cancelled" }
  | { status: "failed"; reason: AiConfigLoginFailure };

/** The connector a login signs in to, as the host's sign-in needs it. */
export interface AiConfigLoginTarget {
  id: string;
  url: string;
  /** Host of `url`, shown in the host's dialogs so the user knows which server they sign in to. */
  host: string;
  /** Already carries the auth "oauth" marker. */
  oauth: boolean;
}

export interface AiConfigLoginDeps {
  /**
   * The host's own confirmation, asked for every request: the sign-in opens the
   * system browser at an address the plugin chose when it installed the
   * connector, so a page request is never consent. False ends it as cancelled.
   */
  approve(target: AiConfigLoginTarget): Promise<boolean>;
  /** Runs the host sign-in; reports a failure's error through onError (signInMcpServer's contract). */
  signIn(target: AiConfigLoginTarget, onError: (error: unknown) => void): Promise<string>;
  /** Saves the auth "oauth" marker on the connector, so connecting sends the host token. */
  markOauth(target: AiConfigLoginTarget): Promise<unknown>;
}

const FAILURE_REASONS: Record<string, AiConfigLoginFailure> = {
  MCP_AUTH_UNSUPPORTED: "unsupported",
  MCP_AUTH_REGISTER_REFUSED: "unsupported",
  MCP_AUTH_INSECURE_URL: "unsupported",
  MCP_AUTH_NETWORK: "network",
  MCP_AUTH_TIMEOUT: "timeout",
  MCP_AUTH_DENIED: "denied",
};

/** The short reason for a failed sign-in; only the "[MCP_AUTH_*]" code is read. */
export function aiConfigLoginFailureReason(error: unknown): AiConfigLoginFailure {
  const code = error === undefined ? null : mcpAuthErrorCode(error);
  return (code && FAILURE_REASONS[code]) || "failed";
}

/**
 * The remote connector this plugin installed for sourceId, matched like
 * applySync does (pluginId, kind "mcp" and sourceId of its syncSource), so a
 * connector another plugin installed under the same sourceId never matches.
 * null when there is none; "local" when it is not a remote connector.
 */
export function findAiConfigLoginTarget(
  servers: readonly Record<string, unknown>[],
  pluginId: string,
  sourceId: string,
): AiConfigLoginTarget | "local" | null {
  const server = servers[findSynced(servers, pluginId, { kind: "mcp", sourceId })];
  if (!server) return null;
  const id = typeof server.id === "string" ? server.id.trim() : "";
  const url = typeof server.url === "string" ? server.url.trim() : "";
  if (!id || server.transport !== "http" || !url) return "local";
  let host = "";
  try {
    host = new URL(url).host;
  } catch {
    // Not an address the host can show (or sign in to).
  }
  return host ? { id, url, host, oauth: server.auth === "oauth" } : "local";
}

/**
 * Signs in to the plugin's connector for sourceId. The result holds only
 * `status` and, on failure, a stable `reason`. A connector without the oauth
 * marker gets it after signing in, as in Settings; if saving the marker fails
 * the sign-in still stands (a later connect asks to sign in again and Settings
 * marks it then).
 */
export async function runAiConfigLogin(
  servers: readonly Record<string, unknown>[],
  pluginId: string,
  sourceId: string,
  deps: AiConfigLoginDeps,
): Promise<AiConfigLoginResult> {
  const target = findAiConfigLoginTarget(servers, pluginId, sourceId);
  if (!target) return { status: "failed", reason: "not-installed" };
  if (target === "local") return { status: "failed", reason: "unsupported" };
  if (!(await deps.approve(target))) return { status: "cancelled" };
  let failure: unknown;
  const outcome = await deps.signIn(target, (error) => {
    failure = error;
  });
  if (outcome === "cancelled") return { status: "cancelled" };
  if (outcome !== "signed-in") return { status: "failed", reason: aiConfigLoginFailureReason(failure) };
  if (!target.oauth) {
    try {
      await deps.markOauth(target);
    } catch {
      // Best effort, see above.
    }
  }
  return { status: "signed-in" };
}

// ai.config auth-status: a read-only answer to "is the connector I installed
// for this sourceId signed in?". Only one of four words per sourceId goes back;
// no token, address, settings or error text.
export type AiConfigAuthStatus = "signed-in" | "signed-out" | "needs-login" | "not-installed";

export interface AiConfigAuthStatusResult {
  statuses: Record<string, AiConfigAuthStatus>;
}

/** The host's status lookup (mcpAuthStatus); its answer is checked, never passed on. */
export type AiConfigAuthStatusLookup = (target: { id: string; url: string }) => Promise<unknown>;

/** Lookups in flight at once (each is a local token-store read). */
const AUTH_STATUS_CONCURRENCY = 8;

async function connectorAuthStatus(
  servers: readonly Record<string, unknown>[],
  pluginId: string,
  sourceId: string,
  lookup: AiConfigAuthStatusLookup,
): Promise<AiConfigAuthStatus> {
  const target = findAiConfigLoginTarget(servers, pluginId, sourceId);
  if (!target) return "not-installed";
  // A local connector has no sign-in.
  if (target === "local") return "signed-out";
  let answer: unknown;
  try {
    answer = await lookup({ id: target.id, url: target.url });
  } catch {
    return "signed-out";
  }
  if (answer === "signed-in") return "signed-in";
  // A connector without the oauth marker does not use the host sign-in yet: only a completed one counts.
  return answer === "needs-login" && target.oauth ? "needs-login" : "signed-out";
}

/**
 * The sign-in status of this plugin's connector for each sourceId, matched like
 * findAiConfigLoginTarget (another plugin's or a hand-made connector is
 * "not-installed"). A failed or unexpected lookup reads as "signed-out".
 */
export async function aiConfigAuthStatuses(
  servers: readonly Record<string, unknown>[],
  pluginId: string,
  sourceIds: readonly string[],
  lookup: AiConfigAuthStatusLookup,
): Promise<AiConfigAuthStatusResult> {
  const answers: AiConfigAuthStatus[] = new Array(sourceIds.length);
  let next = 0;
  const worker = async () => {
    for (let index = next++; index < sourceIds.length; index = next++) {
      answers[index] = await connectorAuthStatus(servers, pluginId, sourceIds[index], lookup);
    }
  };
  await Promise.all(Array.from({ length: Math.min(AUTH_STATUS_CONCURRENCY, sourceIds.length) }, worker));
  // fromEntries defines own properties, so a "__proto__" sourceId stays a plain key.
  return { statuses: Object.fromEntries(sourceIds.map((sourceId, index) => [sourceId, answers[index]])) };
}
