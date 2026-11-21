import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import type { PluginScmProvider } from "@/domain/plugins/scm-commit-message";
import { useVueI18n } from "@/i18n/messages";
import { usePluginDataProvidersStore } from "@/stores/plugin-data-providers";

// Toasts are recorded instead of rendered.
const toast = vi.hoisted(() => ({ messages: [] as Array<[string, string]> }));
vi.mock("element-plus", async (importOriginal) => {
  const original = await importOriginal<typeof import("element-plus")>();
  const record = (kind: string) => (options: unknown) => {
    toast.messages.push([kind, typeof options === "string" ? options : String((options as { message?: unknown }).message)]);
    return { close: () => undefined };
  };
  const ElMessage = Object.assign(record("info"), {
    success: record("success"),
    warning: record("warning"),
    error: record("error"),
    info: record("info"),
  });
  return { ...original, ElMessage };
});

// The desktop bridge is stubbed: every host command is recorded and answered from `bridge.answer`.
const bridge = vi.hoisted(() => ({
  calls: [] as Array<[string, Record<string, unknown>]>,
  answer: (_command: string, _args: Record<string, unknown>): Promise<unknown> | undefined => undefined,
}));
vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/desktop-bridge")>()),
  isDesktopRuntime: () => true,
  listenAppEvent: () => () => undefined,
  copyText: vi.fn(),
  invokeBackend: (command: string, args: Record<string, unknown> = {}) => {
    bridge.calls.push([command, args]);
    const custom = bridge.answer(command, args);
    if (custom) return custom;
    switch (command) {
      case "git_get_status":
        return Promise.resolve({
          branch: "main",
          staged: [{ path: "test.ts", status: "modified" }],
          unstaged: [],
          untracked: [],
          ahead: 0,
          behind: 0,
        });
      case "git_list_branches":
        return Promise.resolve({ current: "main", branches: [] });
      case "git_get_log":
        return Promise.resolve([]);
      case "git_stash_list":
        return Promise.resolve([]);
      case "plugin_host_contribution_cancel":
        return Promise.resolve({ cancelled: true });
      default:
        return Promise.resolve(undefined);
    }
  },
}));
vi.mock("@/stores/ai-assistant", () => ({ useAiAssistantStore: () => ({ selectedModel: "" }) }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/git-service", () => ({
  listProjectRepositories: vi.fn().mockResolvedValue([{ path: "/work/app", relative_path: "", name: "app" }]),
  getRepositorySummaries: vi.fn().mockResolvedValue([]),
  getGitCommitDetail: vi.fn(),
}));

const { default: GitPluginCommitMessageButton } = await import("@/components/projects/GitPluginCommitMessageButton.vue");
const { default: ProjectGitPanel } = await import("@/components/projects/ProjectGitPanel.vue");
const { t } = useVueI18n();

const provider: PluginScmProvider = {
  pluginId: "acme.ticket",
  pluginName: "Ticket",
  id: "conventional",
  title: "Ticket format",
  titleLocalized: { zh: "工单格式" },
  activeInWorkspace: true,
};

// Element Plus dropdowns need a live popper; these stubs keep every slot rendered and forward listeners.
const DropdownItem = defineComponent({
  inheritAttrs: false,
  props: { command: { type: null, default: undefined } },
  setup(_, { attrs, slots }) {
    return () => h("li", { role: "menuitem", tabindex: -1, ...attrs }, slots.default?.());
  },
});
const Dropdown = defineComponent({
  name: "DropdownStub",
  emits: ["command"],
  setup(_, { slots }) {
    return () => h("div", { class: "dropdown-stub" }, [slots.default?.(), slots.dropdown?.()]);
  },
});
const DropdownMenu = defineComponent({
  setup(_, { slots }) {
    return () => h("ul", slots.default?.());
  },
});
const dropdownStubs = {
  ElDropdown: Dropdown,
  "el-dropdown": Dropdown,
  ElDropdownMenu: DropdownMenu,
  "el-dropdown-menu": DropdownMenu,
  ElDropdownItem: DropdownItem,
  "el-dropdown-item": DropdownItem,
};

