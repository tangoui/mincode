import type { SshAgentIdentity, SshAgentStatus, SshAgentStatusState } from "@/domain/ssh/types";
import { invokeBackend } from "@/services/desktop-bridge";

const AGENT_STATES = new Set<SshAgentStatusState>(["ready", "empty", "unavailable"]);
const MAX_IDENTITIES = 8;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function parseIdentity(value: unknown): SshAgentIdentity | undefined {
  if (!isRecord(value)) return undefined;
  const algorithm = typeof value.algorithm === "string" ? value.algorithm.trim() : "";
  const fingerprint = typeof value.fingerprint === "string" ? value.fingerprint.trim() : "";
  if (!algorithm || !fingerprint || algorithm.length > 128 || fingerprint.length > 256) return undefined;
  return { algorithm, fingerprint };
}

function parseSshAgentStatus(value: unknown): SshAgentStatus {
  if (!isRecord(value)) throw new Error("Invalid SSH Agent status response");
  const state = value.state as SshAgentStatusState;
  if (!AGENT_STATES.has(state)) throw new Error("Invalid SSH Agent status state");
  const identityCount =
    typeof value.identityCount === "number"
    && Number.isSafeInteger(value.identityCount)
    && value.identityCount >= 0
      ? value.identityCount
      : 0;
  const identities = Array.isArray(value.identities)
    ? value.identities.slice(0, MAX_IDENTITIES).flatMap((identity) => {
      const parsed = parseIdentity(identity);
      return parsed ? [parsed] : [];
    })
    : [];
  return { state, identityCount, identities };
}

/** Reads the SSH Agent visible to the current desktop process. */
export async function getSshAgentStatus(): Promise<SshAgentStatus> {
  const result = await invokeBackend<unknown>("get_ssh_agent_status");
  return parseSshAgentStatus(result);
}
