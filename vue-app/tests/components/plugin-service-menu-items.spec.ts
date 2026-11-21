import { beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { defineComponent, h, nextTick, ref } from "vue";
import { ElMessage } from "element-plus";
import PluginServiceMenuItems from "@/components/plugins/PluginServiceMenuItems.vue";
import { MENU_VISIBLE_PLUGIN_ITEMS } from "@/domain/plugins/menus";
import type { DbResultServiceTarget, ServiceSurfaceTarget } from "@/domain/plugins/service-surfaces";
import type { PluginServiceEntry } from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";

const services = vi.hoisted(() => ({ entries: [] as unknown[], run: vi.fn(), built: vi.fn() }));

vi.mock("element-plus", async (original) => {
  const actual = await original<typeof import("element-plus")>();
  const message = Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() });
  return { ...actual, ElMessage: message };
});

// Records every request built: the result set must be serialized only on a click.
vi.mock("@/domain/plugins/service-surfaces", async (original) => {
  const actual = await original<typeof import("@/domain/plugins/service-surfaces")>();
  return {
    ...actual,
    surfaceRunRequest: (...args: Parameters<typeof actual.surfaceRunRequest>) => {
      const request = actual.surfaceRunRequest(...args);
      services.built(request);
      return request;
    },
  };
});

vi.mock("@/services/plugin-services", async () => {
  const { shallowRef } = await import("vue");
  const pluginServices = shallowRef<unknown[]>([]);
  return {
    pluginServices: {
      get value() {
        return services.entries.length ? services.entries : pluginServices.value;
      },
    },
    runPluginService: (...args: unknown[]) => services.run(...args),
  };
});

// el-dropdown-item needs a live dropdown; the stub keeps its role and forwards listeners.
const DropdownItem = defineComponent({
  inheritAttrs: false,
  setup(_, { attrs, slots }) {
    return () => h("li", { role: "menuitem", tabindex: -1, ...attrs }, slots.default?.());
  },
});

function entry(overrides: Partial<PluginServiceEntry> = {}): PluginServiceEntry {
  return {
    pluginId: "demo.provider",
    pluginName: "Demo Provider",
    entryId: "convert",
    service: "demo.file.convert",
    title: "Convert",
    description: "",
    input: { kind: "files", accepts: [".png", ".txt"], maxBytes: 1024, maxFiles: 8 },
    output: { kind: "text" },
    surfaces: ["fileMenu", "palette"],
    risk: "read",
    timeoutMs: 30000,
    isDefault: false,
    ...overrides,
  };
}

const stubs = { ElDropdownItem: DropdownItem, "el-dropdown-item": DropdownItem };

function mountTarget(target: () => ServiceSurfaceTarget | null, flat = false) {
  return mount(PluginServiceMenuItems, { props: { target, flat }, global: { stubs } });
}

function mountItems(files: Array<{ name: string; absolutePath: string }>) {
  return mountTarget(() => (files.length ? { surface: "fileMenu", files } : null));
}

function labels(wrapper: ReturnType<typeof mountItems>) {
  return wrapper.findAll("[role=menuitem]").map((item) => item.text());
}

const { t } = useVueI18n();
const MENU = t("plugins.services.menu");

beforeEach(() => {
  services.entries = [];
  services.run.mockReset();
  services.built.mockReset();
  vi.mocked(ElMessage.info).mockClear();
  vi.mocked(ElMessage.warning).mockClear();
});

