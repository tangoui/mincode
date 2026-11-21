import type { BackendCommand } from "@/generated/backend-commands";

type McpInvoke = <T>(command: BackendCommand, args: Record<string, unknown>) => Promise<T>;

export interface McpToolCallRequest {
  serverId: string;
  toolName: string;
  arguments: unknown;
}

/**
 * Calls an MCP tool; aborting `signal` cancels the call in the backend, which
 * also sends notifications/cancelled to the MCP server.
 */
export async function callMcpToolCancellable<T>(
  invoke: McpInvoke,
  request: McpToolCallRequest,
  signal: AbortSignal,
): Promise<T> {
  const callId = `mcp-call-${crypto.randomUUID()}`;
  const cancelCall = () => {
    void invoke("mcp_cancel_tool", { callId }).catch(() => undefined);
  };
  signal.addEventListener("abort", cancelCall, { once: true });
  try {
    return await invoke<T>("mcp_call_tool", { ...request, callId });
  } finally {
    signal.removeEventListener("abort", cancelCall);
  }
}
