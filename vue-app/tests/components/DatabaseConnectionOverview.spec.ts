import { afterEach, describe, expect, it, vi } from "vitest";
import { KeepAlive, computed, defineComponent, h } from "vue";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import DatabaseConnectionOverview from "@/components/database/DatabaseConnectionOverview.vue";
import type { DatabaseConnection } from "@/domain/database/types";

const persistence = vi.hoisted(() => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/i18n/messages", () => ({
  useVueI18n: () => ({
    // The "connected for" label is filled in by the component, so it needs its placeholder.
    t: (key: string) => (key === "database.overviewConnectedFor" ? "Connected {count} min" : key),
    intlLocale: computed(() => "en-US"),
  }),
}));
vi.mock("@/services/persistent-store", () => persistence);
vi.mock("@/composables/database/useDatabaseDriverRuntime", () => ({
  useDatabaseDriverLabels: () => ({
    driverUnavailable: () => false,
    connectionTypeLabel: (connection: DatabaseConnection) => connection.dbType,
  }),
}));

function connection(id: string, name: string, overrides: Partial<DatabaseConnection> = {}): DatabaseConnection {
  return {
    id,
    name,
    dbType: "mysql",
    host: `${id}.example.com`,
    port: 3306,
    group: "SASS",
    tags: [],
    ...overrides,
  } as DatabaseConnection;
}

const connections = [
  connection("a", "orders", { lastConnectedAt: 1 }),
  connection("b", "billing", { lastConnectedAt: 3, database: "billing_db" }),
  connection("c", "analytics"),
];

let wrapper: VueWrapper | undefined;

function mountOverview(
  props: Partial<{
    connections: DatabaseConnection[];
    connectedConnectionIds: string[];
    connectingConnectionIds: Record<string, boolean>;
  }> = {}
) {
  wrapper = mount(DatabaseConnectionOverview, {
    attachTo: document.body,
    props: {
      connections,
      connectedConnectionIds: [],
      connectingConnectionIds: {},
      schemas: {},
      groupOrder: [],
      ...props,
    },
    global: { stubs: { DatabaseTypeIcon: true } },
  });
  return wrapper;
}

async function search(view: VueWrapper, text: string) {
  await view.get(".connection-overview__search input").setValue(text);
}

afterEach(() => {
  vi.useRealTimers();
  wrapper?.unmount();
  wrapper = undefined;
  persistence.loadPersistedValue.mockReset().mockResolvedValue(null);
  persistence.savePersistedValue.mockClear();
});

