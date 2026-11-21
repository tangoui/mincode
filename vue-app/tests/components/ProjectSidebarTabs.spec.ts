import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { markRaw } from "vue";
import { Document } from "@element-plus/icons-vue";
import ProjectSidebarTabs, { type ProjectSidebarTabItem } from "@/components/projects/ProjectSidebarTabs.vue";

const STORAGE_KEY = "test_sidebar_hidden_tabs";
let resize: ((width: number) => void) | null = null;

class ResizeObserverStub {
  constructor(private callback: ResizeObserverCallback) {}
  observe(target: Element) {
    if (!target.classList.contains("project-sidebar-tabs")) return;
    resize = (width) =>
      this.callback([{ contentRect: { width } } as ResizeObserverEntry], this as unknown as ResizeObserver);
  }
  unobserve() {}
  disconnect() {}
}

const icon = markRaw(Document);
const items: ProjectSidebarTabItem[] = [
  { name: "resources", label: "Resources", icon, pinned: true },
  { name: "git", label: "Source", icon, badge: 3 },
  { name: "outline", label: "Outline", icon },
  { name: "tasks", label: "Tasks", icon },
  { name: "views", label: "Views", icon },
  { name: "tests", label: "Tests", icon },
];

function mountTabs(modelValue = "resources") {
  return mount(ProjectSidebarTabs, {
    props: { items, modelValue, storageKey: STORAGE_KEY, fallback: "resources" },
    attachTo: document.body,
  });
}

function inlineTabs(wrapper: ReturnType<typeof mountTabs>) {
  return wrapper.findAll('[role="tab"]').map((tab) => tab.attributes("aria-label"));
}

describe("ProjectSidebarTabs", () => {
  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", ResizeObserverStub);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    resize = null;
  });

  it("renders icon tabs with labels for assistive tech and a count badge", async () => {
    const wrapper = mountTabs("git");
    await flushPromises();

    expect(inlineTabs(wrapper)).toEqual(["Resources", "Source", "Outline", "Tasks", "Views", "Tests"]);
    expect(wrapper.find('[aria-selected="true"]').attributes("aria-label")).toBe("Source");
    expect(wrapper.find(".project-sidebar-tabs__badge").text()).toBe("3");

    await wrapper.findAll('[role="tab"]')[2].trigger("click");
    expect(wrapper.emitted("update:modelValue")).toEqual([["outline"]]);
  });

  it("moves tabs that do not fit into the overflow menu but keeps the active tab inline", async () => {
    const wrapper = mountTabs("tests");
    await flushPromises();
    // 3 tabs of 30px + gaps next to the 28px "more" button.
    resize?.(3 * 32 + 28 - 2);
    await flushPromises();

    expect(inlineTabs(wrapper)).toEqual(["Resources", "Source", "Tests"]);
  });

  it("hides persisted tabs, except the pinned and the active ones", async () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(["resources", "outline", "tasks"]));
    const wrapper = mountTabs("tasks");
    await flushPromises();

    expect(inlineTabs(wrapper)).toEqual(["Resources", "Source", "Tasks", "Views", "Tests"]);

    await wrapper.setProps({ modelValue: "git" });
    expect(inlineTabs(wrapper)).toEqual(["Resources", "Source", "Views", "Tests"]);
  });

  it("hides a tab from its context menu and falls back when it was active", async () => {
    const wrapper = mountTabs("outline");
    await flushPromises();

    await wrapper.findAll('[role="tab"]')[2].trigger("contextmenu", { clientX: 10, clientY: 10 });
    await flushPromises();
    const hide = [...document.querySelectorAll<HTMLElement>(".el-dropdown-menu__item")].find((item) =>
      item.textContent?.includes("Outline") && !item.querySelector(".el-icon")
    );
    expect(hide).toBeTruthy();
    hide!.click();
    await flushPromises();

    expect(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "[]")).toEqual(["outline"]);
    expect(wrapper.emitted("update:modelValue")).toEqual([["resources"]]);
  });
});
