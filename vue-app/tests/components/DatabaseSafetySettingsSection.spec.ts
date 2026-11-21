import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";

const bridge = vi.hoisted(() => ({ invoke: vi.fn() }));
vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: bridge.invoke,
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import DatabaseSafetySettingsSection from "@/components/settings/DatabaseSafetySettingsSection.vue";
import { sqlSafetySettingsVersion } from "@/services/sql-safety-settings-cache";

const GIB = 2 ** 30;
const defaults = { enabled: true, highRiskRows: 1000, retentionDays: 7, maxSnapshotRows: 100000, maxTotalBytes: 2 * GIB };

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  bridge.invoke.mockReset();
});

async function mountSection() {
  wrapper = mount(DatabaseSafetySettingsSection, { attachTo: document.body });
  await flushPromises();
  return wrapper;
}

function numberInputs() {
  return wrapper!.findAll<HTMLInputElement>(".app-sql-safety-settings__number input");
}

function saveButton() {
  return wrapper!.find<HTMLButtonElement>('button[type="submit"]');
}

describe("SQL safety net settings", () => {
  it("shows the backend values with the total limit in GiB and saves it back in bytes", async () => {
    bridge.invoke.mockImplementation(async (command: string, args: { settings?: unknown }) =>
      command === "db_safety_settings_get" ? defaults : args.settings);
    await mountSection();
    expect(bridge.invoke).toHaveBeenCalledWith("db_safety_settings_get", {});
    expect(numberInputs().map((input) => input.element.value)).toEqual(["1000", "7", "100000", "2"]);
    expect(saveButton().attributes("disabled")).toBeDefined();

    const total = numberInputs()[3];
    await total.setValue("0.0625");
    await total.trigger("change");
    const days = numberInputs()[1];
    await days.setValue("30");
    await days.trigger("change");
    const before = sqlSafetySettingsVersion();
    await wrapper!.find("form").trigger("submit");
    await flushPromises();

    expect(bridge.invoke).toHaveBeenLastCalledWith("db_safety_settings_set", {
      settings: { ...defaults, retentionDays: 30, maxTotalBytes: 64 * 2 ** 20 },
    });
    expect(sqlSafetySettingsVersion()).toBe(before + 1);
    expect(saveButton().attributes("disabled")).toBeDefined();
  });

  it("explains the consequence when the safety net is switched off", async () => {
    bridge.invoke.mockResolvedValue(defaults);
    await mountSection();
    expect(wrapper!.text()).not.toContain("只保留通用确认框，不留底、不能撤回");
    await wrapper!.find(".el-switch").trigger("click");
    expect(wrapper!.text()).toContain("只保留通用确认框，不留底、不能撤回");
  });

  it("shows the backend error when saving fails and keeps the edit", async () => {
    bridge.invoke.mockImplementation(async (command: string) => {
      if (command === "db_safety_settings_get") return defaults;
      throw new Error("maxTotalBytes must be between 64 MiB and 20 GiB, got 1");
    });
    await mountSection();
    const threshold = numberInputs()[0];
    await threshold.setValue("500");
    await threshold.trigger("change");
    await wrapper!.find("form").trigger("submit");
    await flushPromises();
    expect(wrapper!.text()).toContain("保存失败：maxTotalBytes must be between 64 MiB and 20 GiB, got 1");
    expect(numberInputs()[0].element.value).toBe("500");
    expect(saveButton().attributes("disabled")).toBeUndefined();

    // Editing again clears the stale error.
    await threshold.setValue("600");
    await threshold.trigger("change");
    await flushPromises();
    expect(wrapper!.text()).not.toContain("保存失败");
  });

  it("does not save an empty field and shows the allowed range", async () => {
    bridge.invoke.mockResolvedValue(defaults);
    await mountSection();
    const snapshot = numberInputs()[2];
    await snapshot.setValue("");
    await snapshot.trigger("change");
    // ElFormItem shows its error message after a short debounce.
    await vi.waitFor(() => expect(wrapper!.text()).toContain("取值范围 1 ~ 100000"));
    expect(saveButton().attributes("disabled")).toBeDefined();
    await wrapper!.find("form").trigger("submit");
    await flushPromises();
    expect(bridge.invoke).toHaveBeenCalledTimes(1);
  });

  it("shows the load error instead of a form with guessed values", async () => {
    bridge.invoke.mockRejectedValue(new Error("无法确定 SQL 安全网数据目录"));
    await mountSection();
    expect(wrapper!.text()).toContain("读取 SQL 安全网设置失败：无法确定 SQL 安全网数据目录");
    expect(wrapper!.find("form").exists()).toBe(false);
  });
});
