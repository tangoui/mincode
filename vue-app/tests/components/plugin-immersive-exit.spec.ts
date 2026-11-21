import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import PluginImmersiveExit from "@/components/plugins/PluginImmersiveExit.vue";
import { setPluginWindowFullscreen, usePluginTakeoverStore } from "@/stores/plugin-takeover";

let wrapper: VueWrapper | undefined;
let pinia = createPinia();
beforeEach(() => {
  pinia = createPinia();
  setActivePinia(pinia);
  setPluginWindowFullscreen(async () => undefined);
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

describe("immersive exit control", () => {
  it("renders only while a plugin is immersive and always exits", async () => {
    const store = usePluginTakeoverStore();
    wrapper = mount(PluginImmersiveExit, { attachTo: document.body, global: { plugins: [pinia] } });
    expect(wrapper.find("button").exists()).toBe(false);
    await store.enterImmersive("s-1");
    store.startCapture("s-1");
    await flushPromises();
    const button = wrapper.get("button");
    expect(button.attributes("aria-label")).toBeTruthy();
    expect(button.text()).toContain("Ctrl+Alt+Enter");
    await button.trigger("click");
    await flushPromises();
    expect(store.immersive).toBeNull();
    expect(store.capture).toBeNull();
    expect(wrapper.find("button").exists()).toBe(false);
  });
});
