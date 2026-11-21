import type { WorkspaceTab, WorkspaceView } from "@/types/workspace";

// Route views that KeepAlive may cache, by component name (order = include order).
const CACHEABLE_VIEWS: ReadonlyArray<readonly [WorkspaceView, string]> = [
  ["terminal", "TerminalWorkspaceView"],
  ["sessions", "SessionsWorkspaceView"],
  ["ssh", "SshWorkspaceView"],
  ["sftp", "SftpWorkspaceView"],
  ["database", "DatabaseWorkspaceView"],
  ["http", "HttpWorkspaceView"],
  ["notes", "NotesWorkspaceView"],
];

/**
 * Component names KeepAlive keeps: the dashboard (it has no tab) plus every view
 * that still has an open tab. Closing a view's last tab drops its cached instance,
 * so its terminals, editors and DOM are released instead of lingering hidden.
 */
export function cachedWorkspaceViewNames(tabs: readonly Pick<WorkspaceTab, "view">[]): string[] {
  const open = new Set(tabs.map((tab) => tab.view));
  return ["DashboardView", ...CACHEABLE_VIEWS.filter(([view]) => open.has(view)).map(([, name]) => name)];
}