describe("PluginServiceMenuItems", () => {
  it("offers only services that accept every selected file, behind a 'Process with plugin' expander", async () => {
    services.entries = [
      entry(),
      entry({ service: "demo.image.resize", entryId: "resize", title: "Resize image", input: { kind: "files", accepts: ["image/*"], maxBytes: 1024, maxFiles: 8 } }),
    ];
    const wrapper = mountItems([
      { name: "a.png", absolutePath: "/repo/a.png" },
      { name: "b.txt", absolutePath: "/repo/b.txt" },
    ]);
    const expander = wrapper.get("[aria-expanded]");
    expect(expander.attributes("aria-expanded")).toBe("false");
    expect(expander.attributes("aria-label")).toBe(MENU);
    expect(labels(wrapper)).toEqual([MENU]);

    await expander.trigger("keydown", { key: "Enter" });
    expect(expander.attributes("aria-expanded")).toBe("true");
    expect(labels(wrapper)).toEqual([MENU, "Convert"]);

    await wrapper.findAll("[role=menuitem]")[1].trigger("click");
    expect(services.run).toHaveBeenCalledWith({
      service: "demo.file.convert",
      surface: "fileMenu",
      paths: ["/repo/a.png", "/repo/b.txt"],
    });
    expect(wrapper.emitted("run")).toHaveLength(1);
  });

  it("shows each service under its title in the UI language", async () => {
    services.entries = [entry({ titleLocalized: { zh: "转换", en: "Convert" } })];
    const wrapper = mountItems([{ name: "a.png", absolutePath: "/repo/a.png" }]);
    await wrapper.get("[aria-expanded]").trigger("click");
    expect(labels(wrapper)).toEqual([MENU, "转换"]);
  });

  it("keeps the dropdown open when the expander is clicked", async () => {
    services.entries = [entry()];
    const wrapper = mountItems([{ name: "b.txt", absolutePath: "/repo/b.txt" }]);
    const click = new MouseEvent("click", { bubbles: true, cancelable: true });
    wrapper.get("[aria-expanded]").element.dispatchEvent(click);
    await nextTick();
    expect(click.defaultPrevented).toBe(true);
    expect(labels(wrapper)).toEqual([MENU, "Convert"]);
  });

  it("collapses services beyond the menu limit into a 'More services' expander", async () => {
    services.entries = Array.from({ length: MENU_VISIBLE_PLUGIN_ITEMS + 1 }, (_, index) => entry({
      service: `demo.file.s${String(index).padStart(2, "0")}`,
      entryId: `s${index}`,
      title: `Service ${String(index).padStart(2, "0")}`,
    }));
    const wrapper = mountItems([{ name: "b.txt", absolutePath: "/repo/b.txt" }]);
    await wrapper.get("[aria-expanded]").trigger("click");
    const visible = labels(wrapper);
    expect(visible).toHaveLength(1 + MENU_VISIBLE_PLUGIN_ITEMS + 1);
    expect(visible).not.toContain("Service 12");
    const more = wrapper.findAll("[aria-expanded]")[1];
    expect(more.text()).toBe(t("plugins.services.moreCount", { count: 1 }));

    await more.trigger("keydown", { key: " " });
    expect(labels(wrapper).at(-1)).toBe("Service 12");
  });

  it("renders nothing for directories, unmatched files or files beyond maxFiles", () => {
    services.entries = [entry({ input: { kind: "files", accepts: [".txt"], maxBytes: 1024, maxFiles: 1 } })];
    expect(mountItems([]).findAll("[role=menuitem]")).toHaveLength(0);
    expect(mountItems([{ name: "a.png", absolutePath: "/repo/a.png" }]).findAll("[role=menuitem]")).toHaveLength(0);
    expect(mountItems([
      { name: "a.txt", absolutePath: "/repo/a.txt" },
      { name: "b.txt", absolutePath: "/repo/b.txt" },
    ]).findAll("[role=menuitem]")).toHaveLength(0);
  });

  it("renders nothing while no menu is open", () => {
    services.entries = [entry()];
    expect(mountTarget(() => null).findAll("[role=menuitem]")).toHaveLength(0);
  });

  it("lists services directly in flat mode, without an expander", async () => {
    services.entries = [entry({
      service: "json.types", entryId: "types", title: "Generate types",
      input: { kind: "file", accepts: [".json"], maxBytes: 1048576 }, surfaces: ["httpResponse"],
    })];
    const wrapper = mountTarget(() => ({
      surface: "httpResponse",
      response: { status: 200, statusText: "OK", contentType: "application/json", headers: [["Set-Cookie", "sid=1"]], body: "{}", sizeBytes: 2, elapsedMs: 3 },
      secrets: [],
      request: { method: "GET", url: "https://api.example.com/x?token=1" },
    }), true);
    expect(wrapper.find("[aria-expanded]").exists()).toBe(false);
    expect(labels(wrapper)).toEqual(["Generate types"]);

    await wrapper.get("[role=menuitem]").trigger("click");
    expect(services.run).toHaveBeenCalledWith({
      service: "json.types",
      surface: "httpResponse",
      // The only provider is pinned, so the host cannot pick one the content was not sized for.
      provider: { pluginId: "demo.provider", entryId: "types" },
      content: { text: "{}", contentType: "application/json" },
      surfaceContext: {
        response: { status: 200, statusText: "OK", contentType: "application/json", elapsedMs: 3, headers: [], headersRedacted: 1 },
        request: { method: "GET", url: "https://api.example.com/x" },
      },
    });
    expect(wrapper.emitted("run")).toHaveLength(1);
  });

  it("greys out a service that cannot run now and names the reason", async () => {
    services.entries = [entry({
      service: "data.chart", entryId: "chart", title: "Chart",
      input: { kind: "file", accepts: [".json"], maxBytes: 1048576 }, surfaces: ["dbResult"],
    })];
    const target: DbResultServiceTarget = {
      surface: "dbResult",
      columns: [{ name: "id", type: null }],
      rows: () => [[1]],
      formatCell: String,
      hasMore: false,
      running: true,
      pendingChanges: false,
      view: { filtered: false, sorted: false, hiddenColumns: 0 },
    };
    const wrapper = mountTarget(() => target, true);
    const item = wrapper.get("[role=menuitem]");
    const reason = t("plugins.services.disabled.queryRunning");
    expect(item.attributes("title")).toBe(reason);
    expect(item.attributes("disabled")).toBeDefined();
    expect(item.text()).toContain(reason);
    await item.trigger("click");
    expect(services.built).not.toHaveBeenCalled();
    expect(services.run).not.toHaveBeenCalled();
  });

  it("serializes a result set only when a service is clicked, and reports a cut", async () => {
    services.entries = [entry({
      service: "data.chart", entryId: "chart", title: "Chart",
      input: { kind: "file", accepts: ["application/json"], maxBytes: 1024 }, surfaces: ["dbResult"],
    })];
    const rows = Array.from({ length: 200 }, (_, index) => [index, { at: index }]);
    const formatCell = vi.fn((value: unknown) => JSON.stringify(value));
    const target: DbResultServiceTarget = {
      surface: "dbResult",
      columns: [{ name: "id", type: "int" }, { name: "meta", type: "json" }],
      rows: () => rows,
      formatCell,
      hasMore: true,
      running: false,
      pendingChanges: false,
      view: { filtered: false, sorted: true, hiddenColumns: 0 },
    };
    const wrapper = mountTarget(() => target);
    await wrapper.get("[aria-expanded]").trigger("click");
    expect(labels(wrapper)).toEqual([MENU, "Chart"]);
    expect(services.built).not.toHaveBeenCalled();
    expect(formatCell).not.toHaveBeenCalled();

    await wrapper.findAll("[role=menuitem]")[1].trigger("click");
    expect(services.built).toHaveBeenCalledTimes(1);
    expect(formatCell).toHaveBeenCalled();
    const request = services.run.mock.calls[0]?.[0] as { surface: string; content: { text: string }; notice?: unknown };
    expect(request.surface).toBe("dbResult");
    expect(request).not.toHaveProperty("notice");
    const file = JSON.parse(request.content.text) as { rowCount: number; truncated: boolean; hasMore: boolean };
    expect(file).toMatchObject({ truncated: true, hasMore: true });
    expect(new TextEncoder().encode(request.content.text).length).toBeLessThanOrEqual(1024);
    expect(ElMessage.info).toHaveBeenCalledWith(expect.objectContaining({
      message: t("plugins.services.dbResultTruncated", { count: file.rowCount }),
    }));
  });

  it("reads a result set's rows once while the menu is open and refuses one whose first row is too large", async () => {
    services.entries = [entry({
      service: "data.chart", entryId: "chart", title: "Chart",
      input: { kind: "file", accepts: [".json"], maxBytes: 1024 }, surfaces: ["dbResult"],
    })];
    const rows = vi.fn(() => [[1, "y".repeat(5000)]]);
    const target: DbResultServiceTarget = {
      surface: "dbResult",
      columns: [{ name: "id", type: null }, { name: "blob", type: null }],
      rows,
      formatCell: String,
      hasMore: false,
      running: false,
      pendingChanges: false,
      view: { filtered: false, sorted: false, hiddenColumns: 0 },
    };
    // The grid's rows are reactive: updates while the menu is open must not re-read them.
    const tick = ref(0);
    rows.mockImplementation(() => (tick.value >= 0 ? [[1, "y".repeat(5000)]] : []));
    const wrapper = mountTarget(() => target, true);
    tick.value++;
    await nextTick();
    tick.value++;
    await nextTick();
    expect(wrapper.findAll("[role=menuitem]")).toHaveLength(1);
    expect(rows).toHaveBeenCalledTimes(1);

    await wrapper.get("[role=menuitem]").trigger("click");
    expect(services.run).not.toHaveBeenCalled();
    expect(ElMessage.warning).toHaveBeenCalledWith(expect.objectContaining({ message: t("plugins.services.dbResultTooLarge") }));
    expect(ElMessage.info).not.toHaveBeenCalled();
  });

  it("offers a terminal service only for an explicit selection", () => {
    services.entries = [entry({ service: "term.explain", entryId: "explain", title: "Explain", input: { kind: "text", maxBytes: 8 }, surfaces: ["terminalSelection"] })];
    expect(mountTarget(() => ({ surface: "terminalSelection", kind: "local", selection: "" }), true).findAll("[role=menuitem]")).toHaveLength(0);
    const tooLarge = mountTarget(() => ({ surface: "terminalSelection", kind: "local", selection: "123456789" }), true);
    expect(tooLarge.get("[role=menuitem]").attributes("title")).toBe(t("plugins.services.disabled.tooLarge"));
  });
});
