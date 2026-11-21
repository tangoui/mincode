import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ProjectGitPanel from "@/components/projects/ProjectGitPanel.vue";

vi.mock("@/stores/ai-assistant", () => ({ useAiAssistantStore: () => ({ selectedModel: "" }) }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/git-service", () => ({
  listProjectRepositories: vi.fn().mockResolvedValue([{ path: "/test-repo", relative_path: "", name: "Test" }]),
  getRepositorySummaries: vi.fn().mockResolvedValue([]),
  getGitCommitDetail: vi.fn(),
}));
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  nativeErrorMessage: (error: unknown) => String(error),
  copyText: vi.fn(),
  invokeBackend: vi.fn(async (command: string) => {
    switch (command) {
      case "git_get_status":
        return {
          branch: "main",
          staged: [{ path: "test.ts", status: "modified" }],
          unstaged: [{ path: "src/App.VUE", status: "renamed", old_path: "src/Old.vue" }],
          untracked: [],
          ahead: 0,
          behind: 0,
        };
      case "git_list_branches":
        return { current: "main", branches: [] };
      case "git_get_log":
        return [
          {
            hash: "abc123def456",
            short_hash: "abc123d",
            author: "Test Author",
            email: "test@example.com",
            message: "feat: first line\n\nbody that stays private",
            time: 1700000000,
            parents: ["p1", "p2"],
            refs: ["refs/heads/main"],
          },
        ];
      case "git_stash_list":
        return [];
      default:
        throw new Error(`Unexpected native command: ${command}`);
    }
  }),
}));

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

describe("ProjectGitPanel commit section", () => {
  it("starts collapsed and preserves the draft and amend option across toggles", async () => {
    wrapper = mount(ProjectGitPanel, {
      props: { projectRoot: "/test-repo", projects: [] },
      global: { stubs: { GitHistoryList: true, GitShelfPanel: true, GitStashPanel: true } },
    });
    await flushPromises();
    const section = wrapper.find(".project-git-commit-collapse");
    const header = section.find(".el-collapse-item__header");
    const content = section.find(".el-collapse-item__wrap");
    expect(header.attributes("aria-expanded")).toBe("false");
    expect(content.attributes("aria-hidden")).toBe("true");
    expect(section.find(".project-git-commit-collapse__count").text()).toMatch(/1/);

    await header.trigger("click");
    await flushPromises();
    expect(header.attributes("aria-expanded")).toBe("true");
    expect(content.attributes("aria-hidden")).toBe("false");
    await section.find("textarea").setValue("feat: keep this draft");
    await section.find('input[type="checkbox"]').setValue(true);

    await header.trigger("click");
    await flushPromises();
    expect(header.attributes("aria-expanded")).toBe("false");
    expect(content.attributes("aria-hidden")).toBe("true");
    // Element Plus supplies the keyboard interaction as well as click.
    await header.trigger("keydown", { code: "Space", key: " " });
    await flushPromises();
    expect(header.attributes("aria-expanded")).toBe("true");
    expect((section.find("textarea").element as HTMLTextAreaElement).value).toBe("feat: keep this draft");
    expect((section.find('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(true);
  });
});
