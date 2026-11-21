/**
 * Frontend stage of the `treeViews` contribution point: re-validation of the
 * views the Go host accepted (backend/plugin/contrib_tree_views.go)
 * and of the children returned by `plugin_host_tree_view`
 * (tree_view_invoke.go). Items are plain text with a built-in icon name;
 * children load lazily, one level per request. A target opens a project
 * file, the plugin's own view, one of its read commands or an http(s) URL in
 * the system browser, only when the user activates the item. No DOM, Vue or
 * IPC here; the runtime wiring lives in services/contributions/tree-views.ts,
 * stores/plugin-tree-views.ts, services/plugin-tree-view-service.ts and
 * components/projects/ProjectPluginViewsPanel.vue.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  PROVIDER_ACTION_ID_PATTERN,
  PROVIDER_ENTRY_ID_PATTERN,
  boundedLine,
  boundedMultiline,
  collectProviderEntries,
  isProviderItemId,
  isRecord,
  localizedTitles,
  parseProviderTarget,
  providerPreviewTitles,
  responseCount,
  type ProviderItemTarget,
  type ProviderPointIssue,
} from "@/domain/plugins/provider-points";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const TREE_VIEWS_CONTRIBUTION_POINT = "treeViews";

/** Mirrors contribution-points.json treeViews limits (the host enforces them first). */
export const TREE_VIEW_LIMITS = {
  maxEntries: 4,
  titleMaxRunes: 48,
  localesMax: 16,
  itemsMax: 200,
  itemIdMaxLength: 128,
  labelMaxRunes: 128,
  descriptionMaxRunes: 128,
  tooltipMaxRunes: 512,
  argsMaxBytes: 1024,
  argsMaxProperties: 8,
  urlMaxLength: 2048,
  pathMaxLength: 1024,
  lineMax: 10_000_000,
  depthMax: 16,
  timeoutMs: 5000,
} as const;

/** Mirrors contribution-points.json treeViews lists.icons. */
export const TREE_VIEW_ICONS = [
  "file", "folder", "symbol", "function", "class", "variable", "package", "module", "bug", "test", "check", "error",
  "warning", "info", "link", "gear", "database", "cloud", "server", "tag", "star", "clock", "user", "key", "branch",
] as const;

export type TreeViewIcon = (typeof TREE_VIEW_ICONS)[number];
const ICONS: ReadonlySet<string> = new Set(TREE_VIEW_ICONS);

export interface PluginTreeView {
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
}

export type TreeViewIssue = ProviderPointIssue;
export type TreeViewTarget = ProviderItemTarget;

export interface TreeViewItem {
  id: string;
  label: string;
  description?: string;
  tooltip?: string;
  icon?: TreeViewIcon;
  collapsible: boolean;
  target?: TreeViewTarget;
}

export interface TreeViewResponse {
  items: TreeViewItem[];
  dropped: number;
  truncated: boolean;
}

/** Re-validates one host-accepted entry (defense in depth). */
export function validateTreeViewEntry(
  raw: unknown,
): { ok: true; value: Omit<PluginTreeView, "pluginId" | "pluginName"> } | { ok: false; error: string } {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!PROVIDER_ENTRY_ID_PATTERN.test(id)) return { ok: false, error: `tree view id "${id}" is invalid` };
  const title = boundedLine(raw.title, TREE_VIEW_LIMITS.titleMaxRunes, true);
  if (!title) return { ok: false, error: `tree view "${id}" title is invalid` };
  const titleLocalized = localizedTitles(raw.titleLocalized, TREE_VIEW_LIMITS.titleMaxRunes, TREE_VIEW_LIMITS.localesMax);
  if (titleLocalized === false) return { ok: false, error: `tree view "${id}" titleLocalized is invalid` };
  if (typeof raw.action !== "string" || !PROVIDER_ACTION_ID_PATTERN.test(raw.action)) return { ok: false, error: `tree view "${id}" action is invalid` };
  return { ok: true, value: { id, title, ...(titleLocalized ? { titleLocalized } : {}) } };
}

/** The views of the active process plugins, in ascending plugin id order. */
export function collectTreeViews(contributions: readonly PointContribution[]): { views: PluginTreeView[]; issues: TreeViewIssue[] } {
  const { entries, issues } = collectProviderEntries(contributions, TREE_VIEW_LIMITS.maxEntries, validateTreeViewEntry, "tree view");
  return { views: entries, issues };
}

/** The view's title for a locale chain (plugin translations only). */
export function treeViewTitle(view: PluginTreeView, locale: string | readonly string[]): string {
  return resolvePluginTitle(view, locale);
}

/** A stable key for a view across snapshots. */
export function treeViewKey(view: Pick<PluginTreeView, "pluginId" | "id">): string {
  return `${view.pluginId}/${view.id}`;
}

/**
 * Re-validates a `plugin_host_tree_view` response; invalid or duplicate
 * items are dropped and counted. `depth` is the requested level (0 = root):
 * items at the deepest level are never expandable. File targets are dropped
 * without a project.
 */
export function parseTreeViewResponse(value: unknown, depth: number, hasProject: boolean): TreeViewResponse {
  const response: TreeViewResponse = { items: [], dropped: 0, truncated: false };
  if (!isRecord(value) || !Array.isArray(value.items)) return response;
  response.dropped = responseCount(value.dropped);
  response.truncated = value.truncated === true || value.items.length > TREE_VIEW_LIMITS.itemsMax;
  const leafOnly = depth + 1 >= TREE_VIEW_LIMITS.depthMax;
  const seen = new Set<string>();
  for (const raw of value.items.slice(0, TREE_VIEW_LIMITS.itemsMax)) {
    const item = isRecord(raw) ? raw : {};
    const id = isProviderItemId(item.id, TREE_VIEW_LIMITS.itemIdMaxLength) ? item.id : "";
    const label = boundedLine(item.label, TREE_VIEW_LIMITS.labelMaxRunes, true);
    const description = boundedLine(item.description, TREE_VIEW_LIMITS.descriptionMaxRunes, false);
    const tooltip = boundedMultiline(item.tooltip, TREE_VIEW_LIMITS.tooltipMaxRunes);
    const icon = item.icon === undefined ? undefined : typeof item.icon === "string" && ICONS.has(item.icon) ? (item.icon as TreeViewIcon) : false;
    const collapsible = item.collapsible === undefined ? false : item.collapsible;
    const target = item.target === undefined || item.target === null ? undefined : parseProviderTarget(item.target, TREE_VIEW_LIMITS);
    const badTarget = item.target !== undefined && item.target !== null && (!target || (target.type === "file" && !hasProject));
    if (!id || !label || description === false || tooltip === false || icon === false || typeof collapsible !== "boolean" || badTarget || seen.has(id)) {
      response.dropped += 1;
      continue;
    }
    seen.add(id);
    response.items.push({
      id,
      label,
      ...(description ? { description } : {}),
      ...(tooltip && tooltip.trim() ? { tooltip } : {}),
      ...(icon ? { icon } : {}),
      collapsible: collapsible && !leafOnly,
      ...(target ? { target } : {}),
    });
  }
  return response;
}

/** Install-preview summary: the titles of the views a manifest would add. */
export function treeViewPreviewSummary(contributes: unknown): string[] {
  return providerPreviewTitles(contributes, TREE_VIEWS_CONTRIBUTION_POINT, TREE_VIEW_LIMITS.maxEntries, TREE_VIEW_LIMITS.titleMaxRunes);
}
