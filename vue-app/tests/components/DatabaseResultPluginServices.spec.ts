import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h, reactive } from "vue";
import { ElMessage } from "element-plus";
import DatabaseResultPane from "@/components/database/results/DatabaseResultPane.vue";
import { dbResultServiceTarget, type DbResultGrid, type DbResultMenuSource } from "@/domain/database/result-service-target";
import type { PluginServiceEntry } from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";

const services = vi.hoisted(() => ({ run: vi.fn(), entries: undefined as unknown as import("vue").ShallowRef<unknown[]> }));
vi.mock("@/services/plugin-services", async (original) => {
  const actual = await original<typeof import("@/services/plugin-services")>();
  const { shallowRef } = await import("vue");
  services.entries = shallowRef<unknown[]>([]);
  return { ...actual, pluginServices: services.entries, runPluginService: (...args: unknown[]) => services.run(...args) };
});

vi.mock("element-plus", async (original) => {
  const actual = await original<typeof import("element-plus")>();
  const message = Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() });
  return { ...actual, ElMessage: message };
});

// The workspace store as the lazy service menu reads it: the saved connections (with fields that
// must never reach a plugin), their dialect and schemas, and the loading state. activeConnection is
// the sidebar's current one, which the menu must not use.
const store = vi.hoisted(() => ({
  connections: [] as Record<string, unknown>[],
  activeConnection: undefined as Record<string, unknown> | undefined,
  schemas: {} as Record<string, unknown>,
  loading: false,
  queryRunning: false,
  connectionDialectType: (connection: { dbType: string }) => connection.dbType,
}));
vi.mock("@/stores/database-workspace", () => ({ useDatabaseWorkspaceStore: () => store }));

// AppContextMenu and the Element Plus items need a live popper; these stubs render the menu inline.
const ContextMenu = defineComponent({
  name: "AppContextMenuStub",
  emits: ["close"],
  setup(_, { slots }) {
    return () => h("ul", { class: "context-menu-stub" }, slots.default?.());
  },
});
const DropdownItem = defineComponent({
  inheritAttrs: false,
  props: { icon: { type: [Object, Function], default: undefined } },
  setup(_, { attrs, slots }) {
    return () => h("li", { role: "menuitem", tabindex: -1, ...attrs }, slots.default?.());
  },
});
const stubs = {
  AppContextMenu: ContextMenu,
  ElDropdownItem: DropdownItem,
  "el-dropdown-item": DropdownItem,
  VirtualResultGrid: true,
  TableV2ResultGrid: true,
  GridExportSubmenu: true,
  GridFilterSubmenu: true,
};

const { t } = useVueI18n();
const MENU = t("plugins.services.menu");

function entry(overrides: Partial<PluginServiceEntry> = {}): PluginServiceEntry {
  return {
    pluginId: "demo.data",
    pluginName: "Demo Data",
    entryId: "chart",
    service: "data.chart",
    title: "Chart",
    description: "",
    input: { kind: "file", accepts: [".json"], maxBytes: 1048576 },
    output: { kind: "text" },
    surfaces: ["dbResult"],
    risk: "read",
    timeoutMs: 30000,
    isDefault: false,
    ...overrides,
  };
}

// The grid as the database workspace holds it: result columns id, email, secret; "secret" hidden,
// email shown first (reordered), rows sorted by id descending.
const resultRows = [
  [1, "a@example.com", "s1"],
  [2, "b@example.com", "s2"],
  [3, null, "s3"],
];
const visibleColumns = [
  { name: "email", index: 1, dataType: "varchar" },
  { name: "id", index: 0 },
];
const displayRows = [2, 1, 0].map((originalIndex) => ({ row: resultRows[originalIndex]!, originalIndex }));
const gridState = {
  filterText: "",
  filters: { logic: "and", conditions: [] },
  sort: { column: "id", direction: "desc" },
  hiddenColumns: ["secret"],
  columnOrder: ["email", "id", "secret"],
  layout: "horizontal",
  frozenColumnCount: 0,
};
const connection = {
  id: "conn-1",
  name: "Prod",
  dbType: "mysql",
  host: "db.internal.example",
  port: 3306,
  username: "root",
  password: "db-secret-password",
  filePath: "/Users/me/secret.sqlite",
};
// The connection the sidebar switched to after the result was loaded.
const otherConnection = { id: "conn-2", name: "Analytics", dbType: "postgres", host: "pg.internal", password: "pg-secret" };

