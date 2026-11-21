import { beforeEach, describe, expect, it, vi } from "vitest";
import { useLocalProjectStore } from "@/stores/local-project";
import { useProjectDocumentLoader } from "@/composables/use-project-document-loader";
import { loadPersistedValue } from "@/services/persistent-store";

vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/desktop-bridge", () => ({
  nativeErrorMessage: (error: unknown) => error instanceof Error ? error.message : String(error),
}));

function deferred() {
  let resolve!: (value: string) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<string>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

beforeEach(() => vi.mocked(loadPersistedValue).mockReset().mockResolvedValue(null));

describe("project document loading", () => {
  it("joins a restored tab's first read instead of returning before it finishes", async () => {
    vi.mocked(loadPersistedValue).mockImplementation(async (key: string) => key === "app_local_project_v1" ? {
      version: 6, projectRoot: "/repo", projects: [{ root: "/repo", name: "repo" }],
      openPaths: ["/repo/README.md"], activePath: "/repo/README.md",
    } : null);
    const project = useLocalProjectStore();
    await project.hydrate();
    const pending = deferred();
    const read = vi.fn(() => pending.promise);
    const loader = useProjectDocumentLoader(project, read);
    const first = loader.load("/repo/README.md", "/repo");
    const second = loader.load("/repo/README.md", "/repo");
    expect(first).toBe(second);
    expect(read).toHaveBeenCalledTimes(1);
    expect(project.activeDocument?.loading).toBe(true);
    pending.resolve("# Ready");
    await second;
    expect(project.activeDocument?.content).toBe("# Ready");
    expect(project.activeDocument?.loaded).toBe(true);
    expect(project.activeDocument?.loading).toBe(false);
    await loader.load("/repo/README.md", "/repo");
    expect(read).toHaveBeenCalledTimes(1);
  });

  it("does not reactivate an old tab when its read succeeds or fails", async () => {
    for (const failure of [false, true]) {
      const project = useLocalProjectStore();
      project.resetProject("/repo");
      project.openDocument("/repo/old.txt");
      const pending = deferred();
      const loader = useProjectDocumentLoader(project, () => pending.promise);
      const task = loader.load("/repo/old.txt", "/repo");
      project.openDocument("/repo/new.txt");
      if (failure) pending.reject(new Error("stat: file does not exist"));
      else pending.resolve("old content");
      await task;
      expect(project.activePath).toBe("/repo/new.txt");
      const old = project.documents.find((document) => document.path.endsWith("old.txt"));
      expect(failure ? old?.errorDetail : old?.content).toBe(failure ? "stat: file does not exist" : "old content");
    }
  });

  it("retries a failed file and clears its previous error on success", async () => {
    const project = useLocalProjectStore();
    project.openDocument("/repo/file.txt");
    const read = vi.fn().mockRejectedValueOnce(new Error("permission denied")).mockResolvedValueOnce("recovered");
    const loader = useProjectDocumentLoader(project, read);
    await loader.load("/repo/file.txt", "/repo");
    expect(project.activeDocument?.errorDetail).toBe("permission denied");
    await loader.load("/repo/file.txt", "/repo");
    expect(project.activeDocument?.content).toBe("recovered");
    expect(project.activeDocument?.errorKey).toBeUndefined();
    expect(project.activeDocument?.errorDetail).toBeUndefined();
  });

  it("does not reopen a closed tab or overwrite its replacement", async () => {
    const project = useLocalProjectStore();
    project.openDocument("/repo/file.txt");
    const pending = deferred();
    const loader = useProjectDocumentLoader(project, () => pending.promise);
    const task = loader.load("/repo/file.txt", "/repo");
    project.documents = [];
    project.activePath = null;
    const replacement = project.openDocument("/repo/file.txt");
    pending.resolve("stale");
    await task;
    expect(replacement.loaded).toBe(false);
    expect(replacement.content).toBe("");
  });

  // 回复先于下一帧到达时，内容（建编辑器）要等新标签画出来之后再应用，
  // 否则打开标签的渲染和建编辑器连成一个长任务，中间不出帧。
  it("applies the content only after the opened tab has painted", async () => {
    const frames: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (fn: FrameRequestCallback) => frames.push(fn));
    try {
      const project = useLocalProjectStore();
      project.openDocument("/repo/file.txt");
      const loader = useProjectDocumentLoader(project, async () => "content");
      const task = loader.load("/repo/file.txt", "/repo");
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(project.activeDocument?.loaded).toBe(false);
      expect(frames).toHaveLength(1);
      frames[0](performance.now()); // 这一帧的绘制在 rAF 回调之后
      expect(project.activeDocument?.loaded).toBe(false);
      await task;
      expect(project.activeDocument?.content).toBe("content");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("does not wait for a frame that never comes (hidden window)", async () => {
    vi.stubGlobal("requestAnimationFrame", () => 0);
    try {
      const project = useLocalProjectStore();
      project.openDocument("/repo/file.txt");
      const loader = useProjectDocumentLoader(project, async () => "content");
      await loader.load("/repo/file.txt", "/repo");
      expect(project.activeDocument?.content).toBe("content");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("drops a response after switching workspace and can reload on returning", async () => {
    const project = useLocalProjectStore();
    await project.hydrate();
    project.restoreProject("/repo");
    project.openDocument("/repo/file.txt");
    const workspace = project.activeWorkspaceId;
    const pending = deferred();
    const read = vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValueOnce("fresh");
    const loader = useProjectDocumentLoader(project, read);
    const task = loader.load("/repo/file.txt", "/repo");
    project.switchWorkspace("workspace:other");
    pending.resolve("stale");
    await task;
    expect(project.documents).toHaveLength(0);
    project.switchWorkspace(workspace);
    expect(project.activeDocument?.loading).toBe(false);
    await loader.load("/repo/file.txt", "/repo");
    expect(project.activeDocument?.content).toBe("fresh");
  });
});
