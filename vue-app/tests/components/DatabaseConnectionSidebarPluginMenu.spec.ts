import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import DatabaseConnectionSidebar from "@/components/database/sidebar/DatabaseConnectionSidebar.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import type { DatabaseConnection } from "@/domain/database/types";
import type { PluginMenuItem } from "@/domain/plugins/menus";
import { pluginMenuItems } from "@/services/contributions/menus";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

const plugin = vi.hoisted(() => ({ execute: vi.fn() }));
vi.mock("@/services/plugin-commands", async () => {
  const { shallowRef } = await import("vue");
  return {
    pluginCommandState: shallowRef({ commands: [{ pluginId: "demo.menus", id: "inspect" }], contexts: {} }),
    executePluginCommand: (...args: unknown[]) => plugin.execute(...args),
    setPluginCommandPaletteOverrides: () => {},
  };
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

function connection(overrides: Partial<DatabaseConnection>): DatabaseConnection {
  return {
    id: "pg",
    name: "Orders DB",
    dbType: "postgres",
    host: "db.internal",
    port: 5432,
    username: "admin",
    password: "hunter2",
    database: "orders",
    filePath: "/secret/path.db",
    token: "tok",
    ssl: false,
    savePassword: true,
    createdAt: 0,
    updatedAt: 0,
    ...overrides,
  };
}

const MENU_ITEM: PluginMenuItem = {
  pluginId: "demo.menus",
  pluginName: "Demo Menus",
  menu: "database/tree/context",
  command: "inspect",
  title: "Inspect object",
  whenClauses: [],
  group: "",
  order: 0,
  passContext: true,
  hidden: false,
};

let wrapper: VueWrapper | undefined;

function mountSidebar() {
  return mount(DatabaseConnectionSidebar, {
    props: {
      explorerCollapsed: false,
      activeConnection: undefined,
      connectingConnectionIds: {},
      selectedDatabase: undefined,
      selectedTable: undefined,
      savedScripts: [],
      activeConnectionHistory: [],
    },
    global: {
      stubs: {
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

function pluginMenus() {
  return wrapper!.findAllComponents(PluginMenuItems);
}

/** The "⋯" dropdown stub of the connection row with this name. */
function connectionDropdown(name: string) {
  return wrapper!.findAll(".db-connection-node").find((row) => row.find(".db-connection-node__meta").text().includes(name))!.findComponent(Dropdown);
}

interface SidebarExpose {
  setConnectionExpandedById: (id: string, expanded: boolean) => void;
  setDatabaseExpanded: (connection: DatabaseConnection, database: string, expanded: boolean) => void;
}

async function openAndRun(dropdown: VueWrapper) {
  dropdown.vm.$emit("visible-change", true);
  await flushPromises();
  const items = dropdown.findAll("[role=menuitem]");
  const last = items.at(-1)!;
  expect(last.text()).toBe("Inspect object");
  expect(last.attributes("divided")).toBeDefined();
  await last.trigger("click");
  await flushPromises();
  const [, , invocation] = plugin.execute.mock.calls.at(-1)! as [string, string, { menu: string; context: Record<string, unknown> }];
  expect(invocation.menu).toBe("database/tree/context");
  const serialized = JSON.stringify(invocation.context);
  for (const secret of ["db.internal", "hunter2", "admin", "/secret/path.db", "tok", "5432"]) {
    expect(serialized).not.toContain(secret);
  }
  expect(invocation.context).not.toHaveProperty("host");
  expect(invocation.context).not.toHaveProperty("password");
  expect(invocation.context).not.toHaveProperty("filePath");
  return invocation.context;
}

describe("DatabaseConnectionSidebar plugin menus", () => {
  beforeEach(() => {
    plugin.execute.mockReset();
    pluginMenuItems.value = [MENU_ITEM];
    const store = useDatabaseWorkspaceStore();
    store.connections = [connection({}), connection({ id: "cache", name: "Cache", dbType: "redis", port: 6379 })];
    store.schemas = { pg: { databases: [{ name: "orders", tables: [{ name: "Invoices", kind: "VIEW" }] }] } };
    store.sessionIds = { pg: "session-1" };
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
    pluginMenuItems.value = [];
  });

  it("offers linked-note commands on the connection menu", async () => {
    wrapper = mountSidebar();
    await flushPromises();
    const dropdown = connectionDropdown("Orders DB");
    const items = dropdown.findAll("[role=menuitem]");
    expect(items.find((item) => item.attributes("command") === "notes-new")?.text()).toBe("新建关联笔记");
    expect(items.find((item) => item.attributes("command") === "notes-view")?.text()).toBe("查看关联笔记（0）");

    dropdown.vm.$emit("command", "notes-view");
    expect(wrapper.emitted("connection-menu")?.at(-1)).toEqual([expect.objectContaining({ id: "pg" }), "notes-view"]);
  });

  it("adds plugin items to the connection, database and table menus with whitelisted context", async () => {
    wrapper = mountSidebar();
    await flushPromises();
    for (const menu of pluginMenus()) expect(menu.props("host")).toBeNull();

    const connectionMenu = connectionDropdown("Orders DB");
    expect(await openAndRun(connectionMenu)).toEqual({
      connection: { id: "pg", name: "Orders DB", kind: "postgres" },
      objectType: "connection",
    });

    // Expand the connection and the database to reach the database and table rows.
    const sidebar = wrapper.vm as unknown as SidebarExpose;
    sidebar.setConnectionExpandedById("pg", true);
    sidebar.setDatabaseExpanded(useDatabaseWorkspaceStore().connections[0]!, "orders", true);
    await flushPromises();

    const databaseMenu = wrapper.get(".db-tree-database__header").findComponent(Dropdown);
    const databaseHost = () => pluginMenus().find((menu) => menu.props("host")?.["database.objectType"] === "database");
    databaseMenu.vm.$emit("visible-change", true);
    await flushPromises();
    expect(databaseHost()?.props("host")).toEqual({
      "database.kind": "postgres",
      "database.objectType": "database",
      "database.tableKind": "",
      "database.connected": true,
    });
    expect(await openAndRun(databaseMenu)).toEqual({
      connection: { id: "pg", name: "Orders DB", kind: "postgres" },
      objectType: "database",
      database: "orders",
    });
    databaseMenu.vm.$emit("visible-change", false);
    await flushPromises();
    for (const menu of pluginMenus()) expect(menu.props("host")).toBeNull();

    // The table menu mounts lazily on the first click of its trigger.
    await wrapper.get(".db-table-row .db-tree-node__menu").trigger("click");
    await flushPromises();
    const tableMenu = wrapper.get(".db-table-row").findComponent(Dropdown);
    tableMenu.vm.$emit("visible-change", true);
    await flushPromises();
    expect(pluginMenus().find((menu) => menu.props("host") !== null)?.props("host")).toMatchObject({
      "database.objectType": "table",
      "database.tableKind": "view",
    });
    expect(await openAndRun(tableMenu)).toEqual({
      connection: { id: "pg", name: "Orders DB", kind: "postgres" },
      objectType: "table",
      database: "orders",
      table: { name: "Invoices", kind: "view" },
    });
  });

  it("offers only the connection-level menu for a Redis connection and uses the SQL editor dialect as database.kind", async () => {
    const store = useDatabaseWorkspaceStore();
    const dialect = vi.spyOn(store, "connectionDialectType").mockImplementation((target) => (target.id === "pg" ? "mysql" : target.dbType));
    wrapper = mountSidebar();
    await flushPromises();

    const redisMenu = connectionDropdown("Cache");
    redisMenu.vm.$emit("visible-change", true);
    await flushPromises();
    expect(pluginMenus().find((menu) => menu.props("host") !== null)?.props("host")).toEqual({
      "database.kind": "redis",
      "database.objectType": "connection",
      "database.tableKind": "",
      "database.connected": false,
    });
    expect(await openAndRun(redisMenu)).toEqual({
      connection: { id: "cache", name: "Cache", kind: "redis" },
      objectType: "connection",
    });

    // Redis has no object tree, so no database or table rows (and no menus) appear under it.
    (wrapper.vm as unknown as SidebarExpose).setConnectionExpandedById("cache", true);
    await flushPromises();
    expect(pluginMenus()).toHaveLength(2);

    const pgMenu = connectionDropdown("Orders DB");
    expect(await openAndRun(pgMenu)).toMatchObject({ connection: { kind: "mysql" } });
    expect(dialect).toHaveBeenCalled();
  });
});
