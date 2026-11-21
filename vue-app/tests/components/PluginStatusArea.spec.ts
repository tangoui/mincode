import { defineComponent } from "vue";
import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { ElAlert, ElButton } from "element-plus";
import PluginStatusArea from "@/plugin-ui/PluginStatusArea.vue";

describe("PluginStatusArea", () => {
  it("keeps concurrent warnings, errors and recovery actions visible", async () => {
    let recovered = false;
    const wrapper = mount(PluginStatusArea, {
      props: { locale: "en" },
      slots: { default: defineComponent({
        components: { ElAlert, ElButton },
        setup: () => ({ recover: () => { recovered = true; } }),
        template: '<ElAlert title="TLS verification disabled" type="warning" :closable="false" /><ElAlert title="Read failed" type="error"><ElButton @click="recover">Retry</ElButton></ElAlert>',
      }) },
    });
    expect(wrapper.attributes("aria-label")).toBe("Connection and operation notices");
    expect(wrapper.findAll(".el-alert")).toHaveLength(2);
    await wrapper.find("button").trigger("click");
    expect(recovered).toBe(true);
    await wrapper.setProps({ locale: "zh-CN" });
    expect(wrapper.attributes("aria-label")).toBe("连接与操作提示");
    wrapper.unmount();
  });
});
