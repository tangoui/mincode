/**
 * Loads one level of a plugin tree view through the host
 * (`plugin_host_tree_view`). The host checks the view still exists, builds
 * the fixed payload {view, parentId?, depth, projectRoot?, locale}, runs the
 * plugin's `read`-risk action with a 5 s timeout and bounded payload/result
 * and returns validated plain-text items, which are re-validated here.
 */
import { parseTreeViewResponse, type PluginTreeView, type TreeViewResponse } from "@/domain/plugins/tree-views";
import { invokeProviderCommand, type ProviderInvokeResult } from "@/services/plugin-provider-invoke";

export interface TreeViewChildrenRequest {
  /** Absent for the root level. */
  parentId?: string;
  /** 0 for the root level, else the parent's depth + 1. */
  depth: number;
  projectRoot?: string;
  locale: string;
}

/** Loads the root items (no parentId) or the children of one item. */
export function loadTreeViewItems(
  view: Pick<PluginTreeView, "pluginId" | "id">,
  request: TreeViewChildrenRequest,
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<TreeViewResponse>> {
  const hasProject = Boolean(request.projectRoot);
  return invokeProviderCommand(
    "plugin_host_tree_view",
    {
      pluginId: view.pluginId,
      viewId: view.id,
      depth: request.depth,
      locale: request.locale,
      ...(request.parentId !== undefined ? { parentId: request.parentId } : {}),
      ...(request.projectRoot ? { projectRoot: request.projectRoot } : {}),
    },
    (value) => parseTreeViewResponse(value, request.depth, hasProject),
    signal,
  );
}
