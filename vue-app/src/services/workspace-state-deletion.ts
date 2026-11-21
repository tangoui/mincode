import { deleteBrowserAiScopeHistory } from "@/services/ai-conversation-persistence";
import { applyPersistedBatch, loadPersistedValue } from "@/services/persistent-store";
import {
  markWorkspaceScopePermanentlyDeleted,
  unmarkWorkspaceScopePermanentlyDeleted,
} from "@/services/workspace-deleted-scopes";

const SSH_SESSIONS_STORAGE_KEY = "app_ssh_workspace_sessions_v1";
const LOCAL_PROJECT_WORKSPACES_STORAGE_KEY = "app_local_project_workspaces_v1";

export const WORKSPACE_SCOPED_RUNTIME_STORAGE_KEYS = [
  "app_browser_state_v1",
  "app_http_state_v1",
  "app_db_query_tabs_v1",
  "app_sessions_list_scope_v1",
  "app_sessions_live_terminals_v1",
  // contributes.dashboardWidgets: hidden/ordered plugin cards per workspace.
  "app_dashboard_plugin_widgets_v1",
] as const;

function workspaceScopedStorageKey(baseKey: string, scopeId: string) {
  return `${baseKey}__workspace__${encodeURIComponent(scopeId)}`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export async function deleteWorkspaceScopePersistence(scopeId: string) {
  const normalizedScopeId = scopeId.trim();
  if (!normalizedScopeId) return;

  // 先阻断缓存视图的延迟写入；批量删除失败时会撤销标记，恢复原有写入能力。
  markWorkspaceScopePermanentlyDeleted(normalizedScopeId);
  try {
    const [storedSshSessions, storedProjectWorkspaces] = await Promise.all([
      loadPersistedValue<unknown>(SSH_SESSIONS_STORAGE_KEY),
      loadPersistedValue<unknown>(LOCAL_PROJECT_WORKSPACES_STORAGE_KEY),
    ]);
    const retainedSshSessions = Array.isArray(storedSshSessions)
      ? storedSshSessions.filter(
          (session) => !isRecord(session) || session.workspaceScopeId !== normalizedScopeId
        )
      : undefined;
    const retainedProjectWorkspaces =
      isRecord(storedProjectWorkspaces) && isRecord(storedProjectWorkspaces.workspaces)
        ? {
            ...storedProjectWorkspaces,
            workspaces: Object.fromEntries(
              Object.entries(storedProjectWorkspaces.workspaces).filter(
                ([workspaceId]) => workspaceId !== normalizedScopeId
              )
            ),
          }
        : undefined;

    const sets = [];
    if (retainedSshSessions !== undefined) {
      sets.push({ key: SSH_SESSIONS_STORAGE_KEY, value: retainedSshSessions });
    }
    if (retainedProjectWorkspaces !== undefined) {
      sets.push({ key: LOCAL_PROJECT_WORKSPACES_STORAGE_KEY, value: retainedProjectWorkspaces });
    }

    await applyPersistedBatch({
      sets,
      deleteKeys: WORKSPACE_SCOPED_RUNTIME_STORAGE_KEYS.map((baseKey) =>
        workspaceScopedStorageKey(baseKey, normalizedScopeId)
      ),
      deleteAiScopeIds: [normalizedScopeId],
    });
    // 浏览器本地模式的 AI 历史在 IndexedDB 中，上面的原生批处理不会删到它（桌面端为空操作）。
    await deleteBrowserAiScopeHistory(normalizedScopeId);
  } catch (error) {
    unmarkWorkspaceScopePermanentlyDeleted(normalizedScopeId);
    throw error;
  }
}
