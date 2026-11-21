import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import AppFilterButton from "@/components/ui/AppFilterButton.vue";

describe("AppFilterButton", () => {
  it("exposes its label and pressed state to assistive tech", async () => {
    const wrapper = mount(AppFilterButton, { props: { label: "Filter rows" } });
    const button = wrapper.get("button");

    expect(button.attributes("aria-label")).toBe("Filter rows");
    expect(button.attributes("title")).toBe("Filter rows");
    expect(button.attributes("aria-pressed")).toBe("false");
    expect(button.classes()).not.toContain("is-active");

    await wrapper.setProps({ active: true, title: "3 filters active" });
    expect(button.attributes("aria-pressed")).toBe("true");
    expect(button.attributes("title")).toBe("3 filters active");
    expect(button.classes()).toContain("is-active");
  });

  it("emits click unless disabled", async () => {
    const wrapper = mount(AppFilterButton, { props: { label: "Filter rows" } });
    await wrapper.get("button").trigger("click");
    expect(wrapper.emitted("click")).toHaveLength(1);

    await wrapper.setProps({ disabled: true });
    await wrapper.get("button").trigger("click");
    expect(wrapper.get("button").attributes("disabled")).toBeDefined();
    expect(wrapper.emitted("click")).toHaveLength(1);
  });
});
