import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import PluginConnectionBar from "@/plugin-ui/PluginConnectionBar.vue";

const labels = {
  waiting: "Waiting", connected: "Connected", disconnected: "Disconnected", unsaved: "Unsaved connection",
  profile: "Connection", manageProfiles: "Manage connections", modified: "Modified",
  newConnection: "New", settings: "Settings", connect: "Connect", disconnect: "Disconnect",
};
const profiles = [{ id: "a", name: "Alpha", endpoint: "a.example:2379" }, { id: "b", name: "Beta", endpoint: "b.example:2379" }];

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => { wrapper?.unmount(); document.body.innerHTML = ""; });

function buttons() { return wrapper!.findAll("button").map((button) => button.text()); }

describe("PluginConnectionBar", () => {
  it("shows the waiting state and disables actions until the host is ready", () => {
    wrapper = mount(PluginConnectionBar, { props: { labels, ready: false, connected: false, modelValue: "", profiles } });
    expect(wrapper.text()).toContain("Waiting");
    const connect = wrapper.findAll("button").find((button) => button.text() === "Connect")!;
    expect(connect.attributes("disabled")).toBeDefined();
    expect(wrapper.find(".el-select__wrapper").classes()).toContain("is-disabled");
  });

  it("swaps Connect for Disconnect only when the plugin supports disconnecting", async () => {
    wrapper = mount(PluginConnectionBar, { props: { labels: { ...labels, connect: "Reconnect" }, ready: true, connected: true, modelValue: "a", profiles } });
    expect(buttons()).toContain("Reconnect");
    expect(buttons()).not.toContain("Disconnect");
    await wrapper.setProps({ disconnectable: true });
    expect(buttons()).toContain("Disconnect");
    expect(buttons()).not.toContain("Reconnect");
    await wrapper.findAll("button").find((button) => button.text() === "Disconnect")!.trigger("click");
    expect(wrapper.emitted("disconnect")).toHaveLength(1);
  });

  it("emits toolbar actions and shows the modified and status overrides", async () => {
    wrapper = mount(PluginConnectionBar, {
      props: { labels, ready: true, connected: true, statusLabel: "node-1 · v2.10", statusTitle: "nats://a", modelValue: "a", profiles, modified: true },
      slots: { actions: '<button class="extra">Sessions</button>' },
    });
    expect(wrapper.text()).toContain("node-1 · v2.10");
    expect(wrapper.text()).toContain("Modified");
    expect(wrapper.find(".extra").exists()).toBe(true);
    for (const [label, event] of [["New", "new"], ["Settings", "settings"], ["Connect", "connect"]] as const) {
      await wrapper.findAll("button").find((button) => button.text() === label)!.trigger("click");
      expect(wrapper.emitted(event)).toHaveLength(1);
    }
  });

  it("lists profiles with details and live markers, and selecting one emits its id", async () => {
    wrapper = mount(PluginConnectionBar, {
      attachTo: document.body,
      props: { labels, ready: true, connected: false, modelValue: "", profiles, profileDetail: (p: { id: string; name: string } & Record<string, unknown>) => String(p.endpoint), profileLive: (p: { id: string }) => p.id === "b" },
    });
    await wrapper.find(".el-select__wrapper").trigger("click");
    await flushPromises();
    const options = [...document.querySelectorAll(".plugin-connection-bar__popper .el-select-dropdown__item")];
    expect(options.map((option) => option.querySelector(".plugin-connection-bar__name")?.textContent)).toEqual(["Unsaved connection", "Alpha", "Beta"]);
    expect(options[2].querySelector(".plugin-connection-bar__detail")?.textContent).toBe("b.example:2379");
    expect(options[2].querySelector(".plugin-connection-bar__dot")?.classList.contains("is-live")).toBe(true);
    expect(options[1].querySelector(".plugin-connection-bar__dot")?.classList.contains("is-live")).toBe(false);
    (options[1] as HTMLElement).click();
    await flushPromises();
    expect(wrapper.emitted("update:modelValue")?.[0]).toEqual(["a"]);
    expect(wrapper.emitted("open")).toHaveLength(1);
  });

  it("closes the dropdown before asking the plugin to open its profile manager", async () => {
    wrapper = mount(PluginConnectionBar, { attachTo: document.body, props: { labels, ready: true, connected: false, modelValue: "", profiles } });
    await wrapper.find(".el-select__wrapper").trigger("click");
    await flushPromises();
    const manage = document.querySelector<HTMLButtonElement>(".plugin-connection-bar__manage");
    expect(manage?.textContent).toContain("Manage connections");
    manage!.click();
    await flushPromises();
    expect(wrapper.emitted("manage")).toHaveLength(1);
    expect(wrapper.find("input").attributes("aria-expanded")).toBe("false");
  });
});
