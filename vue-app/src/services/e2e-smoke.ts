/**
 * Starts the desktop smoke harness when, and only when, the native side says
 * this is an `-tags e2e` build launched by tools/e2e/desktop-smoke.mjs.
 */
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

interface SmokeConfigResponse {
  enabled: boolean;
  config?: unknown;
}

export async function startDesktopSmokeIfRequested() {
  if (!isDesktopRuntime()) return;
  let response: SmokeConfigResponse;
  try {
    response = await invokeBackend<SmokeConfigResponse>("e2e_smoke_config");
  } catch {
    return;
  }
  if (!response?.enabled) return;
  const { runDesktopSmoke } = await import("@/services/e2e-smoke-runner");
  const report = await runDesktopSmoke(response.config);
  await invokeBackend("e2e_smoke_report", { report });
}
