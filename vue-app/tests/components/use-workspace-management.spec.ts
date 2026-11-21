import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { useWorkspaceManagement } from "@/composables/use-workspace-management";
import { DEFAULT_WORKSPACE_ID, useWorkbenchStore } from "@/stores/workbench";

const elementPlus = vi.hoisted(() => ({
  confirm: vi.fn(),
  error: vi.fn(),
}));

vi.mock("element-plus", async (importOriginal) => ({
  ...(await importOriginal<typeof import("element-plus")>()),
  ElMessage: { error: elementPlus.error },
  ElMessageBox: { confirm: elementPlus.confirm },
}));

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
    expect(workbench.activeWorkspaceId).toBe(created?.id);
    expect(navigateToActiveTab).toHaveBeenCalledOnce();
    expect(api.workspaceDialogVisible.value).toBe(false);
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
