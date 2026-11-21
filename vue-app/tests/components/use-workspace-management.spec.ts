import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { useWorkspaceManagement } from "@/composables/use-workspace-management";
import { DEFAULT_WORKSPACE_ID, useWorkbenchStore } from "@/stores/workbench";

const elementPlus = vi.hoisted(() => ({
  confirm: vi.fn(),
  error: vi.fn(),
}));
const gitService = vi.hoisted(() => ({
  addGitWorktree: vi.fn(),
  chooseGitDirectory: vi.fn(),
}));

vi.mock("element-plus", async (importOriginal) => ({
  ...(await importOriginal<typeof import("element-plus")>()),
  ElMessage: { error: elementPlus.error },
  ElMessageBox: { confirm: elementPlus.confirm },
}));
vi.mock("@/services/git-service", () => gitService);

function setup() {
  const navigateToTab = vi.fn();
  const navigateToActiveTab = vi.fn();
  let api!: ReturnType<typeof useWorkspaceManagement>;
  const wrapper = mount(
    defineComponent({
      setup() {
        api = useWorkspaceManagement({ navigateToTab, navigateToActiveTab });
        return () => null;
      },
    })
  );
  return { api, wrapper, navigateToTab, navigateToActiveTab, workbench: useWorkbenchStore() };
}

function clickEvent() {
  return new MouseEvent("click", { cancelable: true });
}

describe("useWorkspaceManagement", () => {
  beforeEach(() => {
    elementPlus.confirm.mockReset();
    elementPlus.error.mockReset();
    gitService.addGitWorktree.mockReset();
    gitService.chooseGitDirectory.mockReset();
  });

  it("creates a plain workspace and navigates to it", async () => {
    const { api, workbench, navigateToActiveTab } = setup();
    api.openCreateWorkspace();
    expect(api.workspaceDialogVisible.value).toBe(true);
    expect(api.workspaceDialogReady.value).toBe(false);

    api.workspaceName.value = "  Feature  ";
    expect(api.workspaceDialogReady.value).toBe(true);
    await api.saveWorkspaceDialog();

    const created = workbench.workspaces.at(-1);
    expect(created?.name).toBe("Feature");
    expect(created?.worktree).toBeUndefined();
    expect(workbench.activeWorkspaceId).toBe(created?.id);
    expect(navigateToActiveTab).toHaveBeenCalledOnce();
    expect(api.workspaceDialogVisible.value).toBe(false);
    expect(gitService.addGitWorktree).not.toHaveBeenCalled();
  });

  it("creates the git worktree before binding it to a new workspace", async () => {
    const { api, workbench } = setup();
    gitService.addGitWorktree.mockResolvedValue(undefined);
    api.openCreateWorkspace();
    api.workspaceName.value = "Fix";
    api.workspaceWorktreeEnabled.value = true;
    expect(api.workspaceDialogReady.value).toBe(false);
    api.workspaceRepoPath.value = "/repo/";
    api.workspaceBranch.value = "fix/login bug";
    expect(api.pendingWorktreePath.value).toBe("/repo/.worktrees/fix-login-bug");

    await api.saveWorkspaceDialog();

    expect(gitService.addGitWorktree).toHaveBeenCalledWith(
      "/repo/",
      "/repo/.worktrees/fix-login-bug",
      "fix/login bug",
      true
    );
    expect(workbench.workspaces.at(-1)?.worktree).toMatchObject({
      worktreePath: "/repo/.worktrees/fix-login-bug",
      branch: "fix/login bug",
    });
    expect(api.workspaceCreating.value).toBe(false);
  });

  it("does not create a workspace when the worktree cannot be created", async () => {
    const { api, workbench, navigateToActiveTab } = setup();
    gitService.addGitWorktree.mockRejectedValue(new Error("branch already checked out"));
    const before = workbench.workspaces.length;
    api.openCreateWorkspace();
    api.workspaceName.value = "Broken";
    api.workspaceWorktreeEnabled.value = true;
    api.workspaceRepoPath.value = "/repo";
    api.workspaceBranch.value = "main";

    await api.saveWorkspaceDialog();

    expect(workbench.workspaces).toHaveLength(before);
    expect(elementPlus.error).toHaveBeenCalledOnce();
    expect(String(elementPlus.error.mock.calls[0]?.[0])).toContain("branch already checked out");
    expect(navigateToActiveTab).not.toHaveBeenCalled();
    expect(api.workspaceDialogVisible.value).toBe(true);
    expect(api.workspaceCreating.value).toBe(false);
  });

  it("keeps the workspace when the delete confirmation is cancelled", async () => {
    const { api, workbench } = setup();
    const workspace = workbench.createWorkspace("Keep me")!;
    elementPlus.confirm.mockRejectedValue("cancel");

    await api.deleteWorkspace(clickEvent(), workspace.id);
    await flushPromises();

    expect(workbench.workspaces.some((item) => item.id === workspace.id)).toBe(true);
    expect(workbench.deletedWorkspaces).toHaveLength(0);
  });

  it("archives a confirmed delete and returns to the default workspace", async () => {
    const { api, workbench, navigateToActiveTab } = setup();
    const workspace = workbench.createWorkspace("Remove me")!;
    elementPlus.confirm.mockResolvedValue("confirm");

    await api.deleteWorkspace(clickEvent(), workspace.id);

    expect(workbench.workspaces.some((item) => item.id === workspace.id)).toBe(false);
    expect(workbench.deletedWorkspaces[0]?.workspace.id).toBe(workspace.id);
    expect(workbench.activeWorkspaceId).toBe(DEFAULT_WORKSPACE_ID);
    expect(navigateToActiveTab).toHaveBeenCalledOnce();
  });

  it("never offers to delete the default workspace", async () => {
    const { api } = setup();
    await api.deleteWorkspace(clickEvent(), DEFAULT_WORKSPACE_ID);
    expect(elementPlus.confirm).not.toHaveBeenCalled();
  });
});
