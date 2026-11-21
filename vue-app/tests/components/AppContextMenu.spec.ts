import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h, KeepAlive } from "vue";
import { ElDropdownItem } from "element-plus";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

describe("AppContextMenu Escape", () => {
  // A kept-alive view whose menu was left open, and another view.
  const MenuView = defineComponent({
    name: "MenuView",
    emits: ["close"],
    setup(_, { emit }) {
      return () => h("div", [h(AppContextMenu, { open: true, x: 10, y: 10, onClose: () => emit("close") }, () => [h(ElDropdownItem, () => "Item")])]);
    },
  });
  const OtherView = defineComponent({ name: "OtherView", setup: () => () => h("div", "other") });

  function escape() {
    const event = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    document.body.dispatchEvent(event);
    return event;
  }

  it("leaves Escape alone while its view is kept alive but hidden", async () => {
    let closed = 0;
    const App = defineComponent({
      props: { view: { type: String, default: "menu" } },
      setup(props) {
        return () =>
          h(KeepAlive, null, [props.view === "menu" ? h(MenuView, { key: "menu", onClose: () => (closed += 1) }) : h(OtherView, { key: "other" })]);
      },
    });
    wrapper = mount(App, { attachTo: document.body });
    await flushPromises();
    await new Promise((resolve) => setTimeout(resolve, 20));
    // Visible: Escape closes it.
    expect(escape().defaultPrevented).toBe(true);
    expect(closed).toBe(1);

    // Its view is deactivated with the menu still open: Escape belongs to the other view.
    await wrapper.setProps({ view: "other" });
    await flushPromises();
    const event = escape();
    expect(event.defaultPrevented).toBe(false);
    expect(closed).toBe(1);
  });

  it("ignores an Escape another handler already took", async () => {
    let closed = 0;
    wrapper = mount(MenuView, { attachTo: document.body, props: { onClose: () => (closed += 1) } });
    await flushPromises();
    await new Promise((resolve) => setTimeout(resolve, 20));
    const event = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    event.preventDefault();
    document.body.dispatchEvent(event);
    expect(closed).toBe(0);
  });
});

// A caller that mounts the menu while `open` is true (the v-if pattern).
const Host = defineComponent({
  props: { open: Boolean },
  emits: ["close"],
  setup(props, { emit }) {
    return () =>
      h("div", [
        h("button", { class: "trigger" }, "row"),
        h("button", { class: "other" }, "other"),
        props.open
          ? h(AppContextMenu, { open: true, x: 10, y: 10, ariaLabel: "Row actions", onClose: () => emit("close") }, () => [h(ElDropdownItem, { class: "item" }, () => "Item")])
          : null,
      ]);
  },
});

// A caller that keeps the menu mounted and toggles `open`.
const ToggleHost = defineComponent({
  props: { open: Boolean },
  setup(props) {
    return () =>
      h("div", [
        h("button", { class: "trigger" }, "row"),
        h(AppContextMenu, { open: props.open, x: 10, y: 10 }, () => [h(ElDropdownItem, { class: "item" }, () => "Item")]),
      ]);
  },
});

async function settle() {
  await flushPromises();
  await new Promise((resolve) => setTimeout(resolve, 20));
}

function focusMenu() {
  const menu = document.querySelector<HTMLElement>(".app-context-menu__popper .el-dropdown-menu")!;
  menu.focus();
  expect(document.activeElement).toBe(menu);
  return menu;
}

describe("AppContextMenu focus return", () => {
  it("returns focus to the element that opened it when it closes", async () => {
    wrapper = mount(Host, { attachTo: document.body, props: { open: false } });
    const trigger = wrapper.find(".trigger").element as HTMLElement;
    trigger.focus();
    await wrapper.setProps({ open: true });
    await settle();
    focusMenu();
    await wrapper.setProps({ open: false });
    await settle();
    expect(document.activeElement).toBe(trigger);
  });

  it("closes on Escape inside the menu and returns focus", async () => {
    wrapper = mount(Host, { attachTo: document.body, props: { open: false } });
    const trigger = wrapper.find(".trigger").element as HTMLElement;
    trigger.focus();
    await wrapper.setProps({ open: true });
    await settle();
    const menu = focusMenu();
    expect(menu.getAttribute("aria-label")).toBe("Row actions");
    expect(menu.hasAttribute("aria-labelledby")).toBe(false);
    const item = menu.querySelector<HTMLElement>(".item")!;
    item.focus();
    item.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await settle();
    expect(wrapper.emitted("close")).toHaveLength(1);
    await wrapper.setProps({ open: false });
    await settle();
    expect(document.activeElement).toBe(trigger);
  });

  it("does not take focus back from an element an action moved it to", async () => {
    wrapper = mount(Host, { attachTo: document.body, props: { open: false } });
    const trigger = wrapper.find(".trigger").element as HTMLElement;
    const other = wrapper.find(".other").element as HTMLElement;
    trigger.focus();
    await wrapper.setProps({ open: true });
    await settle();
    other.focus();
    await wrapper.setProps({ open: false });
    await settle();
    expect(document.activeElement).toBe(other);
  });

  it("also returns focus for callers that toggle open", async () => {
    wrapper = mount(ToggleHost, { attachTo: document.body, props: { open: false } });
    const trigger = wrapper.find(".trigger").element as HTMLElement;
    trigger.focus();
    await wrapper.setProps({ open: true });
    await settle();
    focusMenu();
    await wrapper.setProps({ open: false });
    await settle();
    expect(document.activeElement).toBe(trigger);
  });
});
