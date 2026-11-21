import type { LocalShellUsage } from "@/domain/terminal/resource-usage";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

/** Memory of the local terminal sessions among sessionIds (others, e.g. SSH, are left out). */
export async function fetchLocalShellUsage(sessionIds: string[]): Promise<LocalShellUsage[]> {
  if (!isDesktopRuntime() || sessionIds.length === 0) return [];
  return (await invokeBackend<LocalShellUsage[]>("local_shell_resource_usage", { payload: { sessionIds } })) ?? [];
}

/** Ends a process of a local terminal (with the processes it started); never the terminal's shell. */
export async function endLocalShellProcess(sessionId: string, pid: number): Promise<void> {
  await invokeBackend<void>("local_shell_end_process", { payload: { sessionId, pid } });
}