describe("DatabaseConnectionOverview", () => {
  it("shows the empty state with an add button and no recent or in-use sections without connections", async () => {
    const view = mountOverview({ connections: [] });
    await flushPromises();

    expect(view.find(".el-empty").exists()).toBe(true);
    expect(view.find(".connection-overview__recent").exists()).toBe(false);
    expect(view.find(".db-connection-overview__live").exists()).toBe(false);

    await view.get(".el-empty .el-button").trigger("click");
    expect(view.emitted("new-connection")).toEqual([[]]);
  });

  it("lists recently opened connections newest first and skips never-connected ones", async () => {
    const view = mountOverview();
    await flushPromises();

    const names = view.findAll(".connection-overview__recent-name").map((node) => node.text());
    expect(names).toEqual(["billing", "orders"]);
    expect(view.get(".connection-overview__recent-detail").text()).toContain("billing_db");

    await view.findAll(".connection-overview__recent-card")[0].trigger("click");
    expect(view.emitted("connect")?.[0]?.[0]).toMatchObject({ id: "b" });
  });

  it("hides recent connections opened before the stored clear time", async () => {
    persistence.loadPersistedValue.mockResolvedValue(2);
    const view = mountOverview();
    await flushPromises();

    expect(persistence.loadPersistedValue).toHaveBeenCalledWith("app_db_recent_cleared_at_v1");
    expect(view.findAll(".connection-overview__recent-name").map((node) => node.text())).toEqual(["billing"]);
  });

  it("clears the recent list and remembers the clear time", async () => {
    const view = mountOverview();
    await flushPromises();
    expect(view.find(".connection-overview__recent").exists()).toBe(true);

    await view.get(".connection-overview__recent-block .connection-overview__section-action").trigger("click");
    expect(view.find(".connection-overview__recent").exists()).toBe(false);
    expect(persistence.savePersistedValue).toHaveBeenCalledWith("app_db_recent_cleared_at_v1", expect.any(Number));
  });

  it("connects a disconnected search result and opens a connected one with Enter", async () => {
    const view = mountOverview({ connectedConnectionIds: ["b"] });
    await flushPromises();
    const input = view.get(".connection-overview__search input");

    await search(view, "orders");
    await input.trigger("keydown", { key: "Enter" });
    expect(view.emitted("connect")?.[0]?.[0]).toMatchObject({ id: "a" });

    await search(view, "billing");
    await input.trigger("keydown", { key: "Enter" });
    expect(view.emitted("open")?.[0]?.[0]).toMatchObject({ id: "b" });
    expect(view.emitted("connect")).toHaveLength(1);
  });

  it("highlights nothing until the user searches, so Enter on an idle page does nothing", async () => {
    const view = mountOverview();
    await flushPromises();
    const input = view.get(".connection-overview__search input");

    expect(view.find(".connection-overview__row.is-highlighted").exists()).toBe(false);
    await input.trigger("keydown", { key: "Enter" });
    expect(view.emitted("connect")).toBeUndefined();
    expect(view.emitted("open")).toBeUndefined();

    await search(view, "o");
    expect(view.find(".connection-overview__row.is-highlighted").exists()).toBe(true);
    await search(view, "");
    expect(view.find(".connection-overview__row.is-highlighted").exists()).toBe(false);
  });

  it("starts highlighting from the first row when an arrow key is pressed in the empty search box", async () => {
    const view = mountOverview();
    const input = view.get(".connection-overview__search input");
    const firstRow = view.findAll(".connection-overview__row")[0];

    await input.trigger("keydown", { key: "ArrowDown" });
    expect(view.get(".connection-overview__row.is-highlighted").text()).toBe(firstRow.text());
    await input.trigger("keydown", { key: "ArrowDown" });
    expect(view.get(".connection-overview__row.is-highlighted").text()).toBe(
      view.findAll(".connection-overview__row")[1].text()
    );
  });

  it("shows the in-use section only while a connection is connected", async () => {
    const idle = mountOverview();
    await flushPromises();
    expect(idle.find(".db-connection-overview__live").exists()).toBe(false);
    idle.unmount();

    const live = mountOverview({ connectedConnectionIds: ["b"] });
    await flushPromises();
    expect(live.get(".db-connection-overview__live").text()).toContain("billing");

    await live.get(".db-connection-overview__live-card").trigger("click");
    expect(live.emitted("open")?.[0]?.[0]).toMatchObject({ id: "b" });
  });

  it("emits import-config from the import button", async () => {
    const view = mountOverview();
    await view.get(".db-connection-overview__import").trigger("click");
    expect(view.emitted("import-config")).toEqual([[]]);
  });

  it("selects a row on click and acts once on double-click or the action button", async () => {
    const view = mountOverview();
    const row = view.findAll(".connection-overview__row").find((node) => node.text().includes("analytics"))!;

    await row.trigger("click");
    expect(row.classes()).toContain("is-highlighted");
    expect(view.emitted("connect")).toBeUndefined();

    await row.trigger("dblclick");
    expect(view.emitted("connect")).toEqual([[connections[2]]]);

    const action = row.get(".db-connection-overview__action");
    await action.trigger("click", { detail: 1 });
    await action.trigger("click", { detail: 2 });
    await action.trigger("dblclick");
    expect(view.emitted("connect")).toHaveLength(2);
  });

  it("focuses the search box on mount unless a dialog holds focus", async () => {
    const view = mountOverview();
    await flushPromises();
    expect(document.activeElement).toBe(view.get(".connection-overview__search input").element);
    view.unmount();

    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    dialog.innerHTML = `<button type="button">ok</button>`;
    document.body.append(dialog);
    const dialogButton = dialog.querySelector("button")!;
    dialogButton.focus();
    mountOverview();
    await flushPromises();
    expect(document.activeElement).toBe(dialogButton);
    dialog.remove();
  });

  it("refreshes the connected-for time every minute while the overview is shown", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    vi.setSystemTime(new Date("2026-10-28T10:00:00Z"));
    const view = mountOverview({
      connections: [connection("b", "billing", { lastConnectedAt: Date.now() })],
      connectedConnectionIds: ["b"],
    });
    await flushPromises();
    expect(view.get(".db-connection-overview__live-since").text()).toBe("Connected 0 min");

    vi.advanceTimersByTime(60_000);
    await flushPromises();
    expect(view.get(".db-connection-overview__live-since").text()).toBe("Connected 1 min");

    vi.advanceTimersByTime(120_000);
    await flushPromises();
    expect(view.get(".db-connection-overview__live-since").text()).toBe("Connected 3 min");

    view.unmount();
    wrapper = undefined;
    expect(vi.getTimerCount()).toBe(0);
  });

  it("stops the clock while deactivated and catches up when activated again", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    vi.setSystemTime(new Date("2026-10-28T10:00:00Z"));
    const overviewProps = {
      connections: [connection("b", "billing", { lastConnectedAt: Date.now() })],
      connectedConnectionIds: ["b"],
      connectingConnectionIds: {},
      schemas: {},
      groupOrder: [],
    };
    const Host = defineComponent({
      props: { shown: { type: Boolean, required: true } },
      setup(props) {
        return () =>
          h(KeepAlive, null, { default: () => (props.shown ? h(DatabaseConnectionOverview, overviewProps) : null) });
      },
    });
    const host = mount(Host, {
      attachTo: document.body,
      props: { shown: true },
      global: { stubs: { DatabaseTypeIcon: true } },
    });
    wrapper = host;
    await flushPromises();
    expect(vi.getTimerCount()).toBe(1);

    await host.setProps({ shown: false });
    expect(vi.getTimerCount()).toBe(0);

    vi.advanceTimersByTime(5 * 60_000);
    await host.setProps({ shown: true });
    await flushPromises();
    expect(vi.getTimerCount()).toBe(1);
    expect(host.get(".db-connection-overview__live-since").text()).toBe("Connected 5 min");
  });
});
