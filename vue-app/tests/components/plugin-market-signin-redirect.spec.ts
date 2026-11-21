import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import PluginManagerWorkspaceView from "@/views/PluginManagerWorkspaceView.vue";
import { settingsNavigationRequest } from "@/services/settings-navigation";

const mocks = vi.hoisted(() => ({
  installPreview: vi.fn(),
  cloudStatus: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
}));

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
  listPlugins: async () => [],
  listPluginViews: async () => [],
  previewLocalPlugin: vi.fn(),
  previewRemotePlugin: vi.fn(),
  upgradeLocalPlugin: vi.fn(),
  installLocalPlugin: vi.fn(),
  reloadPlugin: vi.fn(),
  setPluginEnabled: vi.fn(),
  setPluginScope: vi.fn(),
  clearPluginData: vi.fn(),
  exportPluginDataArchive: vi.fn(),
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

async function mountManager() {
  // A single active Pinia keeps the stores the view resolves consistent across mounts.
  setActivePinia(createPinia());
  const wrapper = mount(PluginManagerWorkspaceView);
  await flushPromises();
  return wrapper;
}

function marketInstallButton(wrapper: Awaited<ReturnType<typeof mountManager>>) {
  return wrapper.findAll("button").find((button) => button.text().includes("从云端安装"));
}

beforeEach(() => {
  settingsNavigationRequest.value = { seq: 0 };
  mocks.installPreview.mockReset();
  mocks.cloudStatus.mockReset().mockResolvedValue({ signedIn: true, configured: true, deviceAvailable: false });
  mocks.warning.mockReset();
  mocks.error.mockReset();
});

describe("plugin manager market install sign-in redirect", () => {
  it("opens sign-in before package preflight, including the paid-plan gate", async () => {
    mocks.cloudStatus.mockResolvedValueOnce({ signedIn: false, configured: true, deviceAvailable: false });
    // Paid products can refuse before DownloadMarketArtifact is reached. No preview should run.
    mocks.installPreview.mockRejectedValueOnce(new Error("market install: this plugin requires a paid MinCode plan"));
    const wrapper = await mountManager();
    await marketInstallButton(wrapper)!.trigger("click");
    await flushPromises();

    expect(settingsNavigationRequest.value.category).toBe("cloud");
    expect(mocks.installPreview).not.toHaveBeenCalled();
    expect(mocks.warning).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it("ignores a late signed-out status after the view is unmounted", async () => {
    let resolveStatus!: (value: { signedIn: boolean }) => void;
    mocks.cloudStatus.mockReturnValueOnce(new Promise((resolve) => { resolveStatus = resolve; }));
    const wrapper = await mountManager();
    await marketInstallButton(wrapper)!.trigger("click");
    wrapper.unmount();
    resolveStatus({ signedIn: false });
    await flushPromises();

    expect(settingsNavigationRequest.value.seq).toBe(0);
    expect(mocks.installPreview).not.toHaveBeenCalled();
    expect(mocks.warning).not.toHaveBeenCalled();
  });

  it("opens Settings → Cloud when the backend refuses for a missing session", async () => {
    mocks.installPreview.mockRejectedValueOnce(
      new Error("market install: 请先在设置中登录云账号，再从插件市场安装"),
    );
    const wrapper = await mountManager();
    const button = marketInstallButton(wrapper);
    expect(button, "the cloud catalog should offer an install button").toBeTruthy();

    await button!.trigger("click");
    await flushPromises();

    expect(mocks.installPreview).toHaveBeenCalledWith("demo.cloud");
    expect(settingsNavigationRequest.value.category).toBe("cloud");
    expect(settingsNavigationRequest.value.seq).toBe(1);
    expect(mocks.warning).toHaveBeenCalledTimes(1);
    expect(String(mocks.warning.mock.calls[0][0].message)).toContain("云账号");
    // The preview dialog is closed instead of staying open on an error the user cannot act on.
    expect(document.body.textContent).not.toContain("需要登录云账号");
    wrapper.unmount();
  });

  it("treats a non-member session the same way", async () => {
    mocks.installPreview.mockRejectedValueOnce(
      new Error("market install: 当前会话不是云端会员账号，请重新登录"),
    );
    const wrapper = await mountManager();
    await marketInstallButton(wrapper)!.trigger("click");
    await flushPromises();

    expect(settingsNavigationRequest.value.category).toBe("cloud");
    expect(mocks.warning).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it("keeps other install failures inside the preview dialog", async () => {
    mocks.installPreview.mockRejectedValueOnce(new Error("market install: no published release yet"));
    const wrapper = await mountManager();
    await marketInstallButton(wrapper)!.trigger("click");
    await flushPromises();

    expect(settingsNavigationRequest.value.seq).toBe(0);
    expect(mocks.warning).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("该插件还没有可下载的版本");
    wrapper.unmount();
  });
});
