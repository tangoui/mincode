import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick, ref } from "vue";
import { ROW_GROUP_ATTRIBUTE, useGroupedRowWindow } from "@/composables/use-grouped-row-window";

let wrapper: VueWrapper | undefined;

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.unstubAllGlobals();
});

function mountGroupedRows(counts: Record<string, number>, fallbackRows?: number) {
  const groups = Object.entries(counts).map(([key, count]) => ({
    key,
    rows: Array.from({ length: count }, (_, index) => index),
  }));
  wrapper = mount(defineComponent({
    setup() {
      const scroller = ref<HTMLElement>();
      const { windowOf } = useGroupedRowWindow({
        scroller: () => scroller.value,
        counts: () => counts,
        rowSelector: ".row",
        estimatedRowHeight: 40,
        fallbackRows,
      });
      return () => h("div", { ref: scroller }, groups.map(({ key, rows }) => {
        const window = windowOf(key, rows);
        return h("section", { [ROW_GROUP_ATTRIBUTE]: key }, [
          h("div", { class: "top-spacer", style: { height: `${window.paddingTop}px` } }),
          ...window.rows.map((row) => h("div", { class: "row", key: row }, String(row))),
          h("div", { class: "bottom-spacer", style: { height: `${window.paddingBottom}px` } }),
        ]);
      }));
    },
  }));

  const view = wrapper;
  const group = (key: string) => view.get(`[${ROW_GROUP_ATTRIBUTE}="${key}"]`);
  return {
    scroller: view.element,
    group,
    rowsIn: (key: string) => group(key).findAll(".row").map((row) => Number(row.text())),
    counts: () => groups.map(({ key }) => group(key).findAll(".row").length),
    bottomSpace: (key: string) => group(key).get<HTMLElement>(".bottom-spacer").element.style.height,
  };
}

describe("useGroupedRowWindow", () => {
  it("shares a custom initial budget across open groups in render order", () => {
    const list = mountGroupedRows({ first: 8, collapsed: 0, second: 100, third: 110 }, 12);

    expect(list.counts()).toEqual([8, 0, 4, 0]);
    expect(list.rowsIn("second")).toEqual([0, 1, 2, 3]);
    expect(list.bottomSpace("second")).toBe(`${96 * 40}px`);
    expect(list.bottomSpace("third")).toBe(`${110 * 40}px`);
  });

  it("renders every row of a short list even when it exceeds the initial budget", () => {
    const list = mountGroupedRows({ first: 8, second: 10 }, 12);

    expect(list.counts()).toEqual([8, 10]);
    expect(list.bottomSpace("second")).toBe("0px");
  });

  it("defaults to 60 initial rows across the entire list", () => {
    const list = mountGroupedRows({ first: 8, second: 100, third: 110 });

    expect(list.counts()).toEqual([8, 52, 0]);
  });

  it("uses measured rows and viewport offsets on layout, resize, and scroll", async () => {
    let resize: (() => void) | undefined;
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: () => void) { resize = callback; }
      observe() {}
      disconnect() {}
    });

    const list = mountGroupedRows({ first: 100, second: 120 }, 12);
    const { scroller } = list;
    let viewportHeight = 400;
    Object.defineProperty(scroller, "clientHeight", { get: () => viewportHeight });
    // happy-dom does not lay out rows. Simulate 20px rows with a header before each group.
    vi.spyOn(list.group("first").element, "getBoundingClientRect").mockImplementation(() => new DOMRect(0, 40 - scroller.scrollTop));
    vi.spyOn(list.group("second").element, "getBoundingClientRect").mockImplementation(() => new DOMRect(0, 2080 - scroller.scrollTop));
    // The composable measures the first rendered row, which persists through both resizes.
    Object.defineProperty(list.group("first").get(".row").element, "offsetHeight", { value: 20 });
    await flushPromises();
    expect(list.counts()).toEqual([26, 0]);

    viewportHeight = 640;
    expect(resize).toBeTypeOf("function");
    resize?.();
    await nextTick();
    expect(list.counts()).toEqual([38, 0]);

    scroller.scrollTop = 2400;
    scroller.dispatchEvent(new Event("scroll"));
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    await nextTick();
    expect(list.counts()).toEqual([0, 48]);
    expect(list.rowsIn("second")[0]).toBe(8);
  });
});
