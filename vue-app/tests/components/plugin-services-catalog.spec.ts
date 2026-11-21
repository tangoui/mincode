import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { ElMessage, ElOption, ElSelect } from "element-plus";
import PluginServicesCatalog from "@/components/plugins/PluginServicesCatalog.vue";
import PluginProvidedServices from "@/components/plugins/PluginProvidedServices.vue";
import { useVueI18n } from "@/i18n/messages";
import { pluginServices, startPluginServiceSync } from "@/services/plugin-services";

const bridge = vi.hoisted(() => ({ invoke: vi.fn(), listeners: new Map<string, Set<(payload: unknown) => void>>() }));

vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: (...args: unknown[]) => bridge.invoke(...args),
  isDesktopRuntime: () => true,
  listenAppEvent: (event: string, callback: (payload: unknown) => void) => {
    const set = bridge.listeners.get(event) ?? new Set();
    set.add(callback);
    bridge.listeners.set(event, set);
    return () => set.delete(callback);
  },
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
  chooseNativeSaveFile: vi.fn(),
  copyText: vi.fn(),
}));

vi.mock("element-plus", async (original) => {
  const actual = await original<typeof import("element-plus")>();
  const message = Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() });
  return { ...actual, ElMessage: message };
});

function entry(overrides: Record<string, unknown> = {}) {
  return {
    pluginId: "demo.provider",
    pluginName: "Demo Provider",
    entryId: "reverse",
    service: "demo.text.reverse",
    title: "Reverse text",
    description: "",
    input: { kind: "text", maxBytes: 8192 },
    output: { kind: "text" },
    surfaces: ["selection", "palette"],
    risk: "read",
    timeoutMs: 30000,
    isDefault: false,
    ...overrides,
  };
}

let services: unknown[] = [];
/** What plugin_services_declared returns; by default the listed services, all active. */
let declared: unknown[] | undefined;
let defaultError: Error | undefined;
/** Overrides the list response (a rejected or never-settling promise). */
let listResponse: Promise<unknown> | undefined;
/** Overrides the declared response (a rejected or never-settling promise). */
let declaredResponse: Promise<unknown> | undefined;
let stop: (() => void) | undefined;
const { t } = useVueI18n();

async function startWith(list: unknown[]) {
  services = list;
  stop = startPluginServiceSync();
  await flushPromises();
}

beforeEach(() => {
  bridge.invoke.mockReset();
  vi.mocked(ElMessage.error).mockClear();
  defaultError = undefined;
  listResponse = undefined;
  declared = undefined;
  declaredResponse = undefined;
  bridge.listeners.clear();
  bridge.invoke.mockImplementation(async (command: string, args: Record<string, unknown>) => {
    if (command === "plugin_services_list") {
      if (listResponse) return listResponse;
      return { services };
    }
    if (command === "plugin_services_declared") {
      if (declaredResponse) return declaredResponse;
      const all = declared ?? services.map((item) => ({ ...(item as Record<string, unknown>), active: true }));
      return { services: all.filter((item) => (item as Record<string, unknown>).pluginId === args.pluginId) };
    }
    if (command === "plugin_services_default_set") {
      if (defaultError) throw defaultError;
      services = services.map((item) => {
        const record = item as Record<string, unknown>;
        return record.service === args.service ? { ...record, isDefault: record.pluginId === args.pluginId } : record;
      });
      return { defaults: {} };
    }
    return {};
  });
});

afterEach(() => {
  // Stopping the sync also clears the list a one-shot load left behind.
  (stop ?? startPluginServiceSync())();
  stop = undefined;
});

const twoProviders = [
  entry(),
  entry({ pluginId: "other.provider", pluginName: "Other Provider", entryId: "flip", title: "Flip text", risk: "dangerous" }),
  entry({
    pluginId: "image.tools",
    pluginName: "Image Tools",
    entryId: "resize",
    service: "image.resize",
    title: "Resize image",
    input: { kind: "files", accepts: [".png", "image/*"], maxFiles: 8 },
    output: { kind: "file", mime: "image/png" },
    surfaces: ["fileMenu"],
    risk: "write",
  }),
];

