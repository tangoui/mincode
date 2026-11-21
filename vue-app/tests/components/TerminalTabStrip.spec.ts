import { describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, nextTick } from "vue";
import TerminalTabStrip from "@/components/terminal/TerminalTabStrip.vue";
import type { TerminalTab } from "@/domain/terminal/terminal-tabs";
import type { TerminalTabUsage } from "@/domain/terminal/resource-usage";

function tab(id: string, title?: string): TerminalTab {
  const pane = { id: `${id}-pane`, type: "local" as const, shellType: "system" as const };
  return title ? { id, title, root: pane, activePaneId: pane.id } : { id, root: pane, activePaneId: pane.id };
}

const tabs = [tab("t1", "zsh"), tab("t2", "server")];

// AppContextMenu and the Element Plus items need a live popper; these stubs render the menu inline.
const ContextMenu = defineComponent({
  name: "AppContextMenuStub",
  emits: ["close"],
  setup(_, { slots }) {
    return () => h("ul", { class: "context-menu-stub" }, slots.default?.());
  },
});
const DropdownItem = defineComponent({
  inheritAttrs: false,
  props: { disabled: { type: Boolean, default: false }, divided: { type: Boolean, default: false } },
  setup(props, { attrs, slots }) {
    return () => h("li", { role: "menuitem", "aria-disabled": String(props.disabled), ...attrs }, slots.default?.());
  },
});
const menuStubs = { AppContextMenu: ContextMenu, ElDropdownItem: DropdownItem, "el-dropdown-item": DropdownItem };

function mountStrip(props: Partial<InstanceType<typeof TerminalTabStrip>["$props"]> = {}, stubs = {}) {
  return mount(TerminalTabStrip, {
    props: {
      tabs,
      activeTabId: "t1",
      canAdd: true,
      profiles: [],
      labelFor: (item: TerminalTab) => item.title ?? item.id,
      attentionFor: () => false,
      ...props,
    },
    attachTo: document.body,
    global: { stubs },
  });
}

async function openMenu(wrapper: ReturnType<typeof mountStrip>, index: number) {
  await wrapper.findAll(".el-tabs__item")[index]!.trigger("contextmenu", { clientX: 40, clientY: 20 });
  await flushPromises();
  return wrapper.findAll(".context-menu-stub li");
}

const threeTabs = [tab("t1", "zsh"), tab("t2", "server"), tab("t3", "logs")];

