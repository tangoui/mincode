import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

interface DesktopTarget {
  kind: string;
  value?: string;
}

export async function drainPendingDesktopNavigation(onTarget: (target: DesktopTarget) => void) {
  if (!isDesktopRuntime()) return;
  const targets = await invokeBackend<unknown>("desktop_take_pending_navigation", {}).catch(() => undefined);
  if (!Array.isArray(targets)) return;
  for (const target of targets) {
    const entry = target as { kind?: unknown; value?: unknown } | null;
    if (!entry || typeof entry.kind !== "string") continue;
    onTarget({ kind: entry.kind, value: typeof entry.value === "string" ? entry.value : undefined });
  }
}