const commandCalls = (name: string) => bridge.calls.filter(([command]) => command === name);

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  bridge.calls = [];
  bridge.answer = () => undefined;
  toast.messages = [];
});

const settle = async () => {
  await flushPromises();
  await new Promise((resolve) => setTimeout(resolve, 0));
  await flushPromises();
};

function mountButton(props: Record<string, unknown> = {}) {
  wrapper = mount(GitPluginCommitMessageButton, {
    props: {
      providers: [provider],
      projectRoot: "/work/app",
      repoPath: "/work/app",
      hasStaged: true,
      draft: "",
      busy: false,
      generating: false,
      ...props,
    },
    global: { stubs: dropdownStubs },
    attachTo: document.body,
  });
  return wrapper;
}

async function mountPanel() {
  usePluginDataProvidersStore().setScmProviders([provider]);
  wrapper = mount(ProjectGitPanel, {
    props: { projectRoot: "/work/app", projects: [] },
    global: { stubs: { GitHistoryList: true, GitShelfPanel: true, GitStashPanel: true } },
    attachTo: document.body,
  });
  await settle();
  await wrapper.get(".project-git-commit-collapse .el-collapse-item__header").trigger("click");
  await settle();
  return wrapper;
}

const pluginButton = (view: VueWrapper) => view.get('[data-testid="git-plugin-generate"]');
const textarea = (view: VueWrapper) => view.get(".project-git-commit-collapse textarea");
const buttonByText = (view: VueWrapper, text: string) =>
  view.findAll(".project-git-commit-collapse__actions button, .git-commit-submit-row__actions button")
    .find((button) => button.text().trim() === text)!;

