import { ACP_BUILTIN_AGENTS, parseAcpAdapterId } from "./acp-agents";

export const DEFAULT_ACP_ADAPTER_ID = ACP_BUILTIN_AGENTS[0]!.id;

/** The stored selection, trimmed; an empty one selects the default built-in agent. */
export function selectedAcpAdapterId(stored: string | undefined): string {
  const saved = stored?.trim() ?? "";
  return saved && !saved.startsWith("plugin:") ? saved : DEFAULT_ACP_ADAPTER_ID;
}

/**
 * True when a session recorded under `recorded` must not be loaded by
 * `adapterId`: the two differ and one of them is a plugin agent.
 */
export function acpSessionBelongsElsewhere(recorded: string | undefined, adapterId: string): boolean {
  const from = typeof recorded === "string" ? recorded.trim() : "";
  if (!from || from === adapterId) return false;
  return from.startsWith("plugin:");
}

export interface AcpAgentStartRequest {
  /** acpAdapterId from Settings. */
  selectedAdapterId: string | undefined;
  /** The adapter the conversation recorded, if any. */
  recordedAdapterId: string | undefined;
  /** The conversation has an ACP session the caller would load. */
  hasPreviousSession: boolean;
}

export type AcpAgentStartOutcome =
  | { kind: "started"; adapterId: string }
  | { kind: "conversation-unavailable"; recordedAdapterId: string; adapterId: string };

export async function startAcpAgent(
  request: AcpAgentStartRequest,
  launch: (adapterId: string) => Promise<void>,
): Promise<AcpAgentStartOutcome> {
  const adapterId = selectedAcpAdapterId(request.selectedAdapterId);
  const recordedAdapterId = request.recordedAdapterId?.trim() ?? "";
  if (request.hasPreviousSession && acpSessionBelongsElsewhere(recordedAdapterId, adapterId)) {
    return { kind: "conversation-unavailable", recordedAdapterId, adapterId };
  }
  await launch(adapterId);
  return { kind: "started", adapterId };
}

/** Returns true the first time it sees a key (per app session when module-scoped). */
export function createOnceGate() {
  const seen = new Set<string>();
  return (key: string) => {
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  };
}

export function acpAdapterLabel(adapterId: string, customAgents?: readonly { id: string; name: string }[]): string {
  if (adapterId.startsWith("plugin:")) return adapterId.slice("plugin:".length);
  const ref = parseAcpAdapterId(adapterId);
  if (ref.kind === "custom") {
    return customAgents?.find((item) => item.id === ref.id)?.name?.trim() || ref.id;
  }
  return ACP_BUILTIN_AGENTS.find((agent) => agent.id === ref.id)?.name ?? ref.id;
}
