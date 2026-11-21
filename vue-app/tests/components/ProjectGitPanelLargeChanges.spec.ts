// A large change set renders only the file rows near the viewport (each row carries a checkbox
// and actions); 3000 changed files rendered 34k elements and every scroll frame took >100 ms.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ProjectGitPanel from "@/components/projects/ProjectGitPanel.vue";

const plugin = vi.hoisted(() => ({ execute: vi.fn() }));
vi.mock("@/services/plugin-commands", async () => {
  const { shallowRef } = await import("vue");
  return {
    pluginCommandState: shallowRef({
      commands: [
        { pluginId: "demo.menus", id: "inspect" },
        { pluginId: "demo.menus", id: "stagedOnly" },
      ],
      contexts: {},
    }),
    executePluginCommand: (...args: unknown[]) => plugin.execute(...args),
    setPluginCommandPaletteOverrides: () => {},
  };
});

vi.mock("@/stores/ai-assistant", () => ({ useAiAssistantStore: () => ({ selectedModel: "" }) }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/git-service", () => ({
  listProjectRepositories: vi.fn().mockResolvedValue([{ path: "/test-repo", relative_path: "", name: "Test" }]),
  getRepositorySummaries: vi.fn().mockResolvedValue([]),
  getGitCommitDetail: vi.fn(async (_repo: string, hash: string) => ({
    commit: { hash, short_hash: hash.slice(0, 7), author: "Test Author", email: "", message: "big commit", time: 1700000000, parents: [], refs: [] },
    files: Array.from({ length: 2000 }, (_, i) => ({ path: `pkg/file${i}.ts`, status: "A" })),
  })),
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
          staged: [{ path: "staged.ts", status: "modified" }],
          unstaged: Array.from({ length: 2000 }, (_, i) => ({ path: `pkg/file${i}.ts`, status: "modified" })),
          untracked: Array.from({ length: 1000 }, (_, i) => ({ path: `generated/out${i}.txt`, status: "untracked" })),
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

const ROW = 29;

const HEADER = 28;
/** Content offset of each group's first file row: groups stack as header + files. */
const groupTops = { staged: HEADER, unstaged: HEADER + 1 * ROW + HEADER, untracked: HEADER + (1 + 2000) * ROW + 2 * HEADER };

/** happy-dom has no layout: give the list a viewport, rows their height and groups their positions. */
function layout(element: Element) {
  const wrap = element.querySelector<HTMLElement>(".git-change-scroll .el-scrollbar__wrap")!;
  Object.defineProperty(wrap, "clientHeight", { configurable: true, value: 600 });
  wrap.getBoundingClientRect = () => ({ top: 0, left: 0, right: 300, bottom: 600, width: 300, height: 600, x: 0, y: 0, toJSON: () => ({}) });
  for (const row of element.querySelectorAll<HTMLElement>(".git-file-row")) {
    Object.defineProperty(row, "offsetHeight", { configurable: true, value: ROW });
  }
  for (const files of element.querySelectorAll<HTMLElement>("[data-row-group]")) {
    const top = groupTops[files.dataset.rowGroup as keyof typeof groupTops] - wrap.scrollTop;
    files.getBoundingClientRect = () => ({ top, left: 0, right: 300, bottom: top, width: 300, height: 0, x: 0, y: top, toJSON: () => ({}) });
  }
  return wrap;
}

describe("ProjectGitPanel large change list", { timeout: 120_000 }, () => {
  it("renders a window of a large commit's files and follows scrolling", async () => {
    wrapper = mount(ProjectGitPanel, {
      props: { projectRoot: "/test-repo", projects: [] },
      global: { stubs: { GitHistoryList: true, GitShelfPanel: true, GitStashPanel: true } },
      attachTo: document.body,
    });
    await flushPromises();
    wrapper.findComponent({ name: "ElSegmented" }).vm.$emit("update:modelValue", "history");
    await flushPromises();
    wrapper.findComponent({ name: "GitHistoryList" }).vm.$emit("select", { hash: "abc123def456", short_hash: "abc123d", author: "", message: "big", time: 0, parents: [], refs: [] });
    await flushPromises();
    await vi.waitFor(() => expect(wrapper!.find(".project-git-commit-files").exists()).toBe(true));
    const FILE = 30;
    const commitLayout = () => {
      const wrap = (wrapper!.element as HTMLElement).querySelector<HTMLElement>(".project-git-commit-files .el-scrollbar__wrap")!;
      Object.defineProperty(wrap, "clientHeight", { configurable: true, value: 600 });
      wrap.getBoundingClientRect = () => ({ top: 0, left: 0, right: 300, bottom: 600, width: 300, height: 600, x: 0, y: 0, toJSON: () => ({}) });
      for (const row of (wrapper!.element as HTMLElement).querySelectorAll<HTMLElement>(".project-git-commit-files [data-row-group] > button")) {
        Object.defineProperty(row, "offsetHeight", { configurable: true, value: FILE });
      }
      for (const group of (wrapper!.element as HTMLElement).querySelectorAll<HTMLElement>(".project-git-commit-files [data-row-group]")) {
        const top = -wrap.scrollTop;
        group.getBoundingClientRect = () => ({ top, left: 0, right: 300, bottom: top, width: 300, height: 0, x: 0, y: top, toJSON: () => ({}) });
      }
      return wrap;
    };
    const files = () => wrapper!.findAll(".project-git-commit-files button").map((row) => row.attributes("title"));
    const wrap = commitLayout();
    wrap.dispatchEvent(new Event("scroll"));
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await flushPromises();
    expect(files().length).toBeGreaterThan(0);
    expect(files().length).toBeLessThan(200);
    expect(files()).toContain("pkg/file0.ts");

    wrap.scrollTop = 2000 * FILE - 600;
    commitLayout();
    wrap.dispatchEvent(new Event("scroll"));
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await flushPromises();
    expect(files()).toContain("pkg/file1999.ts");
    expect(files()).not.toContain("pkg/file0.ts");
  });

  it("renders a window of file rows and follows scrolling", async () => {
    wrapper = mount(ProjectGitPanel, {
      props: { projectRoot: "/test-repo", projects: [] },
      global: { stubs: { GitHistoryList: true, GitShelfPanel: true, GitStashPanel: true } },
      attachTo: document.body,
    });
    await flushPromises();
    await vi.waitFor(() => expect(wrapper!.find(".git-change-group__header b").exists()).toBe(true));
    const counts = wrapper.findAll(".git-change-group__header b").map((node) => node.text());
    expect(counts).toEqual(["1", "2000", "1000"]);

    const wrap = layout(wrapper.element);
    wrap.dispatchEvent(new Event("scroll"));
    await flushPromises();
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await flushPromises();
    const rendered = () => wrapper!.findAll(".git-file-row").map((row) => row.attributes("title"));
    expect(rendered().length).toBeLessThan(300);
    expect(rendered()).toContain("pkg/file0.ts");
    expect(rendered()).not.toContain("generated/out999.txt");

    // Scroll near the end of the list: the last untracked files are rendered.
    wrap.scrollTop = groupTops.untracked + 1000 * ROW - 600;
    layout(wrapper.element);
    wrap.dispatchEvent(new Event("scroll"));
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await flushPromises();
    expect(rendered()).toContain("generated/out999.txt");
    expect(rendered()).not.toContain("pkg/file0.ts");
  });
});
