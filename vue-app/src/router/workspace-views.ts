import type { Component } from "vue";
import type { WorkspaceView } from "@/types/workspace";
import { measureDevAsync } from "@/services/dev-performance";

export type RoutedWorkspaceView = Exclude<WorkspaceView, "settings">;
type WorkspaceViewModule = { default: Component };
type WorkspaceViewLoader = () => Promise<WorkspaceViewModule>;

function measuredWorkspaceLoader(
  view: RoutedWorkspaceView,
  loader: WorkspaceViewLoader,
) {
  return () => measureDevAsync(`route:${view}`, loader);
}

const rawWorkspaceViewLoaders = {
  dashboard: measuredWorkspaceLoader(
    "dashboard",
    () => import("@/views/DashboardView.vue"),
  ),
  terminal: measuredWorkspaceLoader(
    "terminal",
    () => import("@/views/TerminalWorkspaceView.vue"),
  ),
  ssh: measuredWorkspaceLoader(
    "ssh",
    () => import("@/views/SshWorkspaceView.vue"),
  ),
  database: measuredWorkspaceLoader(
    "database",
    () => import("@/views/DatabaseWorkspaceView.vue"),
  ),
  sftp: measuredWorkspaceLoader(
    "sftp",
    () => import("@/views/SftpWorkspaceView.vue"),
  ),
  http: measuredWorkspaceLoader(
    "http",
    () => import("@/views/HttpWorkspaceView.vue"),
  ),
  notes: measuredWorkspaceLoader(
    "notes",
    () => import("@/views/NotesWorkspaceView.vue"),
  ),
  sessions: measuredWorkspaceLoader(
    "sessions",
    () => import("@/views/SessionsWorkspaceView.vue"),
  ),
  plugins: measuredWorkspaceLoader(
    "plugins",
    () => import("@/views/PluginManagerWorkspaceView.vue"),
  ),
  "plugin-view": measuredWorkspaceLoader(
    "plugin-view",
    () => import("@/views/PluginWorkspaceView.vue"),
  ),
  "service-result": measuredWorkspaceLoader(
    "service-result",
    () => import("@/views/PluginServiceResultWorkspaceView.vue"),
  ),
  ai: measuredWorkspaceLoader(
    "ai",
    () => import("@/views/AiAssistantWorkspaceView.vue"),
  ),
} satisfies Record<RoutedWorkspaceView, WorkspaceViewLoader>;

export function createWorkspacePreloader<View extends string, Module>(
  loaders: Readonly<Record<View, () => Promise<Module>>>,
) {
  const loads = new Map<View, Promise<Module>>();

  return (view: View) => {
    const existing = loads.get(view);
    if (existing) return existing;

    const pending = loaders[view]().catch((error: unknown) => {
      loads.delete(view);
      throw error;
    });
    loads.set(view, pending);
    return pending;
  };
}

const loadWorkspaceView = createWorkspacePreloader(rawWorkspaceViewLoaders);

// Route navigation and pointer/focus preloading must share the same pending
// promise. Otherwise an immediate click after preloading can invoke the async
// route loader a second time and repeat its measurement/error handling work.
export const workspaceViewLoaders = Object.fromEntries(
  (Object.keys(rawWorkspaceViewLoaders) as RoutedWorkspaceView[]).map(
    (view) => [view, () => loadWorkspaceView(view)],
  ),
) as Record<RoutedWorkspaceView, WorkspaceViewLoader>;

export function preloadWorkspaceView(view: WorkspaceView): Promise<void> {
  if (view === "settings") return Promise.resolve();
  return loadWorkspaceView(view).then(
    () => undefined,
    () => undefined,
  );
}
