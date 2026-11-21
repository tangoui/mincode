import type { WorkspaceView } from "@/types/workspace";

/**
 * These views either expose global resources or manage their workspace runtime
 * state explicitly inside a long-lived component instance. Every other route,
 * including future routes that are not listed here yet, receives a scoped
 * component instance by default.
 */
const NON_SCOPED_ROUTE_INSTANCES = new Set<WorkspaceView>([
  "dashboard",
  "terminal",
  "ssh",
  "sessions",
  "plugins",
  "settings",
  "ai",
  "notes",
]);

export function workspaceRouteUsesScopedInstance(routeName: unknown): boolean {
  return typeof routeName === "string" && !NON_SCOPED_ROUTE_INSTANCES.has(routeName as WorkspaceView);
}

export function resolveWorkspaceRouteInstanceScope(
  routeName: unknown,
  workspaceScopeId: string
): string {
  return workspaceRouteUsesScopedInstance(routeName) ? workspaceScopeId : "shared";
}