describe("GitPluginCommitMessageButton", () => {
  it("fills the draft and never commits", async () => {
    bridge.answer = (command) =>
      command === "plugin_host_scm_commit_message"
        ? Promise.resolve({ message: "feat: x", truncated: false, diffTruncated: false, omittedFiles: 0 })
        : undefined;
    const view = mountButton();
    expect(pluginButton(view).attributes("title")).toBe(t("git.pluginGenerateTitle", { plugin: "Ticket", title: "工单格式" }));
    await pluginButton(view).trigger("click");
    await settle();
    const [[, args]] = commandCalls("plugin_host_scm_commit_message");
    expect(args).toMatchObject({ pluginId: "acme.ticket", providerId: "conventional", projectRoot: "/work/app", repoPath: "/work/app" });
    expect(Object.keys(args).sort()).toEqual(["locale", "pluginId", "projectRoot", "providerId", "repoPath", "requestId"]);
    expect(view.emitted("start")).toHaveLength(1);
    expect(view.emitted("finish")).toEqual([[{ message: "feat: x", draftAtStart: "" }]]);
    view.unmount();
    wrapper = undefined;
    bridge.calls = [];

    const panel = await mountPanel();
    await pluginButton(panel).trigger("click");
    await settle();
    expect((textarea(panel).element as HTMLTextAreaElement).value).toBe("feat: x");
    expect(commandCalls("plugin_host_scm_commit_message")).toHaveLength(1);
    expect(commandCalls("git_commit")).toHaveLength(0);
    expect(commandCalls("git_commit_amend")).toHaveLength(0);
    expect(commandCalls("git_push")).toHaveLength(0);
  });

  it("edited draft is not overwritten", async () => {
    const pending = deferred<unknown>();
    bridge.answer = (command) => (command === "plugin_host_scm_commit_message" ? pending.promise : undefined);
    const panel = await mountPanel();
    await pluginButton(panel).trigger("click");
    await settle();
    await textarea(panel).setValue("wip");
    pending.resolve({ message: "feat: x", truncated: false, diffTruncated: false, omittedFiles: 0 });
    await settle();
    expect((textarea(panel).element as HTMLTextAreaElement).value).toBe("wip");
    expect(toast.messages).toContainEqual(["warning", t("git.pluginGenerateDraftChanged")]);
    expect(commandCalls("git_commit")).toHaveLength(0);
  });

  it("disabled while AI generation runs", async () => {
    const view = mountButton({ busy: true });
    expect(pluginButton(view).attributes("disabled")).toBeDefined();
    await pluginButton(view).trigger("click");
    await settle();
    expect(commandCalls("plugin_host_scm_commit_message")).toHaveLength(0);
    view.unmount();
    wrapper = undefined;

    const pending = deferred<unknown>();
    bridge.answer = (command) => (command === "plugin_host_scm_commit_message" ? pending.promise : undefined);
    const panel = await mountPanel();
    await textarea(panel).setValue("feat: ready");
    expect(buttonByText(panel, t("git.commit")).attributes("disabled")).toBeUndefined();
    await pluginButton(panel).trigger("click");
    await settle();
    expect(buttonByText(panel, t("git.aiGenerate")).attributes("disabled")).toBeDefined();
    expect(buttonByText(panel, t("git.commit")).attributes("disabled")).toBeDefined();
    expect(buttonByText(panel, t("git.commitAndPush")).attributes("disabled")).toBeDefined();
    pending.resolve({ message: "feat: x" });
    await settle();
  });

  it("second click cancels", async () => {
    const pending = deferred<unknown>();
    bridge.answer = (command) => (command === "plugin_host_scm_commit_message" ? pending.promise : undefined);
    const panel = await mountPanel();
    await textarea(panel).setValue("draft before");
    await pluginButton(panel).trigger("click");
    await settle();
    await pluginButton(panel).trigger("click");
    await settle();
    const [[, request]] = commandCalls("plugin_host_scm_commit_message");
    expect(commandCalls("plugin_host_contribution_cancel")).toEqual([
      ["plugin_host_contribution_cancel", { requestId: request.requestId }],
    ]);
    pending.resolve({ message: "feat: late" });
    await settle();
    expect((textarea(panel).element as HTMLTextAreaElement).value).toBe("draft before");
    expect(buttonByText(panel, t("git.aiGenerate")).attributes("disabled")).toBeUndefined();
  });

  it("several providers show a menu", async () => {
    const second: PluginScmProvider = { pluginId: "zeta.notes", pluginName: "Zeta", id: "short", title: "Short", activeInWorkspace: true };
    bridge.answer = (command) =>
      command === "plugin_host_scm_commit_message" ? Promise.resolve({ message: "docs: y" }) : undefined;
    const view = mountButton({ providers: [provider, second] });
    const items = view.findAll("[role=menuitem]");
    expect(items.map((item) => item.text())).toEqual(["工单格式 · Ticket", "Short · Zeta"]);
    view.findComponent(Dropdown).vm.$emit("command", "zeta.notes/short");
    await settle();
    const [[, args]] = commandCalls("plugin_host_scm_commit_message");
    expect(args).toMatchObject({ pluginId: "zeta.notes", providerId: "short" });
    expect(view.emitted("finish")).toEqual([[{ message: "docs: y", draftAtStart: "" }]]);
  });

  it("no staged changes warns", async () => {
    const view = mountButton({ hasStaged: false });
    await pluginButton(view).trigger("click");
    await settle();
    expect(toast.messages).toEqual([["warning", t("git.pluginGenerateRequiresStaged")]]);
    expect(t("git.pluginGenerateRequiresStaged")).not.toMatch(/AI/);
    expect(commandCalls("plugin_host_scm_commit_message")).toHaveLength(0);
    expect(view.emitted("start")).toBeUndefined();
  });

  it("reports errors and notices without touching the draft", async () => {
    bridge.answer = (command) =>
      command === "plugin_host_scm_commit_message" ? Promise.reject(new Error("plugin_error: bad answer")) : undefined;
    const view = mountButton({ draft: "keep" });
    await pluginButton(view).trigger("click");
    await settle();
    expect(view.emitted("finish")).toEqual([[{ draftAtStart: "keep" }]]);
    expect(toast.messages).toEqual([["error", t("git.pluginGenerateFailed", { reason: "plugin_error: bad answer" })]]);

    toast.messages = [];
    bridge.answer = (command) =>
      command === "plugin_host_scm_commit_message"
        ? Promise.resolve({ message: "feat: z", diffTruncated: true, omittedFiles: 2 })
        : undefined;
    await pluginButton(view).trigger("click");
    await settle();
    expect(toast.messages).toEqual([
      ["info", t("git.pluginGenerateOmitted", { count: 2 })],
      ["info", t("git.pluginGenerateTruncated")],
    ]);
    // Omitted files are not all sensitive (paths too long or unrepresentable too).
    expect(t("git.pluginGenerateOmitted", { count: 2 })).not.toMatch(/敏感|sensitive/i);
  });

  it("provider disappearing mid-run resets the panel", async () => {
    const pending = deferred<unknown>();
    bridge.answer = (command) => (command === "plugin_host_scm_commit_message" ? pending.promise : undefined);
    const panel = await mountPanel();
    await textarea(panel).setValue("draft before");
    await pluginButton(panel).trigger("click");
    await settle();
    expect(buttonByText(panel, t("git.aiGenerate")).attributes("disabled")).toBeDefined();
    expect(buttonByText(panel, t("git.commit")).attributes("disabled")).toBeDefined();

    usePluginDataProvidersStore().setScmProviders([]);
    await settle();
    expect(panel.find('[data-testid="git-plugin-generate"]').exists()).toBe(false);
    expect(buttonByText(panel, t("git.aiGenerate")).attributes("disabled")).toBeUndefined();
    expect(buttonByText(panel, t("git.commit")).attributes("disabled")).toBeUndefined();
    const [[, request]] = commandCalls("plugin_host_scm_commit_message");
    expect(commandCalls("plugin_host_contribution_cancel")).toEqual([
      ["plugin_host_contribution_cancel", { requestId: request.requestId }],
    ]);
    pending.resolve({ message: "feat: late" });
    await settle();
    expect((textarea(panel).element as HTMLTextAreaElement).value).toBe("draft before");
    expect(commandCalls("git_commit")).toHaveLength(0);
  });

  it("forbidden and timeout errors are localised", async () => {
    const view = mountButton();
    for (const [code, key] of [["forbidden", "git.pluginGenerateForbidden"], ["timeout", "git.pluginGenerateTimeout"]] as const) {
      toast.messages = [];
      bridge.answer = (command) =>
        command === "plugin_host_scm_commit_message" ? Promise.reject(new Error(`${code}: detail`)) : undefined;
      await pluginButton(view).trigger("click");
      await settle();
      expect(toast.messages).toEqual([["error", t("git.pluginGenerateFailed", { reason: t(key) })]]);
    }
  });

  it("focus follows the menu and cancel buttons", async () => {
    const pending = deferred<unknown>();
    bridge.answer = (command) => (command === "plugin_host_scm_commit_message" ? pending.promise : undefined);
    const second: PluginScmProvider = { pluginId: "zeta.notes", pluginName: "Zeta", id: "short", title: "Short", activeInWorkspace: true };
    const view = mountButton({ providers: [provider, second] });
    (pluginButton(view).element as HTMLButtonElement).focus();
    view.findComponent(Dropdown).vm.$emit("command", "acme.ticket/conventional");
    await settle();
    const cancelButton = pluginButton(view);
    expect(cancelButton.text()).toBe(t("common.cancel"));
    expect(document.activeElement).toBe(cancelButton.element);
    await cancelButton.trigger("click");
    await settle();
    expect(view.emitted("cancel")).toHaveLength(1);
    expect(pluginButton(view).attributes("aria-haspopup")).toBe("menu");
    expect(document.activeElement).toBe(pluginButton(view).element);
    pending.resolve({ message: "feat: late" });
    await settle();
    expect(view.emitted("finish")).toBeUndefined();
  });
});
