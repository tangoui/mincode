import { afterEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { h } from "vue";
import { ElTabPane } from "element-plus";
import AppWorkspaceTabs from "@/components/ui/AppWorkspaceTabs.vue";

function mountTabs() {
  return mount(AppWorkspaceTabs, {
    props: { modelValue: "a" },
    slots: {
      default: () => [
        h(ElTabPane, { name: "a", label: "A" }, () => h("div", { class: "pane-body" }, "body")),
        h(ElTabPane, { name: "b", label: "B" }),
      ],
    },
    attachTo: document.body,
  });
}

function recordNavWheel(nav: Element) {
  const received: WheelEvent[] = [];
  nav.addEventListener("wheel", (event) => received.push(event as WheelEvent));
  return received;
}

function wheel(target: Element, init: WheelEventInit) {
  const event = new WheelEvent("wheel", { bubbles: true, cancelable: true, ...init });
  // happy-dom ignores modifier keys in WheelEventInit.
  Object.defineProperty(event, "shiftKey", { value: init.shiftKey === true });
  target.dispatchEvent(event);
  return event;
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("AppWorkspaceTabs", () => {
  it("re-dispatches a vertical mouse wheel over the tabs as a horizontal one", () => {
    const wrapper = mountTabs();
    const nav = wrapper.get(".el-tabs__nav").element;
    const received = recordNavWheel(nav);

    const original = wheel(wrapper.get(".el-tabs__item").element, { deltaY: 120 });

    expect(original.defaultPrevented).toBe(true);
    expect(received).toHaveLength(1);
    expect(received[0]!.deltaX).toBe(120);
    expect(received[0]!.deltaY).toBe(0);
  });

  it("leaves horizontal, Shift and pane-content wheels alone", () => {
    const wrapper = mountTabs();
    const received = recordNavWheel(wrapper.get(".el-tabs__nav").element);
    const tab = wrapper.get(".el-tabs__item").element;

    wheel(tab, { deltaX: 80, deltaY: 10 });
    wheel(tab, { deltaY: 120, shiftKey: true });
    const inPane = wheel(wrapper.get(".pane-body").element, { deltaY: 120 });

    expect(received.map((event) => [event.deltaX, event.deltaY])).toEqual([
      [80, 10],
      [0, 120],
    ]);
    expect(inPane.defaultPrevented).toBe(false);
  });
});