describe("TerminalTabStrip", () => {
  it("renders the label of every terminal tab", () => {
    const wrapper = mountStrip();
    const labels = wrapper.findAll(".terminal-tab-strip__label").map((label) => label.text());
    expect(labels).toEqual(["zsh", "server"]);
  });

  it("emits select when a tab is clicked", async () => {
    const wrapper = mountStrip();
    await wrapper.findAll(".el-tabs__item")[1]!.trigger("click");
    expect(wrapper.emitted("select")).toEqual([["t2"]]);
  });

  it("emits close from the tab close button", async () => {
    const wrapper = mountStrip();
    await wrapper.findAll(".el-tabs__item")[1]!.find(".is-icon-close").trigger("click");
    expect(wrapper.emitted("close")).toEqual([["t2"]]);
  });

  it("emits add from the + button and disables it at the limit", async () => {
    const wrapper = mountStrip();
    const add = wrapper.get(".terminal-tab-strip__add");
    expect(add.attributes("aria-label")).toBe("新建终端");
    await add.trigger("click");
    expect(wrapper.emitted("add")).toHaveLength(1);

    const full = mountStrip({ canAdd: false });
    const disabled = full.get(".terminal-tab-strip__add");
    expect(disabled.attributes("disabled")).toBeDefined();
    await disabled.trigger("click");
    expect(full.emitted("add")).toBeUndefined();
  });

  it("renames inline after a double click: Enter submits, Escape cancels", async () => {
    const wrapper = mountStrip();
    await wrapper.findAll(".terminal-tab-strip__label")[0]!.trigger("dblclick");
    await nextTick();
    const input = wrapper.get(".terminal-tab-strip__rename input");
    await input.setValue("build");
    await input.trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("rename")).toEqual([["t1", "build"]]);
    expect(wrapper.find(".terminal-tab-strip__rename").exists()).toBe(false);

    await wrapper.findAll(".terminal-tab-strip__label")[1]!.trigger("dblclick");
    await nextTick();
    const second = wrapper.get(".terminal-tab-strip__rename input");
    await second.setValue("ignored");
    await second.trigger("keydown", { key: "Escape" });
    await second.trigger("blur");
    expect(wrapper.emitted("rename")).toHaveLength(1);
    expect(wrapper.find(".terminal-tab-strip__rename").exists()).toBe(false);
  });

  it("shows a disabled close with the reason instead of × while closing is locked", async () => {
    const wrapper = mountStrip({ tabs: [tab("t1", "zsh")], closeDisabledReason: "已固定的标签不能关闭" });
    const item = wrapper.get(".el-tabs__item");
    expect(item.find(".is-icon-close").exists()).toBe(false);
    const locked = item.get(".terminal-tab-strip__close.is-disabled");
    expect(locked.attributes("aria-label")).toBe("已固定的标签不能关闭");
    await locked.trigger("click");
    expect(wrapper.emitted("close")).toBeUndefined();
  });

  it("shows an attention dot on tabs that need the user", () => {
    const wrapper = mountStrip({ attentionFor: (item: TerminalTab) => item.id === "t2" });
    const dots = wrapper.findAll(".terminal-tab-strip__attention");
    expect(dots).toHaveLength(1);
    expect(dots[0]!.attributes("aria-label")).toContain("需要处理");
    expect(wrapper.findAll(".el-tabs__item")[1]!.find(".terminal-tab-strip__attention").exists()).toBe(true);
  });

  it("opens a context menu on right click of a tab", async () => {
    const wrapper = mountStrip({ tabs: threeTabs }, menuStubs);
    expect(wrapper.find(".context-menu-stub").exists()).toBe(false);
    const items = await openMenu(wrapper, 1);
    expect(items.map((item) => item.text())).toEqual(["重命名", "关闭", "关闭其他标签", "关闭左侧标签", "关闭右侧标签"]);
  });

  it("closes the tab, the other tabs, or the tabs on one side from the menu", async () => {
    const wrapper = mountStrip({ tabs: threeTabs }, menuStubs);
    await (await openMenu(wrapper, 1))[1]!.trigger("click");
    await (await openMenu(wrapper, 1))[2]!.trigger("click");
    await (await openMenu(wrapper, 1))[3]!.trigger("click");
    await (await openMenu(wrapper, 1))[4]!.trigger("click");
    expect(wrapper.emitted("close")).toEqual([["t2"]]);
    expect(wrapper.emitted("close-tabs")).toEqual([[["t1", "t3"]], [["t1"]], [["t3"]]]);
    expect(wrapper.find(".context-menu-stub").exists()).toBe(false);
  });

  it("disables menu entries that have nothing to close", async () => {
    const wrapper = mountStrip({ tabs: [tab("t1", "zsh")], closeDisabledReason: "已固定的标签不能关闭" }, menuStubs);
    const items = await openMenu(wrapper, 0);
    expect(items.map((item) => item.attributes("aria-disabled"))).toEqual(["false", "true", "true", "true", "true"]);
  });

  it("starts an inline rename from the menu", async () => {
    const wrapper = mountStrip({ tabs: threeTabs }, menuStubs);
    await (await openMenu(wrapper, 2))[0]!.trigger("click");
    await flushPromises();
    const input = wrapper.get(".terminal-tab-strip__rename input");
    await input.setValue("tail");
    await input.trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("rename")).toEqual([["t3", "tail"]]);
  });

  describe("memory", () => {
    const GiB = 1024 ** 3;
    const usage = (bytes: number): TerminalTabUsage => ({
      bytes,
      processCount: 3,
      processes: [
        { sessionId: "t2-pane", pid: 42, name: "gpu.test", bytes: bytes - 0.2 * GiB, isShell: false },
        { sessionId: "t2-pane", pid: 7, name: "zsh", bytes: 0.2 * GiB, isShell: true },
      ],
    });

    it("shows a tab's memory from 1 GB and warns from 4 GB", () => {
      const sizes: Record<string, number> = { t1: 0.5 * GiB, t2: 1.5 * GiB };
      const wrapper = mountStrip({ usageFor: (item: TerminalTab) => usage(sizes[item.id]!) });
      const badges = wrapper.findAll(".terminal-tab-strip__memory");
      expect(badges).toHaveLength(1);
      expect(badges[0]!.text()).toBe("1.5 GB");
      expect(badges[0]!.classes()).not.toContain("is-high");
      expect(wrapper.findAll(".el-tabs__item")[1]!.find(".terminal-tab-strip__memory").exists()).toBe(true);

      const high = mountStrip({ usageFor: () => usage(5 * GiB) });
      expect(high.findAll(".terminal-tab-strip__memory.is-high")).toHaveLength(2);
    });

    it("lists the largest processes and ends one of them, but not the shell", async () => {
      const wrapper = mountStrip({ usageFor: (item: TerminalTab) => (item.id === "t2" ? usage(2 * GiB) : undefined) });
      await wrapper.get(".terminal-tab-strip__memory").trigger("click");
      await flushPromises();
      const rows = [...document.body.querySelectorAll(".terminal-tab-memory__process")];
      expect(rows.map((row) => row.querySelector(".terminal-tab-memory__name")?.textContent)).toEqual(["gpu.test", "zsh"]);
      expect(rows[1]!.querySelector(".terminal-tab-memory__end")).toBeNull();
      (rows[0]!.querySelector(".terminal-tab-memory__end") as HTMLElement).click();
      expect(wrapper.emitted("end-process")).toEqual([["t2-pane", 42, "gpu.test"]]);
      expect(wrapper.emitted("select")).toBeUndefined();
      wrapper.unmount();
    });
  });
});
