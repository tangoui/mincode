import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import type { PluginSettingsPage } from "@/domain/plugins/settings-pages";

// PluginViewFrame is replaced by a stub that records the props the host gives it.
const frames = vi.hoisted(() => ({ props: [] as Array<Record<string, unknown>> }));
vi.mock("@/components/plugins/PluginViewFrame.vue", async () => {
  const { defineComponent: define, h: render } = await import("vue");
  return {
    default: define({
      name: "PluginViewFrame",
      props: ["pluginId", "viewId", "active", "dropTargetId", "variant", "frameTitle", "warm", "document", "size"],
      emits: ["status"],
      setup(props) {
        frames.props.push({ ...props });
        return () => render("div", { class: "frame-stub", "data-testid": "frame-stub" }, "frame");
      },
    }),
  };
});

const tabs = vi.hoisted(() => ({ open: vi.fn() }));
vi.mock("@/domain/workspace/open-plugin-view", () => ({ openPluginViewTab: tabs.open }));
vi.mock("vue-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("vue-router")>()),
  useRouter: () => ({ push: vi.fn() }),
}));

vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/desktop-bridge")>()),
  isDesktopRuntime: () => true,
  listenAppEvent: () => () => undefined,
}));

const config = vi.hoisted(() => ({ ids: [] as string[] }));
vi.mock("@/services/plugin-configuration-service", () => ({
  listConfigurablePlugins: () => Promise.resolve(config.ids),
  getPluginConfiguration: (pluginId: string) =>
    Promise.resolve({ pluginId, workspaceId: "", schema: { properties: {} }, global: {}, workspace: {}, secrets: {}, ignored: [], orphanSecrets: [] }),
  listenPluginConfigurationChanged: () => () => undefined,
  listenPluginSettingsInvalidated: () => () => undefined,
  resetPluginConfiguration: vi.fn(),
  setPluginConfiguration: vi.fn(),
  setPluginConfigurationSecret: vi.fn(),
}));
vi.mock("@/services/plugin-service", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/plugin-service")>()),
  listPlugins: () => Promise.resolve([{ id: "acme.proxy", name: "Acme Proxy", enabled: true, source: "installed" }]),
}));

const pages = vi.hoisted(() => ({ ref: undefined as undefined | { value: readonly PluginSettingsPage[] } }));
vi.mock("@/services/contributions/settings-pages-runtime", async () => {
  const { shallowRef: ref } = await import("vue");
  pages.ref = ref<readonly PluginSettingsPage[]>([]);
  return { pluginSettingsPages: pages.ref };
});

const { default: PluginSettingsPageSection } = await import("@/components/settings/PluginSettingsPageSection.vue");
const { default: PluginSettingsPanel } = await import("@/components/settings/PluginSettingsPanel.vue");

const page: PluginSettingsPage = {
  pluginId: "acme.proxy",
  pluginName: "Acme Proxy",
  id: "main",
  view: "settings",
  title: "Rules",
  activeInWorkspace: true,
};

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  frames.props = [];
  tabs.open.mockReset();
  config.ids = [];
  if (pages.ref) pages.ref.value = [];
});

const settle = async () => {
  await flushPromises();
  await new Promise((resolve) => setTimeout(resolve, 0));
  await flushPromises();
};

describe("PluginSettingsPageSection", () => {
  it("header names the plugin outside the frame", async () => {
    wrapper = mount(PluginSettingsPageSection, { props: { page }, attachTo: document.body });
    await settle();
    const header = wrapper.find('[role="note"]');
    expect(header.exists()).toBe(true);
    expect(header.text()).toContain("Acme Proxy");
    expect(header.text()).toContain("acme.proxy");
    // The header is host DOM, never inside the plugin frame.
    expect(header.find('[data-testid="frame-stub"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="frame-stub"]').element.contains(header.element)).toBe(false);
    // It cannot be closed or collapsed: its only control opens the page as a tab.
    const buttons = header.findAll("button");
    expect(buttons).toHaveLength(1);
    expect(buttons[0]!.attributes("aria-label") ?? buttons[0]!.text()).not.toMatch(/close|关闭/i);
  });

  it("frame uses the settings surface without drop", async () => {
    wrapper = mount(PluginSettingsPageSection, { props: { page }, attachTo: document.body });
    await settle();
    expect(frames.props).toHaveLength(1);
    const props = frames.props[0]!;
    expect(props.variant).toBe("settings");
    expect(props.pluginId).toBe("acme.proxy");
    expect(props.viewId).toBe("settings");
    expect(props.dropTargetId).toBe("");
    expect(props.warm).toBeUndefined();
    expect(props.document).toBeUndefined();
    expect(String(props.frameTitle)).toContain("Acme Proxy");
    expect(String(props.frameTitle)).toContain("Rules");
  });

  it("inactive plugin mounts no frame", async () => {
    wrapper = mount(PluginSettingsPageSection, { props: { page: { ...page, activeInWorkspace: false } }, attachTo: document.body });
    await settle();
    expect(wrapper.find('[data-testid="frame-stub"]').exists()).toBe(false);
    expect(frames.props).toHaveLength(0);
    expect(wrapper.text()).toContain("此插件在当前工作区未启用");
    // The host header still says whose page this is.
    expect(wrapper.find('[role="note"]').text()).toContain("acme.proxy");
  });

  it("open as tab", async () => {
    wrapper = mount(PluginSettingsPageSection, { props: { page }, attachTo: document.body });
    await settle();
    await wrapper.find('[role="note"] button').trigger("click");
    expect(tabs.open).toHaveBeenCalledTimes(1);
    expect(tabs.open).toHaveBeenCalledWith(expect.anything(), expect.anything(), "acme.proxy", "settings", "Rules");
    // The tab opens behind the Settings dialog, so the section asks Settings to close.
    expect(wrapper.emitted("navigate")).toHaveLength(1);
  });

  it("a failed frame shows the error with a retry that starts a fresh frame", async () => {
    wrapper = mount(PluginSettingsPageSection, { props: { page }, attachTo: document.body });
    await settle();
    wrapper.findComponent({ name: "PluginViewFrame" }).vm.$emit("status", "error", "Plugin crashed");
    await settle();
    const alert = wrapper.find('[role="alert"]');
    expect(alert.exists()).toBe(true);
    expect(alert.text()).toContain("设置页面加载失败");
    expect(alert.text()).toContain("Plugin crashed");
    await alert.find("button").trigger("click");
    await settle();
    expect(frames.props).toHaveLength(2);
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
    // Focus returns to the frame box, not to <body>.
    expect(document.activeElement).toBe(wrapper.find(".app-plugin-settings-page__frame").element);
  });
});

