import type { Component } from "vue";
import type { RouteLocationRaw } from "vue-router";
import {
  Box,
  ChatDotSquare,
  Connection,
  DataAnalysis,
  Files,
  Link,
  Monitor,
  Notebook,
  Odometer,
} from "@element-plus/icons-vue";
import type { MessageKey } from "@/i18n/messages";
import type { WorkspaceView } from "@/types/workspace";

type SidebarWorkspaceView = Exclude<WorkspaceView, "settings" | "plugin-view" | "service-result">;
export type SidebarItemId = SidebarWorkspaceView | "ai";

export interface SidebarItem {
  id: SidebarItemId;
  labelKey: MessageKey;
  icon: Component;
  placement: "main" | "footer";
}

export const sidebarItems = [
  {
    id: "dashboard",
    labelKey: "nav.dashboard",
    icon: Odometer,
    placement: "main",
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
  { id: "plugins", labelKey: "nav.plugins", icon: Box, placement: "main" },
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
  return item.id;
}

export function sidebarItemRoute(item: SidebarMainItem): RouteLocationRaw {
  return { name: item.id };
}

export function sidebarItemIsActive(
  item: SidebarMainItem,
  activeView: WorkspaceView,
  _query: Readonly<Record<string, unknown>>,
) {
  return activeView === item.id;
}
