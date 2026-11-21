import { mount, flushPromises } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { nextTick, reactive } from "vue";
import ProjectEditorSettings from "@/components/projects/ProjectEditorSettings.vue";
import { DEFAULT_EDITOR_PREFERENCES } from "@/domain/projects/types";
import { applySavedLocale } from "@/i18n/messages";

function setup(native = true) {
  const preferences = reactive({ ...DEFAULT_EDITOR_PREFERENCES });
  const wrapper = mount(ProjectEditorSettings, { props: { preferences, native,
    onChange: (patch) => Object.assign(preferences, patch) }, attachTo: document.body });
  return { wrapper, preferences };
}
describe("editor preferences panel", () => {
  it("updates the save-format switch and emits reset and close", async () => {
    const { wrapper, preferences } = setup();
    const toggle = wrapper.find('[role="switch"][aria-label="保存时格式化"]');
    expect(toggle.exists()).toBe(true);
    await toggle.trigger("click");
    expect(preferences.formatOnSave).toBe(true);
    const buttons = wrapper.findAll("button");
    await buttons.find((button) => button.text() === "恢复默认")!.trigger("click");
    await buttons.find((button) => button.text() === "关闭")!.trigger("click");
    expect(wrapper.emitted("reset")).toHaveLength(1);
    expect(wrapper.emitted("close")).toHaveLength(1);
    wrapper.unmount();
  });
  it("shows delay only in delay mode and bounds the number input", async () => {
    const { wrapper, preferences } = setup();
    expect(wrapper.find('[aria-label="延迟（毫秒）"]').exists()).toBe(false);
    preferences.autoSave = "afterDelay"; await nextTick();
    const input = wrapper.find('input[aria-label="延迟（毫秒）"]');
    expect(input.exists()).toBe(true);
    expect(input.attributes("aria-valuemin")).toBe("500");
    expect(input.attributes("aria-valuemax")).toBe("10000");
    wrapper.unmount();
  });
  it("labels browser fallback, offers formatter settings, and renders English", async () => {
    const { wrapper } = setup(false);
    expect(wrapper.text()).toContain("浏览器预览不写入文件");
    expect(wrapper.find('.el-select__wrapper.is-disabled').exists()).toBe(true);
    await wrapper.findAll("button").find((button) => button.text().includes("管理语言格式化器"))!.trigger("click");
    expect(wrapper.emitted("formatters")).toHaveLength(1);
    await applySavedLocale("en");
    await flushPromises();
    expect(wrapper.text()).toContain("Auto save");
    await applySavedLocale("zh");
    wrapper.unmount();
  });
});
