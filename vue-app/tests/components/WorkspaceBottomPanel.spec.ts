import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, ref } from "vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import AppWorkspaceTabs from "@/components/ui/AppWorkspaceTabs.vue";
import WorkspaceBottomPanel from "@/components/layout/WorkspaceBottomPanel.vue";

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
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.unstubAllGlobals();
});

const tabs = [
  { id: "s1", title: "App", closable: true },
  { id: "s2", title: "Tests", closable: true },
];

function mountPanel(props: Partial<InstanceType<typeof WorkspaceBottomPanel>["$props"]> = {}) {
  wrapper = mount(WorkspaceBottomPanel, {
    props: { tabs, activeId: "s1", height: 240, collapsed: false, ...props },
    slots: {
      default: '<div class="main-content">editor</div>',
      tab: '<template #tab="{ tab }"><div class="tab-body">{{ tab.title }}</div></template>',
    },
  });
  return wrapper;
}

describe("WorkspaceBottomPanel", () => {
  it("hides without tabs", () => {
    const panel = mountPanel({ tabs: [], activeId: null });
    expect(panel.find(".main-content").exists()).toBe(true);
    expect(panel.find(".app-bottom-panel").exists()).toBe(false);
  });

  it("renders every tab body and shows the active one", () => {
    const panel = mountPanel();
    const bodies = panel.findAll(".tab-body");
    expect(bodies.map((body) => body.text())).toEqual(["App", "Tests"]);
    expect((bodies[0]!.element.parentElement as HTMLElement).style.display).not.toBe("none");
    expect((bodies[1]!.element.parentElement as HTMLElement).style.display).toBe("none");
  });

  it("emits height on resize end", async () => {
    const panel = mountPanel();
    panel.findComponent(AppSplitPane).vm.$emit("resizeEnd", 320);
    expect(panel.emitted("update:height")).toEqual([[320]]);
  });

  it("collapses to its tab strip and closes tabs", async () => {
    const panel = mountPanel();
    await panel.find(".app-bottom-panel__collapse").trigger("click");
    expect(panel.emitted("update:collapsed")).toEqual([[true]]);
    await panel.setProps({ collapsed: true });
    expect(panel.find(".app-bottom-panel").exists()).toBe(true);
    expect((panel.find(".app-bottom-panel__body").element as HTMLElement).style.display).toBe("none");
    expect(panel.findComponent(AppSplitPane).props("resizable")).toBe(false);
    panel.findComponent(AppWorkspaceTabs).vm.$emit("tab-remove", "s1");
    expect(panel.emitted("close")).toEqual([["s1"]]);
    panel.findComponent(AppWorkspaceTabs).vm.$emit("update:modelValue", "s2");
    expect(panel.emitted("update:activeId")).toEqual([["s2"]]);
  });
});

describe("WorkspaceBottomPanel sizing in the real splitter", () => {
  // Only layout measurements are faked (an 800px tall host); the real Element
  // Plus splitter handles the drag, the panel sizes and their flex-basis.
  beforeEach(() => {
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      width: 1000, height: 800, top: 0, left: 0, right: 1000, bottom: 800,
      x: 0, y: 0, toJSON() {},
    });
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(800);
    vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(1000);
  });

  function panelSizes() {
    return wrapper!.findAll(".el-splitter-panel").map((panel) => parseFloat((panel.element as HTMLElement).style.flexBasis));
  }

  async function dragBy(delta: number) {
    const start = panelSizes()[0]!;
    const event = (type: string, pageY: number) => {
      const value = new MouseEvent(type, { bubbles: true, clientX: 500, clientY: pageY });
      // happy-dom does not derive pageY from clientY; Element Plus reads pageY.
      Object.defineProperty(value, "pageY", { value: pageY });
      return value;
    };
    wrapper!.get(".el-splitter-bar__dragger").element.dispatchEvent(event("mousedown", start));
    window.dispatchEvent(event("mousemove", start + delta));
    await flushPromises();
    window.dispatchEvent(new MouseEvent("mouseup"));
    await flushPromises();
  }

  function mountControlled() {
    const height = ref(240);
    const collapsed = ref(false);
    wrapper = mount(defineComponent({
      setup: () => () => h(WorkspaceBottomPanel, {
        tabs, activeId: "s1", height: height.value, collapsed: collapsed.value,
        "onUpdate:height": (value: number) => { height.value = value; },
        "onUpdate:collapsed": (value: boolean) => { collapsed.value = value; },
      }, { default: () => h("div", "editor") }),
    }), { attachTo: document.body });
    return { height, collapsed };
  }

  it("collapses to exactly the tab strip after the panel was resized by dragging", async () => {
    const { height } = mountControlled();
    await flushPromises();
    expect(panelSizes()).toEqual([560, 240]);
    await dragBy(-60);
    expect(height.value).toBe(300);
    expect(panelSizes()).toEqual([500, 300]);

    for (let i = 0; i < 2; i++) {
      await wrapper!.get(".app-bottom-panel__collapse").trigger("click");
      await flushPromises();
      expect(panelSizes()).toEqual([768, 32]);
      await wrapper!.get(".app-bottom-panel__collapse").trigger("click");
      await flushPromises();
      expect(panelSizes()).toEqual([500, 300]);
    }
    await dragBy(100);
    expect(height.value).toBe(200);
    expect(panelSizes()).toEqual([600, 200]);
  });
});
