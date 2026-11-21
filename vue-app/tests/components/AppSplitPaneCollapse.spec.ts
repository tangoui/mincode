import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, onMounted, onUnmounted, ref } from "vue";
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

function sidebarWidth() {
  const panel = mounted().get(".el-splitter-panel").element as HTMLElement;
  return parseFloat(panel.style.flexBasis);
}

async function drag(delta: number) {
  const start = sidebarWidth();
  const down = new MouseEvent("mousedown", { bubbles: true, clientX: start, clientY: 300 });
  // happy-dom does not derive pageX from clientX; Element Plus reads pageX.
  Object.defineProperty(down, "pageX", { value: start });
  mounted().get(".el-splitter-bar__dragger").element.dispatchEvent(down);
  const move = new MouseEvent("mousemove", { clientX: start + delta, clientY: 300 });
  Object.defineProperty(move, "pageX", { value: start + delta });
  window.dispatchEvent(move);
  await flushPromises();
  window.dispatchEvent(new MouseEvent("mouseup"));
  await flushPromises();
}

// Mirrors ProjectWorkspaceView's sidebar: a collapsible first pane in pixels whose
// collapsed state is restored from storage, without opting into panel refresh.
function mountSidebar(startCollapsed: boolean) {
  const mounts = { mounted: 0, unmounted: 0 };
  const Sidebar = defineComponent({
    setup() {
      onMounted(() => { mounts.mounted++; });
      onUnmounted(() => { mounts.unmounted++; });
      return () => h("aside", "Sidebar");
    },
  });
  const collapsed = ref(startCollapsed);
  const width = ref(240);
  wrapper = mount(defineComponent({
    setup: () => () => h(AppSplitPane, {
      modelValue: width.value, collapsed: collapsed.value, collapsible: true,
      min: 200, max: (size: number) => Math.max(200, size * 0.56), secondMin: 360,
      "onUpdate:modelValue": (value: number) => { width.value = value; },
      "onUpdate:collapsed": (value: boolean) => { collapsed.value = value; },
    }, {
      first: () => h(Sidebar),
      second: () => h("main", "Editor"),
    }),
  }), {
    attachTo: document.body,
    global: { components: { ElSplitter, ElSplitterPanel } },
  });
  return { collapsed, width, mounts };
}

describe("collapsible sidebar split pane", () => {
  it("keeps the sidebar draggable after it is expanded from a restored collapsed state", async () => {
    const { collapsed, width } = mountSidebar(true);
    await flushPromises();
    expect(sidebarWidth()).toBe(0);
    collapsed.value = false;
    await flushPromises();
    expect(sidebarWidth()).toBeCloseTo(240, 0);
    await drag(60);
    expect(sidebarWidth()).toBeCloseTo(300, 0);
    expect(width.value).toBe(300);
    expect(collapsed.value).toBe(false);
    await drag(-500);
    expect(sidebarWidth()).toBeCloseTo(200, 0);
    expect(collapsed.value).toBe(false);
  });

  it("keeps its limits across repeated collapse and expand", async () => {
    const { collapsed, mounts } = mountSidebar(false);
    await flushPromises();
    for (let i = 0; i < 2; i++) {
      collapsed.value = true;
      await flushPromises();
      expect(sidebarWidth()).toBe(0);
      collapsed.value = false;
      await flushPromises();
    }
    await drag(-500);
    expect(sidebarWidth()).toBeCloseTo(200, 0);
    await drag(1000);
    expect(sidebarWidth()).toBeCloseTo(560, 0);
    expect(mounts).toEqual({ mounted: 1, unmounted: 0 });
  });
});

describe("reversing semantic panes", () => {
  it("moves the same mounted content and keeps sidebar width, drag direction and collapse", async () => {
    const mounts = { mounted: 0, unmounted: 0 };
    const Content = defineComponent({ setup() {
      onMounted(() => mounts.mounted++); onUnmounted(() => mounts.unmounted++);
      return () => h('textarea', { class: 'chat-draft', value: 'draft' });
    } });
    const reverse = ref(false), collapsed = ref(false), width = ref(240);
    wrapper = mount(defineComponent({ setup: () => () => h(AppSplitPane, {
      side: 'second', reverse: reverse.value, modelValue: width.value,
      min: 180, max: 400, secondMin: 300, collapsed: collapsed.value,
      onResizeEnd: (value: number) => { width.value = value; },
    }, { first: () => h(Content), second: () => h('aside', { class: 'conversation-list' }, 'List') }) }));
    await flushPromises();
    const draft = wrapper.get('.chat-draft').element;
    expect(wrapper.findAll('.el-splitter-panel').at(-1)?.find('.conversation-list').exists()).toBe(true);
    reverse.value = true; await flushPromises();
    expect(wrapper.findAll('.el-splitter-panel')[0].find('.conversation-list').exists()).toBe(true);
    expect(wrapper.get('.chat-draft').element).toBe(draft);
    expect(mounts).toEqual({ mounted: 1, unmounted: 0 });
    await drag(60);
    expect(width.value).toBe(300);
    collapsed.value = true; await flushPromises();
    expect(wrapper.get('.chat-draft').element).toBe(draft);
    expect(width.value).toBe(300);
    collapsed.value = false; await flushPromises();
    reverse.value = false; await flushPromises();
    expect(wrapper.findAll('.el-splitter-panel').at(-1)?.find('.conversation-list').exists()).toBe(true);
    expect(wrapper.get('.chat-draft').element).toBe(draft);
    await drag(-40);
    expect(width.value).toBe(340);
    await wrapper.get('.el-splitter-bar__dragger').trigger('keydown', { key: 'ArrowRight' });
    await flushPromises();
    expect(width.value).toBe(330);
    reverse.value = true; await flushPromises();
    await wrapper.get('.el-splitter-bar__dragger').trigger('keydown', { key: 'ArrowRight' });
    await flushPromises();
    expect(width.value).toBe(340);
    expect(wrapper.get('.chat-draft').element).toBe(draft);
  });
});

