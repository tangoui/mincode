// The tag drawer shows every local tag with its commit and date, rejects a
// duplicate name before git does, and can fetch tags from the remote.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";

const state = vi.hoisted(() => ({
  tags: [] as unknown[],
  commands: [] as string[],
  validator: undefined as undefined | ((value: string) => true | string),
}));

vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: vi.fn(async () => state.tags),
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));
vi.mock("@/services/git-service", () => ({
  runGitCommand: vi.fn(async (command: string) => {
    state.commands.push(command);
    if (command === "git_fetch_tags") {
      state.tags = [...state.tags, { name: "v2.0.0", target_hash: "f".repeat(40), commit_hash: "f".repeat(40), annotated: false, date: "2026-11-09T10:00:00+08:00", message: null }];
    }
  }),
}));
vi.mock("element-plus", async (importOriginal) => {
  const actual = await importOriginal<typeof import("element-plus")>();
  return {
    ...actual,
    ElMessage: { error: vi.fn(), success: vi.fn(), warning: vi.fn() },
    ElMessageBox: {
      ...actual.ElMessageBox,
      prompt: vi.fn(async (_message: string, _title: string, options: { inputValidator: (value: string) => true | string }) => {
        state.validator = options.inputValidator;
        throw "cancel";
      }),
    },
  };
});

import GitTagPanel from "@/components/git/GitTagPanel.vue";

let wrapper: VueWrapper | undefined;
beforeEach(() => {
  state.commands = [];
  state.validator = undefined;
  state.tags = [
    { name: "v0.2.0", target_hash: "a".repeat(40), commit_hash: "b".repeat(40), annotated: true, date: "2026-11-01T09:00:00+08:00", message: "MinCode 0.2.0" },
    { name: "v0.1.0", target_hash: "c".repeat(40), commit_hash: "c".repeat(40), annotated: false, date: "2026-10-01T09:00:00+08:00", message: null },
  ];
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

const names = () => wrapper!.findAll(".git-ref-list__name").map((node) => node.text());

describe("GitTagPanel", () => {
  it("lists tags with message, commit and annotation", async () => {
    wrapper = mount(GitTagPanel, { props: { repoPath: "/repo" }, attachTo: document.body });
    await flushPromises();
    expect(names()).toEqual(["v0.2.0", "v0.1.0"]);
    const [annotated, lightweight] = wrapper.findAll(".git-tag-row");
    expect(annotated.text()).toContain("附注");
    expect(annotated.get(".git-tag-row__message").text()).toBe("MinCode 0.2.0");
    expect(annotated.get(".git-tag-row__hash").text()).toBe("bbbbbbbb");
    expect(annotated.get(".git-tag-row__date").text()).not.toBe("");
    expect(lightweight.get(".git-tag-row__message").text()).toBe("");
    expect(wrapper.get(".git-ref-list__count").text()).toBe("2");

    await wrapper.get(".git-ref-toolbar__search input").setValue("0.2");
    expect(names()).toEqual(["v0.2.0"]);
  });

  it("shows a Markdown release subject as plain text and keeps the full name", async () => {
    state.tags = [{ name: "v7.1.12-release-candidate", target_hash: "d".repeat(40), commit_hash: "d".repeat(40), annotated: true, date: "2026-03-30T09:00:00+08:00", message: "**完整的更新**：修复 `select` 组件" }];
    wrapper = mount(GitTagPanel, { props: { repoPath: "/repo" }, attachTo: document.body });
    await flushPromises();
    const row = wrapper.get(".git-tag-row");
    expect(row.get(".git-ref-list__name").text()).toBe("v7.1.12-release-candidate");
    expect(row.get(".git-ref-list__name").attributes("title")).toBe("v7.1.12-release-candidate");
    expect(row.get(".git-tag-row__message").text()).toBe("完整的更新：修复 select 组件");
  });

  it("rejects an existing tag name in the create prompt", async () => {
    wrapper = mount(GitTagPanel, { props: { repoPath: "/repo" }, attachTo: document.body });
    await flushPromises();
    await wrapper.findAll(".git-ref-toolbar__buttons button")[0].trigger("click");
    await flushPromises();
    expect(state.validator?.("v0.1.0")).toBe("标签 v0.1.0 已存在");
    expect(state.validator?.(" v9.9.9 ")).toBe(true);
    expect(state.commands).toEqual([]);
  });

  it("fetches remote tags and reloads the list", async () => {
    wrapper = mount(GitTagPanel, { props: { repoPath: "/repo" }, attachTo: document.body });
    await flushPromises();
    await wrapper.findAll(".git-ref-toolbar__buttons button")[1].trigger("click");
    await flushPromises();
    expect(state.commands).toEqual(["git_fetch_tags"]);
    expect(names()).toContain("v2.0.0");
  });
});
