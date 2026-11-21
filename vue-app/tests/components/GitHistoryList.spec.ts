// The history list renders 100 commits at once: rows carry no per-row dropdown or tooltip,
// one shared menu serves the row whose "⋯" was clicked and one tooltip the hovered subject.
import { afterEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import GitHistoryList from "@/components/git/GitHistoryList.vue";

const ContextMenu = defineComponent({
  name: "AppContextMenu",
  props: { open: Boolean, x: Number, y: Number },
  emits: ["close"],
  setup(_, { slots }) {
    return () => h("ul", { class: "context-menu-stub" }, slots.default?.());
  },
});
const DropdownItem = defineComponent({
  inheritAttrs: false,
  setup(_, { attrs, slots }) {
    return () => h("li", { role: "menuitem", ...attrs }, slots.default?.());
  },
});

const entries = Array.from({ length: 100 }, (_, i) => ({
  hash: `hash${i}`.padEnd(40, "0"),
  short_hash: `h${i}`,
  author: "Author",
  message: `commit ${i}`,
  time: 1_700_000_000 - i * 60,
  refs: i === 0 ? ["refs/heads/main"] : [],
}));

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.useRealTimers();
});

function mountList() {
  return mount(GitHistoryList, {
    props: { entries, emptyText: "none", actions: ["copy-hash", "cherry-pick", "revert"] },
    slots: { "menu-extra": ({ entry, open }: { entry: { short_hash: string }; open: boolean }) => h("li", { class: "extra" }, `${entry.short_hash}:${open}`) },
    global: { stubs: { AppContextMenu: ContextMenu, ElDropdownItem: DropdownItem, "el-dropdown-item": DropdownItem } },
    attachTo: document.body,
  });
}

describe("GitHistoryList", () => {
  it("renders rows without a dropdown or tooltip per commit", () => {
    wrapper = mountList();
    expect(wrapper.findAll(".app-git-history__row")).toHaveLength(100);
    expect(wrapper.findAllComponents({ name: "ElDropdown" })).toHaveLength(0);
    expect(wrapper.findAllComponents({ name: "ElTooltip" })).toHaveLength(0);
    expect(wrapper.findComponent(ContextMenu).exists()).toBe(false);
  });

  it("opens one menu for the clicked row and runs its action", async () => {
    wrapper = mountList();
    await wrapper.findAll(".app-git-history__more")[3]!.trigger("click");
    await flushPromises();
    expect(wrapper.emitted("menu-open-change")?.at(-1)).toEqual([entries[3]]);
    const menu = wrapper.findComponent(ContextMenu);
    expect(menu.find(".extra").text()).toBe("h3:true");
    const cherry = menu.findAll("[role=menuitem]").find((item) => item.text().includes("Cherry") || item.text().includes("拣选"))!;
    await cherry.trigger("click");
    await flushPromises();
    expect(wrapper.emitted("action")?.at(-1)).toEqual([{ action: "cherry-pick", entry: entries[3] }]);
    expect(wrapper.findComponent(ContextMenu).exists()).toBe(false);
    expect(wrapper.emitted("menu-open-change")?.at(-1)).toEqual([null]);
  });

  it("shows one tooltip for the subject hovered long enough", async () => {
    vi.useFakeTimers();
    wrapper = mountList();
    const subject = wrapper.findAll(".app-git-history__primary strong")[5]!;
    await subject.trigger("mouseenter");
    expect(wrapper.findAllComponents({ name: "ElTooltip" })).toHaveLength(0);
    vi.advanceTimersByTime(500);
    await flushPromises();
    const tooltips = wrapper.findAllComponents({ name: "ElTooltip" });
    expect(tooltips).toHaveLength(1);
    expect(tooltips[0]!.props("content")).toBe("commit 5");
    await subject.trigger("mouseleave");
    await flushPromises();
    expect(wrapper.findAllComponents({ name: "ElTooltip" })).toHaveLength(0);
  });
});