describe("PluginServicesCatalog", () => {
  it("groups providers by service and shows title, provider, risk and shapes", async () => {
    await startWith(twoProviders);
    const wrapper = mount(PluginServicesCatalog);
    await flushPromises();

    const groups = wrapper.findAll(".plugin-services-catalog__group");
    expect(groups.map((group) => group.find("h3").text())).toEqual(["demo.text.reverse", "image.resize"]);
    const text = wrapper.text();
    expect(text).toContain("Reverse text");
    expect(text).toContain("Demo Provider");
    expect(text).toContain("Flip text");
    expect(text).toContain("Other Provider");
    expect(text).toContain(t("pluginHostUi.risk.dangerous"));
    expect(text).toContain(t("pluginHostUi.risk.write"));
    expect(text).toContain(".png, image/*");
    expect(text).toContain("image/png");
    wrapper.unmount();
  });

  it("shows provider titles in the UI language", async () => {
    await startWith([entry({ titleLocalized: { zh: "反转文字" } })]);
    const catalog = mount(PluginServicesCatalog);
    const provided = mount(PluginProvidedServices, { props: { pluginId: "demo.provider" } });
    await flushPromises();
    expect(catalog.text()).toContain("反转文字");
    expect(catalog.text()).not.toContain("Reverse text");
    expect(provided.text()).toContain("反转文字");
    catalog.unmount();
    provided.unmount();
  });

  it("offers a default provider choice only for services with two or more providers", async () => {
    await startWith(twoProviders);
    const wrapper = mount(PluginServicesCatalog);
    await flushPromises();

    const groups = wrapper.findAll(".plugin-services-catalog__group");
    expect(groups[0].findComponent(ElSelect).exists()).toBe(true);
    expect(groups[1].findComponent(ElSelect).exists()).toBe(false);
    const select = groups[0].findComponent(ElSelect);
    expect(select.props("modelValue")).toBe("");
    const options = select.findAllComponents(ElOption).map((option) => [option.props("value"), option.props("label")]);
    expect(options).toEqual([["", t("plugins.services.default.ask")], ["demo.provider", "Demo Provider"], ["other.provider", "Other Provider"]]);
    // "" is a real choice, so the select shows its label instead of an empty placeholder.
    expect(select.text()).toContain(t("plugins.services.default.ask"));
    wrapper.unmount();
  });

  it("saves the chosen default provider and clears it with Ask every time", async () => {
    await startWith(twoProviders);
    const wrapper = mount(PluginServicesCatalog);
    await flushPromises();

    const select = wrapper.findAll(".plugin-services-catalog__group")[0].findComponent(ElSelect);
    select.vm.$emit("update:modelValue", "other.provider");
    select.vm.$emit("change", "other.provider");
    await flushPromises();
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_services_default_set", { service: "demo.text.reverse", pluginId: "other.provider" });
    expect(pluginServices.value.find((item) => item.pluginId === "other.provider")?.isDefault).toBe(true);
    expect(wrapper.findAll(".plugin-services-catalog__group")[0].findComponent(ElSelect).props("modelValue")).toBe("other.provider");

    const again = wrapper.findAll(".plugin-services-catalog__group")[0].findComponent(ElSelect);
    again.vm.$emit("update:modelValue", "");
    again.vm.$emit("change", "");
    await flushPromises();
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_services_default_set", { service: "demo.text.reverse", pluginId: "" });
    expect(pluginServices.value.some((item) => item.service === "demo.text.reverse" && item.isDefault)).toBe(false);
    wrapper.unmount();
  });

  it("keeps the saved default and reports the error when saving fails", async () => {
    await startWith(twoProviders);
    const wrapper = mount(PluginServicesCatalog);
    await flushPromises();
    defaultError = new Error("unavailable: disk full");

    const select = wrapper.findAll(".plugin-services-catalog__group")[0].findComponent(ElSelect);
    select.vm.$emit("update:modelValue", "other.provider");
    select.vm.$emit("change", "other.provider");
    await flushPromises();
    expect(ElMessage.error).toHaveBeenCalledWith(expect.objectContaining({
      message: t("plugins.services.failed", { name: "demo.text.reverse", detail: "disk full" }),
    }));
    expect(pluginServices.value.some((item) => item.service === "demo.text.reverse" && item.isDefault)).toBe(false);
    expect(wrapper.findAll(".plugin-services-catalog__group")[0].findComponent(ElSelect).props("modelValue")).toBe("");
    wrapper.unmount();
  });

  it("shows an empty state without providers", async () => {
    await startWith([]);
    const wrapper = mount(PluginServicesCatalog);
    await flushPromises();
    expect(wrapper.findAll(".plugin-services-catalog__group")).toHaveLength(0);
    expect(wrapper.text()).toContain(t("plugins.services.catalog.empty"));
    wrapper.unmount();
  });
});

