import { invokeBackend, type BackendCommand, type InvokeBackendOptions } from "@/services/desktop-bridge";
import { shallowRef } from "vue";

type AgentControlCommand = Extract<BackendCommand,
  | "agent_control_ready" | "agent_control_result" | "agent_control_pane_result"
  | "agent_control_status" | "agent_control_set_trusted" | "agent_control_log"
  | "terminal_control_info" | "terminal_control_read" | "terminal_control_wait" | "terminal_control_split">;

/** The controls are registered by the Go host and reach the frontend through the desktop bridge. */
export function invokeAgentControl<T>(command: AgentControlCommand, args: Record<string, unknown> = {}, options: InvokeBackendOptions = {}) {
  return invokeBackend<T>(command, args, options);
}

export interface AgentControlStatus {
  trusted: boolean;
  ready: boolean;
}

export const agentControlTrustChange = shallowRef<AgentControlStatus>();

export async function setAgentControlTrusted(enabled: boolean) {
  const status = await invokeAgentControl<AgentControlStatus>("agent_control_set_trusted", { enabled });
  agentControlTrustChange.value = status;
  return status;
}

export interface AgentControlLogEntry {
  timestamp: string;
  source: "pane" | "external";
  tool: string;
  success: boolean;
  durationMs: number;
}
