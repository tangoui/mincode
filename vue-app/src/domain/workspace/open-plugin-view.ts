import type { Router } from "vue-router";
import type { WorkspaceTab } from "@/types/workspace";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";

interface PluginViewTabStore {
  openPluginView(pluginId: string, viewId: string, title: string): WorkspaceTab | undefined;
}

/**
 * 打开插件视图标签并导航到对应路由。两步必须一起完成：只激活标签而不推路由时，
 * route.name 与激活标签的 view 不一致，App 顶部路由进度条会一直转圈，视图永远切不过去。
 */
export function openPluginViewTab(
  workbench: PluginViewTabStore,
  router: Pick<Router, "push">,
  pluginId: string,
  viewId: string,
  title: string,
): WorkspaceTab | undefined {
  const tab = workbench.openPluginView(pluginId, viewId, title);
  if (tab) void router.push(workspaceTabRoute(tab));
  return tab;
}
