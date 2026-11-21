import type { WorkspaceTab } from "@/types/workspace";

export interface TerminalLayoutTarget {
  layoutId: string;
  fallbackLayoutId?: string;
}

/**
 * Terminal tabs remain independent while workspace isolation is enabled.
 * In shared mode tabs at the same ordinal use a stable layout slot owned by
 * the shared anchor workspace, while the anchor tab supplies the initial
 * snapshot the first time that shared slot is opened.
 */
export function resolveTerminalLayoutTarget(
  tabs: readonly WorkspaceTab[],
  tab: WorkspaceTab,
  isolationEnabled: boolean,
  workspaceScopeId: string
): TerminalLayoutTarget {
  if (isolationEnabled || tab.view !== "terminal") return { layoutId: tab.id };

  const workspaceTerminalTabs = tabs.filter(
    (candidate) => candidate.workspaceId === tab.workspaceId && candidate.view === "terminal"
  );
  const slot = Math.max(0, workspaceTerminalTabs.findIndex((candidate) => candidate.id === tab.id));
  const anchorTab = tabs.filter(
    (candidate) => candidate.workspaceId === workspaceScopeId && candidate.view === "terminal"
  )[slot];

  return {
    layoutId: `workspace-scope:${encodeURIComponent(workspaceScopeId)}:terminal:${slot}`,
    fallbackLayoutId: anchorTab?.id ?? tab.id,
  };
}
