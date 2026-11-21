// The activity bar flags pending updates: Settings for a newer MinCode and the
// plugin manager for installed plugins with a newer market release.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createMemoryHistory, createRouter } from "vue-router";
import { defineComponent } from "vue";

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => false,
  canInvoke: () => false,
  invokeBackend: vi.fn(),
  listenAppEvent: vi.fn(() => () => undefined),
}));
vi.mock("@/services/persistent-store", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/persistent-store")>()),
  loadPersistedValue: vi.fn().mockResolvedValue(null),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/services/plugin-service", () => ({ listPluginViews: vi.fn().mockResolvedValue([]) }));
vi.mock("@/services/plugin-menu-preferences", () => ({
  loadPluginMenu: vi.fn().mockResolvedValue([]),
  normalizePluginMenu: (value: unknown) => (Array.isArray(value) ? value : []),
  PLUGIN_MENU_EVENT: "test-plugin-menu",
  PLUGIN_CATALOG_EVENT: "test-plugin-catalog",
}));
vi.mock("@/router/workspace-views", () => ({ preloadWorkspaceView: vi.fn() }));
vi.mock("@/services/keyboard-platform", () => ({ currentKeyboardPlatform: () => "mac" }));

import AppSidebar from "@/components/layout/AppSidebar.vue";
import { usePluginUpdatesStore } from "@/stores/plugin-updates";

let wrapper: VueWrapper | undefined;

async function mountSidebar() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/", name: "dashboard", component: defineComponent({ render: () => null }) }] });
  await router.push("/");
  wrapper = mount(AppSidebar, { global: { plugins: [router] }, attachTo: document.body });
  await flushPromises();
  return wrapper;
}

function pluginManagerButton() {
  return wrapper!.get(".app-activity-bar__plugin-manager button");
}

beforeEach(() => setActivePinia(createPinia()));
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

describe("activity bar update indicators", () => {
  it("shows a dot and a count on the plugin manager only while upgrades are pending", async () => {
    await mountSidebar();
    expect(wrapper!.find('[data-testid="plugin-updates-dot"]').exists()).toBe(false);
    expect(pluginManagerButton().attributes("aria-label")).toBe("插件");

    usePluginUpdatesStore().publish(["knowledge-base", "redis"]);
    await flushPromises();
    expect(wrapper!.find('[data-testid="plugin-updates-dot"]').exists()).toBe(true);
    expect(pluginManagerButton().attributes("aria-label")).toBe("插件（2 个插件可升级）");

    usePluginUpdatesStore().publish([]);
    await flushPromises();
    expect(wrapper!.find('[data-testid="plugin-updates-dot"]').exists()).toBe(false);
    expect(pluginManagerButton().attributes("aria-label")).toBe("插件");
  });
});
