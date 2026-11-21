import type { RouteLocationRaw } from "vue-router";
import type { WorkspaceTab } from "@/types/workspace";

export function workspaceTabRoute(tab: WorkspaceTab | undefined): RouteLocationRaw {
  return { name: tab?.view ?? "dashboard" };
}