let wrapper: VueWrapper | undefined;

type PaneProps = Partial<DbResultGrid> & {
  activeQueryRunning?: boolean;
  contextMenu?: unknown;
  serviceSource?: () => DbResultMenuSource | null;
};

function mountPane(overrides: PaneProps = {}) {
  wrapper = mount(DatabaseResultPane, {
    attachTo: document.body,
    props: {
      object: "table",
      selectedTable: "users",
      resultRows,
      displayRows,
      columns: visibleColumns,
      gridState,
      formatCell: (value: unknown) => (value == null ? "NULL" : String(value)),
      selectedRowIndexes: [],
      allRowsSelected: false,
      someRowsSelected: false,
      activeQueryRunning: false,
      resultRowCountText: "3",
      page: 1,
      pageSize: 100,
      primaryKeys: [],
      canEditRows: false,
      canInsertRows: false,
      canNextPage: true,
      canLastPage: false,
      lastPageLoading: false,
      supportsOffsetTablePreview: true,
      desktopRuntime: true,
      isPinecone: false,
      contextMenu: { x: 10, y: 10, rowIndex: 0, column: "email", value: "a@example.com" },
      contextCanCopyInsertSql: false,
      contextCanEditCell: false,
      contextCanMutateColumn: false,
      contextCanSetNull: false,
      contextSelectedRowCount: 1,
      headerCanCopyAlterSql: false,
      targetPage: 1,
      pineconeNamespaceDraft: "",
      serviceSource: () => ({ connectionId: "conn-1", database: "shop" }),
      ...overrides,
    } as never,
    global: { stubs },
  });
  return wrapper;
}

async function settle() {
  await vi.dynamicImportSettled();
  await flushPromises();
}

function menu() {
  return wrapper!.findComponent(ContextMenu);
}

async function expandServices() {
  await settle();
  await menu().get(`[aria-label="${MENU}"]`).trigger("click");
  return menu().findAll(".app-plugin-menu-item--nested");
}

function lastRequest() {
  return services.run.mock.calls.at(-1)![0] as { service: string; surface: string; content: { text: string }; surfaceContext?: Record<string, unknown> };
}

beforeEach(async () => {
  // Loads the mocked module (and its test-owned provider list) before any lazy menu does.
  await import("@/services/plugin-services");
  services.entries.value = [entry()];
  services.run.mockReset();
  vi.mocked(ElMessage.info).mockClear();
  Object.assign(store, {
    connections: [connection, otherConnection],
    activeConnection: connection,
    schemas: {
      "conn-1": { databases: [{ name: "shop", tables: [{ name: "users", kind: "VIEW" }] }] },
      "conn-2": { databases: [{ name: "warehouse", tables: [{ name: "users", kind: "TABLE" }] }] },
    },
    loading: false,
    queryRunning: false,
  });
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  services.entries.value = [];
});

