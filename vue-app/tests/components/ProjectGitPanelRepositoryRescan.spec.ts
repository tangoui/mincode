// The repository list is scanned when the project opens; refreshing (the
// refresh button or the periodic refresh) scans it again, so a repository
// whose folder was deleted leaves the list instead of staying as a bare name,
// and a new one appears. When the selected repository is gone, the panel
// moves to the first one left.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ProjectGitPanel from "@/components/projects/ProjectGitPanel.vue";

const git = vi.hoisted(() => ({
  list: vi.fn(),
  summaries: vi.fn(),
  invoke: vi.fn(),
}));

vi.mock("@/stores/ai-assistant", () => ({ useAiAssistantStore: () => ({ selectedModel: "" }) }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/git-service", () => ({
  listProjectRepositories: (...args: unknown[]) => git.list(...args),
  getRepositorySummaries: (...args: unknown[]) => git.summaries(...args),
  getGitCommitDetail: vi.fn(),
}));
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  nativeErrorMessage: (error: unknown) => String(error),
  copyText: vi.fn(),
  invokeBackend: (...args: unknown[]) => git.invoke(...args),
}));

const A = { path: "/w/a", relative_path: "a", name: "a" };
const B = { path: "/w/b", relative_path: "b", name: "b" };
const C = { path: "/w/c", relative_path: "c", name: "c" };

let wrapper: ReturnType<typeof mount> | undefined;

beforeEach(() => {
  git.list.mockReset();
  git.summaries.mockReset().mockResolvedValue([]);
  git.invoke.mockReset().mockImplementation(async (command: string) => {
    switch (command) {
      case "git_get_status":
        return { branch: "main", staged: [], unstaged: [], untracked: [], ahead: 0, behind: 0 };
      case "git_list_branches":
        return { current: "main", branches: [] };
      case "git_get_log":
      case "git_stash_list":
        return [];
      default:
        return null;
    }
  });
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

async function mountPanel() {
  wrapper = mount(ProjectGitPanel, {
    props: { projectRoot: "/w", projects: [] },
    global: { stubs: { GitHistoryList: true, GitShelfPanel: true, GitStashPanel: true } },
  });
  await flushPromises();
  return wrapper;
}

function rowNames() {
  return wrapper!.findAll(".git-repository-row").map((row) => row.attributes("title"));
}

function statusRepoPaths() {
  return git.invoke.mock.calls.filter((call) => call[0] === "git_get_status").map((call) => (call[1] as { repoPath: string }).repoPath);
}

async function clickRefresh() {
  await wrapper!.find(".git-command-bar__actions button").trigger("click");
  await flushPromises();
}

describe("ProjectGitPanel repository rescan", () => {
  it("drops a deleted repository and shows a new one on refresh", async () => {
    git.list.mockResolvedValueOnce([A, B, C]).mockResolvedValue([A, C]);
    await mountPanel();
    expect(rowNames()).toEqual(["a", "b", "c"]);

    await clickRefresh();
    expect(git.list).toHaveBeenCalledTimes(2);
    expect(rowNames()).toEqual(["a", "c"]);
  });

  it("moves to the first repository left when the selected one is gone", async () => {
    git.list.mockResolvedValueOnce([A, B]).mockResolvedValue([B, C]);
    await mountPanel();
    expect(statusRepoPaths().at(-1)).toBe("/w/a");

    await clickRefresh();
    expect(rowNames()).toEqual(["b", "c"]);
    expect(statusRepoPaths().at(-1)).toBe("/w/b");
    expect(wrapper!.find(".git-repository-row.is-active").attributes("title")).toBe("b");
  });

  it("keeps the list when the rescan fails", async () => {
    git.list.mockResolvedValueOnce([A, B]).mockRejectedValue(new Error("gone"));
    await mountPanel();
    await clickRefresh();
    expect(rowNames()).toEqual(["a", "b"]);
    expect(statusRepoPaths().at(-1)).toBe("/w/a");
  });
});
