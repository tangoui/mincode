import type { AiCapabilityRisk } from "./capability-registry";

export type ToolAuthorizationScope = "once" | "session" | "global";

export interface ToolAuthorizationRequest {
  toolName: string;
  resourceId: string;
  conversationId: string;
  parameters: Readonly<Record<string, unknown>>;
  risk: AiCapabilityRisk;
  /**
   * What a "this session" grant covers. Defaults to the exact parameters for dangerous
   * calls and to the whole tool/resource pair otherwise; see sessionGrantBindsParameters.
   */
  sessionGrant?: ToolSessionGrant;
  /**
   * The model's tool call this request authorizes, so the approval can be shown under that step.
   * Display only: grants never match on it.
   */
  toolCallId?: string;
}

/** "parameters": only the approved call again; "tool": any call of this tool on this resource. */
export type ToolSessionGrant = "parameters" | "tool";

interface ToolAuthorizationBase {
  scope: ToolAuthorizationScope;
  toolName: string;
  resourceId: string;
  parameterFingerprint: string;
  risk: AiCapabilityRisk;
}

export interface OnceToolAuthorization extends ToolAuthorizationBase {
  scope: "once";
}

export interface SessionToolAuthorization extends ToolAuthorizationBase {
  scope: "session";
  conversationId: string;
  bindsParameters: boolean;
}

export interface GlobalToolAuthorization extends ToolAuthorizationBase {
  scope: "global";
}

export type ToolAuthorization =
  | OnceToolAuthorization
  | SessionToolAuthorization
  | GlobalToolAuthorization;

export interface ToolAuthorizationStore {
  add(authorization: ToolAuthorization): void;
  replaceGlobals(authorizations: readonly GlobalToolAuthorization[]): void;
  matches(request: ToolAuthorizationRequest): Promise<boolean>;
  consume(request: ToolAuthorizationRequest): Promise<boolean>;
  list(): readonly ToolAuthorization[];
}

function canonicalize(value: unknown, ancestors: Set<object>): string {
  if (value === null) return "null";
  if (typeof value === "string") return JSON.stringify(value);
  if (typeof value === "boolean") return value ? "true" : "false";
  if (typeof value === "number") {
    if (Number.isNaN(value)) return '"$number:nan"';
    if (value === Infinity) return '"$number:infinity"';
    if (value === -Infinity) return '"$number:-infinity"';
    if (Object.is(value, -0)) return '"$number:-0"';
    return String(value);
  }
  if (typeof value === "undefined") return '"$undefined"';
  if (typeof value === "bigint") return JSON.stringify(`$bigint:${value}`);
  if (typeof value !== "object") return JSON.stringify(`$${typeof value}:${String(value)}`);
  if (ancestors.has(value)) throw new Error("Tool authorization parameters must not be cyclic");

  ancestors.add(value);
  let result: string;
  if (Array.isArray(value)) {
    result = `[${value.map((item) => canonicalize(item, ancestors)).join(",")}]`;
  } else {
    const record = value as Record<string, unknown>;
    result = `{${Object.keys(record).sort().map((key) =>
      `${JSON.stringify(key)}:${canonicalize(record[key], ancestors)}`
    ).join(",")}}`;
  }
  ancestors.delete(value);
  return result;
}

export async function fingerprintToolParameters(
  parameters: Readonly<Record<string, unknown>>
): Promise<string> {
  const canonicalized = canonicalize(parameters, new Set());
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(canonicalized));
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

/**
 * A dangerous call (shell commands, SQL, MCP tools, deletions) approved "for this session"
 * must not silently authorize different parameters the user never saw. Capabilities whose
 * parameters legitimately change on every call (Computer Use batches) opt out with "tool".
 */
export function sessionGrantBindsParameters(request: ToolAuthorizationRequest) {
  return (request.sessionGrant ?? (request.risk === "dangerous" ? "parameters" : "tool")) === "parameters";
}

export async function createToolAuthorization(
  scope: ToolAuthorizationScope,
  request: ToolAuthorizationRequest
): Promise<ToolAuthorization> {
  if (scope === "global" && request.risk === "dangerous") {
    throw new Error("dangerous tools cannot receive global authorization");
  }

  const base = {
    toolName: request.toolName,
    resourceId: request.resourceId,
    parameterFingerprint: await fingerprintToolParameters(request.parameters),
    risk: request.risk,
  };
  if (scope === "session") {
    return { ...base, scope, conversationId: request.conversationId, bindsParameters: sessionGrantBindsParameters(request) };
  }
  return { ...base, scope };
}

function matchesAuthorization(
  authorization: ToolAuthorization,
  request: ToolAuthorizationRequest,
  fingerprint: string
) {
  if (authorization.toolName !== request.toolName) return false;
  if (authorization.resourceId !== request.resourceId) return false;
  if (authorization.risk !== request.risk) return false;
  if (authorization.scope === "session") {
    if (authorization.conversationId !== request.conversationId) return false;
    return !authorization.bindsParameters || authorization.parameterFingerprint === fingerprint;
  }
  if (authorization.parameterFingerprint !== fingerprint) return false;
  return authorization.scope !== "global" || request.risk !== "dangerous";
}

export function createToolAuthorizationStore(): ToolAuthorizationStore {
  const authorizations: ToolAuthorization[] = [];

  async function findIndex(request: ToolAuthorizationRequest) {
    const fingerprint = await fingerprintToolParameters(request.parameters);
    return authorizations.findIndex((authorization) =>
      matchesAuthorization(authorization, request, fingerprint)
    );
  }

  return {
    add(authorization) {
      authorizations.push(authorization);
    },
    replaceGlobals(globals) {
      const retained = authorizations.filter((authorization) => authorization.scope !== "global");
      authorizations.splice(0, authorizations.length, ...retained, ...globals);
    },
    async matches(request) {
      return await findIndex(request) >= 0;
    },
    async consume(request) {
      const fingerprint = await fingerprintToolParameters(request.parameters);
      const index = authorizations.findIndex((authorization) =>
        matchesAuthorization(authorization, request, fingerprint)
      );
      if (index < 0) return false;
      if (authorizations[index]?.scope === "once") authorizations.splice(index, 1);
      return true;
    },
    list() {
      return authorizations.slice();
    },
  };
}