describe("PluginSettingsPanel with settings pages", () => {
  const optionLabels = (target: VueWrapper) =>
    target.findAllComponents({ name: "ElOption" }).map((option) => String(option.props("label")));

  it("picker lists plugins that only have a settings page", async () => {
    config.ids = [];
    pages.ref!.value = [page];
    wrapper = mount(PluginSettingsPanel, { attachTo: document.body });
    await settle();
    await settle();
    expect(optionLabels(wrapper)).toContain("Acme Proxy");
    expect(wrapper.find('[data-testid="frame-stub"]').exists()).toBe(true);
    expect(wrapper.find(".el-segmented").exists()).toBe(false);
    expect(wrapper.find('[role="note"]').text()).toContain("acme.proxy");
  });

  it("a plugin with a form and a page switches with el-segmented", async () => {
    config.ids = ["acme.proxy"];
    pages.ref!.value = [page];
    wrapper = mount(PluginSettingsPanel, { attachTo: document.body });
    await settle();
    await settle();
    const segmented = wrapper.find(".el-segmented");
    expect(segmented.exists()).toBe(true);
    const labels = segmented.findAll(".el-segmented__item").map((item) => item.text());
    expect(labels).toEqual(["选项", "Rules"]);
    // The host form comes first; the page frame mounts only when its tab is chosen.
    expect(wrapper.find('[data-testid="frame-stub"]').exists()).toBe(false);
    await segmented.findAll(".el-segmented__item")[1]!.trigger("click");
    await settle();
    await settle();
    expect(wrapper.find('[data-testid="frame-stub"]').exists()).toBe(true);
    // Focus lands on the host header of the page, not on <body>.
    expect(document.activeElement).toBe(wrapper.find('[role="note"]').element);
    await wrapper.find(".el-segmented").findAll(".el-segmented__item")[0]!.trigger("click");
    await settle();
    expect(document.activeElement).toBe(wrapper.find(".app-plugin-settings__body").element);
  });

  it("open as tab asks Settings to close", async () => {
    config.ids = [];
    pages.ref!.value = [page];
    wrapper = mount(PluginSettingsPanel, { attachTo: document.body });
    await settle();
    await settle();
    await wrapper.find('[role="note"] button').trigger("click");
    expect(tabs.open).toHaveBeenCalledTimes(1);
    expect(wrapper.emitted("navigate")).toHaveLength(1);
  });

  it("closing Settings destroys the page frame", async () => {
    config.ids = [];
    pages.ref!.value = [page];
    wrapper = mount(PluginSettingsPanel, { props: { active: true }, attachTo: document.body });
    await settle();
    await settle();
    expect(wrapper.find('[data-testid="frame-stub"]').exists()).toBe(true);
    await wrapper.setProps({ active: false });
    await settle();
    expect(wrapper.find('[data-testid="frame-stub"]').exists()).toBe(false);
  });

  it("a disabled plugin's page leaves the picker with the snapshot", async () => {
    config.ids = [];
    pages.ref!.value = [page];
    wrapper = mount(PluginSettingsPanel, { attachTo: document.body });
    await settle();
    await settle();
    expect(wrapper.find('[data-testid="frame-stub"]').exists()).toBe(true);
    pages.ref!.value = [];
    await settle();
    expect(wrapper.find('[data-testid="frame-stub"]').exists()).toBe(false);
    expect(wrapper.text()).toContain("Acme Proxy");
  });
});
