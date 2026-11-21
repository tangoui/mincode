import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import SshWorkspaceView from "@/views/SshWorkspaceView.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import { parseWhenExpression } from "@/domain/commands/when";
import type { PluginMenuItem } from "@/domain/plugins/menus";
import type { SshConnectionProfile } from "@/domain/ssh/types";
import { pluginMenuItems } from "@/services/contributions/menus";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";

const plugin = vi.hoisted(() => ({ execute: vi.fn() }));
vi.mock("@/services/plugin-commands", async () => {
  const { shallowRef } = await import("vue");
  return {
    pluginCommandState: shallowRef({ commands: ["inspect", "jump", "busy"].map((id) => ({ pluginId: "demo.menus", id })), contexts: {} }),
    executePluginCommand: (...args: unknown[]) => plugin.execute(...args),
    setPluginCommandPaletteOverrides: () => {},
  };
});

const profiles = vi.hoisted(() => [] as SshConnectionProfile[]);
const storedSessions = vi.hoisted(() => ({ value: [] as unknown[] }));

vi.mock("vue-router", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/services/persistent-store", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/persistent-store")>()),
  loadPersistedValue: vi.fn(async (key: string) => (key === "app_ssh_workspace_sessions_v1" ? storedSessions.value : undefined)),
  savePersistedValue: vi.fn(async () => undefined),
  deletePersistedValue: vi.fn(async () => undefined),
}));
vi.mock("@/composables/use-workspace-state-scope", async () => {
  const { ref } = await import("vue");
  return {
    useWorkspaceStateScope: () => ({
      scopeId: ref("scope"),
      capture: () => ({ scopeId: "scope", workspaceId: "workspace" }),
    }),
  };
});
vi.mock("@/stores/ssh-connections", async () => {
  const { reactive } = await import("vue");
  const store = reactive({
    hydrated: true,
    hydrate: vi.fn(async () => undefined),
    profiles,
    sortedProfiles: profiles,
    groupOrder: [] as string[],
    findProfile: (id: string) => profiles.find((item) => item.id === id),
    credentialsFor: () => undefined,
  });
  return { useSshConnectionsStore: () => store };
});
vi.mock("@/stores/ssh-recent", () => ({
  useSshRecentStore: () => ({
    hydrated: true,
    hydrate: vi.fn(async () => undefined),
    openedAt: {},
    clearedAt: 0,
    markOpened: vi.fn(),
    prune: vi.fn(),
    clearRecent: vi.fn(),
  }),
}));

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
// The shared "⋯" menu (one per page) renders its items inline.
const ContextMenu = defineComponent({
  name: "AppContextMenuStub",
  emits: ["close"],
  setup(_, { slots }) {
    return () => h("ul", { class: "context-menu-stub" }, slots.default?.());
  },
});
const DropdownMenu = defineComponent({
  setup(_, { slots }) {
    return () => h("ul", slots.default?.());
  },
});

function profile(overrides: Partial<SshConnectionProfile>): SshConnectionProfile {
  return {
    id: "web",
    name: "Web 1",
    host: "web1.internal",
    port: 2222,
    username: "deploy",
    authType: "key",
    keyFile: "/Users/me/.ssh/id_secret",
    description: "private note",
    defaultPath: "/srv/app",
    postConnectCommands: ["export TOKEN=abc123"],
    group: "Prod",
    tags: ["tag-secret"],
    favorite: false,
    saveCredentials: true,
    portForwards: [{ id: "f1", type: "L", bindPort: 15432, targetHost: "db.hidden", targetPort: 5432 }],
    jumpHost: { host: "bastion.hidden", port: 22, username: "jumper", authType: "key", keyFile: "/Users/me/.ssh/jump_key" },
    socksProxy: { host: "socks.hidden", port: 1080, username: "proxyuser" },
    fileTransfer: { enabled: true, protocol: "ftp", host: "ftp.hidden", port: 21, username: "ftpuser", viaCurrentSsh: false },
    createdAt: 0,
    updatedAt: 0,
    ...overrides,
  };
}

const MENU_ITEM: PluginMenuItem = {
  pluginId: "demo.menus",
  pluginName: "Demo Menus",
  menu: "ssh/connection/context",
  command: "inspect",
  title: "Inspect host",
  whenClauses: [],
  group: "",
  order: 0,
  passContext: true,
  hidden: false,
};

let wrapper: VueWrapper | undefined;

function mountView() {
  return mount(SshWorkspaceView, {
    global: {
      stubs: {
        ElDropdown: Dropdown,
        "el-dropdown": Dropdown,
        ElDropdownMenu: DropdownMenu,
        "el-dropdown-menu": DropdownMenu,
        ElDropdownItem: DropdownItem,
        "el-dropdown-item": DropdownItem,
        SshTerminalPaneTree: true,
        SshServerOverview: true,
        SshConnectionDialog: true,
        SshKnownHostsDialog: true,
        SshConfigSyncDialog: true,
        AppContextMenu: ContextMenu,
      },
    },
  });
}

