import { computed, nextTick, onBeforeUnmount, ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { DEFAULT_WORKSPACE_ID, useWorkbenchStore } from "@/stores/workbench";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useLocalProjectStore } from "@/stores/local-project";
import { peekAiAssistantStore } from "@/stores/ai-assistant-shell";
import { discardDatabaseWorkspaceScope } from "@/stores/database-workspace-shell";
import { defaultWorktreePath } from "@/domain/workspace/workbench-session";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import type { WorkspaceTab } from "@/types/workspace";
import { focusInlineInput } from "@/domain/ui/focus-inline-input";

// Only reached from explicit user actions; kept out of the startup entry chunk.
const loadGitService = () => import("@/services/git-service");
async function deleteWorkspaceScopePersistence(scopeId: string) {
  const { deleteWorkspaceScopePersistence: deleteScope } = await import(
    "@/services/workspace-state-deletion"
  );
  await deleteScope(scopeId);
}

interface WorkspaceManagementOptions {
  navigateToTab: (tab: WorkspaceTab | undefined) => void;
  navigateToActiveTab: () => void;
}

/** Workspace switch/create/rename/delete/restore flows used by the top tab bar. */
export function useWorkspaceManagement({ navigateToTab, navigateToActiveTab }: WorkspaceManagementOptions) {
  const workbench = useWorkbenchStore();
  const terminalWorkspace = useTerminalWorkspaceStore();
  const localProject = useLocalProjectStore();
  const workspaceStatePreferences = useWorkspaceStateScope().preferences;
  const { t } = useVueI18n();
  const workspaceDialogVisible = ref(false);
  const workspaceEditingId = ref("");
  const workspaceName = ref("");
  const workspaceWorktreeEnabled = ref(false);
  const workspaceRepoPath = ref("");
  const workspaceBranch = ref("");
  const workspaceCreating = ref(false);

  let workspaceConversationPreloadTimer: ReturnType<typeof setTimeout> | undefined;
  function preloadWorkspaceConversation(id: string) {
    if (!peekAiAssistantStore()?.hydrated || id === workbench.activeWorkspaceId) return;
    if (workspaceConversationPreloadTimer) clearTimeout(workspaceConversationPreloadTimer);
    workspaceConversationPreloadTimer = setTimeout(() => {
      workspaceConversationPreloadTimer = undefined;
      const scopeId = workspaceStatePreferences.scopeIdFor(id);
      const aiStore = peekAiAssistantStore();
      if (aiStore && scopeId !== aiStore.activeWorkspaceScopeId) {
        void aiStore.preloadWorkspaceScope(scopeId);
      }
    }, 100);
  }
  function switchWorkspace(id: string) {
    if (workspaceConversationPreloadTimer) {
      clearTimeout(workspaceConversationPreloadTimer);
      workspaceConversationPreloadTimer = undefined;
    }
    const tab = workbench.switchWorkspace(id);
    navigateToTab(tab);
  }
  function openCreateWorkspace() {
    workspaceEditingId.value = "";
    workspaceName.value = "";
    workspaceWorktreeEnabled.value = false;
    workspaceRepoPath.value = "";
    workspaceBranch.value = "";
    workspaceDialogVisible.value = true;
  }
  async function openRenameWorkspace(event: Event, id: string) {
    event.preventDefault();
    event.stopPropagation();
    const workspace = workbench.workspaces.find((item) => item.id === id);
    if (!workspace) return;
    workspaceEditingId.value = id;
    workspaceName.value = workspace.name;
    await nextTick();
    focusInlineInput(`[data-workspace-rename-input="${id}"]`);
  }
  function saveWorkspaceRename() {
    if (!workspaceEditingId.value) return;
    const name = workspaceName.value.trim();
    if (name) workbench.renameWorkspace(workspaceEditingId.value, name);
    workspaceEditingId.value = "";
  }
  function cancelWorkspaceRename() {
    workspaceEditingId.value = "";
  }
  const pendingWorktreePath = computed(() =>
    defaultWorktreePath(workspaceRepoPath.value, workspaceBranch.value)
  );
  const workspaceDialogReady = computed(() => {
    if (!workspaceName.value.trim()) return false;
    return !workspaceWorktreeEnabled.value || Boolean(pendingWorktreePath.value);
  });

  async function chooseWorkspaceRepo() {
    const { chooseGitDirectory } = await loadGitService();
    const path = await chooseGitDirectory(t("tab.workspaceRepoPlaceholder"));
    if (path) workspaceRepoPath.value = path;
  }

  async function saveWorkspaceDialog() {
    const name = workspaceName.value.trim();
    if (!name || workspaceCreating.value) return;

    if (!workspaceWorktreeEnabled.value) {
      workbench.createWorkspace(name);
      navigateToActiveTab();
      workspaceDialogVisible.value = false;
      return;
    }

    const repoPath = workspaceRepoPath.value.trim();
    const branch = workspaceBranch.value.trim();
    const worktreePath = pendingWorktreePath.value;
    if (!repoPath || !branch || !worktreePath) return;

    workspaceCreating.value = true;
    try {
      // 先建 worktree 再建工作区：目录创建失败时不留下指向空路径的绑定。
      const { addGitWorktree } = await loadGitService();
      await addGitWorktree(repoPath, worktreePath, branch, true);
    } catch (error) {
      ElMessage.error(t("tab.workspaceWorktreeFailed", { reason: String(error) }));
      return;
    } finally {
      workspaceCreating.value = false;
    }

    workbench.createWorkspace(name, { repoPath, worktreePath, branch });
    navigateToActiveTab();
    workspaceDialogVisible.value = false;
  }
  async function deleteWorkspace(event: Event, id: string) {
    event.preventDefault();
    event.stopPropagation();
    const workspace = workbench.workspaces.find((item) => item.id === id);
    if (!workspace || id === DEFAULT_WORKSPACE_ID) return;
    try {
      await ElMessageBox.confirm(
        t("tab.workspaceDeleteConfirm", { name: workspace.name }),
        t("tab.deleteWorkspace"),
        {
          type: "warning",
          confirmButtonText: t("common.delete"),
          cancelButtonText: t("common.cancel"),
        }
      );
      const wasActive = workbench.activeWorkspaceId === id;
      workbench.deleteWorkspace(id, workspaceStatePreferences.scopeIdFor(id));
      if (wasActive) navigateToActiveTab();
    } catch {
      // User cancelled.
    }
  }
  async function permanentlyDeleteWorkspace(event: Event, id: string) {
    event.preventDefault();
    event.stopPropagation();
    const record = workbench.deletedWorkspaces.find((item) => item.workspace.id === id);
    if (!record) return;
    try {
      await ElMessageBox.confirm(
        t("tab.workspacePermanentDeleteConfirm", { name: record.workspace.name }),
        t("tab.permanentlyDeleteWorkspace"),
        {
          type: "error",
          confirmButtonText: t("tab.permanentlyDelete"),
          cancelButtonText: t("common.cancel"),
          confirmButtonClass: "el-button--danger",
        }
      );

      const scopeId =
        record.stateScopeId ?? workspaceStatePreferences.scopeIdFor(record.workspace.id);
      const deletedScopeStillOwned = workbench.deletedWorkspaces.some(
        (item) =>
          item.workspace.id !== id &&
          (item.stateScopeId ?? workspaceStatePreferences.scopeIdFor(item.workspace.id)) === scopeId
      );
      const activeScopeStillOwned = workbench.workspaces.some(
        (workspace) =>
          workspace.id !== id &&
          (workspaceStatePreferences.scopeIdFor(workspace.id) === scopeId ||
            (!workspaceStatePreferences.isolationEnabled &&
              workspaceStatePreferences.sharedWorkspaceId === scopeId))
      );
      const scopeStillOwned = deletedScopeStillOwned || activeScopeStillOwned;
      if (!scopeStillOwned) {
        await deleteWorkspaceScopePersistence(scopeId);
        localProject.discardWorkspaceState(scopeId);
        await discardDatabaseWorkspaceScope(scopeId);
      }
      const terminalLayoutIds = record.tabs.map((tab) => tab.id);
      if (!scopeStillOwned) {
        record.tabs
          .filter((tab) => tab.view === "terminal")
          .forEach((_, slot) => {
            terminalLayoutIds.push(
              `workspace-scope:${encodeURIComponent(scopeId)}:terminal:${slot}`
            );
          });
      }
      terminalWorkspace.discardTabs(terminalLayoutIds);
      workbench.permanentlyDeleteWorkspace(id);
      await Promise.all([workbench.flushPersistence(), terminalWorkspace.persistLayout()]);
      ElMessage.success(t("tab.workspacePermanentlyDeleted", { name: record.workspace.name }));
    } catch (error) {
      if (error === "cancel" || error === "close") return;
      ElMessage.error(t("tab.workspacePermanentDeleteFailed", { reason: String(error) }));
    }
  }

  function restoreWorkspace(event: Event, id: string) {
    event.preventDefault();
    event.stopPropagation();
    const restored = workbench.restoreDeletedWorkspace(id);
    if (!restored) return;
    navigateToActiveTab();
    ElMessage.success(t("tab.workspaceRestored", { name: restored.name }));
  }

  onBeforeUnmount(() => {
    if (workspaceConversationPreloadTimer) clearTimeout(workspaceConversationPreloadTimer);
  });

  return {
    workspaceDialogVisible,
    workspaceEditingId,
    workspaceName,
    workspaceWorktreeEnabled,
    workspaceRepoPath,
    workspaceBranch,
    workspaceCreating,
    pendingWorktreePath,
    workspaceDialogReady,
    preloadWorkspaceConversation,
    switchWorkspace,
    openCreateWorkspace,
    openRenameWorkspace,
    saveWorkspaceRename,
    cancelWorkspaceRename,
    chooseWorkspaceRepo,
    saveWorkspaceDialog,
    deleteWorkspace,
    permanentlyDeleteWorkspace,
    restoreWorkspace,
  };
}
