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

function boundedString(value: unknown, maxLength: number) {
  if (typeof value !== "string") return "";
  const trimmed = value.trim();
  return trimmed.length > 0 && trimmed.length <= maxLength ? trimmed : "";
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
  const restored: WorkspaceTab[] = [];
  for (const item of value) {
    if (!isRecord(item)) continue;
    const { id, workspaceId, view } = item;
    if (
      typeof id !== "string" ||
      !id ||
      tabIds.has(id) ||
      typeof workspaceId !== "string" ||
      !workspaceIds.has(workspaceId) ||
      !isWorkspaceView(view) ||
      view === "dashboard"
    ) {
      continue;
    }
    tabIds.add(id);
    restored.push({
      id,
      workspaceId,
      view,
      titleKey: viewTitleKeys[view],
      title:
        typeof item.title === "string" &&
        item.title.trim()
          ? item.title.trim().slice(0, 256)
          : undefined,
      closable: true,
      pinned: item.pinned === true,
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
    const savedTabId = typeof candidate.lastActiveTabId === "string" ? candidate.lastActiveTabId : "";
    const lastActiveTabId = archivedTabs.some(
      (tab) => tab.id === savedTabId,
    )
      ? savedTabId
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
  const persistedActiveTabId = typeof value.activeTabId === "string" ? value.activeTabId : "";
  const activeTabId = scopedTabs.some((tab) => tab.id === persistedActiveTabId)
    ? persistedActiveTabId
    : "";
  const lastActiveTabByWorkspace: Record<string, string> = {};
  if (isRecord(value.lastActiveTabByWorkspace)) {
    for (const [workspaceId, tabId] of Object.entries(
      value.lastActiveTabByWorkspace,
    )) {
      if (
        typeof tabId === "string" &&
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
