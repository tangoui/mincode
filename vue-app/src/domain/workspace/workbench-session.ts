import {
  workspaceViews,
  type WorkspaceDefinition,
  type WorkspaceTab,
  type WorkspaceView,
} from "../../types/workspace.ts";

export const WORKBENCH_SESSION_VERSION = 4 as const;
export const DEFAULT_WORKSPACE_ID = "workspace:default";
const MAX_PERSISTED_TABS = 200;
const MAX_REF_LENGTH = 512;

export const viewTitleKeys: Record<WorkspaceView, WorkspaceTab["titleKey"]> = {
  dashboard: "nav.dashboard",
  terminal: "nav.terminal",
  ssh: "nav.ssh",
  database: "nav.database",
  sftp: "nav.sftp",
  http: "nav.http",
  notes: "nav.notes",
  sessions: "nav.sessions",
  plugins: "nav.plugins",
  "plugin-view": "plugins.viewTitle",
  "service-result": "plugins.services.result.title",
  settings: "nav.settings",
  ai: "ai.chat",
};

function now() {
  return Date.now();
}

export function defaultWorkspace(): WorkspaceDefinition {
  const timestamp = now();
  return {
    id: DEFAULT_WORKSPACE_ID,
    name: "IM",
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

export interface DeletedWorkspaceRecord {
  workspace: WorkspaceDefinition;
  tabs: WorkspaceTab[];
  lastActiveTabId: string;
  deletedAt: number;
  stateScopeId?: string;
}

export interface PersistedWorkbenchSession {
  version: typeof WORKBENCH_SESSION_VERSION;
  workspaces: WorkspaceDefinition[];
  activeWorkspaceId: string;
  tabs: WorkspaceTab[];
  activeTabId: string;
  lastActiveTabByWorkspace: Record<string, string>;
  deletedWorkspaces: DeletedWorkspaceRecord[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isWorkspaceView(value: unknown): value is WorkspaceView {
  return (
    typeof value === "string" && workspaceViews.includes(value as WorkspaceView)
  );
}

function normalizedPersistedTabId(
  value: unknown,
  workspaceId: string,
  rawTabs: unknown,
) {
  if (typeof value !== "string") return "";
  const legacyWorkflowTab =
    Array.isArray(rawTabs) &&
    rawTabs.some(
      (item) =>
        isRecord(item) &&
        item.id === value &&
        item.workspaceId === workspaceId &&
        item.view === "workflows",
    );
  return legacyWorkflowTab ? `${workspaceId}:plugins` : value;
}

/**
 * Tabs whose content lives only in memory (service results): never written to
 * the session store, dropped from a restored one, and not archived with a
 * deleted workspace, since their content is gone once the tab is.
 */
export function isTransientTab(tab: Pick<WorkspaceTab, "view">) {
  return tab.view === "service-result";
}

export function pluginViewTabId(
  workspaceId: string,
  pluginId: string,
  viewId: string,
) {
  return `${workspaceId}:plugin-view:${pluginId}:${viewId}`;
}

export function serviceResultTabId(workspaceId: string, resultId: string) {
  return `${workspaceId}:service-result:${resultId}`;
}

function boundedString(value: unknown, maxLength: number) {
  if (typeof value !== "string") return "";
  const trimmed = value.trim();
  return trimmed.length > 0 && trimmed.length <= maxLength ? trimmed : "";
}

export function isPluginViewTab(
  tab: WorkspaceTab,
  workspaceId: string,
  pluginId: string,
  viewId: string,
) {
  return (
    tab.workspaceId === workspaceId &&
    tab.view === "plugin-view" &&
    tab.pluginView?.pluginId === pluginId &&
    tab.pluginView.viewId === viewId
  );
}

function pluginViewIdentityKey(
  workspaceId: string,
  pluginId: string,
  viewId: string,
) {
  return JSON.stringify([workspaceId, pluginId, viewId]);
}

export function normalizeWorkspaces(value: unknown): WorkspaceDefinition[] {
  if (!Array.isArray(value)) return [defaultWorkspace()];
  const restored = value
    .filter((item): item is WorkspaceDefinition => {
      if (!item || typeof item !== "object") return false;
      const workspace = item as Record<string, unknown>;
      return (
        typeof workspace.id === "string" &&
        Boolean(workspace.id) &&
        typeof workspace.name === "string" &&
        Boolean(workspace.name.trim())
      );
    })
    .map((item) => {
      return {
        id: item.id,
        name: item.name.trim(),
        createdAt: Number.isFinite(item.createdAt) ? item.createdAt : now(),
        updatedAt: Number.isFinite(item.updatedAt) ? item.updatedAt : now(),
      };
    });
  const withoutDefault = restored.filter(
    (item) => item.id !== DEFAULT_WORKSPACE_ID,
  );
  const savedDefault = restored.find(
    (item) => item.id === DEFAULT_WORKSPACE_ID,
  );
  return [savedDefault ?? defaultWorkspace(), ...withoutDefault];
}

export function normalizeTabs(
  value: unknown,
  workspaces: readonly WorkspaceDefinition[],
): WorkspaceTab[] {
  if (!Array.isArray(value)) return [];
  const workspaceIds = new Set(workspaces.map((workspace) => workspace.id));
  const tabIds = new Set<string>();
  const pluginViewIdentities = new Set<string>();
  const restored: WorkspaceTab[] = [];
  for (const item of value) {
    if (!isRecord(item)) continue;
    const { id: rawId, workspaceId, view: rawView } = item;
    const migratingWorkflow = rawView === "workflows";
    const view = migratingWorkflow ? "plugins" : rawView;
    const id =
      migratingWorkflow && typeof workspaceId === "string"
        ? `${workspaceId}:plugins`
        : rawId;
    if (
      typeof id !== "string" ||
      !id ||
      tabIds.has(id) ||
      typeof workspaceId !== "string" ||
      !workspaceIds.has(workspaceId) ||
      !isWorkspaceView(view) ||
      view === "dashboard" ||
      isTransientTab({ view })
    ) {
      continue;
    }
    let pluginView: WorkspaceTab["pluginView"];
    if (view === "plugin-view") {
      const metadata = isRecord(item.pluginView) ? item.pluginView : undefined;
      const pluginId =
        typeof metadata?.pluginId === "string" ? metadata.pluginId.trim() : "";
      const viewId =
        typeof metadata?.viewId === "string" ? metadata.viewId.trim() : "";
      if (!pluginId || pluginId.length > 128 || !viewId || viewId.length > 128)
        continue;
      const identity = pluginViewIdentityKey(workspaceId, pluginId, viewId);
      if (pluginViewIdentities.has(identity)) continue;
      pluginViewIdentities.add(identity);
      pluginView = { pluginId, viewId };
    }
    tabIds.add(id);
    restored.push({
      id,
      workspaceId,
      view,
      titleKey: viewTitleKeys[view],
      title:
        !migratingWorkflow &&
        typeof item.title === "string" &&
        item.title.trim()
          ? item.title.trim().slice(0, 256)
          : undefined,
      closable: true,
      pinned: item.pinned === true,
      pluginView,
    });
    if (restored.length >= MAX_PERSISTED_TABS) break;
  }
  return restored;
}

export function normalizeDeletedWorkspaces(
  value: unknown,
  activeWorkspaces: readonly WorkspaceDefinition[],
): DeletedWorkspaceRecord[] {
  if (!Array.isArray(value)) return [];
  const activeIds = new Set(activeWorkspaces.map((workspace) => workspace.id));
  const restoredIds = new Set<string>();
  const restored: DeletedWorkspaceRecord[] = [];
  for (const candidate of value) {
    if (!isRecord(candidate)) continue;
    const workspace = normalizeWorkspaces([candidate.workspace]).find(
      (item) => item.id !== DEFAULT_WORKSPACE_ID,
    );
    if (
      !workspace ||
      activeIds.has(workspace.id) ||
      restoredIds.has(workspace.id)
    )
      continue;
    const archivedTabs = normalizeTabs(candidate.tabs, [workspace]);
    const normalizedLastActiveTabId = normalizedPersistedTabId(
      candidate.lastActiveTabId,
      workspace.id,
      candidate.tabs,
    );
    const lastActiveTabId = archivedTabs.some(
      (tab) => tab.id === normalizedLastActiveTabId,
    )
      ? normalizedLastActiveTabId
      : (archivedTabs[0]?.id ?? "");
    restored.push({
      workspace,
      tabs: archivedTabs,
      lastActiveTabId,
      deletedAt: Number.isFinite(candidate.deletedAt)
        ? Number(candidate.deletedAt)
        : now(),
      stateScopeId: boundedString(candidate.stateScopeId, MAX_REF_LENGTH) || undefined,
    });
    restoredIds.add(workspace.id);
  }
  return restored;
}

export function normalizeWorkbenchSession(
  value: unknown,
): PersistedWorkbenchSession | null {
  if (
    !isRecord(value) ||
    (value.version !== 1 &&
      value.version !== 2 &&
      value.version !== 3 &&
      value.version !== WORKBENCH_SESSION_VERSION)
  ) {
    return null;
  }
  const workspaces = normalizeWorkspaces(value.workspaces);
  const workspaceIds = new Set(workspaces.map((workspace) => workspace.id));
  const tabs = normalizeTabs(value.tabs, workspaces);
  const activeWorkspaceId =
    typeof value.activeWorkspaceId === "string" &&
    workspaceIds.has(value.activeWorkspaceId)
      ? value.activeWorkspaceId
      : DEFAULT_WORKSPACE_ID;
  const scopedTabs = tabs.filter(
    (tab) => tab.workspaceId === activeWorkspaceId,
  );
  const persistedActiveTabId = normalizedPersistedTabId(
    value.activeTabId,
    activeWorkspaceId,
    value.tabs,
  );
  const activeTabId = scopedTabs.some((tab) => tab.id === persistedActiveTabId)
    ? persistedActiveTabId
    : "";
  const lastActiveTabByWorkspace: Record<string, string> = {};
  if (isRecord(value.lastActiveTabByWorkspace)) {
    for (const [workspaceId, rawTabId] of Object.entries(
      value.lastActiveTabByWorkspace,
    )) {
      const tabId = normalizedPersistedTabId(rawTabId, workspaceId, value.tabs);
      if (
        workspaceIds.has(workspaceId) &&
        tabs.some((tab) => tab.id === tabId && tab.workspaceId === workspaceId)
      ) {
        lastActiveTabByWorkspace[workspaceId] = tabId;
      }
    }
  }
  if (activeTabId) lastActiveTabByWorkspace[activeWorkspaceId] = activeTabId;
  return {
    version: WORKBENCH_SESSION_VERSION,
    workspaces,
    activeWorkspaceId,
    tabs,
    activeTabId,
    lastActiveTabByWorkspace,
    deletedWorkspaces: normalizeDeletedWorkspaces(
      value.deletedWorkspaces,
      workspaces,
    ),
  };
}

export function prependDeletedWorkspace(
  records: readonly DeletedWorkspaceRecord[],
  record: DeletedWorkspaceRecord,
): DeletedWorkspaceRecord[] {
  return [
    record,
    ...records.filter((item) => item.workspace.id !== record.workspace.id),
  ];
}
