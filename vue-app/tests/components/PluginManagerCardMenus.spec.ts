import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import PluginManagerWorkspaceView from "@/views/PluginManagerWorkspaceView.vue";

const mocks = vi.hoisted(() => ({
  installPreview: vi.fn(),
  cloudStatus: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
  setScope: vi.fn(async () => undefined),
  exportData: vi.fn(async () => undefined),
}));

const plugins = vi.hoisted(() => ({ list: [] as unknown[] }));

vi.mock("@/services/plugin-full-trust", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/plugin-full-trust")>()),
  getFullTrust: vi.fn(async () => ({ trusted: false, subject: { eligible: true } })),
  getFullTrustAudit: vi.fn(async () => []),
  setFullTrust: vi.fn(),
  listFullTrust: vi.fn(async () => []),
}))

vi.mock("element-plus", async (importOriginal) => ({
  ...(await importOriginal<typeof import("element-plus")>()),
  ElMessage: { warning: mocks.warning, error: mocks.error, success: vi.fn(), info: vi.fn() },
  ElMessageBox: { confirm: vi.fn() },
}));

vi.mock("vue-router", () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: vi.fn() }),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  invokeBackend: vi.fn(async () => ({})),
  listenAppEvent: vi.fn(() => () => {}),
  emitAppEvent: vi.fn(),
  chooseNativeDirectory: vi.fn(async () => ""),
  chooseNativeFile: vi.fn(async () => ""),
  chooseNativeSaveFile: vi.fn(async () => ""),
}));

vi.mock("@/services/cloud-companion-service", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/cloud-companion-service")>()),
  cloudStatus: mocks.cloudStatus,
}));

vi.mock("@/services/market-service", () => ({
  listMarketPlugins: async () => ({
    items: [
      {
        product_key: "demo.cloud",
        name: "Demo Cloud Plugin",
        slug: "demo-cloud-plugin",
        description: "A published plugin used by the sign-in redirect test.",
        homepage: "",
        icon: "",
        category: "other",
        tags: [],
        author: null,
        cover_url: "",
        latest_published_version: "1.2.3",
        updated_at: 1,
        requires_paid_host: false,
        min_host_version: "",
        max_host_version_exclusive: "",
        host_compatibility: "compatible",
        compatible_version: "1.2.3",
        download_count: 0,
        like_count: 0,
      },
    ],
    truncated: false,
  }),
  previewMarketPluginInstall: mocks.installPreview,
}));

vi.mock("@/services/plugin-service", () => ({
  getPluginLogs: vi.fn(),
  listPluginCategories: async () => [],
  listPlugins: async () => plugins.list,
  listPluginViews: async () => [],
  previewLocalPlugin: vi.fn(),
  previewRemotePlugin: vi.fn(),
  upgradeLocalPlugin: vi.fn(),
  installLocalPlugin: vi.fn(),
  reloadPlugin: vi.fn(),
  setPluginEnabled: vi.fn(),
  setPluginScope: mocks.setScope,
  clearPluginData: vi.fn(),
  exportPluginDataArchive: mocks.exportData,
  importPluginDataArchive: vi.fn(),
  inspectPluginDataArchive: vi.fn(),
  uninstallPlugin: vi.fn(),
}));

vi.mock("@/services/plugin-menu-preferences", () => ({
  loadPluginMenu: async () => [],
  savePluginMenu: vi.fn(),
  PLUGIN_CATALOG_EVENT: "mincode:test-plugin-catalog-changed",
}));

vi.mock("@/services/plugin-configuration-service", () => ({
  listConfigurablePlugins: async () => [],
}));


function plugin(index: number) {
  return {
    id: `demo.local-${index}`, name: `Local ${index}`, version: "1.0.0", description: "Local plugin", author: "me",
    enabled: false, scope: "global", workspaces: [], activeInWorkspace: true, source: "local", runtime: "process",
    state: "stopped", capabilities: ["ui"], apiVersion: 2,
  };
}

async function mountManager() {
  setActivePinia(createPinia());
  const wrapper = mount(PluginManagerWorkspaceView, { attachTo: document.body });
  await flushPromises();
  return wrapper;
}

const card = (wrapper: Awaited<ReturnType<typeof mountManager>>, name: string) =>
  wrapper.findAll(".plugin-manager-card").find((item) => item.find(".plugin-manager-meta__title strong").text() === name)!;
const menuItem = (label: string) =>
  [...document.querySelectorAll<HTMLElement>(".app-context-menu__popper .el-dropdown-menu__item")].find((item) => item.textContent?.trim() === label);

beforeEach(() => {
  plugins.list = [plugin(1), plugin(2), plugin(3)];
  mocks.cloudStatus.mockReset().mockResolvedValue({ signedIn: true, configured: true, deviceAvailable: false });
  mocks.setScope.mockClear();
  mocks.exportData.mockClear();
});

describe("plugin manager card menus", () => {
  it("renders no select or dropdown per plugin card", async () => {
    // A page of 24 cards each with an ElSelect and an ElDropdown was slow to show and to page through.
    const wrapper = await mountManager();
    const cards = wrapper.findAll(".plugin-manager-card");
    expect(cards.length).toBeGreaterThanOrEqual(3);
    for (const name of ["ElSelect", "ElDropdown"]) {
      expect(wrapper.findAllComponents({ name }).filter((item) => item.element.closest?.(".plugin-manager-card")), name).toHaveLength(0);
    }
    wrapper.unmount();
  });

  it("mounts card tags without enter transitions and cards without their own scrollbar", async () => {
    // Paging mounts 24 cards: ~6 tags each zooming in at once made the next frame 50-140 ms.
    const wrapper = await mountManager();
    const inCards = (name: string) => wrapper.findAllComponents({ name }).filter((item) => item.element.closest?.(".plugin-manager-card"));
    const tags = inCards("ElTag");
    expect(tags.length).toBeGreaterThan(0);
    for (const tag of tags) expect(tag.props("disableTransitions")).toBe(true);
    expect(inCards("ElScrollbar")).toHaveLength(0);
    wrapper.unmount();
  });

  it("changes a plugin's scope through the shared menu", async () => {
    const wrapper = await mountManager();
    const scope = card(wrapper, "Local 2").get(".plugin-manager-scope-select");
    expect(scope.text()).toContain("所有工作区");
    await scope.trigger("click");
    await flushPromises();
    menuItem("指定工作区")!.click();
    await flushPromises();
    expect(mocks.setScope).toHaveBeenCalledWith("demo.local-2", "workspace", expect.any(Array));
    expect(menuItem("指定工作区")).toBeUndefined();
    wrapper.unmount();
  });

  it("runs a data command for the card that opened the menu", async () => {
    const wrapper = await mountManager();
    await card(wrapper, "Local 3").get(".plugin-manager-data-button").trigger("click");
    await flushPromises();
    expect(menuItem("导出数据")).toBeTruthy();
    expect(menuItem("清除数据")).toBeTruthy();
    wrapper.unmount();
  });
});