function rowOf(name: string) {
  return wrapper!.findAll(".db-connection-entry").find((row) => row.find(".db-connection-node__meta strong").text() === name)!;
}

/** Opens the shared "⋯" menu for a row (the page renders one menu, anchored at that row). */
async function openMenu(name: string) {
  await rowOf(name).get(".db-connection-node__menu").trigger("click");
  await flushPromises();
  openedName = name;
}

async function closeMenu() {
  wrapper!.findComponent(ContextMenu).vm.$emit("close");
  await flushPromises();
  openedName = "";
}

function sharedMenu() {
  return wrapper!.findComponent(ContextMenu);
}

/** Plugin menu host of the open shared menu when it belongs to `name`'s row, else null. */
function pluginHostFor(name: string) {
  const menu = sharedMenu();
  if (!menu.exists()) return null;
  // One menu serves every row; openMenu() records which row's "⋯" opened it.
  const items = menu.findComponent(PluginMenuItems);
  if (!items.exists() || openedName !== name) return null;
  return items.props("host");
}

let openedName = "";

async function openAndRun(name: string) {
  await openMenu(name);
  const last = sharedMenu().findAll("[role=menuitem]").at(-1)!;
  expect(last.text()).toBe("Inspect host");
  expect(last.attributes("divided")).toBeDefined();
  await last.trigger("click");
  await flushPromises();
  const [, , invocation] = plugin.execute.mock.calls.at(-1)! as [string, string, { menu: string; context: Record<string, unknown> }];
  expect(invocation.menu).toBe("ssh/connection/context");
  const serialized = JSON.stringify(invocation.context);
  for (const secret of ["id_secret", "jump_key", "bastion", "jumper", "socks", "proxyuser", "TOKEN", "abc123", "private note", "db.hidden", "15432", "ftp", "tag-secret", "/srv/app"]) {
    expect(serialized).not.toContain(secret);
  }
  return invocation.context;
}