describe("DatabaseResultPane plugin services (dbResult)", () => {
  it("adds 'Process with plugin' after Refresh once the lazy menu has loaded", async () => {
    mountPane();
    await settle();
    const items = menu().findAll("[role=menuitem]");
    const labels = items.map((item) => item.text());
    const refresh = labels.indexOf(t("database.refresh"));
    expect(refresh).toBeGreaterThan(-1);
    expect(labels[refresh + 1]).toBe(MENU);
    expect(items[refresh + 1]!.attributes("divided")).toBeDefined();
  });

  it("hands over the visible columns and rows in grid order, with names only for the database", async () => {
    mountPane();
    const [item] = await expandServices();
    expect(item!.text()).toBe("Chart");
    await item!.trigger("click");
    expect(services.run).toHaveBeenCalledTimes(1);
    const request = lastRequest();
    expect(request.service).toBe("data.chart");
    expect(request.surface).toBe("dbResult");
    expect(JSON.parse(request.content.text)).toEqual({
      format: "mincode.dbResult",
      version: 1,
      columns: [{ name: "email", type: "varchar" }, { name: "id", type: null }],
      rows: [[null, 3], ["b@example.com", 2], ["a@example.com", 1]],
      rowCount: 3,
      truncated: false,
      hasMore: true,
      view: { filtered: false, sorted: true, hiddenColumns: 1 },
    });
    expect(request.content.text).not.toContain("s1");
    expect(request.surfaceContext).toEqual({
      database: { connection: { id: "conn-1", name: "Prod", kind: "mysql" }, database: "shop", table: { name: "users", kind: "view" } },
    });
    const context = JSON.stringify(request.surfaceContext);
    for (const leaked of ["host", "password", "filePath", "db.internal.example", "db-secret-password", "secret.sqlite", "root"]) {
      expect(context).not.toContain(leaked);
    }
    expect(wrapper!.emitted("close-context-menu")).toHaveLength(1);
    expect(ElMessage.info).not.toHaveBeenCalled();
  });

  it("names no table for a query result", async () => {
    mountPane({ object: "query", canNextPage: false, activeResult: { nextPageToken: "" } });
    const [item] = await expandServices();
    await item!.trigger("click");
    expect(JSON.parse(lastRequest().content.text)).toMatchObject({ hasMore: false });
    expect(lastRequest().surfaceContext).toEqual({ database: { connection: { id: "conn-1", name: "Prod", kind: "mysql" }, database: "shop" } });
  });

  it("greys the service out while the query runs or the table has unsaved changes", async () => {
    const cases: [() => PaneProps, "queryRunning" | "pendingChanges"][] = [
      [() => ({ object: "query", activeQueryRunning: true }), "queryRunning"],
      [() => {
        store.loading = true;
        return {};
      }, "queryRunning"],
      [() => {
        // A server-side search or filter page of the table.
        store.queryRunning = true;
        return {};
      }, "queryRunning"],
      [() => ({ pendingRows: new Map([[0, { kind: "updated" }]]) }), "pendingChanges"],
    ];
    for (const [setup, reason] of cases) {
      store.loading = false;
      store.queryRunning = false;
      mountPane(setup());
      const [item] = await expandServices();
      const text = t(`plugins.services.disabled.${reason}`);
      expect(item!.attributes("disabled")).toBeDefined();
      expect(item!.attributes("title")).toBe(text);
      expect(item!.text()).toContain(text);
      await item!.trigger("click");
      expect(services.run).not.toHaveBeenCalled();
      wrapper!.unmount();
      wrapper = undefined;
    }
  });

  it("cuts a result set over the provider's limit at whole rows and says so", async () => {
    services.entries.value = [entry({ input: { kind: "file", accepts: ["application/json"], maxBytes: 600 } })];
    const many = Array.from({ length: 50 }, (_, index) => ({ row: [index, `user${index}@example.com`], originalIndex: index }));
    mountPane({ displayRows: many, columns: [{ name: "id", index: 0 }, { name: "email", index: 1 }] });
    const [item] = await expandServices();
    await item!.trigger("click");
    const file = JSON.parse(lastRequest().content.text) as { rowCount: number; truncated: boolean; rows: unknown[][] };
    expect(file.truncated).toBe(true);
    expect(file.rowCount).toBeLessThan(50);
    expect(file.rows).toHaveLength(file.rowCount);
    expect(ElMessage.info).toHaveBeenCalledWith(expect.objectContaining({
      message: t("plugins.services.dbResultTruncated", { count: file.rowCount }),
    }));
  });

  it("names the connection a query ran on, not the one the sidebar switched to", async () => {
    store.activeConnection = otherConnection;
    mountPane({ object: "query", serviceSource: () => ({ connectionId: "conn-1", database: "shop" }) });
    const [item] = await expandServices();
    await item!.trigger("click");
    expect(lastRequest().surfaceContext).toEqual({ database: { connection: { id: "conn-1", name: "Prod", kind: "mysql" }, database: "shop" } });
  });

  it("names a table tab's own connection and database, not the sidebar's", async () => {
    store.activeConnection = connection;
    mountPane({ object: "table", selectedTable: "users", serviceSource: () => ({ connectionId: "conn-2", database: "warehouse" }) });
    const [item] = await expandServices();
    await item!.trigger("click");
    expect(lastRequest().surfaceContext).toEqual({
      database: { connection: { id: "conn-2", name: "Analytics", kind: "postgres" }, database: "warehouse", table: { name: "users", kind: "table" } },
    });
    expect(JSON.stringify(lastRequest().surfaceContext)).not.toMatch(/pg\.internal|pg-secret|Prod|shop/);
  });

  it("names no database when the result's source is unknown or no longer saved", async () => {
    for (const source of [{}, { connectionId: "conn-gone", database: "shop" }]) {
      mountPane({ object: "query", serviceSource: () => source });
      const [item] = await expandServices();
      await item!.trigger("click");
      expect(lastRequest()).not.toHaveProperty("surfaceContext");
      wrapper!.unmount();
      wrapper = undefined;
    }
  });

  it("leaves the services out with no rows, no match, or no open cell menu", async () => {
    mountPane({ displayRows: [] });
    await settle();
    expect(menu().find(`[aria-label="${MENU}"]`).exists()).toBe(false);
    wrapper!.unmount();

    services.entries.value = [entry({ input: { kind: "file", accepts: [".csv"], maxBytes: 1048576 } })];
    mountPane();
    await settle();
    expect(menu().find(`[aria-label="${MENU}"]`).exists()).toBe(false);
    wrapper!.unmount();

    services.entries.value = [entry()];
    mountPane({ serviceSource: () => null });
    await settle();
    expect(menu().find(`[aria-label="${MENU}"]`).exists()).toBe(false);
  });

  it("reads the rows of the grid as they are when a service is clicked", async () => {
    const rows = reactive([...displayRows]);
    mountPane({ displayRows: rows });
    const [item] = await expandServices();
    rows.splice(0, 1);
    await item!.trigger("click");
    expect(JSON.parse(lastRequest().content.text)).toMatchObject({ rowCount: 2, rows: [["b@example.com", 2], ["a@example.com", 1]] });
  });
});

