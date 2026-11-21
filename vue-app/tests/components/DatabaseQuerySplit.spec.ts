import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, ref } from "vue";
import { ElSplitter, ElSplitterPanel } from "element-plus";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";

// Only browser layout measurements are replaced. The real Element Plus splitter
// handles mouse events, panel registration and size limits.
class LayoutObserver {
  constructor(private callback: ResizeObserverCallback) {}
  observe(target: Element) {
    this.callback([{
      target,
      contentRect: { width: 1000, height: 800 },
      borderBoxSize: [{ inlineSize: 1000, blockSize: 800 }],
    } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver);
  }
  unobserve() {}
  disconnect() {}
}

let wrapper: ReturnType<typeof mount> | undefined;

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", LayoutObserver);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    width: 1000, height: 800, top: 0, left: 0, right: 1000, bottom: 800,
    x: 0, y: 0, toJSON() {},
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(800);
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(1000);
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function mounted() {
  if (!wrapper) throw new Error("split pane is not mounted");
  return wrapper;
}

function editorHeight() {
  const panel = mounted().get(".el-splitter-panel").element as HTMLElement;
  return parseFloat(panel.style.flexBasis);
}

async function drag(delta: number) {
  const start = editorHeight();
  const down = new MouseEvent("mousedown", { bubbles: true, clientX: 300, clientY: start });
  // happy-dom does not derive pageY from clientY; Element Plus reads pageY.
  Object.defineProperty(down, "pageY", { value: start });
  mounted().get(".el-splitter-bar__dragger").element.dispatchEvent(down);
  const move = new MouseEvent("mousemove", { clientX: 300, clientY: start + delta });
  Object.defineProperty(move, "pageY", { value: start + delta });
  window.dispatchEvent(move);
  await flushPromises();
  window.dispatchEvent(new MouseEvent("mouseup"));
  await flushPromises();
}

describe("database query/result divider", () => {
  it("preserves the editor size and drag limits across repeated table/query switches", async () => {
    const query = ref(false);
    const percent = ref(34);
    wrapper = mount(defineComponent({
      setup: () => () => h(AppSplitPane, {
        layout: "vertical", unit: "percent", modelValue: percent.value,
        collapsed: !query.value, collapsedSize: 0, refreshPanelOnCollapse: true,
        min: 20, max: 75, secondMin: 160, resizable: query.value,
        "onUpdate:modelValue": (value: number) => { percent.value = value; },
      }, {
        first: () => query.value ? h("div", "SELECT 1") : null,
        second: () => h("div", "Results"),
      }),
    }), {
      attachTo: document.body,
      global: { components: { ElSplitter, ElSplitterPanel } },
    });
    await flushPromises();
    expect(editorHeight()).toBe(0);
    query.value = true;
    await flushPromises();
    expect(editorHeight()).toBeCloseTo(272, 0);
    await drag(80);
    expect(editorHeight()).toBeCloseTo(352, 0);
    expect(percent.value).toBeCloseTo(44, 0);

    query.value = false;
    await flushPromises();
    expect(editorHeight()).toBe(0);
    expect(percent.value).toBeCloseTo(44, 0);
    query.value = true;
    await flushPromises();
    expect(editorHeight()).toBeCloseTo(352, 0);
    await drag(-40);
    expect(editorHeight()).toBeCloseTo(312, 0);
    expect(percent.value).toBeCloseTo(39, 0);
    await drag(-1000);
    expect(editorHeight()).toBeCloseTo(160, 0);
    expect(percent.value).toBe(20);
    await drag(1000);
    expect(editorHeight()).toBeCloseTo(600, 0);
    expect(percent.value).toBe(75);
  });
});
