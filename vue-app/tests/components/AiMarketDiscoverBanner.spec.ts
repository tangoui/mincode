import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { applySavedLocale } from "@/i18n/messages";
import { aiMarketLinkAction } from "@/domain/ai/ai-market-link";

const host = vi.hoisted(() => ({
  native: true,
  invoke: vi.fn(),
  push: vi.fn(),
  openView: vi.fn(),
  openPluginView: vi.fn(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => host.native,
  invokeBackend: host.invoke,
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));
vi.mock("vue-router", () => ({ useRouter: () => ({ push: host.push }) }));
vi.mock("@/stores/workbench", () => ({
  useWorkbenchStore: () => ({ openView: host.openView, openPluginView: host.openPluginView }),
}));

import AiMarketDiscoverBanner from "@/components/settings/AiMarketDiscoverBanner.vue";

const market = { id: "ai-market", name: "Connector & Skill Market", enabled: true, activeInWorkspace: true };

function listing(plugins: unknown[]) {
  host.invoke.mockImplementation(async (command: string) => (command === "plugin_host_list" ? plugins : undefined));
}

afterEach(async () => {
  host.native = true;
  for (const mock of [host.invoke, host.push, host.openView, host.openPluginView]) mock.mockReset();
  await applySavedLocale("zh");
});

describe("aiMarketLinkAction", () => {
  it("opens the market only when it is installed, enabled and active in this workspace", () => {
    expect(aiMarketLinkAction([market])).toBe("open");
    expect(aiMarketLinkAction([])).toBe("install");
    expect(aiMarketLinkAction([{ ...market, enabled: false }])).toBe("install");
    expect(aiMarketLinkAction([{ ...market, activeInWorkspace: false }])).toBe("install");
    expect(aiMarketLinkAction([{ ...market, id: "ai-prompts" }])).toBe("install");
  });
});

describe("Discover market banner", () => {
  it("opens the market view and closes Settings when the market is enabled", async () => {
    listing([market]);
    const tab = { id: "tab", view: "plugin-view", pluginView: { pluginId: "ai-market", viewId: "main" } };
    host.openPluginView.mockReturnValue(tab);
    const wrapper = mount(AiMarketDiscoverBanner);
    await flushPromises();

    expect(wrapper.text()).toContain("想找更多连接器和技能？打开「连接器与技能市场」");
    const button = wrapper.get('[data-testid="ai-market-banner-action"]');
    expect(button.text()).toBe("打开市场");
    await button.trigger("click");
    await flushPromises();
    expect(host.openPluginView).toHaveBeenCalledWith("ai-market", "main", "连接器与技能市场");
    expect(host.push).toHaveBeenCalledWith({ name: "plugin-view", params: { pluginId: "ai-market", viewId: "main" } });
    expect(wrapper.emitted("navigate")).toHaveLength(1);
  });

  it("keeps Settings open when the market tab cannot be opened", async () => {
    listing([market]);
    host.openPluginView.mockReturnValue(undefined);
    const wrapper = mount(AiMarketDiscoverBanner);
    await flushPromises();
    await wrapper.get('[data-testid="ai-market-banner-action"]').trigger("click");
    expect(wrapper.emitted("navigate")).toBeUndefined();
  });

  it("sends a disabled or missing market to the Plugin Manager search", async () => {
    for (const plugins of [[], [{ ...market, enabled: false }]]) {
      listing(plugins);
      host.openView.mockReturnValue({ id: "plugins" });
      const wrapper = mount(AiMarketDiscoverBanner);
      await flushPromises();
      const button = wrapper.get('[data-testid="ai-market-banner-action"]');
      expect(button.text()).toBe("去安装市场");
      await button.trigger("click");
      await flushPromises();
      expect(host.openView).toHaveBeenCalledWith("plugins");
      expect(host.push).toHaveBeenCalledWith({ name: "plugins", query: { search: "ai-market" } });
      expect(host.openPluginView).not.toHaveBeenCalled();
      expect(wrapper.emitted("navigate")).toHaveLength(1);
      wrapper.unmount();
      host.push.mockReset();
    }
  });

  it("offers the install button in browser preview and when the plugin list fails", async () => {
    host.native = false;
    const preview = mount(AiMarketDiscoverBanner);
    await flushPromises();
    expect(host.invoke).not.toHaveBeenCalled();
    expect(preview.get('[data-testid="ai-market-banner-action"]').text()).toBe("去安装市场");

    host.native = true;
    host.invoke.mockRejectedValue(new Error("host down"));
    const failed = mount(AiMarketDiscoverBanner);
    await flushPromises();
    expect(failed.get('[data-testid="ai-market-banner-action"]').text()).toBe("去安装市场");
  });

  it("follows the interface language", async () => {
    listing([market]);
    await applySavedLocale("en");
    const wrapper = mount(AiMarketDiscoverBanner);
    await flushPromises();
    expect(wrapper.text()).toContain("Looking for more connectors and skills? Open the Connector & Skill Market.");
    expect(wrapper.get('[data-testid="ai-market-banner-action"]').text()).toBe("Open the market");
  });
});
