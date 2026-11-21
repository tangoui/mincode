import type { AiCapabilityRisk } from "./capability-registry";
import type { ToolAuthorizationRequest, ToolAuthorizationScope, ToolSessionGrant } from "./tool-authorization";

interface NativeAuthorizationInput {
  conversationId: string;
  toolName: string;
  module: string;
  args: Readonly<Record<string, unknown>>;
  risk: AiCapabilityRisk;
  sessionGrant?: ToolSessionGrant;
}

interface McpAuthorizationInput {
  conversationId: string;
  toolName: string;
  serverId: string;
  args: Readonly<Record<string, unknown>>;
}

function explicitResourceId(args: Readonly<Record<string, unknown>>) {
  return typeof args.resourceId === "string" && args.resourceId.trim()
    ? args.resourceId.trim()
    : undefined;
}

export function createNativeAuthorizationRequest(
  input: NativeAuthorizationInput
): ToolAuthorizationRequest {
  return {
    conversationId: input.conversationId,
    toolName: input.toolName,
    resourceId: explicitResourceId(input.args) ?? `${input.module}:global`,
    parameters: input.args,
    risk: input.risk,
    ...(input.sessionGrant ? { sessionGrant: input.sessionGrant } : {}),
  };
}

export function createMcpAuthorizationRequest(
  input: McpAuthorizationInput
): ToolAuthorizationRequest {
  return {
    conversationId: input.conversationId,
    toolName: input.toolName,
    resourceId: input.serverId,
    parameters: input.args,
    risk: "dangerous",
  };
}

export function approvalScopesForRisk(risk: AiCapabilityRisk): ToolAuthorizationScope[] {
  return risk === "dangerous" ? ["once", "session"] : ["once", "session", "global"];
}
