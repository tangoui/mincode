import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import AppSearchField from "@/components/ui/AppSearchField.vue";

describe("AppSearchField", () => {
  it("emits model and input updates while typing", async () => {
    const wrapper = mount(AppSearchField, { props: { modelValue: "", placeholder: "Search" } });

    await wrapper.find("input").setValue("users");

    expect(wrapper.emitted("update:modelValue")).toEqual([["users"]]);
    expect(wrapper.emitted("input")).toEqual([["users"]]);
    expect(wrapper.find("input").attributes("aria-label")).toBe("Search");
  });

  it("submits on Enter", async () => {
    const wrapper = mount(AppSearchField, { props: { modelValue: "users" } });

    await wrapper.find("input").trigger("keyup", { key: "Enter" });

    expect(wrapper.emitted("submit")).toHaveLength(1);
  });

  it("shows the clear button only when clearable and non-empty", async () => {
    const wrapper = mount(AppSearchField, {
      props: { modelValue: "", clearable: true, clearLabel: "Clear search" },
    });
    expect(wrapper.find(".app-search-field__clear").exists()).toBe(false);

    await wrapper.setProps({ modelValue: "users" });
    const clear = wrapper.find(".app-search-field__clear");
    expect(clear.attributes("aria-label")).toBe("Clear search");

    await clear.trigger("click");

    expect(wrapper.emitted("update:modelValue")).toEqual([[""]]);
    expect(wrapper.emitted("input")).toEqual([[""]]);
    expect(wrapper.emitted("clear")).toHaveLength(1);
  });

  it("shows the clear button when clearable is enabled after mount", async () => {
    const wrapper = mount(AppSearchField, { props: { modelValue: "users", clearable: false } });
    expect(wrapper.find(".app-search-field__clear").exists()).toBe(false);

    await wrapper.setProps({ clearable: true });

    expect(wrapper.find(".app-search-field__clear").exists()).toBe(true);
  });
});