// A drag makes Element Plus store a size on both panes. AppSplitPane hands the
// unsized pane back to "the rest" once the drag ends, so a later controlled size
// is exact instead of scaled against the stale remainder.
describe("unsized pane after a drag", () => {
  type Case = {
    name: string;
    props: Record<string, unknown>;
    start: number;
    delta: number;
    dragged: number[];
    next: number;
    afterNext: number[];
  };

  function basis() {
    return mounted().findAll(".el-splitter-panel").map((panel) => {
      const value = (panel.element as HTMLElement).style.flexBasis;
      return Math.round(parseFloat(value) * 100) / 100;
    });
  }

  async function dragHolding(layout: "horizontal" | "vertical", delta: number) {
    const dragger = mounted().get(".el-splitter-bar__dragger").element;
    const point = (type: string, offset: number) => {
      const event = new MouseEvent(type, { bubbles: true });
      // happy-dom does not derive page coordinates; Element Plus reads pageX/pageY.
      Object.defineProperty(event, "pageX", { value: layout === "horizontal" ? 400 + offset : 0 });
      Object.defineProperty(event, "pageY", { value: layout === "vertical" ? 400 + offset : 0 });
      return event;
    };
    dragger.dispatchEvent(point("mousedown", 0));
    window.dispatchEvent(point("mousemove", delta / 2));
    await flushPromises();
    window.dispatchEvent(point("mousemove", delta));
    await flushPromises();
    const during = basis();
    window.dispatchEvent(new MouseEvent("mouseup"));
    await flushPromises();
    return during;
  }

  const cases: Case[] = [
    {
      name: "side first in pixels (sidebar)",
      props: { min: 200, max: 600, secondMin: 300 },
      start: 240, delta: 60, dragged: [300, 700], next: 400, afterNext: [400, 600],
    },
    {
      name: "side second, vertical (bottom panel)",
      props: { side: "second", layout: "vertical", min: 120, max: 640, secondMin: 160 },
      start: 240, delta: -60, dragged: [500, 300], next: 400, afterNext: [400, 400],
    },
    {
      name: "side second, reversed",
      props: { side: "second", reverse: true, min: 180, max: 400, secondMin: 300 },
      start: 240, delta: 60, dragged: [300, 700], next: 200, afterNext: [200, 800],
    },
    {
      name: "side second in percent",
      props: { side: "second", unit: "percent", min: 20, max: 80 },
      start: 40, delta: -100, dragged: [500, 500], next: 30, afterNext: [700, 300],
    },
  ];

  for (const testCase of cases) {
    it(`keeps drag sizes and applies the next controlled size exactly: ${testCase.name}`, async () => {
      const value = ref(testCase.start);
      const layout = (testCase.props.layout as "horizontal" | "vertical" | undefined) ?? "horizontal";
      wrapper = mount(defineComponent({
        setup: () => () => h(AppSplitPane, {
          ...testCase.props,
          modelValue: value.value,
          "onUpdate:modelValue": (next: number) => { value.value = next; },
        }, { first: () => h("div", "first"), second: () => h("div", "second") }),
      }), { attachTo: document.body, global: { components: { ElSplitter, ElSplitterPanel } } });
      await flushPromises();

      const during = await dragHolding(layout, testCase.delta);
      expect(during).toEqual(testCase.dragged);
      expect(basis()).toEqual(testCase.dragged);

      value.value = testCase.next;
      await flushPromises();
      expect(basis()).toEqual(testCase.afterNext);
    });
  }

  it("collapses and restores a dragged collapsible sidebar that refreshes on collapse", async () => {
    const collapsed = ref(false);
    const width = ref(240);
    wrapper = mount(defineComponent({
      setup: () => () => h(AppSplitPane, {
        modelValue: width.value, collapsed: collapsed.value, collapsible: true, refreshPanelOnCollapse: true,
        min: 200, max: 600, secondMin: 300,
        "onUpdate:modelValue": (next: number) => { width.value = next; },
        "onUpdate:collapsed": (next: boolean) => { collapsed.value = next; },
      }, { first: () => h("aside", "Sidebar"), second: () => h("main", "Editor") }),
    }), { attachTo: document.body, global: { components: { ElSplitter, ElSplitterPanel } } });
    await flushPromises();

    expect(await dragHolding("horizontal", 80)).toEqual([320, 680]);
    expect(width.value).toBe(320);
    collapsed.value = true;
    await flushPromises();
    expect(basis()).toEqual([0, 1000]);
    collapsed.value = false;
    await flushPromises();
    expect(basis()).toEqual([320, 680]);
    expect(await dragHolding("horizontal", -40)).toEqual([280, 720]);
    expect(width.value).toBe(280);
  });
});
