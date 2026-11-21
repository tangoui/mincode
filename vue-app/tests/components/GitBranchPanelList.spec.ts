// The branch drawer lists local branches with the current one first and lets
// the user narrow the list by name without changing the remaining order.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";

vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: vi.fn(async (command: string) => {
    if (command === "git_list_branches") {
      return {
        current: "Rbac",
        branches: [
          { name: "main", is_current: false, is_remote: false },
          { name: "Rbac", is_current: true, is_remote: false },
          { name: "fix/login", is_current: false, is_remote: false },
          { name: "origin/main", is_current: false, is_remote: true },
        ],
      };
    }
    return { staged: [], unstaged: [], untracked: [] };
  }),
}));
vi.mock("@/services/git-service", () => ({
  getGitMergeState: vi.fn(async () => ({ active: false, conflicts: [] })),
  runGitCommand: vi.fn(),
}));

import GitBranchPanel from "@/components/git/GitBranchPanel.vue";

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

const rowNames = () => wrapper!.findAll(".git-ref-list__name").map((node) => node.text());

describe("GitBranchPanel list", () => {
  it("puts the current branch first, hides remotes and filters by name", async () => {
    wrapper = mount(GitBranchPanel, { props: { repoPath: "/repo" }, attachTo: document.body });
    await flushPromises();
    expect(rowNames()).toEqual(["Rbac", "main", "fix/login"]);
    expect(wrapper.get(".git-ref-list__row.is-current").text()).toContain("当前");
    expect(wrapper.get(".git-ref-list__count").text()).toBe("3");
    expect(wrapper.findAll(".git-inspector-row__action").map((node) => node.attributes("aria-label"))).toEqual(["删除分支", "删除分支"]);

    await wrapper.get(".git-ref-toolbar__search input").setValue("LOG");
    expect(rowNames()).toEqual(["fix/login"]);
    expect(wrapper.get(".git-ref-list__count").text()).toBe("1 / 3");

    await wrapper.get(".git-ref-toolbar__search input").setValue("nothing");
    expect(rowNames()).toEqual([]);
    expect(wrapper.get(".git-ref-list__empty").text()).toContain("没有匹配的分支");
  });

  it("clears the search when the repository changes", async () => {
    wrapper = mount(GitBranchPanel, { props: { repoPath: "/repo" }, attachTo: document.body });
    await flushPromises();
    await wrapper.get(".git-ref-toolbar__search input").setValue("main");
    await wrapper.setProps({ repoPath: "/other" });
    await flushPromises();
    expect((wrapper.get(".git-ref-toolbar__search input").element as HTMLInputElement).value).toBe("");
    expect(rowNames()).toHaveLength(3);
  });
});
