// The plugin manager entry in the activity bar carries a dot while installed
// market plugins have a newer compatible release, like Settings does for
// MinCode updates. The store checks in the background and the plugin manager
// publishes its own fresher catalog after it refreshes.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const host = vi.hoisted(() => ({
  plugins: [] as unknown[],
  market: [] as unknown[],
  failPlugins: false,
  failMarket: false,
  desktop: true,
}));

vi.mock("@/services/desktop-bridge", () => ({ isDesktopRuntime: () => host.desktop }));
vi.mock("@/services/plugin-service", () => ({
  listPlugins: vi.fn(async () => {
    if (host.failPlugins) throw new Error("plugin host down");
    return host.plugins;
  }),
}));
vi.mock("@/services/market-service", () => ({
  listMarketPlugins: vi.fn(async () => {
    if (host.failMarket) throw new Error("offline");
    return { items: host.market, truncated: false };
  }),
}));

import { usePluginUpdatesStore } from "@/stores/plugin-updates";
import { listMarketPlugins } from "@/services/market-service";

const cloud = (key: string, latest: string | null) => ({
  product_key: key, name: key, category: "other", tags: [], latest_published_version: latest,
  host_compatibility: "compatible", compatible_version: latest,
});
const installed = (id: string, version: string, source = "local") => ({ id, version, source, name: id, category: "other" });

beforeEach(() => {
  setActivePinia(createPinia());
  host.plugins = [installed("knowledge-base", "0.1.0"), installed("redis", "1.0.0"), installed("builtin", "0.1.0", "builtin")];
  host.market = [cloud("knowledge-base", "0.2.0"), cloud("redis", "1.0.0"), cloud("builtin", "0.5.0"), cloud("market-only", "1.0.0")];
  host.failPlugins = false;
  host.failMarket = false;
  host.desktop = true;
  vi.mocked(listMarketPlugins).mockClear();
});

afterEach(() => {
  usePluginUpdatesStore().stopAutoCheck();
  vi.useRealTimers();
});

describe("plugin updates store", () => {
  it("lists installed market plugins with a newer compatible release", async () => {
    const store = usePluginUpdatesStore();
    await store.refresh();
    expect(store.upgradableIds).toEqual(["knowledge-base"]);
    expect(store.count).toBe(1);
  });

  it("keeps the last known result when a background check fails", async () => {
    const store = usePluginUpdatesStore();
    await store.refresh();
    host.failMarket = true;
    await store.refresh();
    expect(store.upgradableIds).toEqual(["knowledge-base"]);
    host.failMarket = false;
    host.failPlugins = true;
    await expect(store.refresh()).resolves.toBeUndefined();
    expect(store.count).toBe(1);
  });

  it("clears once the plugin manager publishes a catalog without upgrades", async () => {
    const store = usePluginUpdatesStore();
    await store.refresh();
    store.publish(["redis"]);
    expect(store.upgradableIds).toEqual(["redis"]);
    store.publish([]);
    expect(store.count).toBe(0);
  });

  it("checks after a startup delay and then periodically, never outside the desktop app", async () => {
    vi.useFakeTimers();
    const store = usePluginUpdatesStore();
    store.startAutoCheck();
    expect(listMarketPlugins).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(listMarketPlugins).toHaveBeenCalledTimes(1);
    expect(store.count).toBe(1);
    await vi.advanceTimersByTimeAsync(6 * 60 * 60 * 1000);
    expect(listMarketPlugins).toHaveBeenCalledTimes(2);
    store.stopAutoCheck();

    setActivePinia(createPinia());
    host.desktop = false;
    const browser = usePluginUpdatesStore();
    browser.startAutoCheck();
    await vi.advanceTimersByTimeAsync(7 * 60 * 60 * 1000);
    expect(listMarketPlugins).toHaveBeenCalledTimes(2);
  });
});
