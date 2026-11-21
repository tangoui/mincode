import {
  createRouter,
  createMemoryHistory,
  type RouteRecordRaw,
} from "vue-router";
import { workspaceViewLoaders } from "@/router/workspace-views";

const routes: RouteRecordRaw[] = [
  { path: "/", name: "dashboard", component: workspaceViewLoaders.dashboard },
  {
    path: "/terminal",
    name: "terminal",
    component: workspaceViewLoaders.terminal,
  },
  { path: "/ssh", name: "ssh", component: workspaceViewLoaders.ssh },
  {
    path: "/projects",
    name: "projects",
    component: workspaceViewLoaders.projects,
  },
  {
    path: "/database",
    name: "database",
    component: workspaceViewLoaders.database,
  },
  { path: "/sftp", name: "sftp", component: workspaceViewLoaders.sftp },
  { path: "/http", name: "http", component: workspaceViewLoaders.http },
  { path: "/notes", name: "notes", component: workspaceViewLoaders.notes },
  {
    path: "/workflows",
    redirect: { name: "plugins", query: { tab: "capabilities" } },
  },
  {
    path: "/sessions",
    name: "sessions",
    component: workspaceViewLoaders.sessions,
  },
  {
    path: "/browser",
    name: "browser",
    component: workspaceViewLoaders.browser,
  },
  {
    path: "/plugins",
    name: "plugins",
    component: workspaceViewLoaders.plugins,
  },
  {
    path: "/plugins/:pluginId/:viewId",
    name: "plugin-view",
    component: workspaceViewLoaders["plugin-view"],
  },
  {
    path: "/service-results/:resultId",
    name: "service-result",
    component: workspaceViewLoaders["service-result"],
  },
  { path: "/ai", name: "ai", component: workspaceViewLoaders.ai },
  { path: "/git", redirect: { name: "projects" } },
  { path: "/settings", redirect: { name: "dashboard" } },
  { path: "/:pathMatch(.*)*", redirect: { name: "dashboard" } },
];

// In-memory history: the desktop shell has no address bar, the start view is restored from
// the saved workbench session (App.vue) and popup windows route themselves (PopupTabWindow).
// WebKit rejects more than 100 pushState/replaceState calls per 10 seconds; with browser
// history, rapid tab switching hit that limit and every navigation then stalled ~80 ms in
// vue-router's location.replace/assign fallback.
export default createRouter({ history: createMemoryHistory(), routes });
