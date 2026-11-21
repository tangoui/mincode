import type { RouteLocationRaw } from "vue-router";
import type { WorkspaceTab } from "@/types/workspace";

export function workspaceTabRoute(tab: WorkspaceTab | undefined): RouteLocationRaw {
  if (!tab) return { name: "dashboard" };
  if (tab.view === "plugin-view" && tab.pluginView) {
    return {
      name: "plugin-view",
      params: { pluginId: tab.pluginView.pluginId, viewId: tab.pluginView.viewId },
    };
  }
  if (tab.view === "service-result" && tab.serviceResult) {
    return { name: "service-result", params: { resultId: tab.serviceResult.resultId } };
  }
  return { name: tab.view };
}
