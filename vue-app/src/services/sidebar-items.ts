import type { Component } from "vue";
import type { LocationQueryRaw, RouteLocationRaw } from "vue-router";
import {
  ChatDotSquare,
  Compass,
  Connection,
  DataAnalysis,
  Files,
  FolderOpened,
  ForkSpoon,
  Link,
  Monitor,
  Notebook,
  Odometer,
} from "@element-plus/icons-vue";
import type { MessageKey } from "@/i18n/messages";
import type { WorkspaceView } from "@/types/workspace";

type SidebarWorkspaceView = Exclude<WorkspaceView, "settings">;
export type SidebarItemId = SidebarWorkspaceView | "source-control" | "ai";

export interface SidebarItem {
  id: SidebarItemId;
  labelKey: MessageKey;
  icon: Component;
  placement: "main" | "footer";
  view?: SidebarWorkspaceView;
  query?: LocationQueryRaw;
}

export const sidebarItems = [
  {
    id: "dashboard",
    labelKey: "nav.dashboard",
    icon: Odometer,
    placement: "main",
  },
  {
    id: "projects",
    labelKey: "nav.projects",
    icon: FolderOpened,
    placement: "main",
  },
  {
    id: "source-control",
    labelKey: "nav.sourceControl",
    icon: ForkSpoon,
    placement: "main",
    view: "projects",
    query: { panel: "git" },
  },
  {
    id: "database",
    labelKey: "nav.database",
    icon: DataAnalysis,
    placement: "main",
  },
  { id: "ssh", labelKey: "nav.ssh", icon: Connection, placement: "main" },
  {
    id: "terminal",
    labelKey: "nav.terminal",
    icon: Monitor,
    placement: "main",
  },
  { id: "sftp", labelKey: "nav.sftp", icon: Files, placement: "main" },
  {
    id: "sessions",
    labelKey: "nav.sessions",
    icon: ChatDotSquare,
    placement: "main",
  },
  { id: "http", labelKey: "nav.http", icon: Link, placement: "main" },
  { id: "notes", labelKey: "nav.notes", icon: Notebook, placement: "main" },
  { id: "browser", labelKey: "nav.browser", icon: Compass, placement: "main" },
  { id: "ai", labelKey: "ai.chat", icon: ChatDotSquare, placement: "footer" },
] as const satisfies readonly SidebarItem[];

export const sidebarMainItems = sidebarItems.filter(
  (
    item,
  ): item is Extract<(typeof sidebarItems)[number], { placement: "main" }> =>
    item.placement === "main",
);

export type SidebarMainItem = (typeof sidebarMainItems)[number];

export function sidebarItemView(item: SidebarMainItem): SidebarWorkspaceView {
  return "view" in item ? item.view : item.id;
}

export function sidebarItemRoute(item: SidebarMainItem): RouteLocationRaw {
  return {
    name: sidebarItemView(item),
    ...("query" in item ? { query: item.query } : {}),
  };
}

function queryMatches(item: SidebarMainItem, query: Readonly<Record<string, unknown>>) {
  return (
    "query" in item &&
    Object.entries(item.query).every(([key, value]) => query[key] === value)
  );
}

export function sidebarItemIsActive(
  item: SidebarMainItem,
  activeView: WorkspaceView,
  query: Readonly<Record<string, unknown>>,
) {
  const view = sidebarItemView(item);
  if (activeView !== view) return false;
  if ("query" in item) return queryMatches(item, query);
  return !sidebarMainItems.some(
    (candidate) =>
      candidate.id !== item.id &&
      sidebarItemView(candidate) === view &&
      queryMatches(candidate, query),
  );
}
