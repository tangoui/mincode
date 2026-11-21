import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import ProjectGitPanel from "@/components/projects/ProjectGitPanel.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import { parseWhenExpression } from "@/domain/commands/when";
import type { PluginContextMenuId, PluginMenuItem } from "@/domain/plugins/menus";
import { useVueI18n } from "@/i18n/messages";
import { pluginMenuItems } from "@/services/contributions/menus";

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

// Element Plus dropdowns need a live popper; these stubs keep every slot rendered and forward listeners.
const DropdownItem = defineComponent({
  inheritAttrs: false,
  setup(_, { attrs, slots }) {
    return () => h("li", { role: "menuitem", tabindex: -1, ...attrs }, slots.default?.());
  },
});
const Dropdown = defineComponent({
  name: "DropdownStub",
  emits: ["visible-change", "command"],
  setup(_, { slots }) {
    return () => h("div", { class: "dropdown-stub" }, [slots.default?.(), slots.dropdown?.()]);
  },
});
const DropdownMenu = defineComponent({
  setup(_, { slots }) {
    return () => h("ul", slots.default?.());
  },
});
const ContextMenu = defineComponent({
  name: "AppContextMenu",
  props: { open: Boolean },
  emits: ["close"],
  setup(props, { slots }) {
    return () => h("ul", { class: "context-menu-stub", "data-open": String(props.open) }, slots.default?.());
  },
});

function menuItem(menu: PluginContextMenuId, command: string, title: string, when = ""): PluginMenuItem {
  return {
    pluginId: "demo.menus",
    pluginName: "Demo Menus",
    menu,
    command,
    title,
    ...(when ? { when } : {}),
    whenClauses: when ? parseWhenExpression(when) : [],
    group: "",
    order: 0,
    passContext: true,
    hidden: false,
  };
}

function mountPanel() {
  return mount(ProjectGitPanel, {
    props: { projectRoot: "/test-repo", projects: [] },
    global: {
      stubs: {
        GitShelfPanel: true,
        GitStashPanel: true,
        AppContextMenu: ContextMenu,
        ElDropdown: Dropdown,
        "el-dropdown": Dropdown,
        ElDropdownMenu: DropdownMenu,
        "el-dropdown-menu": DropdownMenu,
        ElDropdownItem: DropdownItem,
        "el-dropdown-item": DropdownItem,
      },
    },
  });
}

function menuLabels(root: { findAll: (selector: string) => Array<{ text: () => string }> }) {
  return root.findAll("[role=menuitem]").map((item) => item.text());
}

describe("ProjectGitPanel plugin menus", () => {
  beforeEach(() => {
    plugin.execute.mockReset();
    pluginMenuItems.value = [
      menuItem("git/changes/context", "inspect", "Inspect change"),
      menuItem("git/changes/context", "stagedOnly", "Staged only", 'git.group == "staged"'),
      menuItem("git/commit/context", "inspect", "Inspect commit", "git.isMerge && git.hasRefs"),
    ];
  });
  afterEach(() => {
    pluginMenuItems.value = [];
  });

  it("adds plugin items after 'Copy relative path' and passes the whitelisted change context", async () => {
    wrapper = mountPanel();
    await flushPromises();
    const menu = () => wrapper!.get(".context-menu-stub");
    const changeMenu = () => wrapper!.findAllComponents(PluginMenuItems).find((item) => item.props("menu") === "git/changes/context")!;
    expect(changeMenu().props("host")).toBeNull();
    expect(menuLabels(menu())).not.toContain("Inspect change");

    const rows = wrapper.findAll(".git-file-row");
    const unstagedRow = rows.find((row) => row.attributes("title") === "src/App.VUE")!;
    await unstagedRow.trigger("contextmenu", { clientX: 10, clientY: 10 });
    const labels = menuLabels(menu());
    expect(labels.at(-1)).toBe("Inspect change");
    expect(labels.at(-2)).toBe(useVueI18n().t("git.fileContext.copyRelativePath"));
    const items = menu().findAll("[role=menuitem]");
    expect(items.at(-1)!.attributes("divided")).toBeDefined();
    expect(labels).not.toContain("Staged only");
    expect(changeMenu().props("host")).toMatchObject({ "git.group": "unstaged", "git.status": "renamed", "git.extname": ".vue" });

    await items.at(-1)!.trigger("click");
    await flushPromises();
    expect(plugin.execute).toHaveBeenCalledWith("demo.menus", "inspect", {
      menu: "git/changes/context",
      context: {
        projectName: "test-repo",
        repositoryPath: "/test-repo",
        relativePath: "src/App.VUE",
        oldRelativePath: "src/Old.vue",
        status: "renamed",
        group: "unstaged",
        path: "/test-repo/src/App.VUE",
      },
    });
    // Choosing an item closes the menu; a closed menu evaluates nothing.
    await flushPromises();
    expect(wrapper.get(".context-menu-stub").attributes("data-open")).toBe("false");
    expect(changeMenu().props("host")).toBeNull();

    const stagedRow = wrapper.findAll(".git-file-row").find((row) => row.attributes("title") === "test.ts")!;
    await stagedRow.trigger("contextmenu", { clientX: 10, clientY: 10 });
    expect(menuLabels(menu()).slice(-2)).toEqual(["Inspect change", "Staged only"]);
  });

  it("adds plugin items to the commit '...' menu with the first line of the subject only", async () => {
    wrapper = mountPanel();
    await flushPromises();
    wrapper.findComponent({ name: "ElSegmented" }).vm.$emit("update:modelValue", "history");
    await flushPromises();

    const commitMenu = () => wrapper!.findAllComponents(PluginMenuItems).find((item) => item.props("menu") === "git/commit/context");
    const history = wrapper.findComponent({ name: "GitHistoryList" });
    // No menu (and so no plugin items) is mounted until a row's "⋯" is clicked.
    expect(commitMenu()).toBeUndefined();

    await history.get(".app-git-history__more").trigger("click");
    await flushPromises();
    expect(commitMenu()!.props("host")).toEqual({ "git.isMerge": true, "git.hasRefs": true });
    const menu = history.findComponent({ name: "AppContextMenu" });
    const items = menu.findAll("[role=menuitem]");
    expect(items.at(-1)!.text()).toBe("Inspect commit");
    expect(items.at(-1)!.attributes("divided")).toBeDefined();

    await items.at(-1)!.trigger("click");
    await flushPromises();
    expect(plugin.execute).toHaveBeenCalledWith("demo.menus", "inspect", {
      menu: "git/commit/context",
      context: {
        projectName: "test-repo",
        repositoryPath: "/test-repo",
        hash: "abc123def456",
        shortHash: "abc123d",
        subject: "feat: first line",
        author: "Test Author",
        time: 1700000000,
        parents: ["p1", "p2"],
        refs: ["refs/heads/main"],
      },
    });
    // A plugin item is not a history action.
    expect(history.emitted("action")).toBeUndefined();

    menu.vm.$emit("close");
    await flushPromises();
    expect(commitMenu()).toBeUndefined();
  });
});
