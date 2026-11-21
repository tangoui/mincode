import type { SshPortForwardRule, SshPortForwardStatus } from "@/domain/ssh/types";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

function requireDesktopRuntime() {
  if (!isDesktopRuntime()) {
    throw new Error("Port forwarding controls require the desktop runtime.");
  }
}

function isPortForwardStatus(value: unknown): value is SshPortForwardStatus {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Record<string, unknown>;
  const validErrorCodes = new Set(["address_in_use", "invalid_rule", "start_failed"]);
  return (
    typeof candidate.ruleId === "string" &&
    typeof candidate.isRunning === "boolean" &&
    (candidate.errorCode === undefined ||
      (typeof candidate.errorCode === "string" && validErrorCodes.has(candidate.errorCode))) &&
    (candidate.errorMessage === undefined || typeof candidate.errorMessage === "string")
  );
}

/** Retrieves the active forwarding rules for one running SSH session. */
export async function listSshPortForwards(sessionId: string): Promise<SshPortForwardStatus[]> {
  requireDesktopRuntime();
  const result = await invokeBackend<unknown>("list_port_forwards", {
    payload: { sessionId },
  });

  return Array.isArray(result) ? result.filter(isPortForwardStatus) : [];
}

/** Stops one configured forwarding rule without closing the SSH terminal session. */
export async function stopSshPortForward(sessionId: string, ruleId: string): Promise<void> {
  requireDesktopRuntime();
  await invokeBackend<void>("stop_port_forward", {
    payload: { sessionId, ruleId },
  });
}

/** Starts one configured forwarding rule on an already-running SSH terminal session. */
export async function resumeSshPortForward(
  sessionId: string,
  rule: SshPortForwardRule
): Promise<void> {
  requireDesktopRuntime();
  await invokeBackend<void>("resume_port_forward", {
    payload: { sessionId, rule },
  });
}
