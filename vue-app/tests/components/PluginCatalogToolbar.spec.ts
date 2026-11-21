import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { ElDropdown, ElSelect } from "element-plus";
import PluginCatalogToolbar from "@/components/plugins/PluginCatalogToolbar.vue";

function toolbar(busy = false) {
  return mount(PluginCatalogToolbar, {
    props: { filters: { category: "all", source: "all", status: "all", search: "" }, sourcePath: "", busy },
  });
}

describe("PluginCatalogToolbar", () => {
  it("installs from a path typed into the search box instead of a separate field", async () => {
    const wrapper = toolbar();
    expect(wrapper.find(".app-plugin-toolbar__path").exists()).toBe(false);
    expect(wrapper.find(".app-plugin-toolbar__preview").exists()).toBe(false);
    const input = wrapper.get(".app-search-field input");
    await input.setValue("/tmp/example.min");
    expect(wrapper.emitted("update:filters")?.at(-1)).toEqual([{ category: "all", source: "all", status: "all", search: "/tmp/example.min" }]);
    // The parent owns the filters; reflect the typed value back like v-model does.
    await wrapper.setProps({ filters: { category: "all", source: "all", status: "all", search: "/tmp/example.min" } });
    expect(wrapper.emitted("update:sourcePath")?.at(-1)).toEqual(["/tmp/example.min"]);
    await wrapper.setProps({ sourcePath: "/tmp/example.min" });
    await wrapper.get(".app-plugin-toolbar__preview").trigger("click");
    await input.trigger("keyup", { key: "Enter" });
    expect(wrapper.emitted("preview")).toHaveLength(2);
    // Ordinary search text clears the install source and hides the preview action.
    await wrapper.setProps({ filters: { category: "all", source: "all", status: "all", search: "redis" } });
    expect(wrapper.emitted("update:sourcePath")?.at(-1)).toEqual([""]);
    await wrapper.setProps({ sourcePath: "" });
    expect(wrapper.find(".app-plugin-toolbar__preview").exists()).toBe(false);
    await input.trigger("keyup", { key: "Enter" });
    expect(wrapper.emitted("preview")).toHaveLength(2);
    wrapper.unmount();
  });

  it("shows a picked package in the search box", async () => {
    const wrapper = toolbar();
    await wrapper.setProps({ sourcePath: "/Users/me/demo.min" });
    expect(wrapper.emitted("update:filters")?.at(-1)).toEqual([{ category: "all", source: "all", status: "all", search: "/Users/me/demo.min" }]);
    wrapper.unmount();
  });

  it("installs a picked package from the button and a picked directory from its menu", async () => {
    const wrapper = toolbar();
    const dropdown = wrapper.getComponent(ElDropdown);
    dropdown.vm.$emit("click");
    dropdown.vm.$emit("command", "directory");
    expect(wrapper.emitted("choosePackage")).toHaveLength(1);
    expect(wrapper.emitted("chooseDirectory")).toHaveLength(1);
    expect(wrapper.emitted("preview")).toBeUndefined();
    wrapper.unmount();
  });

  it("preserves the other filters when changing source or status", async () => {
    const wrapper = toolbar();
    const selects = wrapper.findAllComponents(ElSelect);
    selects[0]!.vm.$emit("update:modelValue", "cloud");
    selects[1]!.vm.$emit("update:modelValue", "installed");
    expect(wrapper.emitted("update:filters")).toEqual([
      [{ category: "all", source: "cloud", status: "all", search: "" }],
      [{ category: "all", source: "all", status: "installed", search: "" }],
    ]);
    wrapper.unmount();
  });

  it("blocks all installation actions while busy", async () => {
    const wrapper = toolbar(true);
    wrapper.getComponent(ElDropdown).vm.$emit("click");
    wrapper.getComponent(ElDropdown).vm.$emit("command", "directory");
    await wrapper.setProps({ filters: { category: "all", source: "all", status: "all", search: "/tmp/example.min" }, sourcePath: "/tmp/example.min" });
    await wrapper.get(".app-search-field input").trigger("keyup", { key: "Enter" });
    expect(wrapper.get(".app-plugin-toolbar__preview").attributes("disabled")).toBeDefined();
    expect(wrapper.emitted("choosePackage")).toBeUndefined();
    expect(wrapper.emitted("chooseDirectory")).toBeUndefined();
    expect(wrapper.emitted("preview")).toBeUndefined();
    wrapper.unmount();
  });
  it("offers an upgradable status between installed and not installed", async () => {
    const wrapper = toolbar();
    const status = wrapper.findAllComponents(ElSelect)[1]!;
    const options = status.findAllComponents({ name: "ElOption" }).map(option => [option.props("value"), option.props("label")]);
    expect(options).toEqual([
      ["all", "全部状态"], ["installed", "已安装"], ["upgradable", "可升级"], ["available", "未安装"],
    ]);
    status.vm.$emit("update:modelValue", "upgradable");
    expect(wrapper.emitted("update:filters")?.at(-1)).toEqual([{ category: "all", source: "all", status: "upgradable", search: "" }]);
    wrapper.unmount();
  });
});
