import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { applySavedLocale } from "@/i18n/messages";
import { formatDataBytes, groupDataUsage, type DataUsage } from "@/domain/settings/data-usage";

const bridge = vi.hoisted(() => ({
  native: true,
  invoke: vi.fn(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => bridge.native,
  invokeBackend: bridge.invoke,
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import DataStorageUsagePanel from "@/components/settings/DataStorageUsagePanel.vue";

const usage: DataUsage = {
  dataRoot: "/Users/me/Library/Application Support/com.mincode.desktop",
  cacheRoot: "/Users/me/Library/Caches/MinCode",
  totalBytes: 5 * 1024 * 1024 + 2048,
  items: [
    { id: "database", group: "data", path: "/Users/me/Library/Application Support/com.mincode.desktop/mincode.sqlite3", bytes: 5 * 1024 * 1024, files: 3, exists: true },
    { id: "pluginData", group: "data", path: "/Users/me/Library/Application Support/com.mincode.desktop", bytes: 0, files: 0, exists: false },
    { id: "updates", group: "cache", path: "/Users/me/Library/Caches/MinCode/updates", bytes: 2048, files: 1, exists: true },
  ],
};

afterEach(() => {
  bridge.native = true;
  bridge.invoke.mockReset();
});

describe("data usage helpers", () => {
  it("formats sizes and groups items by location", () => {
    expect(formatDataBytes(0)).toBe("0 B");
    expect(formatDataBytes(1536)).toBe("1.5 KB");
    expect(formatDataBytes(5 * 1024 * 1024)).toBe("5.0 MB");
    expect(formatDataBytes(-1)).toBe("—");
    const groups = groupDataUsage(usage);
    expect(groups.map((group) => [group.group, group.bytes])).toEqual([["data", 5 * 1024 * 1024], ["cache", 2048]]);
    expect(groupDataUsage({ ...usage, cacheRoot: "", items: usage.items.filter((item) => item.group === "data") })).toHaveLength(1);
  });
});

describe("storage details panel", () => {
  it("shows the data location, each category's size and reveals paths", async () => {
    bridge.invoke.mockImplementation(async (command: string) => (command === "app_data_usage" ? usage : undefined));
    const wrapper = mount(DataStorageUsagePanel, { attachTo: document.body });
    await flushPromises();

    expect(bridge.invoke).toHaveBeenCalledWith("app_data_usage", {}, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(wrapper.text()).toContain("存储明细");
    expect(wrapper.text()).toContain(usage.dataRoot);
    expect(wrapper.text()).toContain(usage.cacheRoot);
    expect(wrapper.find('[data-item="database"]').text()).toContain("5.0 MB");
    expect(wrapper.find('[data-item="database"]').text()).toContain("3 个文件");
    expect(wrapper.find('[data-item="pluginData"]').text()).toContain("无");
    expect(wrapper.find('[data-item="pluginData"] button').attributes("disabled")).toBeDefined();

    await wrapper.find('[data-group="data"] .settings-data-usage__group-head button').trigger("click");
    expect(bridge.invoke).toHaveBeenLastCalledWith("local_fs_show_in_file_manager", { path: usage.dataRoot, select: true });
    await wrapper.find('[data-item="updates"] button').trigger("click");
    expect(bridge.invoke).toHaveBeenLastCalledWith("local_fs_show_in_file_manager", { path: usage.items[2].path, select: true });

    await applySavedLocale("en");
    await flushPromises();
    expect(wrapper.text()).toContain("Storage details");
    await applySavedLocale("zh");
    wrapper.unmount();
  });

  it("reports failures and refreshes on demand", async () => {
    bridge.invoke.mockRejectedValueOnce(new Error("disk unavailable")).mockResolvedValueOnce(usage);
    const wrapper = mount(DataStorageUsagePanel, { attachTo: document.body });
    await flushPromises();
    expect(wrapper.text()).toContain("统计失败：disk unavailable");

    await wrapper.find(".settings-data-usage__header button").trigger("click");
    await flushPromises();
    expect(bridge.invoke).toHaveBeenCalledTimes(2);
    expect(wrapper.text()).toContain("总占用");
    wrapper.unmount();
  });

  it("explains that details need the desktop app in a browser preview", async () => {
    bridge.native = false;
    const wrapper = mount(DataStorageUsagePanel, { attachTo: document.body });
    await flushPromises();
    expect(bridge.invoke).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("存储明细仅在桌面版中可用。");
    wrapper.unmount();
  });
});
