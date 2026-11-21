import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { nextTick, watch } from "vue";
import { useLocalProjectStore } from "@/stores/local-project";

vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));

beforeEach(() => setActivePinia(createPinia()));

describe("local project store: allListedRoots", () => {
  it("lists the projects of every workspace, not only the active one", async () => {
    const project = useLocalProjectStore();
    await project.hydrate("workspace-a");
    project.restoreProject("/repo/a");
    project.restoreProject("/repo/shared");
    project.switchWorkspace("workspace-b");
    project.restoreProject("/repo/b");
    project.restoreProject("/repo/shared");

    expect(project.allListedRoots().sort()).toEqual(["/repo/a", "/repo/b", "/repo/shared"]);

    // Removing a project from one workspace keeps it listed while another still has it.
    project.removeProject("/repo/shared");
    expect(project.allListedRoots()).toContain("/repo/shared");
    project.switchWorkspace("workspace-a");
    project.removeProject("/repo/shared");
    expect(project.allListedRoots()).not.toContain("/repo/shared");

    // A discarded (permanently deleted) workspace no longer contributes its projects.
    expect(project.discardWorkspaceState("workspace-b")).toBe(true);
    expect(project.allListedRoots()).toEqual(["/repo/a"]);
  });

  it("reports other workspaces' projects even when this one lists the same root", async () => {
    const project = useLocalProjectStore();
    await project.hydrate("workspace-a");
    project.restoreProject("/repo/shared");
    project.switchWorkspace("workspace-b");
    project.restoreProject("/repo/shared");
    project.restoreProject("/repo/only-b");
    expect(project.rootsListedByOtherWorkspaces()).toEqual(["/repo/shared"]);
    project.switchWorkspace("workspace-a");
    expect(project.rootsListedByOtherWorkspaces().sort()).toEqual(["/repo/only-b", "/repo/shared"]);
  });

  it("is reactive to discarding a stashed workspace (permanent deletion)", async () => {
    const project = useLocalProjectStore();
    await project.hydrate("workspace-a");
    project.restoreProject("/repo/a");
    project.switchWorkspace("workspace-b");
    project.restoreProject("/repo/b");
    project.switchWorkspace("workspace-a");
    const seen: string[][] = [];
    const stop = watch(() => project.allListedRoots(), (roots) => seen.push([...roots].sort()));
    expect(project.discardWorkspaceState("workspace-b")).toBe(true);
    await nextTick();
    stop();
    expect(seen).toEqual([["/repo/a"]]);
  });

  it("groups other workspaces' roots by workspace, excluding the active and discarded ones", async () => {
    const project = useLocalProjectStore();
    await project.hydrate("workspace-a");
    project.restoreProject("/repo/a");
    project.switchWorkspace("workspace-b");
    project.restoreProject("/repo/b");
    project.switchWorkspace("workspace-c");
    project.restoreProject("/repo/c");

    const grouped = project.rootsByOtherWorkspace();
    expect([...grouped.keys()].sort()).toEqual(["workspace-a", "workspace-b"]);
    expect(grouped.get("workspace-a")).toContain("/repo/a");
    expect(grouped.get("workspace-b")).toContain("/repo/b");
    expect(grouped.has("workspace-c")).toBe(false);

    const seen: string[][] = [];
    const stop = watch(() => project.rootsByOtherWorkspace(), (value) => seen.push([...value.keys()].sort()));
    expect(project.discardWorkspaceState("workspace-b")).toBe(true);
    await nextTick();
    stop();
    expect(seen).toEqual([["workspace-a"]]);
  });
});
