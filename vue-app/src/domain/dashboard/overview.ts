import type { WorkspaceTab } from "@/types/workspace";

export const dashboardQuickActions = [
  { view: "terminal", titleKey: "dashboard.quick.terminal", descriptionKey: "dashboard.quick.terminalHint" },
  { view: "ssh", titleKey: "dashboard.quick.ssh", descriptionKey: "dashboard.quick.sshHint" },
  { view: "database", titleKey: "dashboard.quick.database", descriptionKey: "dashboard.quick.databaseHint" },
] as const;

export function orderDashboardTabs(tabs: readonly WorkspaceTab[]) {
  return [...tabs].sort((left, right) => Number(Boolean(right.pinned)) - Number(Boolean(left.pinned)));
}

export function dashboardBasename(path: string) {
  return (
    path
      .replace(/[\\/]+$/, "")
      .split(/[\\/]/)
      .filter(Boolean)
      .pop() ?? ""
  );
}