describe("dbResultServiceTarget", () => {
  const grid: DbResultGrid = { object: "table", selectedTable: "users", resultRows, displayRows, columns: visibleColumns, gridState, formatCell: String, canNextPage: false };
  const source = { running: false, connection, kind: "mysql", database: "shop" };

  it("names the database object from what is open", () => {
    expect(dbResultServiceTarget({ ...grid, object: "query" }, source).database).toEqual({
      connection: { id: "conn-1", name: "Prod", kind: "mysql" },
      connected: true,
      objectType: "database",
      database: "shop",
    });
    expect(dbResultServiceTarget(grid, source).database?.table).toEqual({ name: "users", kind: "table" });
    expect(dbResultServiceTarget({ ...grid, object: "query" }, { ...source, database: undefined }).database?.objectType).toBe("connection");
    expect(dbResultServiceTarget(grid, { ...source, connection: undefined }).database).toBeUndefined();
    expect(dbResultServiceTarget(grid, { ...source, kind: "" }).database).toBeUndefined();
  });

  it("reports filtering: rows filtered out locally, a search text or filter conditions", () => {
    expect(dbResultServiceTarget(grid, source).view).toEqual({ filtered: false, sorted: true, hiddenColumns: 1 });
    expect(dbResultServiceTarget({ ...grid, displayRows: displayRows.slice(1) }, source).view.filtered).toBe(true);
    expect(dbResultServiceTarget({ ...grid, gridState: { ...gridState, filterText: " a " } }, source).view.filtered).toBe(true);
    expect(dbResultServiceTarget({ ...grid, gridState: { ...gridState, filters: { conditions: [{}] } } }, source).view.filtered).toBe(true);
  });
});