describe("SshWorkspaceView connection plugin menus", () => {
  beforeEach(() => {
    plugin.execute.mockReset();
    profiles.splice(0, profiles.length,
      profile({}),
      profile({ id: "db", name: "DB 1", group: undefined, authType: "agent", jumpHost: undefined, host: "db1.internal", port: 22, username: "root" }),
    );
    storedSessions.value = [];
    pluginMenuItems.value = [MENU_ITEM];
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
    pluginMenuItems.value = [];
  });

  it("adds plugin items to a connection's menu with only the whitelisted context", async () => {
    wrapper = mountView();
    await flushPromises();
    // No menu is mounted until one is opened: nothing is exposed to plugins.
    expect(sharedMenu().exists()).toBe(false);

    expect(await openAndRun("Web 1")).toEqual({
      connectionId: "web",
      name: "Web 1",
      group: "Prod",
      host: "web1.internal",
      port: 2222,
      username: "deploy",
    });
    expect(pluginHostFor("Web 1")).toEqual({ "ssh.status": "idle", "ssh.authType": "key", "ssh.hasJumpHost": true });
    expect(pluginHostFor("DB 1")).toBeNull();

    // Running the item closed the menu; closing explicitly is also clean.
    if (sharedMenu().exists()) await closeMenu();
    expect(sharedMenu().exists()).toBe(false);

    expect(await openAndRun("DB 1")).toEqual({ connectionId: "db", name: "DB 1", host: "db1.internal", port: 22, username: "root" });
  });

  it("offers new and view linked notes for the connection", async () => {
    wrapper = mountView();
    await flushPromises();
    const drawer = useLinkedNotesDrawer();
    const open = vi.spyOn(drawer, "open").mockResolvedValue();

    await openMenu("Web 1");
    const items = sharedMenu().findAll("[role=menuitem]");
    const create = items.find((item) => item.text() === "新建关联笔记")!;
    expect(items.some((item) => item.text() === "查看关联笔记（0）")).toBe(true);
    await create.trigger("click");
    await flushPromises();
    expect(open).toHaveBeenCalledWith({ objectType: "ssh", objectId: "web", nameSnapshot: "Web 1" }, { createNew: true });
    expect(sharedMenu().exists()).toBe(false);
  });

  it("evaluates when clauses against ssh.authType, ssh.hasJumpHost and ssh.status", async () => {
    pluginMenuItems.value = [
      { ...MENU_ITEM, whenClauses: parseWhenExpression('ssh.authType == "agent"') },
      { ...MENU_ITEM, command: "jump", title: "Via jump host", whenClauses: parseWhenExpression("ssh.hasJumpHost") },
      { ...MENU_ITEM, command: "busy", title: "While connecting", whenClauses: parseWhenExpression('ssh.status == "connecting"') },
    ];
    storedSessions.value = [
      { id: "tab-1", workspaceScopeId: "scope", activePaneId: "pane-1", root: { id: "pane-1", type: "terminal", profileId: "web" } },
    ];
    wrapper = mountView();
    await flushPromises();

    await openMenu("Web 1");
    expect(pluginHostFor("Web 1")).toMatchObject({ "ssh.status": "connecting" });
    expect(sharedMenu().findAll(".app-plugin-menu-item").map((item) => item.text())).toEqual(["Via jump host", "While connecting"]);

    await closeMenu();
    await openMenu("DB 1");
    expect(sharedMenu().findAll(".app-plugin-menu-item").map((item) => item.text())).toEqual(["Inspect host"]);
  });

  it("drops the open target when the row disappears without a close event", async () => {
    wrapper = mountView();
    await flushPromises();
    await openMenu("Web 1");
    expect(pluginHostFor("Web 1")).not.toBeNull();

    // Collapse the "Prod" group: the row (and its open menu) unmounts.
    await wrapper.findAll(".db-connection-group__title").find((title) => title.text().includes("Prod"))!.trigger("click");
    await flushPromises();
    await wrapper.findAll(".db-connection-group__title").find((title) => title.text().includes("Prod"))!.trigger("click");
    await flushPromises();
    expect(sharedMenu().exists()).toBe(false);
    expect(pluginHostFor("Web 1")).toBeNull();
  });

  it("renders only the sidebar rows near the viewport for many connections", async () => {
    profiles.splice(0, profiles.length, ...Array.from({ length: 500 }, (_, i) => profile({ id: `p${i}`, name: `Server ${i}` })));
    wrapper = mountView();
    await flushPromises();
    const wrap = wrapper.get(".db-tree-scroll .el-scrollbar__wrap").element as HTMLElement;
    Object.defineProperty(wrap, "clientHeight", { configurable: true, value: 500 });
    wrap.dispatchEvent(new Event("scroll"));
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await flushPromises();
    const rows = wrapper.findAll(".db-connection-entry").length;
    expect(rows).toBeGreaterThan(0);
    expect(rows).toBeLessThan(150);
  });

  it("windows many small groups once their total is large", async () => {
    // 10 groups × 50 connections: each group alone is short, together they are not.
    profiles.splice(0, profiles.length, ...Array.from({ length: 500 }, (_, i) => profile({ id: `g${i}`, name: `Server ${i}`, group: `group-${i % 10}` })));
    wrapper = mountView();
    await flushPromises();
    const wrap = wrapper.get(".db-tree-scroll .el-scrollbar__wrap").element as HTMLElement;
    Object.defineProperty(wrap, "clientHeight", { configurable: true, value: 500 });
    wrap.getBoundingClientRect = () => ({ top: 0, left: 0, right: 300, bottom: 500, width: 300, height: 500, x: 0, y: 0, toJSON: () => ({}) });
    // happy-dom has no layout: stack the groups (header + 50 rows of 44px each) as the real list does.
    wrapper.findAll("[data-row-group]").forEach((group, index) => {
      const top = 30 + index * (30 + 50 * 44);
      group.element.getBoundingClientRect = () => ({ top, left: 0, right: 300, bottom: top, width: 300, height: 0, x: 0, y: top, toJSON: () => ({}) });
    });
    for (const row of wrapper.findAll(".db-connection-entry")) Object.defineProperty(row.element, "offsetHeight", { configurable: true, value: 44 });
    wrap.dispatchEvent(new Event("scroll"));
    await new Promise((resolve) => requestAnimationFrame(resolve));
    await flushPromises();
    // The first group fills the viewport; groups below it mount only a few rows each.
    expect(wrapper.findAll(".db-connection-entry").length).toBeLessThan(150);
  });

  it("renders one shared menu, not a dropdown per connection", async () => {
    profiles.splice(0, profiles.length, ...Array.from({ length: 40 }, (_, i) => profile({ id: `p${i}`, name: `Server ${i}` })));
    wrapper = mountView();
    await flushPromises();
    expect(wrapper.findAll(".db-connection-entry")).toHaveLength(40);
    expect(wrapper.findAllComponents(Dropdown).filter((dropdown) => dropdown.element.closest(".db-connection-entry"))).toHaveLength(0);
    expect(wrapper.findAllComponents(PluginMenuItems).filter((menu) => menu.props("menu") === "ssh/connection/context")).toHaveLength(0);
    await openMenu("Server 7");
    expect(wrapper.findAllComponents(PluginMenuItems).filter((menu) => menu.props("menu") === "ssh/connection/context")).toHaveLength(1);
  });
});