describe("PluginProvidedServices", () => {
  it("lists the services one plugin provides", async () => {
    await startWith(twoProviders);
    const wrapper = mount(PluginProvidedServices, { props: { pluginId: "image.tools" } });
    await flushPromises();
    const text = wrapper.text();
    expect(text).toContain(t("plugins.services.provided"));
    expect(text).toContain("image.resize");
    expect(text).toContain("Resize image");
    expect(text).not.toContain("demo.text.reverse");
    wrapper.unmount();
  });

  it("renders nothing for a plugin without services", async () => {
    await startWith(twoProviders);
    const wrapper = mount(PluginProvidedServices, { props: { pluginId: "no.services" } });
    await flushPromises();
    expect(wrapper.find("section").exists()).toBe(false);
    wrapper.unmount();
  });

  it("reads the plugin's declared services and marks active ones without a tag", async () => {
    await startWith(twoProviders);
    const wrapper = mount(PluginProvidedServices, { props: { pluginId: "image.tools" } });
    await flushPromises();
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_services_declared", { pluginId: "image.tools" });
    expect(wrapper.find(".plugin-provided-services__item--inactive").exists()).toBe(false);
    expect(wrapper.text()).not.toContain(t("plugins.services.inactive.disabled"));
    expect(wrapper.text()).not.toContain(t("plugins.services.inactive.inactiveWorkspace"));
    wrapper.unmount();
  });

  it("lists a disabled plugin's services greyed with a Plugin disabled tag", async () => {
    await startWith([]);
    declared = [entry({ pluginId: "off.plugin", pluginName: "Off", active: false, inactiveReason: "disabled" })];
    const wrapper = mount(PluginProvidedServices, { props: { pluginId: "off.plugin" } });
    await flushPromises();
    const rows = wrapper.findAll("li");
    expect(rows).toHaveLength(1);
    expect(rows[0].classes()).toContain("plugin-provided-services__item--inactive");
    expect(rows[0].text()).toContain("Reverse text");
    expect(rows[0].text()).toContain(t("plugins.services.inactive.disabled"));
    wrapper.unmount();
  });

  it("tags services of a plugin not active in this workspace and keeps them out of the Services tab", async () => {
    await startWith([]);
    declared = [entry({ pluginId: "scoped.plugin", active: false, inactiveReason: "inactiveWorkspace", isDefault: true })];
    const wrapper = mount(PluginProvidedServices, { props: { pluginId: "scoped.plugin" } });
    const catalog = mount(PluginServicesCatalog);
    await flushPromises();
    const row = wrapper.find("li");
    expect(row.classes()).toContain("plugin-provided-services__item--inactive");
    expect(row.text()).toContain(t("plugins.services.inactive.inactiveWorkspace"));
    expect(row.text()).not.toContain(t("plugins.services.default.tag"));
    expect(catalog.text()).toContain(t("plugins.services.catalog.empty"));
    expect(catalog.text()).not.toContain("Reverse text");
    wrapper.unmount();
    catalog.unmount();
  });

  it("drops entries of other plugins and entries with an unknown state", async () => {
    await startWith([]);
    declared = [
      entry({ pluginId: "off.plugin", active: false, inactiveReason: "disabled" }),
      entry({ pluginId: "off.plugin", entryId: "odd", title: "Odd", active: false, inactiveReason: "sleepy" }),
      entry({ pluginId: "off.plugin", entryId: "noflag", title: "No flag" }),
    ];
    const wrapper = mount(PluginProvidedServices, { props: { pluginId: "off.plugin" } });
    await flushPromises();
    expect(wrapper.findAll("li")).toHaveLength(1);
    expect(wrapper.text()).not.toContain("Odd");
    expect(wrapper.text()).not.toContain("No flag");
    wrapper.unmount();
  });

  it("reloads when plugin state or the workspace changes", async () => {
    vi.useFakeTimers();
    try {
      await startWith([]);
      declared = [entry({ pluginId: "off.plugin", active: false, inactiveReason: "disabled" })];
      const wrapper = mount(PluginProvidedServices, { props: { pluginId: "off.plugin" } });
      await flushPromises();
      expect(wrapper.find("li").text()).toContain(t("plugins.services.inactive.disabled"));

      declared = [entry({ pluginId: "off.plugin", active: true })];
      for (const callback of bridge.listeners.get("plugin:state-changed") ?? []) callback({});
      await vi.runAllTimersAsync();
      await flushPromises();
      expect(wrapper.find(".plugin-provided-services__item--inactive").exists()).toBe(false);

      declared = [entry({ pluginId: "off.plugin", active: false, inactiveReason: "inactiveWorkspace" })];
      for (const callback of bridge.listeners.get("plugin:workspace-changed") ?? []) callback({});
      await vi.runAllTimersAsync();
      await flushPromises();
      expect(wrapper.find("li").text()).toContain(t("plugins.services.inactive.inactiveWorkspace"));
      wrapper.unmount();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("plugin services where the sync does not run (popped-out window)", () => {
  function listCalls() {
    return bridge.invoke.mock.calls.filter(([command]) => command === "plugin_services_list").length;
  }

  it("the catalog loads the services itself instead of showing the empty state", async () => {
    services = twoProviders;
    const wrapper = mount(PluginServicesCatalog);
    await flushPromises();
    expect(listCalls()).toBe(1);
    expect(wrapper.findAll(".plugin-services-catalog__group").map((group) => group.find("h3").text())).toEqual(["demo.text.reverse", "image.resize"]);
    expect(wrapper.text()).not.toContain(t("plugins.services.catalog.empty"));
    wrapper.unmount();
  });

  it("re-reads the list after saving a default", async () => {
    services = twoProviders;
    const wrapper = mount(PluginServicesCatalog);
    await flushPromises();
    const select = wrapper.findAll(".plugin-services-catalog__group")[0].findComponent(ElSelect);
    select.vm.$emit("update:modelValue", "other.provider");
    select.vm.$emit("change", "other.provider");
    await flushPromises();
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_services_default_set", { service: "demo.text.reverse", pluginId: "other.provider" });
    expect(listCalls()).toBe(2);
    expect(pluginServices.value.find((item) => item.pluginId === "other.provider")?.isDefault).toBe(true);
    wrapper.unmount();
  });

  it("shows a labelled error with Retry instead of the empty state when loading fails", async () => {
    listResponse = Promise.reject(new Error("unavailable: plugin host stopped"));
    listResponse.catch(() => undefined);
    const wrapper = mount(PluginServicesCatalog);
    await flushPromises();
    expect(wrapper.text()).toContain(t("plugins.services.catalog.loadFailed", { detail: "plugin host stopped" }));
    expect(wrapper.text()).not.toContain(t("plugins.services.catalog.empty"));

    listResponse = undefined;
    services = twoProviders;
    const retry = wrapper.findAll("button").find((button) => button.text() === t("plugins.services.catalog.retry"));
    expect(retry).toBeDefined();
    await retry!.trigger("click");
    await flushPromises();
    expect(listCalls()).toBe(2);
    expect(wrapper.findAll(".plugin-services-catalog__group")).toHaveLength(2);
    expect(wrapper.text()).not.toContain(t("plugins.services.catalog.loadFailed", { detail: "plugin host stopped" }));
    wrapper.unmount();
  });

  it("shows loading, not the empty state, while the list is on its way", async () => {
    listResponse = new Promise(() => undefined);
    const wrapper = mount(PluginServicesCatalog);
    await flushPromises();
    expect(wrapper.text()).not.toContain(t("plugins.services.catalog.empty"));
    expect(wrapper.find("[aria-busy='true']").exists()).toBe(true);
    wrapper.unmount();
  });

  it("the plugin details load the services the plugin declares", async () => {
    services = twoProviders;
    const wrapper = mount(PluginProvidedServices, { props: { pluginId: "image.tools" } });
    await flushPromises();
    expect(wrapper.text()).toContain("image.resize");
    wrapper.unmount();
  });

  it("the plugin details show nothing while loading and a short error on failure", async () => {
    declaredResponse = new Promise(() => undefined);
    const pending = mount(PluginProvidedServices, { props: { pluginId: "image.tools" } });
    await flushPromises();
    expect(pending.find("section").exists()).toBe(false);
    pending.unmount();

    declaredResponse = Promise.reject(new Error("unavailable: plugin host stopped"));
    declaredResponse.catch(() => undefined);
    const failed = mount(PluginProvidedServices, { props: { pluginId: "image.tools" } });
    await flushPromises();
    expect(failed.text()).toContain(t("plugins.services.provided"));
    expect(failed.text()).toContain(t("plugins.services.catalog.loadFailed", { detail: "plugin host stopped" }));
    failed.unmount();
  });

  it("does not load again where the sync keeps the list fresh", async () => {
    await startWith(twoProviders);
    const wrapper = mount(PluginServicesCatalog);
    await flushPromises();
    expect(listCalls()).toBe(1);
    wrapper.unmount();
  });
});
